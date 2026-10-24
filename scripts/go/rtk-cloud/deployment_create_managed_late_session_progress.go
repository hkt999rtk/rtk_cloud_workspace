package main

// PRIVATE DRAFT. Every runtime member is derived from an admitted producer or
// a genuinely allocated prior child. No unknown stage or lease is accepted.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedLateSeedObservation struct {
	Schema, Parent, Review                                      string
	Session                                                     managedSessionState
	CertificatePEM, KeySHA, CSRSHA, SeedPlanSHA, SeedJournalSHA string
	ObservedAt                                                  time.Time
}

func managedLateOriginalSessionUnchanged(old, current managedSessionState, now time.Time) bool {
	if old.Session != current.Session || old.Issuer != current.Issuer || old.Root != current.Root || old.Fingerprint != current.Fingerprint || !old.Expires.Equal(current.Expires) || old.Expires.IsZero() || old.Expires.After(now) || current.OtherActive != 0 || !slices.Equal(old.Subjects, current.Subjects) || !slices.Equal(old.Installed, current.Installed) {
		return false
	}
	return current.Status == old.Status || old.Status == "active" && current.Status == "expired"
}

func managedLateAuthorizationCompleted(j environmentCreateJournal, late *createManagedLateSessionRecovery, base string, recipe deploymentplan.Object) bool {
	if late == nil || !environmentPreflightDigest.MatchString(late.Review) || recipe == nil {
		return false
	}
	var outer *environmentCreateJournalEntry
	indices := map[string]int{}
	var at int
	for index := range j.Entries {
		entry := &j.Entries[index]
		if entry.Key == base+"-"+late.Review {
			if outer != nil {
				return false
			}
			outer = entry
			at = index
		}
	}
	if outer == nil || outer.Phase != "late-native-authorization" || outer.State != "completed" || outer.CompletedAt.IsZero() || outer.IntentSHA256 != activeTransportJournalDigest(recipe) || activeTransportJournalDigest(jsonRawObject(outer.Receipt)) != activeTransportJournalDigest(recipe) {
		return false
	}
	for _, kind := range []string{"policy_intent", "role_intent"} {
		key, intent, err := managedLateAuthorizationEntryIntent(recipe, kind)
		if err != nil {
			return false
		}
		found := false
		for index, entry := range j.Entries {
			if entry.Key != key {
				continue
			}
			if found || index <= at || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.Before(outer.StartedAt) || entry.CompletedAt.IsZero() || entry.CompletedAt.After(outer.CompletedAt) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(intent) {
				return false
			}
			found = true
			indices[kind] = index
		}
		if !found {
			return false
		}
	}
	return indices["role_intent"] == indices["policy_intent"]+1 && indices["policy_intent"] == at+1
}

func managedLateSeedAuthorizationCompleted(j environmentCreateJournal, late *createManagedLateSessionRecovery) bool {
	return late != nil && managedLateAuthorizationCompleted(j, late, "late-seed-authorization", late.SeedAuthorization)
}

// Completed and in-progress child identities are selected only by this parent
// entry's receipt or its next exact recorded child lease. The stored plan must
// reproduce the parent intent before any scope validator sees it.
func readManagedLateProgressChild(store secretStore, e *environmentCreateExecution, entry environmentCreateJournalEntry, lease *deploymentplan.LeaseOwner) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, deploymentplan.Journal{}, environmentCreateBootstrapBlocker("late runtime child intent/receipt/lease differs: " + entry.Key)
	}
	sha := ""
	if entry.State == "completed" {
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
			return bad()
		}
		sha = receipt.Plan
	} else if entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 && lease != nil {
		sha = strings.SplitN(lease.Holder, ":", 2)[0]
	} else {
		return bad()
	}
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(sha) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", sha, "plan.json"), &plan) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil || entry.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) || entry.Phase != strings.TrimPrefix(entry.Key, "kubernetes/") {
		return bad()
	}
	if entry.State == "completed" {
		if !createOpenBaoACKCompletedChild(&plan, journal) || !journal.AcceptanceStarted {
			return bad()
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(journal.Owners) {
			return bad()
		}
		for index, resource := range plan.Resources {
			if journal.Completed[index] != resource.ID || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) || resource.UID != "" && resource.UID != journal.Owners[resource.ID] {
				return bad()
			}
		}
	}
	return &plan, journal, nil
}

