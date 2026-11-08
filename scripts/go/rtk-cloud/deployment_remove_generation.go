package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const generationRemovalIntent = "deployment/remove-original-generation.json"

type originalGenerationRemoval struct {
	Schema           int      `json:"schema"`
	Environment      string   `json:"environment"`
	ClusterID        string   `json:"cluster_id"`
	LedgerSHA256     string   `json:"ledger_sha256"`
	KubeSHA256       string   `json:"kubeconfig_sha256"`
	VolumeIDs        []string `json:"volume_ids"`
	WorkerIDs        []string `json:"worker_ids"`
	KubernetesPurged bool     `json:"kubernetes_purged"`
}

type generationProviderTarget struct {
	environmentCreateProviderReceipt
	path string
}

var generationTurnKey = regexp.MustCompile(`^turn-0[1-5]$`)

func runRemoveOriginalGeneration(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	env := fs.String("environment", "", "selected dev or staging environment")
	confirm := fs.String("confirm", "", "exact environment stack")
	clusterID := fs.String("original-cluster-id", "", "original numeric LKE cluster ID")
	workspace := fs.String("workspace", "", "workspace containing maintained environment settings")
	plan := fs.Bool("plan", false, "verify and list original targets without changing them")
	timeout := fs.Duration("timeout", 30*time.Minute, "native provider operation timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*env != "dev" && *env != "staging") || *confirm != "video-cloud-"+*env || *timeout <= 0 {
		return errors.New("select dev or staging, confirm its exact stack and specify a positive timeout")
	}
	if _, err := positiveIntValue("original-cluster-id", *clusterID); err != nil {
		return err
	}
	if *workspace == "" {
		root, err := workspaceRoot()
		if err != nil {
			return err
		}
		*workspace = root
	}
	cfg, err := resolveDeploymentConfig(*workspace, *env, "")
	if err != nil {
		return err
	}
	store, err := newSecretStore("", *env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return removeOriginalGeneration(ctx, cfg, store, *clusterID, *plan)
}

func loadGenerationRemovalTargets(cfg deploymentConfig, store secretStore, clusterID string) ([]generationProviderTarget, string, string, error) {
	fail := func(reason string) ([]generationProviderTarget, string, string, error) {
		return nil, "", "", fmt.Errorf("original provider generation cannot be verified: %s", reason)
	}
	var active managedTransportVolumeLedgerActive
	if readEnvironmentPreflightJSONWithCanonical(filepath.Join(store.Root, "deployment/resources/active.json"), &active, false) != nil ||
		active.Environment != cfg.Environment || active.Stack != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(active.Plan) {
		return fail("active resource ledger pointer is invalid")
	}
	e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: active.Environment, Stack: active.Stack, PlanSHA256: active.Plan}}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil || validateManagedTransportVolumeLedger(ledger, active) != nil || ledger.Provider["lke"].NativeID != clusterID {
		return fail("active resource ledger differs from its pointer or the original cluster ID")
	}
	root := filepath.Join(store.Root, "deployment/create/executions", active.Plan)
	if err := validateEnvironmentPreflightDirectories(store, root); err != nil {
		return fail("original create execution directory is unavailable")
	}
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) != 1 || !strings.HasPrefix(dirs[0].Name(), "execution-") {
		return fail("original create execution attempt is missing or ambiguous")
	}
	dir := filepath.Join(root, dirs[0].Name())
	var journal environmentCreateJournal
	if validateEnvironmentPreflightDirectories(store, dir) != nil ||
		readEnvironmentPreflightJSON(filepath.Join(dir, "journal.json"), &journal) != nil ||
		journal.Schema != environmentCreateJournalSchema || journal.Environment != active.Environment || journal.Stack != active.Stack ||
		journal.PlanSHA256 != active.Plan || !environmentPreflightDigest.MatchString(journal.ArtifactSHA256) ||
		journal.AdmittedAt.IsZero() || journal.CompletedAt.IsZero() || journal.CompletedAt.Before(journal.AdmittedAt) || journal.CompletedAt.After(time.Now().UTC()) {
		return fail("original create journal is invalid or incomplete")
	}
	entries := map[string]environmentCreateJournalEntry{}
	for _, entry := range journal.Entries {
		if entries[entry.Key].Key != "" || entry.Key == "" || entry.State != "completed" || len(entry.Receipt) == 0 ||
			entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(journal.CompletedAt) {
			return fail("original create journal contains a missing, duplicate or unfinished entry")
		}
		entries[entry.Key] = entry
	}
	var access struct {
		ClusterID string `json:"cluster_id"`
		KubeSHA   string `json:"kubeconfig_sha256"`
	}
	if entries["provider-kube-access"].Phase != "provider" ||
		json.Unmarshal(entries["provider-kube-access"].Receipt, &access) != nil || access.ClusterID != clusterID ||
		!environmentPreflightDigest.MatchString(access.KubeSHA) {
		return fail("original Kubernetes access receipt differs from the selected cluster")
	}
	values := environmentCreateProviderValues(cfg)
	targets := make([]generationProviderTarget, 0, len(ledger.Provider))
	paths := map[string]bool{}
	for key, receipt := range ledger.Provider {
		entry := entries[key]
		var fromJournal environmentCreateProviderReceipt
		if receipt.Key != key || entry.Phase != "provider" || decodeEnvironmentJSON(entry.Receipt, &fromJournal) != nil ||
			fromJournal.Key != key || fromJournal.NativeID != receipt.NativeID || fromJournal.Name != receipt.Name ||
			fromJournal.Kind != receipt.Kind || fromJournal.Region != receipt.Region {
			return fail("provider receipt differs from the completed journal for " + key)
		}
		var path string
		switch key {
		case "lke":
			if receipt.Kind != "lke" || receipt.Name != active.Stack+"-lke" {
				return fail("original LKE receipt has an unexpected kind or label")
			}
			path = "/lke/clusters/" + clusterID
		case "bucket-runtime-media", "bucket-ota-firmware":
			expected := cfg.Storage.RuntimeMedia
			if key == "bucket-ota-firmware" {
				expected = cfg.Storage.OTAFirmware
			}
			if receipt.Kind != "bucket" || receipt.Name != expected.Bucket || receipt.Region != expected.Region ||
				receipt.NativeID != receipt.Region+"/"+receipt.Name {
				return fail("bucket receipt differs from the maintained storage binding for " + key)
			}
			if err := validateDestroyRuntimeBucket(values, receipt.Name, receipt.Region); err != nil {
				return nil, "", "", err
			}
			path = "/object-storage/buckets/" + url.PathEscape(receipt.Region) + "/" + url.PathEscape(receipt.Name)
		default:
			if receipt.Kind != "instance" || key != "edge" && !generationTurnKey.MatchString(key) || !strings.HasPrefix(receipt.Name, active.Stack+"-") {
				return fail("unsupported original instance receipt for " + key)
			}
			if _, err := positiveIntValue("instance ID", receipt.NativeID); err != nil {
				return fail("original instance ID is invalid for " + key)
			}
			path = "/linode/instances/" + receipt.NativeID
		}
		if paths[path] || receipt.Region == "" {
			return fail("original provider resource has a duplicate path or empty region")
		}
		paths[path] = true
		targets = append(targets, generationProviderTarget{receipt, path})
	}
	for _, key := range []string{"lke", "edge", "turn-01", "bucket-runtime-media", "bucket-ota-firmware"} {
		if ledger.Provider[key].Key != key {
			return fail("required original receipt is missing for " + key)
		}
	}
	slices.SortFunc(targets, func(a, b generationProviderTarget) int {
		if a.Kind == "lke" && b.Kind != "lke" {
			return -1
		}
		if b.Kind == "lke" && a.Kind != "lke" {
			return 1
		}
		return strings.Compare(a.path, b.path)
	})
	return targets, active.LedgerSHA, access.KubeSHA, nil
}

