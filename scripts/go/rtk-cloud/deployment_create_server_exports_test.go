package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateOwnedServerExportFencesExactPVCReaderAndNativePod(t *testing.T) {
	for _, mode := range []string{"success", "omitted-readonly", "readonly-drift", "foreign-stack", "foreign-parent", "foreign-backend", "foreign-claim", "missing-pvc", "foreign-pvc-label", "pvc-before", "pvc-after", "reader-before", "reader-after", "reader-recipe-after", "foreign-pod-controller", "pod-after", "replicaset-after", "selector-collision", "missing-pod", "malformed-pod-list", "export-failure", "private-malformed-output"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			_, p, _, service := createServiceRecipeFixture(t, "dev")
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			execution, err := startEnvironmentCreateExecution(ctx, cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { execution.Close() })
			owner := environmentCreateApplicationOwners()[1]
			namespace := p.Stack + "-video-cloud"
			serverName := owner.Name + "." + namespace + ".svc"
			serverState := filepath.Join(filepath.Dir(owner.ClientState), "server.json")
			runtime := environmentCreateBootstrapRuntime{Execution: execution, ExecutionSHA256: artifact.PlanSHA256, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-frozen-export-kubeconfig")}}
			fixture := t.TempDir()
			writeJSON := func(name string, object any) {
				t.Helper()
				body, err := json.Marshal(object)
				if err != nil || os.WriteFile(filepath.Join(fixture, name), body, 0600) != nil {
					t.Fatal("cannot prepare isolated native export response")
				}
			}
			claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": owner.Claim, "namespace": namespace, "uid": "original-claim-uid", "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}}}
			if mode == "foreign-pvc-label" {
				deploymentplan.Map(deploymentplan.Map(claim["metadata"])["labels"])["rtk.realtek.com/stack"] = "video-cloud-staging"
			}
			writeJSON("claim.json", claim)
			changedClaim := deploymentplan.Clone(claim)
			deploymentplan.Map(changedClaim["metadata"])["uid"] = "replacement-claim-uid"
			writeJSON("claim-changed.json", changedClaim)
			if mode == "missing-pvc" {
				if err := os.Remove(filepath.Join(fixture, "claim.json")); err != nil {
					t.Fatal(err)
				}
			}
			// Native exporter validation belongs to its independently tested
			// registry/identity implementation. This bounded adapter fixture
			// supplies a coherent synthetic pair solely to test owner fences.
			certEncoded, keyEncoded := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth)
			certificate, _ := base64.StdEncoding.DecodeString(certEncoded)
			privateKey, _ := base64.StdEncoding.DecodeString(keyEncoded)
			material := environmentCreateOwnedServerMaterial{Certificate: string(certificate) + string(certificate), Key: string(privateKey), Root: service.Root.RootFingerprint, Name: serverName, Environment: cfg.Environment}
			writeJSON("export.json", material)
			if mode == "private-malformed-output" {
				if err := os.WriteFile(filepath.Join(fixture, "export.json"), []byte("private-native-export-error-marker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/sh
set -eu
case "$*" in
  *' get PersistentVolumeClaim '*)
    test ! -e "$RTK_EXPORT_FIXTURE/claim.json" || cat "$RTK_EXPORT_FIXTURE/claim.json" ;;
  *' get Deployment '*)
    count=0
    if [ -e "$RTK_EXPORT_FIXTURE/reader-count" ]; then count=$(cat "$RTK_EXPORT_FIXTURE/reader-count"); fi
    count=$((count + 1)); printf '%s' "$count" > "$RTK_EXPORT_FIXTURE/reader-count"
    if [ "$RTK_EXPORT_MODE" = reader-before ] && [ "$count" -eq 2 ]; then cp "$RTK_EXPORT_FIXTURE/reader-changed.json" "$RTK_EXPORT_FIXTURE/reader.json"; fi
    test ! -e "$RTK_EXPORT_FIXTURE/reader.json" || cat "$RTK_EXPORT_FIXTURE/reader.json" ;;
  *' get pods '*) cat "$RTK_EXPORT_FIXTURE/pods.json" ;;
  *' get Pod '*) cat "$RTK_EXPORT_FIXTURE/pod.json" ;;
  *' get ReplicaSet '*) cat "$RTK_EXPORT_FIXTURE/rs.json" ;;
  *' exec pod/'*)
    printf 'native-pod-export\n' >> "$RTK_EXPORT_FIXTURE/exec-calls"
    case "$RTK_EXPORT_MODE" in
      pvc-after) cp "$RTK_EXPORT_FIXTURE/claim-changed.json" "$RTK_EXPORT_FIXTURE/claim.json" ;;
      reader-after|reader-recipe-after) cp "$RTK_EXPORT_FIXTURE/reader-changed.json" "$RTK_EXPORT_FIXTURE/reader.json" ;;
      pod-after) cp "$RTK_EXPORT_FIXTURE/pod-changed.json" "$RTK_EXPORT_FIXTURE/pod.json" ;;
      replicaset-after) cp "$RTK_EXPORT_FIXTURE/rs-changed.json" "$RTK_EXPORT_FIXTURE/rs.json" ;;
      export-failure) printf 'private-native-export-error-marker' >&2; exit 1 ;;
    esac
    cat "$RTK_EXPORT_FIXTURE/export.json" ;;
  *) exit 99 ;;
