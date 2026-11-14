package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Establish historical transport evidence only in the isolated store. The
// current Mint still freshly authenticates the original native trust owner.
func recordServiceSessionNativeMintTransport(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, trust deploymentplan.Object, now time.Time) {
	t.Helper()
	parent := e.Journal.PlanSHA256
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	var source environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &source); err != nil {
		t.Fatal(err)
	}
	resource := activeTransportRenewalResource{ID: deploymentplan.ID(trust), UID: deploymentplan.String(deploymentplan.Map(trust["metadata"])["uid"]), Before: deploymentplan.Clone(trust), Desired: deploymentplan.Clone(trust)}
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: parent, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate), Resources: []activeTransportRenewalResource{resource}}
	artifact := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	review := deploymentplan.Digest(artifact)
	journalPath, err := filepath.Rel(store.Root, e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	proof := initialTransportSuccessorProof{Schema: "rtk-cloud.initial-transport-successor-proof/v1", Parent: parent, Review: review, CandidateSHA: plan.CandidateSHA, Journal: journalPath, Completed: true}
	key := "initial-transport-successor-" + review
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(initialTransportSuccessorIntent(proof)), now); err != nil {
		t.Fatal(err)
	}
	part := initialTransportSuccessorResourceIntent(resource)
	partKey := key + "-" + strings.ReplaceAll(resource.ID, "/", "-")
	if err := e.BeginEntry(partKey, "recovery", mustProviderRecoveryJSON(part), now); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt(partKey, mustProviderRecoveryJSON(part)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(partKey, now); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(key, now); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("deployment/create/transport-successor", review)
	for path, value := range map[string]any{filepath.Join(base, "qualification.json"): artifact, filepath.Join(base, "source-journal.json"): source, filepath.Join(base, "completed.json"): proof, filepath.Join("deployment/create/material", parent, "initial-transport-successor.json"): proof} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
}