func generationProviderAbsent(err error) bool {
	var native *environmentCreateProviderHTTPError
	return errors.As(err, &native) && native.Status == http.StatusNotFound
}

func inspectGenerationProviderTarget(ctx context.Context, token, stack string, t generationProviderTarget) (bool, error) {
	raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, t.path, nil)
	if generationProviderAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var actual struct {
		ID      int      `json:"id"`
		Label   string   `json:"label"`
		Region  string   `json:"region"`
		Cluster string   `json:"cluster"`
		Tags    []string `json:"tags"`
		Objects *int64   `json:"objects"`
	}
	if json.Unmarshal(raw, &actual) != nil || actual.Label != t.Name || firstNonEmpty(actual.Region, actual.Cluster) != t.Region {
		return false, fmt.Errorf("native identity differs from original receipt at %s", t.path)
	}
	if t.Kind == "bucket" {
		if actual.Objects == nil || *actual.Objects != 0 {
			return false, fmt.Errorf("original bucket has objects or emptiness is unknown at %s", t.path)
		}
	} else if strconv.Itoa(actual.ID) != t.NativeID || !slices.Contains(actual.Tags, stack) {
		return false, fmt.Errorf("native ID or stack ownership differs at %s", t.path)
	}
	return true, nil
}

func readOriginalClusterWorkers(ctx context.Context, token, cluster string) ([]string, error) {
	pools, err := environmentCreateProviderList(ctx, token, "/lke/clusters/"+cluster+"/pools")
	if err != nil {
		return nil, err
	}
	workers := []string{}
	for _, raw := range pools {
		var pool struct {
			Nodes []struct {
				ID int `json:"instance_id"`
			} `json:"nodes"`
		}
		if json.Unmarshal(raw, &pool) != nil || pool.Nodes == nil {
			return nil, errors.New("original LKE node membership is unavailable")
		}
		for _, n := range pool.Nodes {
			id := strconv.Itoa(n.ID)
			if n.ID <= 0 || slices.Contains(workers, id) {
				return nil, errors.New("original LKE node membership is incomplete or ambiguous")
			}
			workers = append(workers, id)
		}
	}
	slices.Sort(workers)
	return workers, nil
}

