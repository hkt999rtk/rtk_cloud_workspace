package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type removalRecoveryRequest struct {
	SourcePreflight, SourceKubeconfig, ClusterID string
	NodeIDs                                      []string
	Confirm                                      string
	ReviewSHA256                                 string
	Report                                       string
}

type removalRecoveryReader func(context.Context, string, string, map[string]any) ([]byte, error)

type removalRecoveryMapping struct {
	Namespace     string    `json:"namespace"`
	PVC           string    `json:"pvc"`
	PVCUID        string    `json:"pvc_uid"`
	VolumeName    string    `json:"volume_name"`
	VolumeID      string    `json:"volume_id"`
	VolumeCreated time.Time `json:"volume_created"`
	AttachEventID int       `json:"attach_event_id"`
	AttachedAt    time.Time `json:"attached_at"`
	NodeID        string    `json:"node_id"`
	SelectedNode  string    `json:"selected_node,omitempty"`
}

type removalRecoveryNode struct {
	ID            string    `json:"id"`
	Label         string    `json:"label"`
	DeleteEventID int       `json:"delete_event_id"`
	DeletedAt     time.Time `json:"deleted_at"`
}

type deploymentRemovalRecovery struct {
	Method           string                   `json:"method"`
	Scope            string                   `json:"scope"`
	ReviewSHA256     string                   `json:"review_sha256"`
	ClusterBinding   string                   `json:"cluster_binding"`
	SourcePreflight  string                   `json:"source_preflight"`
	SourceKubeconfig string                   `json:"source_kubeconfig"`
	SourceSHA256     map[string]string        `json:"source_sha256"`
	PlanSHA256       string                   `json:"plan_sha256"`
	ProofSHA256      string                   `json:"proof_sha256"`
	Generation       string                   `json:"generation"`
	QualifiedAt      time.Time                `json:"qualified_at"`
	Region           string                   `json:"region"`
	Nodes            []removalRecoveryNode    `json:"nodes"`
	Mappings         []removalRecoveryMapping `json:"mappings"`
	NativeSHA256     map[string]string        `json:"native_sha256"`
}

type removalRecoverySource struct {
	Plan           deploymentplan.Plan
	Proof          environmentPreflightProof
	KubeSHA256     string
	Region         string
	ClusterBinding string
	Hashes         map[string]string
	Mappings       []removalRecoveryMapping
}

type removalRecoveryVolume struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Region   string `json:"region"`
	Status   string `json:"status"`
	Created  string `json:"created"`
	LinodeID *int   `json:"linode_id"`
}

type removalRecoveryEntity struct {
	ID    json.RawMessage `json:"id"`
	Type  string          `json:"type"`
	Label string          `json:"label"`
	URL   string          `json:"url"`
}

type removalRecoveryEvent struct {
	ID        int                   `json:"id"`
	Created   string                `json:"created"`
	Action    string                `json:"action"`
	Status    string                `json:"status"`
	Entity    removalRecoveryEntity `json:"entity"`
	Secondary removalRecoveryEntity `json:"secondary_entity"`
}

