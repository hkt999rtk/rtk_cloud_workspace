package main

// Qualification binds original producers before allocating new native UIDs.

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedLateLogProof struct {
	Schema, Parent, Review, HelperBindingSHA, ProducerSHA                 string
	OriginalDeploymentUID, HelperJobUID, HelperPodUID, NewSessionID       string
	IdentitySHA, NativeClientsSHA, PrivateBytesSHA, NativeSessionSHA      string
	ObservationSHA, OriginalClaimUID                                      string
	StopJournalSHA, HelperJournalSHA, RetireJournalSHA, RestoreJournalSHA string
	StoppedAt, StartedAt, FinishedAt, RetiredAt, RestoredAt               time.Time
}

func validateManagedLateLogCompletion(late *createManagedLateSessionRecovery, proof *createManagedLateLogProof) error {
	if late == nil || proof == nil || late.LogProducer == nil || proof.Schema != "rtk-cloud.create-managed-late-log-proof/v1" || proof.Parent != late.ParentSHA || proof.Review != late.Review || proof.HelperBindingSHA != late.HelperBindingSHA || proof.ProducerSHA != deploymentplan.Digest(late.LogProducer) || proof.OriginalDeploymentUID != late.LogProducer.Original.UID || proof.NewSessionID != late.NewSessionID || proof.IdentitySHA != deploymentplan.Digest(late.LogProducer.Identity) || proof.NativeClientsSHA != late.OriginalClientsSHA || proof.PrivateBytesSHA != late.LogProducer.Identity.StateSHA256 || proof.OriginalClaimUID != late.LogProducer.OriginalClaimUID {
		return logACKDraftBlock("durable original producer/identity")
	}
	for _, hash := range []string{proof.Parent, proof.Review, proof.HelperBindingSHA, proof.ProducerSHA, proof.IdentitySHA, proof.NativeClientsSHA, proof.PrivateBytesSHA, proof.NativeSessionSHA, proof.ObservationSHA, proof.StopJournalSHA, proof.HelperJournalSHA, proof.RetireJournalSHA, proof.RestoreJournalSHA} {
		if !environmentPreflightDigest.MatchString(hash) {
			return logACKDraftBlock("durable proof digests")
		}
	}
	if !environmentBootstrapOwnerUUID.MatchString(proof.HelperJobUID) || !environmentBootstrapOwnerUUID.MatchString(proof.HelperPodUID) || proof.StoppedAt.IsZero() || !proof.StartedAt.After(proof.StoppedAt) || !proof.FinishedAt.After(proof.StartedAt) || !proof.RetiredAt.After(proof.FinishedAt) || !proof.RestoredAt.After(proof.RetiredAt) || proof.FinishedAt.Sub(proof.StartedAt) > logACKTTL+logACKGrace || !late.LogNotDueUntil.After(proof.StartedAt.Add(logACKTTL+logACKGrace+logACKDueMargin)) {
		return logACKDraftBlock("exclusive helper temporal proof")
	}
	return nil
}

