package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

const managedLateRedisDurableMaterial = "accepted-service-core-late-normal-redis-durable-"
const managedLateRedisDurableSchema = "rtk-cloud.create-managed-late-redis-durable/v1"
const managedLateRedisDurableKey = "normal-redis-durable"
const managedLateRedisDurableFilename = "normal-tail-redis-durable.json"

type createManagedLateRedisDurable struct {
	Resume                                                                              *managedLateRedisPrePause `json:",omitempty"`
	ExtensionIntentSHA                                                                  string                    `json:",omitempty"`
	SupplementIntentSHA                                                                 string                    `json:",omitempty"`
	Schema, ParentSHA, SourceSHA, OwnerSHA, NormalAuthoritySHA                          string
	AcceptedPlanSHA, ResourceID, UID, PodName, PodUID, ReplicaSet, ReplicaSetUID, Image string
	OriginalVolumes                                                                     map[string]environmentCreateVolumeOwner
	OriginalVolumesSHA, QualifiedReview                                                 string
	SourceLeases                                                                        int
	Original                                                                            deploymentplan.Resource
	Claim, Helper, Switch                                                               deploymentplan.Object
	Volume                                                                              *environmentCreateVolumeOwner `json:",omitempty"`
	source                                                                              environmentCreateJournal
	admitted                                                                            bool
	artifactSHA, qualifiedBytesSHA                                                      string
}

type managedLateRedisDurableSaved struct {
	ExtensionAuthoritySHA               string `json:",omitempty"`
	SupplementAuthoritySHA              string `json:",omitempty"`
	Descriptor                          createManagedLateRedisDurable
	Source                              environmentCreateJournal
	QualificationSHA, QualifiedBytesSHA string
}

type managedLateRedisDurableReceipt struct {
	ExtensionAuthoritySHA                             string `json:",omitempty"`
	SupplementAuthoritySHA                            string `json:",omitempty"`
	Schema, DescriptorSHA, NormalAuthoritySHA, RDBSHA string
	Snapshot                                          managedLateRedisSnapshot
	Volume                                            environmentCreateVolumeOwner
	TargetEvidence                                    map[string]string
	DeploymentUID, PodUID                             string
	CompletedAt                                       time.Time
}

func managedLateRedisDurableDigest(d *createManagedLateRedisDurable) string {
	if d == nil {
		return ""
	}
	c := *d
	c.QualifiedReview = ""
	c.Volume = nil
	c.Resume = nil
	return deploymentplan.Digest(c)
}
func managedLateRedisDurableBase(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/redis-durable")
}
func managedLateRedisDurableError(stage string) error {
	return createOpenBaoACKBlock("redis-durable-" + stage)
}

func managedLateRedisSourcePod(deployment deploymentplan.Object, objects []deploymentplan.Object) (deploymentplan.Object, string, error) {
	uid := deploymentplan.String(deploymentplan.Map(deployment["metadata"])["uid"])
	namespace := deploymentplan.String(deploymentplan.Map(deployment["metadata"])["namespace"])
	sets := map[string]string{}
	for _, o := range objects {
		if o["kind"] != "ReplicaSet" || deploymentplan.Map(o["metadata"])["namespace"] != namespace {
			continue
		}
		for _, v := range certIssuerObjectList(deploymentplan.Map(o["metadata"])["ownerReferences"]) {
			ref := deploymentplan.Map(v)
			if ref["kind"] == "Deployment" && ref["uid"] == uid && ref["controller"] == true {
				sets[deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])] = deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
			}
		}
	}
	var found deploymentplan.Object
	rs := ""
	for _, o := range objects {
		if o["kind"] != "Pod" || deploymentplan.Map(o["metadata"])["namespace"] != namespace || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			continue
		}
		for _, v := range certIssuerObjectList(deploymentplan.Map(o["metadata"])["ownerReferences"]) {
			ref := deploymentplan.Map(v)
			if ref["kind"] == "ReplicaSet" && ref["controller"] == true && sets[deploymentplan.String(ref["uid"])] != "" {
				if found != nil {
					return nil, "", managedLateRedisDurableError("multiple-source-pods")
				}
				found = o
				rs = deploymentplan.String(ref["uid"])
			}
		}
	}
	if found == nil || deploymentplan.Map(found["status"])["phase"] != "Running" || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(found["metadata"])["uid"])) {
		return nil, "", managedLateRedisDurableError("source-pod")
	}
	containers := certIssuerObjectList(deploymentplan.Map(found["spec"])["containers"])
	if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != "redis" {
		return nil, "", managedLateRedisDurableError("source-container")
	}
	return found, rs, nil
}

// Kubernetes may inject its own projected service-account volume. Only the
// migration's named data binding is asserted here; the existing stage readback
// separately verifies the full controller, Pod ownership, readiness and image.
func managedLateRedisDataPodMatches(pod deploymentplan.Object, image string) bool {
	if pod == nil || pod["kind"] != "Pod" {
		return false
	}
	spec := deploymentplan.Map(pod["spec"])
	dataVolumes := 0
	for _, value := range certIssuerObjectList(spec["volumes"]) {
		volume := deploymentplan.Map(value)
		if volume["name"] != "data" {
			continue
		}
		claim := deploymentplan.Map(volume["persistentVolumeClaim"])
		if len(volume) != 2 || claim["claimName"] != "redis-data" || len(claim) < 1 || len(claim) > 2 {
			return false
		}
		if readOnly, exists := claim["readOnly"]; exists && readOnly != false {
			return false
		}
		dataVolumes++
	}
	containers := certIssuerObjectList(spec["containers"])
	if dataVolumes != 1 || len(containers) != 1 {
		return false
	}
	container := deploymentplan.Map(containers[0])
	if container["name"] != "redis" || container["image"] != image {
		return false
	}
	dataMounts := 0
	for _, value := range certIssuerObjectList(container["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		if mount["name"] != "data" {
			if path := deploymentplan.String(mount["mountPath"]); path == "/data" || strings.HasPrefix(path, "/data/") {
				return false
			}
			continue
		}
		if mount["mountPath"] != "/data" || (mount["subPath"] != nil && mount["subPath"] != "") || (mount["subPathExpr"] != nil && mount["subPathExpr"] != "") {
			return false
		}
		if readOnly, exists := mount["readOnly"]; exists && readOnly != false {
			return false
		}
		dataMounts++
	}
	return dataMounts == 1
}

