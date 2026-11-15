package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func isolatedProofRedisVolumeSnapshot(kubeDir, namespace string) (map[string]string, error) {
	if !isolatedScriptTempPath(kubeDir) || namespace == "" || filepath.Base(namespace) != namespace {
		return nil, errors.New("isolated volume identity required")
	}
	claimPath := filepath.Join(kubeDir, "PersistentVolumeClaim_"+namespace+"_redis-data.json")
	if _, err := os.Lstat(claimPath); errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	} else if err != nil {
		return nil, errors.New("isolated original claim cannot be inspected")
	}
	raw, err := readBoundedEnvironmentFile(claimPath, true)
	var claim deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &claim) != nil || claim["kind"] != "PersistentVolumeClaim" {
		return nil, errors.New("isolated original claim unreadable")
	}
	meta, spec := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(claim["spec"])
	name := deploymentplan.String(spec["volumeName"])
	uid := deploymentplan.String(meta["uid"])
	if meta["name"] != "redis-data" || meta["namespace"] != namespace || uid == "" || name == "" || filepath.Base(name) != name || deploymentplan.Map(claim["status"])["phase"] != "Bound" {
		return nil, errors.New("isolated original claim not bound")
	}
	volumePath := filepath.Join(kubeDir, "PersistentVolume__"+name+".json")
	volumeRaw, err := readBoundedEnvironmentFile(volumePath, true)
	var volume deploymentplan.Object
	if err != nil || json.Unmarshal(volumeRaw, &volume) != nil || volume["kind"] != "PersistentVolume" {
		return nil, errors.New("isolated original volume unreadable")
	}
	ref := deploymentplan.Map(deploymentplan.Map(volume["spec"])["claimRef"])
	if deploymentplan.Map(volume["metadata"])["name"] != name || ref["name"] != "redis-data" || ref["namespace"] != namespace || ref["uid"] != uid {
		return nil, errors.New("isolated original volume claim differs")
	}
	return map[string]string{"claim": environmentPreflightBytesSHA256(raw), "volume": environmentPreflightBytesSHA256(volumeRaw)}, nil
}

type isolatedProofWorkloadEvidence struct {
	digest string
	info   os.FileInfo
}

