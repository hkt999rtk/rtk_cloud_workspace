package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Pre-admission qualification is distinct from loadAdmittedCreateRecovery:
// this reader requires the actual unchanged source, not a fabricated admission.
func managedLateQualifiedArtifactValid(a createFoundationRecoveryArtifact, e *environmentCreateExecution, base createFoundationRecoveryPlan, review string, now time.Time, schemas ...string) bool {
	schema := managedLateSessionSchema
	if len(schemas) > 1 || len(schemas) == 1 && schemas[0] != managedLateSeedForwardSchema && schemas[0] != managedLatePostMintSchema && schemas[0] != managedLateExpiredRenewalSchema && schemas[0] != managedLateNormalTailSchema {
		return false
	}
	if len(schemas) == 1 {
		schema = schemas[0]
	}
	if e == nil || !environmentPreflightDigest.MatchString(review) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != schema || deploymentplan.Digest(a.Plan) != review || a.Plan.ParentSHA != e.Journal.PlanSHA256 || a.Plan.ParentOwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) || a.Plan.CheckerSHA != base.CheckerSHA || a.Plan.KubeconfigSHA != base.KubeconfigSHA || a.Plan.FoundationSHA != base.FoundationSHA || a.Plan.FoundationJournalSHA != base.FoundationJournalSHA || deploymentplan.Digest(a.Plan.Receipts) != deploymentplan.Digest(base.Receipts) || a.Plan.Plan.Environment != base.Plan.Environment || a.Plan.Plan.Stack != base.Plan.Stack || a.Plan.Plan.ConfigurationSHA256 != base.Plan.ConfigurationSHA256 || deploymentplan.Digest(a.Plan.Plan.Release) != deploymentplan.Digest(base.Plan.Release) || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 {
		return false
	}
	for _, pin := range []string{a.Plan.ParentSHA, a.Plan.ParentOwnerSHA, a.Plan.ParentJournalSHA, a.Plan.CheckerSHA, a.Plan.KubeconfigSHA, a.Plan.FoundationSHA, a.Plan.FoundationJournalSHA, a.Plan.Plan.ConfigurationSHA256} {
		if !environmentPreflightDigest.MatchString(pin) {
			return false
		}
	}
	if e.owner.PlanSHA256 != e.Journal.PlanSHA256 || e.owner.OwnerToken != e.Journal.OwnerToken || !e.Journal.CompletedAt.IsZero() {
		return false
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return false
		}
	}
	for key, value := range base.Materials {
		if a.Plan.Materials[key] != value {
			return false
		}
	}
	return true
}

func managedLateQualifiedAncestryMatches(loaded, late *createManagedLateSessionRecovery) bool {
	return managedLateQualifiedAncestryDifference(loaded, late) == ""
}

