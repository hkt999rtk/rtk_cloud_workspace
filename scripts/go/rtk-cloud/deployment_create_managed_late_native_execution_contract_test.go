package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the Kubernetes command protocol is simulated. The adapter still writes
// a private frozen kubeconfig, decodes owner identities and evaluates readiness.
func lateNativeReadContractBackend(t *testing.T, store secretStore, objects map[string]deploymentplan.Object) (environmentKubernetesBackend, string, string) {
	t.Helper()
	dir := t.TempDir()
	for id, object := range objects {
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json"), mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
set -eu
test "$1" = --kubeconfig && test -f "$2" || exit 71
case "$3" in --request-timeout=*) ;; *) exit 72;; esac
shift 3
printf '%s\n' "$*" >> "$RTK_LATE_NATIVE_CALLS"
if test "$1" = get; then
 kind="$2"; name="$3"; shift 3
 namespace=""
 if test "$1" = --namespace; then namespace="$2"; shift 2; fi
 test "$#" = 3 && test "$1" = --ignore-not-found=true && test "$2" = -o && test "$3" = json || exit 73
 file="$RTK_LATE_NATIVE_OBJECTS/${kind}_${namespace}_${name}.json"
 if test -f "$file"; then cat "$file"; fi
elif test "$1" = --namespace && test "$3" = rollout && test "$4" = status; then
 test "$#" = 6 || exit 74
 if test -f "$RTK_LATE_NATIVE_OBJECTS/fail-ready"; then exit 75; fi
else
 exit 76
fi
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_LATE_NATIVE_CALLS", calls)
	t.Setenv("RTK_LATE_NATIVE_OBJECTS", dir)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-native-kubeconfig"), RolloutTimeout: time.Second}, dir, calls
}

func lateNativeAcceptedOwnersContract(t *testing.T) (environmentCreateBootstrapRuntime, *deploymentplan.Plan, map[string]deploymentplan.Object, string) {
	t.Helper()
	late, plan := managedLateOwnersFixture(t)
	late.Review = strings.Repeat("7", 64)
	late.OwnerCandidate = plan
	store, e, _, backend := freshStageFixture(t)
	for id, owner := range late.Owners {
		body := deploymentplan.Clone(owner.Desired)
		deploymentplan.Map(body["metadata"])["uid"] = owner.UID
		deploymentplan.Map(body["metadata"])["resourceVersion"] = "original-1"
		backend.objects[id] = body
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return backend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "managed-late-session-owners-"+late.Review, plan, backend, qualify, fence, accept, e.Check, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, dir, _ := lateNativeReadContractBackend(t, store, backend.objects)
	return environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: &createServiceSessionRecovery{ManagedLateSession: late}}, plan, backend.objects, dir
}

func TestManagedLateNativeAcceptedOwnersExecutionContract(t *testing.T) {
	runtime, plan, objects, dir := lateNativeAcceptedOwnersContract(t)
	before := deploymentplan.Digest(runtime.Execution.Journal)
	if err := verifyManagedLateAcceptedOwners(t.Context(), deploymentConfig{}, runtime); err != nil {
		t.Fatal(err)
	}
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/managed-late-session-owners-"+runtime.ServiceSessionRecovery.ManagedLateSession.Review)
	loaded, j, err := readManagedLateProgressChild(runtime.Execution.Store, runtime.Execution, *entry, nil)
	if err != nil || loaded.SHA256() != plan.SHA256() || !j.Accepted {
		t.Fatal("accepted progress", err)
	}
	owners, fingerprints := map[string]deploymentplan.Resource{}, map[string]string{}
	if err := observeManagedLateAcceptedOwners(t.Context(), runtime.Backend, plan, j, len(plan.Resources), owners, fingerprints); err != nil {
		t.Fatal(err)
	}
	if len(owners) != 10 || len(fingerprints) != 10 {
		t.Fatal("partial owner observation")
	}
	for id, o := range objects {
		if owners[id].UID != deploymentplan.Map(o["metadata"])["uid"] || fingerprints[id] != deploymentplan.Digest(deploymentplan.Projection(o)) {
			t.Fatal("actual owner not retained", id)
		}
	}
	if deploymentplan.Digest(runtime.Execution.Journal) != before {
		t.Fatal("native read changed original journal")
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "rollout status") != 10 {
		t.Fatal("not every original workload was checked ready")
	}
}

