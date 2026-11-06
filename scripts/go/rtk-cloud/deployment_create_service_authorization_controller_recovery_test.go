package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

// The external boundary is kubectl only: child lookup, owner/spec validation,
// snapshot creation and immutable recovery evidence use their real code paths.
func TestServiceAuthorizationControllerRecoveryBindsOriginalChildAndSnapshot(t *testing.T) {
	for _, mode := range []string{"first-repair", "resume-repair", "apply-fails", "wrong-child-proof", "unaccepted-child", "changed-controller-owner", "changed-controller-spec", "unknown-image", "changed-executable", "changed-retained-owner", "changed-retained-data", "missing-listener", "changed-snapshot-owner", "changed-snapshot-history"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "staging")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			const oldImage = "ghcr.io/example/controller@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			const newImage = "ghcr.io/example/controller@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			stack := cfg.Values["CLOUD_STACK_NAME"]
			ns := stack + "-video-cloud"
			var definitions []deploymentplan.Definition
			for _, name := range []string{"pki-controller", "certissuer"} {
				object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "image": oldImage, "command": []any{"/app/pkicontroller"}, "args": []any{"original-argument"}}, deploymentplan.Object{"name": "sidecar", "image": "unchanged-sidecar"}}}}}}
				definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "bootstrap"})
			}
			trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "service-root"}, "data": deploymentplan.Object{"root": "original-trust"}}
			definitions = append(definitions, deploymentplan.Definition{Object: trust, Phase: "bootstrap"})
			if mode == "missing-listener" {
				definitions = definitions[1:]
			}
			child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "staging", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			native := map[string]deploymentplan.Object{}
			journal := deploymentplan.Journal{PlanSHA256: child.SHA256(), Accepted: true, Owners: map[string]string{}}
			for i, r := range child.Resources {
				object := deploymentplan.Clone(r.Desired)
				uid := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
				deploymentplan.Map(object["metadata"])["uid"] = uid
				native[r.ID] = object
				journal.Owners[r.ID] = uid
				journal.Completed = append(journal.Completed, r.ID)
			}
			controllerID := "Deployment/" + ns + "/pki-controller"
			retainedID := "ConfigMap/" + ns + "/service-root"
			resume := mode == "resume-repair" || strings.HasPrefix(mode, "changed-snapshot-")
			if resume {
				for _, name := range []string{"pki-controller", "certissuer"} {
					serviceAuthorizationControllerContainer(native["Deployment/"+ns+"/"+name], stack)["image"] = newImage
				}
			}
			switch mode {
			case "wrong-child-proof":
				journal.PlanSHA256 = strings.Repeat("f", 64)
			case "unaccepted-child":
				journal.Accepted = false
			case "changed-controller-owner":
				deploymentplan.Map(native[controllerID]["metadata"])["uid"] = "foreign-controller-owner"
			case "changed-controller-spec":
				deploymentplan.Map(native[controllerID]["spec"])["replicas"] = float64(2)
			case "unknown-image":
				serviceAuthorizationControllerContainer(native[controllerID], stack)["image"] = "unreviewed-image"
			case "changed-executable":
				serviceAuthorizationControllerContainer(native[controllerID], stack)["command"] = []any{"/app/other"}
			case "changed-retained-owner":
				deploymentplan.Map(native[retainedID]["metadata"])["uid"] = "foreign-trust-owner"
			case "changed-retained-data":
				deploymentplan.Map(native[retainedID]["data"])["root"] = "substituted-trust"
			}
			for name, value := range map[string]any{"plan.json": child, "journal.json": journal} {
				if err := store.write(filepath.Join("deployment/rollback", child.SHA256(), name), mustProviderRecoveryJSON(value), true); err != nil {
					t.Fatal(err)
				}
			}
			intent, err := environmentCreateKubernetesStageIntent(child)
			if err != nil {
				t.Fatal(err)
			}
			key := "kubernetes/service-root-trust"
			if err := e.BeginEntry(key, "service-root-trust", intent, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"plan_sha256": child.SHA256()})); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			e.Journal.KubernetesLeases = []deploymentplan.LeaseOwner{{Holder: child.SHA256() + ":original-owner"}}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			checkpoint := e.Journal
			snapshot := filepath.Join("deployment/create", e.Journal.PlanSHA256, "service-authorization-recovery/source-journal.json")
			stage := "service-authorization-controller-" + strings.Repeat("c", 40)
			if resume {
				saved := checkpoint
				if mode == "changed-snapshot-owner" {
					saved.OwnerToken = "foreign-owner"
				}
				if mode == "changed-snapshot-history" {
					saved.Entries = append(append([]environmentCreateJournalEntry(nil), saved.Entries...), environmentCreateJournalEntry{Key: "unknown-write", State: "completed"})
				}
				if err := writeEnvironmentCreateDerivedPrivateFile(store, snapshot, mustProviderRecoveryJSON(saved)); err != nil {
					t.Fatal(err)
				}
				if err := e.BeginEntry("kubernetes/"+stage, stage, mustProviderRecoveryJSON(map[string]string{"image": newImage}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			backend := serviceAuthorizationNativeFixture(t, store, ns, native)
			// Precreate the private temp directory to keep state comparisons about
			// recovery evidence, not the backend's first-use directory initialization.
			_, cleanup, err := backend.privateKubeconfig()
			if err != nil {
				t.Fatal(err)
			}
			cleanup()
			before := createBootstrapStateSnapshot(t, store.Root)
			original, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			applyFailure := errors.New("isolated controller apply interrupted")
			image := environmentReleaseImage{Reference: newImage, SourceRevision: strings.Repeat("c", 40)}
			plan := &environmentCreatePlan{Stack: stack, Bootstrap: environmentCreateBootstrapPlan{Image: oldImage}}
			err = recoverServiceAuthorizationControllerStage(t.Context(), cfg, store, plan, e, backend, checkpoint, snapshot, stage, image, func(_ context.Context, gotStage string, got []deploymentplan.Definition) error {
				calls++
				if gotStage != stage || len(got) != 2 {
					t.Fatal("repair selected unexpected stage or resource count")
				}
				for _, d := range got {
					c := serviceAuthorizationControllerContainer(d.Object, stack)
					if c == nil || c["image"] != oldImage {
						t.Fatal("repair lost frozen original listener definitions")
					}
					c["image"] = "caller-mutation"
				}
				if mode == "apply-fails" {
					return applyFailure
				}
				return nil
			})
			success := mode == "first-repair" || mode == "resume-repair" || mode == "apply-fails"
			if !success {
				if err == nil || calls != 0 {
					t.Fatal("unbound recovery reached apply", err, calls)
				}
				if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
					t.Fatal("rejection altered original evidence")
				}
				return
			}
			if calls != 1 || mode == "apply-fails" && !errors.Is(err, applyFailure) || mode != "apply-fails" && err != nil {
				t.Fatal("qualified original repair did not apply once", calls, err)
			}
			saved, err := os.ReadFile(filepath.Join(store.Root, snapshot))
			if err != nil {
				t.Fatal(err)
			}
			if !resume && string(saved) != string(original) {
				t.Fatal("snapshot did not preserve exact original durable journal")
			}
			var savedImage environmentReleaseImage
			if err := readExecutionJSON(filepath.Join(store.Root, filepath.Dir(snapshot), "controller-release.json"), &savedImage); err != nil || savedImage.Reference != newImage || savedImage.SourceRevision != image.SourceRevision {
				t.Fatal("formal repair image proof was not retained", err)
			}
			after, err := os.ReadFile(e.JournalPath)
			if err != nil || string(after) != string(original) {
				t.Fatal("recovery rewrote parent journal before apply", err)
			}
			afterState := createBootstrapStateSnapshot(t, store.Root)
			for path, value := range before {
				if afterState[path] != value {
					t.Fatalf("recovery changed existing evidence: %s", path)
				}
			}
		})
	}
}

