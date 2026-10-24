package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const historicalRealTimeExpiryEnv = "RTK_HISTORICAL_REALTIME_EXPIRY"

func historicalExpiryDuration(realtime, quick time.Duration) time.Duration {
	if os.Getenv(historicalRealTimeExpiryEnv) == "1" {
		return realtime
	}
	return quick
}

func TestHistoricalExpiryDurationKeepsLongWaitsOnlyWhenExplicit(t *testing.T) {
	t.Setenv(historicalRealTimeExpiryEnv, "")
	if got := historicalExpiryDuration(45*time.Second, 2*time.Second); got != 2*time.Second {
		t.Fatal("PR fixture did not select the short genuine expiry", got)
	}
	t.Setenv(historicalRealTimeExpiryEnv, "1")
	if got := historicalExpiryDuration(45*time.Second, 2*time.Second); got != 45*time.Second {
		t.Fatal("nightly fixture did not retain the real-time expiry", got)
	}
}

// The original seed is issued by its real native Job boundary. Its lifetime is
// selected before signing; expiry is observed by elapsed wall time, never by
// editing a certificate, receipt, or recorded native row.
func historicalOriginalSessionLeaf(t *testing.T, f *acceptedCoreExecutionContract, session string, lifetime time.Duration) (string, managedSessionState) {
	t.Helper()
	leaf, err := parseFirstCertificatePEM([]byte(f.certificate))
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
	leaf.NotBefore, leaf.NotAfter = time.Now().UTC().Add(-time.Second), time.Now().UTC().Add(lifetime)
	der, err := x509.CreateCertificate(rand.Reader, leaf, issuer, leaf.PublicKey, f.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := managedSessionState{Session: session, Issuer: f.service.Intermediate.ID, Root: f.service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(der), Status: "active", Expires: leaf.NotAfter, Subjects: subjects, Installed: []string{}}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + f.service.Intermediate.ChainPEM, state
}

func historicalWaitForOriginalSessionExpiry(t *testing.T, state managedSessionState) {
	t.Helper()
	t.Logf("waiting for actual original session expiry: remaining=%s", time.Until(state.Expires).Round(time.Second))
	for state.Expires.After(time.Now().UTC()) {
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Second):
		}
	}
}

