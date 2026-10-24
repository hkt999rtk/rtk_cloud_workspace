package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// preserveHistoricalIssuedSession records a registry row only at the actual
// certificate publication boundary. Later selected seeds do not erase earlier
// native rows; no missing original session is inferred from archived receipts.
func (f *acceptedCoreExecutionContract) preserveHistoricalIssuedSession(t *testing.T) {
	t.Helper()
	var native struct {
		Session     string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root"`
		Fingerprint string    `json:"fingerprint"`
		Status      string    `json:"status"`
		Expires     time.Time `json:"expires"`
		Subjects    []string  `json:"subjects"`
		Installed   []string  `json:"installed"`
		OtherActive int       `json:"other_active"`
	}
	if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(f.protocol["session"]), &native); err != nil {
		t.Fatal("actual issued native session is malformed", err)
	}
	leaf, err := parseFirstCertificatePEM([]byte(f.certificate))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	if err != nil || issuerErr != nil || leaf.CheckSignatureFrom(issuer) != nil || native.Session != f.session || native.Issuer != f.service.Intermediate.ID || native.Root != f.service.Root.RootFingerprint || native.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !native.Expires.Equal(leaf.NotAfter) || len(native.Subjects) == 0 || native.Status != "active" && native.Status != "expired" {
		t.Fatal("actual issued native session differs from its original signed certificate")
	}
	seen := map[string]bool{}
	for _, subject := range native.Installed {
		if seen[subject] || !slices.Contains(native.Subjects, subject) {
			t.Fatal("actual issued native installation exceeds its subjects")
		}
		seen[subject] = true
	}
	state := managedSessionState{Session: native.Session, Issuer: native.Issuer, Root: native.Root, Fingerprint: native.Fingerprint, Status: native.Status, Expires: native.Expires, Subjects: slices.Clone(native.Subjects), Installed: slices.Clone(native.Installed), OtherActive: native.OtherActive}
	rows, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
	if f.protocol["historical_sessions"] != nil && !ok {
		t.Fatal("actual native session history is malformed")
	}
	if rows == nil {
		rows = map[string]managedSessionState{}
	}
	if original, exists := rows[state.Session]; exists && deploymentplan.Digest(original) != deploymentplan.Digest(state) {
		t.Fatal("actual issuance would replace an existing native session row")
	}
	rows[state.Session] = state
	f.protocol["historical_sessions"] = rows
	recordHistoricalBootstrapIssuance(t, f, state, f.certificate)
}

