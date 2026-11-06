package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreatePKICeremonyUsesAuthenticatedOperatorAndExactRegistryDigests(t *testing.T) {
	const user = "11111111-1111-4111-8111-111111111111"
	const operation = "22222222-2222-4222-8222-222222222222"
	const issuer = "33333333-3333-4333-8333-333333333333"
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			var calls []string
			request := environmentCreateIssuerRequest{Environment: environment, Domain: "service", Kind: "root"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/v1/auth/login" {
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["email"] != "operator@example.test" || body["password"] != "private-password" || r.Header.Get("Authorization") != "" {
						t.Error("login did not use genuine password inputs")
					}
					fmt.Fprintf(w, `{"user":{"id":%q,"email":"operator@example.test"},"tokens":{"access_token":"private-real-user-session"}}`, user)
					return
				}
				if r.Header.Get("Authorization") != "Bearer private-real-user-session" {
					t.Error("issuer request bypassed the authenticated user session")
				}
				switch r.URL.Path {
				case "/v1/platform/pki/operations":
					var actual environmentCreateIssuerRequest
					if json.NewDecoder(r.Body).Decode(&actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(request) || r.Header.Get("Idempotency-Key") != "reviewed-parent-service-root" {
						t.Error("issuer request drifted")
					}
					fmt.Fprintf(w, `{"operation_id":%q,"issuer_id":%q,"request_sha256":%q,"status":"requested"}`, operation, issuer, deploymentplan.Digest(request))
				case "/v1/platform/pki/issuers/" + issuer:
					json.NewEncoder(w).Encode(map[string]string{"issuer_id": issuer, "environment": environment, "status": "approved"})
				case "/v1/platform/pki/operations/" + operation + "/authorize":
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["request_sha256"] != deploymentplan.Digest(request) {
						t.Error("authorization did not bind the exact policy")
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), environment, user)
			if err != nil {
				t.Fatal(err)
			}
			if actual, err := client.Authenticate(context.Background(), "operator@example.test", "private-password"); err != nil || actual != user {
				t.Fatal(actual, err)
			}
			op, err := client.ReserveIssuer(context.Background(), "reviewed-parent-service-root", request)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.AuthorizeIssuer(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 4 {
				t.Fatal("unexpected mutation or reauthentication", calls)
			}
		})
	}
}

func TestCreatePKIAuthorizationRetriesTransientReadWithoutRepeatingWrite(t *testing.T) {
	const issuer = "33333333-3333-4333-8333-333333333333"
	const operation = "22222222-2222-4222-8222-222222222222"
	var writes, reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/platform/pki/operations/"+operation+"/authorize":
			writes++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/platform/pki/issuers/"+issuer:
			reads++
			if reads == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer_id": issuer, "environment": "staging", "status": "approved"})
		default:
			t.Error("unexpected registry request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "staging", issuer)
	if err != nil {
		t.Fatal(err)
	}
	client.token = "isolated-authenticated-operator"
	op := environmentCreateIssuerOperation{ID: operation, IssuerID: issuer, Digest: strings.Repeat("a", 64)}
	if err := client.AuthorizeIssuer(t.Context(), op); err != nil || writes != 1 || reads != 2 {
		t.Fatalf("authorization should reconcile with one write and two reads: writes=%d reads=%d err=%v", writes, reads, err)
	}
}

func TestStoppedServiceAuthorizationRequiresExactOriginalPendingIntent(t *testing.T) {
	request := environmentCreateIssuerRequest{
		Environment: "staging", Domain: "service", Kind: "intermediate",
		ParentID: "11111111-1111-4111-8111-111111111111", ServiceClientIDs: []string{"service:deployment-bootstrap"},
	}
	operation := environmentCreateIssuerOperation{
		ID: "22222222-2222-4222-8222-222222222222", IssuerID: "33333333-3333-4333-8333-333333333333",
		Digest: deploymentplan.Digest(request), Status: "requested",
	}
	reserveSHA, err := environmentCreateIssuerIntentSHA(request)
	if err != nil {
		t.Fatal(err)
	}
	authSHA, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(operation))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, mode := range []string{"valid", "wrong-intent", "unknown-write", "foreign", "additional-write", "earlier-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			j := environmentCreateJournal{Environment: "staging", Stack: "video-cloud-staging", Entries: []environmentCreateJournalEntry{
				{Key: "pki-initial-root-crl-service", State: "completed", CompletedAt: now},
				{Key: "pki-reserve-service-intermediate", State: "completed", CompletedAt: now, IntentSHA256: reserveSHA, Receipt: mustProviderRecoveryJSON(operation)},
				{Key: "pki-authorize-" + operation.ID, Phase: "bootstrap", State: "pending", IntentSHA256: authSHA, StartedAt: now},
			}}
			switch mode {
			case "wrong-intent":
				j.Entries[2].IntentSHA256 = strings.Repeat("f", 64)
			case "unknown-write":
				j.Entries[2].Receipt = json.RawMessage(`{"unknown":"write"}`)
			case "foreign":
				j.Environment = "dev"
			case "additional-write":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "later-write", State: "completed", CompletedAt: now})
			case "earlier-incomplete":
				j.Entries[0].State = "pending"
			}
			got, err := stoppedServiceAuthorization(j, request)
			if mode == "valid" {
				if err != nil || got != operation {
					t.Fatal("lost original authorization", err)
				}
			} else if err == nil {
				t.Fatal("unverified original authorization admitted")
			}
		})
	}
}