func managedLateQualifiedAncestryDifference(loaded, late *createManagedLateSessionRecovery) string {
	if loaded == nil || late == nil || loaded.AcceptedCore == nil || late.AcceptedCore == nil {
		return "typed-source"
	}
	checks := []struct {
		name         string
		actual, want any
	}{
		{"mode", loaded.Mode, managedLateSessionACKReady},
		{"archived-mode", late.Mode, loaded.Mode},
		{"parent", loaded.ParentSHA, late.ParentSHA},
		{"source-owner", loaded.SourceOwnerSHA, late.SourceOwnerSHA},
		{"source", loaded.SourceSHA, late.SourceSHA},
		{"core-source", loaded.CoreSourceSHA, late.CoreSourceSHA},
		{"source-artifact", loaded.SourceArtifactSHA, late.SourceArtifactSHA},
		{"original-review", loaded.OriginalRepairReview, late.OriginalRepairReview},
		{"restore-review", loaded.RestoreReview, late.RestoreReview},
		{"original-session", loaded.OriginalSession, late.OriginalSession},
		{"enrollment-session", loaded.OriginalEnrollmentSession, late.OriginalEnrollmentSession},
		{"clients", loaded.OriginalClientsSHA, late.OriginalClientsSHA},
		{"key", loaded.OriginalKeySHA, late.OriginalKeySHA},
		{"csr", loaded.OriginalCSRSHA, late.OriginalCSRSHA},
		{"accepted-core", loaded.AcceptedCore.SHA256(), late.AcceptedCore.SHA256()},
		{"accepted-core-journal", deploymentplan.Digest(loaded.AcceptedCoreJournal), deploymentplan.Digest(late.AcceptedCoreJournal)},
		{"ceremony", deploymentplan.Digest(loaded.SourceCeremony), deploymentplan.Digest(late.SourceCeremony)},
		{"renewed-intent", deploymentplan.Digest(loaded.SourceRenewedIntent), deploymentplan.Digest(late.SourceRenewedIntent)},
		{"mqtt-repair", deploymentplan.Digest(loaded.MQTTRepair), deploymentplan.Digest(late.MQTTRepair)},
		{"mqtt-completion", deploymentplan.Digest(loaded.MQTTCompletion), deploymentplan.Digest(late.MQTTCompletion)},
		{"tls-repair", deploymentplan.Digest(loaded.OpenBaoTLSRepair), deploymentplan.Digest(late.OpenBaoTLSRepair)},
		{"tls-completion", deploymentplan.Digest(loaded.OpenBaoTLSCompletion), deploymentplan.Digest(late.OpenBaoTLSCompletion)},
	}
	for _, check := range checks {
		if deploymentplan.Digest(check.actual) != deploymentplan.Digest(check.want) {
			return check.name
		}
	}
	return ""
}

func managedLateQualifiedMaterialPins(late *createManagedLateSessionRecovery) map[string]string {
	if late == nil || late.Renewal == nil || late.SeedCandidate == nil || late.OwnerCandidate == nil || late.CustodyCandidate == nil || late.LogProducer == nil || late.LogStopCandidate == nil || late.LogHelperCandidate == nil || late.LogRetireCandidate == nil || late.LogRestoreCandidate == nil {
		return nil
	}
	return map[string]string{
		"source": late.SourceSHA, "source-descriptor": managedLateSessionSourceDigest(late), "original-enrollment-session": deploymentplan.Digest(late.OriginalEnrollmentSession), "original-key-seed-plan": late.SeedCandidate.SHA256(),
		"owners-plan": late.OwnerCandidate.SHA256(), "custody-plan": late.CustodyCandidate.SHA256(), "retained-trust": deploymentplan.Digest(late.TrustOwners), "retained-verifiers": deploymentplan.Digest(late.VerifierOwners),
		"log-producer": deploymentplan.Digest(late.LogProducer), "log-helper-binding": late.HelperBindingSHA, "log-stop-plan": late.LogStopCandidate.SHA256(), "log-helper-plan": late.LogHelperCandidate.SHA256(), "log-retire-plan": late.LogRetireCandidate.SHA256(), "log-restore-plan": late.LogRestoreCandidate.SHA256(),
		"native-late-seed-authorization": deploymentplan.Digest(late.SeedAuthorization), "native-late-cleanup-authorization": deploymentplan.Digest(late.CleanupAuthorization), "old-auth-roles": deploymentplan.Digest(map[string]string{"signer": late.OriginalSignerRoleSHA, "controller": late.OriginalControllerRoleSHA}),
	}
}

func validateManagedLateQualifiedPreviews(store secretStore, review string, late *createManagedLateSessionRecovery, materials map[string]string) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("late qualified descriptor, preview and material pins differ")
	}
	pins := managedLateQualifiedMaterialPins(late)
	if pins == nil || late.Renewal.CandidateSHA256 != late.SeedCandidate.SHA256() {
		return bad()
	}
	for name, value := range pins {
		if !environmentPreflightDigest.MatchString(value) || materials["accepted-service-core-late-session-"+name] != value {
			return bad()
		}
	}
	for filename, expected := range map[string]*deploymentplan.Plan{
		"used-bootstrap-renewal-plan.json": late.SeedCandidate, managedLateSessionOwnerPlanFilename: late.OwnerCandidate, "managed-late-session-custody-plan.json": late.CustodyCandidate,
		"managed-late-log-stop-plan.json": late.LogStopCandidate, "managed-late-log-helper-plan.json": late.LogHelperCandidate, "managed-late-log-retire-plan.json": late.LogRetireCandidate, "managed-late-log-restore-plan.json": late.LogRestoreCandidate,
	} {
		var actual deploymentplan.Plan
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, filename), &actual) != nil || actual.SHA256() != expected.SHA256() {
			return bad()
		}
	}
	return nil
}

