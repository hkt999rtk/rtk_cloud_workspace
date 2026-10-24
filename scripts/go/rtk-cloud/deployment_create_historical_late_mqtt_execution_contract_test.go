package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This composes maintained source/native qualifiers and the real publisher at
// the isolated historical-admission boundary. It does not claim public CLI or
// six-gate preflight coverage. The source must already contain an actual accepted
// Core restore, genuine expired seed and original signed Root#1/cache evidence.
func advanceHistoricalLateMQTTRootCRL(t *testing.T, device *deviceTrustExecutionContract) (*createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	waitHistoricalSignedCRLExpiry(t, f, "14c1a269-e32e-4eac-aa93-db5ad8e85f7e")
	// This connected stage performs source qualification, a dry run, admission,
	// native import and two independent consumer readbacks after the signed
	// test-only CRL has genuinely expired. Keep the maintained preparation budget
	// for those operations; the test certificate lifetime does not cap execution.
	ctx, cancel := context.WithTimeout(t.Context(), managedLatePreparationLimit)
	defer cancel()
	p := createFoundationRecoveryPlan{Plan: f.p.Plan, Materials: map[string]string{}}
	r, late, err := loadManagedLateSessionSource(f.cfg, f.store, f.e, &p, managedLateSessionMQTTRepair)
	if err != nil {
		t.Fatal("actual original restored Core source", err)
	}
	if err := verifyManagedLateSessionObservationWithMode(ctx, f.cfg, f.store, f.e, f.backend, r, &p, late, managedLateSessionMQTTRepair); err != nil {
		t.Fatal("actual original late native source", err)
	}
	originals, err := managedLatePairedOriginalRecords(f.store, f.e, r, managedLateSessionMQTTRepair)
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := readManagedLateRegistryCRLs(ctx, f.cfg, f.e, f.backend, r, managedLateObserveMQTTPrior, originals)
	if err != nil {
		t.Fatal(err)
	}
	paired, err := observeManagedLatePairedCRLPrior(ctx, f.cfg, f.store, f.e, f.backend, r, late, records)
	if err != nil {
		t.Fatal("actual prior signed caches and ACKs", err)
	}
	p.Materials[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(paired)
	native := map[string]string{}
	for key, value := range p.Materials {
		native[key] = value
	}
	root := *r.MQTTRoot
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, f.cfg, f.e, f.backend, root.ID)
	if err != nil || snapshot.Prior.Number != "1" {
		t.Fatal("actual original MQTT revocation inventory", err)
	}
	material, err := createMQTTRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
	if err != nil {
		t.Fatal(err)
	}
	installHistoricalOfflineCRLSigner(t, f.store)
	candidate, request, signed, err := prepareMQTTRootCRLCandidate(ctx, f.store, f.e, p.Plan.Bootstrap.Image, root, snapshot, material)
	if err != nil {
		t.Fatal("actual original offline MQTT signature", err)
	}
	controller, cm, targets, podName, podUID, err := loadMQTTRootCRLTargets(ctx, f.cfg, f.e, f.backend, r, late, root, snapshot.Prior)
	if err != nil {
		t.Fatal("actual retained MQTT consumers", err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	source := f.e.Journal
	plan := createMQTTRootCRLPlan{Schema: createMQTTRootCRLSchema, Parent: f.e.Journal.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(f.e.owner), SourceJournalSHA: deploymentplan.Digest(source), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(f.cfg), Configuration: f.cfg, Generation: p.Plan, KubeconfigSHA: environmentPreflightBytesSHA256(f.backend.KubeconfigContent), Image: p.Plan.Bootstrap.Image, SourceArtifactSHA: late.SourceArtifactSHA, SourceCoreSHA: late.AcceptedCore.SHA256(), SourceAppsSHA: r.ManagedCorePartial.Apps.SHA256(), Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed, Candidate: candidate, Material: material, ConfigMap: cm, Controller: controller, ControllerPodName: podName, ControllerPodUID: podUID, Consumers: targets, NativeSourceEvidence: native, PairedPrior: paired}
	plan.ConsumerVolumes, plan.OriginalVolumes, err = readMQTTRootCRLVolumes(f.e, plan.Consumers, plan.NativeSourceEvidence, late.RestoreReview)
	if err != nil {
		t.Fatal("actual eighteen original volumes", err)
	}
	if _, _, err := validateMQTTRootCRLSourceAncestry(f.store, f.e, plan, &p); err != nil {
		t.Fatal("actual MQTT immutable ancestry", err)
	}
	finish := installHistoricalRootCRLPublicationProtocol(t, f, root, request, signed, targets)
	defer finish()
	if _, err := runNativeCreateRootCRLImport(ctx, f.cfg, f.backend, mqttRootCRLNativeImportPlan(plan), false); err != nil {
		t.Fatal("actual supported native MQTT dry-run", err)
	}
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, f.cfg, f.e, f.backend, root.ID)
	if err != nil || deploymentplan.Digest(fresh) != plan.SnapshotSHA {
		t.Fatal("native dry-run changed original revocation inventory", err)
	}
	if err := verifyMQTTRootCRLInputs(ctx, f.cfg, f.store, f.e, f.backend, plan, false); err != nil {
		t.Fatal("native original MQTT requalification", err)
	}
	review := deploymentplan.Digest(plan)
	if err := checkCreateRootCRLLeaseAvailable(ctx, f.e, f.backend, rootCRLLeasePlan(f.cfg, review)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	qualification := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createMQTTRootCRLGates {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	artifact := createMQTTRootCRLArtifact{Schema: createMQTTRootCRLSchema + "/artifact", Plan: plan, Qualification: qualification, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	for name, value := range map[string]any{"source-journal.json": source, "source-owner.json": f.e.owner, "qualification.json": artifact} {
		if err := writeMQTTRootCRLImmutable(f.store, filepath.Join(mqttRootCRLBase(review), name), value); err != nil {
			t.Fatal(err)
		}
	}
	loaded, captured, err := loadCreateMQTTRootCRLArtifact(f.cfg, f.store, f.e, review)
	if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(artifact) || deploymentplan.Digest(captured) != deploymentplan.Digest(source) {
		t.Fatal("fresh qualified MQTT artifact cannot be independently reloaded", err)
	}
	if err := executeCreateMQTTRootCRL(ctx, f.cfg, f.store, f.e, f.backend, artifact, source, io.Discard); err != nil {
		t.Fatal("actual native MQTT Root#2 publication", err)
	}
	actual, completed, err := readCreateMQTTRootCRLRecovery(f.store, f.e.Journal.PlanSHA256)
	if err != nil || actual == nil || completed == nil || !completed.Completed || completed.Receipt.Digest != signed.Digest {
		t.Fatal("actual MQTT publication lacks independent complete proof", err)
	}
	return actual, completed
}