esac
`
			kubectl := filepath.Join(fixture, "kubectl")
			if err := os.WriteFile(kubectl, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
			t.Setenv("RTK_EXPORT_FIXTURE", fixture)
			t.Setenv("RTK_EXPORT_MODE", mode)
			applyCalls, retirementCalls := 0, 0
			apply := environmentCreateBootstrapApply(func(_ context.Context, _ string, definitions []deploymentplan.Definition) error {
				applyCalls++
				if len(definitions) != 1 {
					t.Fatal("export adapter applied an unrelated resource")
				}
				definition := definitions[0]
				body := deploymentplan.Clone(definition.Object)
				if definition.Retire {
					// Exercise the real planner contract as well as the native
					// adapter; an invented cleanup phase must not pass this fixture.
					if _, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, definitions, []deploymentplan.Object{body}); err != nil {
						return err
					}
					retirementCalls++
					return os.Remove(filepath.Join(fixture, "reader.json"))
				}
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), material.Key) || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(privateKey)) {
					t.Fatal("reader recipe contained native serving key material")
				}
				meta := deploymentplan.Map(body["metadata"])
				meta["uid"] = "original-reader-uid"
				writeJSON("reader.json", body)
				changed := deploymentplan.Clone(body)
				deploymentplan.Map(changed["metadata"])["uid"] = "replacement-reader-uid"
				if mode == "reader-recipe-after" {
					deploymentplan.Map(changed["metadata"])["uid"] = meta["uid"]
					deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed)["containers"])[0])["image"] = "foreign-reader-image"
				}
				writeJSON("reader-changed.json", changed)
				name := deploymentplan.String(meta["name"])
				rsName := name + "-owned"
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": rsName + "-pod", "namespace": namespace, "uid": "original-pod-uid", "labels": deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(body["spec"])["template"])["metadata"])["labels"], "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "name": rsName, "uid": "original-rs-uid", "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(body)), "status": deploymentplan.Object{"phase": "Running"}}
				if mode == "omitted-readonly" || mode == "readonly-drift" {
					mount := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["volumeMounts"])[0])
					if mount["readOnly"] != false {
						t.Fatal("native original owner-state fixture has no explicit writable mount")
					}
					delete(mount, "readOnly")
					if mode == "readonly-drift" {
						mount["readOnly"] = true
					}
				}
				rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": rsName, "namespace": namespace, "uid": "original-rs-uid", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": name, "uid": meta["uid"], "controller": true}}}}
				if mode == "foreign-pod-controller" {
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])[0])["uid"] = "foreign-deployment-uid"
				}
				writeJSON("pod.json", pod)
				writeJSON("rs.json", rs)
				changedPod, changedRS := deploymentplan.Clone(pod), deploymentplan.Clone(rs)
				deploymentplan.Map(changedPod["metadata"])["uid"] = "replacement-pod-uid"
				deploymentplan.Map(changedRS["metadata"])["uid"] = "replacement-rs-uid"
				writeJSON("pod-changed.json", changedPod)
				writeJSON("rs-changed.json", changedRS)
				items := []deploymentplan.Object{pod}
				if mode == "selector-collision" {
					items = append(items, changedPod)
				}
				if mode == "missing-pod" {
					items = nil
				}
				// Match the complete real kubectl List envelope, including API
				// metadata that is unrelated to the exact selected owner fences.
				writeJSON("pods.json", map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{"resourceVersion": "42"}, "items": items})
				if mode == "malformed-pod-list" {
					if err := os.WriteFile(filepath.Join(fixture, "pods.json"), []byte("{\"items\":["), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "pvc-before" {
					writeJSON("claim.json", changedClaim)
				}
				return nil
			})
			selectedClaim := owner.Claim
			switch mode {
			case "foreign-stack":
				p.Stack = "video-cloud-staging"
			case "foreign-parent":
				runtime.ExecutionSHA256 = strings.Repeat("f", 64)
			case "foreign-backend":
				runtime.Backend.Store.Root += "-foreign"
			case "foreign-claim":
				selectedClaim += "-foreign"
			}
			actual, err := readEnvironmentCreateOwnedServer(ctx, cfg, p, runtime, service, namespace, owner.Name, owner.Role, selectedClaim, owner.StateRoot, owner.ClientState, serverState, serverName, apply)
			if mode == "success" || mode == "omitted-readonly" {
				if err != nil || !reflect.DeepEqual(actual, material) || applyCalls != 2 || retirementCalls != 1 {
					t.Fatal("exact native Pod export did not complete and retire its reader", err)
				}
			} else if err == nil || !reflect.DeepEqual(actual, environmentCreateOwnedServerMaterial{}) || retirementCalls != 0 || strings.Contains(err.Error(), "private-native-export-error-marker") || strings.Contains(err.Error(), string(privateKey)) {
				t.Fatal("foreign or uncertain native owner exported private serving material or leaked diagnostics", err)
			}
			if mode != "success" && mode != "omitted-readonly" && len(execution.Journal.Entries) != 0 {
				t.Fatal("failed original-owner checks fabricated an export receipt")
			}
			if err := filepath.WalkDir(store.Root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(body), material.Key) || strings.Contains(string(body), base64.StdEncoding.EncodeToString(privateKey)) || strings.Contains(string(body), "private-native-export-error-marker") {
					t.Fatal("native export payload escaped into local adapter evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