// Recompile only already reviewed definitions against their archived baselines.
// This is pure recipe validation; current baseline admission remains mandatory.
func managedLateQualifiedRecipeMatches(parent environmentCreatePlan, stage string, definitions []deploymentplan.Definition, candidate *deploymentplan.Plan) bool {
	if candidate == nil {
		return false
	}
	var baselines []deploymentplan.Object
	for _, resource := range candidate.Resources {
		if resource.Action == "create" {
			if resource.UID != "" || resource.Baseline != nil {
				return false
			}
			continue
		}
		if resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return false
		}
		body := deploymentplan.Clone(resource.Baseline)
		deploymentplan.Map(body["metadata"])["uid"] = resource.UID
		baselines = append(baselines, body)
	}
	compiled, err := managedLateCompile(parent, stage, definitions, baselines)
	return err == nil && compiled.SHA256() == candidate.SHA256()
}

func validateManagedLateQualifiedOwners(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("late qualified owners differ from original recorded authority")
	}
	all, err := managedLateSessionRecordedSourceOwners(store, e, r, late)
	if err != nil {
		return err
	}
	if len(late.Owners) != 10 || len(late.TrustOwners) != 3 || len(late.VerifierOwners) != 3 {
		return bad()
	}
	for _, id := range managedLateNativeOwnerIDs(e.Journal.Stack) {
		if owner, ok := late.Owners[id]; !ok || deploymentplan.Digest(owner) != deploymentplan.Digest(all[id]) {
			return bad()
		}
	}
	for _, ns := range late.SourceCeremony.BootstrapCredentialNamespaces {
		for prefix, owners := range map[string]map[string]deploymentplan.Resource{"ConfigMap/": late.TrustOwners, "Secret/": late.VerifierOwners} {
			name := "pki-create-service-trust"
			if prefix == "Secret/" {
				name = "pki-verifier-database"
			}
			id := prefix + ns + "/" + name
			if owner, ok := owners[id]; !ok || deploymentplan.Digest(owner) != deploymentplan.Digest(all[id]) {
				return bad()
			}
		}
	}
	custodyID := "Deployment/" + late.SourceCeremony.BootstrapCredentialNamespace + "/" + late.SourceCeremony.CustodyReader
	if late.Custody.ID != custodyID || deploymentplan.Digest(late.Custody) != deploymentplan.Digest(all[custodyID]) {
		return bad()
	}
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	definitions, err := managedLateSessionOwnerDefinitions(late, service)
	if err != nil {
		return err
	}
	definitions, err = managedLateDefinitionClosure(definitions, all)
	if err != nil {
		return err
	}
	dependencies := map[string]deploymentplan.Resource{}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if _, native := late.Owners[id]; !native {
			dependencies[id] = managedLateExpiredOwnerDependency(late, all[id])
		}
	}
	if deploymentplan.Digest(dependencies) != deploymentplan.Digest(late.DependencyOwners) {
		return bad()
	}
	return nil
}

// Pure local reader. The caller supplies its current checker/configuration and
// time; no native command, new UID, receipt or source snapshot is produced.
func loadManagedLateQualifiedSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string, now time.Time) (*createServiceSessionRecovery, error) {
	return loadManagedLateQualifiedContent(cfg, store, e, p, review, now, nil)
}

