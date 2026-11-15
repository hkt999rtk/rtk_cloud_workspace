package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// unifiedOpenBaoParentFixture holds one synthetic provider-recovery parent and
// the genuine in-place CLI receipt issued under its original execution fence.
// Kubernetes and publication transports are isolated fixtures, not a live API.
type unifiedOpenBaoParentFixture struct {
	cfg       deploymentConfig
	store     secretStore
	execution *environmentCreateExecution
	plan      environmentCreatePlan
	native    environmentKubernetesBackend
	nativeDir string
	fault     *sessionNativeFaultBackend
	backend   freshStageBackend
	owner     openBaoInPlaceOwner
	calls     string
}

func newUnifiedOpenBaoParentFixture(t *testing.T, apiImage environmentReleaseImage, sameNative ...bool) unifiedOpenBaoParentFixture {
	t.Helper()
	return newUnifiedOpenBaoParentFixtureWithHook(t, apiImage, nil, sameNative...)
}

func newUnifiedOpenBaoParentFixtureWithHook(t *testing.T, apiImage environmentReleaseImage, beforeProducer func(unifiedOpenBaoParentFixture, *deploymentplan.Plan), sameNative ...bool) unifiedOpenBaoParentFixture {
	return newUnifiedOpenBaoParentFixtureWithConfiguration(t, apiImage, beforeProducer, nil, sameNative...)
}

func newUnifiedOpenBaoParentFixtureWithConfiguration(t *testing.T, apiImage environmentReleaseImage, beforeProducer func(unifiedOpenBaoParentFixture, *deploymentplan.Plan), configure func(*deploymentConfig), sameNative ...bool) unifiedOpenBaoParentFixture {
	return newUnifiedOpenBaoParentFixtureWithAdmission(t, apiImage, beforeProducer, configure, nil, sameNative...)
}