func validateManagedLateCredentialChild(late *createManagedLateSessionRecovery, plan *deploymentplan.Plan) error {
	if late == nil || plan == nil || len(plan.Resources) != 3 {
		return environmentCreateBootstrapBlocker("late credential publication requires exactly three new Secrets")
	}
	seen := map[string]bool{}
	certificate := ""
	ica, err := parseFirstCertificatePEM([]byte(late.SourceCeremony.Intermediate.Certificate))
	if err != nil {
		return err
	}
	for _, resource := range plan.Resources {
		meta := deploymentplan.Map(resource.Desired["metadata"])
		ns := deploymentplan.String(meta["namespace"])
		if seen[ns] || !slices.Contains(late.SourceCeremony.BootstrapCredentialNamespaces, ns) || resource.ID != "Secret/"+ns+"/service-bootstrap-"+late.NewSessionID || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || resource.Desired["immutable"] != true {
			return environmentCreateBootstrapBlocker("late new Secret allocation changed")
		}
		seen[ns] = true
		data := deploymentplan.Map(resource.Desired["data"])
		if len(data) != 3 {
			return environmentCreateBootstrapBlocker("late Secret contains unrelated credential material")
		}
		decode := func(key string) ([]byte, error) {
			return base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data[key]))
		}
		cert, err := decode("tls.crt")
		if err != nil {
			return err
		}
		key, err := decode("tls.key")
		if err != nil {
			return err
		}
		ca, err := decode("ca.crt")
		if err != nil {
			return err
		}
		identity, err := tls.X509KeyPair(cert, key)
		if err != nil || !managedLateBootstrapChainMatches(identity.Certificate, late.SourceCeremony) || environmentPreflightBytesSHA256(key) != late.OriginalKeySHA || string(ca) != late.SourceCeremony.Root.Certificate {
			return environmentCreateBootstrapBlocker("late Secret does not contain the original key and exact original chain")
		}
		leaf, err := x509.ParseCertificate(identity.Certificate[0])
		if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) || leaf.NotAfter.Before(leaf.NotBefore) || leaf.CheckSignatureFrom(ica) != nil {
			return environmentCreateBootstrapBlocker("late credential native signer changed")
		}
		if certificate != "" && certificate != string(cert) {
			return environmentCreateBootstrapBlocker("late credential copies differ")
		}
		certificate = string(cert)
		definition := environmentCreateSecret(ns, "service-bootstrap-"+late.NewSessionID, map[string]string{"tls.crt": string(cert), "tls.key": string(key), "ca.crt": string(ca)})
		definition.Object["immutable"] = true
		deploymentplan.Map(definition.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": late.NewSessionID, "app.kubernetes.io/managed-by": "rtk-cloud"}
		// Compile normalizes stringData to data, so compare the persisted shape.
		delete(definition.Object, "stringData")
		definition.Object["data"] = deploymentplan.Clone(deploymentplan.Object{"data": data})["data"]
		if deploymentplan.Digest(definition.Object) != deploymentplan.Digest(resource.Desired) {
			return environmentCreateBootstrapBlocker("late Secret producer changed beyond its genuine signed certificate")
		}
	}
	return nil
}

