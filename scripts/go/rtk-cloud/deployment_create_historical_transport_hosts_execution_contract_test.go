package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The legacy host producer wrote three owners serially, before the maintained
// dependency barrier existed. These helpers replay that external protocol and
// the two documented recovery generations. They create no recovery admission:
// callers composing the full ancestry must produce their real admission first.
func historicalTransportHostDefinitions(defs []deploymentplan.Definition, generation int) []deploymentplan.Definition {
	var out []deploymentplan.Definition
	for _, original := range defs {
		d := original
		d.Object = deploymentplan.Clone(original.Object)
		d.Dependencies = slices.Clone(original.Dependencies)
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		policy := name == "pki-controller" || name == "pki-create-service-trust"
		dependency := name == "mqtt-pki-config" || name == "mqtt-pki-runtime"
		if generation < 2 && policy || generation == 0 && dependency {
			continue
		}
		if generation < 2 {
			d.Dependencies = nil
		}
		if d.Object["kind"] == "ConfigMap" && name == "openbao-file" {
			// The original managed replacement omitted its declarative audit
			// stanza; the later accepted audit repair restores this exact block.
			data := deploymentplan.Map(d.Object["data"])
			data["file.hcl"] = strings.TrimSuffix(deploymentplan.String(data["file.hcl"]), managedTransportOriginalAuditBlock)
		}
		if d.Object["kind"] == "StatefulSet" {
			pod := environmentPKIPod(d.Object)
			delete(pod, "imagePullSecrets")
			// This source predates the later ACK repair. Preserve its actual
			// historical shell PID producer before compiling any owned plan.
			for _, value := range certIssuerObjectList(pod["containers"]) {
				c := deploymentplan.Map(value)
				if c["name"] != "openbao" {
					continue
				}
				command := certIssuerObjectList(c["command"])
				if len(command) == 3 {
					script := strings.ReplaceAll(deploymentplan.String(command[2]), `"$$$$"`, `"$$"`)
					script = strings.ReplaceAll(script, `> '/run/openbao-pki/private/openbao.pid'`, `> /run/openbao-pki/private/openbao.pid`)
					command[2], c["command"] = script, command
				}
			}
		}
		if generation == 0 && d.Object["kind"] == "Deployment" {
			for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(value)
				var env []any
				for _, value := range certIssuerObjectList(c["env"]) {
					if deploymentplan.Map(value)["name"] != "PKI_DATABASE_URL" {
						env = append(env, value)
					}
				}
				c["env"] = env
			}
		}
		if d.Object["kind"] == "Secret" {
			data := deploymentplan.Map(d.Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for key, value := range deploymentplan.Map(d.Object["stringData"]) {
				data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
			}
			d.Object["data"] = data
			delete(d.Object, "stringData")
		}
		out = append(out, d)
	}
	return out
}

