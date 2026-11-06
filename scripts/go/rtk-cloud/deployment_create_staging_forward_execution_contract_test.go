package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Qualification tests cover the complete enrollment Job/PVC recipe. This
// contract uses the existing native stage's ConfigMap payload to isolate the
// forward coordinator: real API writes, leases, acceptance and parent receipts.
func TestStagingEnrollmentForwardExecutionPublishesOnlyAcceptedChild(t *testing.T) {
	for _, kind := range []string{"core", "server"} {
		for _, mode := range []string{"accepted", "response-lost"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newNativeCreateStageContract(t, mode)
				stage := stagingCoreEnrollmentStage
				if kind == "server" {
					stage = "server-enrollment-video-cloud-api-pki"
				}
				job := stagingQualificationJob(f.parent.Stack+"-video-cloud", "original-failed-enrollment", "original-service-state")
				original, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.parent.Stack, Operation: "scoped", ConfigurationSHA256: f.parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.parent.Release), BindingsSHA256: deploymentplan.Digest("original-enrollment")}, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				failedID := original.Resources[0].ID
				uid := "44444444-4444-4444-8444-444444444444"
				originalJournal := deploymentplan.Journal{PlanSHA256: original.SHA256(), Owners: map[string]string{failedID: uid}, Failed: failedID}
				for name, value := range map[string]any{"plan.json": original, "journal.json": originalJournal} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := f.store.write(filepath.Join("deployment/rollback", original.SHA256(), name), raw, false); err != nil {
						t.Fatal(err)
					}
				}
				if err := saveEnvironmentCreateStageInput(t.Context(), f.store, f.e, stage, original); err != nil {
					t.Fatal(err)
				}
				intent, err := environmentCreateKubernetesStageIntent(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.e.BeginEntry("kubernetes/"+stage, stage, intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				state := f.read(t)
				state.Objects[failedID] = stagingQualificationFailedJob(original.Resources[0].Desired, uid)
				f.save(t, state)
				beforeJob := deploymentplan.Digest(state.Objects[failedID])
				journalPath := filepath.Join(f.store.Root, "deployment/rollback", original.SHA256(), "journal.json")
				beforeJournal, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				run := func(ctx context.Context) error {
					if kind == "core" {
						return executeStagingServiceCoreForward(ctx, f.cfg, f.store, &f.parent, f.e, &stagingServiceCoreForward{OriginalSHA: original.SHA256(), JournalSHA: deploymentplan.Digest(originalJournal), FailedJob: failedID, FailedUID: uid, Definitions: f.definitions})
					}
					return executeStagingServerEnrollmentForward(ctx, f.cfg, f.store, &f.parent, f.e, &stagingServerEnrollmentForward{Stage: stage, Original: original.SHA256(), FailedUID: uid, Definition: f.definitions[0]})
				}
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				err = run(ctx)
				parentEntry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stage)
				forwardStage := stage + "-rwo-" + original.SHA256()[:12]
				child, readErr := readEnvironmentCreateStageInput(f.store, f.e, forwardStage)
				if readErr != nil || child == nil {
					t.Fatal("actual child input missing", readErr)
				}
				if mode == "accepted" {
					if err != nil {
						t.Fatal(err)
					}
					_, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+forwardStage)
					if err != nil || !j.Accepted {
						t.Fatal("completed source has no accepted child", err)
					}
					var receipt map[string]string
					if json.Unmarshal(parentEntry.Receipt, &receipt) != nil || parentEntry.State != "completed" || parentEntry.CompletedAt.IsZero() || receipt["actual_qualified_forward_stage"] != "kubernetes/"+forwardStage || receipt["original_failed_plan_sha256"] != original.SHA256() || receipt["original_failed_job_uid"] != uid {
						t.Fatal("forward completion lost original lineage", parentEntry)
					}
					if kind == "core" && (receipt["original_failed_journal_sha256"] != deploymentplan.Digest(originalJournal) || receipt["original_failed_job"] != failedID) {
						t.Fatal("core receipt lost original failed Job or journal")
					}
				} else {
					if err == nil || parentEntry.State != "pending" || len(parentEntry.Receipt) != 0 {
						t.Fatal("unknown child write completed source", err)
					}
					var j deploymentplan.Journal
					if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", child.SHA256(), "journal.json"), &j); err != nil || j.InFlight == "" || j.Accepted {
						t.Fatal("unknown native outcome was not retained", err)
					}
					writes := f.read(t).Writes
					if err := run(ctx); err == nil || f.read(t).Writes != writes {
						t.Fatal("unknown write was replayed", err)
					}
				}
				afterJournal, err := os.ReadFile(journalPath)
				if err != nil || !bytes.Equal(beforeJournal, afterJournal) || deploymentplan.Digest(f.read(t).Objects[failedID]) != beforeJob {
					t.Fatal("forward changed original failure evidence", err)
				}
				live := f.read(t)
				if live.Writes != 1 || live.Leases != live.Releases {
					t.Fatal("forward did not preserve one owned write and released leases", live.Writes, live.Leases, live.Releases)
				}
			})
		}
	}
}

