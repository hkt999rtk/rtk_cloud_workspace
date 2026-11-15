package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func ciOpenBaoRequireBlock(t *testing.T, err error, cause string) {
	t.Helper()
	var block *deploymentplan.Blocker
	if !errors.As(err, &block) || block.ID != "deployment.create.recovery" || !strings.Contains(block.Cause, cause) {
		t.Fatalf("want recovery blocker %q, got %v", cause, err)
	}
}

func ciOpenBaoAssertReadOnly(t *testing.T, store secretStore, before map[string]string) {
	t.Helper()
	after := createBootstrapStateSnapshot(t, store.Root)
	var changes []string
	for path, value := range before {
		if after[path] != value {
			changes = append(changes, "changed:"+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changes = append(changes, "added:"+path)
		}
	}
	if len(changes) != 0 {
		t.Fatal("original store changed", changes)
	}
}

func ciOpenBaoClone[T any](t *testing.T, value T) T {
	t.Helper()
	var copy T
	if err := json.Unmarshal(mustProviderRecoveryJSON(value), &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func ciOpenBaoDeployment(stack, name string) deploymentplan.Object {
	container := deploymentplan.Object{
		"name": "app", "image": "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64),
		"env":          environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao"}),
		"volumeMounts": []any{environmentCreateServiceMount("pki-custody-auth", "/run/pki-custody-auth", true)},
	}
	token := deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"audience": "openbao", "path": "openbao.jwt", "expirationSeconds": float64(900)}}
	volume := deploymentplan.Object{"name": "pki-custody-auth", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{token}}}
	pod := deploymentplan.Object{"containers": []any{container}, "volumes": []any{volume}}
	return deploymentplan.Object{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": deploymentplan.Object{"name": name, "namespace": stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}},
		"spec":     deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": pod}},
	}
}

// Record a real stopped child through the normal executor; no accepted receipt
// is invented for a readiness failure.
func ciOpenBaoRunStage(t *testing.T, store secretStore, e *environmentCreateExecution, in deploymentplan.Inputs, b freshStageBackend, stage string, defs []deploymentplan.Definition, stop bool) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	var live []deploymentplan.Object
	for _, o := range b.objects {
		live = append(live, deploymentplan.Clone(o))
	}
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	p, err := deploymentplan.Compile(in, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if b.leases < len(e.Journal.KubernetesLeases) {
		b.leases = len(e.Journal.KubernetesLeases)
	}
	b.failure = ""
	b.failed = false
	if stop {
		b.failure = "after-write-workload"
	}
	err = executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }, func(context.Context) error { return nil }, io.Discard)
	if stop && err == nil || !stop && err != nil {
		t.Fatalf("stage stop=%t: %v", stop, err)
	}
	stageErr := err
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || j.Accepted == stop {
		t.Fatalf("real child journal: exists=%t accepted=%t err=%v stageError=%v", exists, j.Accepted, err, stageErr)
	}
	return p, j
}

func TestCIOpenBaoScopedCommandsRejectBeforeEnvironmentAccess(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	for _, cmd := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"custody-controller", runCreateCustodyControllerRecovery, []string{"--environment=dev", "--parent-plan-sha256=" + strings.Repeat("a", 64), "--confirm=foreign"}},
		{"managed-unseal", runCreateManagedOpenBaoUnseal, []string{"--environment=dev", "--parent-plan-sha256=" + strings.Repeat("a", 64), "--confirm=foreign"}},
		{"public-ca", runCreateOpenBaoPublicCAHandoff, []string{"--environment=dev", "--timeout=0"}},
		{"public-crl", runCreateOpenBaoPublicCRLRecovery, []string{"--environment=dev", "--action=create", "--confirm=video-cloud-dev"}},
	} {
		t.Run(cmd.name, func(t *testing.T) { ciOpenBaoRequireBlock(t, cmd.run(cmd.args), "") })
	}
}

