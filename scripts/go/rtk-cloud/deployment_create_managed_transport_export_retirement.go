package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedTransportExportRetire struct {
	Review, SourceSHA, ArtifactSHA             string
	ExportSHA, RetireSHA, CertificateSHA       string
	ClaimUID, ReaderUID, PodUID, ReplicaSetUID string
	ServerName, RootSHA                        string
	MaterialSHA                                string
}

func managedTransportExportRetirementReview(j environmentCreateJournal) string {
	if len(j.Entries) < 9 || !environmentPreflightDigest.MatchString(j.PlanSHA256) {
		return ""
	}
	tail := j.Entries[len(j.Entries)-3:]
	const prefix = "openbao-recovery-admission-"
	review := strings.TrimPrefix(tail[0].Key, prefix)
	if !strings.HasPrefix(tail[0].Key, prefix) || !environmentPreflightDigest.MatchString(review) || tail[1].Key != "server-export-video-cloud-api-pki" || tail[1].Phase != "bootstrap" || tail[2].Key != "retire-pki-export-video-cloud-api-pki-"+j.PlanSHA256[:12] || tail[2].Phase != "bootstrap" {
		return ""
	}
	for _, entry := range tail[:2] {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return ""
		}
	}
	retire := tail[2]
	if retire.State != "pending" || !retire.CompletedAt.IsZero() || len(retire.Receipt) != 0 || !environmentPreflightDigest.MatchString(retire.IntentSHA256) {
		return ""
	}
	before := j
	before.Entries = j.Entries[:len(j.Entries)-3]
	if managedTransportPostManagedReview(before) == "" {
		return ""
	}
	return review
}

func loadManagedTransportExportRetirement(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, *createManagedTransportPostManaged, error) {
	return loadManagedTransportExportRetirementSource(store, e, false)
}

// Historical derivation authenticates retained receipts without treating a later
// native generation of operational metadata as the old ledger or reader.
func loadManagedTransportExportRetirementSource(store secretStore, e *environmentCreateExecution, historicalOnly bool) (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, *createManagedTransportPostManaged, error) {
	bad := func() (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, *createManagedTransportPostManaged, error) {
		return nil, nil, nil, nil, createProviderRecoveryBlock("API export retirement requires its admitted completed managed source, original export proof and no retirement child writes")
	}
	review := managedTransportExportRetirementReview(e.Journal)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-transports-partial-recovery/v1" || managedTransportExportRetirementReview(source) != "" || managedTransportPostManagedReview(source) == "" || len(e.Journal.Entries) != len(source.Entries)+3 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases) {
		return bad()
	}
	normalized := e.Journal
	normalized.Entries = e.Journal.Entries[:len(source.Entries)]
	if deploymentplan.Digest(normalized) != deploymentplan.Digest(source) {
		return bad()
	}
	historical := *e
	historical.Journal = source
	base, prior, accepted, post, err := loadManagedTransportPostManaged(store, &historical)
	if err != nil || post == nil || post.ExportRetire != nil {
		return bad()
	}
	original, _, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil || a.Plan.Materials["accepted-service-core-managed-partial-original-plan"] != original.SHA256() || !environmentPreflightDigest.MatchString(a.Plan.Materials["accepted-service-core-managed-partial-native-proof"]) {
		return bad()
	}
	var preview deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-partial-forward-plan"] {
		return bad()
	}
	partial := &createManagedTransportPartialRecovery{Original: original, PostManaged: post}
	defs := []deploymentplan.Definition{}
	for _, resource := range preview.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
	}
	if validateManagedTransportPostManagedDefinitions(partial, defs) != nil || validateManagedTransportPartialForwardActions(partial, &preview) != nil {
		return bad()
	}
	var volume environmentCreateVolumeOwner
	if !historicalOnly {
		ledger, err := readEnvironmentCreateLedger(e)
		var active managedTransportVolumeLedgerActive
		if err != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil || active.LedgerSHA != a.Plan.Materials["accepted-service-core-managed-partial-original-volume-ledger"] {
			return bad()
		}
		claimID := "PersistentVolumeClaim/" + source.Stack + "-video-cloud/video-cloud-api-pki-controller-identity"
		volume, err = managedTransportRecordedHostVolume(ledger, claimID)
		if err != nil {
			return bad()
		}
	}
	publication, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "pki/service/issuer.json"), true)
	var public struct {
		Root         map[string]any `json:"root"`
		Intermediate map[string]any `json:"intermediate"`
	}
	if err != nil || environmentPreflightBytesSHA256(publication) != a.Plan.Materials["accepted-service-core-managed-published-service"] || json.Unmarshal(publication, &public) != nil {
		return bad()
	}
	root, err := environmentCreateRegisteredIssuerFromMap(public.Root, "")
	if err != nil || root.Status != "active" || root.Domain != "service" || root.Environment != source.Environment {
		return bad()
	}
	readerPlan := post.ExportPlans["kubernetes/server-custody-video-cloud-api-pki"]
	readerJournal := post.ExportJournals["kubernetes/server-custody-video-cloud-api-pki"]
	if readerPlan == nil || len(readerPlan.Resources) != 1 {
		return bad()
	}
	readerID := readerPlan.Resources[0].ID
	export, retire := e.Journal.Entries[len(source.Entries)+1], e.Journal.Entries[len(source.Entries)+2]
	var proof struct {
		Certificate string `json:"certificate_sha256"`
		Claim       string `json:"claim_uid"`
		Reader      string `json:"reader_uid"`
		Pod         string `json:"pod_uid"`
		ReplicaSet  string `json:"replicaset_uid"`
		Name        string `json:"server_name"`
	}
	if json.Unmarshal(export.Receipt, &proof) != nil || !environmentPreflightDigest.MatchString(proof.Certificate) || (!historicalOnly && proof.Claim != volume.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(proof.Claim) || proof.Reader != readerJournal.Owners[readerID] || !environmentBootstrapOwnerUUID.MatchString(proof.Pod) || !environmentBootstrapOwnerUUID.MatchString(proof.ReplicaSet) || proof.Name != "video-cloud-api-pki."+source.Stack+"-video-cloud.svc" {
		return bad()
	}
	intent := deploymentplan.Object{"owner": "video-cloud-api-pki", "namespace": source.Stack + "-video-cloud", "claim_uid": proof.Claim, "reader_uid": proof.Reader, "pod_uid": proof.Pod, "replicaset_uid": proof.ReplicaSet, "server_name": proof.Name, "root_sha256": root.RootFingerprint}
	retireIntent := deploymentplan.Object{"resource_id": readerID, "uid": proof.Reader}
	if export.IntentSHA256 != activeTransportJournalDigest(intent) || retire.IntentSHA256 != activeTransportJournalDigest(retireIntent) {
		return bad()
	}
	post.ExportRetire = &createManagedTransportExportRetire{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), ExportSHA: activeTransportJournalDigest(export), RetireSHA: activeTransportJournalDigest(retire), CertificateSHA: proof.Certificate, ClaimUID: proof.Claim, ReaderUID: proof.Reader, PodUID: proof.Pod, ReplicaSetUID: proof.ReplicaSet, ServerName: proof.Name, RootSHA: root.RootFingerprint}
	return base, prior, accepted, post, nil
}