func TestStagingManagedHostsForwardExecutionCompletesOnlyOwnedCutover(t *testing.T) {
	for _, mode := range []string{"accepted", "child-failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "staging")
			roots["service"], intermediates["service"] = service.Root, service.Intermediate
			for i := range p.Domains {
				if p.Domains[i].Domain == "device" {
					p.Domains[i].BundleConsumers = append(p.Domains[i].BundleConsumers, "pkibroker")
				}
			}
			f := newStagingQualificationFixture(t)
			runtime.Execution, runtime.ExecutionSHA256, runtime.Backend.Store = f.e, f.e.Journal.PlanSHA256, f.store
			for _, entry := range rtkSecretCatalog() {
				if err := f.store.write(filepath.Join("runtime", entry.ID), []byte("isolated-existing-"+entry.ID), false); err != nil {
					t.Fatal(err)
				}
			}
			definitions, err := renderEnvironmentCreateTransportDefinitions(t.Context(), cfg, p, runtime, service, roots, intermediates, release)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := f.stage(t, stagingManagedHostsStage, definitions, false)
			// This recovery starts at an unwritten admission; only its immutable
			// source input exists, with no native child journal or lease.
			for _, name := range []string{"plan.json", "journal.json"} {
				if err := os.Remove(filepath.Join(f.store.Root, "deployment/rollback", original.SHA256(), name)); err != nil {
					t.Fatal(err)
				}
			}
			f.e.Journal.KubernetesLeases = f.e.Journal.KubernetesLeases[:len(f.e.Journal.KubernetesLeases)-1]
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			native := freshStageBackend{&normalTailE2EBackend{e: f.e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
			openbaoID := "StatefulSet/" + p.Stack + "-secrets/openbao"
			originalUID := "11111111-1111-4111-8111-111111111111"
			for i := range definitions {
				d := &definitions[i]
				if deploymentplan.ID(d.Object) == openbaoID {
					old := deploymentplan.Clone(d.Object)
					deploymentplan.Map(old["metadata"])["uid"] = originalUID
					deploymentplan.Map(old["metadata"])["resourceVersion"] = "1"
					container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(old)["containers"])[0])
					container["image"] = "original-bootstrap-image"
					native.objects[openbaoID] = old
					deploymentplan.Map(d.Object["metadata"])["uid"] = originalUID
				}
			}
			if native.objects[openbaoID] == nil {
				t.Fatal("real transport recipe omitted OpenBao")
			}
			claimsBefore := deploymentplan.Digest(deploymentplan.Map(native.objects[openbaoID]["spec"])["volumeClaimTemplates"])
			forward := &stagingManagedHostsForward{Original: original.SHA256(), Definitions: definitions, Roots: roots, Intermediates: intermediates}
			recovery := &createServiceAuthorizationRecovery{ManagedHosts: forward, Enrollment: &service}
			forwardStage := stagingManagedHostsStage + "-retained-claims-" + original.SHA256()[:12]
			stopped := errors.New("isolated next server enrollment boundary")
			calls := []string{}
			apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				calls = append(calls, stage)
				if stage != forwardStage {
					if stage != "server-enrollment-video-cloud-api-pki" {
						t.Fatalf("unexpected continuation stage %s", stage)
					}
					return stopped
				}
				if deploymentplan.Digest(defs) != deploymentplan.Digest(definitions) {
					t.Fatal("executor replaced qualified cutover definitions")
				}
				live := []deploymentplan.Object{}
				for _, o := range native.objects {
					live = append(live, o)
				}
				child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: f.parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live)
				if err != nil {
					return err
				}
				if mode == "child-failure" {
					native.failure = "unknown-write"
				}
				return executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, child, native, func(ctx context.Context, p *deploymentplan.Plan) error {
					return p.AdmitResumable(ctx, native, f.store.Root)
				}, func(ctx context.Context, p *deploymentplan.Plan) error {
					return native.CheckParentExecution(ctx, p, f.store.Root)
				}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, native) }, func(ctx context.Context) error { return f.e.Check(ctx) }, io.Discard)
			}
			journalPath := environmentCreateStageInputPath(f.e, stagingManagedHostsStage)
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			_, err = executeStagingManagedHostsForward(ctx, cfg, p, release, f.store, nil, runtime, recovery, apply)
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stagingManagedHostsStage)
			if mode == "accepted" {
				if !errors.Is(err, stopped) {
					t.Fatal("cutover did not reach next server enrollment", err, calls)
				}
				var receipt map[string]string
				if json.Unmarshal(entry.Receipt, &receipt) != nil || entry.State != "completed" || receipt["actual_qualified_forward_stage"] != "kubernetes/"+forwardStage || receipt["original_failed_plan_sha256"] != original.SHA256() {
					t.Fatal("cutover source completion lost accepted forward lineage", entry)
				}
				_, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+forwardStage)
				if err != nil || j.Owners[openbaoID] != originalUID {
					t.Fatal("accepted cutover replaced original OpenBao owner", err)
				}
				actual := native.objects[openbaoID]
				if deploymentplan.Map(actual["metadata"])["uid"] != originalUID || deploymentplan.Digest(deploymentplan.Map(actual["spec"])["volumeClaimTemplates"]) != claimsBefore {
					t.Fatal("cutover replaced original claims")
				}
				forward.AlreadyAccepted = true
				prior := deploymentplan.Digest(native.writes)
				calls = nil
				if _, err := executeStagingManagedHostsForward(ctx, cfg, p, release, f.store, nil, runtime, recovery, apply); !errors.Is(err, stopped) || deploymentplan.Digest(native.writes) != prior || len(calls) != 1 {
					t.Fatal("accepted managed cutover was replayed", err, calls)
				}
			} else if err == nil || !strings.Contains(err.Error(), "unknown-write") || entry.State != "pending" || len(entry.Receipt) != 0 || len(calls) != 1 {
				t.Fatal("unknown cutover write completed original stage or advanced", err, calls)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("cutover changed original immutable stage input", err)
			}
		})
	}
}