func managedLateRedisRecipes(d *createManagedLateRedisDurable, cfg deploymentConfig) (deploymentplan.Object, deploymentplan.Object, deploymentplan.Object, error) {
	if d == nil || !environmentPreflightDigest.MatchString(d.ParentSHA) || !environmentBootstrapOwnerUUID.MatchString(d.UID) || len(strings.Split(d.ResourceID, "/")) != 3 || strings.Split(d.ResourceID, "/")[0] != "Deployment" || !immutableEnvironmentImage.MatchString(d.Image) || !environmentDurableSize.MatchString(cfg.Values["REDIS_DATA_STORAGE"]) || cfg.Values["DURABLE_DATA_STORAGE_CLASS"] == "" {
		return nil, nil, nil, managedLateRedisDurableError("recipe")
	}
	ns := strings.Split(d.ResourceID, "/")[1]
	claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "redis-data", "namespace": ns}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": cfg.Values["DURABLE_DATA_STORAGE_CLASS"], "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": cfg.Values["REDIS_DATA_STORAGE"]}}}}
	helper := managedLateRedisTargetPod(ns, "redis-durable-"+d.ParentSHA[:12], d.Image)
	switched := deploymentplan.Clone(d.Original.Desired)
	deploymentplan.Map(switched["metadata"])["uid"] = d.UID
	spec := deploymentplan.Map(switched["spec"])
	spec["strategy"] = deploymentplan.Object{"type": "Recreate"}
	pod := environmentPKIPod(switched)
	pod["volumes"] = []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "redis-data"}}}
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	container["command"] = []any{"redis-server"}
	container["args"] = []any{"--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec"}
	return claim, helper, switched, nil
}

