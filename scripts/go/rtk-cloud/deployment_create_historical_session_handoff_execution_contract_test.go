package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalSessionProtocolPreservesOriginalNativePolicyAndExclusiveRole(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	original := &createServiceSessionRecovery{Ceremony: f.service, Intent: createServiceSessionIntent{SessionID: f.session}}
	snapshot := createBootstrapStateSnapshot(t, f.store.Root)
	states, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
	if !ok || states[f.session].Session != f.session {
		t.Fatal("original issued native session is unavailable")
	}
	originalRows := deploymentplan.Digest(states)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{f.session: states[f.session]})
	if deploymentplan.Digest(f.protocol["historical_sessions"]) != originalRows {
		t.Fatal("native protocol handoff lost an originally issued row")
	}
	runtime := f.runtime
	runtime.Backend, runtime.Execution = f.backend, f.e
	read, _, err := readManagedSessionState(t.Context(), f.cfg, runtime, f.session)
	if err != nil || read.OtherActive != 0 || read.Fingerprint != states[f.session].Fingerprint {
		t.Fatal("original native session projection differs", err)
	}
	// A conflicting native row is adversarial input, not accepted history. The
	// real reader must see its contribution to the SQL competing-session count.
	foreign := states[f.session]
	foreign.Session = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	states[foreign.Session] = foreign
	f.publish(t)
	read, _, err = readManagedSessionState(t.Context(), f.cfg, runtime, f.session)
	delete(states, foreign.Session)
	f.publish(t)
	if err != nil || read.OtherActive != 1 || read.Fingerprint != states[f.session].Fingerprint {
		t.Fatal("native projection concealed a competing active row", err)
	}
	if deploymentplan.Digest(states) != originalRows {
		t.Fatal("native session query mutated original registry rows")
	}
	if err := verifyServiceSessionAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, original); err != nil {
		t.Fatal(err)
	}
	assertRecoveryContractReadOnly(t, f.store, snapshot)
	path := os.Getenv("RTK_HISTORICAL_SESSION_BAO")
	var native map[string]any
	if err := readExecutionJSON(path, &native); err != nil {
		t.Fatal(err)
	}
	baseline := mustProviderRecoveryJSON(native)
	for _, mode := range []string{"policy", "role"} {
		if err := decodeEnvironmentJSON(baseline, &native); err != nil {
			t.Fatal(err)
		}
		if mode == "policy" {
			name := "rtk-" + f.cfg.Environment + "-service-bootstrap-" + f.session
			native["policies"].(map[string]any)[name] = "foreign native policy"
		} else {
			role := native["roles"].(map[string]any)["pki-service-bootstrap-"+f.cfg.Environment].(map[string]any)
			role["token_policies"] = []any{"foreign-policy"}
		}
		if err := os.WriteFile(path, mustProviderRecoveryJSON(native), 0600); err != nil {
			t.Fatal(err)
		}
		if err := verifyServiceSessionAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, original); err == nil {
			t.Fatal("changed original native authentication accepted", mode)
		}
		assertRecoveryContractReadOnly(t, f.store, snapshot)
	}
	if err := os.WriteFile(path, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	if err := readExecutionJSON(path, &native); err != nil || native["policy_writes"] != float64(0) || native["role_writes"] != float64(0) {
		t.Fatal("handoff republished original native authentication", err, deploymentplan.Digest(native))
	}
}

