package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const normalTailNativeContractStage = "normal-tail-config-contract"

// This is the explicit admitted preparation-stage boundary, not qualification
// of the complete Redis/product/routing history. The artifact declares frozen
// product outputs, not issued identities. The real stage wrapper compiles its
// original plan, fences the held parent, acquires/releases native leases, writes
// and observes Kubernetes, and captures volumes through the native API model.
func newNormalTailNativeExecutionContract(t *testing.T, mode string) (*nativeCreateStageContract, *createNormalTailHandoff) {
	t.Helper()
	f := newNativeCreateStageContract(t, mode, func(architecture string) {
		raw := readTestFile(t, architecture)
		raw = strings.Replace(raw, "MQTT_FOUNDATION_REGISTRATION_ENABLED=false", "MQTT_FOUNDATION_REGISTRATION_ENABLED=true", 1)
		writeTestFile(t, architecture, strings.Replace(raw, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED=false", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED=true", 1))
	})
	outputs, err := environmentCreateProductIdentityIntent(f.cfg)
	if err != nil || len(outputs) == 0 {
		t.Fatal("selected maintained product output declarations", err)
	}
	h := &createNormalTailHandoff{Execution: f.e, OriginalPlan: &f.parent, Pins: map[string]string{"declared-stage-source": deploymentplan.Digest(f.read(t).Objects)}}
	p := createNormalTailProgram{
		Schema: createNormalTailProgramSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner),
		HandoffSHA256: deploymentplan.Digest(h.Pins), ConfigurationSHA256: deploymentplan.Digest(f.cfg), MaintainedConfigSHA256: deploymentplan.Digest(f.cfg),
		ClusterSHA256: deploymentplan.Digest(f.read(t).Objects), IdentitySHA256: deploymentplan.Digest(outputs), Release: f.parent.Release,
		Bindings: environmentIdentityBindings{Version: 1, Environment: f.cfg.Environment, Stack: f.parent.Stack, Owners: map[string]string{}}, Definitions: f.definitions,
	}
	p.Products = &createNormalTailProductLifecycle{ParentSHA256: p.ParentSHA256, OwnerSHA256: p.OwnerSHA256, SourceSHA256: deploymentplan.Digest(f.e.Journal), Outputs: outputs, SourceDefinitions: p.Definitions}
	now := time.Now().UTC()
	digest := deploymentplan.Digest("explicit isolated historical admission")
	a := createNormalTailProgramArtifact{Program: p, Generation: digest, CheckerSHA256: digest, QualifiedAt: now, ValidUntil: now.Add(time.Hour), Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: deploymentplan.Digest(p)}}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: a.Qualification.PlanSHA256})
	}
	if err := saveCreateNormalTailProgramArtifact(f.store, f.e, a); err != nil {
		t.Fatal(err)
	}
	return f, h
}

func applyNormalTailNativeContract(t *testing.T, f *nativeCreateStageContract, h *createNormalTailHandoff) error {
	t.Helper()
	return applyCreateNormalTailStage(withCreateNormalTailHandoff(t.Context(), h), f.cfg, f.store, &f.parent, f.e, normalTailNativeContractStage, f.definitions, io.Discard)
}

func readNormalTailNativeContract(t *testing.T, f *nativeCreateStageContract, completed bool) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	p, err := readCreateNormalTailPreparationPlan(f.store, f.e, normalTailNativeContractStage)
	if err != nil || p == nil || len(p.Resources) != 1 {
		t.Fatal("original preparation plan", err)
	}
	j, exists, err := p.ReadExecution(f.store.Root)
	if err != nil || !exists || !j.Accepted || len(j.Completed) != 1 || len(j.Owners) != 1 {
		t.Fatal("genuine accepted child", err)
	}
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+normalTailNativeContractStage)
	state := f.read(t)
	want := "allocated"
	if completed {
		want = "completed"
	}
	if entry == nil || entry.State != want || state.Writes != 1 || state.Leases == 0 || state.Leases != state.Releases || len(state.Objects) != 2 || j.Owners[p.Resources[0].ID] != deploymentplan.Map(state.Objects[p.Resources[0].ID]["metadata"])["uid"] {
		t.Fatal("native child/parent receipt/UID/lease mismatch", entry, state.Writes, state.Leases, state.Releases)
	}
	if err := f.e.Check(t.Context()); err != nil {
		t.Fatal("original parent fence", err)
	}
	return p, j
}