// This opt-in boundary includes a normal first session and the genuine original
// serving-enrollment interruption before the first same-key renewal. The whole
// source reader qualifies its actual candidate and material pins. Admission is
// the explicit isolated historical stage contract, not public six-gate GO.
func (f *acceptedCoreExecutionContract) completeOriginalSessionPrefix(t *testing.T, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, hooks coreExecutionContractHooks, manifest []byte, expired bool) *acceptedCoreExecutionContract {
	t.Helper()
	p := f.p.Plan.Bootstrap
	kubeCA := base64.StdEncoding.EncodeToString([]byte(runtime.TransportCAPEM))
	f.backend = environmentKubernetesBackend{Store: f.store, KubeconfigContent: []byte("clusters:\n- cluster:\n    certificate-authority-data: " + kubeCA + "\n")}
	// Recovery readers authenticate the selected cluster from the canonical
	// environment file. Keep this fixture's durable source and backend handle
	// identical before any original session source is captured.
	if err := f.store.write("kube/kubeconfig.yaml", f.backend.KubeconfigContent, true); err != nil {
		t.Fatal("persist selected fixture kubeconfig", err)
	}
	if f.cfg.Values[deploymentRolloutTimeoutKey] != "" {
		var err error
		f.backend.RolloutTimeout, err = environmentRolloutTimeout(f.cfg.Values)
		if err != nil {
			t.Fatal(err)
		}
	}
	f.service, f.runtime = service, runtime
	runtime.Execution, runtime.ExecutionSHA256, runtime.Backend = f.e, f.e.Journal.PlanSHA256, f.backend
	runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
	if raw, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true); err != nil || strings.TrimSpace(string(raw)) == "" {
		t.Fatal("original initialized OpenBao token required", err)
	}
	f.protocol = map[string]any{"objects": historicalNativeInventory(f.native.objects), "operator": f.operator["PKI_OPERATOR_USER_ID"], "email": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], "password": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"], "issuers": map[string]any{service.Root.ID: service.Root.Document, service.Intermediate.ID: service.Intermediate.Document}, "policy": environmentCreateOpenBaoIssuerPolicies{Environment: f.cfg.Environment, IssuerID: service.Intermediate.ID, ServiceClientSignerPolicy: "path \"pki/isolated/sign/service-client\" { capabilities = [\"update\"] }\n"}, "role": map[string]any{}, "custody": map[string]string{}, "historical_sessions": map[string]managedSessionState{}}
	if hooks.ImageManifests != nil {
		f.protocol["image_manifests"] = hooks.ImageManifests
	}
	f.installProtocol(t, manifest)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
	if f.native.objects["Secret/"+p.Stack+"-video-cloud/pki-verifier-database"] == nil {
		f.acceptedHistoricalStage(t, "original-primary-verifier", []deploymentplan.Definition{f.originalVerifierDefinition(t, p.Stack+"-video-cloud")})
	}
	active, err := renderEnvironmentCreateFirstTrustDefinitions(t.Context(), f.cfg, p, runtime, service.Root, service.Intermediate)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "service-active-listeners", active)
	apply := historicalTransportSessionApply(t, f, &createServiceSessionRecovery{}, "", managedSessionState{})
	var originalState managedSessionState
	var originalCertificate string
	originalApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "service-session" {
			var intent createServiceSessionIntent
			if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-intent.json"), &intent); err != nil {
				return err
			}
			originalCertificate, originalState = historicalOriginalSessionLeaf(t, f, intent.SessionID, historicalExpiryDuration(45*time.Second, 2*time.Second))
			f.protocol["historical_sessions"].(map[string]managedSessionState)[originalState.Session] = originalState
			recordHistoricalBootstrapIssuance(t, f, originalState, originalCertificate)
			f.protocol["custody"] = map[string]string{"/run/pki-bootstrap/bootstrap.key": f.key, "/run/pki-bootstrap/bootstrap.csr": f.csr, "/run/pki-bootstrap/bootstrap.crt": originalCertificate}
			f.publish(t)
		}
		return nil
	}
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	original, err := createEnvironmentServiceSession(t.Context(), f.cfg, p, f.store, runtime, service, subjects, originalApply)
	if err != nil {
		t.Fatal("actual original session producer", err)
	}
	f.service, f.session, f.certificate = original, original.BootstrapSessionID, originalCertificate
	base := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service")
	originalIntentBytes, err := os.ReadFile(filepath.Join(base, "session-intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	originalObservedBytes, err := os.ReadFile(filepath.Join(base, "session-observed.json"))
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := renderEnvironmentCreateServiceIssuerDefinitions(t.Context(), f.cfg, p, runtime, original)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "service-enrollment-issuer", enrollment)
	servers, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(t.Context(), f.cfg, p, runtime, original)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for i := range servers {
		servers[i].Dependencies = nil
		servers[i].Phase = "configuration"
		if servers[i].Object["kind"] == "Job" {
			servers[i].Phase, failed = "bootstrap", deploymentplan.ID(servers[i].Object)
		}
	}
	stopped, journal := f.historicalSerialInterruptedStage(t, "kubernetes/service-issuer-server-enrollment", servers, failed)
	if stopped == nil || journal.Failed != failed || len(journal.Completed) != 0 {
		t.Fatal("original enrollment interruption missing")
	}
	f.native.objects[failed]["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	historicalWaitForOriginalSessionExpiry(t, originalState)
	if hooks.AfterOriginalSessionExpiry != nil {
		hooks.AfterOriginalSessionExpiry(f)
	}
	qualification := f.p
	qualification.Materials = map[string]string{}
	r, err := loadServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &qualification, true)
	if err != nil {
		t.Fatal("actual original expired-session source qualification", err)
	}
	if r.Original.SHA256() != stopped.SHA256() || r.Intent.SessionID != original.BootstrapSessionID || r.Candidate == nil || len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) || r.OriginalCSRSHA != environmentPreflightBytesSHA256([]byte(f.csr)) {
		t.Fatal("qualified original renewal lost its real interruption or custody")
	}
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-original-session-execution-contract/v1", qualification.Materials)
	f.review = r.Review
	lifetime := 3 * time.Hour
	if expired {
		lifetime = historicalExpiryDuration(45*time.Second, 2*time.Second)
	}
	certificate, state := historicalOriginalSessionLeaf(t, f, r.RenewedIntent.SessionID, lifetime)
	runtime.ServiceSessionRecovery = r
	current, err := createEnvironmentServiceSession(t.Context(), f.cfg, p, f.store, runtime, original, subjects, historicalTransportSessionApply(t, f, r, certificate, state))
	if err != nil {
		t.Fatal("actual first same-key renewed session producer", err)
	}
	f.service, f.session, f.certificate = current, current.BootstrapSessionID, certificate
	for _, stage := range []struct {
		name   string
		render func(context.Context, deploymentConfig, environmentCreateBootstrapPlan, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony) ([]deploymentplan.Definition, error)
	}{{"service-enrollment-issuer", renderEnvironmentCreateServiceIssuerDefinitions}, {"service-issuer-server-enrollment", renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions}, {"service-managed-enrollment-issuer", renderEnvironmentCreateManagedServiceIssuerDefinitions}, {"service-core-enrollment", renderEnvironmentCreateServiceEnrollmentDefinitions}} {
		defs, err := stage.render(t.Context(), f.cfg, p, runtime, current)
		if err != nil {
			t.Fatal(err)
		}
		name := stage.name
		if name == "service-enrollment-issuer" || name == "service-issuer-server-enrollment" {
			name += "-renewal-" + r.Review
		}
		f.acceptedHistoricalStage(t, name, defs)
		if stage.name == "service-issuer-server-enrollment" {
			if err := f.e.RecordReceipt("kubernetes/service-issuer-server-enrollment", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + name, "original_failed_plan_sha256": stopped.SHA256(), "renewed_session_id": current.BootstrapSessionID})); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("kubernetes/service-issuer-server-enrollment", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(base, "session-intent.json")); err != nil || string(raw) != string(originalIntentBytes) {
		t.Fatal("same-key renewal changed original session intent", err)
	}
	if raw, err := os.ReadFile(filepath.Join(base, "session-observed.json")); err != nil || string(raw) != string(originalObservedBytes) {
		t.Fatal("same-key renewal changed original session observation", err)
	}
	for ns, uid := range original.CredentialUIDs {
		object := f.native.objects["Secret/"+ns+"/"+original.BootstrapCredentialSecret]
		if object == nil || deploymentplan.Map(object["metadata"])["uid"] != uid {
			t.Fatal("original session credential owner changed", ns)
		}
	}
	if expired {
		historicalWaitForOriginalSessionExpiry(t, state)
	}
	f.protocol["session"] = map[string]any{"session_id": state.Session, "issuer_id": state.Issuer, "root": state.Root, "fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "installed": state.Installed, "expires": state.Expires, "other_active": state.OtherActive}
	f.preserveHistoricalIssuedSession(t)
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.runtime = runtime
	f.runtime.ServiceSessionRecovery = nil
	f.publish(t)
	assertHistoricalOriginalSessionCustodyAdmission(t, f)
	return f
}