func loadManagedLateAdmittedHistoricalSource(cfg deploymentConfig, store secretStore, current *environmentCreateExecution, review string) (*createServiceSessionRecovery, error) {
	a, source, err := loadAdmittedCreateRecovery(store, current, review)
	if err != nil {
		return nil, err
	}
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil {
		return nil, environmentCreateBootstrapBlocker("late historical admitted descriptor unavailable")
	}
	entry := createOpenBaoACKCompletionEntry(current.Journal, "openbao-recovery-admission-"+review)
	if entry == nil || !managedLateAdmissionMatches(a, source, *entry, &late) {
		return nil, environmentCreateBootstrapBlocker("late historical admission differs")
	}
	old := *current
	old.Journal = source
	plan := a.Plan
	return loadManagedLateQualifiedContent(cfg, store, &old, &plan, review, entry.StartedAt, &a, current)
}

func loadManagedLateQualifiedContent(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string, now time.Time, historical *createFoundationRecoveryArtifact, volumeExecutions ...*environmentCreateExecution) (*createServiceSessionRecovery, error) {
	bad := func(stage string) (*createServiceSessionRecovery, error) {
		return nil, environmentCreateBootstrapBlocker("fresh qualified late candidate reuse requires exact immutable evidence: stage=" + stage)
	}
	if p == nil || e == nil || store.Root != e.Store.Root {
		return bad("selected-source")
	}
	var a createFoundationRecoveryArtifact
	base := filepath.Join("deployment/create/openbao-recovery", review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || !managedLateQualifiedArtifactValid(a, e, *p, review, now) {
		return bad("fresh-six-go")
	}
	derived := *p
	derived.Materials = map[string]string{}
	r, loaded, err := loadManagedLateSessionSource(cfg, store, e, &derived, managedLateSessionACKReady)
	if err != nil {
		return nil, err
	}
	var late createManagedLateSessionRecovery
	if err := createOpenBaoACKCompletionRead(store, filepath.Join(base, "managed-late-session-source.json"), &late); err != nil {
		return nil, err
	}
	if late.Review != review {
		return bad("original-source-review")
	}
	if difference := managedLateQualifiedAncestryDifference(loaded, &late); difference != "" {
		return bad("original-source-" + difference)
	}
	if deploymentplan.Digest(derived.Plan) != deploymentplan.Digest(a.Plan.Plan) {
		return bad("original-bootstrap-plan")
	}
	// The original application admission predates the genuine two trust-PVC
	// additions. The qualifier independently pinned the current canonical ledger
	// and active pointer; compare these exact two keys to that same local authority.
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if ledgerErr != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return bad("current-recorded-volume-authority")
	}
	derived.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	derived.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	if historical != nil {
		if deploymentplan.Digest(*historical) != deploymentplan.Digest(a) {
			return bad("historical-volume-authority")
		}
		volumeExecution := e
		if len(volumeExecutions) > 1 {
			return bad("historical-volume-execution")
		}
		if len(volumeExecutions) == 1 {
			volumeExecution = volumeExecutions[0]
		}
		historicalLedger, projectionErr := managedLateRedisHistoricalVolumes(store, volumeExecution, ledger)
		if projectionErr != nil || len(historicalLedger.Volumes) != 18 {
			return bad("historical-volume-authority")
		}
		owners := map[string]string{}
		for _, record := range historicalLedger.Volumes {
			id := "PersistentVolumeClaim/" + record.Namespace + "/" + record.PVCName
			owner, ownerErr := managedTransportRecordedHostVolume(ledger, id)
			if ownerErr != nil {
				return bad("historical-original-volume-record")
			}
			owners[id] = deploymentplan.Digest(owner)
		}
		if deploymentplan.Digest(owners) != a.Plan.Materials["accepted-service-core-managed-complete-current-native-volumes"] {
			return bad("historical-eighteen-volume-pins")
		}
		for _, key := range []string{"accepted-service-core-managed-application-current-volume-ledger", "accepted-service-core-managed-application-current-volume-active"} {
			if !environmentPreflightDigest.MatchString(a.Plan.Materials[key]) {
				return bad("historical-recorded-volume-pin")
			}
			derived.Materials[key] = a.Plan.Materials[key]
		}
	}
	for key, value := range derived.Materials {
		if a.Plan.Materials[key] != value {
			return bad("original-source-materials")
		}
	}
	if err := validateManagedLateQualifiedPreviews(store, review, &late, a.Plan.Materials); err != nil {
		return nil, err
	}
	if err := validateManagedLateQualifiedOwners(store, e, r, &late); err != nil {
		return nil, err
	}
	if err := validateManagedLateQualifiedRecipes(cfg, store, e, r, a.Plan, &late, now); err != nil {
		return nil, err
	}
	*p = a.Plan
	r.ManagedLateSession = &late
	r.Review = review
	r.Candidate = late.OwnerCandidate
	r.RenewalCandidate = late.SeedCandidate
	r.RenewalBindingSHA = late.Renewal.BindingSHA256
	r.RenewedCertPath = late.NewCertificatePath
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = late.NewSessionID
	r.RenewedIntent.Subjects = slices.Clone(late.SourceRenewedIntent.Subjects)
	return r, nil
}