func TestManagedLateNativeAcceptedOwnersExecutionRejectsDrift(t *testing.T) {
	for _, mode := range []string{"replaced-owner", "missing-owner", "image-drift", "receipt-owner", "pending-child", "wrong-qualified-candidate", "readiness-failed"} {
		t.Run(mode, func(t *testing.T) {
			runtime, plan, objects, dir := lateNativeAcceptedOwnersContract(t)
			id := plan.Resources[0].ID
			body := deploymentplan.Clone(objects[id])
			path := filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json")
			switch mode {
			case "replaced-owner":
				deploymentplan.Map(body["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "missing-owner":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "image-drift":
				managedNamedField(environmentPKIPod(body)["containers"], "native")["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
			case "receipt-owner":
				entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/managed-late-session-owners-"+runtime.ServiceSessionRecovery.ManagedLateSession.Review)
				entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": map[string]string{id: "foreign"}})
			case "pending-child":
				createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/managed-late-session-owners-"+runtime.ServiceSessionRecovery.ManagedLateSession.Review).State = "pending"
			case "wrong-qualified-candidate":
				copy := *plan
				copy.ReleaseSHA256 = strings.Repeat("f", 64)
				runtime.ServiceSessionRecovery.ManagedLateSession.OwnerCandidate = &copy
			case "readiness-failed":
				if err := os.WriteFile(filepath.Join(dir, "fail-ready"), []byte("fail"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "missing-owner" {
				if err := os.WriteFile(path, mustProviderRecoveryJSON(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := deploymentplan.Digest(runtime.Execution.Journal)
			var err error
			if mode == "readiness-failed" {
				_, j, readErr := managedTransportPostAcceptedStage(runtime.Execution.Store, runtime.Execution, "kubernetes/managed-late-session-owners-"+runtime.ServiceSessionRecovery.ManagedLateSession.Review)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = observeManagedLateAcceptedOwners(t.Context(), runtime.Backend, plan, j, len(plan.Resources), map[string]deploymentplan.Resource{}, map[string]string{})
			} else {
				err = verifyManagedLateAcceptedOwners(t.Context(), deploymentConfig{}, runtime)
			}
			if err == nil {
				t.Fatal("changed live or durable owner passed")
			}
			if deploymentplan.Digest(runtime.Execution.Journal) != before {
				t.Fatal("refusal changed evidence")
			}
		})
	}
}

func TestManagedLateNativeRetainedDependenciesExecutionContract(t *testing.T) {
	for _, mode := range []string{"valid", "replaced-owner", "deleting-owner", "changed-material", "missing-owner", "missing-verifier"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			late := &createManagedLateSessionRecovery{TrustOwners: map[string]deploymentplan.Resource{}, VerifierOwners: map[string]deploymentplan.Resource{}}
			objects := map[string]deploymentplan.Object{}
			for i, ns := range []string{"video-cloud-dev-video-cloud", "video-cloud-dev-secrets", "video-cloud-dev-account-manager"} {
				for _, kind := range []string{"ConfigMap", "Secret"} {
					body := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": ns, "name": "retained-trust"}, "data": deploymentplan.Object{"value": "original-public-pin"}}
					id := deploymentplan.ID(body)
					uid := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
					owner := deploymentplan.Resource{ID: id, UID: uid, Desired: deploymentplan.Clone(body)}
					if kind == "ConfigMap" {
						late.TrustOwners[id] = owner
					} else {
						late.VerifierOwners[id] = owner
					}
					deploymentplan.Map(body["metadata"])["uid"] = uid
					objects[id] = body
				}
			}
			id := "ConfigMap/video-cloud-dev-video-cloud/retained-trust"
			switch mode {
			case "replaced-owner":
				deploymentplan.Map(objects[id]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "deleting-owner":
				deploymentplan.Map(objects[id]["metadata"])["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			case "changed-material":
				deploymentplan.Map(objects[id]["data"])["value"] = "foreign"
			case "missing-owner":
				delete(objects, id)
			case "missing-verifier":
				delete(late.VerifierOwners, "Secret/video-cloud-dev-video-cloud/retained-trust")
			}
			backend, _, calls := lateNativeReadContractBackend(t, store, objects)
			before := deploymentplan.Digest(late)
			err := verifyManagedLateSessionRetainedDependencies(t.Context(), backend, late)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if deploymentplan.Digest(late) != before {
				t.Fatal("retained source changed")
			}
			raw, readErr := os.ReadFile(calls)
			if mode == "valid" && (readErr != nil || strings.Count(string(raw), "get ") != 6) {
				t.Fatal("did not re-read all retained owners", readErr)
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
		})
	}
}