func TestServiceAuthorizationRecoveryOverridesOnlyControllerListeners(t *testing.T) {
	stack := "video-cloud-staging"
	makeDeployment := func(namespace, name, executable string) deploymentplan.Object {
		return deploymentplan.Object{
			"kind": "Deployment", "metadata": map[string]any{"namespace": namespace, "name": name},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": name, "image": "original", "command": []any{executable}, "args": []any{"preserved"}},
				map[string]any{"name": "sidecar", "image": "sidecar"},
			}}}},
		}
	}
	controller := makeDeployment(stack+"-video-cloud", "pki-controller", "/app/pkicontroller")
	initialIssuer := makeDeployment(stack+"-video-cloud", "certissuer", "/app/pkicontroller")
	issuer := makeDeployment(stack+"-video-cloud", "certissuer", "/app/certissuer")
	foreign := makeDeployment("another-stack", "pki-controller", "/app/pkicontroller")
	definitions := []deploymentplan.Definition{{Object: controller}, {Object: initialIssuer}, {Object: issuer}, {Object: foreign}}
	bindCreateServiceAuthorizationController(definitions, stack, "formal-repair")
	for i, original := range []deploymentplan.Object{controller, initialIssuer} {
		containers := certIssuerObjectList(environmentPKIPod(definitions[i].Object)["containers"])
		candidate := deploymentplan.Map(containers[0])
		if candidate["image"] != "formal-repair" || deploymentplan.Digest(candidate["args"]) != deploymentplan.Digest([]any{"preserved"}) || deploymentplan.Map(containers[1])["image"] != "sidecar" {
			t.Fatalf("repair did not isolate the Controller executable in %s", deploymentplan.ID(original))
		}
		if deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["image"] != "original" {
			t.Fatal("Controller repair rewrote the historical body")
		}
	}
	if deploymentplan.Digest(definitions[2].Object) != deploymentplan.Digest(issuer) || deploymentplan.Digest(definitions[3].Object) != deploymentplan.Digest(foreign) {
		t.Fatal("Controller repair rewrote another workload")
	}
}

func TestServiceAuthorizationRecoveryDispatchesRestoredEnrollment(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	runtime := environmentCreateBootstrapRuntime{ServiceAuthorizationRecovery: &createServiceAuthorizationRecovery{
		Enrollment: &environmentCreateServiceCeremony{BootstrapSessionID: session},
	}}
	// The isolated fixture has no native role adapter. A restored session must
	// still reach the enrollment guard and return its original session identity.
	got, err := executeEnvironmentCreateServiceBootstrap(context.Background(), deploymentConfig{}, environmentCreateBootstrapPlan{}, secretStore{}, nil, runtime, nil)
	if err == nil || !strings.Contains(err.Error(), "exact native selected Kubernetes auth role") || got.BootstrapSessionID != session {
		t.Fatalf("restored session did not enter enrollment: %v", err)
	}
}