// Prospective session fields deliberately claim no native allocation. Runtime
// substitutes a fresh genuine native row on a COPY after its accepted mint Job.
func validateManagedLateLogProducer(late *createManagedLateSessionRecovery, producer *createLogACKHelperDraft, now time.Time) error {
	if late == nil || producer == nil || producer.Review != late.HelperBindingSHA || producer.Parent != late.ParentSHA || producer.SourceJournalSHA != late.SourceSHA || producer.SourceOwnerSHA != late.SourceOwnerSHA || producer.OriginalSession != managedLateOriginalWorkloadSession(late) || producer.OriginalClientsSHA != late.OriginalClientsSHA || producer.OriginalPrivateBytesSHA != producer.Identity.StateSHA256 || producer.Identity.Pending || producer.Identity.Subject != logACKSubject || producer.NewSession.Session != late.NewSessionID || producer.NewSession.Session == producer.OriginalSession || producer.NewSession.Status != "qualified-not-issued" || !producer.NewSession.Expires.IsZero() || producer.NewSession.Fingerprint != "" || producer.NewSession.OtherActive != 0 || len(producer.NewSession.Installed) != 0 {
		return logACKDraftBlock("explicit unallocated session producer")
	}
	for _, hash := range []string{producer.Parent, producer.Review, producer.SourceJournalSHA, producer.SourceOwnerSHA, producer.OriginalPlanSHA, producer.OriginalJournalSHA, producer.NewSessionAuthoritySHA, producer.OriginalClientsSHA, producer.OriginalServersSHA, producer.OriginalVolumesSHA, producer.Identity.Fingerprint, producer.Identity.PublicKeySHA256, producer.Identity.RootSHA256, producer.Identity.StateSHA256} {
		if !environmentPreflightDigest.MatchString(hash) {
			return logACKDraftBlock("source producer hashes")
		}
	}
	if !environmentBootstrapOwnerUUID.MatchString(producer.Original.UID) || !environmentBootstrapOwnerUUID.MatchString(producer.SourcePodUID) || !environmentBootstrapOwnerUUID.MatchString(producer.OriginalClaimUID) || producer.Original.ID != "Deployment/video-cloud-dev-video-cloud/video-cloud-logingester" {
		return logACKDraftBlock("source producer original owner")
	}
	leaf, err := parseFirstCertificatePEM([]byte(producer.LeafPEM))
	if err != nil || leaf.Subject.CommonName != logACKSubject || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) || environmentPreflightBytesSHA256(leaf.Raw) != producer.Identity.Fingerprint || leaf.NotBefore.After(now) || !leaf.NotAfter.After(now) || environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo) != producer.Identity.PublicKeySHA256 {
		return logACKDraftBlock("original current leaf and key")
	}
	due := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	if !due.Equal(late.LogNotDueUntil) || !due.After(now.Add(logACKTTL+logACKGrace+logACKDueMargin)) {
		return logACKDraftBlock("source not-due entire bounded helper")
	}
	c := managedNamedField(environmentPKIPod(producer.Original.Desired)["containers"], "app")
	if c == nil || c["image"] != logACKNativeImage || deploymentplan.Digest(c["command"]) != deploymentplan.Digest([]any{"/app/logingester"}) || mqttFoundationEnv(c, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE") != logACKStatePath || mqttFoundationEnv(c, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256") != producer.Identity.RootSHA256 || mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != managedLateOriginalWorkloadSession(late) {
		return logACKDraftBlock("source original image/state/root/session")
	}
	return nil
}

func readManagedLateLogSourcePod(ctx context.Context, b environmentKubernetesBackend, owner deploymentplan.Resource) (deploymentplan.Object, error) {
	ns := deploymentplan.String(deploymentplan.Map(owner.Desired["metadata"])["namespace"])
	raw, err := b.command(ctx, nil, "--namespace", ns, "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-logingester", "-o", "json")
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return nil, logACKDraftBlock("one original source Pod")
	}
	pod := list.Items[0]
	meta := deploymentplan.Map(pod["metadata"])
	refs := certIssuerObjectList(meta["ownerReferences"])
	if !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(meta["uid"])) || meta["deletionTimestamp"] != nil || len(refs) != 1 {
		return nil, logACKDraftBlock("source Pod UID/lineage")
	}
	ref := deploymentplan.Map(refs[0])
	if ref["kind"] != "ReplicaSet" || ref["controller"] != true || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(ref["uid"])) {
		return nil, logACKDraftBlock("source replica owner")
	}
	rs, err := b.Read(ctx, "ReplicaSet/"+ns+"/"+deploymentplan.String(ref["name"]))
	rmeta := deploymentplan.Map(rs["metadata"])
	rrefs := certIssuerObjectList(rmeta["ownerReferences"])
	if err != nil || rs == nil || rmeta["uid"] != ref["uid"] || rmeta["deletionTimestamp"] != nil || len(rrefs) != 1 {
		return nil, logACKDraftBlock("source original ReplicaSet")
	}
	dep := deploymentplan.Map(rrefs[0])
	if dep["kind"] != "Deployment" || dep["uid"] != owner.UID || dep["name"] != "video-cloud-logingester" || dep["controller"] != true {
		return nil, logACKDraftBlock("original Log Deployment chain")
	}
	expected := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(owner.Desired["spec"])["template"]}}
	actual := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(rs["spec"])["template"]}}
	if !deploymentplan.Matches(expected, actual) {
		return nil, logACKDraftBlock("original Log source template")
	}
	return pod, nil
}

