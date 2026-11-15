package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type completedAppRoutingFixture struct {
	*nativeCreateStageContract
	backend environmentKubernetesBackend
	runtime environmentCreateBootstrapRuntime
	apply   environmentCreateBootstrapApply
}

func newCompletedAppRoutingFixture(t *testing.T) *completedAppRoutingFixture {
	t.Helper()
	f := newNativeCreateStageContract(t, "normal")
	p := f.parent
	p.Bootstrap = environmentCreateBootstrapPlan{Environment: f.cfg.Environment, Stack: p.Stack, AppRoutingMode: "internal-service", Image: p.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference}
	e, _ := providerExecutionContractFork(t, f.e, p, "isolated-completed-routing")
	e.originalPlanSHA = deploymentplan.Digest(p)
	f.e, f.parent = e, p
	state := f.read(t)
	ns := p.Stack + "-video-cloud"
	service := environmentAppInternalServiceDependencies(map[string]string{"CLOUD_STACK_NAME": p.Stack})[0].Object
	deploymentplan.Map(service["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
	listener := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api-app-pki", "namespace": ns, "uid": "33333333-3333-4333-8333-333333333333", "labels": deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "video-cloud-api-app-pki", "image": p.Bootstrap.Image}}}}}}
	state.Objects[deploymentplan.ID(service)], state.Objects[deploymentplan.ID(listener)] = service, listener
	f.save(t, state)
	raw, err := readBoundedEnvironmentFile(f.store.KubeconfigPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), f.cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: backend}
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		return applyEnvironmentCreateStage(ctx, f.cfg, f.store, &f.parent, e, stage, defs, io.Discard)
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(f.store, "pki/app/issuer.json", deploymentplan.Object{"original": "retained-issuer"}); err != nil {
		t.Fatal(err)
	}
	return &completedAppRoutingFixture{f, backend, runtime, apply}
}

func (f *completedAppRoutingFixture) publish(t *testing.T) {
	t.Helper()
	if err := publishEnvironmentCreateAppRouting(t.Context(), f.cfg, f.parent.Bootstrap, f.runtime, f.apply); err != nil {
		t.Fatal("actual routing producer/native child", err)
	}
}

func TestCompletedOriginalAppRoutingObservesProducerAndResumesWithoutReplay(t *testing.T) {
	f := newCompletedAppRoutingFixture(t)
	if sha, err := readCompletedOriginalAppRouting(t.Context(), f.store, f.e, f.backend); err != nil || sha != "" {
		t.Fatal("unpublished routing must remain absent", err)
	}
	f.publish(t)
	path := filepath.Join(f.store.Root, "pki/app/routing.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, journal := f.read(t), deploymentplan.Digest(f.e.Journal)
	snapshot := createBootstrapStateSnapshot(t, f.store.Root)
	if sha, err := readCompletedOriginalAppRouting(t.Context(), f.store, f.e, f.backend); err != nil || sha != environmentPreflightBytesSHA256(raw) {
		t.Fatal("genuine original completed route was rejected", err)
	}
	// A same-UID listener may have accepted a later selected image. Reading
	// its completed publication must not re-enroll or compare the old image.
	listenerID := "Deployment/" + f.parent.Stack + "-video-cloud/video-cloud-api-app-pki"
	state := f.read(t)
	mqttFoundationPrimary(state.Objects[listenerID])["image"] = "qualified-later-image@sha256:" + strings.Repeat("f", 64)
	f.save(t, state)
	forbiddenApply := func(context.Context, string, []deploymentplan.Definition) error {
		t.Fatal("completed publisher replayed its native child")
		return nil
	}
	if err := publishEnvironmentCreateAppRouting(t.Context(), f.cfg, f.parent.Bootstrap, f.runtime, forbiddenApply); err != nil {
		t.Fatal("completed producer did not observe original publication", err)
	}
	after := f.read(t)
	if after.Writes != before.Writes || after.Leases != before.Leases || after.Releases != before.Releases || after.VolumeReads != before.VolumeReads || deploymentplan.Digest(f.e.Journal) != journal {
		t.Fatal("completed observation changed native writes/leases/capture or original journal")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, actual) {
		t.Fatal("completed publisher rewrote original private routing bytes", err)
	}
	assertRecoveryContractReadOnly(t, f.store, snapshot)
}

func TestCompletedOriginalAppRoutingPublisherDoesNotReplayAcceptedStage(t *testing.T) {
	f := newCompletedAppRoutingFixture(t)
	f.publish(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if err := publishEnvironmentCreateAppRouting(t.Context(), f.cfg, f.parent.Bootstrap, f.runtime, func(context.Context, string, []deploymentplan.Definition) error {
		t.Fatal("completed publisher replayed its accepted native routing child")
		return nil
	}); err != nil {
		t.Fatal("completed producer refused its exact existing proof", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestCompletedOriginalAppRoutingResumesOnlyOriginalLocalPublication(t *testing.T) {
	f := newCompletedAppRoutingFixture(t)
	f.publish(t)
	path := filepath.Join(f.store.Root, "pki/app/routing.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original := mustProviderRecoveryJSON(f.e.Journal)
	for _, mode := range []string{"before-file", "before-proof", "pending-proof", "allocated-proof"} {
		t.Run(mode, func(t *testing.T) {
			if err := json.Unmarshal(original, &f.e.Journal); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			proof := createOpenBaoACKCompletionEntry(f.e.Journal, "native-app-routing-profile-proof")
			if mode == "before-file" || mode == "before-proof" {
				for i, entry := range f.e.Journal.Entries {
					if entry.Key == proof.Key {
						f.e.Journal.Entries = append(f.e.Journal.Entries[:i:i], f.e.Journal.Entries[i+1:]...)
						break
					}
				}
				if mode == "before-file" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				proof.CompletedAt = time.Time{}
				proof.State = "allocated"
				if mode == "pending-proof" {
					proof.State, proof.Receipt = "pending", nil
				}
			}
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			before, journal := f.read(t), deploymentplan.Digest(f.e.Journal)
			sha, err := readCompletedOriginalAppRouting(t.Context(), f.store, f.e, f.backend)
			wantSHA := environmentPreflightBytesSHA256(raw)
			if mode == "before-file" {
				wantSHA = ""
			}
			if err != nil || sha != wantSHA || deploymentplan.Digest(f.e.Journal) != journal {
				t.Fatal("preflight must observe resumable native publication without completing local proof", mode, err)
			}
			if err := publishEnvironmentCreateAppRouting(t.Context(), f.cfg, f.parent.Bootstrap, f.runtime, func(context.Context, string, []deploymentplan.Definition) error {
				t.Fatal("local publication resume replayed native routing stage")
				return nil
			}); err != nil {
				t.Fatal("exact original local routing publication did not resume", mode, err)
			}
			proof = createOpenBaoACKCompletionEntry(f.e.Journal, "native-app-routing-profile-proof")
			if proof == nil || proof.State != "completed" {
				t.Fatal("original local proof was not completed", mode)
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, actual) {
				t.Fatal("local publication bytes changed", mode, err)
			}
			after := f.read(t)
			if before.Writes != after.Writes || before.Leases != after.Leases || before.Releases != after.Releases || before.VolumeReads != after.VolumeReads {
				t.Fatal("local proof resume wrote/reaccepted native child", mode)
			}
			if _, err := readCompletedOriginalAppRouting(t.Context(), f.store, f.e, f.backend); err != nil {
				t.Fatal("resumed publication lacks completed proof", err)
			}
		})
	}
}

func TestCompletedOriginalAppRoutingRejectsMissingTamperedOrForeignPublication(t *testing.T) {
	f := newCompletedAppRoutingFixture(t)
	f.publish(t)
	path := filepath.Join(f.store.Root, "pki/app/routing.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var original environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(f.e.Journal), &original); err != nil {
		t.Fatal(err)
	}
	stateRaw := mustProviderRecoveryJSON(f.read(t))
	plan, progress, err := managedTransportPostAcceptedStage(f.store, f.e, "kubernetes/native-app-routing-profile")
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(f.store.Root, "deployment/rollback", plan.SHA256(), "journal.json")
	childRaw, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-file", "public-file", "extra-file", "symlink-file", "foreign-parent", "foreign-service-binding", "unknown-json", "duplicate-proof", "proof-intent", "proof-receipt", "proof-time", "missing-stage", "incomplete-child", "missing-lease", "foreign-cm-uid", "foreign-cm-body", "mutable-cm", "foreign-service-uid", "foreign-deployment-uid", "deleting-deployment", "service-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(func() {
				_ = os.Remove(path)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(filepath.Join(f.store.Root, "pki/app/extra.json"))
				if err := os.WriteFile(f.state, stateRaw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(childPath, childRaw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(mustProviderRecoveryJSON(original), &f.e.Journal); err != nil {
					t.Fatal(err)
				}
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			})
			proof := createOpenBaoACKCompletionEntry(f.e.Journal, "native-app-routing-profile-proof")
			state := f.read(t)
			ns := f.parent.Stack + "-video-cloud"
			cmID, serviceID, deploymentID := "ConfigMap/"+ns+"/"+environmentAppRoutingDeclaration, "Service/"+ns+"/video-cloud-api-app-pki", "Deployment/"+ns+"/video-cloud-api-app-pki"
			switch mode {
			case "missing-file":
				_ = os.Remove(path)
			case "public-file":
				_ = os.Chmod(path, 0644)
			case "extra-file":
				_ = os.WriteFile(filepath.Join(f.store.Root, "pki/app/extra.json"), []byte("foreign"), 0600)
			case "symlink-file":
				outside := filepath.Join(t.TempDir(), "routing.json")
				if err := os.WriteFile(outside, raw, 0600); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(path)
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "foreign-parent", "foreign-service-binding", "unknown-json":
				binding := jsonRawObject(raw)
				if mode == "foreign-parent" {
					binding["create_plan_sha256"] = strings.Repeat("9", 64)
				} else if mode == "foreign-service-binding" {
					binding["service_uid"] = "99999999-9999-4999-8999-999999999999"
				} else {
					binding["unqualified"] = true
				}
				if err := os.WriteFile(path, mustProviderRecoveryJSON(binding), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-proof", "missing-stage":
				key := "native-app-routing-profile-proof"
				if mode == "missing-stage" {
					key = "kubernetes/native-app-routing-profile"
				}
				for i, entry := range f.e.Journal.Entries {
					if entry.Key == key {
						f.e.Journal.Entries = append(f.e.Journal.Entries[:i:i], f.e.Journal.Entries[i+1:]...)
						break
					}
				}
			case "duplicate-proof":
				f.e.Journal.Entries = append(f.e.Journal.Entries, *proof)
			case "proof-intent":
				proof.IntentSHA256 = strings.Repeat("9", 64)
			case "proof-receipt":
				proof.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"foreign": true})
			case "proof-time":
				proof.CompletedAt = proof.StartedAt.Add(-time.Second)
			case "incomplete-proof":
				proof.State, proof.CompletedAt = "allocated", time.Time{}
			case "incomplete-child":
				progress.Accepted = false
				if err := os.WriteFile(childPath, mustProviderRecoveryJSON(progress), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-lease":
				f.e.Journal.KubernetesLeases = nil
			case "foreign-cm-uid":
				deploymentplan.Map(state.Objects[cmID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "foreign-cm-body":
				deploymentplan.Map(state.Objects[cmID]["data"])["binding.json"] = "foreign"
			case "mutable-cm":
				state.Objects[cmID]["immutable"] = false
			case "foreign-service-uid":
				deploymentplan.Map(state.Objects[serviceID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "foreign-deployment-uid":
				deploymentplan.Map(state.Objects[deploymentID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "deleting-deployment":
				deploymentplan.Map(state.Objects[deploymentID]["metadata"])["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			case "service-endpoint":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(state.Objects[serviceID]["spec"])["ports"])[0])["targetPort"] = float64(9443)
			}
			f.save(t, state)
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if _, err := readCompletedOriginalAppRouting(t.Context(), f.store, f.e, f.backend); err == nil {
				t.Fatal("unproven original routing publication accepted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