// Extend the existing native Kubernetes process only at external protocols:
// registry reads, original custody reads, policy/role readback and Job wait.
// There is no replacement Mint callback or synthetic current certificate.
func installServiceSessionNativeMintProtocol(t *testing.T, dir string, cfg deploymentConfig, service environmentCreateServiceCeremony, states map[string]managedSessionState, material map[string][]byte) map[string][]byte {
	t.Helper()
	write := func(name string, value any) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), mustProviderRecoveryJSON(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("mint-states.json", states)
	// Keep key and CSR in immutable fixture files so both attempts read the same
	// custody instead of assigning synthetic credential effects in the test.
	files := map[string][]byte{}
	paths := map[string]string{}
	for path, raw := range material {
		file := filepath.Join(dir, "custody-"+environmentPreflightBytesSHA256([]byte(path)))
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		paths[path] = file
		files[file] = append([]byte(nil), raw...)
	}
	write("mint-custody.json", paths)
	write("mint-protocol.json", map[string]string{"environment": cfg.Environment, "issuer": service.Intermediate.ID, "namespace": service.BootstrapCredentialNamespace, "stack": cfg.Values["CLOUD_STACK_NAME"]})
	launcher := filepath.Join(dir, "kubectl")
	launch, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	launch = []byte(strings.Replace(string(launch), `"$@"`, `"$@" 2>> "$RTK_LATE_NATIVE_OBJECTS/mint-errors.log"`, 1))
	if err := os.WriteFile(launcher, launch, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "unified-kubectl.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	// The shared fixture passes a sentinel; generic stage reconstruction
	// freezes its known synthetic context. Accept only these test identities.
	script = strings.Replace(script, "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)", "    identity = f.read()\n    if identity != b'isolated-native-kubeconfig' and not (b'token: private-frozen-token\\n' in identity and b'server: https://selected.example:6443\\n' in identity and b'current-context: selected\\n' in identity): sys.exit(72)", 1)
	t.Cleanup(func() {
		if t.Failed() {
			if raw, err := os.ReadFile(filepath.Join(dir, "mint-errors.log")); err == nil {
				t.Logf("isolated native adapter: %s", raw)
			}
		}
	})
	script = strings.Replace(script, "    def fail(message):\n", "    def fail(message):\n        with open(os.path.join(root, 'mint-errors.log'), 'a') as f: f.write(message+'\\n')\n", 1)
	// The simulator returns canonical API UUIDs, which the native source owner
	// checks require. Keep this extension local to the Mint fixture.
	script = strings.Replace(script, "'fixture-' + uuid.uuid4().hex", "str(uuid.uuid4())", 1)
	marker := "    elif 'exec' in a:\n"
	protocol := `    elif 'exec' in a:
        protocol = load(os.path.join(root, 'mint-protocol.json'))
        if 'registry-read' in a:
            query = a[-1]
            if 'pki_service_client_issuances' in query:
                print('[]')
            else:
                ids = re.findall(r"session_id='([0-9a-f-]{36})'", query)
                states = load(os.path.join(root, 'mint-states.json'))
                if not ids or ids[-1] not in states: fail('unexpected fixture registry query')
                print(json.dumps(states[ids[-1]]))
        elif 'render-openbao-policy' in a:
            if a[-1] != protocol['issuer']: fail('foreign fixture policy issuer')
            print(json.dumps({'issuer_id':protocol['issuer'], 'environment':protocol['environment'], 'service_client_signer_policy':'isolated-policy'}))
        elif 'custody' in a and a[-2] == 'cat':
            files = load(os.path.join(root, 'mint-custody.json'))
            if a[-1] not in files: fail('unknown original fixture custody path')
            with open(files[a[-1]]) as f: sys.stdout.write(f.read())
        elif 'custody' in a and 'stat' in a:
            print('10001:10001')
        elif 'openbao' in a and 'bao policy' in a[-1]:
            if 'policy write' in a[-1]:
                data = sys.stdin.read().split('\n', 1)
                if len(data) != 2 or data[0] != 'isolated-root-token' or data[1] != 'isolated-policy': fail('foreign fixture policy')
            print('isolated-policy')
        elif 'openbao' in a and 'auth/kubernetes/role/pki-service-bootstrap-' in a[-1]:
            pattern = r'token_policies=([^ ]+)'
            saved = os.path.join(root, 'mint-role.json')
            if 'bao write' in a[-1]:
                matches = re.findall(pattern, a[-1])
                if len(matches) != 1: fail('unknown fixture auth binding')
                save(saved, {'data': {'bound_service_account_names':['pki-service-bootstrap'], 'bound_service_account_namespaces':[protocol['namespace']], 'token_policies':matches[0].split(','), 'audience':'openbao', 'token_no_default_policy':True, 'token_ttl':900, 'token_max_ttl':900}})
            role = load(saved)
            if role is None: fail('missing original fixture role')
            print(json.dumps(role))
        else: fail('unexpected Mint exec protocol')
`
	if strings.Count(script, marker) != 1 {
		t.Fatal("native exec protocol boundary changed")
	}
	start := strings.Index(script, marker)
	end := strings.Index(script[start:], "    elif ('rollout'") + start
	if end < start {
		t.Fatal("native wait protocol boundary changed")
	}
	script = script[:start] + protocol + script[end:]
	waitMarker := "    elif ('rollout' in a and 'status' in a) or 'wait' in a:\n"
	script = strings.Replace(script, waitMarker, waitMarker+"        if 'wait' in a and any(x.startswith('Job/') for x in a):\n            with open(os.environ['RTK_LATE_NATIVE_CALLS'], 'a') as f: f.write('fixture native Job wait interrupted\\n')\n            fail('fixture native Job wait interruption')\n", 1)
	writeMarker := "        if '--dry-run=server' not in a: save(file, obj)\n"
	writeLog := "        if '--dry-run=server' not in a:\n            save(file, obj)\n            if kind == 'Job':\n                with open(os.environ['RTK_LATE_NATIVE_CALLS'], 'a') as f: f.write('fixture native Job created\\n')\n"
	if strings.Count(script, writeMarker) != 1 {
		t.Fatal("native write protocol boundary changed")
	}
	script = strings.Replace(script, writeMarker, writeLog, 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return files
}