func TestHistoricalSessionProtocolReinstallationPreservesNativeStateAndDelegation(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	rows := f.protocol["historical_sessions"].(map[string]managedSessionState)
	selected := map[string]managedSessionState{f.session: rows[f.session]}
	installHistoricalTransportSessionProtocol(t, f, selected)
	path := os.Getenv("RTK_HISTORICAL_SESSION_BAO")
	rootToken := strings.TrimSpace(readTestFile(t, filepath.Join(f.store.Root, "openbao/root-token")))
	const name = "isolated-native-handoff-policy"
	const policy = "path \"isolated-original-only\" { capabilities = [\"read\"] }\n"
	command := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy write " + name + " -\nbao policy read " + name + "\n"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	raw, err := environmentCreateOpenBaoExec(ctx, f.cfg, f.backend, []byte(rootToken+"\n"+policy), command)
	if err != nil || string(raw) != policy {
		t.Fatal("actual native policy publication", err)
	}
	originalState := readTestFile(t, path)
	originalRows := deploymentplan.Digest(rows)
	originalPrivate := createBootstrapStateSnapshot(t, f.store.Root)
	objectID := "Secret/" + f.service.BootstrapCredentialNamespace + "/pki-verifier-database"
	expected := f.native.objects[objectID]
	if expected == nil {
		for id, object := range f.native.objects {
			objectID, expected = id, object
			break
		}
	}
	if expected == nil {
		t.Fatal("no genuine original native object to reobserve")
	}
	for i := 0; i < 2; i++ {
		installHistoricalTransportSessionProtocol(t, f, selected)
		if os.Getenv("RTK_HISTORICAL_SESSION_BAO") != path || readTestFile(t, path) != originalState || deploymentplan.Digest(f.protocol["historical_sessions"]) != originalRows {
			t.Fatal("reinstallation erased native writes, old policies or issued rows")
		}
		raw, err = environmentCreateOpenBaoExec(ctx, f.cfg, f.backend, []byte(rootToken+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+name+"\n")
		if err != nil || string(raw) != policy {
			t.Fatal("reinstallation lost actual earlier policy", err)
		}
		observed, err := f.backend.Read(ctx, objectID)
		if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(expected) {
			t.Fatal("immutable predecessor delegation failed or changed original native object", err)
		}
		assertRecoveryContractReadOnly(t, f.store, originalPrivate)
	}
	var state map[string]any
	if err := readExecutionJSON(path, &state); err != nil || state["policy_writes"] != float64(1) || state["role_writes"] != float64(0) {
		t.Fatal("reinstallation reset or replayed native mutation counters", err)
	}
}

func TestHistoricalSessionProtocolOriginalProjectionUsesActualIssuanceTables(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	states := f.protocol["historical_sessions"].(map[string]managedSessionState)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{f.session: states[f.session]})
	runtime := f.runtime
	runtime.Backend, runtime.Execution = f.backend, f.e
	query := "SELECT json_build_object('count',(SELECT count(*) FROM pki_deployment_bootstrap_sessions),'servers',(SELECT count(*) FROM pki_server_issuances),'clients',(SELECT count(*) FROM pki_service_client_issuances),'session_id',session_id,'issuer_id',issuer_id,'root',root_sha256,'caller',caller,'fingerprint',bootstrap_fingerprint,'subjects',subjects,'installed',installed_subjects,'status',status,'expires',expires_at) FROM pki_deployment_bootstrap_sessions WHERE session_id='" + f.session + "'"
	read := func() map[string]any {
		t.Helper()
		raw, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, runtime, query)
		var out map[string]any
		if err != nil || decodeEnvironmentJSON(raw, &out) != nil {
			t.Fatal("actual initial-session SQL projection", err)
		}
		return out
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	nativeBefore := deploymentplan.Digest(f.protocol)
	initial := read()
	state := states[f.session]
	if initial["count"] != float64(1) || initial["servers"] != float64(0) || initial["clients"] != float64(1) || initial["caller"] != "service:deployment-bootstrap" || initial["session_id"] != f.session || initial["issuer_id"] != state.Issuer || initial["root"] != state.Root || initial["fingerprint"] != state.Fingerprint || initial["status"] != "active" || initial["other_active"] != nil || initial["root_sha256"] != nil {
		t.Fatal("initial SQL aliases or actual issuance counts differ", initial)
	}
	if deploymentplan.Digest(initial["subjects"]) != deploymentplan.Digest(state.Subjects) || deploymentplan.Digest(initial["installed"]) != deploymentplan.Digest(state.Installed) {
		t.Fatal("initial SQL projection lost native subject/install evidence")
	}
	var expires time.Time
	if err := expires.UnmarshalText([]byte(initial["expires"].(string))); err != nil || !expires.Equal(state.Expires) {
		t.Fatal("initial SQL projection changed actual DER expiry", err)
	}
	if deploymentplan.Digest(f.protocol) != nativeBefore {
		t.Fatal("readonly original-session query mutated native tables")
	}
	assertRecoveryContractReadOnly(t, f.store, before)

	// These adversarial native rows are deliberately not accepted deployment
	// history. A query must expose their real counts so the whole source guard
	// rejects a no-longer-exclusive first issuance.
	foreign := state
	foreign.Session = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	states[foreign.Session] = foreign
	clientCert, _, clientCSR := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, "service:account-manager", nil)
	serverCert, _, serverCSR := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, "isolated.invalid", []string{"isolated.invalid"})
	f.protocol["historical_clients"] = []managedSessionClient{{Subject: "service:account-manager", Caller: "service:account-manager", Request: "adversarial-additional-client", Issuer: state.Issuer, Certificate: clientCert, CSR: clientCSR}}
	f.protocol["historical_servers"] = []managedTransportServerEvidence{{Caller: "service:certissuer", Request: "adversarial-additional-server", Issuer: state.Issuer, Certificate: serverCert, CSR: serverCSR}}
	f.publish(t)
	counts := read()
	if counts["count"] != float64(2) || counts["clients"] != float64(2) || counts["servers"] != float64(1) {
		t.Fatal("projection hid extra native rows or counted an unissued session as a client", counts)
	}
	issuances := f.protocol["historical_bootstrap_issuances"].(map[string]historicalBootstrapIssuance)
	original := issuances[f.session]
	delete(issuances, f.session)
	f.publish(t)
	if _, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, runtime, query); err == nil {
		t.Fatal("missing original issuance silently fabricated the seed caller")
	}
	issuances[f.session] = original
	f.publish(t)
	if _, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, runtime, query+" AND status='active'"); err == nil {
		t.Fatal("unsupported filtered count query accepted")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}