func TestNormalTailNativePreparationPreservesCompiledOwnerOnRetry(t *testing.T) {
	f, h := newNormalTailNativeExecutionContract(t, "normal")
	if err := applyNormalTailNativeContract(t, f, h); err != nil {
		t.Fatal("actual preparation", err)
	}
	p, j := readNormalTailNativeContract(t, f, true)
	before := f.read(t)
	originalPlan := readTestFile(t, createNormalTailStagePlanPath(f.e, normalTailNativeContractStage))
	originalReceipt := append([]byte(nil), createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+normalTailNativeContractStage).Receipt...)
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil || before.VolumeReads != 1 || ledger.UpdatedAt.IsZero() || len(ledger.Volumes) != 0 {
		t.Fatal("actual empty allocation capture", err)
	}
	if err := applyNormalTailNativeContract(t, f, h); err != nil {
		t.Fatal("completed retry", err)
	}
	retained, owners := readNormalTailNativeContract(t, f, true)
	after := f.read(t)
	if retained.SHA256() != p.SHA256() || deploymentplan.Digest(owners.Owners) != deploymentplan.Digest(j.Owners) || after.Writes != before.Writes || after.VolumeReads != before.VolumeReads || readTestFile(t, createNormalTailStagePlanPath(f.e, normalTailNativeContractStage)) != originalPlan || !bytes.Equal(originalReceipt, createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+normalTailNativeContractStage).Receipt) {
		t.Fatal("retry replaced compiled input, original ownership, capture, or completed receipt")
	}
	for _, mode := range []string{"configuration", "definitions", "release", "native-uid", "native-body", "lost-plan", "malformed-plan", "handoff"} {
		t.Run(mode, func(t *testing.T) {
			native := f.read(t)
			oldDefinitions, oldRelease, oldPins := f.definitions, f.parent.Release, h.Pins
			t.Cleanup(func() {
				f.save(t, native)
				f.definitions, f.parent.Release, h.Pins = oldDefinitions, oldRelease, oldPins
			})
			switch mode {
			case "configuration":
				original := readTestFile(t, f.architecture)
				writeTestFile(t, f.architecture, original+"\nDEPLOYMENT_UPGRADE_WINDOW_SECONDS=3601\n")
				t.Cleanup(func() { writeTestFile(t, f.architecture, original) })
			case "definitions":
				f.definitions = []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Clone(oldDefinitions[0].Object)}}
				deploymentplan.Map(f.definitions[0].Object["data"])["selected"] = "changed"
			case "release":
				f.parent.Release = environmentRelease{}
				if deploymentplan.Digest(f.parent.Release) == deploymentplan.Digest(oldRelease) {
					t.Fatal("original release fixture must be populated")
				}
			case "native-uid", "native-body":
				state := f.read(t)
				object := state.Objects[p.Resources[0].ID]
				if mode == "native-uid" {
					deploymentplan.Map(object["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
				} else {
					deploymentplan.Map(object["data"])["selected"] = "foreign"
				}
				f.save(t, state)
			case "lost-plan", "malformed-plan":
				path := createNormalTailStagePlanPath(f.e, normalTailNativeContractStage)
				if mode == "lost-plan" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(path, []byte(originalPlan), 0600); err != nil {
						t.Fatal(err)
					}
				})
			case "handoff":
				h.Pins = map[string]string{"changed": deploymentplan.Digest("foreign source")}
			}
			writes := f.read(t).Writes
			err := applyNormalTailNativeContract(t, f, h)
			cause := map[string]string{
				"configuration": "preparation inputs changed", "definitions": "preparation scope differs", "release": "preparation scope differs",
				"native-uid": "a recorded write owner or desired body changed", "native-body": "a recorded write owner or desired body changed",
				"lost-plan": "started preparation lost its original compiled plan", "malformed-plan": "original preparation input cannot be read",
				"handoff": "workload plan differs from its authenticated handoff",
			}[mode]
			if err == nil || !strings.Contains(err.Error(), cause) {
				t.Fatal("changed original preparation did not reach its exact refusal", mode, err)
			}
			if f.read(t).Writes != writes || !bytes.Equal(originalReceipt, createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+normalTailNativeContractStage).Receipt) {
				t.Fatal("refusal overwrote native state or original completion")
			}
		})
	}
}