// These commands open the real original owner from its canonical private store.
// An absent later checkpoint must not be promoted merely because foundation ran.
func TestCIOpenBaoPublicCommandsPreserveEarlierOwner(t *testing.T) {
	f, workspace, _, requests, mu := openBaoCommandContractFixture(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	for _, cmd := range []struct {
		name, cause string
		run         func([]string) error
		args        []string
	}{
		{"custody-controller", "original temporary transport continuation", runCreateCustodyControllerRecovery, []string{"--environment=dev", "--parent-plan-sha256=" + f.sha, "--confirm=video-cloud-dev"}},
		{"managed-unseal", "original managed host stage", runCreateManagedOpenBaoUnseal, []string{"--environment=dev", "--parent-plan-sha256=" + f.sha, "--confirm=video-cloud-dev"}},
		{"public-ca", "latest recorded original owner", runCreateOpenBaoPublicCAHandoff, []string{"--environment=dev"}},
		{"public-crl", "original-failed-core-input", runCreateOpenBaoPublicCRLRecovery, []string{"--environment=dev"}},
	} {
		t.Run(cmd.name, func(t *testing.T) { ciOpenBaoRequireBlock(t, cmd.run(cmd.args), cmd.cause) })
	}
	_, prepareErr := prepareCreateOpenBaoInPlace(t.Context(), "dev")
	ciOpenBaoRequireBlock(t, prepareErr, "authenticated new OpenBao host image selection")
	ciOpenBaoAssertReadOnly(t, f.store, before)
	mu.Lock()
	defer mu.Unlock()
	if len(*requests) != 0 {
		t.Fatal("earlier checkpoint refusal contacted provider", *requests)
	}
}

func TestCIOpenBaoCustodyControllerRestorationAuthenticatesOriginalWrite(t *testing.T) {
	store, e, in, b := freshStageFixture(t)
	original := ciOpenBaoDeployment(e.Journal.Stack, "pki-controller")
	deploymentplan.Map(original["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	deploymentplan.Map(original["metadata"])["resourceVersion"] = "original-controller"
	id := deploymentplan.ID(original)
	b.objects[id] = deploymentplan.Clone(original)
	bad := deploymentplan.Clone(original)
	environmentPKIPod(bad)["volumes"] = []any{deploymentplan.Object{"name": "legacy-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "pki-controller-state"}}}
	p, j := ciOpenBaoRunStage(t, store, e, in.Inputs, b, "transport-enrollment-issuer", []deploymentplan.Definition{{Object: bad, Phase: "workload"}}, true)
	before := createBootstrapStateSnapshot(t, store.Root)
	_, err := interruptedCustodyControllerStage(store, e)
	ciOpenBaoRequireBlock(t, err, "restoration has not completed")
	ciOpenBaoAssertReadOnly(t, store, before)
	r := p.Resources[0]
	intent := custodyControllerRestoreIntent(p, r)
	key := "custody-controller-restore-" + deploymentplan.Digest(intent)
	// Model the native repair itself, then record only its actual same-owner result.
	repaired := deploymentplan.Clone(b.objects[id])
	deploymentplan.Map(repaired["spec"])["template"] = deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(r.Baseline["spec"])["template"]))
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(t.Context(), "update", repaired, false); err != nil {
		t.Fatal(err)
	}
	if !deploymentplan.Matches(r.Baseline, b.objects[id]) {
		t.Fatal("repair did not restore native original template")
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	selected, err := interruptedCustodyControllerStage(store, e)
	if err != nil || selected.SHA256() != p.SHA256() {
		t.Fatal("completed native restoration rejected", err)
	}
	entry := *createOpenBaoACKCompletionEntry(e.Journal, key)
	got, err := completedRecordedCustodyControllerRestore(store, e, entry)
	if err != nil || got.UID != j.Owners[id] || !deploymentplan.Matches(got.Before, bad) || !deploymentplan.Matches(got.Desired, original) {
		t.Fatal("restoration lost original owner or template", err)
	}
	for _, mode := range []string{"key", "phase", "state", "clock", "intent", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			mut := entry
			switch mode {
			case "key":
				mut.Key += "changed"
			case "phase":
				mut.Phase = "bootstrap"
			case "state":
				mut.State = "allocated"
			case "clock":
				mut.CompletedAt = time.Time{}
			case "intent":
				mut.IntentSHA256 = deploymentplan.Digest("foreign")
			case "receipt":
				mut.Receipt = mustProviderRecoveryJSON(map[string]string{"resource": "foreign"})
			}
			ciOpenBaoRequireBlock(t, func() error { _, err := completedRecordedCustodyControllerRestore(store, e, mut); return err }(), "original native receipt")
		})
	}
	if custodyControllerForwardStage(p) != "transport-enrollment-issuer-custody-forward-"+p.SHA256()[:12] || !createPodHasVolume(environmentPKIPod(original), "pki-custody-auth") || createPodHasPVC(environmentPKIPod(original), "pki-controller-state") || !createPodHasPVC(environmentPKIPod(bad), "pki-controller-state") {
		t.Fatal("controller repair crossed custody boundaries")
	}
}

func ciOpenBaoTokenFixture(t *testing.T) (secretStore, *environmentCreateExecution, serviceSessionRecordedPlan, []deploymentplan.Object, freshStageBackend) {
	t.Helper()
	store, e, in, b := freshStageFixture(t)
	body := ciOpenBaoDeployment(e.Journal.Stack, "factoryenroll")
	p, j := ciOpenBaoRunStage(t, store, e, in.Inputs, b, "ci-custody-runtime", []deploymentplan.Definition{{Object: body, Phase: "workload"}}, true)
	id := p.Resources[0].ID
	ns := e.Journal.Stack + "-video-cloud"
	rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "factory-rs", "namespace": ns, "uid": "22222222-2222-4222-8222-222222222222", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": "factoryenroll", "uid": j.Owners[id], "controller": true}}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "factory-pod", "namespace": ns, "uid": "33333333-3333-4333-8333-333333333333", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "factory-rs", "uid": deploymentplan.Map(rs["metadata"])["uid"], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(body)), "status": deploymentplan.Object{"containerStatuses": []any{deploymentplan.Object{"name": "app", "imageID": "containerd://sha256:" + strings.Repeat("a", 64), "user": deploymentplan.Object{"linux": deploymentplan.Object{"uid": float64(10001), "gid": float64(54321), "supplementalGroups": []any{float64(54321)}}}}}}}
	return store, e, serviceSessionRecordedPlan{Stage: "ci-custody-runtime", Plan: p, Journal: j, SelectedIDs: map[string]bool{id: true}}, []deploymentplan.Object{b.objects[id], rs, pod}, b
}

func TestCIOpenBaoCustodyTokenRejectsAmbiguousNativeIdentity(t *testing.T) {
	_, _, source, live, _ := ciOpenBaoTokenFixture(t)
	for _, mode := range []string{"duplicate-object", "nil-plan", "wrong-journal", "in-flight", "unselected", "duplicate-source", "foreign-owner", "zero-replicas", "existing-group", "other-mode", "not-custody", "explicit-user", "terminating-pod", "foreign-controller", "missing-user", "root-user", "root-group", "conflicting-groups"} {
		t.Run(mode, func(t *testing.T) {
			s := ciOpenBaoClone(t, source)
			objects := ciOpenBaoClone(t, live)
			sources := []serviceSessionRecordedPlan{s}
			wantNil := false
			pod := objects[2]
			switch mode {
			case "duplicate-object":
				objects = append(objects, objects[0])
			case "nil-plan":
				sources[0].Plan = nil
			case "wrong-journal":
				sources[0].Journal.PlanSHA256 = deploymentplan.Digest("foreign")
			case "in-flight":
				sources[0].Journal.InFlight = s.Plan.Resources[0].ID
			case "unselected":
				sources[0].SelectedIDs = map[string]bool{}
				wantNil = true
			case "duplicate-source":
				sources = append(sources, s)
			case "foreign-owner":
				deploymentplan.Map(objects[0]["metadata"])["uid"] = "foreign"
			case "zero-replicas":
				deploymentplan.Map(objects[0]["spec"])["replicas"] = float64(0)
				wantNil = true
				sources[0].Plan.Resources[0].Desired = deploymentplan.Clone(objects[0])
				sources[0].Journal.PlanSHA256 = sources[0].Plan.SHA256()
			case "existing-group":
				environmentPKIPod(objects[0])["securityContext"] = deploymentplan.Object{"fsGroup": float64(54321)}
				wantNil = true
				sources[0].Plan.Resources[0].Desired = deploymentplan.Clone(objects[0])
				sources[0].Journal.PlanSHA256 = sources[0].Plan.SHA256()
			case "other-mode":
				deploymentplan.Map(managedNamedField(environmentPKIPod(objects[0])["volumes"], "pki-custody-auth")["projected"])["defaultMode"] = float64(0400)
				wantNil = true
				sources[0].Plan.Resources[0].Desired = deploymentplan.Clone(objects[0])
				sources[0].Journal.PlanSHA256 = sources[0].Plan.SHA256()
			case "not-custody":
				managedNamedField(environmentPKIPod(objects[0])["containers"], "app")["env"] = environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "file"})
				wantNil = true
				sources[0].Plan.Resources[0].Desired = deploymentplan.Clone(objects[0])
				sources[0].Journal.PlanSHA256 = sources[0].Plan.SHA256()
			case "explicit-user":
				environmentPKIPod(objects[0])["securityContext"] = deploymentplan.Object{"runAsUser": float64(10001)}
				wantNil = true
				sources[0].Plan.Resources[0].Desired = deploymentplan.Clone(objects[0])
				sources[0].Journal.PlanSHA256 = sources[0].Plan.SHA256()
			case "terminating-pod":
				deploymentplan.Map(pod["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
			case "foreign-controller":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(objects[1]["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			case "missing-user":
				managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "app")["user"] = nil
			case "root-user":
				deploymentplan.Map(deploymentplan.Map(managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "app")["user"])["linux"])["uid"] = float64(0)
				wantNil = true
			case "root-group":
				deploymentplan.Map(deploymentplan.Map(managedNamedField(deploymentplan.Map(pod["status"])["containerStatuses"], "app")["user"])["linux"])["supplementalGroups"] = []any{float64(0)}
				wantNil = true
			case "conflicting-groups":
				other := deploymentplan.Clone(pod)
				deploymentplan.Map(other["metadata"])["name"] = "other-pod"
				deploymentplan.Map(other["metadata"])["uid"] = "44444444-4444-4444-8444-444444444444"
				deploymentplan.Map(deploymentplan.Map(managedNamedField(deploymentplan.Map(other["status"])["containerStatuses"], "app")["user"])["linux"])["gid"] = float64(12345)
				objects = append(objects, other)
			}
			before := deploymentplan.Digest(objects)
			p, _, err := compileCustodyTokenAccess(sources, objects)
			if wantNil {
				if err != nil || p != nil {
					t.Fatal("no repair should be needed", err)
				}
			} else {
				ciOpenBaoRequireBlock(t, err, "exact native runtime identity")
			}
			if deploymentplan.Digest(objects) != before {
				t.Fatal("qualification rewrote native observations")
			}
		})
	}
}

func TestCIOpenBaoCustodyTokenProofBindsOnlyRuntimeGroup(t *testing.T) {
	store, e, source, live, _ := ciOpenBaoTokenFixture(t)
	p, facts, err := compileCustodyTokenAccess([]serviceSessionRecordedPlan{source}, live)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	q := custodyTokenAccessProof{Plan: p, Facts: facts}
	for _, mode := range []string{"nil-plan", "no-resources", "no-facts", "action", "owner", "baseline", "kind", "preexisting-group", "uid", "gid", "image", "pod", "container", "image-id", "unrelated-fact", "wrong-image", "unrelated-mutation"} {
		t.Run(mode, func(t *testing.T) {
			proof := ciOpenBaoClone(t, q)
			r := &proof.Plan.Resources[0]
			f := &proof.Facts[0]
			switch mode {
			case "nil-plan":
				proof.Plan = nil
			case "no-resources":
				proof.Plan.Resources = nil
			case "no-facts":
				proof.Facts = nil
			case "action":
				r.Action = "create"
			case "owner":
				r.UID = ""
			case "baseline":
				r.Baseline = nil
			case "kind":
				r.Desired["kind"] = "StatefulSet"
			case "preexisting-group":
				environmentPKIPod(r.Baseline)["securityContext"] = deploymentplan.Object{"fsGroup": float64(54321)}
			case "uid":
				f.UID = 0
			case "gid":
				f.GID = 0
			case "image":
				f.Image = "mutable:latest"
			case "pod":
				f.PodUID = ""
			case "container":
				f.Container = ""
			case "image-id":
				f.ImageID = ""
			case "unrelated-fact":
				f.Resource = "Deployment/other/other"
			case "wrong-image":
				f.Image = "ghcr.io/example/foreign@sha256:" + strings.Repeat("b", 64)
			case "unrelated-mutation":
				deploymentplan.Map(r.Desired["spec"])["replicas"] = float64(2)
			}
			ciOpenBaoRequireBlock(t, validateCustodyTokenAccessDelta(proof), "only the recorded runtime group")
		})
	}
	parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev", Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600"}}
	saved := custodyTokenAccessProof{Schema: "rtk-cloud.custody-token-access/v1", Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Configuration: deploymentplan.Digest(cfg), Plan: p, Facts: facts, Sources: []custodyTokenAccessSource{{Stage: source.Stage, Plan: source.Plan.SHA256(), Journal: deploymentplan.Digest(source.Journal)}}}
	path := filepath.Join(e.Directory, "custody-token-access.json")
	for _, mode := range []string{"shape", "source-plan", "source-journal"} {
		t.Run(mode, func(t *testing.T) {
			proof := ciOpenBaoClone(t, saved)
			cause := ""
			switch mode {
			case "shape":
				proof.Owner = deploymentplan.Digest("foreign")
				cause = "qualification changed"
			case "source-plan":
				proof.Sources[0].Plan = deploymentplan.Digest("foreign")
				cause = "source input changed"
			case "source-journal":
				proof.Sources[0].Journal = deploymentplan.Digest("foreign")
				cause = "source journal changed"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(proof), 0600); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			handled, err := tryUnifiedCustodyTokenAccess(t.Context(), cfg, unifiedDeploymentOptions{Action: "preflight"}, &unifiedDeploymentReport{}, store, e, environmentKubernetesBackend{Store: store}, &parent, io.Discard, io.Discard)
			if !handled {
				t.Fatal("known proof not selected")
			}
			ciOpenBaoRequireBlock(t, err, cause)
			ciOpenBaoAssertReadOnly(t, store, before)
		})
	}
}