func prepareManagedLateLogCandidates(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	if late == nil || late.OwnerCandidate == nil || r.RenewalCandidate == nil || r.ManagedCorePartial == nil {
		return logACKDraftBlock("qualified seed/owner authority")
	}
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/video-cloud-logingester"
	owner := late.Owners[id]
	pod, err := readManagedLateLogSourcePod(ctx, b, owner)
	if err != nil {
		return err
	}
	identity, err := readLogACKDraftIdentity(ctx, b, deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), r.Ceremony.Root.RootFingerprint)
	if err != nil {
		return err
	}
	query := "SELECT json_build_object('certificate_pem',certificate_pem,'fingerprint',fingerprint) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject='service:video-cloud-logingester' AND status='succeeded' AND revoked_at IS NULL"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var native struct {
		Certificate string `json:"certificate_pem"`
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &native) != nil || native.Fingerprint != identity.Fingerprint {
		return logACKDraftBlock("one original native Log leaf")
	}
	leaf, err := parseFirstCertificatePEM([]byte(native.Certificate))
	if err != nil {
		return logACKDraftBlock("native signed Log leaf")
	}
	ica, err := parseFirstCertificatePEM([]byte(r.Ceremony.Intermediate.Certificate))
	if err != nil || leaf.CheckSignatureFrom(ica) != nil {
		return logACKDraftBlock("original Service signature")
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return err
	}
	claim, err := managedLateLogClaim(owner.Desired, ledger)
	if err != nil {
		return err
	}
	authority := deploymentplan.Digest(map[string]string{"source": late.SourceSHA, "seed_plan": r.RenewalCandidate.SHA256(), "intent": deploymentplan.Digest(r.RenewedIntent), "original_key": late.OriginalKeySHA, "original_csr": late.OriginalCSRSHA})
	late.LogStateSHA, late.LogFingerprint, late.LogSPKISHA = identity.StateSHA256, identity.Fingerprint, identity.PublicKeySHA256
	late.LogNotDueUntil = leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	late.HelperBindingSHA = deploymentplan.Digest(map[string]string{"schema": managedLateSessionSchema, "source": late.SourceSHA, "new_session": late.NewSessionID, "seed_authority": authority, "original_owner": deploymentplan.Digest(owner), "identity": deploymentplan.Digest(identity), "claim_uid": claim.PVCUID})
	late.LogProducer = &createLogACKHelperDraft{Parent: late.ParentSHA, Review: late.HelperBindingSHA, SourceJournalSHA: late.SourceSHA, SourceOwnerSHA: late.SourceOwnerSHA, OriginalPlanSHA: r.ManagedCorePartial.Apps.SHA256(), OriginalJournalSHA: deploymentplan.Digest(r.ManagedCorePartial.AppsJournal), Original: owner, SourcePodUID: deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), OriginalClaimUID: claim.PVCUID, OriginalSession: managedLateOriginalWorkloadSession(late), OriginalServiceIssuer: r.Ceremony.Intermediate.ID, NewSessionAuthoritySHA: authority, NewSession: managedSessionState{Session: late.NewSessionID, Issuer: r.Ceremony.Intermediate.ID, Root: r.Ceremony.Root.RootFingerprint, Status: "qualified-not-issued", Subjects: slices.Clone(r.RenewedIntent.Subjects)}, Identity: identity, LeafPEM: native.Certificate, OriginalClientsSHA: late.OriginalClientsSHA, OriginalServersSHA: deploymentplan.Digest(map[string]string{"host": r.ManagedTransportRecovery.Partial.ServerSHA, "api": p.Materials["accepted-service-core-managed-application-current-api-servers"]}), OriginalVolumesSHA: deploymentplan.Digest(ledger.Volumes), OriginalPrivateBytesSHA: identity.StateSHA256}
	if err := validateManagedLateLogProducer(late, late.LogProducer, time.Now().UTC()); err != nil {
		return err
	}
	helper, err := renderLogACKDraftHelper(late.LogProducer)
	if err != nil {
		return err
	}
	// Owner stage retains the Log template exactly. The real source body
	// keeps Kubernetes defaults and unrelated metadata in both baselines.
	var afterOwner deploymentplan.Resource
	for _, resource := range late.OwnerCandidate.Resources {
		if resource.ID == id {
			afterOwner = resource
		}
	}
	actual, readErr := b.Read(ctx, id)
	if readErr != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != owner.UID || afterOwner.ID == "" || afterOwner.UID != owner.UID || afterOwner.Action != "retain" || !deploymentplan.Matches(owner.Desired, actual) || deploymentplan.Digest(afterOwner.Desired) != deploymentplan.Digest(owner.Desired) {
		return logACKDraftBlock("owner stage retains genuine Log source")
	}
	postOwner := deploymentplan.Clone(actual)
	stop := renderLogACKDraftQuiesce(late.LogProducer)
	late.LogStopCandidate, err = managedLateCompile(p.Plan, "managed-late-log-stop", []deploymentplan.Definition{stop}, []deploymentplan.Object{postOwner})
	if err != nil {
		return err
	}
	late.LogHelperCandidate, err = managedLateCompile(p.Plan, "managed-late-log-existing-ack", []deploymentplan.Definition{helper}, nil)
	if err != nil {
		return err
	}
	// UID absent by design: retire preview binds the sole Job producer and the
	// lifecycle resolver must later require its actually accepted UID.
	late.LogRetireCandidate = &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: late.LogHelperCandidate.Inputs, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(helper.Object), Phase: "retirement", Action: "retire", Desired: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired), Baseline: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired)}}}
	late.LogRetireCandidate.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-log-retire", []deploymentplan.Definition{{Phase: "retirement", Object: deploymentplan.Clone(helper.Object), Retire: true}})
	stopped := deploymentplan.Clone(actual)
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	restore := renderLogACKDraftRestore(late.LogProducer)
	late.LogRestoreCandidate, err = managedLateCompile(p.Plan, "managed-late-log-restore", []deploymentplan.Definition{restore}, []deploymentplan.Object{stopped})
	if err != nil {
		return err
	}
	if err := validateManagedLateLogCandidates(late); err != nil {
		return err
	}
	for name, plan := range map[string]*deploymentplan.Plan{"stop": late.LogStopCandidate, "helper": late.LogHelperCandidate, "retire": late.LogRetireCandidate, "restore": late.LogRestoreCandidate} {
		p.Materials["accepted-service-core-late-session-log-"+name+"-plan"] = plan.SHA256()
	}
	p.Materials["accepted-service-core-late-session-log-producer"] = deploymentplan.Digest(late.LogProducer)
	p.Materials["accepted-service-core-late-session-log-helper-binding"] = late.HelperBindingSHA
	return e.Check(ctx)
}