func TestNormalTailNativePreparationCaptureAndUnknownWriteOutcomes(t *testing.T) {
	for _, mode := range []string{"capture-failure", "response-lost"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newNormalTailNativeExecutionContract(t, mode)
			if err := applyNormalTailNativeContract(t, f, h); err == nil {
				t.Fatal("injected native interruption succeeded")
			}
			before := f.read(t)
			if before.Writes != 1 {
				t.Fatal("original native write did not occur exactly once")
			}
			if mode == "capture-failure" {
				p, j := readNormalTailNativeContract(t, f, false)
				if err := applyNormalTailNativeContract(t, f, h); err != nil {
					t.Fatal("known accepted child/capture retry", err)
				}
				again, actual := readNormalTailNativeContract(t, f, true)
				if p.SHA256() != again.SHA256() || deploymentplan.Digest(j.Owners) != deploymentplan.Digest(actual.Owners) || f.read(t).VolumeReads != before.VolumeReads+1 {
					t.Fatal("capture retry rebuilt or replayed child")
				}
			} else {
				p, err := readCreateNormalTailPreparationPlan(f.store, f.e, normalTailNativeContractStage)
				if err != nil || p == nil {
					t.Fatal(err)
				}
				if _, _, err := p.ReadExecution(f.store.Root); err == nil {
					t.Fatal("accepted reader promoted unknown native evidence")
				}
				journalPath := filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json")
				original, err := os.ReadFile(journalPath)
				var j deploymentplan.Journal
				if err != nil || json.Unmarshal(original, &j) != nil || j.Accepted || j.InFlight != p.Resources[0].ID || len(j.Owners) != 0 {
					t.Fatal("unknown write was promoted to known owner", err)
				}
				if err := applyNormalTailNativeContract(t, f, h); err == nil {
					t.Fatal("ambiguous native write retried")
				}
				final, err := os.ReadFile(journalPath)
				if err != nil || !bytes.Equal(original, final) || f.read(t).Writes != before.Writes || f.read(t).Leases != before.Leases {
					t.Fatal("unknown write was replayed or original evidence changed", err)
				}
			}
		})
	}
}

func TestNormalTailDNSQualificationObservesOriginalPublication(t *testing.T) {
	for _, mode := range []string{"unstarted", "completed", "allocated", "pending", "unowned-A", "unowned-AAAA", "unowned-CNAME", "changed-address", "conflicting-CNAME", "changed-intent", "changed-phase", "changed-receipt", "denied", "missing-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateDNSExecutionContract(t)
			var parent environmentCreatePlan
			a, err := loadEnvironmentCreateArtifact(f.cfg, deploymentplan.Digest(f.operator), f.e.Journal.PlanSHA256, time.Now().UTC())
			if err != nil || json.Unmarshal(a.Plan, &parent) != nil {
				t.Fatal("original DNS plan", err)
			}
			h := &createNormalTailHandoff{Execution: f.e, OriginalPlan: &parent, Receipts: f.receipts}
			published := mode == "completed" || mode == "allocated" || mode == "pending" || strings.HasPrefix(mode, "changed-") || mode == "conflicting-CNAME"
			if published {
				f.failReadback, f.loseWriteResponse = mode == "allocated", mode == "pending"
				ctx := context.WithValue(t.Context(), environmentCreateBootstrapRuntimeKey{}, environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256})
				err := executeEnvironmentCreateDNS(ctx, f.cfg, f.plan, f.receipts, f.operator, f.e)
				if (err != nil) != (mode == "allocated" || mode == "pending") {
					t.Fatal("actual DNS producer", err)
				}
			}
			first := f.plan.Records[0]
			key := "dns/" + first.Type + "/" + first.Name
			switch mode {
			case "unowned-A", "unowned-AAAA", "unowned-CNAME":
				f.records[f.key(first, strings.TrimPrefix(mode, "unowned-"))] = []string{"occupied"}
			case "changed-address":
				f.records[f.key(first, first.Type)] = []string{"198.51.100.99"}
			case "conflicting-CNAME":
				f.records[f.key(first, "CNAME")] = []string{"foreign.example.invalid"}
			case "changed-intent", "changed-phase", "changed-receipt":
				for i := range f.e.Journal.Entries {
					entry := &f.e.Journal.Entries[i]
					if entry.Key != key {
						continue
					}
					switch mode {
					case "changed-intent":
						entry.IntentSHA256 = deploymentplan.Digest("foreign DNS intent")
					case "changed-phase":
						entry.Phase = "workload"
					case "changed-receipt":
						entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"name": "foreign"})
					}
				}
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			case "denied":
				f.deny = true
			case "missing-owner":
				h.Execution = nil
			}
			before, puts := createBootstrapStateSnapshot(t, f.store.Root), f.puts
			err = qualifyCreateNormalTailDNS(t.Context(), f.cfg, h, f.operator)
			want := mode == "unstarted" || mode == "completed" || mode == "allocated"
			if (err == nil) != want {
				t.Fatal("unexpected continuation qualification", err)
			}
			if !want && mode != "denied" {
				cause := "DNS request outcome is uncertain or differs"
				switch mode {
				case "unowned-A", "unowned-AAAA", "unowned-CNAME", "conflicting-CNAME":
					cause = "unrecorded DNS owner is occupied"
				case "changed-address":
					cause = "recorded DNS target changed"
				case "missing-owner":
					cause = "original DNS continuation owner unavailable"
				}
				if !strings.Contains(err.Error(), cause) {
					t.Fatal("DNS refusal missed selected boundary", mode, err)
				}
			}
			if f.puts != puts || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("read-only DNS qualification changed originals or republished DNS")
			}
		})
	}
}