func qualifyManagedLateRedisDurable(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r == nil || r.normalTail == nil {
		return nil
	}
	if e == nil || e.Check(ctx) != nil || store != e.Store || b.Store != store || !managedLateNormalTailControllerSelected(e.Journal) || e.Journal.Entries[431].State != "completed" || r.normalTail.Authority == nil {
		return managedLateRedisDurableError("qualified-source")
	}
	if managedLateRedisPrePauseSelected(e.Journal) {
		return qualifyManagedLateRedisPrePause(ctx, cfg, store, e, b, r, p)
	}
	id := "Deployment/" + e.Journal.Stack + "-platform/redis"
	current, err := b.Read(ctx, id)
	if err != nil || current == nil {
		return managedLateRedisDurableError("current-source")
	}
	podSpec := environmentPKIPod(current)
	volumes := certIssuerObjectList(podSpec["volumes"])
	if len(volumes) == 1 && deploymentplan.Map(deploymentplan.Map(volumes[0])["persistentVolumeClaim"])["claimName"] == "redis-data" {
		return managedLateRedisDurableError("unrecorded-persistent-switch")
	}
	if len(volumes) != 1 || deploymentplan.Map(volumes[0])["name"] != "data" || deploymentplan.Map(volumes[0])["emptyDir"] == nil {
		return managedLateRedisDurableError("original-emptydir")
	}
	var accepted deploymentplan.Resource
	planSHA := ""
	for _, entry := range e.Journal.Entries[:434] {
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		plan, j, readErr := managedTransportPostAcceptedStage(store, e, entry.Key)
		if readErr != nil {
			continue
		}
		for _, resource := range plan.Resources {
			if resource.ID == id && environmentBootstrapOwnerUUID.MatchString(j.Owners[id]) {
				accepted = resource
				accepted.UID = j.Owners[id]
				planSHA = plan.SHA256()
				break
			}
		}
		if planSHA != "" {
			break
		}
	}
	uid := deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"])
	if planSHA == "" || accepted.UID != uid || !deploymentplan.Matches(accepted.Desired, current) {
		return managedLateRedisDurableError("accepted-original-owner")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	pod, rs, err := managedLateRedisSourcePod(current, objects)
	if err != nil {
		return err
	}
	image := deploymentplan.String(deploymentplan.Map(certIssuerObjectList(podSpec["containers"])[0])["image"])
	if !immutableEnvironmentImage.MatchString(image) || deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] != image {
		return managedLateRedisDurableError("original-image")
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil || len(ledger.Volumes) != 18 {
		return managedLateRedisDurableError("original-eighteen-volumes")
	}
	original := map[string]environmentCreateVolumeOwner{}
	for _, v := range ledger.Volumes {
		original["PersistentVolumeClaim/"+v.Namespace+"/"+v.PVCName] = v
	}
	d := &createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, ParentSHA: e.Journal.PlanSHA256, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), NormalAuthoritySHA: deploymentplan.Digest(r.normalTail.Authority), AcceptedPlanSHA: planSHA, ResourceID: id, UID: uid, PodName: deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), PodUID: deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), ReplicaSetUID: rs, Image: image, Original: accepted, OriginalVolumes: original, OriginalVolumesSHA: deploymentplan.Digest(original), SourceLeases: len(e.Journal.KubernetesLeases), SupplementIntentSHA: managedLateNormalTailSupplementIntent(r), ExtensionIntentSHA: managedLateNormalTailExtensionIntent(r), source: e.Journal}
	d.Claim, d.Helper, d.Switch, err = managedLateRedisRecipes(d, cfg)
	if err != nil {
		return err
	}
	for _, object := range []deploymentplan.Object{d.Claim, d.Helper} {
		actual, readErr := b.Read(ctx, deploymentplan.ID(object))
		if readErr != nil || actual != nil {
			return managedLateRedisDurableError("unrecorded-target")
		}
	}
	for _, relative := range []string{"source.json", "source.rdb", "completed.json"} {
		path, _ := store.safePath(filepath.Join(managedLateRedisDurableBase(d.ParentSHA), relative))
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return managedLateRedisDurableError("orphan-local-state")
		}
	}
	for _, permission := range [][2]string{{"create", "persistentvolumeclaims"}, {"create", "pods"}, {"delete", "pods"}, {"create", "pods/exec"}, {"update", "deployments.apps"}, {"patch", "deployments.apps"}} {
		raw, accessErr := b.command(ctx, nil, "auth", "can-i", permission[0], permission[1], "--namespace", strings.Split(id, "/")[1])
		if accessErr != nil || strings.TrimSpace(string(raw)) != "yes" {
			return managedLateRedisDurableError("required-kubernetes-permission")
		}
	}
	for _, object := range []deploymentplan.Object{d.Claim, d.Helper} {
		admitted, admissionErr := b.Write(ctx, "create", deploymentplan.Clone(object), true)
		if admissionErr != nil || admitted == nil || !deploymentplan.Matches(object, admitted) {
			return managedLateRedisDurableError("target-admission")
		}
	}
	stop := deploymentplan.Clone(d.Original.Desired)
	deploymentplan.Map(stop["metadata"])["uid"] = d.UID
	deploymentplan.Map(stop["spec"])["replicas"] = float64(0)
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: managedLateRedisDurableDigest(d)}
	for _, definition := range []deploymentplan.Definition{{Object: d.Claim, Phase: "storage"}, {Object: d.Helper, Phase: "workload"}, {Object: stop, Phase: "workload"}, {Object: d.Switch, Phase: "workload"}} {
		if _, compileErr := deploymentplan.Compile(inputs, []deploymentplan.Definition{definition}, objects); compileErr != nil {
			return managedLateRedisDurableError("child-plan-prerequisites")
		}
	}
	for _, recipe := range []deploymentplan.Object{stop, d.Switch} {
		dry := deploymentplan.Clone(recipe)
		deploymentplan.Map(dry["metadata"])["uid"] = uid
		deploymentplan.Map(dry["metadata"])["resourceVersion"] = deploymentplan.Map(current["metadata"])["resourceVersion"]
		if admitted, admissionErr := b.Write(ctx, "update", dry, true); admissionErr != nil || admitted == nil || !deploymentplan.Matches(recipe, admitted) {
			return managedLateRedisDurableError("writer-admission")
		}
	}
	if prior := r.redisDurable; prior != nil {
		if managedLateRedisDurableDigest(prior) != managedLateRedisDurableDigest(d) {
			return managedLateRedisDurableError("qualified-prerequisite-drift")
		}
		d.QualifiedReview, d.admitted, d.artifactSHA, d.qualifiedBytesSHA = prior.QualifiedReview, prior.admitted, prior.artifactSHA, prior.qualifiedBytesSHA
	}
	r.redisDurable = d
	state, err := readManagedLateRedisNative(ctx, managedLateRedisRunner(cfg, store, e, b, r, d, ""), "source")
	if err != nil || state.AOF != 0 || state.RDBChanges != 0 {
		return managedLateRedisDurableError("source-prerequisites")
	}
	p.Materials[managedLateRedisDurableMaterial+"intent"] = managedLateRedisDurableDigest(d)
	p.Materials[managedLateRedisDurableMaterial+"original-volumes"] = d.OriginalVolumesSHA
	return e.Check(ctx)
}

func managedLateRedisRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, d *createManagedLateRedisDurable) error {
	if e == nil || r == nil || r.normalTail == nil || r.normalTail.Authority == nil || d == nil || e.Check(ctx) != nil || store != e.Store || d.ParentSHA != e.Journal.PlanSHA256 || d.OwnerSHA != deploymentplan.Digest(e.owner) || d.NormalAuthoritySHA != deploymentplan.Digest(r.normalTail.Authority) || !time.Now().Before(managedLateNormalTailEffectiveDeadline(r)) {
		return managedLateRedisDurableError("runtime-source")
	}
	a, err := readManagedLateNormalTailCurrentAuthority(store, r.normalTail)
	if err != nil || a == nil || deploymentplan.Digest(a) != d.NormalAuthoritySHA {
		return managedLateRedisDurableError("unchanged-first-authority")
	}
	if d.SupplementIntentSHA != managedLateNormalTailSupplementIntent(r) {
		return managedLateRedisDurableError("supplement-intent-drift")
	}
	if d.ExtensionIntentSHA != managedLateNormalTailExtensionIntent(r) {
		return managedLateRedisDurableError("extension-intent-drift")
	}
	if d.admitted {
		if err := verifyManagedLateNormalTailSupplementRuntime(store, r); err != nil {
			return err
		}
	}
	if managedLateRedisPrePauseSelected(e.Journal) {
		resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
		if err != nil || saved.Descriptor.SourceSHA != d.SourceSHA || resume.SourceSHA != d.SourceSHA {
			return managedLateRedisDurableError("runtime-pre-pause")
		}
	} else if _, err := managedLateRedisDurableSourcePrefix(d.source, e.Journal); err != nil {
		return err
	}
	return e.Check(ctx)
}