func managedLateLogClaim(body deploymentplan.Object, ledger environmentCreateResourceLedger) (environmentCreateVolumeOwner, error) {
	var empty environmentCreateVolumeOwner
	c := managedNamedField(environmentPKIPod(body)["containers"], "app")
	if c == nil {
		return empty, logACKDraftBlock("original Log state mount")
	}
	var role string
	for _, value := range certIssuerObjectList(c["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		path := deploymentplan.String(mount["mountPath"])
		if path != "" && strings.HasPrefix(logACKStatePath, path+"/") {
			if role != "" {
				return empty, logACKDraftBlock("ambiguous original state mount")
			}
			role = deploymentplan.String(mount["name"])
			if mount["readOnly"] == true || mount["subPath"] != nil {
				return empty, logACKDraftBlock("original writable whole-state mount")
			}
		}
	}
	volume := managedNamedField(environmentPKIPod(body)["volumes"], role)
	name := deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
	ns := deploymentplan.String(deploymentplan.Map(body["metadata"])["namespace"])
	if role == "" || name == "" {
		return empty, logACKDraftBlock("original private claim")
	}
	return managedTransportRecordedHostVolume(ledger, "PersistentVolumeClaim/"+ns+"/"+name)
}

func validateManagedLateLogCandidates(late *createManagedLateSessionRecovery) error {
	if late == nil || late.LogProducer == nil || late.LogStopCandidate == nil || late.LogHelperCandidate == nil || late.LogRetireCandidate == nil || late.LogRestoreCandidate == nil {
		return logACKDraftBlock("four exact lifecycle candidates")
	}
	for _, plan := range []*deploymentplan.Plan{late.LogStopCandidate, late.LogHelperCandidate, late.LogRetireCandidate, late.LogRestoreCandidate} {
		if len(plan.Resources) != 1 || plan.Environment != late.AcceptedCore.Environment || plan.Stack != late.AcceptedCore.Stack {
			return logACKDraftBlock("one-owner lifecycle scope")
		}
	}
	stop, helper, retire, restore := late.LogStopCandidate.Resources[0], late.LogHelperCandidate.Resources[0], late.LogRetireCandidate.Resources[0], late.LogRestoreCandidate.Resources[0]
	wantedHelper, err := renderLogACKDraftHelper(late.LogProducer)
	if err != nil {
		return err
	}
	if stop.Action != "update" || restore.Action != "update" || stop.UID != late.LogProducer.Original.UID || restore.UID != stop.UID || stop.ID != late.LogProducer.Original.ID || restore.ID != stop.ID || helper.Action != "create" || helper.UID != "" || helper.ID != deploymentplan.ID(wantedHelper.Object) || retire.Action != "retire" || retire.UID != "" || retire.ID != helper.ID || retire.Phase != "retirement" || !deploymentplan.Matches(retire.Desired, helper.Desired) || !deploymentplan.Matches(retire.Baseline, helper.Desired) {
		return logACKDraftBlock("original owner/new helper/allocated-only retirement")
	}
	if !deploymentplan.Matches(renderLogACKDraftQuiesce(late.LogProducer).Object, stop.Desired) || !deploymentplan.Matches(renderLogACKDraftRestore(late.LogProducer).Object, restore.Desired) || !deploymentplan.Matches(wantedHelper.Object, helper.Desired) || !deploymentplan.Matches(late.LogProducer.Original.Desired, stop.Baseline) || !deploymentplan.Matches(stop.Desired, restore.Baseline) {
		return logACKDraftBlock("SID-only stop/restore and exact native wrapper")
	}
	return nil
}

func validateManagedLateLogRetireAllocated(late *createManagedLateSessionRecovery, actual *deploymentplan.Plan, helperUID string) error {
	if validateManagedLateLogCandidates(late) != nil || actual == nil || !environmentBootstrapOwnerUUID.MatchString(helperUID) || len(actual.Resources) != 1 {
		return logACKDraftBlock("real helper retirement allocation")
	}
	want := late.LogRetireCandidate.Resources[0]
	got := actual.Resources[0]
	if got.UID != helperUID || got.Action != "retire" || got.ID != want.ID || got.Phase != want.Phase || !deploymentplan.Matches(want.Desired, got.Desired) || !deploymentplan.Matches(want.Baseline, got.Baseline) || len(got.Dependencies) != 0 || actual.Environment != late.LogRetireCandidate.Environment || actual.Stack != late.LogRetireCandidate.Stack || actual.ConfigurationSHA256 != late.LogRetireCandidate.ConfigurationSHA256 || actual.ReleaseSHA256 != late.LogRetireCandidate.ReleaseSHA256 {
		return logACKDraftBlock("retirement changed beyond actual accepted UID")
	}
	return nil
}