// publishHistoricalCoreRenewal models the native issuance result with the
// original leaf public key and original intermediate signer. The caller chooses
// the historical leaf lifetime before any immutable Secret/history is written.
// Every child stage executes its real guarded transaction and native ownership.
func (f *acceptedCoreExecutionContract) publishHistoricalCoreRenewal(t *testing.T, r *createServiceSessionRecovery, review string, expired bool, expiresAt ...time.Time) {
	t.Helper()
	var defs []deploymentplan.Definition
	for _, v := range r.Candidate.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	block, _ := pem.Decode([]byte(f.certificate))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	leaf.SerialNumber, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotBefore = time.Now().UTC().Add(-time.Minute)
	leaf.NotAfter = time.Now().UTC().Add(3 * time.Hour)
	if expired {
		leaf.NotBefore = time.Now().UTC().Add(-45 * time.Minute)
		leaf.NotAfter = time.Now().UTC().Add(-time.Minute)
	}
	if len(expiresAt) > 1 {
		t.Fatal("one fixed historical expiry is required")
	}
	if len(expiresAt) == 1 {
		leaf.NotAfter = expiresAt[0].UTC()
		if !leaf.NotAfter.After(leaf.NotBefore) {
			t.Fatal("historical expiry predates original certificate validity")
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, issuer, leaf.PublicKey, f.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f.certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + f.service.Intermediate.ChainPEM
	f.session = r.RenewedIntent.SessionID
	s := f.service
	s.BootstrapSessionID = f.session
	s.BootstrapCredentialSecret = "service-bootstrap-" + f.session
	policy := f.protocol["policy"].(environmentCreateOpenBaoIssuerPolicies)
	name := "rtk-" + f.cfg.Environment + "-service-bootstrap-" + f.session
	policyIntent := deploymentplan.Object{"environment": f.cfg.Environment, "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy))}
	role := "pki-service-bootstrap-" + f.cfg.Environment
	roleIntent := deploymentplan.Object{"role": role, "namespace": s.BootstrapCredentialNamespace, "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{name}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	_, nativePolicyEndpoint := f.protocol["historical_bao_root_token"]
	if nativePolicyEndpoint {
		runtime := f.runtime
		runtime.Execution, runtime.ExecutionSHA256, runtime.Backend = f.e, f.e.Journal.PlanSHA256, f.backend
		if err := installEnvironmentCreateOpenBaoPolicy(t.Context(), f.cfg, runtime, name, policy.ServiceClientSignerPolicy); err != nil {
			t.Fatal("actual renewed native policy", err)
		}
		if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, runtime, role, []string{"pki-service-bootstrap"}, []string{name}); err != nil {
			t.Fatal("actual renewed exclusive native role", err)
		}
	} else {
		f.record(t, "openbao-policy-"+name, policyIntent, policyIntent)
		f.record(t, "openbao-role-"+role+"-"+deploymentplan.Digest(roleIntent)[:12], roleIntent, roleIntent)
	}
	f.acceptedHistoricalStage(t, "service-session-original-key-renewal-"+review, defs)
	reader := renderEnvironmentCreateCustodyReader(f.cfg, f.p.Plan.Bootstrap, s)
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(reader.Object)["containers"])[0])
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": r.RenewedCertPath}))
	f.acceptedHistoricalStage(t, "service-custody-reader-renewal-"+review, []deploymentplan.Definition{reader})
	var credentials []deploymentplan.Definition
	for _, ns := range s.BootstrapCredentialNamespaces {
		d := environmentCreateSecret(ns, s.BootstrapCredentialSecret, map[string]string{"tls.crt": f.certificate, "tls.key": f.key, "ca.crt": s.Root.Certificate})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.p.Plan.Stack, "rtk.cloud/bootstrap-session": f.session}
		credentials = append(credentials, d)
	}
	manifest, err := environmentCreateServiceManifest(s.Root, s.Intermediate)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range s.BootstrapCredentialNamespaces[1:] {
		credentials = append(credentials, f.originalVerifierDefinition(t, ns), environmentCreateServiceTrustDefinition(ns, s.Root, f.runtime.TransportCAPEM, manifest))
	}
	f.acceptedHistoricalStage(t, "service-credential-secrets-renewal-"+review, credentials)
	uids := map[string]string{}
	for _, ns := range s.BootstrapCredentialNamespaces {
		uids[ns] = deploymentplan.String(deploymentplan.Map(f.native.objects["Secret/"+ns+"/"+s.BootstrapCredentialSecret]["metadata"])["uid"])
	}
	s.CleanupBinding.SessionID = f.session
	s.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + f.session[:12]
	s.CleanupBinding.SecretUID = uids[s.BootstrapCredentialNamespace]
	s.CleanupBinding.PolicySHA256 = environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy))
	s.CredentialUIDs = uids
	f.service = s
	f.protocol["custody"].(map[string]string)[r.RenewedCertPath] = f.certificate
	native := f.protocol["session"].(map[string]any)
	native["session_id"], native["fingerprint"], native["expires"], native["status"] = f.session, environmentPreflightBytesSHA256(der), leaf.NotAfter, "active"
	native["installed"] = []string{}
	if !leaf.NotAfter.After(time.Now().UTC()) {
		native["status"] = "expired"
	}
	f.preserveHistoricalIssuedSession(t)
	if !nativePolicyEndpoint {
		f.protocol["role"].(map[string]any)["data"].(map[string]any)["token_policies"] = []string{name}
	}
	issued := f.protocol["historical_sessions"].(map[string]managedSessionState)[f.session]
	if issued.Session != f.session || issued.Issuer != s.Intermediate.ID || issued.Root != s.Root.RootFingerprint || issued.Fingerprint != environmentPreflightBytesSHA256(der) || !issued.Expires.Equal(leaf.NotAfter) {
		t.Fatal("historical renewal observation differs from its exact native session")
	}
	f.write(t, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json"), createServiceSessionObserved{Cleanup: s.CleanupBinding, UIDs: uids, Reader: s.CustodyReader, Session: mustProviderRecoveryJSON(issued)})
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
}

func newHistoricalCoreApplicationFailure(t *testing.T, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	return newHistoricalCoreApplicationFailureWithHooks(t, coreExecutionContractHooks{}, setup...)
}