func managedLateRedisRunner(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, d *createManagedLateRedisDurable, targetUID string) managedLateRedisNativeRun {
	return func(ctx context.Context, target string, input []byte, args ...string) ([]byte, error) {
		if err := managedLateRedisRuntime(ctx, cfg, store, e, r, d); err != nil {
			return nil, err
		}
		name, uid, container := d.PodName, d.PodUID, "redis"
		if target == "target" {
			name = deploymentplan.String(deploymentplan.Map(d.Helper["metadata"])["name"])
			uid = targetUID
			container = "redis-migration"
		} else if target != "source" {
			return nil, managedLateRedisDurableError("endpoint")
		}
		ns := strings.Split(d.ResourceID, "/")[1]
		pod, err := b.Read(ctx, "Pod/"+ns+"/"+name)
		if err != nil || pod == nil || deploymentplan.Map(pod["metadata"])["uid"] != uid || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil {
			return nil, managedLateRedisDurableError("exact-endpoint-owner")
		}
		if target == "target" {
			if d.Volume == nil || !deploymentplan.Matches(d.Helper, pod) {
				return nil, managedLateRedisDurableError("isolated-target-recipe")
			}
			claim, claimErr := b.Read(ctx, deploymentplan.ID(d.Claim))
			if claimErr != nil || claim == nil || deploymentplan.Map(claim["metadata"])["uid"] != d.Volume.PVCUID || deploymentplan.Map(claim["status"])["phase"] != "Bound" {
				return nil, managedLateRedisDurableError("target-current-claim")
			}
		}
		containers := certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])
		if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != container || deploymentplan.Map(containers[0])["image"] != d.Image {
			return nil, managedLateRedisDurableError("endpoint-image")
		}
		if !d.admitted && !(len(input) == 0 && (len(args) == 4 && args[0] == "valkey-cli" && args[1] == "--raw" && args[2] == "INFO" && args[3] == "all" || len(args) == 5 && args[0] == "valkey-cli" && args[1] == "--raw" && args[2] == "EVAL_RO" && args[3] == managedLateRedisDataDigest && args[4] == "0")) {
			return nil, managedLateRedisDurableError("qualification-read-only-command")
		}
		command := []string{"exec", "--namespace", ns, name, "-c", container}
		if len(input) > 0 {
			command = append(command, "-i")
		}
		command = append(command, "--")
		command = append(command, args...)
		raw, err := b.command(ctx, input, command...)
		if err != nil {
			return nil, managedLateRedisDurableError("native-command")
		}
		return raw, e.Check(ctx)
	}
}

func managedLateRedisVolume(ctx context.Context, b environmentKubernetesBackend, d *createManagedLateRedisDurable, uid string) (environmentCreateVolumeOwner, error) {
	var zero environmentCreateVolumeOwner
	pvc, err := b.Read(ctx, deploymentplan.ID(d.Claim))
	if err != nil || pvc == nil || deploymentplan.Map(pvc["metadata"])["uid"] != uid || deploymentplan.Map(pvc["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(pvc["status"])["phase"] != "Bound" {
		return zero, managedLateRedisDurableError("claim-bound-owner")
	}
	pvName := deploymentplan.String(deploymentplan.Map(pvc["spec"])["volumeName"])
	pv, err := b.Read(ctx, "PersistentVolume//"+pvName)
	if err != nil || pv == nil {
		return zero, managedLateRedisDurableError("native-pv")
	}
	spec := deploymentplan.Map(pv["spec"])
	ref := deploymentplan.Map(spec["claimRef"])
	csi := deploymentplan.Map(spec["csi"])
	meta := deploymentplan.Map(pv["metadata"])
	if !environmentBootstrapOwnerUUID.MatchString(uid) || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(meta["uid"])) || meta["deletionTimestamp"] != nil || deploymentplan.Map(pv["status"])["phase"] != "Bound" || spec["storageClassName"] != deploymentplan.Map(d.Claim["spec"])["storageClassName"] || ref["uid"] != uid || ref["name"] != "redis-data" || ref["namespace"] != deploymentplan.Map(d.Claim["metadata"])["namespace"] || csi["driver"] != "linodebs.csi.linode.com" || spec["persistentVolumeReclaimPolicy"] != "Retain" {
		return zero, managedLateRedisDurableError("native-csi-binding")
	}
	native := deploymentplan.String(csi["volumeHandle"])
	if prefix, suffix, ok := strings.Cut(native, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
		native = prefix
	}
	if _, err := positiveIntValue("redis-volume-id", native); err != nil {
		return zero, managedLateRedisDurableError("native-volume-id")
	}
	return environmentCreateVolumeOwner{NativeID: native, PVName: pvName, PVUID: deploymentplan.String(meta["uid"]), PVCName: "redis-data", PVCUID: uid, Namespace: deploymentplan.String(ref["namespace"])}, nil
}