func TestServiceEnrollmentJobsFollowResidentRWOClaimOwner(t *testing.T) {
	for _, role := range []string{"account-manager", "certissuer", "pki-controller"} {
		t.Run(role, func(t *testing.T) {
			ns := "video-cloud-staging-video-cloud"
			job := deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never"}}}}
			bindServiceEnrollmentToClaimOwner(job, ns, role)
			pod := environmentPKIPod(job)
			affinity := deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAffinity"])
			terms := certIssuerObjectList(affinity["requiredDuringSchedulingIgnoredDuringExecution"])
			if role == "pki-controller" {
				if len(terms) != 0 {
					t.Fatal("unused Controller claim acquired an unnecessary placement constraint")
				}
				return
			}
			if len(terms) != 1 {
				t.Fatal("Job can be scheduled away from its original RWO owner")
			}
			term := deploymentplan.Map(terms[0])
			selector := deploymentplan.Map(deploymentplan.Map(term["labelSelector"])["matchLabels"])
			if term["topologyKey"] != "kubernetes.io/hostname" || deploymentplan.Digest(term["namespaces"]) != deploymentplan.Digest([]string{ns}) || selector["app.kubernetes.io/name"] != role || pod["restartPolicy"] != "Never" {
				t.Fatal("Job placement failed to preserve its exact claim owner and pod recipe")
			}
		})
	}
}

func TestServiceEnrollmentApplicationReplicasShareClaimNode(t *testing.T) {
	stack := "video-cloud-staging"
	for _, owner := range environmentCreateApplicationOwners() {
		t.Run(owner.Name, func(t *testing.T) {
			labels := deploymentplan.Object{"app.kubernetes.io/name": owner.Name}
			spread := deploymentplan.Object{"topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule", "labelSelector": deploymentplan.Object{"matchLabels": labels}}
			zoneSpread := deploymentplan.Object{"topologyKey": "topology.kubernetes.io/zone", "whenUnsatisfiable": "DoNotSchedule", "labelSelector": deploymentplan.Object{"matchLabels": labels}}
			pod := deploymentplan.Object{"affinity": deploymentplan.Object{"nodeAffinity": deploymentplan.Object{"existing": true}}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", owner.Claim)}, "topologySpreadConstraints": []any{spread, zoneSpread}}
			object := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"spec": pod}}}
			if bindCreateApplicationSharedClaimPlacement(object, stack) {
				t.Fatal("single replica acquired a shared-claim placement constraint")
			}
			deploymentplan.Map(object["spec"])["replicas"] = float64(2)
			if !bindCreateApplicationSharedClaimPlacement(object, stack) || !bindCreateApplicationSharedClaimPlacement(object, stack) {
				t.Fatal("shared claim owner was not bound to one node")
			}
			if spread["whenUnsatisfiable"] != "ScheduleAnyway" || zoneSpread["whenUnsatisfiable"] != "DoNotSchedule" {
				t.Fatal("same-node placement conflicts with hard hostname spread or changes unrelated spread")
			}
			affinity := deploymentplan.Map(pod["affinity"])
			terms := certIssuerObjectList(deploymentplan.Map(affinity["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])
			if len(terms) != 1 || deploymentplan.Map(affinity["nodeAffinity"])["existing"] != true {
				t.Fatal("applying placement did not preserve existing scheduler settings")
			}
			term := deploymentplan.Map(terms[0])
			if term["topologyKey"] != "kubernetes.io/hostname" || deploymentplan.Map(deploymentplan.Map(term["labelSelector"])["matchLabels"])["app.kubernetes.io/name"] != owner.Name {
				t.Fatal("shared replicas were not matched to their own workload")
			}
		})
	}
}

func TestServiceEnrollmentForwardRejectsUnknownJobOutcomes(t *testing.T) {
	for _, outcome := range []struct {
		name, reason string
		active       float64
		succeeded    float64
		allow        bool
	}{
		{name: "deadline", reason: "DeadlineExceeded", allow: true},
		{name: "signing-failed", reason: "BackoffLimitExceeded"},
		{name: "still-running", reason: "DeadlineExceeded", active: 1},
		{name: "issued", reason: "DeadlineExceeded", succeeded: 1},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			job := deploymentplan.Object{"status": deploymentplan.Object{"active": outcome.active, "succeeded": outcome.succeeded, "failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": outcome.reason}}}}
			if got := stagingEnrollmentJobDeadlineExceeded(job); got != outcome.allow {
				t.Fatalf("unexpected admission result %v", got)
			}
		})
	}
}