func validateManagedLateSeedObserved(store secretStore, late *createManagedLateSessionRecovery, j environmentCreateJournal, plan *deploymentplan.Plan, entry environmentCreateJournalEntry) error {
	bad := func(stage string) error {
		return environmentCreateBootstrapBlocker("late signed credential lacks exact native allocation observation: " + stage)
	}
	var observed createManagedLateSeedObservation
	if late == nil || plan == nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), &observed) != nil || observed.Schema != managedLateSessionSchema || observed.Parent != late.ParentSHA || observed.Review != late.Review || observed.KeySHA != late.OriginalKeySHA || observed.CSRSHA != late.OriginalCSRSHA || observed.ObservedAt.IsZero() || observed.ObservedAt.After(entry.StartedAt) {
		return bad("descriptor")
	}
	current := &environmentCreateExecution{Store: store, Journal: j}
	seed, actualSeedEntry, seedJournal, err := readManagedLateEffectiveSeed(store, current, late)
	seedEntry := &actualSeedEntry
	custody := createOpenBaoACKCompletionEntry(j, "kubernetes/service-custody-reader-renewal-"+late.Review)
	if err != nil || seed == nil || !managedLateSamePlan(seed, late.SeedCandidate) && !managedLateEffectiveSeedIsForward(store, current, late, seed) || observed.SeedPlanSHA != seed.SHA256() || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", seed.SHA256(), "journal.json"), &seedJournal) != nil || observed.SeedJournalSHA != deploymentplan.Digest(seedJournal) || seedEntry == nil || observed.ObservedAt.Before(seedEntry.CompletedAt) || custody == nil || custody.State != "completed" || observed.ObservedAt.Before(custody.CompletedAt) {
		return bad("accepted-seed")
	}
	block, err := parseFirstCertificatePEM([]byte(observed.CertificatePEM))
	state := observed.Session
	if err != nil || state.Session != late.NewSessionID || state.Issuer != late.SourceCeremony.Intermediate.ID || state.Root != late.SourceCeremony.Root.RootFingerprint || state.Status != "active" || state.OtherActive != 0 || len(state.Installed) != 0 || !slices.Equal(state.Subjects, late.SourceRenewedIntent.Subjects) || state.Fingerprint != environmentPreflightBytesSHA256(block.Raw) || !state.Expires.Equal(block.NotAfter) || !state.Expires.After(observed.ObservedAt) || block.NotBefore.After(observed.ObservedAt) {
		return bad("native-session")
	}
	for _, resource := range plan.Resources {
		raw, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["tls.crt"]))
		if err != nil || string(raw) != observed.CertificatePEM {
			return bad("credential-copy")
		}
	}
	return nil
}

func validateManagedLateCleanupChild(late *createManagedLateSessionRecovery, plan *deploymentplan.Plan, credentialJournal deploymentplan.Journal) error {
	if late == nil || plan == nil || len(plan.Resources) != 5 {
		return environmentCreateBootstrapBlocker("late cleanup requires one new binding and four original RBAC owners")
	}
	var binding environmentBootstrapCleanupBinding
	found := false
	for _, resource := range plan.Resources {
		if resource.ID != "ConfigMap/"+plan.Stack+"-video-cloud/pki-bootstrap-cleanup-binding-"+late.NewSessionID[:12] {
			continue
		}
		if found || decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["binding.json"])), &binding) != nil {
			return environmentCreateBootstrapBlocker("late cleanup binding malformed")
		}
		found = true
	}
	if !found || binding.Version != 1 || binding.Environment != plan.Environment || binding.SessionID != late.NewSessionID || binding.NamespaceUID != late.SourceCeremony.CleanupBinding.NamespaceUID || binding.PolicySHA256 != late.SignerPolicySHA || binding.SecretUID != credentialJournal.Owners["Secret/"+late.SourceCeremony.BootstrapCredentialNamespace+"/service-bootstrap-"+late.NewSessionID] {
		return environmentCreateBootstrapBlocker("late cleanup UID was not allocated by the accepted new credential child")
	}
	binding.KubernetesCA = late.SourceCeremony.CleanupBinding.KubernetesCA
	binding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + late.NewSessionID[:12]
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	service.CleanupBinding = binding
	defs, err := managedLateSessionCleanupDefinitions(late, service)
	if err != nil {
		return err
	}
	live := []deploymentplan.Object{}
	for _, resource := range plan.Resources {
		if resource.UID != "" {
			body := deploymentplan.Clone(resource.Baseline)
			deploymentplan.Map(body["metadata"])["uid"] = resource.UID
			live = append(live, body)
		}
	}
	expected, err := deploymentplan.Compile(plan.Inputs, defs, live)
	if err != nil || expected.SHA256() != plan.SHA256() {
		return environmentCreateBootstrapBlocker("late cleanup producer or original RBAC baseline changed")
	}
	return nil
}