func readManagedLateRedisArchive(store secretStore, parent, sha string) ([]byte, error) {
	path, err := privateDeploymentStatePath(store.Environment, filepath.Join(managedLateRedisDurableBase(parent), "source.rdb"))
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, managedLateRedisDurableError("archive-path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 17 || info.Size() > 64<<20 {
		return nil, managedLateRedisDurableError("archive-file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil || len(raw) > 64<<20 || environmentPreflightBytesSHA256(raw) != sha {
		return nil, managedLateRedisDurableError("archive-bytes")
	}
	return raw, nil
}

func readManagedLateRedisDurableVolumeAuthority(store secretStore, e *environmentCreateExecution) (map[string]environmentCreateVolumeOwner, *environmentCreateVolumeOwner, error) {
	if e != nil && managedLateRedisPrePauseSelected(e.Journal) {
		_, saved, err := readManagedLateRedisPrePauseSource(store, e)
		if err != nil {
			return nil, nil, err
		}
		var v environmentCreateVolumeOwner
		if createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(saved.Descriptor.ParentSHA), "volume.json"), &v) != nil {
			return nil, nil, managedLateRedisDurableError("pre-pause-volume")
		}
		return saved.Descriptor.OriginalVolumes, &v, nil
	}
	var saved managedLateRedisDurableSaved
	if e == nil || createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(e.Journal.PlanSHA256), "source.json"), &saved) != nil || saved.Descriptor.Schema != managedLateRedisDurableSchema || saved.Descriptor.ParentSHA != e.Journal.PlanSHA256 || saved.Descriptor.SourceSHA != deploymentplan.Digest(saved.Source) || len(saved.Source.Entries) != 434 || saved.Descriptor.OwnerSHA != deploymentplan.Digest(e.owner) || saved.Descriptor.OriginalVolumesSHA != deploymentplan.Digest(saved.Descriptor.OriginalVolumes) {
		return nil, nil, managedLateRedisDurableError("saved-source")
	}
	d := &saved.Descriptor
	var a createFoundationRecoveryArtifact
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", d.QualifiedReview, "qualification.json"), &a) != nil || deploymentplan.Digest(a.Plan) != d.QualifiedReview || a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != managedLateRedisDurableDigest(d) || a.Plan.ParentJournalSHA != d.SourceSHA || a.Plan.ParentOwnerSHA != d.OwnerSHA || deploymentplan.Digest(a) != saved.QualificationSHA {
		return nil, nil, managedLateRedisDurableError("qualified-source")
	}
	raw, err := readManagedLateNormalTailPair(store, d.ParentSHA)
	var normal createManagedLateNormalTailAuthority
	if err != nil || decodeEnvironmentJSON(raw, &normal) != nil || deploymentplan.Digest(normal) != d.NormalAuthoritySHA {
		return nil, nil, managedLateRedisDurableError("saved-authority")
	}
	qualifiedRaw, rawErr := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", d.QualifiedReview, "qualification.json"), true)
	var first createManagedLateNormalTail
	if rawErr != nil || environmentPreflightBytesSHA256(qualifiedRaw) != saved.QualifiedBytesSHA || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", normal.Review, managedLateNormalTailFilename), &first) != nil {
		return nil, nil, managedLateRedisDurableError("saved-raw-authorities")
	}
	if authority, authorityErr := readManagedLateNormalTailAuthority(store, &first); authorityErr != nil || authority == nil || deploymentplan.Digest(authority) != d.NormalAuthoritySHA {
		return nil, nil, managedLateRedisDurableError("saved-first-authority")
	}
	deadline, admittedAt := normal.Deadline, normal.AdmittedAt
	if d.SupplementIntentSHA != "" {
		supplement, supplementErr := readManagedLateNormalTailSupplementAuthority(store, d.ParentSHA)
		if supplementErr != nil || supplement == nil || deploymentplan.Digest(supplement) != saved.SupplementAuthoritySHA || supplement.IntentSHA != d.SupplementIntentSHA || supplement.OriginalAuthoritySHA != d.NormalAuthoritySHA || supplement.SourceSHA != d.SourceSHA || supplement.OwnerSHA != d.OwnerSHA {
			return nil, nil, managedLateRedisDurableError("saved-supplement-authority")
		}
		deadline, admittedAt = supplement.Deadline, supplement.AdmittedAt
	} else if saved.SupplementAuthoritySHA != "" {
		return nil, nil, managedLateRedisDurableError("unexpected-supplement-authority")
	}
	if d.ExtensionIntentSHA != "" {
		extension, extensionErr := readManagedLateNormalTailExtensionAuthority(store, d.ParentSHA)
		if extensionErr != nil || extension == nil || deploymentplan.Digest(extension) != saved.ExtensionAuthoritySHA || extension.IntentSHA != d.ExtensionIntentSHA || extension.OriginalAuthoritySHA != d.NormalAuthoritySHA || extension.BaseAuthoritySHA != saved.SupplementAuthoritySHA || extension.SourceSHA != d.SourceSHA || extension.OwnerSHA != d.OwnerSHA {
			return nil, nil, managedLateRedisDurableError("saved-extension-authority")
		}
		deadline, admittedAt = extension.Deadline, extension.AdmittedAt
	} else if saved.ExtensionAuthoritySHA != "" {
		return nil, nil, managedLateRedisDurableError("unexpected-extension-authority")
	}
	current := e.Journal
	migrationEnd := len(current.Entries)
	if migrationEnd > 443 {
		migrationEnd = 443
	}
	for _, entry := range current.Entries[min(434, migrationEnd):migrationEnd] {
		if entry.StartedAt.Before(admittedAt) || entry.StartedAt.After(deadline) || !entry.CompletedAt.IsZero() && entry.CompletedAt.After(deadline) {
			return nil, nil, managedLateRedisDurableError("saved-migration-window")
		}
	}
	if len(current.Entries) >= 443 {
		var completed managedLateRedisDurableReceipt
		outer := createOpenBaoACKCompletionEntry(current, managedLateRedisDurableKey)
		var recorded managedLateRedisDurableReceipt
		if outer == nil || outer.State != "completed" || decodeEnvironmentJSON(outer.Receipt, &recorded) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "completed.json"), &completed) != nil || completed.DescriptorSHA != managedLateRedisDurableDigest(d) || completed.Schema != managedLateRedisDurableSchema || completed.NormalAuthoritySHA != d.NormalAuthoritySHA || completed.SupplementAuthoritySHA != saved.SupplementAuthoritySHA || completed.ExtensionAuthoritySHA != saved.ExtensionAuthoritySHA || completed.CompletedAt.Before(admittedAt) || !completed.CompletedAt.Before(deadline) || deploymentplan.Digest(completed) != deploymentplan.Digest(recorded) || len(current.KubernetesLeases) < saved.Descriptor.SourceLeases+5 {
			return nil, nil, managedLateRedisDurableError("completed-migration-prefix")
		}

		current.Entries = append([]environmentCreateJournalEntry(nil), current.Entries[:443]...)
		current.KubernetesLeases = current.KubernetesLeases[:saved.Descriptor.SourceLeases+5]
		current.CompletedAt = time.Time{}
	}
	if _, err := managedLateRedisDurableSourcePrefix(saved.Source, current); err != nil {
		return nil, nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/normal-redis-durable-claim")
	if entry == nil || entry.State != "completed" {
		return nil, nil, managedLateRedisDurableError("accepted-claim")
	}
	plan, j, err := managedTransportPostAcceptedStage(store, e, entry.Key)
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].ID != deploymentplan.ID(d.Claim) || plan.Resources[0].Action != "create" || !deploymentplan.Matches(d.Claim, plan.Resources[0].Desired) {
		return nil, nil, managedLateRedisDurableError("accepted-claim-plan")
	}
	var volume environmentCreateVolumeOwner
	if createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "volume.json"), &volume) != nil || volume.PVCUID != j.Owners[plan.Resources[0].ID] || volume.PVCName != "redis-data" || volume.Namespace != strings.Split(d.ResourceID, "/")[1] {
		return nil, nil, managedLateRedisDurableError("accepted-volume-proof")
	}
	snapshotEntry := createOpenBaoACKCompletionEntry(e.Journal, "normal-redis-durable-snapshot")
	if snapshotEntry == nil && len(e.Journal.Entries) == 437 && e.Journal.Entries[436].State == "completed" {
		return d.OriginalVolumes, &volume, nil
	}
	var snapshotReceipt struct {
		Snapshot managedLateRedisSnapshot
		Volume   environmentCreateVolumeOwner
	}
	if snapshotEntry == nil || snapshotEntry.State != "completed" || decodeEnvironmentJSON(snapshotEntry.Receipt, &snapshotReceipt) != nil || snapshotReceipt.Volume != volume || !environmentPreflightDigest.MatchString(snapshotReceipt.Snapshot.SHA) {
		return nil, nil, managedLateRedisDurableError("genuine-volume-snapshot-receipt")
	}
	return d.OriginalVolumes, &volume, nil
}