func TestServiceEnrollmentForwardReadsNativeListEnvelopes(t *testing.T) {
	const job = "pki-enroll-account-manager-client-test"
	const jobUID = "11111111-1111-4111-8111-111111111111"
	const podUID = "22222222-2222-4222-8222-222222222222"
	const volume = "pvc-test"
	const pod = job + "-abcde"
	event := func(kind, name, uid, reason, message string) deploymentplan.Object {
		return deploymentplan.Object{"involvedObject": deploymentplan.Object{"kind": kind, "name": name, "uid": uid}, "reason": reason, "message": message}
	}
	list := func(items []deploymentplan.Object) []byte {
		return mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "metadata": deploymentplan.Object{"resourceVersion": ""}, "items": items})
	}
	pods := list([]deploymentplan.Object{})
	events := []deploymentplan.Object{
		event("Job", job, jobUID, "SuccessfulCreate", "Created pod: "+pod),
		event("Job", job, jobUID, "SuccessfulDelete", "Deleted pod: "+pod),
		event("Pod", pod, podUID, "FailedAttachVolume", `Waiting for detach for volume "pvc-test" Volume is already used by pod(s) account-manager-test`),
	}
	if !stagingEnrollmentAttachmentEvidence(pods, list(events), job, jobUID, volume, "account-manager") {
		t.Fatal("native list metadata prevented validation of an exact unstarted Job")
	}
	if stagingEnrollmentAttachmentEvidence(pods, list(events), job, jobUID, "pvc-unrelated", "account-manager") || stagingEnrollmentAttachmentEvidence(list([]deploymentplan.Object{{"kind": "Pod"}}), list(events), job, jobUID, volume, "account-manager") {
		t.Fatal("an unrelated volume or active Pod was admitted")
	}
	events = append(events, event("Pod", pod, podUID, "Started", "Started container enroll"))
	if stagingEnrollmentAttachmentEvidence(pods, list(events), job, jobUID, volume, "account-manager") {
		t.Fatal("an original Pod that ran was admitted")
	}
}

func TestCreatePKICeremonyRejectsWrongUsersDigestsAndRedirectsWithoutRetry(t *testing.T) {
	const user = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"wrong-user", "disabled-user", "wrong-digest", "redirect", "denied", "invalid-public-response"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "redirect" {
					w.Header().Set("Location", "/private-token-redirect")
					w.WriteHeader(307)
					return
				}
				if mode == "denied" {
					w.WriteHeader(403)
					fmt.Fprint(w, "private-provider-diagnostic")
					return
				}
				if r.URL.Path == "/v1/auth/login" {
					id := user
					disabled := ""
					if mode == "wrong-user" {
						id = "44444444-4444-4444-8444-444444444444"
					}
					if mode == "disabled-user" {
						disabled = `,"disabled_at":"2026-10-01T00:00:00Z"`
					}
					fmt.Fprintf(w, `{"user":{"id":%q,"email":"operator@example.test"%s},"tokens":{"access_token":"private-token"}}`, id, disabled)
					return
				}
				if mode == "invalid-public-response" {
					fmt.Fprint(w, "private-invalid-public-output")
					return
				}
				fmt.Fprint(w, `{"operation_id":"22222222-2222-4222-8222-222222222222","issuer_id":"33333333-3333-4333-8333-333333333333","request_sha256":"`+strings.Repeat("f", 64)+`","status":"requested"}`)
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", user)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Authenticate(context.Background(), "operator@example.test", "private-password")
			if err == nil {
				_, err = client.ReserveIssuer(context.Background(), "parent-root", environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"})
			}
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("invalid ceremony succeeded or leaked private diagnostics", err)
			}
			want := 1
			if mode == "wrong-digest" || mode == "invalid-public-response" {
				want = 2
			}
			if calls != want {
				t.Fatal("mutation retried or redirected", calls, want)
			}
		})
	}
}