func serviceAuthorizationNativeFixture(t *testing.T, store secretStore, namespace string, objects map[string]deploymentplan.Object) environmentKubernetesBackend {
	t.Helper()
	dir := t.TempDir()
	var items []deploymentplan.Object
	for id, object := range objects {
		items = append(items, object)
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json"), mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]any{"namespaces.json": map[string]any{"items": []deploymentplan.Object{{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace, "uid": "44444444-4444-4444-8444-444444444444"}}}}, "inventory.json": map[string]any{"items": items}} {
		if err := os.WriteFile(filepath.Join(dir, name), mustProviderRecoveryJSON(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
set -eu
test "$1" = --kubeconfig && test -f "$2" || exit 71
shift 2
case "$1" in --request-timeout=*) shift;; esac
test "$1" = get || exit 72
shift
case "$1" in
 Deployment) kind="$1"; name="$2"; shift 2; test "$1" = --namespace || exit 73; cat "$RTK_SERVICE_AUTH_OBJECTS/${kind}_${2}_${name}.json" ;;
 namespace) cat "$RTK_SERVICE_AUTH_OBJECTS/namespaces.json" ;;
 *configmap*) case "$*" in *--namespace*) cat "$RTK_SERVICE_AUTH_OBJECTS/inventory.json" ;; *) printf '{"items":[]}' ;; esac ;;
 *) printf '{"items":[]}' ;;
esac
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_SERVICE_AUTH_OBJECTS", dir)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-original-kubeconfig"), RolloutTimeout: time.Second}
}