func TestHistoricalOriginalSessionProducerRetainsGenuineFirstIssuance(t *testing.T) {
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	hooks.OriginalServiceSessionLifecycle = true
	hooks.AfterOriginalSessionExpiry = func(f *acceptedCoreExecutionContract) {
		before := createBootstrapStateSnapshot(t, f.store.Root)
		writes := deploymentplan.Digest(f.native.writes)
		p := f.p
		p.Materials = map[string]string{}
		_, err := loadServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true)
		if err != nil {
			t.Fatal("genuine expired original normal-session source must qualify without an earlier ordering failure", err)
		}
		assertRecoveryContractReadOnly(t, f.store, before)
		if deploymentplan.Digest(f.native.writes) != writes {
			t.Fatal("original session qualification wrote native owners")
		}
		assertHistoricalOriginalSessionSourceRefusals(t, f)
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
	rows := f.protocol["historical_sessions"].(map[string]managedSessionState)
	if len(rows) != 2 {
		t.Fatal("actual original and first renewed native rows required")
	}
	var original createServiceSessionObserved
	base := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service")
	if err := readExecutionJSON(filepath.Join(base, "session-observed.json"), &original); err != nil || original.Cleanup.SessionID == f.session || len(original.UIDs) != 3 {
		t.Fatal("actual original completion unavailable", err)
	}
	if row := rows[original.Cleanup.SessionID]; row.Session == "" || row.Expires.After(time.Now().UTC()) || row.Fingerprint == rows[f.session].Fingerprint {
		t.Fatal("original native issuance was replaced or did not genuinely expire")
	}
	p := f.p
	p.Materials = map[string]string{}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if _, err := loadAcceptedServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p); err != nil {
		t.Fatal("full accepted Core from genuine original session", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestHistoricalOriginalSessionContinuesThroughThirdRenewalAndMQTT(t *testing.T) {
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	hooks.OriginalServiceSessionLifecycle = true
	hooks.OriginalServiceCRLLifetime = 30 * time.Minute
	hooks.OriginalServiceCALifetime = 7 * 24 * time.Hour
	f, review := newHistoricalAcceptedCoreApplicationsWithHooks(t, true, hooks, historicalCompleteDomainPolicy(t))
	mqttRoot := f.trustAuthority(t, "mqtt", 31, false)
	f.installHistoricalAuthorityProtocol(t)
	mqtt := finishHistoricalPostServiceMQTT(t, f, review, &mqttRoot)
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	var original createServiceSessionObserved
	path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-observed.json")
	if err := readExecutionJSON(path, &original); err != nil {
		t.Fatal(err)
	}
	rows := f.protocol["historical_sessions"].(map[string]managedSessionState)
	if len(rows) != 4 || rows[original.Cleanup.SessionID].Session == "" || !rows[original.Cleanup.SessionID].Expires.Before(time.Now()) {
		t.Fatal("original issuance or one of three actual renewal rows was lost")
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil {
		t.Fatal("complete original issuance/Core/App/MQTT ancestry rejected", err)
	}
	if !r.PostServiceResume || !r.PostServiceRenewal || r.SourceRenewalStage != "kubernetes/service-session-original-key-renewal-"+review || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) || r.MQTTPendingPlan.SHA256() != mqtt.original.SHA256() || r.Intent.SessionID != original.Cleanup.SessionID {
		t.Fatal("downstream source lost the authentic first-session lineage")
	}
	firstRenewed := assertHistoricalOriginalSessionCustodyAdmission(t, f)
	query := "SELECT COALESCE(json_agg(subject ORDER BY subject),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + f.cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	raw, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, f.runtime, query)
	var issued []string
	if err != nil || json.Unmarshal(raw, &issued) != nil || len(issued) == 0 {
		t.Fatal("actual current native client observation", err)
	}
	proof := managedTransportPreservedClients{Original: firstRenewed, Renewed: f.session, Key: environmentPreflightBytesSHA256([]byte(f.key)), CSR: environmentPreflightBytesSHA256([]byte(f.csr)), Clients: deploymentplan.Digest(issued)}
	for _, mode := range []string{"original", "changed-key", "changed-csr", "same-session", "foreign-renewed-session", "missing-clients-proof"} {
		t.Run(mode, func(t *testing.T) {
			candidate := proof
			switch mode {
			case "changed-key":
				candidate.Key = strings.Repeat("e", 64)
			case "changed-csr":
				candidate.CSR = strings.Repeat("e", 64)
			case "same-session":
				candidate.Original = f.session
			case "foreign-renewed-session":
				candidate.Renewed = "99999999-1111-4111-8111-111111111111"
			case "missing-clients-proof":
				candidate.Clients = ""
			}
			materials := map[string]string{}
			pins, err := loadManagedTransportOriginalCustodyPins(f.store, f.e, candidate, f.session, materials)
			if (err == nil) != (mode == "original") {
				t.Fatal("actual first admission custody boundary", mode, err)
			}
			if err == nil && (pins["bootstrap.key"] != proof.Key || pins["bootstrap.csr"] != proof.CSR || len(materials) != 2) {
				t.Fatal("original custody authentication lost its exact evidence")
			}
		})
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

// Read only the original real interruption and its first admitted renewal.
// Later custody readers must authenticate these bytes, never infer them from
// a new session's certificate or append material to an already captured source.
func assertHistoricalOriginalSessionCustodyAdmission(t *testing.T, f *acceptedCoreExecutionContract) string {
	t.Helper()
	var alias struct {
		Stage    string `json:"actual_qualified_forward_stage"`
		Original string `json:"original_failed_plan_sha256"`
		Session  string `json:"renewed_session_id"`
	}
	for _, entry := range f.e.Journal.Entries {
		if entry.Key == "kubernetes/service-issuer-server-enrollment" {
			if json.Unmarshal(entry.Receipt, &alias) != nil {
				t.Fatal("original serving-enrollment alias")
			}
		}
	}
	prefix := "kubernetes/service-issuer-server-enrollment-renewal-"
	if !strings.HasPrefix(alias.Stage, prefix) {
		t.Fatal("actual first renewal alias required")
	}
	review := strings.TrimPrefix(alias.Stage, prefix)
	artifact, source, err := loadAdmittedCreateRecovery(f.store, f.e, review)
	if err != nil || artifact.Plan.Materials["original-service-session-plan"] != alias.Original || artifact.Plan.Materials["original-service-session-custody-bootstrap.key"] != environmentPreflightBytesSHA256([]byte(f.key)) || artifact.Plan.Materials["original-service-session-custody-bootstrap.csr"] != environmentPreflightBytesSHA256([]byte(f.csr)) {
		t.Fatal("first immutable admission lost original failed child or same-key custody", err)
	}
	var original deploymentplan.Plan
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", alias.Original, "plan.json"), &original); err != nil || original.SHA256() != alias.Original {
		t.Fatal("actual original failed child changed", err)
	}
	intent, err := environmentCreateKubernetesStageIntent(&original)
	if err != nil {
		t.Fatal(err)
	}
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	last := source.Entries[len(source.Entries)-1]
	if last.Key != "kubernetes/service-issuer-server-enrollment" || last.State != "pending" || len(last.Receipt) != 0 || last.IntentSHA256 != intentSHA {
		t.Fatal("admitted source does not contain the genuine unresolved original serving child")
	}
	return alias.Session
}