// Partial/uncertain migration outcomes deliberately retain the original fence.
// They require exact journal reconciliation; no missing Pod, claim or archive
// is permission to regenerate, adopt, restore emptyDir or replay a switch.
func executeManagedLateRedisDurable(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (result error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.redisDurable == nil {
		return nil
	}
	d := r.redisDurable
	if err := managedLateRedisRuntime(ctx, cfg, store, e, r, d); err != nil {
		return err
	}
	if old := createOpenBaoACKCompletionEntry(e.Journal, managedLateRedisDurableKey); old != nil && d.Resume == nil {
		return managedLateRedisDurableError("recorded-outcome-needs-reconciliation")
	}
	if time.Until(managedLateNormalTailEffectiveDeadline(r)) < 15*time.Minute {
		return managedLateRedisDurableError("pause-reserve")
	}
	var a createFoundationRecoveryArtifact
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", d.QualifiedReview, "qualification.json"), &a) != nil || deploymentplan.Digest(a.Plan) != d.QualifiedReview || a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != managedLateRedisDurableDigest(d) {
		return managedLateRedisDurableError("fresh-qualified-intent")
	}
	bytesSHA, err := readManagedLateQualificationFingerprint(store, a)
	checker, checkerErr := environmentPreflightCheckerSHA256()
	if err != nil || checkerErr != nil || checker != a.Plan.CheckerSHA || !d.admitted || d.artifactSHA != deploymentplan.Digest(a) || d.qualifiedBytesSHA != bytesSHA || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 {
		return managedLateRedisDurableError("fresh-six-go")
	}
	if d.Resume != nil {
		if err := resumeManagedLateRedisHelperAcceptance(ctx, cfg, store, runtime, a); err != nil {
			return err
		}
	} else {
		saved := managedLateRedisDurableSaved{ExtensionAuthoritySHA: managedLateNormalTailExtensionAuthoritySHA(r), SupplementAuthoritySHA: managedLateNormalTailSupplementAuthoritySHA(r), Descriptor: *d, Source: d.source, QualificationSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA}
		if managedLateSourceReuseActive(e) {
			if err := checkManagedLateSourceReuse(e); err != nil {
				return err
			}
		}
		invalidateManagedLateSourceReuse(e)
		if _, err := writeManagedLateRedisDurableSource(store, saved); err != nil {
			return err
		}
		intent := mustProviderRecoveryJSON(deploymentplan.Object{"descriptor_sha256": managedLateRedisDurableDigest(d), "review_sha256": d.QualifiedReview, "authority_sha256": d.NormalAuthoritySHA})
		if err := e.BeginEntry(managedLateRedisDurableKey, "redis-durable", intent, time.Now().UTC()); err != nil {
			return err
		}
		// Allocate/verify the isolated destination while the original writer is live.
		if err := apply(ctx, "normal-redis-durable-claim", []deploymentplan.Definition{{Object: d.Claim, Phase: "storage"}}); err != nil {
			return err
		}
		claimPlan, claimJournal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/normal-redis-durable-claim")
		if err != nil || len(claimPlan.Resources) != 1 {
			return managedLateRedisDurableError("claim-receipt")
		}
		volume, err := managedLateRedisVolume(ctx, runtime.Backend, d, claimJournal.Owners[deploymentplan.ID(d.Claim)])
		if err != nil {
			return err
		}
		d.Volume = &volume
		if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "volume.json"), volume); err != nil {
			return err
		}
		if err := apply(ctx, "normal-redis-durable-target", []deploymentplan.Definition{{Object: d.Helper, Phase: "workload"}}); err != nil {
			return err
		}
	}
	var volume environmentCreateVolumeOwner
	if d.Volume == nil || createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "volume.json"), &volume) != nil || volume != *d.Volume {
		return managedLateRedisDurableError("retained-target-volume")
	}
	_, targetJournal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/normal-redis-durable-target")
	if err != nil {
		return err
	}
	targetUID := targetJournal.Owners[deploymentplan.ID(d.Helper)]
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", strings.Split(d.ResourceID, "/")[1], "wait", "--for=condition=Ready", "Pod/"+deploymentplan.String(deploymentplan.Map(d.Helper["metadata"])["name"]), "--timeout=120s"); err != nil {
		return managedLateRedisDurableError("target-pod-not-ready")
	}
	run := managedLateRedisRunner(cfg, store, e, runtime.Backend, r, d, targetUID)
	source, err := readManagedLateRedisNative(ctx, run, "source")
	if err != nil || source.AOF != 0 {
		return managedLateRedisDurableError("pre-pause-source")
	}
	if err := managedLateRedisVerifyOriginalVolumes(ctx, runtime.Backend, d.OriginalVolumes); err != nil {
		return err
	}
	if time.Until(managedLateNormalTailEffectiveDeadline(r)) < 15*time.Minute {
		return managedLateRedisDurableError("pre-pause-reserve")
	}
	if err := e.BeginEntry("normal-redis-durable-snapshot", "redis-durable", mustProviderRecoveryJSON(deploymentplan.Object{"descriptor_sha256": managedLateRedisDurableDigest(d), "state_sha256": managedLateRedisNativeStateSHA(source)}), time.Now().UTC()); err != nil {
		return err
	}
	var supplements []*createManagedLateNormalTailSupplementAuthority
	if r.normalSupplement != nil {
		supplements = append(supplements, r.normalSupplement.Authority)
	}
	var pause *managedLateRedisPause
	if r.normalSupplement != nil && r.normalSupplement.Extension != nil {
		pause, err = beginManagedLateRedisPauseWithExtension(ctx, run, managedLateNormalTailEffectiveDeadline(r), r.normalSupplement.Extension.Authority)
	} else {
		pause, err = beginManagedLateRedisPause(ctx, run, managedLateNormalTailEffectiveDeadline(r), supplements...)
	}
	if err != nil {
		return err
	}
	switchAttempted := false
	defer func() {
		if !switchAttempted {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if unpauseErr := pause.Unpause(cleanup); unpauseErr != nil {
				result = unpauseErr
			}
		}
	}()
	snapshot, err := snapshotManagedLateRedis(ctx, run, pause, source)
	if err != nil {
		return err
	}
	if err := store.write(filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "source.rdb"), snapshot.RDB, false); err != nil {
		return managedLateRedisDurableError("exclusive-original-archive")
	}
	if raw, err := readManagedLateRedisArchive(store, d.ParentSHA, snapshot.SHA); err != nil || !bytes.Equal(raw, snapshot.RDB) {
		return managedLateRedisDurableError("retained-archive")
	}
	if err := e.RecordReceipt("normal-redis-durable-snapshot", mustProviderRecoveryJSON(struct {
		Snapshot managedLateRedisSnapshot
		Volume   environmentCreateVolumeOwner
	}{snapshot, volume})); err != nil {
		return err
	}
	if err := e.CompleteEntry("normal-redis-durable-snapshot", time.Now().UTC()); err != nil {
		return err
	}
	evidence, err := prepareManagedLateRedisTarget(ctx, run, pause, snapshot)
	if err != nil {
		return err
	}
	if err := e.BeginEntry("normal-redis-durable-target-verified", "redis-durable", mustProviderRecoveryJSON(evidence), time.Now().UTC()); err != nil {
		return err
	}
	if err := e.RecordReceipt("normal-redis-durable-target-verified", mustProviderRecoveryJSON(evidence)); err != nil {
		return err
	}
	if err := e.CompleteEntry("normal-redis-durable-target-verified", time.Now().UTC()); err != nil {
		return err
	}
	helper, err := runtime.Backend.Read(ctx, deploymentplan.ID(d.Helper))
	if err != nil || helper == nil || deploymentplan.Map(helper["metadata"])["uid"] != targetUID {
		return managedLateRedisDurableError("exact-helper-retirement")
	}
	if err := apply(ctx, "normal-redis-durable-retire", []deploymentplan.Definition{{Object: deploymentplan.Projection(helper), Phase: "retirement", Retire: true}}); err != nil {
		return err
	}
	if err := pause.Renew(ctx); err != nil {
		return err
	}
	if err := managedLateRedisRuntime(ctx, cfg, store, e, r, d); err != nil {
		return err
	}
	current, err := runtime.Backend.Read(ctx, d.ResourceID)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != d.UID || !deploymentplan.Matches(d.Original.Desired, current) {
		return managedLateRedisDurableError("original-writer-before-switch")
	}
	beforeStop, stateErr := readManagedLateRedisNative(ctx, run, "source")
	if stateErr != nil || beforeStop.RunID != snapshot.State.RunID || !managedLateRedisSameData(beforeStop, snapshot.State) || pause.Check(time.Now().UTC()) != nil {
		return managedLateRedisDurableError("paused-source-before-stop")
	}
	stopped := deploymentplan.Clone(d.Original.Desired)
	deploymentplan.Map(stopped["metadata"])["uid"] = d.UID
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	switchAttempted = true
	stopCtx, stopCancel := context.WithDeadline(ctx, pause.Until.Add(-10*time.Second))
	stopErr := apply(stopCtx, "normal-redis-durable-stop", []deploymentplan.Definition{{Object: stopped, Phase: "workload"}})
	if stopErr == nil {
		for {
			oldPod, readErr := runtime.Backend.Read(stopCtx, "Pod/"+strings.Split(d.ResourceID, "/")[1]+"/"+d.PodName)
			if readErr != nil {
				stopErr = readErr
				break
			}
			if oldPod == nil {
				break
			}
			if deploymentplan.Map(oldPod["metadata"])["uid"] != d.PodUID {
				stopErr = managedLateRedisDurableError("source-pod-replaced")
				break
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-stopCtx.Done():
				timer.Stop()
				stopErr = stopCtx.Err()
			case <-timer.C:
			}
			if stopErr != nil {
				break
			}
		}
	}
	stopCancel()
	if stopErr != nil || pause.Check(time.Now().UTC()) != nil {
		return managedLateRedisDurableError("source-stop-unverified-no-pvc-start")
	}
	stoppedActual, stopReadErr := runtime.Backend.Read(ctx, d.ResourceID)
	if stopReadErr != nil || stoppedActual == nil || deploymentplan.Map(stoppedActual["metadata"])["uid"] != d.UID || !deploymentplan.Matches(stopped, stoppedActual) {
		return managedLateRedisDurableError("stopped-original-owner")
	}
	stoppedInventory, inventoryErr := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	stopPlan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped"}, Resources: []deploymentplan.Resource{{ID: d.ResourceID, UID: d.UID, Action: "retain", Phase: "workload", Desired: stopped}}}
	if inventoryErr != nil || verifyEnvironmentTargetWorkloads(ctx, stopPlan, stoppedInventory, func(context.Context, string) (map[string]bool, error) {
		return nil, managedLateRedisDurableError("stopped-owner-has-pod")
	}) != nil || pause.Check(time.Now().UTC()) != nil {
		return managedLateRedisDurableError("source-controller-not-quiescent")
	}
	if err := apply(ctx, "normal-redis-durable-switch", []deploymentplan.Definition{{Object: d.Switch, Phase: "workload"}}); err != nil {
		return err
	}
	objects, err := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	current, err = runtime.Backend.Read(ctx, d.ResourceID)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != d.UID || !deploymentplan.Matches(d.Switch, current) {
		return managedLateRedisDurableError("same-owner-switch")
	}
	newPod, _, err := managedLateRedisSourcePod(current, objects)
	if err != nil {
		return err
	}
	if !managedLateRedisDataPodMatches(newPod, d.Image) {
		return managedLateRedisDurableError("actual-target-data-mount")
	}
	verified := *d
	verified.PodName = deploymentplan.String(deploymentplan.Map(newPod["metadata"])["name"])
	verified.PodUID = deploymentplan.String(deploymentplan.Map(newPod["metadata"])["uid"])
	final, err := readManagedLateRedisNative(ctx, managedLateRedisRunner(cfg, store, e, runtime.Backend, r, &verified, ""), "source")
	if err != nil || final.AOF != 1 || final.AOFWriteStatus != "ok" || final.AOFStatus != "ok" || final.AOFRewriting != 0 || final.AOFScheduled != 0 || !managedLateRedisSameData(final, snapshot.State) {
		return managedLateRedisDurableError("native-loaded-data")
	}
	if _, err := managedLateRedisVolume(ctx, runtime.Backend, d, volume.PVCUID); err != nil {
		return err
	}
	if err := managedLateRedisVerifyOriginalVolumes(ctx, runtime.Backend, d.OriginalVolumes); err != nil {
		return err
	}
	if err := managedLateRedisRuntime(ctx, cfg, store, e, r, d); err != nil {
		return err
	}
	receipt := managedLateRedisDurableReceipt{ExtensionAuthoritySHA: managedLateNormalTailExtensionAuthoritySHA(r), SupplementAuthoritySHA: managedLateNormalTailSupplementAuthoritySHA(r), Schema: managedLateRedisDurableSchema, DescriptorSHA: managedLateRedisDurableDigest(d), NormalAuthoritySHA: d.NormalAuthoritySHA, RDBSHA: snapshot.SHA, Snapshot: snapshot, Volume: volume, TargetEvidence: evidence, DeploymentUID: d.UID, PodUID: verified.PodUID, CompletedAt: time.Now().UTC()}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "completed.json"), receipt); err != nil {
		return err
	}
	if err := e.BeginEntry("normal-redis-durable-verified", "redis-durable", mustProviderRecoveryJSON(receipt), time.Now().UTC()); err != nil {
		return err
	}
	if err := e.RecordReceipt("normal-redis-durable-verified", mustProviderRecoveryJSON(receipt)); err != nil {
		return err
	}
	if err := e.CompleteEntry("normal-redis-durable-verified", time.Now().UTC()); err != nil {
		return err
	}
	if err := e.RecordReceipt(managedLateRedisDurableKey, mustProviderRecoveryJSON(receipt)); err != nil {
		return err
	}
	return e.CompleteEntry(managedLateRedisDurableKey, time.Now().UTC())
}