func historicalTransportHostPreview(t *testing.T, f *acceptedCoreExecutionContract, defs []deploymentplan.Definition) *deploymentplan.Plan {
	t.Helper()
	var live []deploymentplan.Object
	for _, object := range f.native.objects {
		live = append(live, object)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("transport-managed-hosts-forward", defs)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func historicalTransportOriginalHostInterruption(t *testing.T, f *acceptedCoreExecutionContract, defs []deploymentplan.Definition) *createManagedTransportPartialRecovery {
	t.Helper()
	defs = historicalTransportHostDefinitions(defs, 0)
	preview := historicalTransportHostPreview(t, f, defs)
	if len(preview.Resources) != 3 {
		t.Fatal("original host inventory", len(preview.Resources))
	}
	for _, r := range preview.Resources {
		if r.Action != "update" || r.UID == "" {
			t.Fatal("original host must update its retained native owner", r.ID, r.Action)
		}
	}
	p, j := f.historicalSerialInterruptedStage(t, "kubernetes/transport-managed-hosts", defs, "Deployment/"+f.p.Plan.Stack+"-video-cloud/mqtt-pki")
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	if len(j.Completed) != 1 || len(j.Owners) != 2 || j.Accepted || j.AcceptanceStarted || j.InFlight != "" {
		t.Fatal("original interruption differs", j)
	}
	if err := verifyManagedTransportPartialBodies(t.Context(), f.backend, p, j); err != nil {
		t.Fatal(err)
	}
	return &createManagedTransportPartialRecovery{Original: p, Journal: j}
}

func historicalTransportForwardDefinitions(t *testing.T, f *acceptedCoreExecutionContract, partial *createManagedTransportPartialRecovery, defs []deploymentplan.Definition) []deploymentplan.Definition {
	t.Helper()
	generation := 1
	if partial.PriorForward != nil {
		generation = 2
	}
	defs = historicalTransportHostDefinitions(defs, generation)
	if generation == 2 {
		// Recovery renders from the authenticated retained policy owners, not
		// from a freshly reconstructed initial credential ConfigMap.
		runtime := f.runtime
		runtime.ServiceSessionRecovery = &createServiceSessionRecovery{ManagedTransportRecovery: &createManagedTransportRecovery{Partial: partial}}
		policy, err := renderCreateTransportServicePolicyDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, f.service)
		if err != nil {
			t.Fatal("retained policy renderer", err)
		}
		for i := range defs {
			for _, d := range policy {
				if deploymentplan.ID(defs[i].Object) == deploymentplan.ID(d.Object) {
					defs[i] = d
				}
			}
		}
	}
	return defs
}

// review is the caller's genuine selected recovery identifier. This producer
// only appends the actual failed child; it never invents the preceding GO.
func historicalTransportHostForwardInterruption(t *testing.T, f *acceptedCoreExecutionContract, partial *createManagedTransportPartialRecovery, defs []deploymentplan.Definition, review string) *createManagedTransportInterruptedForward {
	t.Helper()
	failed := "Deployment/" + f.p.Plan.Stack + "-video-cloud/mqtt-pki"
	if partial.PriorForward != nil {
		failed = "StatefulSet/" + f.p.Plan.Stack + "-secrets/openbao"
	}
	defs = historicalTransportForwardDefinitions(t, f, partial, defs)
	if err := verifyManagedTransportCurrentPartialBodies(t.Context(), f.backend, partial); err != nil {
		t.Fatal("source native bodies", err)
	}
	if err := validateManagedTransportPartialForwardDefinitions(partial, defs); err != nil {
		t.Fatal("forward definitions", err)
	}
	preview := historicalTransportHostPreview(t, f, defs)
	if err := validateManagedTransportPartialForwardActions(partial, preview); err != nil {
		t.Fatal("forward actions", err)
	}
	// Exercise the maintained forward coordinator as well as its actual
	// historical child protocol. The explicit boundary model supplies only
	// the candidate already compiled and validated above, not a GO artifact.
	runtime := f.runtime
	runtime.ServiceSessionRecovery = &createServiceSessionRecovery{Review: review, Candidate: preview, Ceremony: f.service, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: partial}}
	var p *deploymentplan.Plan
	var j deploymentplan.Journal
	interruption := errors.New("isolated native host readiness interruption")
	err := executeManagedTransportPartialForward(t.Context(), f.cfg, runtime, defs, func(_ context.Context, stage string, actual []deploymentplan.Definition) error {
		if stage != "transport-managed-hosts-forward-"+review {
			t.Fatal("forward selected another stage", stage)
		}
		p, j = f.historicalSerialInterruptedStage(t, "kubernetes/"+stage, actual, failed)
		return interruption
	})
	if !errors.Is(err, interruption) || p == nil {
		t.Fatal("forward did not preserve actual native interruption", err)
	}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	err = nil
	if partial.PriorForward == nil {
		err = validateManagedTransportInterruptedForward(partial.Original, p, j)
	} else {
		err = validateManagedTransportWrittenOpenBaoJournal(p, j)
	}
	if err != nil {
		t.Fatal("actual interrupted forward", err)
	}
	return &createManagedTransportInterruptedForward{Previous: partial.PriorForward, Review: review, Plan: p, Journal: j}
}