func validateGenerationRemovalInventory(ctx context.Context, cfg deploymentConfig, token string, targets []generationProviderTarget, workers []string) error {
	allowed := map[string]bool{}
	workerPrefix := ""
	for _, target := range targets {
		allowed[target.path] = true
		if target.Kind == "lke" {
			workerPrefix = "lke" + target.NativeID + "-"
		}
	}
	for _, id := range workers {
		allowed["/linode/instances/"+id] = true
	}
	values := environmentCreateProviderValues(cfg)
	match := linodeDestroyMatcher(values, values["CLOUD_STACK_NAME"], "")
	for _, group := range []string{"/lke/clusters", "/linode/instances", "/networking/firewalls", "/vpcs", "/nodebalancers", "/object-storage/buckets"} {
		items, err := environmentCreateProviderList(ctx, token, group)
		if err != nil {
			return err
		}
		for _, raw := range items {
			var item struct {
				ID                     int
				Label, Region, Cluster string
				Tags                   []string
			}
			if json.Unmarshal(raw, &item) != nil {
				return errors.New("native removal inventory is invalid")
			}
			path := group + "/" + strconv.Itoa(item.ID)
			candidate := match(item.Label, item.Tags) || workerPrefix != "" && strings.HasPrefix(item.Label, workerPrefix)
			if group == "/object-storage/buckets" {
				path = group + "/" + url.PathEscape(firstNonEmpty(item.Region, item.Cluster)) + "/" + url.PathEscape(item.Label)
				candidate = candidate || item.Label == cfg.Storage.RuntimeMedia.Bucket || item.Label == cfg.Storage.OTAFirmware.Bucket
			}
			if candidate && !allowed[path] {
				return fmt.Errorf("provider inventory includes unbound stack resource %s at %s", item.Label, path)
			}
		}
	}
	return nil
}