func verifyManagedTransportExportRetirementMaterial(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, post *createManagedTransportPostManaged, owned environmentCreateExportPod, reader deploymentplan.Resource) error {
	retained := post.ExportRetire
	bad := func() error {
		return createProviderRecoveryBlock("retained original API serving material or export owner differs")
	}
	if retained == nil {
		return bad()
	}
	verify := func() error {
		claim, claimErr := b.Read(ctx, "PersistentVolumeClaim/"+post.APIVolume.Namespace+"/"+post.APIVolume.PVCName)
		current, readerErr := b.Read(ctx, reader.ID)
		if claimErr != nil || readerErr != nil || claim == nil || current == nil || deploymentplan.Map(claim["metadata"])["uid"] != retained.ClaimUID || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(current["metadata"])["uid"] != retained.ReaderUID || !deploymentplan.Matches(reader.Desired, current) || owned.Verify(ctx, b, reader.Desired, retained.ReaderUID) != nil {
			return bad()
		}
		return e.Check(ctx)
	}
	if verify() != nil {
		return bad()
	}
	var owner environmentCreateApplicationOwner
	for _, original := range environmentCreateApplicationOwners() {
		if original.Name == "video-cloud-api-pki" {
			owner = original
		}
	}
	if owner.Name == "" || owner.Role == "" || owner.ClientState == "" {
		return bad()
	}
	read, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := b.command(read, nil, "--namespace", post.Plan.Stack+"-video-cloud", "exec", "pod/"+owned.Name, "--container", "custody", "--", "/app/serviceidentity-bootstrap", "export-server", filepath.Join(filepath.Dir(owner.ClientState), "server.json"), "service", "service:"+owner.Role, retained.RootSHA, retained.ServerName)
	if err != nil {
		return bad()
	}
	digest, err := validateManagedTransportRetainedExportMaterial(raw, cfg.Environment, retained)
	if err != nil || verify() != nil {
		return bad()
	}
	retained.MaterialSHA = digest
	return nil
}

func validateManagedTransportRetainedExportMaterial(raw []byte, environment string, retained *createManagedTransportExportRetire) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("retained original API serving material differs")
	}
	var material environmentCreateOwnedServerMaterial
	if retained == nil || len(raw) > 256<<10 || decodeEnvironmentJSON(raw, &material) != nil || material.Environment != environment || material.Root != retained.RootSHA || material.Name != retained.ServerName || environmentPreflightBytesSHA256([]byte(material.Certificate)) != retained.CertificateSHA || material.Key == "" {
		return bad()
	}
	identity, err := tls.X509KeyPair([]byte(material.Certificate), []byte(material.Key))
	if err != nil || len(identity.Certificate) < 2 {
		return bad()
	}
	return deploymentplan.Digest(material), nil
}
