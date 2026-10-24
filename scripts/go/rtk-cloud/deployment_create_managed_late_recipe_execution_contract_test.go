package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type lateRecipeExecutionContract struct {
	f    *acceptedCoreExecutionContract
	r    *createServiceSessionRecovery
	p    createFoundationRecoveryPlan
	late *createManagedLateSessionRecovery
}

// This validates the typed recipe boundary. Its prior eight installations and
// host source digest are explicit already-qualified inputs; it does not claim
// the complete historical source loader or public admission. The original seed
// is genuinely expired and signed, native owners/leases/child journals are real
// executions, and the Log claim, identity and policy recipes use their actual
// maintained producers. No ancestor rows are padded or retroactively inserted.
func newLateRecipeExecutionContract(t *testing.T) lateRecipeExecutionContract {
	t.Helper()
	f := newCoreExecutionContractAtBoundaryWithHooks(t, true, true, false, historicalOriginalFoundationHooks(t), historicalCompleteDomainPolicy(t))
	originalState := f.protocol["historical_sessions"].(map[string]managedSessionState)[f.session]
	originalNativeState := originalState
	_, late, _ := newHistoricalOriginalLogNativeContract(t, f)
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalState.Installed = nil
	for _, subject := range subjects {
		if subject != logACKSubject {
			originalState.Installed = append(originalState.Installed, subject)
		}
	}
	if originalState.Expires.After(time.Now()) || !managedLateSessionMayRenew(originalState, f.service, subjects, time.Now()) {
		t.Fatal("typed source does not preserve the actual expired signed session")
	}
	// Create the other nine typed workload recipes through the original scoped
	// executor. This fixture has real original owner UIDs, not invented receipts.
	templates, _ := managedLateOwnersFixture(t)
	var defs []deploymentplan.Definition
	for _, name := range f.p.Plan.Bootstrap.Claims {
		suffix := "video-cloud"
		if name == "account-manager-service-state" {
			suffix = "account-manager"
		}
		if name == "openbao-pki-identity" {
			suffix = "secrets"
		}
		defs = append(defs, environmentCreateServiceClaimDefinition(f.cfg, f.p.Plan.Stack+"-"+suffix, name))
	}
	privateClaims := map[string]string{"account-manager": "account-manager-service-state", "pki-controller": "pki-controller-state", "certissuer": "certissuer-service-state", "factoryenroll": "factory-service-state", "video-cloud-api": "video-cloud-api-service-state", "video-cloud-api-pki": "video-cloud-api-pki-controller-identity", "video-cloud-api-app-pki": "video-cloud-api-app-service-state", "mqtt-pki": "mqtt-pki-host-identity", "openbao": "openbao-pki-identity"}
	for _, id := range managedLateNativeOwnerIDs(f.p.Plan.Stack) {
		if id == late.LogProducer.Original.ID {
			continue
		}
		body := deploymentplan.Clone(templates.Owners[id].Desired)
		c := managedNamedField(environmentPKIPod(body)["containers"], "native")
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": f.session}))
		// These are recipe-input workloads, independent of native installation
		// qualification. Their private-state field and actual claim stay fixed.
		parts := strings.Split(id, "/")
		claim := privateClaims[parts[2]]
		if claim == "" {
			t.Fatal("original private claim recipe absent", id)
		}
		deploymentplan.Map(managedNamedField(environmentPKIPod(body)["volumes"], "identity")["persistentVolumeClaim"])["claimName"] = claim
		defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: body, Dependencies: []string{"PersistentVolumeClaim/" + parts[1] + "/" + claim}})
	}
	defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Clone(late.LogProducer.Original.Desired), Retain: true})
	sourcePlan := f.stage(t, "kubernetes/late-recipe-original-owners", defs)
	_, sourceJournal, err := acceptedCreateStage(f.store, f.e, "kubernetes/late-recipe-original-owners")
	if err != nil {
		t.Fatal(err)
	}
	late.AcceptedCore, late.AcceptedCoreJournal = sourcePlan, sourceJournal
	all := map[string]deploymentplan.Resource{}
	if err := managedLateRecordedOwners(sourcePlan, sourceJournal, all); err != nil {
		t.Fatal(err)
	}
	late.Owners, late.DependencyOwners = map[string]deploymentplan.Resource{}, map[string]deploymentplan.Resource{}
	for id, owner := range all {
		if strings.HasPrefix(id, "PersistentVolumeClaim/") {
			late.DependencyOwners[id] = owner
		} else {
			late.Owners[id] = owner
		}
	}
	late.SourceCeremony, late.OriginalSessionState = f.service, originalState
	late.OriginalSession, late.OriginalEnrollmentSession = f.session, f.session
	late.OriginalKeySHA, late.OriginalCSRSHA = environmentPreflightBytesSHA256([]byte(f.key)), environmentPreflightBytesSHA256([]byte(f.csr))
	// Preserve the genuinely signed Log row and add the remaining original
	// subjects, including the three separately owned API clients.
	clients := slices.Clone(f.protocol["historical_clients"].([]managedSessionClient))
	keys := f.protocol["historical_client_keys"].(map[string]string)
	for _, subject := range subjects {
		if subject == logACKSubject {
			continue
		}
		count := 1
		if subject == "service:video-cloud-api" {
			count = 3
		}
		for i := 0; i < count; i++ {
			certificate, key, csr := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, subject, nil)
			leaf, err := parseFirstCertificatePEM([]byte(certificate))
			if err != nil {
				t.Fatal(err)
			}
			request := fmt.Sprintf("%s:original-recipe-client:%d", subject, i)
			clients = append(clients, managedSessionClient{Subject: subject, Caller: subject, Request: request, Issuer: f.service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "succeeded", CSR: csr, Certificate: certificate})
			keys[request] = key
		}
	}
	slices.SortFunc(clients, func(a, b managedSessionClient) int {
		return strings.Compare(a.Subject+"\x00"+a.Caller+"\x00"+a.Request, b.Subject+"\x00"+b.Caller+"\x00"+b.Request)
	})
	if !validateManagedSessionClients(clients, f.service, subjects, time.Now()) {
		t.Fatal("original eleven signed clients")
	}
	installHistoricalTransportProtocol(t, f, historicalTransportIdentityObservations{Clients: clients, ClientKeys: keys})
	ledger := historicalTransportCaptureVolumes(t, f)
	if len(ledger.Volumes) != 18 {
		t.Fatal("canonical bootstrap15 and original foundation3 claims", len(ledger.Volumes))
	}
	late.OriginalClientsSHA = deploymentplan.Digest(clients)
	// Retain the original exclusive signer role while genuinely publishing the
	// selected controller cleanup policy via the native protocol and journal.
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{f.session: originalNativeState})
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: filepath.Join(f.store.Root, "openbao/root-token")}}
	cleanupName := "create-" + f.session + "-cleanup-controller"
	if err := installEnvironmentCreateOpenBaoPolicy(t.Context(), f.cfg, runtime, cleanupName, "path \"sys/policies/acl/rtk-dev-service-bootstrap-"+f.session+"\" { capabilities = [\"read\", \"delete\"] }\n"); err != nil {
		t.Fatal("actual original controller policy", err)
	}
	late.ControllerPolicies = environmentCreateControllerPolicies(runtime)
	if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, runtime, "pki-controller-dev", []string{"pki-controller"}, late.ControllerPolicies); err != nil {
		t.Fatal("actual original controller role", err)
	}
	late.OriginalSignerRole, err = readManagedLateOpenBaoRole(t.Context(), f.cfg, f.store, f.backend, "pki-service-bootstrap-dev")
	if err != nil {
		t.Fatal(err)
	}
	late.OriginalControllerRole, err = readManagedLateOpenBaoRole(t.Context(), f.cfg, f.store, f.backend, "pki-controller-dev")
	if err != nil {
		t.Fatal(err)
	}
	late.OriginalSignerRoleSHA, late.OriginalControllerRoleSHA = deploymentplan.Digest(late.OriginalSignerRole), deploymentplan.Digest(late.OriginalControllerRole)
	late.SignerPolicySHA = environmentPreflightBytesSHA256([]byte(f.protocol["policy"].(environmentCreateOpenBaoIssuerPolicies).ServiceClientSignerPolicy))
	late.SourceSHA, late.SourceOwnerSHA = deploymentplan.Digest(f.e.Journal), deploymentplan.Digest(f.e.owner)
	r := &createServiceSessionRecovery{Ceremony: f.service, SourceRenewalStage: "kubernetes/service-session-original-key-renewal-" + f.review, RenewedCertPath: "/run/pki-bootstrap/renew-" + f.review[:12] + "/bootstrap.crt", OriginalKeySHA: late.OriginalKeySHA, OriginalCSRSHA: late.OriginalCSRSHA, ManagedCorePartial: &createManagedCorePartialRecovery{Apps: sourcePlan, AppsJournal: sourceJournal}, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{ServerSHA: deploymentplan.Digest("qualified-host-source-boundary")}}}
	// The accepted renewal's exact mounted certificate path comes from its
	// original observed custody, never a guessed replacement path.
	original, originalJournal, err := acceptedCreateStage(f.store, f.e, r.SourceRenewalStage)
	if err != nil {
		t.Fatal(err)
	}
	oldContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original.Resources[0].Desired)["containers"])[0])
	r.RenewedCertPath = mqttFoundationEnv(oldContainer, "PKI_BOOTSTRAP_CERT_PATH")
	if r.RenewedCertPath == "" {
		t.Fatal("original accepted seed certificate path missing")
	}
	if err := createOpenBaoACKCompletionRead(f.store, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-intent.json"), &r.Intent); err != nil {
		t.Fatal("original intent", err)
	}
	late.Renewal = &createManagedSessionRecovery{OriginalSession: f.session, OriginalCertificatePath: r.RenewedCertPath, OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte(f.certificate)), SessionSHA256: deploymentplan.Digest(originalState), ClientsSHA256: late.OriginalClientsSHA, SourceStage: r.SourceRenewalStage, SourcePlanSHA256: original.SHA256(), SourceJournalSHA256: deploymentplan.Digest(originalJournal)}
	binding := deploymentplan.Digest(map[string]string{"schema": managedLateSessionSchema, "parent": late.ParentSHA, "accepted_source": late.SourceSHA, "accepted_core": late.AcceptedCore.SHA256(), "accepted_core_journal": deploymentplan.Digest(late.AcceptedCoreJournal), "original_session": late.OriginalSession, "original_session_state": late.Renewal.SessionSHA256, "clients": late.OriginalClientsSHA, "original_key": late.OriginalKeySHA, "original_csr": late.OriginalCSRSHA})
	late.Renewal.BindingSHA256 = binding
	late.NewSessionID = sessionRenewalUUID(late.ParentSHA, binding, late.OriginalSession)
	late.NewCertificatePath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	image := f.installFormalRepairPublication(t)
	_, selection, err := readManagedLateQualifiedSeedImage(f.store, late.ParentSHA)
	if err != nil {
		t.Fatal(err)
	}
	p := createFoundationRecoveryPlan{Plan: f.p.Plan, Materials: map[string]string{"accepted-service-core-late-session-image": deploymentplan.Digest(image), "accepted-service-core-late-session-image-selection": selection, "accepted-service-core-managed-application-current-api-servers": deploymentplan.Digest("qualified-api-server-source-boundary")}}
	seed, err := managedSessionRenewalDefinition(original.Resources[0].Desired, late.ParentSHA, binding, late.NewSessionID, image)
	if err != nil {
		t.Fatal(err)
	}
	late.SeedCandidate, err = managedLateCompile(p.Plan, "managed-late-original-key-session", []deploymentplan.Definition{seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	late.Renewal.CandidateSHA256 = late.SeedCandidate.SHA256()
	r.RenewalCandidate = late.SeedCandidate
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	ownerDefs, err := managedLateSessionOwnerDefinitions(late, service)
	if err != nil {
		t.Fatal(err)
	}
	ownerDefs, err = managedLateDefinitionClosure(ownerDefs, late.DependencyOwners)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for _, o := range f.native.objects {
		live = append(live, o)
	}
	late.OwnerCandidate, err = managedLateCompile(p.Plan, "managed-late-session-owners", ownerDefs, live)
	if err != nil {
		t.Fatal(err)
	}
	custodyPlan, custodyJournal, err := acceptedCreateStage(f.store, f.e, "kubernetes/service-custody-reader-renewal-"+f.review)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range custodyPlan.Resources {
		if owner.Desired["kind"] == "Deployment" {
			owner.UID = custodyJournal.Owners[owner.ID]
			late.Custody = owner
		}
	}
	reader := deploymentplan.Clone(late.Custody.Desired)
	c := managedNamedField(environmentPKIPod(reader)["containers"], "custody")
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": late.NewSessionID, "PKI_BOOTSTRAP_CERT_PATH": late.NewCertificatePath}))
	late.CustodyCandidate, err = managedLateCompile(p.Plan, "service-custody-reader-renewal", []deploymentplan.Definition{{Phase: "bootstrap", Object: reader}}, live)
	if err != nil {
		t.Fatal(err)
	}
	late.SeedAuthorization, late.CleanupAuthorization, err = managedLateSessionNativeRecipes(f.cfg, late)
	if err != nil {
		t.Fatal(err)
	}
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = late.NewSessionID
	r.RenewedIntent.Subjects = slices.Clone(subjects)
	if err := prepareManagedLateLogCandidates(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r, late); err != nil {
		t.Fatal("actual native Log recipe qualification", err)
	}
	return lateRecipeExecutionContract{f: f, r: r, p: p, late: late}
}

func TestManagedLateQualifiedRecipesPreserveSignedSourceAndRejectCoordinatedDrift(t *testing.T) {
	x := newLateRecipeExecutionContract(t)
	before := createBootstrapStateSnapshot(t, x.f.store.Root)
	if err := validateManagedLateQualifiedRecipes(x.f.cfg, x.f.store, x.f.e, x.r, x.p, x.late, time.Now().UTC()); err != nil {
		t.Fatal("typed qualified producer", err)
	}
	if err := validateManagedLateQualifiedRecipes(x.f.cfg, x.f.store, x.f.e, x.r, x.p, x.late, time.Now().UTC()); err != nil {
		t.Fatal("read-only recipe requalification", err)
	}
	assertRecoveryContractReadOnly(t, x.f.store, before)
	for _, tc := range []struct {
		name, want string
		change     func(*createManagedLateSessionRecovery, *createServiceSessionRecovery, *createFoundationRecoveryPlan)
	}{
		{"original-csr-binding", "distinct-original-key-authority", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.OriginalCSRSHA = deploymentplan.Digest("foreign-csr")
		}},
		{"prior-session-subject", "original-expired-eight", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.OriginalSessionState.Installed = l.OriginalSessionState.Installed[:7]
		}},
		{"source-job-journal", "original-signing-job", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.Renewal.SourceJournalSHA256 = deploymentplan.Digest("foreign-original-job")
		}},
		{"different-new-session", "distinct-original-key-authority", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.NewSessionID = "88888888-8888-4888-8888-888888888888"
		}},
		{"foreign-image-selection", "formal-seed-image", func(_ *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, p *createFoundationRecoveryPlan) {
			p.Materials["accepted-service-core-late-session-image-selection"] = deploymentplan.Digest("foreign-image")
		}},
		{"seed-key-path", "seed-recipe", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(l.SeedCandidate.Resources[0].Desired)["containers"])[0])
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_KEY_PATH": "/replacement/key"}))
		}},
		{"owner-identity-path", "sid-only-owners", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			for i := range l.OwnerCandidate.Resources {
				r := &l.OwnerCandidate.Resources[i]
				if r.Action == "update" {
					c := managedNamedField(environmentPKIPod(r.Desired)["containers"], "native")
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PRIVATE_IDENTITY_STATE": "/replacement/private"}))
					return
				}
			}
		}},
		{"custody-original-uid", "custody-recipe", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.CustodyCandidate.Resources[0].UID = "88888888-8888-4888-8888-888888888888"
		}},
		{"signer-extra-policy", "original-native-roles", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.OriginalSignerRole["token_policies"] = append(certIssuerObjectList(l.OriginalSignerRole["token_policies"]), "default")
			l.OriginalSignerRoleSHA = deploymentplan.Digest(l.OriginalSignerRole)
		}},
		{"cleanup-policy-widening", "native-recipes", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			deploymentplan.Map(l.CleanupAuthorization["policy_intent"])["policy_sha256"] = deploymentplan.Digest("broad-policy")
		}},
		{"log-original-claim", "original-log-source-pins", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.LogProducer.OriginalClaimUID = "88888888-8888-4888-8888-888888888888"
		}},
		{"log-private-state-binding", "original-log-source-pins", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.LogStateSHA = deploymentplan.Digest("replacement-private-state")
		}},
		{"log-helper-binding", "log-source-binding", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.HelperBindingSHA = deploymentplan.Digest("different-helper")
		}},
		{"retire-extra-scope", "exclusive-log-producer", func(l *createManagedLateSessionRecovery, _ *createServiceSessionRecovery, _ *createFoundationRecoveryPlan) {
			l.LogRetireCandidate.Resources = append(l.LogRetireCandidate.Resources, l.LogRetireCandidate.Resources[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var l createManagedLateSessionRecovery
			var r createServiceSessionRecovery
			var p createFoundationRecoveryPlan
			if json.Unmarshal(mustProviderRecoveryJSON(x.late), &l) != nil || json.Unmarshal(mustProviderRecoveryJSON(x.r), &r) != nil || json.Unmarshal(mustProviderRecoveryJSON(x.p), &p) != nil {
				t.Fatal("fixture clone")
			}
			tc.change(&l, &r, &p)
			err := validateManagedLateQualifiedRecipes(x.f.cfg, x.f.store, x.f.e, &r, p, &l, time.Now().UTC())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("wrong precise recipe refusal", tc.name, err)
			}
			assertRecoveryContractReadOnly(t, x.f.store, before)
		})
	}
}