func isolatedProofWorkloadSnapshot(kubeDir, stack string) (map[string]isolatedProofWorkloadEvidence, error) {
	if !isolatedScriptTempPath(kubeDir) || stack == "" || filepath.Base(stack) != stack {
		return nil, errors.New("isolated workload inventory required")
	}
	entries, err := os.ReadDir(kubeDir)
	if err != nil {
		return nil, err
	}
	out := map[string]isolatedProofWorkloadEvidence{}
	for _, entry := range entries {
		name := entry.Name()
		selected := strings.HasPrefix(name, "PersistentVolume__")
		for _, kind := range []string{"Pod", "ReplicaSet", "StatefulSet", "Deployment", "PersistentVolumeClaim"} {
			for _, namespace := range []string{stack + "-secrets", stack + "-platform"} {
				selected = selected || strings.HasPrefix(name, kind+"_"+namespace+"_")
			}
		}
		if !selected || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(kubeDir, name)
		prior, err := os.Lstat(path)
		if err != nil {
			return nil, errors.New("isolated workload owner unavailable")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			return nil, errors.New("isolated workload observation unreadable")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(prior, info) || !prior.ModTime().Equal(info.ModTime()) {
			return nil, errors.New("isolated workload owner changed")
		}
		out[name] = isolatedProofWorkloadEvidence{digest: environmentPreflightBytesSHA256(raw), info: info}
	}
	return out, nil
}

func sameIsolatedProofWorkloads(a, b map[string]isolatedProofWorkloadEvidence) bool {
	if len(a) != len(b) {
		return false
	}
	for name, before := range a {
		after, ok := b[name]
		if !ok || before.digest != after.digest || !os.SameFile(before.info, after.info) || !before.info.ModTime().Equal(after.info.ModTime()) {
			return false
		}
	}
	return true
}

func setIsolatedProofRolloutTimeout(cfg deploymentConfig, backend *environmentKubernetesBackend) error {
	value, err := environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	backend.RolloutTimeout = value
	return nil
}

func TestIsolatedProofUsesEnvironmentRolloutTimeout(t *testing.T) {
	cfg := deploymentConfig{Values: map[string]string{"DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS": "37", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "60"}}
	var backend environmentKubernetesBackend
	if err := setIsolatedProofRolloutTimeout(cfg, &backend); err != nil || backend.RolloutTimeout != 37*time.Second {
		t.Fatal("selected rollout timeout not used", err)
	}
	delete(cfg.Values, "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS")
	if err := setIsolatedProofRolloutTimeout(cfg, &backend); err == nil {
		t.Fatal("missing rollout timeout accepted")
	}
}

func TestIsolatedProofRedisVolumeSnapshotRejectsForeignClaim(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ns := "video-cloud-dev-platform"
	claimPath := filepath.Join(dir, "PersistentVolumeClaim_"+ns+"_redis-data.json")
	volumePath := filepath.Join(dir, "PersistentVolume__isolated-pv.json")
	claim := deploymentplan.Object{"kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "redis-data", "namespace": ns, "uid": "original-uid"}, "spec": deploymentplan.Object{"volumeName": "isolated-pv"}, "status": deploymentplan.Object{"phase": "Bound"}}
	volume := deploymentplan.Object{"kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "isolated-pv"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": "redis-data", "namespace": ns, "uid": "original-uid"}}}
	if err := os.WriteFile(claimPath, mustProviderRecoveryJSON(claim), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(volumePath, mustProviderRecoveryJSON(volume), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := isolatedProofRedisVolumeSnapshot(dir, ns)
	if err != nil || len(before) != 2 {
		t.Fatal("original bound claim unavailable", err)
	}
	after, err := isolatedProofRedisVolumeSnapshot(dir, ns)
	if err != nil || deploymentplan.Digest(before) != deploymentplan.Digest(after) {
		t.Fatal("read-only snapshot changed evidence", err)
	}
	deploymentplan.Map(deploymentplan.Map(volume["spec"])["claimRef"])["uid"] = "foreign"
	if err := os.WriteFile(volumePath, mustProviderRecoveryJSON(volume), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedProofRedisVolumeSnapshot(dir, ns); err == nil {
		t.Fatal("foreign volume claim accepted")
	}
}

func TestIsolatedProofWorkloadSnapshotDetectsMutationAndPresence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Pod_video-cloud-dev-secrets_openbao-0.json")
	if err := os.WriteFile(path, []byte(`{"kind":"Pod"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := isolatedProofWorkloadSnapshot(dir, "video-cloud-dev")
	if err != nil || len(before) != 1 {
		t.Fatal("original workload snapshot unavailable", err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte(`{"kind":"Pod"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := isolatedProofWorkloadSnapshot(dir, "video-cloud-dev")
	if err != nil || sameIsolatedProofWorkloads(before, after) {
		t.Fatal("same-content owner replacement was not detected", err)
	}
	before = after
	second := filepath.Join(dir, "PersistentVolume__isolated.json")
	if err := os.WriteFile(second, []byte(`{"kind":"PersistentVolume"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err = isolatedProofWorkloadSnapshot(dir, "video-cloud-dev")
	if err != nil || sameIsolatedProofWorkloads(before, after) {
		t.Fatal("added volume was not detected", err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"kind":"Pod","changed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err = isolatedProofWorkloadSnapshot(dir, "video-cloud-dev")
	if err != nil || sameIsolatedProofWorkloads(before, after) {
		t.Fatal("changed owner was not detected", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	after, err = isolatedProofWorkloadSnapshot(dir, "video-cloud-dev")
	if err != nil || sameIsolatedProofWorkloads(before, after) {
		t.Fatal("removed owner was not detected", err)
	}
}

func isolatedProofBridgeMatches(selected, original map[string]string, upstream, kubeDir string, foundation *isolatedOpenBaoFirstController, job *isolatedMigrationController) bool {
	if !isolatedScriptTempPath(upstream) || filepath.Base(upstream) != "kubectl" || !isolatedScriptTempPath(kubeDir) {
		return false
	}
	for key, value := range original {
		if selected[key] != value {
			return false
		}
	}
	if job == nil {
		for key := range selected {
			if strings.HasPrefix(key, "RTK_TEST_JOB_") {
				return false
			}
		}
		return selected["RTK_CLOUD_KUBECTL"] == upstream
	}
	shim := filepath.Join(job.root, "kubectl")
	rootInfo, rootErr := os.Lstat(job.root)
	info, err := os.Lstat(shim)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0700 || rootInfo.Mode()&os.ModeSymlink != 0 || err != nil || !isolatedScriptTempPath(shim) || info.Mode().Perm() != 0700 || !info.Mode().IsRegular() || foundation == nil || job.listener == nil || !strings.HasPrefix(job.listener.Addr().String(), "127.0.0.1:") || job.objects != kubeDir || job.network == "" || job.network != foundation.foundationNetwork || job.owner == "" || job.owner != foundation.foundationOwner || job.stack != foundation.cfg.Values["CLOUD_STACK_NAME"] {
		return false
	}
	expected := map[string]string{"RTK_CLOUD_KUBECTL": shim, "RTK_TEST_JOB_FALLBACK": upstream, "RTK_TEST_JOB_KUBECONFIG": original["RTK_FRESH_EXPECTED_KUBECONFIG"], "RTK_TEST_JOB_BRIDGE": job.listener.Addr().String(), "RTK_TEST_JOB_CAPABILITY": job.capability, "RTK_TEST_JOB_PLATFORM_NAMESPACE": job.stack + "-platform"}
	if expected["RTK_TEST_JOB_KUBECONFIG"] == "" || expected["RTK_TEST_JOB_CAPABILITY"] == "" {
		return false
	}
	for key, value := range expected {
		if selected[key] != value {
			return false
		}
	}
	for key := range selected {
		if strings.HasPrefix(key, "RTK_TEST_JOB_") {
			if _, ok := expected[key]; !ok {
				return false
			}
		}
	}
	return true
}

// This hook consumes only a checkpoint genuinely produced by the immediately
// preceding isolated create. It never manufactures a parent, journal or ACK.
func runIsolatedOpenBaoFirstCheckpointProof(t *testing.T, store secretStore, cfg deploymentConfig, env []string, provider *isolatedFreshProvider, original map[string]string, upstream, kubeDir string, foundation *isolatedOpenBaoFirstController, job *isolatedMigrationController, profile, workspace, environmentRoot, createOutput string) {
	t.Helper()
	if os.Getenv("RTK_TEST_OPENBAO_FIRST_PROOF") != "1" {
		return
	}
	stopRequested := os.Getenv(isolatedCustodyStopEnv) == "1"
	if stopRequested && !strings.Contains(createOutput, isolatedCustodyStopMessage) {
		t.Fatal("isolated create did not reach the before-custody interruption")
	}
	if os.Getenv("RTK_TEST_FRESH_CREATE") != "1" || os.Getenv("RTK_TEST_OPENBAO_FIRST") != "1" || cfg.Environment != "dev" || store.Environment != cfg.Environment || !isolatedScriptTempPath(store.Root) || provider == nil || provider.server == nil || !strings.HasPrefix(provider.server.URL, "http://127.0.0.1:") {
		t.Fatal("same-parent checkpoint test requires the isolated original fixture")
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		if stopRequested {
			t.Fatal("before-custody original checkpoint unavailable")
		}
		t.Log("same-parent checkpoint evidence not reached: original checkpoint unavailable")
		return
	}
	for _, key := range []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config", "management-transport-material", "account-signer-material"} {
		entry := createOpenBaoACKCompletionEntry(checkpoint.Journal, key)
		if entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			if stopRequested {
				t.Fatal("before-custody original initialization or material incomplete")
			}
			t.Log("same-parent checkpoint evidence not reached: original initialization or material incomplete")
			return
		}
	}
	if checkpoint.Profile != "openbao-first-foundation" {
		t.Fatal("completed original evidence has no supported early checkpoint")
	}
	if stopRequested {
		entry := createOpenBaoACKCompletionEntry(checkpoint.Journal, "kubernetes/foundation-services")
		if entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || len(checkpoint.Journal.Entries) == 0 || checkpoint.Journal.Entries[len(checkpoint.Journal.Entries)-1].Key != entry.Key {
			t.Fatal("original accepted foundation-services is not the exact before-custody boundary")
		}
	}
	selected := map[string]string{}
	for _, assignment := range env {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			continue
		}
		if key == "RTK_CLOUD_KUBECTL" || key == "RTK_CLOUD_LINODE_API_ROOT" || key == "RTK_CLOUD_TEST_MODE" || strings.HasPrefix(key, "RTK_FIRST_BAO_") || strings.HasPrefix(key, "RTK_UNIFIED_") || strings.HasPrefix(key, "RTK_LATE_NATIVE_") || strings.HasPrefix(key, "RTK_FRESH_") || strings.HasPrefix(key, "RTK_TEST_JOB_") {
			if _, exists := selected[key]; exists {
				t.Fatal("ambiguous isolated checkpoint fixture environment")
			}
			selected[key] = value
		}
	}
	if selected["RTK_CLOUD_LINODE_API_ROOT"] != provider.server.URL || selected["RTK_CLOUD_TEST_MODE"] != "1" || !isolatedProofBridgeMatches(selected, original, upstream, kubeDir, foundation, job) {
		t.Fatal("checkpoint provider or Kubernetes bridge is not the same isolated fixture")
	}
	if _, exists := selected["RTK_FIRST_BAO_READ_ONLY"]; exists {
		t.Fatal("create unexpectedly selected proof-only bridge mode")
	}
	if job != nil {
		network, owner, err := foundation.ensureIsolatedFoundationNetwork(t.Context())
		if err != nil || network != job.network || owner != job.owner {
			t.Fatal("original isolated foundation network changed")
		}
	}
	volumeBefore, err := isolatedProofRedisVolumeSnapshot(kubeDir, cfg.Values["CLOUD_STACK_NAME"]+"-platform")
	if err != nil {
		t.Fatal("original simulated Redis volume cannot be inspected", err)
	}
	workloadsBefore, err := isolatedProofWorkloadSnapshot(kubeDir, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		t.Fatal("original simulated workload inventory cannot be inspected", err)
	}
	for key, value := range selected {
		t.Setenv(key, value)
	}
	// The in-process proof may observe existing simulated storage but may not
	// ask the fixture controller to manufacture or rewrite an owner.
	t.Setenv("RTK_FIRST_BAO_READ_ONLY", "1")
	selectedStore, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil || selectedStore != store {
		t.Fatal("original isolated operator unavailable", err)
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		t.Fatal("original isolated kubeconfig unavailable", err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil || backend.Store != store {
		t.Fatal("original isolated Kubernetes identity unavailable", err)
	}
	if err := setIsolatedProofRolloutTimeout(cfg, &backend); err != nil {
		t.Fatal("original environment rollout timeout unavailable", err)
	}
	e, err := openCreateExecutionForPublication(t.Context(), store, checkpoint)
	if err != nil {
		t.Fatal("original isolated parent lock unavailable", err)
	}
	defer e.Close()
	before := deploymentplan.Digest(e.Journal)
	material := map[string]string{}
	paths := map[string]string{"init": "openbao/init.json", "unseal": "openbao/unseal-key", "root": "openbao/root-token"}
	for _, key := range []string{"management-transport-material", "account-signer-material"} {
		_, selectedPaths, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, key)
		if err != nil {
			t.Fatal("original material selection unavailable", err)
		}
		for name, path := range selectedPaths {
			paths[key+"/"+name] = path
		}
	}
	for name, relative := range paths {
		path, err := store.safePath(relative)
		if err != nil {
			t.Fatal("original material path unavailable", err)
		}
		value, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			t.Fatal("original material unavailable", name)
		}
		material[name] = environmentPreflightBytesSHA256(value)
	}
	proof, sha, err := publishOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, operator)
	if err != nil {
		t.Fatal("actual same-parent checkpoint publication failed", err)
	}
	loaded, err := loadOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, operator, sha)
	if err != nil || deploymentplan.Digest(proof) != deploymentplan.Digest(loaded) || proof.Qualification.ParentJournalSHA256 != before {
		t.Fatal("actual same-parent checkpoint readback failed", err)
	}
	foreign := map[string]string{}
	for key, value := range operator {
		foreign[key] = value
	}
	foreign["LINODE_TOKEN"] = "isolated-foreign-token"
	if _, _, err := publishOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, foreign); err == nil {
		t.Fatal("changed operator published checkpoint evidence")
	}
	if _, err := loadOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, operator, sha); err == nil {
		t.Fatal("invalidated checkpoint evidence accepted")
	}
	if stopRequested {
		proof, sha, err = publishOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, operator)
		if err != nil {
			t.Fatal("requalification after invalidation failed", err)
		}
		loaded, err = loadOpenBaoFirstCheckpointProof(t.Context(), cfg, store, e, backend, operator, sha)
		if err != nil || deploymentplan.Digest(proof) != deploymentplan.Digest(loaded) {
			t.Fatal("requalified checkpoint unavailable", err)
		}
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("checkpoint test changed original journal")
	}
	volumeAfter, err := isolatedProofRedisVolumeSnapshot(kubeDir, cfg.Values["CLOUD_STACK_NAME"]+"-platform")
	if err != nil || deploymentplan.Digest(volumeBefore) != deploymentplan.Digest(volumeAfter) {
		t.Fatal("checkpoint readback changed original simulated Redis PVC/PV", err)
	}
	workloadsAfter, err := isolatedProofWorkloadSnapshot(kubeDir, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil || !sameIsolatedProofWorkloads(workloadsBefore, workloadsAfter) {
		t.Fatal("checkpoint readback changed original simulated workload inventory", err)
	}
	for name, relative := range paths {
		path, _ := store.safePath(relative)
		value, err := readBoundedEnvironmentFile(path, true)
		if err != nil || material[name] != environmentPreflightBytesSHA256(value) {
			t.Fatal("checkpoint test changed original material", name)
		}
	}
	t.Log("same-parent checkpoint evidence publication, readback and invalidation passed; this is not a complete preflight GO")
	if stopRequested {
		if err := e.Close(); err != nil {
			t.Fatal("original parent lock could not be released", err)
		}
		resumeErr := runIsolatedOpenBaoFirstResumeSubprocess(t, env, profile, workspace, environmentRoot, store.ConfigRoot, e.Journal.PlanSHA256, sha, proof.CheckerSHA256)
		for name, relative := range paths {
			path, _ := store.safePath(relative)
			value, err := readBoundedEnvironmentFile(path, true)
			if err != nil || material[name] != environmentPreflightBytesSHA256(value) {
				t.Fatal("resume changed original material", name)
			}
		}
		if resumeErr != nil {
			if job != nil {
				requests, authenticated, eligible, executed, succeeded := job.safeProgress()
				t.Log("isolated migration controller counts:", requests, authenticated, eligible, executed, succeeded)
			}
			t.Fatal("isolated same-parent resume did not complete; inspect its private log", resumeErr)
		}
	}
}