func managedLateAdmissionMatches(a createFoundationRecoveryArtifact, source environmentCreateJournal, entry environmentCreateJournalEntry, late *createManagedLateSessionRecovery) bool {
	if late == nil || !managedLateSchemaMatches(a.Plan.Schema, late) || deploymentplan.Digest(a.Plan) != late.Review || a.Plan.ParentSHA != late.ParentSHA || a.Plan.ParentJournalSHA != late.SourceSHA || deploymentplan.Digest(source) != late.SourceSHA || a.Plan.ParentOwnerSHA != late.SourceOwnerSHA || a.Qualification.PlanSHA256 != late.Review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || entry.Key != "openbao-recovery-admission-"+late.Review || entry.Phase != "recovery" || entry.State != "completed" || entry.StartedAt.Before(a.QualifiedAt) || !entry.StartedAt.Before(a.ValidUntil) || entry.CompletedAt.Before(entry.StartedAt) || a.Plan.Materials["accepted-service-core-late-session-source-descriptor"] != managedLateSessionSourceDigest(late) {
		return false
	}
	for index, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		gate := a.Qualification.Gates[index]
		if gate.ID != id || gate.Status != "PASS" || !gate.Required || gate.EvidenceSHA256 != late.Review {
			return false
		}
	}
	var receipt map[string]string
	return entry.IntentSHA256 == activeTransportJournalDigest(map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": late.SourceSHA}) && decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && len(receipt) == 2 && receipt["qualified_recovery_sha256"] == late.Review && environmentPreflightDigest.MatchString(receipt["audit_sha256"])
}

type managedLateProgressStep struct {
	Key, Phase, Kind string
	Intent           deploymentplan.Object
	Candidate        *deploymentplan.Plan
}

func managedLateProgressSteps(late *createManagedLateSessionRecovery) ([]managedLateProgressStep, error) {
	if late == nil || late.SeedCandidate == nil || late.CustodyCandidate == nil || late.OwnerCandidate == nil || validateManagedLateLogCandidates(late) != nil {
		return nil, environmentCreateBootstrapBlocker("late descriptor has no exact complete producer plans")
	}
	var steps []managedLateProgressStep
	addNative := func(base string, recipe deploymentplan.Object) error {
		steps = append(steps, managedLateProgressStep{Key: base + "-" + late.Review, Phase: "late-native-authorization", Kind: "native", Intent: recipe})
		for _, which := range []string{"policy_intent", "role_intent"} {
			key, intent, err := managedLateAuthorizationEntryIntent(recipe, which)
			if err != nil {
				return err
			}
			steps = append(steps, managedLateProgressStep{Key: key, Phase: "bootstrap", Kind: "inner", Intent: intent})
		}
		return nil
	}
	addKube := func(base, kind string, candidate *deploymentplan.Plan) {
		stage := base + "-" + late.Review
		steps = append(steps, managedLateProgressStep{Key: "kubernetes/" + stage, Phase: stage, Kind: kind, Candidate: candidate})
	}
	if err := addNative("late-seed-authorization", late.SeedAuthorization); err != nil {
		return nil, err
	}
	addKube("service-session-original-key-renewal", "kube", late.SeedCandidate)
	addKube("service-custody-reader-renewal", "kube", late.CustodyCandidate)
	addKube("service-credential-secrets-renewal", "credentials", nil)
	addKube("service-cleanup-binding-renewal", "cleanup", nil)
	if err := addNative("late-cleanup-authorization", late.CleanupAuthorization); err != nil {
		return nil, err
	}
	addKube("managed-late-session-owners", "owners", late.OwnerCandidate)
	addKube("managed-late-log-stop", "kube", late.LogStopCandidate)
	addKube("managed-late-log-existing-ack", "kube", late.LogHelperCandidate)
	addKube("managed-late-log-retire", "retire", late.LogRetireCandidate)
	addKube("managed-late-log-restore", "kube", late.LogRestoreCandidate)
	steps = append(steps, managedLateProgressStep{Key: "managed-late-log-verified-" + late.Review, Phase: "recovery", Kind: "log-proof"})
	return steps, nil
}