func TestCreatePKICeremonyRefusesForeignOrInsecureOrigins(t *testing.T) {
	for _, origin := range []string{"http://account-manager.dev.example", "https://user:pass@example.test", "https://example.test/path", "https://example.test?token=private", "https://example.test#fragment"} {
		if _, err := newEnvironmentCreatePKIClient(origin, nil, "dev", ""); err == nil {
			t.Fatal("invalid initial ceremony origin accepted", origin)
		}
	}
	if _, err := newEnvironmentCreatePKIClient("https://account-manager.example.test", nil, "prod", ""); err == nil {
		t.Fatal("production inherited non-production initial PKI qualification")
	}
}

func TestCreatePKICeremonyCannotReserveFutureOrForeignIssuerBeforeAuthentication(t *testing.T) {
	client, err := newEnvironmentCreatePKIClient("https://account-manager.example.test", nil, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []environmentCreateIssuerRequest{{Environment: "dev", Domain: "service", Kind: "root"}, {Environment: "staging", Domain: "service", Kind: "root"}, {Environment: "dev", Domain: "service", Kind: "intermediate", ParentID: "future-root"}} {
		if _, err := client.ReserveIssuer(context.Background(), "parent", request); err == nil {
			t.Fatal("issuer reservation bypassed genuine authenticated authority")
		}
	}
}

func TestCreateAccountManagerSigningReferencesRemainLifecycleBound(t *testing.T) {
	for _, name := range []string{"JWT_SIGNER_PROVIDER", "JWT_ACCESS_PRIVATE_KEY_PATH", "JWT_ACCESS_PUBLIC_KEY_PATH", "JWT_REFRESH_PRIVATE_KEY_PATH", "JWT_REFRESH_PUBLIC_KEY_PATH"} {
		if !environmentIdentitySetting(name) {
			t.Fatal("original Account Manager signing identity reference would be dropped", name)
		}
	}
	for _, name := range []string{"JWT_ACCESS_TTL", "JWT_REFRESH_SECRET", "JWT_ACCESS_PRIVATE_KEY"} {
		if environmentIdentitySetting(name) {
			t.Fatal("process policy or inline private key was treated as an identity reference", name)
		}
	}
}

func TestCreateRootCeremonyUsesNativeEncryptedKeysAndKeepsOriginalCustody(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("native encrypted-custody fixture needs OpenSSL")
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("operator/env/PKI_OPERATOR_SIGNER_REF", []byte("offline:dev/service-root"), false); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pkiceremony")
	build := exec.Command("go", "build", "-o", binary, "./cmd/pkiceremony")
	build.Dir = filepath.Join(workspace, "repos", "rtk_video_cloud")
	build.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native ceremony fixture build failed: %v %s", err, output)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nset -eu\nwhile [ $# -gt 0 ]; do case \"$1\" in generate|sign|crl) break;; esac; shift; done\npython3 - \"$@\" <<'PY'\nimport os, subprocess, sys\nargs=[(" + fmt.Sprintf("%q", store.Root) + "+a[len('/store'):]) if a.startswith('/store/') else a for a in sys.argv[1:]]\nsys.exit(subprocess.call([" + fmt.Sprintf("%q", binary) + "]+args))\nPY\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	const user = "11111111-1111-4111-8111-111111111111"
	const opID = "22222222-2222-4222-8222-222222222222"
	const issuerID = "33333333-3333-4333-8333-333333333333"
	issuer := map[string]any{"issuer_id": issuerID, "issuer_version": 1, "environment": "dev", "trust_domain": "service", "kind": "root", "status": "approved", "signer_provider": "offline", "created_by": user}
	request := environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-authenticated-fixture" {
			t.Error("registry mutation bypassed authentic user session")
		}
		switch r.URL.Path {
		case "/v1/platform/pki/operations":
			fmt.Fprintf(w, `{"operation_id":%q,"issuer_id":%q,"request_sha256":%q,"status":"requested"}`, opID, issuerID, deploymentplan.Digest(request))
		case "/v1/platform/pki/operations/" + opID + "/authorize":
			w.WriteHeader(204)
		case "/v1/platform/pki/issuers/" + issuerID:
			json.NewEncoder(w).Encode(issuer)
		case "/v1/platform/pki/operations/" + opID + "/provision":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid provision request")
			}
			issuer["csr_pem"], issuer["status"] = body["csr_pem"], "provisioning"
			json.NewEncoder(w).Encode(issuer)
		case "/v1/platform/pki/operations/" + opID + "/import":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid signed import")
			}
			block, _ := pem.Decode([]byte(body["certificate_pem"]))
			if block == nil {
				t.Fatal("invalid certificate")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || cert.CheckSignatureFrom(cert) != nil {
				t.Fatal("native Root certificate was not signed")
			}
			sum := sha256.Sum256(cert.Raw)
			issuer["certificate_pem"], issuer["status"], issuer["certificate_fingerprint_sha256"] = body["certificate_pem"], "ready", hex.EncodeToString(sum[:])
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected initial ceremony route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", user)
	if err != nil {
		t.Fatal(err)
	}
	client.token = "real-authenticated-fixture"
	p := environmentCreateBootstrapPlan{Environment: "dev", Stack: "video-cloud-dev", Image: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)}
	_, ready, err := executeEnvironmentCreateRootCeremony(context.Background(), client, deploymentConfig{Environment: "dev"}, p, store, strings.Repeat("b", 64), "service")
	if err != nil || ready["status"] != "ready" {
		t.Fatal("native initial Root ceremony failed", err)
	}
	keyPath := filepath.Join(store.Root, "pki", "custody", "roots", issuerID, "ca-key.encrypted.pem")
	key, err := os.ReadFile(keyPath)
	if err != nil || !strings.Contains(string(key), "BEGIN ENCRYPTED PRIVATE KEY") {
		t.Fatal("unencrypted or missing custody key", err)
	}
	manifestPath := filepath.Join(filepath.Dir(keyPath), "manifest.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil || strings.Contains(string(manifest), "BEGIN") || !strings.Contains(string(manifest), "rtk-root-custody/v1") {
		t.Fatal("invalid secret-free custody manifest", err)
	}
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("custody key permissions", err)
	}
	if _, _, err := executeEnvironmentCreateRootCeremony(context.Background(), client, deploymentConfig{Environment: "dev"}, p, store, strings.Repeat("b", 64), "service"); err == nil {
		t.Fatal("an earlier native Root ceremony was overwritten")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(key) {
		t.Fatal("earlier encrypted key changed during retry", err)
	}
}