func validateManagedLateQualifiedRecipes(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, p createFoundationRecoveryPlan, late *createManagedLateSessionRecovery, now time.Time) error {
	bad := func(stage string) error {
		return environmentCreateBootstrapBlocker("late qualified producer changed: " + stage)
	}
	if late.Renewal == nil || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) || late.NewSessionID == late.OriginalSession || late.Renewal.OriginalSession != late.OriginalSession || late.Renewal.OriginalCertificatePath != r.RenewedCertPath || late.Renewal.ClientsSHA256 != late.OriginalClientsSHA || late.Renewal.SourceStage != r.SourceRenewalStage {
		return bad("original-key-renewal")
	}
	for _, pin := range []string{late.Renewal.OriginalCertificateSHA256, late.Renewal.SessionSHA256, late.Renewal.SourcePlanSHA256, late.Renewal.SourceJournalSHA256, late.Renewal.BindingSHA256} {
		if !environmentPreflightDigest.MatchString(pin) {
			return bad("renewal-proof")
		}
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil || !managedLateSessionMayRenewForScope(late.OriginalSessionState, late.SourceCeremony, subjects, late, now) {
		return bad("original-expired-eight")
	}
	original, journal, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if late.ExpiredRenewal != nil {
		original, _, journal, err = readManagedLateEffectiveSeed(store, e, late.ExpiredRenewal.PriorLate)
	}
	if err != nil || len(original.Resources) != 1 || late.Renewal.SourcePlanSHA256 != original.SHA256() || late.Renewal.SourceJournalSHA256 != deploymentplan.Digest(journal) {
		return bad("original-signing-job")
	}
	binding := deploymentplan.Digest(map[string]string{"schema": managedLateSessionSchema, "parent": late.ParentSHA, "accepted_source": late.SourceSHA, "accepted_core": late.AcceptedCore.SHA256(), "accepted_core_journal": deploymentplan.Digest(late.AcceptedCoreJournal), "original_session": late.OriginalSession, "original_session_state": late.Renewal.SessionSHA256, "clients": late.OriginalClientsSHA, "original_key": late.OriginalKeySHA, "original_csr": late.OriginalCSRSHA})
	if late.ExpiredRenewal != nil {
		binding = managedLateExpiredRenewalBinding(late.ExpiredRenewal, late)
	}
	if binding != late.Renewal.BindingSHA256 || sessionRenewalUUID(late.ParentSHA, binding, late.OriginalSession) != late.NewSessionID || late.NewCertificatePath != "/run/pki-bootstrap/renew-"+binding[:12]+"/bootstrap.crt" {
		return bad("distinct-original-key-authority")
	}
	// The image is additionally rechecked against formal publication by the
	// runtime wrapper; the exact canonical selected manifest supplies metadata.
	image, selection, err := readManagedLateQualifiedSeedImage(store, late.ParentSHA)
	if err != nil || p.Materials["accepted-service-core-late-session-image"] != deploymentplan.Digest(image) || p.Materials["accepted-service-core-late-session-image-selection"] != selection {
		return bad("formal-seed-image")
	}
	seedSource := original.Resources[0].Desired
	if late.ExpiredRenewal != nil {
		seedSource = deploymentplan.Clone(seedSource)
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(seedSource)["containers"])[0])
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	}
	definition, err := managedSessionRenewalDefinition(seedSource, late.ParentSHA, binding, late.NewSessionID, image)
	if err != nil || !managedLateQualifiedRecipeMatches(p.Plan, "managed-late-original-key-session", []deploymentplan.Definition{definition}, late.SeedCandidate) {
		return bad("seed-recipe")
	}
	r.GrantImage = image
	if validateManagedLateSessionOwnerCandidate(late, late.OwnerCandidate) != nil {
		return bad("sid-only-owners")
	}
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	defs, err := managedLateSessionOwnerDefinitions(late, service)
	if err == nil {
		defs, err = managedLateDefinitionClosure(defs, late.DependencyOwners)
	}
	if err != nil || !managedLateQualifiedRecipeMatches(p.Plan, "managed-late-session-owners", defs, late.OwnerCandidate) {
		return bad("owner-recipe")
	}
	reader := deploymentplan.Clone(late.Custody.Desired)
	c := managedNamedField(environmentPKIPod(reader)["containers"], "custody")
	if c == nil {
		return bad("original-custody")
	}
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": late.NewSessionID, "PKI_BOOTSTRAP_CERT_PATH": late.NewCertificatePath}))
	if len(late.CustodyCandidate.Resources) != 1 || !deploymentplan.Matches(late.Custody.Desired, late.CustodyCandidate.Resources[0].Baseline) || late.CustodyCandidate.Resources[0].UID != late.Custody.UID || !managedLateQualifiedRecipeMatches(p.Plan, "service-custody-reader-renewal", []deploymentplan.Definition{{Phase: "bootstrap", Object: reader}}, late.CustodyCandidate) {
		return bad("custody-recipe")
	}
	if !managedLateRoleMatches(late.OriginalSignerRole, cfg, []string{"pki-service-bootstrap"}, []string{"rtk-" + cfg.Environment + "-service-bootstrap-" + late.OriginalSession}) || !managedLateRoleMatches(late.OriginalControllerRole, cfg, []string{"pki-controller"}, environmentCreateControllerPolicies(environmentCreateBootstrapRuntime{Execution: e})) || !slices.Equal(late.ControllerPolicies, environmentCreateControllerPolicies(environmentCreateBootstrapRuntime{Execution: e})) {
		return bad("original-native-roles")
	}
	seed, cleanup, err := managedLateSessionNativeRecipes(cfg, late)
	if err != nil || deploymentplan.Digest(seed) != deploymentplan.Digest(late.SeedAuthorization) || deploymentplan.Digest(cleanup) != deploymentplan.Digest(late.CleanupAuthorization) {
		return bad("native-recipes")
	}
	producer := late.LogProducer
	intent := r.Intent
	intent.SessionID, intent.Subjects = late.NewSessionID, slices.Clone(subjects)
	authority := deploymentplan.Digest(map[string]string{"source": late.SourceSHA, "seed_plan": late.SeedCandidate.SHA256(), "intent": deploymentplan.Digest(intent), "original_key": late.OriginalKeySHA, "original_csr": late.OriginalCSRSHA})
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	if ledgerErr == nil {
		ledger, ledgerErr = managedLateRedisHistoricalVolumes(store, e, ledger)
	}
	claim, claimErr := managedLateLogClaim(producer.Original.Desired, ledger)
	if ledgerErr != nil || claimErr != nil || producer.NewSessionAuthoritySHA != authority || producer.OriginalClaimUID != claim.PVCUID || producer.OriginalVolumesSHA != deploymentplan.Digest(ledger.Volumes) || producer.OriginalPlanSHA != r.ManagedCorePartial.Apps.SHA256() || producer.OriginalJournalSHA != deploymentplan.Digest(r.ManagedCorePartial.AppsJournal) || producer.OriginalServiceIssuer != r.Ceremony.Intermediate.ID || producer.NewSession.Issuer != r.Ceremony.Intermediate.ID || producer.NewSession.Root != r.Ceremony.Root.RootFingerprint || !slices.Equal(producer.NewSession.Subjects, subjects) || producer.OriginalServersSHA != deploymentplan.Digest(map[string]string{"host": r.ManagedTransportRecovery.Partial.ServerSHA, "api": p.Materials["accepted-service-core-managed-application-current-api-servers"]}) || deploymentplan.Digest(producer.Original) != deploymentplan.Digest(late.Owners[producer.Original.ID]) || late.LogStateSHA != producer.Identity.StateSHA256 || late.LogFingerprint != producer.Identity.Fingerprint || late.LogSPKISHA != producer.Identity.PublicKeySHA256 {
		return bad("original-log-source-pins")
	}
	helperBinding := deploymentplan.Digest(map[string]string{"schema": managedLateSessionSchema, "source": late.SourceSHA, "new_session": late.NewSessionID, "seed_authority": authority, "original_owner": deploymentplan.Digest(producer.Original), "identity": deploymentplan.Digest(producer.Identity), "claim_uid": claim.PVCUID})
	if helperBinding != late.HelperBindingSHA {
		return bad("log-source-binding")
	}
	if validateManagedLateLogProducer(late, late.LogProducer, now) != nil || validateManagedLateLogCandidates(late) != nil {
		return bad("exclusive-log-producer")
	}
	helper, err := renderLogACKDraftHelper(late.LogProducer)
	if err != nil || !managedLateQualifiedRecipeMatches(p.Plan, "managed-late-log-existing-ack", []deploymentplan.Definition{helper}, late.LogHelperCandidate) || !managedLateQualifiedRecipeMatches(p.Plan, "managed-late-log-stop", []deploymentplan.Definition{renderLogACKDraftQuiesce(late.LogProducer)}, late.LogStopCandidate) || !managedLateQualifiedRecipeMatches(p.Plan, "managed-late-log-restore", []deploymentplan.Definition{renderLogACKDraftRestore(late.LogProducer)}, late.LogRestoreCandidate) {
		return bad("exclusive-log-recipes")
	}
	retire := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: late.LogHelperCandidate.Inputs, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(helper.Object), Phase: "retirement", Action: "retire", Desired: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired), Baseline: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired)}}}
	retire.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-log-retire", []deploymentplan.Definition{{Phase: "retirement", Object: deploymentplan.Clone(helper.Object), Retire: true}})
	if retire.SHA256() != late.LogRetireCandidate.SHA256() {
		return bad("allocated-only-retirement-preview")
	}
	return nil
}