// Return only the captured source view after proving the actual live suffix.
// Native readers still receive e itself, never this historical copy.
func managedLateSessionRuntimeProgress(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*environmentCreateExecution, *createFoundationRecoveryArtifact, error) {
	bad := func(stage string) (*environmentCreateExecution, *createFoundationRecoveryArtifact, error) {
		return nil, nil, environmentCreateBootstrapBlocker("late runtime requires exact admitted monotonic source: " + stage)
	}
	if e == nil || late == nil || store.Root != e.Store.Root || late.Mode != managedLateSessionACKReady || !environmentPreflightDigest.MatchString(late.Review) {
		return bad("selected-owner")
	}
	framed, postMint, frameErr := managedLatePostMintRuntimeFrame(store, e, late)
	if frameErr != nil {
		return nil, nil, frameErr
	}
	e = framed
	a, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil || len(e.Journal.Entries) < len(source.Entries)+1 || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) {
		return bad("actual-six-go-admission")
	}
	var captured createManagedLateSessionRecovery
	var owner environmentCreateOwner
	base := filepath.Join("deployment/create/openbao-recovery", late.Review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "managed-late-session-source.json"), &captured) != nil || managedLateSessionSourceDigest(&captured) != managedLateSessionSourceDigest(late) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || deploymentplan.Digest(owner) != late.SourceOwnerSHA || owner.PlanSHA256 != late.ParentSHA || owner.OwnerToken != source.OwnerToken || e.owner.Schema != "" && deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) || !managedLateAdmissionMatches(a, source, e.Journal.Entries[len(source.Entries)], late) {
		return bad("source-descriptor-and-admission")
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:len(source.Entries)]
	prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return bad("immutable-prefix-metadata")
	}
	pure := *e
	pure.Journal = source
	if late.ExpiredRenewal != nil {
		_, f, err := loadManagedLateExpiredRenewalSource(late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair.Plan.Configuration, store, &pure)
		if err != nil {
			return nil, nil, fmt.Errorf("authenticate expired unused historical source: %w", err)
		}
		if managedLateExpiredRenewalSourcePins(f) != managedLateExpiredRenewalSourcePins(late.ExpiredRenewal) {
			return bad("authenticated-expired-unused-source")
		}
	} else if _, _, _, _, _, err := loadManagedLateSessionOriginalSource(store, &pure); err != nil {
		return bad("authenticated-core-mqtt-tls-source")
	}
	seedForward, parsedEntries, parsedLeases, frameErr := managedLateSeedForwardFrame(store, e, late, source)
	if frameErr != nil {
		return nil, nil, frameErr
	}
	steps, err := managedLateProgressSteps(late)
	if err != nil {
		return nil, nil, err
	}
	if postMint != nil {
		for i := range steps {
			if steps[i].Kind == "owners" {
				steps[i].Candidate = postMint.Candidate
			}
		}
	}
	tail := parsedEntries[len(source.Entries)+1:]
	leaseIndex := len(source.KubernetesLeases)
	stepIndex := 0
	var credentialJournal deploymentplan.Journal
	helperUID := ""
	unsealSeen := false
	for _, entry := range tail {
		// A controlled same-owner OpenBao replacement inserts only its actual
		// original-share unseal proof directly after the owner child starts.
		if strings.HasPrefix(entry.Key, "openbao-managed-unseal-") {
			if unsealSeen || stepIndex == 0 || steps[stepIndex-1].Kind != "owners" || validateManagedLateUnseal(store, late, entry) != nil {
				return bad("original-share-unseal")
			}
			unsealSeen = true
			continue
		}
		if stepIndex >= len(steps) {
			return bad("unknown-tail")
		}
		step := steps[stepIndex]
		if stepIndex > 0 {
			previous := createOpenBaoACKCompletionEntry(e.Journal, steps[stepIndex-1].Key)
			if previous == nil || previous.State != "completed" && !(steps[stepIndex-1].Kind == "native" && step.Kind == "inner") {
				return bad("pending-stage-cannot-authorize-next-stage")
			}
			if previous.State == "completed" && steps[stepIndex-1].Kind != "native" && entry.StartedAt.Before(previous.CompletedAt) {
				return bad("non-monotonic-stage-timestamps")
			}
		}
		if step.Key == "kubernetes/service-session-original-key-renewal-"+late.Review {
			outer := createOpenBaoACKCompletionEntry(e.Journal, "late-seed-authorization-"+late.Review)
			if !managedLateSeedAuthorizationCompleted(e.Journal, late) || outer == nil || entry.StartedAt.Before(outer.CompletedAt) {
				return bad("native-signer-not-completed-before-seed")
			}
		}
		if step.Kind == "owners" {
			outer := createOpenBaoACKCompletionEntry(e.Journal, "late-cleanup-authorization-"+late.Review)
			if !managedLateAuthorizationCompleted(e.Journal, late, "late-cleanup-authorization", late.CleanupAuthorization) || outer == nil || entry.StartedAt.Before(outer.CompletedAt) {
				return bad("native-cleanup-not-completed-before-owners")
			}
		}
		if entry.Key != step.Key || entry.Phase != step.Phase || entry.StartedAt.IsZero() || entry.State != "pending" && entry.State != "completed" || entry.State == "pending" && (!entry.CompletedAt.IsZero() || len(entry.Receipt) != 0) || entry.State == "completed" && (entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt)) {
			return bad("literal-step-order")
		}
		if step.Kind == "native" || step.Kind == "inner" {
			if entry.IntentSHA256 != activeTransportJournalDigest(step.Intent) || entry.State == "completed" && activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(step.Intent) {
				return bad("native-qualified-recipe")
			}
			if step.Kind == "native" {
				if leaseIndex >= len(parsedLeases) {
					return bad("native-scoped-lease")
				}
				lease := parsedLeases[leaseIndex]
				bindings := deploymentplan.Digest(map[string]string{"schema": a.Plan.Schema, "review": late.Review, "stage": entry.Key, "recipe": deploymentplan.Digest(step.Intent)})
				planned := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: source.Environment, Stack: source.Stack, Operation: "scoped", BindingsSHA256: bindings}}
				if !managedLateLeaseMatches(lease, source.Stack, planned.SHA256()) {
					return bad("exact-native-lease")
				}
				leaseIndex++
			}
		} else if step.Kind == "log-proof" {
			var proof createManagedLateLogProof
			if decodeEnvironmentJSON(entry.Receipt, &proof) != nil || validateManagedLateLogCompletion(late, &proof) != nil || entry.State != "completed" || entry.IntentSHA256 != activeTransportJournalDigest(proof) || entry.CompletedAt.Before(proof.RestoredAt) {
				return bad("genuine-log-final-proof")
			}
		} else {
			if leaseIndex >= len(parsedLeases) {
				return bad("actual-child-lease")
			}
			lease := parsedLeases[leaseIndex]
			var plan *deploymentplan.Plan
			var journal deploymentplan.Journal
			var err error
			forwardSeed := seedForward != nil && entry.Key == "kubernetes/service-session-original-key-renewal-"+late.Review && entry.State == "completed"
			if forwardSeed {
				plan, _, journal, err = readManagedLateEffectiveSeed(store, e, late)
			} else {
				plan, journal, err = readManagedLateProgressChild(store, e, entry, &lease)
			}
			if err != nil || plan.ConfigurationSHA256 != a.Plan.Plan.ConfigurationSHA256 || plan.ReleaseSHA256 != deploymentplan.Digest(a.Plan.Plan.Release) || !managedLateLeaseMatches(lease, source.Stack, plan.SHA256()) && !(forwardSeed && managedLateLeaseMatches(lease, source.Stack, seedForward.FailedPlanSHA)) {
				return bad("exact-child-inputs-lease")
			}
			leaseIndex++
			switch step.Kind {
			case "credentials":
				if err := validateManagedLateCredentialChild(late, plan); err != nil {
					return nil, nil, fmt.Errorf("%w: three-new-credential-producer: %v", environmentCreateBootstrapBlocker("late runtime requires exact admitted monotonic source"), err)
				}
				if err := validateManagedLateSeedObserved(store, late, e.Journal, plan, entry); err != nil {
					return nil, nil, fmt.Errorf("%w: three-new-credential-producer: %v", environmentCreateBootstrapBlocker("late runtime requires exact admitted monotonic source"), err)
				}
				credentialJournal = journal
			case "cleanup":
				if validateManagedLateCleanupChild(late, plan, credentialJournal) != nil {
					return bad("allocated-cleanup-producer")
				}
			case "retire":
				if validateManagedLateLogRetireAllocated(late, plan, helperUID) != nil {
					return bad("actual-helper-retirement")
				}
			default:
				if !forwardSeed && !managedLateSamePlan(plan, step.Candidate) {
					return bad("unchanged-qualified-child")
				}
			}
			if step.Key == "kubernetes/managed-late-log-existing-ack-"+late.Review && entry.State == "completed" {
				helperUID = journal.Owners[plan.Resources[0].ID]
			}
		}
		stepIndex++
	}
	if leaseIndex != len(parsedLeases) {
		return bad("undeclared-lease")
	}
	// Completed wrappers necessarily contain both exact native child receipts.
	for _, item := range []struct {
		base   string
		recipe deploymentplan.Object
	}{{"late-seed-authorization", late.SeedAuthorization}, {"late-cleanup-authorization", late.CleanupAuthorization}} {
		entry := createOpenBaoACKCompletionEntry(e.Journal, item.base+"-"+late.Review)
		if entry != nil && entry.State == "completed" && !managedLateAuthorizationCompleted(e.Journal, late, item.base, item.recipe) {
			return bad("native-wrapper-inner-completion")
		}
	}
	return &pure, &a, nil
}