func runDeploymentRemovalRecovery(args []string) error {
	fs := flag.NewFlagSet("reconcile-removal-volumes", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "workspace")
	environment := fs.String("environment", "", "selected environment")
	source := fs.String("source-preflight", "", "absolute original qualified artifact directory")
	kube := fs.String("source-kubeconfig", "", "absolute original selected-environment teardown archive kubeconfig")
	cluster := fs.String("cluster-id", "", "explicit original Linode LKE cluster ID")
	nodes := fs.String("node-ids", "", "explicit original node IDs, separated by commas")
	confirm := fs.String("confirm", "", "selected stack; publishes recovery metadata only")
	reviewSHA := fs.String("review-sha256", "", "exact reviewed recovery payload SHA-256; required with --confirm")
	report := fs.String("report", "", "optional absolute canonical selected deployment/reports review JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) {
		return errors.New("explicit --environment and original evidence arguments are required")
	}
	var err error
	if *workspace == "" {
		*workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return reconcileDeploymentRemovalVolumes(ctx, cfg, removalRecoveryRequest{SourcePreflight: *source, SourceKubeconfig: *kube, ClusterID: *cluster, NodeIDs: strings.Split(*nodes, ","), Confirm: *confirm, ReviewSHA256: *reviewSHA, Report: *report}, readRemovalRecoveryNative, os.Stdout)
}

var errRemovalRecoveryNativeNotFound = errors.New("native original resource is absent")

func readRemovalRecoveryNative(ctx context.Context, token, path string, filter map[string]any) ([]byte, error) {
	args := []string{"--fail-with-body", "-sS", "-X", "GET", "https://api.linode.com/v4" + path, "-H", "Authorization: Bearer " + token, "-H", "Content-Type: application/json", "--write-out", "\n%{http_code}"}
	if filter != nil {
		raw, err := json.Marshal(filter)
		if err != nil {
			return nil, errors.New("native event filter cannot be encoded")
		}
		args = append(args, "-H", "X-Filter: "+string(raw))
	}
	cmd := exec.CommandContext(ctx, "curl", args...)
	cmd.WaitDelay = time.Second
	raw, err := cmd.Output()
	index := bytes.LastIndexByte(raw, '\n')
	if index >= 0 && string(raw[index+1:]) == "404" {
		return nil, errRemovalRecoveryNativeNotFound
	}
	if err != nil || index < 0 || len(raw) > 32<<20 || string(raw[index+1:]) != "200" {
		return nil, errors.New("bounded native read-only removal evidence is unavailable")
	}
	return raw[:index], nil
}

func completeRemovalRecoveryList[T any](ctx context.Context, token, path string, filter map[string]any, read removalRecoveryReader, id func(T) int) ([]T, string, error) {
	var all []T
	seen := map[int]bool{}
	var pageHashes []string
	pages, results := -1, -1
	for page := 1; ; page++ {
		raw, err := read(ctx, token, fmt.Sprintf("%s?page_size=500&page=%d", path, page), filter)
		if err != nil || len(raw) > 32<<20 {
			return nil, "", errors.New("complete native removal evidence cannot be read")
		}
		var list struct {
			Page    int  `json:"page"`
			Pages   *int `json:"pages"`
			Results *int `json:"results"`
			Data    []T  `json:"data"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Pages == nil || list.Results == nil || list.Page != page || list.Data == nil || *list.Pages < 0 || *list.Pages > 1000 || *list.Results < 0 || *list.Results > 500000 {
			return nil, "", errors.New("native removal evidence has incomplete pagination")
		}
		if page == 1 {
			pages, results = *list.Pages, *list.Results
		} else if pages != *list.Pages || results != *list.Results {
			return nil, "", errors.New("native removal evidence changed during pagination")
		}
		if pages == 0 && (results != 0 || len(list.Data) != 0 || page != 1) || pages > 0 && page > pages {
			return nil, "", errors.New("native removal evidence pagination is inconsistent")
		}
		for _, item := range list.Data {
			key := id(item)
			if key <= 0 || seen[key] {
				return nil, "", errors.New("native removal evidence contains missing or duplicate IDs")
			}
			seen[key] = true
			all = append(all, item)
		}
		pageHashes = append(pageHashes, environmentPreflightBytesSHA256(raw))
		if pages == 0 || page == pages {
			if len(all) != results {
				return nil, "", errors.New("native removal evidence lacks reported resources")
			}
			return all, deploymentplan.Digest(pageHashes), nil
		}
	}
}

func removalRecoveryTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("native removal evidence lacks a valid UTC timestamp")
}

func removalRecoveryEntityID(entity removalRecoveryEntity, kind string, id string) bool {
	var text string
	if json.Unmarshal(entity.ID, &text) != nil {
		text = string(entity.ID)
	}
	if entity.Type != kind || text != id {
		return false
	}
	path := "/volumes/" + id
	if kind == "linode" {
		path = "/linode/instances/" + id
	}
	return entity.URL == "/v4"+path || entity.URL == path
}

func removalRecoverySourcePath(store secretStore, path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("explicit absolute canonical original source paths are required")
	}
	relative, err := filepath.Rel(store.Root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", errors.New("original removal evidence must belong to the selected canonical environment")
	}
	canonical, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || canonical != path {
		return "", errors.New("original removal evidence path or private permissions cannot be verified")
	}
	return relative, nil
}

func loadRemovalRecoverySource(cfg deploymentConfig, request removalRecoveryRequest) (removalRecoverySource, error) {
	var source removalRecoverySource
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return source, err
	}
	relative, err := filepath.Rel(filepath.Join(store.Root, "deployment", "preflight"), request.SourcePreflight)
	if err != nil || !environmentPreflightArtifactName.MatchString(filepath.ToSlash(relative)) {
		return source, errors.New("source preflight must name one original selected-environment qualified artifact")
	}
	kubeRelative, err := removalRecoverySourcePath(store, request.SourceKubeconfig)
	parts := strings.Split(filepath.ToSlash(kubeRelative), "/")
	if err != nil || len(parts) != 4 || parts[0] != "migration-backup" || !strings.HasPrefix(parts[1], "destroy-lke-") || parts[2] != "kube" || parts[3] != "kubeconfig.yaml" {
		return source, errors.New("source kubeconfig must be the original selected-environment teardown archive")
	}
	source.Hashes = map[string]string{}
	readSource := func(path string, target any) ([]byte, error) {
		rel, err := removalRecoverySourcePath(store, path)
		if err != nil {
			return nil, err
		}
		raw, err := readStorageEvidenceFile(path)
		if err != nil || target != nil && decodeEnvironmentJSON(raw, target) != nil {
			return nil, errors.New("original removal source is missing or malformed")
		}
		source.Hashes[rel] = environmentPreflightBytesSHA256(raw)
		return raw, nil
	}
	if _, err := readSource(filepath.Join(request.SourcePreflight, "plan.json"), &source.Plan); err != nil {
		return source, err
	}
	if _, err := readSource(filepath.Join(request.SourcePreflight, "proof.json"), &source.Proof); err != nil {
		return source, err
	}
	var release environmentRelease
	var bindings environmentIdentityBindings
	if _, err := readSource(filepath.Join(request.SourcePreflight, "release.json"), &release); err != nil {
		return source, err
	}
	if _, err := readSource(filepath.Join(request.SourcePreflight, "bindings.json"), &bindings); err != nil {
		return source, err
	}
	p, proof := &source.Plan, source.Proof
	if p.Version != deploymentplan.Version || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || proof.Schema != environmentPreflightProofSchema || proof.Environment != p.Environment || proof.Stack != p.Stack || proof.Operation != p.Operation || proof.PlanSHA256 != p.SHA256() || filepath.Base(filepath.Dir(request.SourcePreflight)) != proof.PlanSHA256 || proof.InputsSHA256 != deploymentplan.Digest(p.Inputs) || proof.ConfigurationSHA256 != p.ConfigurationSHA256 || proof.ReleaseSHA256 != p.ReleaseSHA256 || proof.BindingsSHA256 != p.BindingsSHA256 || deploymentplan.Digest(release) != proof.ReleaseSHA256 || deploymentplan.Digest(bindings) != proof.BindingsSHA256 || !environmentPreflightDigest.MatchString(proof.Generation) || proof.QualifiedAt.IsZero() || proof.QualifiedAt.After(time.Now()) || !proof.ValidUntil.After(proof.QualifiedAt) || proof.PKICheckedAt.IsZero() || proof.PKICheckedAt.After(proof.QualifiedAt) || proof.PKIVerifiedUntil.Before(proof.QualifiedAt) {
		return source, errors.New("historical qualified source does not match the original environment, exact plan and complete input hashes")
	}
	if proof.CheckerVersion != environmentPreflightCheckerVersion || !environmentPreflightDigest.MatchString(proof.CheckerSHA256) || !environmentPreflightDigest.MatchString(proof.MaintainedConfigSHA256) || !environmentPreflightDigest.MatchString(p.ConfigurationSHA256) || !environmentPreflightDigest.MatchString(p.ReleaseSHA256) || !environmentPreflightDigest.MatchString(p.BindingsSHA256) {
		return source, errors.New("historical qualification checker and exact input hash bindings are incomplete")
	}
	if err := validateEnvironmentPreflightReleaseProofs(proof, release); err != nil {
		return source, errors.New("historical release publication evidence is incomplete")
	}
	ids := deploymentplan.RequiredGateIDs(cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] == "production")
	if p.Operation == "upgrade" {
		ids = ids[:6]
	} else if p.Operation != "create" {
		return source, errors.New("original source is not a complete environment qualification")
	}
	if proof.Qualification.Decision != "GO" || proof.Qualification.PlanSHA256 != p.SHA256() || len(proof.Qualification.Gates) != len(ids) {
		return source, errors.New("original qualification did not pass every required environment gate")
	}
	for index, gate := range proof.Qualification.Gates {
		if gate.ID != ids[index] || gate.Status != "PASS" || !gate.Required || !environmentPreflightDigest.MatchString(gate.EvidenceSHA256) || gate.Cause != "" || gate.Next != "" || gate.Impact != "" || gate.Recheck != "" {
			return source, errors.New("original qualification contains incomplete or failed gate evidence")
		}
	}
	kubeRaw, err := readSource(request.SourceKubeconfig, nil)
	if err != nil {
		return source, err
	}
	backend, err := newEnvironmentKubernetesBackendAt(string(kubeRaw), cfg.Environment, proof.QualifiedAt)
	if err != nil {
		return source, errors.New("original archived Kubernetes identity cannot be validated")
	}
	cluster, identity, err := environmentPreflightKubernetesIdentity(backend.KubeconfigContent)
	if err != nil || cluster != proof.ClusterSHA256 || identity != proof.KubernetesIdentitySHA256 {
		return source, errors.New("archived Kubernetes cluster and authentication identity differ from the original qualification")
	}
	source.KubeSHA256 = environmentPreflightBytesSHA256(kubeRaw)
	source.Region = cfg.AdapterResolved["LKE_REGION"]
	if source.Region == "" {
		return source, errors.New("selected maintained Linode region is required")
	}
	statePath := filepath.Join(store.Root, "migration-backup", parts[1], "deployment", "adapters", "lke", "state.env")
	if _, err := os.Lstat(statePath); err == nil {
		raw, err := readSource(statePath, nil)
		if err != nil {
			return source, err
		}
		state, err := parseStrictEnv(raw, "original archived provider state")
		if err != nil || validateLKEProviderState(cfg.Environment, p.Stack, state) != nil || state["LKE_CLUSTER_ID"] != request.ClusterID || state["LKE_CLUSTER_REGION"] != source.Region {
			return source, errors.New("original archived provider state differs from the reviewed cluster and region")
		}
		source.ClusterBinding = "archived-provider-state"
	} else if !errors.Is(err, os.ErrNotExist) {
		return source, errors.New("original archived provider state cannot be inspected")
	} else {
		source.ClusterBinding = "explicit-reviewed-node-set"
	}
	seen := map[string]bool{}
	uidPattern := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	for _, resource := range p.Resources {
		if resource.Desired["kind"] != "PersistentVolumeClaim" {
			continue
		}
		base := resource.Baseline
		meta, spec := deploymentplan.Map(base["metadata"]), deploymentplan.Map(base["spec"])
		namespace, name := deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"])
		volumeName := deploymentplan.String(spec["volumeName"])
		uid := resource.UID
		if base["kind"] != "PersistentVolumeClaim" || !strings.HasPrefix(namespace, p.Stack+"-") || name == "" || !uidPattern.MatchString(uid) || deploymentplan.String(meta["uid"]) != "" && deploymentplan.String(meta["uid"]) != uid || resource.ID != deploymentplan.ID(base) || seen[volumeName] || volumeName != "pvc-"+strings.ReplaceAll(uid, "-", "")[:16] {
			return source, errors.New("original qualified PVC inventory lacks exact UID, namespace and volume bindings")
		}
		seen[volumeName] = true
		source.Mappings = append(source.Mappings, removalRecoveryMapping{Namespace: namespace, PVC: name, PVCUID: uid, VolumeName: volumeName, SelectedNode: deploymentplan.String(deploymentplan.Map(meta["annotations"])["volume.kubernetes.io/selected-node"])})
	}
	if len(source.Mappings) == 0 {
		return source, errors.New("original qualification has no complete observed PVC inventory")
	}
	sort.Slice(source.Mappings, func(i, j int) bool {
		return source.Mappings[i].Namespace+"/"+source.Mappings[i].PVC < source.Mappings[j].Namespace+"/"+source.Mappings[j].PVC
	})
	return source, nil
}

func removalRecoveryNodeIDs(request removalRecoveryRequest) (map[string]bool, error) {
	if _, err := positiveIntValue("cluster_id", request.ClusterID); err != nil || len(request.NodeIDs) == 0 {
		return nil, errors.New("explicit original cluster and node IDs are required")
	}
	nodes := map[string]bool{}
	for _, id := range request.NodeIDs {
		if _, err := positiveIntValue("node_id", id); err != nil || nodes[id] {
			return nil, errors.New("original node IDs must be distinct positive numeric IDs")
		}
		nodes[id] = true
	}
	return nodes, nil
}

func checkRemovalRecoveryGeneration(cfg deploymentConfig, request removalRecoveryRequest, source removalRecoverySource) error {
	path, err := privateDeploymentStatePath(cfg.Environment, "kube/kubeconfig.yaml")
	if err != nil {
		return err
	}
	if raw, err := readStorageEvidenceFile(path); err == nil {
		if environmentPreflightBytesSHA256(raw) != source.KubeSHA256 {
			return errors.New("another canonical Kubernetes identity generation exists; removal recovery is blocked")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("current canonical Kubernetes identity cannot be inspected")
	}
	state, err := readLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	if state["LKE_CLUSTER_ID"] != "" && state["LKE_CLUSTER_ID"] != request.ClusterID {
		return errors.New("another recorded cluster generation exists; removal recovery is blocked")
	}
	return nil
}

func reviewRemovalRecoveryNative(ctx context.Context, cfg deploymentConfig, request removalRecoveryRequest, source removalRecoverySource, token string, read removalRecoveryReader, allowAbsent bool) (deploymentRemovalRecovery, error) {
	recovery := deploymentRemovalRecovery{Method: "linode-event-recovery", Scope: "qualified-original-PVC-inventory", ClusterBinding: source.ClusterBinding, SourcePreflight: request.SourcePreflight, SourceKubeconfig: request.SourceKubeconfig, SourceSHA256: source.Hashes, PlanSHA256: source.Plan.SHA256(), ProofSHA256: source.Proof.SHA256(), Generation: source.Proof.Generation, QualifiedAt: source.Proof.QualifiedAt, Region: source.Region, NativeSHA256: map[string]string{}}
	nodes, err := removalRecoveryNodeIDs(request)
	if err != nil {
		return recovery, err
	}
	if err := checkRemovalRecoveryGeneration(cfg, request, source); err != nil {
		return recovery, err
	}
	type resource struct {
		ID    int      `json:"id"`
		Label string   `json:"label"`
		Tags  []string `json:"tags"`
	}
	clusters, hash, err := completeRemovalRecoveryList(ctx, token, "/lke/clusters", nil, read, func(item resource) int { return item.ID })
	if err != nil {
		return recovery, err
	}
	recovery.NativeSHA256["lke/clusters"] = hash
	match := linodeDestroyMatcher(cfg.Values, cfg.Values["CLOUD_STACK_NAME"], "")
	for _, cluster := range clusters {
		if strconv.Itoa(cluster.ID) == request.ClusterID || match(cluster.Label, cluster.Tags) {
			return recovery, errors.New("selected original or replacement LKE cluster still exists; recovery never creates or downloads a kubeconfig")
		}
	}
	instances, hash, err := completeRemovalRecoveryList(ctx, token, "/linode/instances", nil, read, func(item resource) int { return item.ID })
	if err != nil {
		return recovery, err
	}
	recovery.NativeSHA256["linode/instances"] = hash
	for _, instance := range instances {
		if nodes[strconv.Itoa(instance.ID)] {
			return recovery, errors.New("an explicitly selected original node still exists")
		}
	}
	for _, id := range request.NodeIDs {
		numeric, _ := strconv.Atoi(id)
		events, hash, err := completeRemovalRecoveryList(ctx, token, "/account/events", map[string]any{"entity.id": numeric, "entity.type": "linode"}, read, func(event removalRecoveryEvent) int { return event.ID })
		if err != nil {
			return recovery, err
		}
		recovery.NativeSHA256["linode-events/"+id] = hash
		var deleted removalRecoveryNode
		for _, event := range events {
			if !removalRecoveryEntityID(event.Entity, "linode", id) {
				return recovery, errors.New("native node events do not match the exact entity ID and URI filter")
			}
			if event.Action != "linode_delete" || event.Status != "finished" {
				continue
			}
			created, err := removalRecoveryTime(event.Created)
			if err != nil || created.After(time.Now()) || !created.After(source.Proof.QualifiedAt) || !strings.HasPrefix(event.Entity.Label, "lke"+request.ClusterID+"-") {
				return recovery, errors.New("original node deletion lacks the selected cluster label and post-qualification completion evidence")
			}
			if deleted.ID == "" || created.After(deleted.DeletedAt) {
				deleted = removalRecoveryNode{ID: id, Label: event.Entity.Label, DeleteEventID: event.ID, DeletedAt: created}
			}
		}
		if deleted.ID == "" {
			return recovery, errors.New("original node lacks a completed native deletion event")
		}
		recovery.Nodes = append(recovery.Nodes, deleted)
	}
	sort.Slice(recovery.Nodes, func(i, j int) bool { return recovery.Nodes[i].ID < recovery.Nodes[j].ID })
	volumes, hash, err := completeRemovalRecoveryList(ctx, token, "/volumes", nil, read, func(item removalRecoveryVolume) int { return item.ID })
	if err != nil {
		return recovery, err
	}
	recovery.NativeSHA256["volumes"] = hash
	byLabel := map[string][]removalRecoveryVolume{}
	byID := map[string]removalRecoveryVolume{}
	for _, volume := range volumes {
		byLabel[volume.Label] = append(byLabel[volume.Label], volume)
		byID[strconv.Itoa(volume.ID)] = volume
	}
	for _, mapping := range source.Mappings {
		matched := byLabel[mapping.VolumeName]
		if allowAbsent {
			raw, err := read(ctx, token, "/volumes/"+mapping.VolumeID, nil)
			if errors.Is(err, errRemovalRecoveryNativeNotFound) {
				continue
			}
			var original removalRecoveryVolume
			if err != nil || json.Unmarshal(raw, &original) != nil || strconv.Itoa(original.ID) != mapping.VolumeID {
				return recovery, errors.New("original numeric volume identity or absence cannot be confirmed by its native endpoint")
			}
			listed, exists := byID[mapping.VolumeID]
			if !exists || deploymentplan.Digest(original) != deploymentplan.Digest(listed) || original.Label != mapping.VolumeName || len(matched) != 1 || matched[0].ID != original.ID {
				return recovery, errors.New("an original recovered numeric volume ID changed its label or identity binding")
			}
		}
		if len(matched) != 1 {
			return recovery, errors.New("an original PVC has no unique exact native volume label binding")
		}
		volume := matched[0]
		created, err := removalRecoveryTime(volume.Created)
		if err != nil || volume.Region != source.Region || volume.Status != "active" || volume.LinodeID != nil || !created.Before(source.Proof.QualifiedAt) || allowAbsent && !created.Equal(mapping.VolumeCreated) {
			return recovery, errors.New("an original volume is attached, not active, foreign-region or newer than qualification")
		}
		id := strconv.Itoa(volume.ID)
		events, hash, err := completeRemovalRecoveryList(ctx, token, "/account/events", map[string]any{"entity.id": volume.ID, "entity.type": "volume"}, read, func(event removalRecoveryEvent) int { return event.ID })
		if err != nil {
			return recovery, err
		}
		recovery.NativeSHA256["volume-events/"+id] = hash
		var attached removalRecoveryEvent
		var attachedAt time.Time
		for _, event := range events {
			if !removalRecoveryEntityID(event.Entity, "volume", id) {
				return recovery, errors.New("native volume events do not match the exact entity ID and URI filter")
			}
			if event.Action != "volume_attach" || event.Status != "finished" {
				continue
			}
			at, err := removalRecoveryTime(event.Created)
			if err != nil || at.Before(created) {
				return recovery, errors.New("native volume attachment timestamp predates the current volume identity")
			}
			if attachedAt.IsZero() || at.After(attachedAt) {
				attached, attachedAt = event, at
			}
		}
		var nodeID string
		for id := range nodes {
			if removalRecoveryEntityID(attached.Secondary, "linode", id) {
				nodeID = id
			}
		}
		if attached.ID == 0 || nodeID == "" || attachedAt.After(source.Proof.QualifiedAt) || attached.Entity.Label != mapping.VolumeName || !strings.HasPrefix(attached.Secondary.Label, "lke"+request.ClusterID+"-") || mapping.SelectedNode != "" && mapping.SelectedNode != attached.Secondary.Label {
			return recovery, errors.New("latest completed attachment lacks original numeric volume-to-node lineage before qualification")
		}
		mapping.VolumeID, mapping.VolumeCreated = id, created
		mapping.AttachEventID, mapping.AttachedAt, mapping.NodeID = attached.ID, attachedAt, nodeID
		recovery.Mappings = append(recovery.Mappings, mapping)
	}
	return recovery, nil
}

func validateRecoveredRemovalProof(cfg deploymentConfig, proof deploymentRemovalVolumes) error {
	recovery := proof.Recovery
	if proof.Schema != 2 || recovery == nil || recovery.Method != "linode-event-recovery" || recovery.Scope != "qualified-original-PVC-inventory" || !keySet("archived-provider-state", "explicit-reviewed-node-set")[recovery.ClusterBinding] || !environmentPreflightDigest.MatchString(recovery.ReviewSHA256) || removalRecoveryReviewSHA(*recovery) != recovery.ReviewSHA256 || len(recovery.Mappings) != len(proof.VolumeIDs) || len(recovery.Mappings) == 0 || len(recovery.Nodes) == 0 {
		return errors.New("declared recovery scope, exact reviewed payload or original mappings are incomplete")
	}
	request := removalRecoveryRequest{SourcePreflight: recovery.SourcePreflight, SourceKubeconfig: recovery.SourceKubeconfig, ClusterID: proof.ClusterID}
	nodes := map[string]removalRecoveryNode{}
	for _, node := range recovery.Nodes {
		if _, err := positiveIntValue("node_id", node.ID); err != nil || nodes[node.ID].ID != "" || node.DeleteEventID <= 0 || node.DeletedAt.After(time.Now()) || !node.DeletedAt.After(recovery.QualifiedAt) || !strings.HasPrefix(node.Label, "lke"+proof.ClusterID+"-") {
			return errors.New("declared recovery lacks complete original node deletion lineage")
		}
		nodes[node.ID] = node
		request.NodeIDs = append(request.NodeIDs, node.ID)
	}
	source, err := loadRemovalRecoverySource(cfg, request)
	if err != nil {
		return err
	}
	if source.KubeSHA256 != proof.KubeSHA256 || source.ClusterBinding != recovery.ClusterBinding || source.Region != recovery.Region || source.Plan.SHA256() != recovery.PlanSHA256 || source.Proof.SHA256() != recovery.ProofSHA256 || source.Proof.Generation != recovery.Generation || !source.Proof.QualifiedAt.Equal(recovery.QualifiedAt) || deploymentplan.Digest(source.Hashes) != deploymentplan.Digest(recovery.SourceSHA256) || len(source.Mappings) != len(recovery.Mappings) {
		return errors.New("declared recovery differs from its original qualified sources and generation")
	}
	ids := map[string]bool{}
	baselines := map[string]removalRecoveryMapping{}
	for _, mapping := range source.Mappings {
		baselines[mapping.Namespace+"/"+mapping.PVC] = mapping
	}
	for _, mapping := range recovery.Mappings {
		original, found := baselines[mapping.Namespace+"/"+mapping.PVC]
		if _, err := positiveIntValue("volume_id", mapping.VolumeID); err != nil || ids[mapping.VolumeID] || !found || original.PVCUID != mapping.PVCUID || original.VolumeName != mapping.VolumeName || original.SelectedNode != mapping.SelectedNode || nodes[mapping.NodeID].ID == "" || mapping.SelectedNode != "" && mapping.SelectedNode != nodes[mapping.NodeID].Label || mapping.AttachEventID <= 0 || mapping.VolumeCreated.IsZero() || !mapping.VolumeCreated.Before(recovery.QualifiedAt) || mapping.AttachedAt.Before(mapping.VolumeCreated) || mapping.AttachedAt.After(recovery.QualifiedAt) {
			return errors.New("declared recovery has foreign, duplicate or incomplete original volume lineage")
		}
		ids[mapping.VolumeID] = true
		delete(baselines, mapping.Namespace+"/"+mapping.PVC)
	}
	if len(baselines) != 0 {
		return errors.New("declared recovery omits original qualified PVC bindings")
	}
	for _, id := range proof.VolumeIDs {
		if !ids[id] {
			return errors.New("declared recovery volume IDs differ from the reviewed original mappings")
		}
		delete(ids, id)
	}
	if len(ids) != 0 {
		return errors.New("declared recovery repeats or omits original numeric volume IDs")
	}
	return nil
}

func validateRecoveredRemovalTarget(ctx context.Context, cfg deploymentConfig, proof deploymentRemovalVolumes, read removalRecoveryReader) error {
	if err := validateRecoveredRemovalProof(cfg, proof); err != nil {
		return err
	}
	recovery := proof.Recovery
	request := removalRecoveryRequest{SourcePreflight: recovery.SourcePreflight, SourceKubeconfig: recovery.SourceKubeconfig, ClusterID: proof.ClusterID}
	for _, node := range recovery.Nodes {
		request.NodeIDs = append(request.NodeIDs, node.ID)
	}
	source, err := loadRemovalRecoverySource(cfg, request)
	if err != nil {
		return err
	}
	source.Mappings = append([]removalRecoveryMapping(nil), recovery.Mappings...)
	tokenPath, err := privateDeploymentStatePath(cfg.Environment, "operator/env/LINODE_TOKEN")
	if err != nil {
		return err
	}
	raw, err := readStorageEvidenceFile(tokenPath)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return errors.New("selected canonical Linode credentials are required to consume recovery")
	}
	fresh, err := reviewRemovalRecoveryNative(ctx, cfg, request, source, strings.TrimSpace(string(raw)), read, true)
	if err != nil {
		return err
	}
	expected := map[string]removalRecoveryMapping{}
	for _, mapping := range recovery.Mappings {
		expected[mapping.VolumeID] = mapping
	}
	for _, mapping := range fresh.Mappings {
		if deploymentplan.Digest(mapping) != deploymentplan.Digest(expected[mapping.VolumeID]) {
			return errors.New("original recovered volume lineage changed before removal")
		}
	}
	if deploymentplan.Digest(fresh.Nodes) != deploymentplan.Digest(recovery.Nodes) {
		return errors.New("original node deletion lineage changed before removal")
	}
	return nil
}

func reconcileDeploymentRemovalVolumes(ctx context.Context, cfg deploymentConfig, request removalRecoveryRequest, read removalRecoveryReader, out io.Writer) error {
	if cfg.Adapter != "lke" || read == nil || request.Confirm != "" && request.Confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return errors.New("selected LKE environment and exact --confirm stack are required")
	}
	if request.Confirm != "" && !environmentPreflightDigest.MatchString(request.ReviewSHA256) || request.Confirm == "" && request.ReviewSHA256 != "" {
		return errors.New("publication requires --confirm STACK and the exact --review-sha256 from read-only review")
	}
	if request.Report != "" {
		if err := validateEnvironmentDeploymentReportPath(cfg.Environment, request.Report); err != nil {
			return err
		}
	}
	if _, err := removalRecoveryNodeIDs(request); err != nil {
		return err
	}
	source, err := loadRemovalRecoverySource(cfg, request)
	if err != nil {
		return err
	}
	tokenPath, err := privateDeploymentStatePath(cfg.Environment, "operator/env/LINODE_TOKEN")
	if err != nil {
		return err
	}
	tokenRaw, err := readStorageEvidenceFile(tokenPath)
	token := strings.TrimSpace(string(tokenRaw))
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("selected canonical Linode credentials are required for read-only recovery evidence")
	}
	recovery, err := reviewRemovalRecoveryNative(ctx, cfg, request, source, token, read, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[REVIEW] environment=%s original_cluster=%s qualified_original_pvc_volumes=%d method=linode-event-recovery\n", cfg.Environment, request.ClusterID, len(recovery.Mappings))
	reviewSHA := removalRecoveryReviewSHA(recovery)
	fmt.Fprintf(out, "review-sha256: %s\ncluster-binding: %s\n", reviewSHA, recovery.ClusterBinding)
	if request.Report != "" {
		review := struct {
			Environment  string                    `json:"environment"`
			Stack        string                    `json:"stack"`
			ReviewSHA256 string                    `json:"review_sha256"`
			Recovery     deploymentRemovalRecovery `json:"recovery"`
		}{cfg.Environment, cfg.Values["CLOUD_STACK_NAME"], reviewSHA, recovery}
		raw, err := json.MarshalIndent(review, "", "  ")
		if err != nil {
			return err
		}
		store, err := newSecretStore("", cfg.Environment)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(store.Root, request.Report)
		if err != nil || writePrivateDeploymentState(cfg.Environment, relative, append(raw, '\n')) != nil {
			return errors.New("private recovery review report could not be saved")
		}
	}
	ids := make([]string, 0, len(recovery.Mappings))
	for _, mapping := range recovery.Mappings {
		ids = append(ids, mapping.VolumeID)
		fmt.Fprintf(out, "  volume=%s namespace=%s pvc=%s original_pv=%s attach_event=%d original_node=%s\n", mapping.VolumeID, mapping.Namespace, mapping.PVC, mapping.VolumeName, mapping.AttachEventID, mapping.NodeID)
	}
	fmt.Fprintln(out, "  Scope: the original qualified PVC inventory only; other volumes remain outside this recovery. Historical preflight expiry is not extended or reused for deployment admission.")
	if request.Confirm == "" {
		fmt.Fprintln(out, "Read-only review; no recovery proof was published and no cloud resource was deleted.")
		return nil
	}
	if request.ReviewSHA256 != reviewSHA {
		return errors.New("reviewed recovery payload differs from --review-sha256; no metadata was published")
	}
	checked, err := loadRemovalRecoverySource(cfg, request)
	currentToken, tokenErr := readStorageEvidenceFile(tokenPath)
	if err != nil || tokenErr != nil || !bytes.Equal(tokenRaw, currentToken) || deploymentplan.Digest(checked.Hashes) != deploymentplan.Digest(source.Hashes) {
		return errors.New("original recovery source bytes or selected credentials changed during review")
	}
	fresh, err := reviewRemovalRecoveryNative(ctx, cfg, request, checked, token, read, false)
	if err != nil || removalRecoveryReviewSHA(fresh) != reviewSHA {
		return errors.New("native recovery evidence changed before metadata publication")
	}
	recovery.NativeSHA256 = fresh.NativeSHA256
	recovery.ReviewSHA256 = reviewSHA
	sort.Strings(ids)
	proof := deploymentRemovalVolumes{Schema: 2, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ClusterID: request.ClusterID, KubeSHA256: source.KubeSHA256, VolumeIDs: ids, Recovery: &recovery}
	raw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path, err := privateDeploymentStatePath(cfg.Environment, "deployment/remove-volume-ownership.json")
	if err != nil {
		return err
	}
	if current, err := readStorageEvidenceFile(path); err == nil {
		if !bytes.Equal(current, raw) {
			return errors.New("an earlier volume ownership proof already exists and must never be overwritten")
		}
		fmt.Fprintln(out, "Recovery proof already matches; no metadata was replaced.")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("existing original removal evidence cannot be inspected")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := publishOriginalStorageEvidence(path, raw); err != nil {
		return errors.New("exclusive recovery publication failed; existing original evidence was retained")
	}
	fmt.Fprintln(out, "[PUBLISHED] declared Linode event recovery metadata; no cloud resource was deleted. Ordinary remove can consume this exact original volume inventory.")
	return nil
}

func removalRecoveryReviewSHA(recovery deploymentRemovalRecovery) string {
	// Complete inventories verify absence and uniqueness. Unrelated resources and
	// native acknowledgement fields remain outside this fixed approval scope.
	recovery.NativeSHA256 = nil
	recovery.ReviewSHA256 = ""
	return deploymentplan.Digest(recovery)
}