// Read selected metadata locally; the execution wrapper also rechecks the
// upstream successful publication without recreating a candidate.
func readManagedLateQualifiedSeedImage(store secretStore, parent string) (environmentReleaseImage, string, error) {
	bad := func() (environmentReleaseImage, string, error) {
		return environmentReleaseImage{}, "", environmentCreateBootstrapBlocker("late selected formal seed publication changed")
	}
	var selection struct {
		Schema      string `json:"schema"`
		Parent      string `json:"parent_plan_sha256"`
		Revision    string `json:"source_revision"`
		RunID       int64  `json:"run_id"`
		Attempt     int    `json:"run_attempt"`
		ManifestSHA string `json:"manifest_sha256"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/repairs/service-bootstrap/selection.json"), true)
	if err != nil || json.Unmarshal(raw, &selection) != nil || selection.Schema != "rtk-cloud.create-service-trust-repair-selection/v1" || selection.Parent != parent || !deploymentUpgradeSHA.MatchString(selection.Revision) || selection.RunID <= 0 || selection.Attempt <= 0 || !environmentPreflightDigest.MatchString(selection.ManifestSHA) {
		return bad()
	}
	manifest, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/repairs/service-bootstrap", selection.Revision, "image-manifest.json"), true)
	if err != nil || environmentPreflightBytesSHA256(manifest) != selection.ManifestSHA {
		return bad()
	}
	repo := "hkt999rtk/rtk_video_cloud"
	image, err := selectEnvironmentPublishedImage(manifest, repo, selection.RunID, selection.Attempt, selection.Revision, "ghcr.io/"+repo+"/video-cloud-api", false)
	if err != nil {
		return bad()
	}
	return image, environmentPreflightBytesSHA256(raw), nil
}

func loadManagedLateQualifiedSession(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	r, err := loadManagedLateQualifiedSource(cfg, store, e, p, review, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, err
	}
	if deploymentplan.Digest(image) != p.Materials["accepted-service-core-late-session-image"] || selection != p.Materials["accepted-service-core-late-session-image-selection"] || deploymentplan.Digest(image) != deploymentplan.Digest(r.GrantImage) {
		return nil, environmentCreateBootstrapBlocker("late qualified formal seed image changed")
	}
	return r, nil
}

// Reobserve the native source without preparing a second session, recompiling
// plans, or copying a new native baseline into the qualification.
func verifyManagedLateQualifiedCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	bad := func(stage string) error {
		return environmentCreateBootstrapBlocker("late current qualified evidence changed: " + stage)
	}
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateSession.Renewal == nil || p == nil {
		return bad("prepared-source")
	}
	late := r.ManagedLateSession
	// The gate starts with an empty observed map. Pure completed-source
	// derivation restores the original API CSR/issuance authority before fresh
	// SQL writes a new value, and preserves the authenticated Bootstrap policy.
	_, loaded, sourceErr := loadManagedLateSessionSource(cfg, store, e, p, managedLateSessionACKReady)
	if sourceErr != nil {
		return sourceErr
	}
	if !managedLateQualifiedAncestryMatches(loaded, late) {
		return bad("original-source-materials")
	}
	if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, p, late, managedLateSessionACKReady); err != nil {
		return err
	}
	original, journal, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if err != nil || len(original.Resources) != 1 || original.SHA256() != late.Renewal.SourcePlanSHA256 || deploymentplan.Digest(journal) != late.Renewal.SourceJournalSHA256 {
		return bad("original-signing-job")
	}
	resource := original.Resources[0]
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
		return bad("original-signing-job-owner")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, late.Renewal); err != nil {
		return err
	}
	certificate, err := b.command(ctx, nil, "--namespace", late.SourceCeremony.BootstrapCredentialNamespace, "exec", "deployment/"+late.SourceCeremony.CustodyReader, "--container", "custody", "--", "cat", late.Renewal.OriginalCertificatePath)
	if err != nil || environmentPreflightBytesSHA256(certificate) != late.Renewal.OriginalCertificateSHA256 {
		return bad("original-expired-certificate")
	}
	for name, expected := range map[string]string{"pki-service-bootstrap-" + cfg.Environment: late.OriginalSignerRoleSHA, "pki-controller-" + cfg.Environment: late.OriginalControllerRoleSHA} {
		role, err := readManagedLateOpenBaoRole(ctx, cfg, store, b, name)
		if err != nil || deploymentplan.Digest(role) != expected {
			return bad("original-native-role")
		}
	}
	pod, err := readManagedLateLogSourcePod(ctx, b, late.LogProducer.Original)
	if err != nil || deploymentplan.Map(pod["metadata"])["uid"] != late.LogProducer.SourcePodUID {
		return bad("original-log-pod")
	}
	identity, err := readLogACKDraftIdentity(ctx, b, deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), late.SourceCeremony.Root.RootFingerprint)
	if err != nil || deploymentplan.Digest(identity) != deploymentplan.Digest(late.LogProducer.Identity) || validateManagedLateLogProducer(late, late.LogProducer, time.Now().UTC()) != nil {
		return bad("original-log-identity-and-not-due")
	}
	if err := verifyManagedLateSessionRetainedDependencies(ctx, b, late); err != nil {
		return err
	}
	return e.Check(ctx)
}
