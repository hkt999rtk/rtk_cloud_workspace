package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createDeviceRootCRLCompletionSchema = "rtk-cloud.create-device-root-crl-rotation-completion/v1"

type createDeviceRootCRLCompletionPlan struct {
	Schema, OriginalReview, OriginalArtifactSHA, SourceRepairJournalSHA string
	CheckerSHA, ConfigurationSHA, KubeconfigSHA, ProviderSHA            string
	QualifiedAt, Deadline                                               time.Time
	Signed                                                              createRootCRLRecord
	NativeEvidence                                                      map[string]string
}

type createDeviceRootCRLCompletionArtifact struct {
	Schema                  string
	Plan                    createDeviceRootCRLCompletionPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}

func deviceRootCRLCompletionBase(original, review string) string {
	return filepath.Join(deviceRootCRLBase(original), "completion-only", review)
}

func deviceRootCRLCompletionQualificationValid(a createDeviceRootCRLCompletionArtifact, original createDeviceRootCRLArtifact) bool {
	p, old := a.Plan, original.Plan
	review := deploymentplan.Digest(p)
	if !deviceRootCRLQualificationValid(original) || a.Schema != createDeviceRootCRLCompletionSchema+"/artifact" || p.Schema != createDeviceRootCRLCompletionSchema || p.OriginalReview != deploymentplan.Digest(old) || p.OriginalArtifactSHA != deploymentplan.Digest(original) || p.ConfigurationSHA != old.ConfigurationSHA || p.KubeconfigSHA != old.KubeconfigSHA || p.ProviderSHA != old.ProviderSHA || p.QualifiedAt.IsZero() || !p.QualifiedAt.Equal(a.QualifiedAt) || !p.QualifiedAt.Before(p.Deadline) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createDeviceRootCRLGates) {
		return false
	}
	for _, sha := range []string{p.OriginalReview, p.OriginalArtifactSHA, p.SourceRepairJournalSHA, p.CheckerSHA, p.ConfigurationSHA, p.KubeconfigSHA, p.ProviderSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	for i, gate := range a.Qualification.Gates {
		if gate.ID != createDeviceRootCRLGates[i] || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != review {
			return false
		}
	}
	for _, key := range []string{"accepted-service-core-managed-complete-current-native-volumes", managedLateAPICurrentMaterial, "accepted-service-core-managed-application-current-servers", "accepted-service-core-managed-application-current-clients"} {
		if !environmentPreflightDigest.MatchString(p.NativeEvidence[key]) || p.NativeEvidence[key] != old.NativeEvidence[key] {
			return false
		}
	}
	return true
}

func loadDeviceRootCRLCompletionArtifact(store secretStore, original createDeviceRootCRLArtifact, review string) (createDeviceRootCRLCompletionArtifact, createDeviceRootCRLJournal, error) {
	var a createDeviceRootCRLCompletionArtifact
	var source createDeviceRootCRLJournal
	old := deploymentplan.Digest(original.Plan)
	base := deviceRootCRLCompletionBase(old, review)
	if !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || deploymentplan.Digest(a.Plan) != review || !deviceRootCRLCompletionQualificationValid(a, original) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-repair-journal.json"), &source) != nil || !deviceRootCRLJournalValid(original, source, a.QualifiedAt) || deploymentplan.Digest(source) != a.Plan.SourceRepairJournalSHA || !source.Deadline.Equal(a.Plan.Deadline) || validateDeviceRootCRLRotationReadback(original.Plan, source, a.Plan.Signed, a.QualifiedAt) != nil {
		return a, source, deviceRootCRLError("qualified-rotation-completion-source")
	}
	return a, source, nil
}

// The fresh authority can advance only the original admitted rotating repair.
// Original intent, epoch, deadline and provider-call time are never projected.
func deviceRootCRLCompletionMembership(a createDeviceRootCRLCompletionArtifact, source, current createDeviceRootCRLJournal) bool {
	if deploymentplan.Digest(source) != a.Plan.SourceRepairJournalSHA || !current.Deadline.Equal(a.Plan.Deadline) || current.CompletionReview != deploymentplan.Digest(a.Plan) || current.CompletionStartedAt.Before(a.QualifiedAt) || !current.CompletionStartedAt.Before(a.ValidUntil) || !current.CompletionStartedAt.Before(current.Deadline) || current.CompletionStartedAt.After(time.Now().UTC()) {
		return false
	}
	if current.Stage != "rotating" && current.Stage != "rotated" && current.Stage != "imported" && current.Stage != "api-verified" && current.Stage != "consumers-verified" && current.Stage != "completed" {
		return false
	}
	if current.Stage == "rotating" {
		if current.Signed.IssuerID != "" || !current.RotatedAt.IsZero() {
			return false
		}
	} else if deploymentplan.Digest(current.Signed) != deploymentplan.Digest(a.Plan.Signed) || current.RotatedAt.Before(current.CompletionStartedAt) {
		return false
	}
	expected := source
	expected.Stage, expected.Signed, expected.Evidence = current.Stage, current.Signed, current.Evidence
	expected.Lease, expected.LeaseReleased = current.Lease, current.LeaseReleased
	expected.RotatedAt, expected.ImportedAt, expected.CompletedAt = current.RotatedAt, current.ImportedAt, current.CompletedAt
	expected.CompletionReview, expected.CompletionStartedAt = current.CompletionReview, current.CompletionStartedAt
	return deploymentplan.Digest(expected) == deploymentplan.Digest(current)
}