func newHistoricalInterruptedTransportHosts(t *testing.T) (*acceptedCoreExecutionContract, *createManagedTransportPartialRecovery) {
	t.Helper()
	f, roots, intermediates, clients := newHistoricalTransportCoordinatorExecutionContract(t)
	// The prior MQTT application is actually installed, while its future managed
	// configuration/runtime dependencies remain absent until the first forward.
	apps, err := renderEnvironmentCreateApplicationOwners(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "isolated-original-app-transport-owner", environmentCreateApplicationDefinitionsFor(apps, "mqtt-pki"))
	originalPVCs := map[string]string{}
	for id, o := range f.native.objects {
		if strings.HasPrefix(id, "PersistentVolumeClaim/") {
			originalPVCs[id] = deploymentplan.Digest(o)
		}
	}
	originalClientWrites := map[string]int{}
	for _, r := range clients.Resources {
		originalClientWrites[r.ID] = f.native.writes[r.ID]
	}
	var defs []deploymentplan.Definition
	stop := errors.New("isolated host history boundary")
	apply := historicalTransportSessionApply(t, f, &createServiceSessionRecovery{}, "", managedSessionState{})
	err = executeEnvironmentCreateManagedTransports(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.runtime, f.service, roots, intermediates, func(ctx context.Context, stage string, d []deploymentplan.Definition) error {
		if stage == "transport-managed-hosts" {
			defs = d
			return stop
		}
		return apply(ctx, stage, d)
	})
	if !errors.Is(err, stop) || len(defs) != 7 {
		t.Fatal("actual enrollment coordinator did not reach hosts", err, len(defs))
	}
	owners, err := loadManagedTransportAdditionalSourceOwners(f.store, f.e, f.review)
	if err != nil {
		t.Fatal("accepted original policy owners", err)
	}
	partial := historicalTransportOriginalHostInterruption(t, f, defs)
	partial.AdditionalOwners = owners
	originalJournalPath := filepath.Join(f.store.Root, "deployment/rollback", partial.Original.SHA256(), "journal.json")
	originalJournal := historicalTransportHistoryRead(t, originalJournalPath)
	first := historicalTransportHostForwardInterruption(t, f, partial, defs, strings.Repeat("a", 64))
	partial.PriorForward = first
	firstJournalPath := filepath.Join(f.store.Root, "deployment/rollback", first.Plan.SHA256(), "journal.json")
	firstJournal := historicalTransportHistoryRead(t, firstJournalPath)
	second := historicalTransportHostForwardInterruption(t, f, partial, defs, strings.Repeat("b", 64))
	partial.PriorForward = second
	for _, r := range second.Plan.Resources {
		if _, ok := partial.AdditionalOwners[r.ID]; ok {
			partial.AdditionalOwners[r.ID] = r
		}
	}
	if err := verifyManagedTransportCurrentPartialBodies(t.Context(), f.backend, partial); err != nil {
		t.Fatal("second generation native readback", err)
	}
	if string(originalJournal) != string(historicalTransportHistoryRead(t, originalJournalPath)) || string(firstJournal) != string(historicalTransportHistoryRead(t, firstJournalPath)) {
		t.Fatal("forward rewrote original failure evidence")
	}
	for id, sha := range originalPVCs {
		if deploymentplan.Digest(f.native.objects[id]) != sha {
			t.Fatal("host recovery altered original PVC", id)
		}
	}
	for id, count := range originalClientWrites {
		if f.native.writes[id] != count {
			t.Fatal("host recovery replayed original client", id)
		}
	}
	return f, partial
}

func TestHistoricalTransportHostInterruptedGenerationsPreserveOriginalOwners(t *testing.T) {
	f, partial := newHistoricalInterruptedTransportHosts(t)
	for _, r := range partial.PriorForward.Plan.Resources {
		t.Run("refuses-native-owner-"+strings.ReplaceAll(r.ID, "/", "-"), func(t *testing.T) {
			object := f.native.objects[r.ID]
			uid := deploymentplan.Map(object["metadata"])["uid"]
			deploymentplan.Map(object["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			f.protocol["objects"] = historicalNativeInventory(f.native.objects)
			f.publish(t)
			if err := verifyManagedTransportCurrentPartialBodies(t.Context(), f.backend, partial); err == nil {
				t.Fatal("changed owner accepted")
			}
			deploymentplan.Map(object["metadata"])["uid"] = uid
			f.protocol["objects"] = historicalNativeInventory(f.native.objects)
			f.publish(t)
		})
	}
}

func historicalTransportHistoryRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