func newHistoricalCoreApplicationFailureWithHooks(t *testing.T, hooks coreExecutionContractHooks, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	f := newCoreExecutionContractAtBoundaryWithHooks(t, true, true, false, hooks, setup...)
	grants, err := renderEnvironmentCreatePKIPhase(context.Background(), f.cfg, f.p.Plan.Bootstrap, "grants")
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "pki-grants", grants)
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	for i := range defs {
		defs[i].Dependencies = nil
		defs[i].Phase = "configuration"
		if defs[i].Object["kind"] == "Deployment" {
			defs[i].Phase = "migration"
		}
	}
	original, j := f.interruptedStage(t, "kubernetes/service-managed-core", defs, "Deployment/"+f.p.Plan.Stack+"-account-manager/account-manager")
	if !knownManagedCoreOrderFailure(original, j, f.p.Plan.Stack) {
		t.Fatal("missing authentic original managed Core ordering failure")
	}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true)
	if err != nil {
		t.Fatal("original Core partial source rejected", err)
	}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-managed-core-order-recovery/v1", p.Materials)
	f.publishHistoricalCoreRenewal(t, r, review, true)
	f.runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(f.cfg, f.runtime.InitialListeners)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	stage := "service-managed-core-order-recovery-" + review
	f.acceptedHistoricalStage(t, stage, defs)
	if err := f.e.RecordReceipt("kubernetes/service-managed-core", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": original.SHA256(), "renewed_session_id": f.session})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/service-managed-core", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	apps, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for _, d := range apps {
		if strings.HasPrefix(deploymentplan.ID(d.Object), "Job/"+f.p.Plan.Stack+"-video-cloud/pki-enroll-factoryenroll-client-") {
			failed = deploymentplan.ID(d.Object)
		}
	}
	app, j := f.interruptedStage(t, "kubernetes/application-client-enrollment", apps, failed)
	if !knownApplicationEnrollmentTrustFailure(app, j, f.p.Plan.Stack) {
		t.Fatal("missing authentic original application TLS refusal")
	}
	f.installFormalRepairPublication(t)
	objects := coreContractNativeInventory(f.native.objects)
	objects[failed]["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}
	f.protocol["objects"] = objects
	name := deploymentplan.String(deploymentplan.Map(objects[failed]["metadata"])["name"])
	f.protocol["logs"] = map[string]string{"job/" + name: "Service client enrollment refused: Post \"https://certissuer." + f.p.Plan.Stack + "-video-cloud.svc:18445/v1/enroll\": remote error: tls: bad certificate"}
	f.protocol["issued"] = []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
	f.publish(t)
	p = f.p
	p.Materials = map[string]string{}
	r, err = loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil {
		t.Fatal("full original second-renewal/application source rejected", err)
	}
	f.p.Materials = p.Materials
	return f, r
}

func TestHistoricalCoreApplicationExecutionContractLoadsCompleteSecondRenewalAncestry(t *testing.T) {
	f, r := newHistoricalCoreApplicationFailure(t)
	if !r.ApplicationForward || r.IdentityLockForward || r.Candidate == nil || r.GrantCandidate == nil || r.SourceRenewalStage == "kubernetes/service-session-original-key-renewal-"+f.review || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) {
		t.Fatal("full application source lost distinct second renewal or original identity")
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	if _, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true); err != nil {
		t.Fatal("independent full source reread rejected", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

// newHistoricalAcceptedCoreApplications preserves the three original same-key
// renewals and both actual interrupted Core/App cohorts. The final leaf lifetime
// is selected before its immutable Secret and observed records are produced.
func newHistoricalAcceptedCoreApplications(t *testing.T, expiredFinal bool, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, string) {
	t.Helper()
	return newHistoricalAcceptedCoreApplicationsWithHooks(t, expiredFinal, coreExecutionContractHooks{}, setup...)
}

func newHistoricalAcceptedCoreApplicationsWithHooks(t *testing.T, expiredFinal bool, hooks coreExecutionContractHooks, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, string) {
	t.Helper()
	f, renewal := newHistoricalCoreApplicationFailureWithHooks(t, hooks, setup...)
	originalApplication := renewal.PartialCore.SHA256()
	review, _ := f.admitRecovery(t, "rtk-cloud.create-application-trust-recovery/v1", f.p.Materials)
	f.acceptedHistoricalStage(t, "service-trust-grants-recovery-"+review, renewal.GrantDefinitions)
	f.publishHistoricalCoreRenewal(t, renewal, review, expiredFinal)
	core, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	account := "Deployment/" + f.p.Plan.Stack + "-account-manager/account-manager"
	for i := range core {
		d := &core[i]
		d.Dependencies = nil
		d.Phase = "configuration"
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = renewal.GrantImage.Reference
				}
			}
		}
		if deploymentplan.ID(d.Object) == account {
			d.Phase = "migration"
			spec := deploymentplan.Map(d.Object["spec"])
			spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate"}
			metadata := deploymentplan.Map(deploymentplan.Map(spec["template"])["metadata"])
			delete(deploymentplan.Map(metadata["annotations"]), "rtk.realtek.com/pki-identity-owner-mode")
		}
	}
	trustKey := "kubernetes/service-managed-core-trust-recovery-" + review
	pending, journal := f.interruptedStage(t, trustKey, core, account)
	if len(journal.Completed) != 14 || len(journal.Owners) != 15 {
		t.Fatal("third renewal did not retain the original identity-lock boundary")
	}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.protocol["logs"] = map[string]string{"account-manager-pod": "Service identity already owned: resource temporarily unavailable"}
	f.protocol["issued"] = []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
	f.publish(t)
	r := &createServiceSessionRecovery{Ceremony: f.service}
	if _, err := loadApplicationIdentityLockSource(f.store, f.e, r); err != nil {
		t.Fatal("original third-renewal identity history rejected", err)
	}
	p := f.p
	p.Materials = map[string]string{}
	if expiredFinal {
		// The historical rollout completed while this leaf was usable. Read
		// its immutable history at the later expired-session boundary without
		// changing any recorded certificate to make today's active check pass.
		if err := prepareApplicationIdentityLockRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
			t.Fatal("historical native identity owner rejected", err)
		}
	} else {
		r, err = loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
		if err != nil {
			t.Fatal("full third-renewal identity source rejected", err)
		}
	}
	var forward []deploymentplan.Definition
	for _, v := range r.Candidate.Resources {
		forward = append(forward, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	identityStage := "service-managed-core-identity-recovery-" + review
	f.acceptedHistoricalStage(t, identityStage, forward)
	if err := f.e.RecordReceipt(trustKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + identityStage, "original_failed_plan_sha256": pending.SHA256(), "renewed_session_id": f.session})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(trustKey, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	apps, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range apps {
		if d.Object["kind"] == "Job" && strings.Contains(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "factoryenroll-client-") {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			path := mqttFoundationEnv(c, "PKI_CLIENT_IDENTITY_STATE")
			if path != environmentCreateServiceState+"/private/client.json" {
				t.Fatal("original Factory state path changed")
			}
			c["command"] = []any{"/bin/sh", "-ec", "test -s " + path + " && grep -q '\"pending\"' " + path + " && exec /app/serviceidentity-bootstrap service:factory-enroll"}
		}
	}
	appStage := "application-client-enrollment-trust-recovery-" + review
	f.acceptedHistoricalStage(t, appStage, apps)
	if err := f.e.RecordReceipt("kubernetes/application-client-enrollment", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + appStage, "original_failed_plan_sha256": originalApplication, "renewed_session_id": f.session})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/application-client-enrollment", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return f, review
}

func TestHistoricalCoreApplicationExecutionContractLoadsThirdRenewalIntoMQTT(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "expired"}[expired], func(t *testing.T) {
			f, review := newHistoricalAcceptedCoreApplications(t, expired, historicalCompleteDomainPolicy(t))
			mqttRoot := f.trustAuthority(t, "mqtt", 31, false)
			f.installHistoricalAuthorityProtocol(t)
			mqtt := finishHistoricalPostServiceMQTT(t, f, review, &mqttRoot)
			f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
			if err != nil {
				t.Fatal("complete original Core/App/MQTT ancestry rejected", err)
			}
			if !r.PostServiceResume || r.PostServiceRenewal != expired || r.SourceRenewalStage != "kubernetes/service-session-original-key-renewal-"+review || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) || r.MQTTPendingPlan.SHA256() != mqtt.original.SHA256() || r.MQTTRoot.ID != mqttRoot.ID {
				t.Fatal("qualified downstream source lost original renewal/authority")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