func validateDeviceRootCRLCompletionJournal(store secretStore, original createDeviceRootCRLArtifact, current createDeviceRootCRLJournal) error {
	if current.CompletionReview == "" {
		if !current.CompletionStartedAt.IsZero() {
			return deviceRootCRLError("unqualified-completion-time")
		}
		return nil
	}
	a, source, err := loadDeviceRootCRLCompletionArtifact(store, original, current.CompletionReview)
	if err != nil || !deviceRootCRLCompletionMembership(a, source, current) || !managedLateLeaseMatches(current.Lease, original.Plan.Configuration.Values["CLOUD_STACK_NAME"], rootCRLLeasePlan(original.Plan.Configuration, current.CompletionReview).SHA256()) {
		return deviceRootCRLError("authenticated-fresh-completion-link")
	}
	return nil
}

func qualifyDeviceRootCRLCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, original createDeviceRootCRLArtifact, checker string, out io.Writer) error {
	var source createDeviceRootCRLJournal
	old := deploymentplan.Digest(original.Plan)
	if createOpenBaoACKCompletionRead(store, filepath.Join(deviceRootCRLBase(old), "journal.json"), &source) != nil || !deviceRootCRLJournalValid(original, source, time.Now().UTC()) || source.CompletionReview != "" || !source.CompletionStartedAt.IsZero() || !time.Now().UTC().Before(source.Deadline) {
		return deviceRootCRLError("original-rotation-intent-before-deadline")
	}
	if _, err := os.Lstat(filepath.Join(store.Root, deviceRootCRLPointer(original.Plan.Parent))); !os.IsNotExist(err) {
		return deviceRootCRLError("completion-pointer-must-be-absent")
	}
	ctx, cancel := context.WithDeadline(ctx, source.Deadline)
	defer cancel()
	client, err := appRootCRLHumanClient(ctx, cfg, store, e, b, operator, original.Plan.Operator)
	if err != nil {
		return err
	}
	issuer, err := client.ReadIssuer(ctx, original.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(issuer) != deploymentplan.Digest(original.Plan.Root.Document) {
		return deviceRootCRLError("completion-current-original-human-authority")
	}
	provider, raw, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, original.Plan.Root, false)
	if err != nil || provider != original.Plan.ProviderSHA {
		return deviceRootCRLError("completion-original-provider-key")
	}
	signed, err := deviceRootCRLProviderRecord(original.Plan.Root, raw)
	if err != nil || validateDeviceRootCRLRotationReadback(original.Plan, source, signed, time.Now().UTC()) != nil {
		return deviceRootCRLError("completion-exact-post-intent-signed-outcome")
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, original.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(original.Plan.Snapshot) {
		return deviceRootCRLError("completion-current-registry-and-union")
	}
	native, err := observeDeviceRootCRLNative(ctx, cfg, store, e, b, original.Plan, nil)
	if err != nil {
		return err
	}
	// This is a read-only provider recheck. No signer or import is reachable.
	proof, again, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, original.Plan.Root, false)
	if err != nil || proof != provider || environmentPreflightBytesSHA256(again) != environmentPreflightBytesSHA256(raw) {
		return deviceRootCRLError("completion-provider-outcome-drift")
	}
	var unchanged createDeviceRootCRLJournal
	if createOpenBaoACKCompletionRead(store, filepath.Join(deviceRootCRLBase(old), "journal.json"), &unchanged) != nil || deploymentplan.Digest(unchanged) != deploymentplan.Digest(source) || e.Check(ctx) != nil || !time.Now().UTC().Before(source.Deadline) {
		return deviceRootCRLError("completion-unchanged-original-repair")
	}
	at := time.Now().UTC()
	p := createDeviceRootCRLCompletionPlan{Schema: createDeviceRootCRLCompletionSchema, OriginalReview: old, OriginalArtifactSHA: deploymentplan.Digest(original), SourceRepairJournalSHA: deploymentplan.Digest(source), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: original.Plan.KubeconfigSHA, ProviderSHA: provider, QualifiedAt: at, Deadline: source.Deadline, Signed: signed, NativeEvidence: native}
	review := deploymentplan.Digest(p)
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, review)); err != nil {
		return err
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	if ctx.Err() != nil || !time.Now().UTC().Before(source.Deadline) {
		return deviceRootCRLError("completion-original-deadline")
	}
	q := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range createDeviceRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	a := createDeviceRootCRLCompletionArtifact{Schema: createDeviceRootCRLCompletionSchema + "/artifact", Plan: p, Qualification: q, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
	if !deviceRootCRLCompletionQualificationValid(a, original) {
		return deviceRootCRLError("completion-sixGO-inputs")
	}
	for name, value := range map[string]any{"qualification.json": a, "source-repair-journal.json": source} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(deviceRootCRLCompletionBase(old, review), name), value); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-device-root-crl-completion original-review-sha256=%s review-sha256=%s crl-number=%s deadline=%s\n", old, review, signed.Number, source.Deadline.UTC().Format(time.RFC3339Nano))
	return nil
}