func newUnifiedOpenBaoParentFixtureWithAdmission(t *testing.T, apiImage environmentReleaseImage, beforeProducer func(unifiedOpenBaoParentFixture, *deploymentplan.Plan), configure func(*deploymentConfig), admission *providerExecutionContractInputs, sameNative ...bool) unifiedOpenBaoParentFixture {
	t.Helper()
	if len(sameNative) > 1 {
		t.Fatal("ambiguous native source mode")
	}
	useNative := len(sameNative) == 1 && sameNative[0]
	var cfg deploymentConfig
	var store secretStore
	var old *environmentCreateExecution
	var admitted environmentCreatePlan
	if admission == nil {
		cfg, store, old, _, admitted = providerExecutionContractFixtureWithConfiguration(t, configure)
	} else {
		cfg, store, old, _, admitted = providerExecutionContractFixtureWithConfiguration(t, configure, *admission)
	}
	publication := newCreateOpenBaoACKImageFixture(t)
	// The publication fixture establishes its own root. All subsequent parent,
	// receipt, and operator operations must use the original isolated store.
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	_, _, objects := existingFileOpenBaoFixture("dev", true)
	set := deploymentplan.Clone(objects[0])
	// Preserve the original host environment and add the same synthetic
	// session/trust projection later consumed by the unified session fixture.
	podSpec := environmentPKIPod(set)
	host := managedNamedField(podSpec["containers"], "openbao-pki")
	if host == nil {
		t.Fatal("original synthetic OpenBao host missing")
	}
	host["env"] = mergeEnvironmentNamedFields(host["env"], environmentCreateEnv(map[string]string{
		"PKI_BOOTSTRAP_SESSION_ID":        "33333333-3333-4333-8333-333333333333",
		"OPENBAO_PKI_HOST_RENEWAL_TLS_CA": "/run/pki-service/root.pem",
	}))
	host["volumeMounts"] = append(certIssuerObjectList(host["volumeMounts"]), deploymentplan.Object{"name": "service-trust", "mountPath": "/run/pki-service", "readOnly": true})
	podSpec["volumes"] = append(certIssuerObjectList(podSpec["volumes"]), deploymentplan.Object{"name": "service-trust", "configMap": deploymentplan.Object{"name": "service-trust"}})
	meta := deploymentplan.Map(set["metadata"])
	if useNative {
		meta["uid"] = "11111111-aaaa-4aaa-8aaa-111111111111"
		meta["generation"] = float64(1)
		set["status"] = deploymentplan.Object{"observedGeneration": float64(1), "updateRevision": "old-revision"}
	}
	meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}
	oldRef := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	managedNamedField(environmentPKIPod(set)["containers"], "openbao-pki")["image"] = oldRef
	managedNamedField(environmentPKIPod(set)["initContainers"], "openbao-pki-install")["image"] = oldRef
	target := createVersionImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("d", 64), SourceRevision: publication.selection.Revision, Publication: &createVersionPublication{Repository: "hkt999rtk/rtk_video_cloud", RunID: 77, Attempt: 2, ManifestSHA: publication.selection.ManifestSHA}}
	previous := map[string]string{"UNRELATED": "keep"}
	if admission != nil {
		previous = appendMap(previous, admission.operator)
	}
	selection := map[string]any{"environment": "dev", "images": map[string]any{"LKE_OPENBAO_PKI_IMAGE": target}}
	selected, _ := json.Marshal(selection)
	current := appendMap(previous, map[string]string{"DEPLOYMENT_IMAGES": string(selected)})
	receipt, _ := json.Marshal(map[string]any{"previous_selection": "", "selection": selection, "differences": []any{map[string]any{"role": "LKE_OPENBAO_PKI_IMAGE", "current": oldRef, "latest": target, "run_url": "https://github.com/example"}}})
	if err := store.write("deployment/version-updates/test.json", receipt, false); err != nil {
		t.Fatal(err)
	}
	for key, value := range current {
		if admission != nil {
			if old, exists := admission.operator[key]; exists {
				if old != value {
					t.Fatal("joined original operator changed")
				}
				continue
			}
		}
		if err := store.write(filepath.Join("operator/env", key), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	parent := environmentCreatePlan{Environment: "dev", Stack: old.Journal.Stack, ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: deploymentplan.Digest(previous), Bootstrap: environmentCreateBootstrapPlan{Image: apiImage.Reference}, Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: oldRef}, "LKE_VIDEO_CLOUD_IMAGE": apiImage}}}
	if admission != nil {
		// All original output declarations enter admission before any native stage.
		parent = admitted
		parent.OperatorSHA256 = deploymentplan.Digest(previous)
		parent.Bootstrap = admission.bootstrap
		parent.Bootstrap.Image = apiImage.Reference
		parent.Bootstrap.Images = map[string]string{}
		parent.Release.Images = map[string]environmentReleaseImage{}
		for role, ref := range admission.bootstrap.Images {
			parent.Bootstrap.Images[role] = ref
			parent.Release.Images[role] = environmentReleaseImage{Reference: ref}
		}
		parent.Bootstrap.Images["LKE_VIDEO_CLOUD_IMAGE"] = apiImage.Reference
		parent.Bootstrap.Images["LKE_OPENBAO_PKI_IMAGE"] = oldRef
		parent.Release.Images["LKE_VIDEO_CLOUD_IMAGE"] = apiImage
		parent.Release.Images["LKE_OPENBAO_PKI_IMAGE"] = environmentReleaseImage{Reference: oldRef}
	}
	e, _ := providerExecutionContractFork(t, old, parent, "isolated-unified-session")
	e.originalPlanSHA = deploymentplan.Digest(parent)
	e.versionState = &createOperatorVersionState{}
	_, image, err := verifyCreateEnvironmentOperator(t.Context(), cfg, store, e, current, &parent, publication.command(t))
	if err != nil {
		t.Fatal("synthetic publication", err)
	}
	selectionSHA, manifestSHA, ok := createOperatorVersionEvidence(e, current)
	if !ok {
		t.Fatal("missing verified synthetic publication")
	}
	ns := e.Journal.Stack + "-secrets"
	setID := deploymentplan.ID(set)
	podID := "Pod/" + ns + "/openbao-0"
	podUID := "original-pod"
	if useNative {
		podUID = "22222222-aaaa-4aaa-8aaa-222222222222"
	}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": podUID, "resourceVersion": "3", "labels": deploymentplan.Object{"controller-revision-hash": "old-revision"}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "controller": true, "kind": "StatefulSet", "name": "openbao", "uid": meta["uid"]}}}, "spec": deploymentplan.Clone(environmentPKIPod(set)), "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "provider-id"}, deploymentplan.Object{"name": "openbao-pki", "ready": true, "containerID": "old-host", "imageID": oldRef}}, "initContainerStatuses": []any{deploymentplan.Object{"name": "openbao-pki-install", "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}}}
	// The StatefulSet controller adds claim-template PVC volumes to the Pod.
	actualSpec := deploymentplan.Map(pod["spec"])
	actualVolumes := certIssuerObjectList(actualSpec["volumes"])
	for _, claim := range certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"]) {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(claim)["metadata"])["name"])
		actualVolumes = append(actualVolumes, environmentCreateServiceVolume(name, "persistentVolumeClaim", name+"-openbao-0"))
	}
	actualSpec["volumes"] = actualVolumes
	nativeObjects := map[string]deploymentplan.Object{setID: set, podID: pod}
	var originalConfig deploymentplan.Object
	if admission != nil {
		// This is the same provider configuration supplied with the original
		// StatefulSet, retained before any source or session admission.
		originalConfig = deploymentplan.Clone(objects[1])
		configMeta := deploymentplan.Map(originalConfig["metadata"])
		configMeta["uid"] = "33333333-aaaa-4aaa-8aaa-333333333333"
		configMeta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": parent.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
		nativeObjects[deploymentplan.ID(originalConfig)] = originalConfig
	}
	if useNative {
		for index, suffix := range []string{"-secrets", "-video-cloud", "-platform"} {
			name := e.Journal.Stack + suffix
			nativeObjects["Namespace//"+name] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": name, "uid": fmt.Sprintf("%08d-aaaa-4aaa-8aaa-111111111111", index+1), "resourceVersion": "1"}}
		}
	}
	native, dir, calls := unifiedSessionNativeBackend(t, store, nativeObjects, nil)
	native.RolloutTimeout = time.Second
	installOpenBaoScopedFakePatch(t, dir)
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	fault := &sessionNativeFaultBackend{environmentCreateKubernetesBackend: backend}
	sourceBackend := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{deploymentplan.ID(set): deploymentplan.Clone(set)}, writes: map[string]int{}}}
	definitions := []deploymentplan.Definition{{Object: set, Phase: "workload"}}
	sourceInventory := []deploymentplan.Object{set}
	if originalConfig != nil {
		definitions = append(definitions, deploymentplan.Definition{Object: originalConfig, Phase: "configuration", Retain: true})
		sourceInventory = append(sourceInventory, originalConfig)
		sourceBackend.objects[deploymentplan.ID(originalConfig)] = deploymentplan.Clone(originalConfig)
	}
	source, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: deploymentplan.Digest("source")}, definitions, sourceInventory)
	if err != nil {
		t.Fatal(err)
	}
	var stageBackend deploymentplan.Backend = sourceBackend
	if useNative {
		stageBackend = fault
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, stageBackend, store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if useNative {
			return fault.CheckParentExecution(ctx, p, store.Root)
		}
		return sourceBackend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, stageBackend) }
	if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "original-openbao", source, stageBackend, qualify, fence, accept, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal("original OpenBao source", err)
	}
	// Freeze the synthetic initial OpenBao identity before the producer is
	// qualified. All bytes and ownership belong only to this temporary store.
	initialization := []byte(`{"root_token":"isolated-root-token","unseal_keys_b64":["aXNvbGF0ZWQtdW5zZWFsLWtleQ=="]}`)
	for path, raw := range map[string][]byte{
		"openbao/init.json":  initialization,
		"openbao/root-token": []byte("isolated-root-token"),
		"openbao/unseal-key": []byte("aXNvbGF0ZWQtdW5zZWFsLWtleQ=="),
	} {
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	const initialKey = "openbao-initialize"
	if err := e.BeginEntry(initialKey, "openbao", mustProviderRecoveryJSON(map[string]string{"fixture": "original"}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	initialReceipt := map[string]string{"init_sha256": environmentPreflightBytesSHA256(initialization), "pod_uid": podUID, "statefulset_uid": deploymentplan.String(meta["uid"]), "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"}
	if err := e.RecordReceipt(initialKey, mustProviderRecoveryJSON(initialReceipt)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(initialKey, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	expectedPodUID := podUID
	if beforeProducer != nil {
		beforeProducer(unifiedOpenBaoParentFixture{cfg: cfg, store: store, execution: e, plan: parent, native: native, nativeDir: dir, fault: fault, backend: sourceBackend, calls: calls}, source)
		retainedPod, err := native.Read(t.Context(), podID)
		if err != nil || retainedPod == nil {
			t.Fatal("pre-handoff retained Pod unavailable", err)
		}
		expectedPodUID = deploymentplan.String(deploymentplan.Map(retainedPod["metadata"])["uid"])
	}
	prepare := func(ctx context.Context, environment string) (*openBaoInPlacePrepared, error) {
		if environment != "dev" {
			return nil, fmt.Errorf("unexpected isolated environment")
		}
		_, actual, err := readEnvironmentCreateOperator(cfg)
		if err != nil {
			return nil, err
		}
		_, verified, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, actual, &parent, publication.command(t))
		if err != nil {
			return nil, err
		}
		s, m, ok := createOperatorVersionEvidence(e, actual)
		if !ok || s != selectionSHA || m != manifestSHA || verified.Reference != image.Reference {
			return nil, fmt.Errorf("isolated publication changed")
		}
		return &openBaoInPlacePrepared{cfg: cfg, store: store, execution: e, image: verified, selection: s, manifest: m, native: native, backend: backend, cleanup: func() {}}, nil
	}
	if err := runCreateOpenBaoInPlaceWith([]string{"--environment", "dev"}, prepare); err != nil {
		t.Fatal("scoped producer preflight", err)
	}
	files, err := filepath.Glob(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "openbao-inplace", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("scoped producer qualification missing", err)
	}
	review := strings.TrimSuffix(filepath.Base(files[0]), ".json")
	if err := runCreateOpenBaoInPlaceWith([]string{"--environment", "dev", "--execute", "--plan-sha256", review, "--timeout", "30s"}, prepare); err != nil {
		t.Fatal("scoped producer execute", err)
	}
	owner, err := verifyOpenBaoInPlaceReady(t.Context(), cfg, store, e, native)
	if err != nil || owner.PodUID != expectedPodUID || owner.ContainerID != "new-host" {
		t.Fatal("native accepted OpenBao owner", err)
	}
	return unifiedOpenBaoParentFixture{cfg: cfg, store: store, execution: e, plan: parent, native: native, nativeDir: dir, fault: fault, backend: sourceBackend, owner: owner, calls: calls}
}

func TestUnifiedOpenBaoParentProducerUsesSameFencedCreate(t *testing.T) {
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	f := newUnifiedOpenBaoParentFixture(t, image)
	if f.execution.Journal.PlanSHA256 == "" || f.execution.Store != f.store || f.plan.Bootstrap.Image != image.Reference || f.plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference != image.Reference {
		t.Fatal("source and producer did not retain the same parent and API image")
	}
	if err := f.execution.Check(t.Context()); err != nil {
		t.Fatal("original parent fence", err)
	}
	owner, err := verifyOpenBaoInPlaceReady(t.Context(), f.cfg, f.store, f.execution, f.native)
	if err != nil || owner.PodUID != f.owner.PodUID || owner.StatefulSetUID != f.owner.StatefulSetUID || owner.ContainerID != f.owner.ContainerID {
		t.Fatal("producer receipt cannot be reverified on original owner", err)
	}
}

func TestUnifiedOpenBaoParentNativeSource(t *testing.T) {
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	f := newUnifiedOpenBaoParentFixture(t, image, true)
	if f.fault == nil || f.nativeDir == "" {
		t.Fatal("single native Kubernetes backend unavailable")
	}
	if _, err := verifyOpenBaoInPlaceReady(t.Context(), f.cfg, f.store, f.execution, f.native); err != nil {
		t.Fatal("same native producer receipt", err)
	}
}