func TestServiceEnrollmentSingleStagingAPIOwnerPreservesIdentity(t *testing.T) {
	stack := "video-cloud-staging"
	for _, owner := range environmentCreateApplicationOwners() {
		t.Run(owner.Name, func(t *testing.T) {
			pod := deploymentplan.Object{"volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", owner.Claim)}}
			spec := deploymentplan.Object{"replicas": float64(2), "template": deploymentplan.Object{"spec": pod}}
			object := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": stack + "-video-cloud"}, "spec": spec}
			original := deploymentplan.Digest(object)
			got := bindStagingSingleAPIOwner(object, stack)
			if got != (owner.Role == "video-cloud-api") {
				t.Fatal("replica change escaped the selected API identity owners")
			}
			if got {
				if spec["replicas"] != float64(1) {
					t.Fatal("exclusive identity owner still has multiple replicas")
				}
				spec["replicas"] = float64(2)
			}
			if deploymentplan.Digest(object) != original {
				t.Fatal("replica change modified the original identity recipe")
			}
		})
	}
}

func TestServiceEnrollmentCoreMediaBindsOnlyMissingPublicAllocation(t *testing.T) {
	settings := map[string]string{}
	projectCoreMediaAllocation(settings, environmentOTAStorageObservation{Bucket: "original", Region: "region", Prefix: "prefix", Endpoint: "https://example.test"})
	c := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "true", "KEEP": "original"})}
	o := deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}}}}}
	if !bindStagingMissingCoreMedia(o, settings) || mqttFoundationEnv(c, "KEEP") != "original" || mqttFoundationEnv(c, "VIDEO_CLOUD_BLOB_BUCKET") != "original" || bindStagingMissingCoreMedia(o, settings) {
		t.Fatal("verified allocation did not fill only the missing Core media settings")
	}
}