func removeOriginalGeneration(ctx context.Context, cfg deploymentConfig, store secretStore, clusterID string, plan bool) error {
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return err
	}
	if err := checkGenerationArchiveIdle(store); err != nil {
		return err
	}
	targets, ledgerSHA, kubeSHA, err := loadGenerationRemovalTargets(cfg, store, clusterID)
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" || store.Environment != cfg.Environment {
		return errors.New("removal requires the selected LKE environment store")
	}
	providerState, err := readLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil || providerState["LKE_CLUSTER_ID"] != clusterID {
		return errors.New("canonical provider state does not identify the original removal cluster")
	}
	token, err := dnsReconciliationToken(cfg)
	if err != nil {
		return err
	}
	if err := validateDestroyBackupConfigurations(cfg.Workspace, cfg.RuntimeRoot, environmentCreateProviderValuesWithStorage(cfg)); err != nil {
		return err
	}
	clusterPresent := false
	for _, target := range targets {
		present, err := inspectGenerationProviderTarget(ctx, token, cfg.Values["CLOUD_STACK_NAME"], target)
		if err != nil {
			return err
		}
		if target.Kind == "lke" {
			clusterPresent = present
		}
	}
	path := filepath.Join(store.Root, generationRemovalIntent)
	intent := originalGenerationRemoval{Schema: 1, Environment: cfg.Environment, ClusterID: clusterID, LedgerSHA256: ledgerSHA, KubeSHA256: kubeSHA}
	_, statErr := os.Lstat(path)
	starting := errors.Is(statErr, os.ErrNotExist)
	if starting {
		if !clusterPresent {
			return errors.New("original cluster is absent without a prior admitted removal")
		}
		intent.WorkerIDs, err = readOriginalClusterWorkers(ctx, token, clusterID)
		if err != nil {
			return err
		}
	} else if statErr != nil || readEnvironmentPreflightJSON(path, &intent) != nil || intent.Schema != 1 ||
		intent.Environment != cfg.Environment || intent.ClusterID != clusterID || intent.LedgerSHA256 != ledgerSHA ||
		intent.KubeSHA256 != kubeSHA || intent.VolumeIDs == nil || intent.WorkerIDs == nil {
		return errors.New("pending removal does not match the original exact generation")
	}
	if err := validateGenerationRemovalInventory(ctx, cfg, token, targets, intent.WorkerIDs); err != nil {
		return err
	}
	if plan {
		proof, known, err := readDeploymentRemovalVolumes(cfg)
		if err != nil || !known || proof.ClusterID != clusterID || proof.KubeSHA256 != kubeSHA {
			return errors.New("read-only removal plan needs the original captured volume ownership")
		}
		for _, target := range targets {
			fmt.Printf("[VERIFIED] original %s %s %s\n", target.Kind, target.NativeID, target.Name)
		}
		fmt.Printf("[VERIFIED] original LKE worker IDs=%s, captured volumes=%d\n", strings.Join(intent.WorkerIDs, ","), len(proof.VolumeIDs))
		return nil
	}
	if !intent.KubernetesPurged {
		if !clusterPresent {
			return errors.New("original cluster disappeared before verified Kubernetes purge")
		}
		_, hash, err := deploymentRemovalKubeconfig(cfg)
		if err != nil || hash != kubeSHA {
			return errors.New("canonical kubeconfig differs from original create access receipt")
		}
		ids, err := deploymentPersistentVolumeIDs(cfg)
		if err != nil {
			return err
		}
		if !starting && !slices.Equal(ids, intent.VolumeIDs) {
			return errors.New("original volume ownership changed since removal admission")
		}
		intent.VolumeIDs = ids
	}
	save := func(replace bool) error {
		raw, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if err := store.write(generationRemovalIntent, raw, replace); err != nil {
			return err
		}
		return syncEnvironmentPreflightDirectory(filepath.Dir(path))
	}
	if starting {
		if err := save(false); err != nil {
			return err
		}
	}
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return err
	}
	proof, known, err := readDeploymentRemovalVolumes(cfg)
	if err != nil || !known || proof.ClusterID != clusterID || proof.KubeSHA256 != kubeSHA || !slices.Equal(proof.VolumeIDs, intent.VolumeIDs) {
		return errors.New("removal lost its exact original volume ownership evidence")
	}
	adapter, err := newDNSAdapter(cfg.DNSAdapter)
	if err != nil {
		return err
	}
	if err := validateDeploymentDNSReconciliation(ctx, cfg, readRemovalRecoveryNative, adapter); err != nil {
		return err
	}
	if !intent.KubernetesPurged {
		backend, hash, err := deploymentRemovalKubeconfig(cfg)
		if err != nil || hash != intent.KubeSHA256 {
			return errors.New("original Kubernetes identity changed before removal")
		}
		lease, err := backend.Read(ctx, "Lease/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/rtk-deployment-execution")
		if err != nil {
			return err
		}
		if lease != nil {
			return errors.New("original Kubernetes execution lease is occupied")
		}
		values := appendMap(cfg.Values, cfg.DNSValues)
		values["CLOUD_ENV_NAME"] = cfg.Environment
		if err := removeOwnedDNSRecords(newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{}), values); err != nil {
			return err
		}
		if err := purgeDeploymentK8s(cfg); err != nil {
			return err
		}
		intent.KubernetesPurged = true
		if err := save(true); err != nil {
			return err
		}
	}
	for _, target := range targets {
		present, err := inspectGenerationProviderTarget(ctx, token, cfg.Values["CLOUD_STACK_NAME"], target)
		if err != nil {
			return err
		}
		if present {
			fmt.Printf("[RUNNING] remove original %s %s\n", target.Kind, target.NativeID)
			_, err := environmentCreateProviderRequest(ctx, token, http.MethodDelete, target.path, nil)
			if err != nil && !generationProviderAbsent(err) {
				return err
			}
		}
		if err := waitGenerationProviderAbsent(ctx, token, target.path); err != nil {
			return err
		}
	}
	for _, id := range intent.WorkerIDs {
		if err := waitGenerationProviderAbsent(ctx, token, "/linode/instances/"+id); err != nil {
			return err
		}
	}
	if err := deleteDeploymentVolumesWithEvidence(ctx, token, intent.VolumeIDs, remainingGenerationRemovalTime(ctx), readRemovalRecoveryNative, func(token, id string) error {
		_, err := environmentCreateProviderRequest(ctx, token, http.MethodDelete, "/volumes/"+id, nil)
		return err
	}); err != nil {
		return err
	}
	if err := verifyArchivedGenerationProviderAbsence(ctx, cfg, deploymentGenerationArchive{ClusterID: clusterID, VolumeIDs: intent.VolumeIDs}); err != nil {
		return err
	}
	fmt.Printf("[VERIFIED] environment=%s original-cluster-id=%s exact provider generation absent\n", cfg.Environment, clusterID)
	return nil
}

func remainingGenerationRemovalTime(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return 30 * time.Minute
}

func checkGenerationRemovalIdle(root string) error {
	if _, err := os.Lstat(filepath.Join(root, generationRemovalIntent)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("original-generation removal is active or awaiting archive")
	}
	return nil
}

func environmentCreateProviderValuesWithStorage(cfg deploymentConfig) map[string]string {
	values := environmentCreateProviderValues(cfg)
	values["VIDEO_CLOUD_BLOB_BUCKET"] = cfg.Storage.RuntimeMedia.Bucket
	values["VIDEO_CLOUD_OTA_BLOB_BUCKET"] = cfg.Storage.OTAFirmware.Bucket
	return values
}

func waitGenerationProviderAbsent(ctx context.Context, token, path string) error {
	for {
		_, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, path, nil)
		if generationProviderAbsent(err) {
			return nil
		}
		if err != nil {
			return err
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("timed out verifying original provider absence at %s: %w", path, ctx.Err())
		case <-timer.C:
		}
	}
}
