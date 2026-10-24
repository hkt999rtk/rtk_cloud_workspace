package main

import (
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This test begins at the explicit native-stage boundary. The three interrupted
// children and accepted pull child are actual executor outcomes; it does not
// claim the separate full historical source qualification.
func TestHistoricalManagedOpenBaoAuditReloadBindsNativeProcessAndOriginalCustody(t *testing.T) {
	f, partial := newHistoricalInterruptedTransportHosts(t)
	r := &createServiceSessionRecovery{Ceremony: f.service, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: partial}}
	historicalManagedOpenBaoPod(t, f, false)
	installHistoricalManagedOpenBaoProtocol(t, f)
	_, before, err := verifyManagedTransportOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, r, false)
	if err != nil || before == nil {
		t.Fatal("actual interrupted native source", err)
	}
	var defs []deploymentplan.Definition
	for _, resource := range partial.PriorForward.Plan.Resources {
		body := deploymentplan.Clone(resource.Desired)
		if body["kind"] == "StatefulSet" {
			environmentPKIPod(body)["imagePullSecrets"] = []any{deploymentplan.Object{"name": lkeImagePullSecretName(f.cfg.Values)}}
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	const pullStage = "isolated-original-managed-pull"
	p := f.acceptedHistoricalStage(t, pullStage, defs)
	pod := historicalManagedOpenBaoPod(t, f, true)
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod); err != nil {
		t.Fatal(err)
	}
	_, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+pullStage)
	if err != nil || validateManagedTransportAcceptedForwardJournal(p, j) != nil {
		t.Fatal("actual accepted seven-owner child", err)
	}
	key := "openbao-managed-unseal-" + deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	unseal := createOpenBaoACKCompletionEntry(f.e.Journal, key)
	if unseal == nil {
		t.Fatal("actual original-share unseal missing")
	}
	partial.AcceptedForward = &createManagedTransportAcceptedForward{Plan: p, Journal: j, UnsealKey: key, UnsealSHA: activeTransportJournalDigest(*unseal)}
	_, retained, err := verifyManagedTransportOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, r, false)
	if err != nil || retained["provider_process_sha256"] == nil {
		t.Fatal("native accepted missing-audit proof", err)
	}
	defs = nil
	for _, resource := range p.Resources {
		body := deploymentplan.Clone(resource.Desired)
		if body["kind"] == "ConfigMap" && deploymentplan.Map(body["metadata"])["name"] == "openbao-file" {
			data := deploymentplan.Map(body["data"])
			data["file.hcl"] = deploymentplan.String(data["file.hcl"]) + managedTransportOriginalAuditBlock
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	if err := validateManagedTransportAcceptedAuditDefinitions(partial, defs); err != nil {
		t.Fatal(err)
	}
	r.Candidate = historicalTransportHostPreview(t, f, defs)
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-managed-transports-partial-recovery/v1", map[string]string{"accepted-service-core-managed-partial-openbao-retained-prerequisites": deploymentplan.Digest(retained)})
	f.write(t, filepath.Join("deployment/create/openbao-recovery", r.Review, "openbao-root-trust-plan.json"), r.Candidate)
	f.acceptedHistoricalStage(t, "transport-managed-hosts-forward-"+r.Review, defs)
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	original := deploymentplan.Clone(f.protocol["historical_bao_process"].(deploymentplan.Object))
	for _, field := range []string{"pid", "start_time", "executable_sha256"} {
		changed := deploymentplan.Clone(original)
		if field == "executable_sha256" {
			changed[field] = deploymentplan.Digest("foreign native executable")
		} else {
			changed[field] = "654321"
		}
		f.protocol["historical_bao_process"] = changed
		f.publish(t)
		snapshot := createBootstrapStateSnapshot(t, f.store.Root)
		if err := reloadManagedTransportOriginalAudit(t.Context(), f.cfg, f.store, runtime); err == nil {
			t.Fatal("changed native process authorized HUP", field)
		}
		assertRecoveryContractReadOnly(t, f.store, snapshot)
	}
	f.protocol["historical_bao_process"] = original
	f.publish(t)
	if err := reloadManagedTransportOriginalAudit(t.Context(), f.cfg, f.store, runtime); err != nil {
		t.Fatal("actual original-provider audit reload", err)
	}
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, "openbao-managed-audit-reload-"+r.Review)
	if entry == nil || entry.State != "completed" || entry.Phase != "openbao-audit-reload" {
		t.Fatal("actual audit reload receipt missing")
	}
	var native map[string]any
	if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_FOUNDATION_STATE"), &native); err != nil || native["signals"] != float64(1) || native["audit"] != true {
		t.Fatal("native redacted audit reload differs", err)
	}
}