func managedLateRedisVerifyOriginalVolumes(ctx context.Context, b environmentKubernetesBackend, original map[string]environmentCreateVolumeOwner) error {
	if len(original) != 18 {
		return managedLateRedisDurableError("original-eighteen")
	}
	for id, owner := range original {
		claim, err := b.Read(ctx, id)
		if err != nil || claim == nil || deploymentplan.Map(claim["metadata"])["uid"] != owner.PVCUID || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.Map(claim["spec"])["volumeName"] != owner.PVName {
			return managedLateRedisDurableError("original-pvc-drift")
		}
		pv, err := b.Read(ctx, "PersistentVolume//"+owner.PVName)
		if err != nil || pv == nil || deploymentplan.Map(pv["metadata"])["uid"] != owner.PVUID || deploymentplan.Map(pv["metadata"])["deletionTimestamp"] != nil {
			return managedLateRedisDurableError("original-pv-drift")
		}
		spec := deploymentplan.Map(pv["spec"])
		ref := deploymentplan.Map(spec["claimRef"])
		csi := deploymentplan.Map(spec["csi"])
		native := deploymentplan.String(csi["volumeHandle"])
		if prefix, suffix, ok := strings.Cut(native, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
			native = prefix
		}
		if csi["driver"] != "linodebs.csi.linode.com" || native != owner.NativeID || ref["uid"] != owner.PVCUID || ref["name"] != owner.PVCName || ref["namespace"] != owner.Namespace {
			return managedLateRedisDurableError("original-csi-drift")
		}
	}
	return ctx.Err()
}