func managedLateLeaseMatches(lease deploymentplan.LeaseOwner, stack, sha string) bool {
	return lease.ResourceID == "Lease/"+stack+"-video-cloud/rtk-deployment-execution" && environmentBootstrapOwnerUUID.MatchString(lease.UID) && strings.HasPrefix(lease.Holder, sha+":") && strings.TrimPrefix(lease.Holder, sha+":") != ""
}

func validateManagedLateUnseal(store secretStore, late *createManagedLateSessionRecovery, entry environmentCreateJournalEntry) error {
	var proof struct {
		Pod         string `json:"pod_uid"`
		Revision    string `json:"revision"`
		Initialized bool   `json:"initialized"`
		Sealed      bool   `json:"sealed"`
	}
	if decodeEnvironmentJSON(entry.Receipt, &proof) != nil || entry.Phase != "openbao-unseal" || entry.State != "completed" || entry.CompletedAt.IsZero() || !environmentBootstrapOwnerUUID.MatchString(proof.Pod) || proof.Revision == "" || !proof.Initialized || proof.Sealed || entry.Key != "openbao-managed-unseal-"+proof.Pod {
		return environmentCreateBootstrapBlocker("late original-share unseal proof changed")
	}
	var originalInit struct {
		Keys []string `json:"unseal_keys_b64"`
		Root string   `json:"root_token"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	if err != nil || json.Unmarshal(raw, &originalInit) != nil || len(originalInit.Keys) != 1 {
		return environmentCreateBootstrapBlocker("late original unseal custody unavailable")
	}
	intent := deploymentplan.Object{"pod_uid": proof.Pod, "controller_uid": late.Owners["StatefulSet/"+late.AcceptedCore.Stack+"-secrets/openbao"].UID, "revision": proof.Revision, "original_init_sha256": environmentPreflightBytesSHA256(raw), "unseal_sha256": environmentPreflightBytesSHA256([]byte(originalInit.Keys[0]))}
	if entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return environmentCreateBootstrapBlocker("late original unseal intent differs")
	}
	return nil
}

func verifyManagedLateSessionRuntimeSource(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery) error {
	if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.ManagedLateRemaining != nil {
		if err := verifyManagedLateRemainingAuthorityCurrent(store, runtime.Execution, runtime.ServiceSessionRecovery.ManagedLateRemaining); err != nil {
			return err
		}
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, runtime.Execution, late); err != nil {
		return err
	}
	source, a, err := managedLateSessionRuntimeProgress(store, runtime.Execution, late)
	if err != nil {
		return err
	}
	p := &createFoundationRecoveryPlan{Plan: a.Plan.Plan, Materials: map[string]string{}}
	r, loaded, err := loadManagedLateEffectiveSource(cfg, store, source, p, late)
	if err != nil || loaded.SourceSHA != late.SourceSHA {
		return environmentCreateBootstrapBlocker("late original source could not be rederived")
	}
	// Only the initial boundary observes the old seed copies/custody/workloads.
	// After admission progress starts, those exact owners legitimately change
	// under the already validated child recipes; identity continuity remains a
	// native read of unchanged clients/servers/issuers/volumes, not old bodies.
	if late.ExpiredRenewal == nil && len(runtime.Execution.Journal.Entries) == len(source.Journal.Entries)+1 {
		return verifyManagedLateSessionObservationWithMode(ctx, cfg, store, runtime.Execution, runtime.Backend, r, p, late, late.Mode)
	}
	for key, value := range a.Plan.Materials {
		p.Materials[key] = value
	}
	if runtime.ServiceSessionRecovery == nil || runtime.ServiceSessionRecovery.ManagedLateSession == nil || managedLateSessionSourceDigest(runtime.ServiceSessionRecovery.ManagedLateSession) != managedLateSessionSourceDigest(late) {
		return environmentCreateBootstrapBlocker("late current native verifier requires the immutable runtime descriptor")
	}
	return verifyManagedLateSessionCurrentNative(ctx, cfg, store, runtime.Execution, runtime.Backend, runtime.ServiceSessionRecovery, p, late)
}