func TestCIOpenBaoPublicCRLQualificationArtifactsStayImmutable(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	q := &createOpenBaoPublicCRLQualification{Schema: openBaoPublicCRLSchema, Parent: e.Journal.PlanSHA256, SourceJournal: e.Journal}
	before := createBootstrapStateSnapshot(t, store.Root)
	if got, review, err := readSelectedOpenBaoPublicCRL(store, e); err != nil || got != nil || review != "" {
		t.Fatal("missing selected artifact should remain absent", err)
	}
	ciOpenBaoAssertReadOnly(t, store, before)
	if err := writeOpenBaoPublicCRLQualification(store, q); err != nil {
		t.Fatal(err)
	}
	review := deploymentplan.Digest(q)
	relative := filepath.Join(openBaoPublicCRLBase(q.Parent), review, "qualification.json")
	path := filepath.Join(store.Root, relative)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOpenBaoPublicCRLQualification(store, q); err != nil {
		t.Fatal("same immutable artifact not reusable", err)
	}
	if err := store.write(filepath.Join(openBaoPublicCRLBase(q.Parent), "selected.json"), mustProviderRecoveryJSON(map[string]string{"review_sha256": review}), false); err != nil {
		t.Fatal(err)
	}
	got, sha, err := readSelectedOpenBaoPublicCRL(store, e)
	if err != nil || sha != review || deploymentplan.Digest(got) != review {
		t.Fatal("selected immutable proof did not roundtrip", err)
	}
	for _, mode := range []string{"malformed-pointer", "wrong-digest", "different-bytes", "unsafe-file"} {
		t.Run(mode, func(t *testing.T) {
			pointer := filepath.Join(store.Root, openBaoPublicCRLBase(q.Parent), "selected.json")
			switch mode {
			case "malformed-pointer":
				if err := os.WriteFile(pointer, []byte(`{"review_sha256":"invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
				_, _, err = readSelectedOpenBaoPublicCRL(store, e)
				ciOpenBaoRequireBlock(t, err, "selected-qualification")
			case "wrong-digest":
				changed := *q
				changed.OwnerSHA = "foreign"
				if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pointer, mustProviderRecoveryJSON(map[string]string{"review_sha256": review}), 0600); err != nil {
					t.Fatal(err)
				}
				_, _, err = readSelectedOpenBaoPublicCRL(store, e)
				ciOpenBaoRequireBlock(t, err, "immutable-qualification")
			case "different-bytes":
				if err := os.WriteFile(path, append(append([]byte(nil), original...), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				ciOpenBaoRequireBlock(t, writeOpenBaoPublicCRLQualification(store, q), "immutable-qualification-bytes")
			case "unsafe-file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				ciOpenBaoRequireBlock(t, writeOpenBaoPublicCRLQualification(store, q), "unsafe-qualification-file")
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, mode := range []string{"nil", "schema", "parent", "journal", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			proof := ciOpenBaoClone(t, *q)
			candidate := &proof
			cause := "qualification-shape"
			switch mode {
			case "nil":
				candidate = nil
			case "schema":
				proof.Schema = "foreign"
			case "parent":
				proof.Parent = "foreign"
			case "journal":
				proof.SourceJournal.PlanSHA256 = deploymentplan.Digest("foreign")
			case "oversize":
				proof.SourceDefinitions = []deploymentplan.Definition{{Object: deploymentplan.Object{"value": strings.Repeat("x", environmentPreflightArtifactLimit)}}}
				cause = "bounded-qualification"
			}
			ciOpenBaoRequireBlock(t, writeOpenBaoPublicCRLQualification(store, candidate), cause)
		})
	}
}

func TestCIOpenBaoPublicCRLAdmissionRequiresCompleteObservations(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{}}
	if _, err := openBaoPublicCRLReadAdapters(cfg, nil); err != nil {
		t.Fatal(err)
	}
	for _, selected := range [][]openBaoPublicCRLObservations{{{}}, {{}, {}}} {
		_, err := openBaoPublicCRLReadAdapters(cfg, selected)
		ciOpenBaoRequireBlock(t, err, "complete-observation-adapters")
	}
	if err := qualifyEnvironmentCreateOpenBaoPublicCRLs(t.Context(), cfg, secretStore{}, nil, environmentCreateBootstrapRuntime{}, environmentCreateServiceCeremony{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvironmentCreateOpenBaoPublicCRLs(t.Context(), cfg, secretStore{}, nil, environmentCreateBootstrapRuntime{}, environmentCreateServiceCeremony{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenBaoPublicCRLRecords(t.Context(), cfg, environmentCreateBootstrapRuntime{}, []createOpenBaoPublicCRLPurpose{{Issuer: environmentCreateRegisteredIssuer{Environment: "foreign", Domain: "openbao_tls", ID: "11111111-1111-4111-8111-111111111111"}}}); err == nil {
		t.Fatal("foreign issuer reached registry")
	} else {
		ciOpenBaoRequireBlock(t, err, "original-issuer")
	}
	store, e, p, _ := freshStageFixture(t)
	parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	cfg = deploymentConfig{Environment: "dev", Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600"}}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: environmentKubernetesBackend{Store: store}}
	for _, mode := range []string{"nil-proof", "schema", "owner", "configuration", "journal", "expired"} {
		t.Run(mode, func(t *testing.T) {
			proof := &createOpenBaoPublicCRLQualification{Schema: openBaoPublicCRLSchema, Parent: e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), SourceJournal: e.Journal, Plan: p, QualifiedAt: time.Now().UTC().Add(-time.Minute), ValidUntil: time.Now().UTC().Add(time.Hour)}
			cause := "qualified-original-scope"
			switch mode {
			case "nil-proof":
				proof = nil
			case "schema":
				proof.Schema = "foreign"
			case "owner":
				proof.OwnerSHA = "foreign"
			case "configuration":
				proof.ConfigurationSHA = "foreign"
			case "journal":
				proof.SourceJournal.OwnerToken = "foreign"
			case "expired":
				proof.ValidUntil = time.Now().UTC().Add(-time.Second)
				cause = "qualified-clock-or-checker"
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			ciOpenBaoRequireBlock(t, verifyOpenBaoPublicCRLQualification(t.Context(), cfg, store, &parent, runtime, proof, deploymentplan.Digest(proof)), cause)
			ciOpenBaoAssertReadOnly(t, store, before)
		})
	}
}

// Only this owned temporary API changes its observations. The canonical create
// source, journal and original kubeconfig are still loaded by the public CLI.
func ciOpenBaoOriginalNative(t *testing.T, f *initializedAuditContractFixture, objects map[string]deploymentplan.Object, responses map[string]string) (environmentKubernetesBackend, string, string) {
	t.Helper()
	native, dir, calls := unifiedSessionNativeBackend(t, f.store, objects, responses)
	kube, err := os.ReadFile(f.store.KubeconfigPath())
	if err != nil {
		t.Fatal(err)
	}
	protocol := filepath.Join(dir, "unified-kubectl.py")
	raw, err := os.ReadFile(protocol)
	if err != nil {
		t.Fatal(err)
	}
	original := "if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
	replacement := fmt.Sprintf("if f.read() not in (b'isolated-native-kubeconfig', bytes.fromhex('%x')): sys.exit(72)", kube)
	if !strings.Contains(string(raw), original) {
		t.Fatal("native kubeconfig fence unavailable")
	}
	if err := os.WriteFile(protocol, []byte(strings.Replace(string(raw), original, replacement, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	// Materialize the normal private kubectl scratch directory before taking
	// read-only snapshots. The original selected kubeconfig is used throughout.
	native.KubeconfigContent = kube
	for id := range objects {
		if _, err := native.Read(t.Context(), id); err != nil {
			t.Fatal("original native API observation", err)
		}
		break
	}
	return native, dir, calls
}

func ciOpenBaoNativeObject(t *testing.T, dir string, o deploymentplan.Object) {
	t.Helper()
	m := deploymentplan.Map(o["metadata"])
	path := filepath.Join(dir, deploymentplan.String(o["kind"])+"_"+deploymentplan.String(m["namespace"])+"_"+deploymentplan.String(m["name"])+".json")
	if err := os.WriteFile(path, mustProviderRecoveryJSON(o), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCIOpenBaoManagedUnsealRequiresOriginalStoppedWriteAndLease(t *testing.T) {
	f, workspace, _, _, _ := openBaoCommandContractFixture(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	e, _, err := loadActiveTransportExecution(f.cfg, f.store, f.sha)
	if err != nil {
		t.Fatal(err)
	}
	_, _, bodies := existingFileOpenBaoFixture("dev", true)
	set := deploymentplan.Clone(bodies[0])
	m := deploymentplan.Map(set["metadata"])
	m["uid"] = "11111111-1111-4111-8111-111111111111"
	m["resourceVersion"] = "original-version"
	m["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	set["status"] = deploymentplan.Object{"updateRevision": "managed-original"}
	id := deploymentplan.ID(set)
	b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{id: deploymentplan.Clone(set)}, writes: map[string]int{}}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("original-managed-release"), BindingsSHA256: deploymentplan.Digest("original-managed-intent")}
	original, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: set, Phase: "workload"}}, []deploymentplan.Object{set})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveEnvironmentCreateStageInput(t.Context(), f.store, e, "transport-managed-hosts", original); err != nil {
		t.Fatal(err)
	}
	desired := deploymentplan.Clone(set)
	pod := environmentPKIPod(desired)
	pod["securityContext"] = deploymentplan.Object{"fsGroup": float64(10001)}
	forward, j := ciOpenBaoRunStage(t, f.store, e, in, b, managedAllocationForwardStage(original), []deploymentplan.Definition{{Object: desired, Phase: "workload"}}, true)
	if j.Owners[id] == "" || len(e.Journal.KubernetesLeases) == 0 {
		t.Fatal("stopped native write has no original owner")
	}
	owned := deploymentplan.Clone(b.objects[id])
	owned["status"] = deploymentplan.Object{"updateRevision": "managed-original"}
	ns := e.Journal.Stack + "-secrets"
	replacement := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": "22222222-2222-4222-8222-222222222222", "resourceVersion": "replacement-version", "labels": deploymentplan.Object{"controller-revision-hash": "managed-original"}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": j.Owners[id], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(owned))}
	lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	lm := strings.Split(lease.ResourceID, "/")
	leaseObject := deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": deploymentplan.Object{"namespace": lm[1], "name": lm[2], "uid": lease.UID, "resourceVersion": "original-lease"}, "spec": deploymentplan.Object{"holderIdentity": lease.Holder}}
	// Initialization evidence is confined to this synthetic original owner. The
	// already-unsealed status tests safe lease retirement without reinitializing.
	init := []byte(`{"root_token":"isolated-original-root","unseal_keys_b64":["aXNvbGF0ZWQtb3JpZ2luYWwtc2hhcmU="]}`)
	for name, value := range map[string][]byte{"openbao/init.json": init, "openbao/root-token": []byte("isolated-original-root"), "openbao/unseal-key": []byte("aXNvbGF0ZWQtb3JpZ2luYWwtc2hhcmU=")} {
		if err := f.store.write(name, value, true); err != nil {
			t.Fatal(err)
		}
	}
	receipt := deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(init), "pod_uid": "original-bootstrap-pod", "statefulset_uid": j.Owners[id], "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"}
	if err := e.BeginEntry("openbao-initialize", "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"pod_uid": "original-bootstrap-pod", "statefulset_uid": j.Owners[id], "key_shares": 1, "key_threshold": 1}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt("openbao-initialize", mustProviderRecoveryJSON(receipt)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	statusArgs := []string{"--namespace", ns, "exec", "-i", "openbao-0", "--container", "openbao", "--", "env", "BAO_ADDR=https://127.0.0.1:8200", "BAO_CACERT=/run/openbao-pki-root/root.pem", "BAO_TLS_SERVER_NAME=openbao." + ns + ".svc", "/bin/sh", "-ec", "set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]"}
	_, dir, calls := ciOpenBaoOriginalNative(t, f, map[string]deploymentplan.Object{id: owned, deploymentplan.ID(replacement): replacement, lease.ResourceID: leaseObject}, map[string]string{strings.Join(statusArgs, " "): `{"initialized":true,"sealed":false}`})
	args := []string{"--environment=dev", "--parent-plan-sha256=" + f.sha, "--confirm=video-cloud-dev"}
	for _, mode := range []string{"controller-owner", "pod-owner", "lease-owner"} {
		t.Run(mode, func(t *testing.T) {
			target := deploymentplan.Clone(owned)
			wrongPod := deploymentplan.Clone(replacement)
			wrongLease := deploymentplan.Clone(leaseObject)
			cause := ""
			switch mode {
			case "controller-owner":
				deploymentplan.Map(target["metadata"])["uid"] = "foreign"
				cause = "controller differs from the recorded write"
			case "pod-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(wrongPod["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
				cause = "replacement Pod is unavailable"
			case "lease-owner":
				deploymentplan.Map(wrongLease["spec"])["holderIdentity"] = forward.SHA256() + ":foreign"
				cause = "lease differs from its original owner"
			}
			ciOpenBaoNativeObject(t, dir, target)
			ciOpenBaoNativeObject(t, dir, wrongPod)
			ciOpenBaoNativeObject(t, dir, wrongLease)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			ciOpenBaoRequireBlock(t, runCreateManagedOpenBaoUnseal(args), cause)
			ciOpenBaoAssertReadOnly(t, f.store, before)
		})
	}
	ciOpenBaoNativeObject(t, dir, owned)
	ciOpenBaoNativeObject(t, dir, replacement)
	ciOpenBaoNativeObject(t, dir, leaseObject)
	if err := runCreateManagedOpenBaoUnseal(args); err != nil {
		t.Fatal("safe same-owner unseal continuation", err)
	}
	check, _, err := loadActiveTransportExecution(f.cfg, f.store, f.sha)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	key := "managed-openbao-lease-release-" + deploymentplan.Digest(deploymentplan.Object{"resource": lease.ResourceID, "uid": lease.UID, "holder": lease.Holder, "interrupted_plan_sha256": forward.SHA256()})
	completed := createOpenBaoACKCompletionEntry(check.Journal, key)
	if completed == nil || completed.State != "completed" {
		t.Fatal("actual lease release was not durably committed")
	}
	if raw, err := os.ReadFile(calls); err != nil || strings.Contains(string(raw), "sys/init") || strings.Contains(string(raw), "sys/unseal") {
		t.Fatal("already-unsealed observation tried to initialize or unseal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Lease_"+lm[1]+"_"+lm[2]+".json")); !os.IsNotExist(err) {
		t.Fatal("exact original lease was not released", err)
	}
}

func TestCIOpenBaoPublicCAHandoffPreservesBothAuthoritiesAndNativeOwner(t *testing.T) {
	f, workspace, _, _, _ := openBaoCommandContractFixture(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	e, _, err := loadActiveTransportExecution(f.cfg, f.store, f.sha)
	if err != nil {
		t.Fatal(err)
	}
	_, _, oldPEM := coreContractCertificate(t, "isolated-bootstrap-ca", true, nil, nil)
	managed, key, managedPEM := coreContractCertificate(t, "isolated-managed-ca", true, nil, nil)
	pin := environmentPreflightBytesSHA256(managed.Raw)
	server := "openbao." + e.Journal.Stack + "-secrets.svc"
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	leaf := &x509.Certificate{SerialNumber: big.NewInt(91), Subject: pkix.Name{CommonName: server}, DNSNames: []string{server}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, managed, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + managedPEM
	ns := e.Journal.Stack + "-video-cloud"
	source := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-openbao-transport-ca"}, "data": deploymentplan.Object{"ca.crt": oldPEM, "unrelated": "preserved-original-data"}}
	b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("original-ca-release"), BindingsSHA256: deploymentplan.Digest("original-ca-source")}
	_, foundation := ciOpenBaoRunStage(t, f.store, e, in, b, "foundation", []deploymentplan.Definition{{Object: source, Phase: "configuration"}}, false)
	controller := ciOpenBaoDeployment(e.Journal.Stack, "pki-controller")
	cp := environmentPKIPod(controller)
	c := managedNamedField(cp["containers"], "app")
	c["name"] = "pki-controller"
	c["env"] = environmentCreateEnv(map[string]string{"PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256": pin, "PKI_STATE_OPENBAO_SERVER_PKI_NAME": server, "PKI_STATE_RECORD_PREFIX": "dev/" + f.sha + "/pki-controller", "PKI_STATE_OPENBAO_TLS_CA": "/run/custody/trust/managed-root.pem"})
	c["volumeMounts"] = []any{environmentCreateServiceMount("pki-custody-trust", "/run/custody/trust", true)}
	cp["volumes"] = []any{deploymentplan.Object{"name": "pki-custody-trust", "configMap": deploymentplan.Object{"name": "managed-custody-root"}}}
	ciOpenBaoRunStage(t, f.store, e, in, b, "ci-original-managed-controller", []deploymentplan.Definition{{Object: controller, Phase: "workload"}}, false)
	snapshot := environmentCustodyTransportTrust{Schema: "rtk.custody.transport/v1", Environment: "dev", Generation: f.sha, ServerName: server, ManagedRootSHA256: pin, ManagedPEM: managedPEM, ManagedOnly: true}
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "managed-custody-root", "uid": "33333333-3333-4333-8333-333333333333"}, "data": deploymentplan.Object{"managed-root.pem": managedPEM, "transport.json": string(mustProviderRecoveryJSON(snapshot))}}
	baoNS := e.Journal.Stack + "-secrets"
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": baoNS, "name": "original-openbao-file"}, "data": deploymentplan.Object{"file.hcl": "listener \"tcp\" {\n tls_cert_file = \"/run/openbao/chain.pem\"\n}\n"}}
	set := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": baoNS, "name": "openbao", "uid": "44444444-4444-4444-8444-444444444444"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "file-config", "configMap": deploymentplan.Object{"name": "original-openbao-file"}}}}}}}
	objects := map[string]deploymentplan.Object{}
	for id, o := range b.objects {
		objects[id] = deploymentplan.Clone(o)
	}
	for _, o := range []deploymentplan.Object{trust, config, set} {
		objects[deploymentplan.ID(o)] = o
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	native, dir, calls := ciOpenBaoOriginalNative(t, f, objects, map[string]string{strings.Join([]string{"--namespace", baoNS, "exec", "statefulset/openbao", "--container", "openbao", "--", "cat", "/run/openbao/chain.pem"}, " "): chain})
	args := []string{"--environment=dev", "--timeout=15s"}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if err := runCreateOpenBaoPublicCAHandoff(args); err != nil {
		t.Fatal("public CA overlap preflight", err)
	}
	ciOpenBaoAssertReadOnly(t, f.store, before)
	targetID := deploymentplan.ID(source)
	originalOwner := foundation.Owners[targetID]
	for _, mode := range []string{"foreign-owner", "unrelated-data", "wrong-generation", "ambiguous-chain-path"} {
		t.Run(mode, func(t *testing.T) {
			actual := deploymentplan.Clone(objects[targetID])
			managedTrust := deploymentplan.Clone(trust)
			conf := deploymentplan.Clone(config)
			cause := ""
			switch mode {
			case "foreign-owner":
				deploymentplan.Map(actual["metadata"])["uid"] = "foreign"
				cause = "public CA owner changed"
			case "unrelated-data":
				deploymentplan.Map(actual["data"])["unrelated"] = "changed"
				cause = "unrelated original public CA data changed"
			case "wrong-generation":
				wrong := snapshot
				wrong.Generation = deploymentplan.Digest("foreign")
				deploymentplan.Map(managedTrust["data"])["transport.json"] = string(mustProviderRecoveryJSON(wrong))
				cause = "snapshot does not bind the original generation"
			case "ambiguous-chain-path":
				deploymentplan.Map(conf["data"])["duplicate.hcl"] = "tls_cert_file = \"/run/openbao/other.pem\"\n"
				cause = "certificate path is ambiguous"
			}
			ciOpenBaoNativeObject(t, dir, actual)
			ciOpenBaoNativeObject(t, dir, managedTrust)
			ciOpenBaoNativeObject(t, dir, conf)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			ciOpenBaoRequireBlock(t, runCreateOpenBaoPublicCAHandoff(args), cause)
			ciOpenBaoAssertReadOnly(t, f.store, before)
		})
	}
	ciOpenBaoNativeObject(t, dir, objects[targetID])
	ciOpenBaoNativeObject(t, dir, trust)
	ciOpenBaoNativeObject(t, dir, config)
	if err := runCreateOpenBaoPublicCAHandoff(append(args, "--execute")); err != nil {
		t.Fatal("native public CA overlap execution", err)
	}
	actual, err := native.Read(t.Context(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	data := deploymentplan.Map(actual["data"])
	if deploymentplan.Map(actual["metadata"])["uid"] != originalOwner || data["unrelated"] != "preserved-original-data" || data["ca.crt"] != strings.TrimSpace(oldPEM)+"\n"+strings.TrimSpace(managedPEM)+"\n" {
		t.Fatal("overlap changed owner, unrelated data or an original authority")
	}
	rawCalls, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(rawCalls), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "delete ") || strings.Contains(line, "/sys/") {
			t.Fatal("public CA handoff attempted a private authority operation")
		}
	}
}

func TestCIOpenBaoCustodyControllerCLIResumesRecordedRestoreAndRelease(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	kube, err := newEnvironmentKubernetesBackend(string(mustProviderRecoveryJSON(frozenKubernetesFixture(t))), "dev")
	if err != nil {
		t.Fatal(err)
	}
	operator := map[string]string{"LINODE_TOKEN": "isolated-provider-token", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-admin-password"}
	cfg, store, old, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: operator, kubeconfig: kube.KubeconfigContent})
	e, _ := providerExecutionContractFork(t, old, parent, "ci-original-custody-controller")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	sha := e.Journal.PlanSHA256
	now := time.Now().UTC()
	// The real transport producer and successor executor retain each serving key.
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, sha, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, sha, now)
	if err != nil {
		t.Fatal(err)
	}
	originalCA, err := os.ReadFile(filepath.Join(store.Root, "deployment/create/material", sha, "openbao-transport/ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := renderEnvironmentCustodyTrust(cfg, sha, string(originalCA), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := renderEnvironmentCustodyTrust(cfg, sha, candidate.Transports["openbao"].CACert, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create", sha, "custody/transport.json"), []byte(deploymentplan.String(deploymentplan.Map(before[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: deploymentplan.Digest("ci-original-custody")}
	for _, stage := range []string{"device-root-active", "app-root-trust", "app-intermediate-trust"} {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "ci-" + stage}, "data": deploymentplan.Object{"public-source": "isolated-original"}}
		ciOpenBaoRunStage(t, store, e, in, b, stage, []deploymentplan.Definition{{Object: body, Phase: "configuration"}}, false)
	}
	continuation := issuerEnrollmentContinuationPrefix + deploymentplan.Digest("ci-original-issuer")
	if err := e.BeginEntry(continuation, "continuation", mustProviderRecoveryJSON(deploymentplan.Object{"original": true}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	role := deploymentplan.Object{"role": "certissuer-dev", "namespace": e.Journal.Stack + "-video-cloud", "service_accounts": []string{"certissuer"}, "policies": []string{"create-device-signer"}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	if err := e.BeginEntry("openbao-role-certissuer-dev-"+deploymentplan.Digest(role)[:12], "bootstrap", mustProviderRecoveryJSON(role), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	source := ciOpenBaoClone(t, e.Journal)
	successor := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: sha, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate)}
	api := &initialSuccessorFlowBackend{objects: map[string]deploymentplan.Object{}, expected: map[string]deploymentplan.Object{}}
	for i := range before {
		id := deploymentplan.ID(before[i].Object)
		uid := fmt.Sprintf("%08d-3333-4333-8333-333333333333", i+1)
		actual := deploymentplan.Clone(before[i].Object)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		deploymentplan.Map(actual["metadata"])["resourceVersion"] = "original-trust"
		api.objects[id] = actual
		successor.Resources = append(successor.Resources, activeTransportRenewalResource{ID: id, UID: uid, Before: before[i].Object, Desired: after[i].Object})
	}
	artifact := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: successor, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/transport-successor", deploymentplan.Digest(artifact))
	for name, value := range map[string]any{"qualification.json": artifact, "source-journal.json": source} {
		if err := store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	reloads := 0
	check := func() error { return e.Check(t.Context()) }
	verify := func() error {
		for i := range after {
			if !deploymentplan.Matches(after[i].Object, api.objects[deploymentplan.ID(after[i].Object)]) {
				return errors.New("isolated successor trust not observed")
			}
		}
		return nil
	}
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, api, artifact, source, candidate, check, func() error { reloads++; return nil }, verify); err != nil {
		t.Fatal(err)
	}
	if reloads != 1 {
		t.Fatal("original listener reload was not observed")
	}
	original := ciOpenBaoDeployment(e.Journal.Stack, "pki-controller")
	deploymentplan.Map(original["metadata"])["uid"] = "55555555-5555-4555-8555-555555555555"
	deploymentplan.Map(original["metadata"])["resourceVersion"] = "original-controller"
	id := deploymentplan.ID(original)
	b.objects[id] = deploymentplan.Clone(original)
	broken := deploymentplan.Clone(original)
	environmentPKIPod(broken)["volumes"] = []any{deploymentplan.Object{"name": "legacy-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "pki-controller-state"}}}
	_, j := ciOpenBaoRunStage(t, store, e, in, b, "transport-enrollment-issuer", []deploymentplan.Definition{{Object: broken, Phase: "workload"}}, true)
	if j.Owners[id] != deploymentplan.Map(original["metadata"])["uid"] {
		t.Fatal("interrupted original owner changed")
	}
	lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	parts := strings.Split(lease.ResourceID, "/")
	leaseObject := deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": deploymentplan.Object{"namespace": parts[1], "name": parts[2], "uid": lease.UID, "resourceVersion": "original-interrupted-lease"}, "spec": deploymentplan.Object{"holderIdentity": lease.Holder}}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	f := &initializedAuditContractFixture{cfg: cfg, store: store, sha: sha}
	native, dir, calls := ciOpenBaoOriginalNative(t, f, map[string]deploymentplan.Object{id: b.objects[id], lease.ResourceID: leaseObject}, nil)
	args := []string{"--environment=dev", "--parent-plan-sha256=" + sha, "--confirm=video-cloud-dev"}
	if err := runCreateCustodyControllerRecovery(args); err != nil {
		t.Fatal("recorded custody controller restoration", err)
	}
	actual, err := native.Read(t.Context(), id)
	if err != nil || !deploymentplan.Matches(original, actual) {
		t.Fatal("original template was not restored", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Lease_"+parts[1]+"_"+parts[2]+".json")); !os.IsNotExist(err) {
		t.Fatal("original stopped lease remains", err)
	}
	beforeState := createBootstrapStateSnapshot(t, store.Root)
	beforeCalls, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if err := runCreateCustodyControllerRecovery(args); err != nil {
		t.Fatal("completed restore was not idempotently observable", err)
	}
	ciOpenBaoAssertReadOnly(t, store, beforeState)
	finalCalls, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if delta := string(finalCalls[len(beforeCalls):]); strings.Contains(delta, "replace ") || strings.Contains(delta, "delete ") {
		t.Fatal("completed restoration replayed a native write", delta)
	}
}

func TestCIOpenBaoInPlaceScopeCannotSelectAnotherExecution(t *testing.T) {
	store, e, p, _ := freshStageFixture(t)
	for _, mode := range []string{"prepare-error", "missing-owner", "missing-cleanup", "no-original-source", "unreviewed-execution"} {
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("original operator unavailable")
			closed := 0
			prepare := func(context.Context, string) (*openBaoInPlacePrepared, error) {
				switch mode {
				case "prepare-error":
					return nil, sentinel
				case "missing-owner":
					return &openBaoInPlacePrepared{cleanup: func() {}}, nil
				case "missing-cleanup":
					return &openBaoInPlacePrepared{execution: e}, nil
				}
				return &openBaoInPlacePrepared{store: store, execution: e, cleanup: func() { closed++ }}, nil
			}
			args := []string{"--environment=dev"}
			if mode == "unreviewed-execution" {
				args = append(args, "--execute", "--plan-sha256="+strings.Repeat("a", 64))
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := runCreateOpenBaoInPlaceWith(args, prepare)
			switch mode {
			case "prepare-error":
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			case "missing-owner", "missing-cleanup":
				ciOpenBaoRequireBlock(t, err, "execution preparation is unavailable")
			case "no-original-source":
				ciOpenBaoRequireBlock(t, err, "StatefulSet source unavailable")
			case "unreviewed-execution":
				ciOpenBaoRequireBlock(t, err, "qualification changed")
			}
			if mode == "no-original-source" || mode == "unreviewed-execution" {
				if closed != 1 {
					t.Fatal("prepared original execution was not closed")
				}
			}
			ciOpenBaoAssertReadOnly(t, store, before)
		})
	}
	for _, args := range [][]string{{"--environment=dev", "--execute"}, {"--environment=dev", "--plan-sha256=" + strings.Repeat("a", 64)}, {"--environment=dev", "--timeout=0"}} {
		called := false
		ciOpenBaoRequireBlock(t, runCreateOpenBaoInPlaceWith(args, func(context.Context, string) (*openBaoInPlacePrepared, error) { called = true; return nil, nil }), "select environment")
		if called {
			t.Fatal("invalid image admission opened an execution")
		}
	}
	if _, _, err := openBaoInPlaceSource(store, e, openBaoInPlaceQualification{SourceStage: "absent"}); err == nil {
		t.Fatal("missing original source adopted")
	} else {
		ciOpenBaoRequireBlock(t, err, "image source changed")
	}
	if _, err := openBaoInPlaceTemplatePlan(p, ciOpenBaoDeployment(e.Journal.Stack, "wrong-kind"), nil, "mutable:latest", "foreign-template"); err == nil {
		t.Fatal("a non-OpenBao template was accepted")
	} else {
		ciOpenBaoRequireBlock(t, err, "host template unavailable")
	}
	for _, pod := range []deploymentplan.Object{nil, {"status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}} {
		_, _, err := openBaoInPlaceStatuses(pod)
		ciOpenBaoRequireBlock(t, err, "not running and ready")
	}
}

func TestCIOpenBaoPublicCRLSourceRequiresRecordedProviderOwners(t *testing.T) {
	store, e, base, b := freshStageFixture(t)
	cfg := deploymentConfig{Environment: "dev", Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600"}}
	var defs []deploymentplan.Definition
	for _, role := range []string{"pki-controller", "certissuer"} {
		defs = append(defs, deploymentplan.Definition{Object: ciOpenBaoDeployment(e.Journal.Stack, role), Phase: "workload"})
	}
	ciOpenBaoRunStage(t, store, e, base.Inputs, b, "ci-original-crl-providers", defs, false)
	before := createBootstrapStateSnapshot(t, store.Root)
	source, child, owners, err := openBaoPublicCRLSource(t.Context(), cfg, store, e, b, defs)
	if err != nil || source != "" || child != "" || len(owners) != 2 {
		t.Fatal("accepted original provider owners not recovered", err)
	}
	ciOpenBaoAssertReadOnly(t, store, before)
	id := deploymentplan.ID(defs[0].Object)
	original := deploymentplan.Clone(b.objects[id])
	for _, mode := range []string{"absent", "terminating", "malformed-uid", "unrecorded-owner"} {
		t.Run(mode, func(t *testing.T) {
			live := deploymentplan.Clone(original)
			cause := ""
			switch mode {
			case "absent":
				live = nil
				cause = "original-provider-owner"
			case "terminating":
				deploymentplan.Map(live["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
				cause = "original-provider-owner"
			case "malformed-uid":
				deploymentplan.Map(live["metadata"])["uid"] = "foreign"
				cause = "original-provider-uid"
			case "unrecorded-owner":
				deploymentplan.Map(live["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				cause = "recorded-provider-history"
			}
			b.objects[id] = live
			before := createBootstrapStateSnapshot(t, store.Root)
			_, _, _, err := openBaoPublicCRLSource(t.Context(), cfg, store, e, b, defs)
			ciOpenBaoRequireBlock(t, err, cause)
			ciOpenBaoAssertReadOnly(t, store, before)
		})
	}
	b.objects[id] = original
	changed := ciOpenBaoClone(t, defs)
	for i := range changed {
		environmentPKIPod(changed[i].Object)["securityContext"] = deploymentplan.Object{"fsGroup": float64(10001)}
	}
	p, j := ciOpenBaoRunStage(t, store, e, base.Inputs, b, "managed-complete-issuer", changed, true)
	source, child, owners, err = openBaoPublicCRLSource(t.Context(), cfg, store, e, b, changed)
	if err != nil || source != p.SHA256() || child != deploymentplan.Digest(j) || len(owners) != 2 {
		t.Fatal("literal failed provider stage was not authenticated", err)
	}
	parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = prepareOpenBaoPublicCRLRecoveryQualification(t.Context(), cfg, store, &parent, environmentCreateBootstrapRuntime{Execution: e})
	ciOpenBaoRequireBlock(t, err, "original-session-lineage")
	if openBaoPublicCRLSourceSnapshotMatches(&createOpenBaoPublicCRLQualification{SourcePlanSHA: p.SHA256(), SourceChildSHA: child, Owners: owners}, p.SHA256(), "changed-child", owners, false) {
		t.Fatal("unfinished source accepted changed child state")
	}
	if !openBaoPublicCRLSourceSnapshotMatches(&createOpenBaoPublicCRLQualification{SourcePlanSHA: p.SHA256(), SourceChildSHA: child, Owners: owners}, p.SHA256(), "legitimately-advanced-child", owners, true) {
		t.Fatal("completed source cannot be observed after advancement")
	}
}

func TestCIOpenBaoMaterialProducerNeverClaimsPartialPrivateFiles(t *testing.T) {
	cfg, store, e := openBaoFirstTransportFixture(t)
	for _, key := range []string{"foreign-material", "management-transport-material"} {
		before := createBootstrapStateSnapshot(t, store.Root)
		called := false
		err := createOpenBaoFirstMaterial(t.Context(), cfg, store, e, key, func() error { called = true; return errors.New("isolated producer interrupted") })
		if key == "foreign-material" {
			ciOpenBaoRequireBlock(t, err, "unsupported original material checkpoint")
			if called {
				t.Fatal("unsupported checkpoint invoked producer")
			}
			ciOpenBaoAssertReadOnly(t, store, before)
		} else {
			if !called || err == nil || err.Error() != "isolated producer interrupted" {
				t.Fatal("producer failure was not preserved", err)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
				t.Fatal("interrupted producer was silently accepted")
			}
			called = false
			ciOpenBaoRequireBlock(t, createOpenBaoFirstMaterial(t.Context(), cfg, store, e, key, func() error { called = true; return nil }), "already has intent")
			if called {
				t.Fatal("uncertain producer replayed")
			}
		}
	}
	_, _, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, "unsupported")
	ciOpenBaoRequireBlock(t, err, "unsupported original material checkpoint")
	ciOpenBaoRequireBlock(t, verifyOpenBaoFirstMaterialParent(t.Context(), cfg, store, nil), "parent unavailable")
	other := cfg
	other.Environment = "staging"
	ciOpenBaoRequireBlock(t, verifyOpenBaoFirstMaterialParent(t.Context(), other, store, e), "parent unavailable")
	if _, _, _, err := readOpenBaoFirstMaterial(t.Context(), cfg, store, nil, "management-transport-material", nil); err == nil {
		t.Fatal("absent owner read original material")
	} else {
		ciOpenBaoRequireBlock(t, err, "incomplete or changed")
	}
	if err := validateOpenBaoFirstManagementTransport(cfg, environmentCreateTransport{CACert: "untrusted bytes"}); err == nil {
		t.Fatal("malformed public root accepted")
	} else {
		ciOpenBaoRequireBlock(t, err, "management transport identity differs")
	}
}

func TestCIOpenBaoCustodyTokenPreflightRequiresOriginalExecutionDeadline(t *testing.T) {
	store, e, source, live, _ := ciOpenBaoTokenFixture(t)
	cfg := deploymentConfig{Environment: "dev", Adapter: "lke", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "600"}}
	parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]deploymentplan.Object{}
	for _, o := range live {
		objects[deploymentplan.ID(o)] = o
	}
	namespace := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": e.Journal.Stack + "-video-cloud", "uid": "44444444-4444-4444-8444-444444444444"}}
	objects[deploymentplan.ID(namespace)] = namespace
	native, _, calls := unifiedSessionNativeBackend(t, store, objects, nil)
	beforeJournal := deploymentplan.Digest(e.Journal)
	beforeObjects := deploymentplan.Digest(objects)
	handled, err := tryUnifiedCustodyTokenAccess(t.Context(), cfg, unifiedDeploymentOptions{Action: "preflight"}, &unifiedDeploymentReport{}, store, e, native, &parent, io.Discard, io.Discard)
	if !handled {
		t.Fatal("recorded original custody workload was ignored")
	}
	ciOpenBaoRequireBlock(t, err, "original custody access execution deadline unavailable")
	var proof custodyTokenAccessProof
	if err := readExecutionJSON(filepath.Join(e.Directory, "custody-token-access.json"), &proof); err != nil {
		t.Fatal("qualified native runtime facts were not retained", err)
	}
	if proof.Parent != e.Journal.PlanSHA256 || proof.Owner != deploymentplan.Digest(e.owner) || proof.Configuration != deploymentplan.Digest(cfg) || len(proof.Facts) != 1 || len(proof.Sources) != 1 || proof.Sources[0].Plan != source.Plan.SHA256() || proof.Sources[0].Journal != deploymentplan.Digest(source.Journal) {
		t.Fatal("preflight proof lost original runtime/source identity")
	}
	if err := validateCustodyTokenAccessDelta(proof); err != nil {
		t.Fatal(err)
	}
	if beforeJournal != deploymentplan.Digest(e.Journal) || beforeObjects != deploymentplan.Digest(objects) {
		t.Fatal("unavailable original deadline changed execution or native observation")
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(raw), "\n") {
		if (strings.HasPrefix(call, "replace ") || strings.HasPrefix(call, "create ") || strings.HasPrefix(call, "delete ")) && !strings.Contains(call, "--dry-run=server") {
			t.Fatal("deadline refusal made a native write", call)
		}
	}
}
