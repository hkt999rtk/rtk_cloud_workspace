package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type devicePublicationSignerFixture struct {
	Root              environmentCreateRegisteredIssuer
	Key               []byte
	Snapshot          createRootCRLNativeSnapshot
	Journal           string
	DropRevocations   bool
	AddLiveRevocation bool
}

// The provider signs when the actual rotate request arrives, avoiding a
// scheduling-dependent synthetic ThisUpdate. It also requires the real journal
// writer to have persisted rotating intent before exposing any signed result.
func TestCRLExecutionContractDeviceSignerProcess(t *testing.T) {
	if os.Getenv("RTK_CRL_DEVICE_SIGNER") != "1" {
		t.Skip("native provider signing subprocess")
	}
	root := os.Getenv("RTK_CRL_DEVICE_STORE")
	if !filepath.IsAbs(root) {
		t.Fatal("explicit isolated provider root required")
	}
	raw, err := os.ReadFile(filepath.Join(root, "deployment/tmp/device-native-signer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture devicePublicationSignerFixture
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("provider signing input")
	}
	if filepath.Clean(fixture.Journal) != fixture.Journal || filepath.IsAbs(fixture.Journal) || !strings.HasPrefix(fixture.Journal, "deployment/create/") {
		t.Fatal("journal escapes selected synthetic root")
	}
	var journal createDeviceRootCRLJournal
	if readExecutionJSON(filepath.Join(root, fixture.Journal), &journal) != nil || journal.Stage != "rotating" || journal.RotationStartedAt.IsZero() {
		t.Fatal("rotation reached provider before durable admission")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(fixture.Key)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("original provider key")
	}
	entries, err := deviceRootCRLUnion(fixture.Root, fixture.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.DropRevocations {
		entries = entries[1:]
	}
	now := time.Now().UTC().Truncate(time.Second)
	record := signedRootCRLFixture(t, fixture.Root, key, createRootCRLRequest{IssuerID: fixture.Root.ID, Fingerprint: fixture.Root.RootFingerprint, Number: "4", ThisUpdate: now, NextUpdate: now.Add(24 * time.Hour), Entries: entries})
	statePath := os.Getenv("RTK_CRL_EXECUTOR_STATE")
	raw, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state crlExecutorBoundary
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("provider boundary")
	}
	state.ProviderRotatedCRL = record.PEM
	if fixture.AddLiveRevocation {
		snapshot := state.Snapshots[fixture.Root.ID]
		snapshot.Bindings = append(snapshot.Bindings, createRootCRLRevocation{Serial: "ff", RevokedAt: now})
		state.Snapshots[fixture.Root.ID] = snapshot
	}
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCRLExecutionContractDevicePublicationReconcilesExactlyOnce(t *testing.T) {
	for _, mode := range []string{"complete", "provider-response-lost", "HTTP-response-lost", "HTTP-rejected-then-retry", "revocations-changed", "signed-union-truncated", "provider-key-changed", "lease-lost", "owner-lost", "qualified-completion", "changed-completion-outcome"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			p, key, now := deviceRootCRLFixture(t)
			p.Parent = f.execution.Journal.PlanSHA256
			p.OwnerSHA = deploymentplan.Digest(f.execution.owner)
			p.SourceSHA = deploymentplan.Digest(f.execution.Journal)
			p.Configuration = f.cfg
			p.ConfigurationSHA = deploymentplan.Digest(f.cfg)
			observation := deploymentplan.Object{"data": deploymentplan.Object{"certificate": p.Root.Certificate, "ca_chain": []string{p.Root.Certificate}, "issuer_id": "99999999-9999-4999-8999-999999999999", "key_id": "88888888-8888-4888-8888-888888888888", "usage": "issuing-certificates,crl-signing"}}
			var err error
			p.ProviderSHA, err = deviceTrustSignerObservation(mustProviderRecoveryJSON(observation), p.Root)
			if err != nil {
				t.Fatal(err)
			}
			p.NativeEvidence = map[string]string{}
			for _, name := range []string{"accepted-service-core-managed-complete-current-native-volumes", managedLateAPICurrentMaterial, "accepted-service-core-managed-application-current-servers", "accepted-service-core-managed-application-current-clients"} {
				p.NativeEvidence[name] = deploymentplan.Digest("original observed " + name)
			}
			a := deviceRootCRLArtifactFixture(p, now)
			token := []byte("isolated-original-provider-token")
			if err := f.store.write("openbao/root-token", token, false); err != nil {
				t.Fatal(err)
			}
			state := f.state(t)
			state.Snapshots = map[string]createRootCRLNativeSnapshot{p.Root.ID: p.Snapshot}
			state.ProviderObservation = mustProviderRecoveryJSON(observation)
			state.ProviderTokenSHA = environmentPreflightBytesSHA256(token)
			state.ProviderCRL = p.Snapshot.Prior.PEM
			state.ProviderRotationResponseLost = mode == "provider-response-lost"
			if mode == "provider-key-changed" {
				deploymentplan.Map(observation["data"])["key_id"] = "77777777-7777-4777-8777-777777777777"
				state.ProviderObservation = mustProviderRecoveryJSON(observation)
			}
			f.writeState(t, state)
			private, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			fixture := devicePublicationSignerFixture{Root: p.Root, Key: private, Snapshot: p.Snapshot, Journal: filepath.ToSlash(filepath.Join(deviceRootCRLBase(deploymentplan.Digest(p)), "journal.json")), DropRevocations: mode == "signed-union-truncated", AddLiveRevocation: mode == "revocations-changed"}
			if err := f.store.write("deployment/tmp/device-native-signer.json", mustProviderRecoveryJSON(fixture), false); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CRL_DEVICE_SIGNER", "1")
			t.Setenv("RTK_CRL_DEVICE_BINARY", binary)
			t.Setenv("RTK_CRL_DEVICE_STORE", f.store.Root)
			var imports atomic.Int32
			var reject atomic.Bool
			reject.Store(mode == "HTTP-rejected-then-retry")
			operator := map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@isolated.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-password"}
			operatorID := "77777777-7777-4777-8777-777777777777"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("unexpected human operation", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 400)
					return
				}
				if r.URL.Path == "/v1/auth/login" {
					var credentials map[string]string
					if json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials["email"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || credentials["password"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
						t.Error("changed original human credentials")
						http.Error(w, "credentials", 403)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": operatorID, "email": credentials["email"]}, "tokens": map[string]string{"access_token": "isolated-human-session"}})
					return
				}
				if r.URL.Path != "/v1/platform/pki/issuers/"+p.Root.ID+"/crl" || r.Header.Get("Authorization") != "Bearer isolated-human-session" {
					t.Error("unbound native import", r.URL.Path)
					http.Error(w, "unauthorized", 403)
					return
				}
				imports.Add(1)
				if reject.Load() {
					http.Error(w, "temporarily unavailable", 503)
					return
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("import body")
					http.Error(w, "body", 400)
					return
				}
				current := f.state(t)
				if body["crl_pem"] != current.ProviderCRL {
					t.Error("import replaced provider output")
					http.Error(w, "different", 409)
					return
				}
				record, err := deviceRootCRLProviderRecord(p.Root, []byte(body["crl_pem"]))
				if err != nil {
					t.Error(err)
					http.Error(w, "record", 400)
					return
				}
				snapshot := current.Snapshots[p.Root.ID]
				snapshot.Prior = record
				current.Snapshots[p.Root.ID] = snapshot
				f.writeState(t, current)
				if mode == "HTTP-response-lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
					return
				}
				json.NewEncoder(w).Encode(record)
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", operatorID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Authenticate(t.Context(), operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
				t.Fatal(err)
			}
			lease, err := f.backend.AcquireExecution(t.Context(), rootCRLLeasePlan(f.cfg, deploymentplan.Digest(p)))
			if err != nil {
				t.Fatal(err)
			}
			originalOwner := f.execution.Journal.OwnerToken
			defer func() {
				f.execution.Journal.OwnerToken = originalOwner
				for id, o := range f.state(t).Objects {
					if o["kind"] == "Lease" {
						s := f.state(t)
						deploymentplan.Map(s.Objects[id]["metadata"])["uid"] = lease.Owner().UID
						f.writeState(t, s)
					}
				}
				if err := lease.Release(t.Context()); err != nil {
					t.Error("release actual test lease", err)
				}
			}()
			started := time.Now().UTC()
			j := createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: deploymentplan.Digest(p), SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "admitted", StartedAt: started, Deadline: started.Add(30 * time.Minute), Lease: lease.Owner(), Evidence: map[string]string{}}
			if err := saveDeviceRootCRLJournal(f.store, a, j); err != nil {
				t.Fatal("real admission writer", err)
			}
			var completion *createDeviceRootCRLCompletionArtifact
			var completionSource createDeviceRootCRLJournal
			if mode == "qualified-completion" || mode == "changed-completion-outcome" {
				if err := lease.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
				started = now.Add(-2 * time.Minute)
				completionSource = j
				completionSource.StartedAt, completionSource.Deadline = started, started.Add(30*time.Minute)
				completionSource.Stage, completionSource.RotationStartedAt, completionSource.LeaseReleased = "rotating", now.Add(-time.Minute), true
				if err := saveDeviceRootCRLJournal(f.store, a, completionSource); err != nil {
					t.Fatal("original rotating admission", err)
				}
				entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
				if err != nil {
					t.Fatal(err)
				}
				signed := signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{IssuerID: p.Root.ID, Fingerprint: p.Root.RootFingerprint, Number: "4", ThisUpdate: now.Add(-30 * time.Second), NextUpdate: now.Add(24 * time.Hour), Entries: entries})
				at := time.Now().UTC()
				completion = &createDeviceRootCRLCompletionArtifact{Schema: createDeviceRootCRLCompletionSchema + "/artifact", Plan: createDeviceRootCRLCompletionPlan{Schema: createDeviceRootCRLCompletionSchema, OriginalReview: deploymentplan.Digest(p), OriginalArtifactSHA: deploymentplan.Digest(a), SourceRepairJournalSHA: deploymentplan.Digest(completionSource), CheckerSHA: deploymentplan.Digest("original checker"), ConfigurationSHA: p.ConfigurationSHA, KubeconfigSHA: p.KubeconfigSHA, ProviderSHA: p.ProviderSHA, QualifiedAt: at, Deadline: completionSource.Deadline, Signed: signed, NativeEvidence: p.NativeEvidence}, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
				deviceRootCRLCompletionFixtureGates(completion)
				deviceRootCRLCompletionFixtureWrite(t, f.store, *completion, completionSource)
				lease, err = f.backend.AcquireExecution(t.Context(), rootCRLLeasePlan(f.cfg, deploymentplan.Digest(completion.Plan)))
				if err != nil {
					t.Fatal(err)
				}
				j = completionSource
				j.Lease, j.LeaseReleased, j.CompletionReview, j.CompletionStartedAt = lease.Owner(), false, deploymentplan.Digest(completion.Plan), time.Now().UTC()
				if err := saveDeviceRootCRLJournal(f.store, a, j); err != nil {
					t.Fatal("fresh completion admission", err)
				}
				s := f.state(t)
				if mode == "changed-completion-outcome" {
					signed = signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{IssuerID: p.Root.ID, Fingerprint: p.Root.RootFingerprint, Number: "5", ThisUpdate: now.Add(-20 * time.Second), NextUpdate: now.Add(24 * time.Hour), Entries: entries})
				}
				s.ProviderCRL = signed.PEM
				f.writeState(t, s)
			}
			if mode == "lease-lost" {
				s := f.state(t)
				for _, o := range s.Objects {
					if o["kind"] == "Lease" {
						deploymentplan.Map(o["metadata"])["uid"] = "foreign"
					}
				}
				f.writeState(t, s)
			}
			if mode == "owner-lost" {
				f.execution.Journal.OwnerToken = "foreign"
			}
			run := func() error {
				return reconcileDeviceRootCRLPublication(t.Context(), f.cfg, f.store, environmentCreateBootstrapRuntime{Execution: f.execution, Backend: f.backend}, operator, client, a, &j, lease, completion, completionSource, io.Discard)
			}
			err = run()
			if mode == "HTTP-rejected-then-retry" {
				if err == nil || j.Stage != "rotated" {
					t.Fatal("rejected import lost durable rotation", j.Stage, err)
				}
				reject.Store(false)
				err = run()
			}
			success := mode == "qualified-completion" || mode == "complete" || mode == "provider-response-lost" || mode == "HTTP-response-lost" || mode == "HTTP-rejected-then-retry"
			if success {
				if err != nil || j.Stage != "imported" || j.Signed.Number != "4" {
					t.Fatal("actual publication did not reconcile", j.Stage, err)
				}
				before := deploymentplan.Digest(j)
				if err := run(); err != nil || deploymentplan.Digest(j) != before {
					t.Fatal("completed publication replay changed history", err)
				}
			} else if err == nil {
				t.Fatal("unproven publication accepted")
			}
			after := f.state(t)
			rotations := 0
			for _, op := range after.ProviderCalls {
				if op == "rotate" {
					rotations++
				}
			}
			expectedRotations := 1
			if mode == "provider-key-changed" || mode == "lease-lost" || mode == "owner-lost" || mode == "qualified-completion" || mode == "changed-completion-outcome" {
				expectedRotations = 0
			}
			if rotations != expectedRotations {
				t.Fatal("rotation repeated or bypassed owner", after.ProviderCalls)
			}
			wantImports := int32(0)
			if success {
				wantImports = 1
			}
			if mode == "HTTP-rejected-then-retry" {
				wantImports = 2
			}
			if imports.Load() != wantImports {
				t.Fatal("uncertain human POST replayed or unproven data imported", imports.Load())
			}
			var saved createDeviceRootCRLJournal
			if readExecutionJSON(filepath.Join(f.store.Root, fixture.Journal), &saved) != nil || !saved.StartedAt.Equal(started) || !saved.Deadline.Equal(started.Add(30*time.Minute)) {
				t.Fatal("original admission deadline reset")
			}
			if success && deploymentplan.Digest(saved) != deploymentplan.Digest(j) {
				t.Fatal("successful publication not durably recorded")
			}
			if _, err := os.Stat(filepath.Join(f.store.Root, deviceRootCRLPointer(p.Parent))); !os.IsNotExist(err) {
				t.Fatal("inner publication bypassed final source and consumer verification")
			}
		})
	}
}
