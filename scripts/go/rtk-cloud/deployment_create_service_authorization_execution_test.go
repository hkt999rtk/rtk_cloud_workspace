package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

// A readback must close the original authorization, never issue another write
// or replace a recorded outcome when ownership or registry evidence changed.
func TestServiceAuthorizationReconciliationPreservesOriginalExecution(t *testing.T) {
	for _, mode := range []string{"approved", "operation-id", "issuer-id", "request-digest", "unapproved-operation", "unapproved-issuer", "wrong-parent", "wrong-domain", "wrong-kind", "wrong-provider", "foreign-environment", "operation-read-fails", "issuer-read-fails", "changed-owner", "changed-durable-journal", "existing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "staging")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			root := environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111"}
			request := environmentCreateIssuerRequest{Environment: "staging", Domain: "service", Kind: "intermediate", ParentID: root.ID}
			op := environmentCreateIssuerOperation{ID: "22222222-2222-4222-8222-222222222222", IssuerID: "33333333-3333-4333-8333-333333333333", Digest: deploymentplan.Digest(request), Status: "requested"}
			record := func(key string, intent, receipt any) {
				t.Helper()
				if err := e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if receipt != nil {
					if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
						t.Fatal(err)
					}
					if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			record("pki-initial-root-crl-service", map[string]string{"root": root.ID}, map[string]string{"crl": "original"})
			record("pki-reserve-service-intermediate", request, op)
			key := "pki-authorize-" + op.ID
			record(key, op, nil)
			selected, err := stoppedServiceAuthorization(e.Journal, request)
			if err != nil || selected != op {
				t.Fatal("fixture must qualify the original stopped authorization", err)
			}
			prefix := append([]environmentCreateJournalEntry(nil), e.Journal.Entries[:2]...)
			pending := e.Journal.Entries[2]
			live := op
			live.Status = "approved"
			approved := map[string]any{"issuer_id": op.IssuerID, "environment": "staging", "status": "approved", "kind": "intermediate", "trust_domain": "service", "parent_issuer_id": root.ID, "signer_provider": "openbao"}
			switch mode {
			case "operation-id":
				live.ID = root.ID
			case "issuer-id":
				live.IssuerID = root.ID
			case "request-digest":
				live.Digest = strings.Repeat("f", 64)
			case "unapproved-operation":
				live.Status = "requested"
			case "unapproved-issuer":
				approved["status"] = "requested"
			case "wrong-parent":
				approved["parent_issuer_id"] = op.ID
			case "wrong-domain":
				approved["trust_domain"] = "mqtt"
			case "wrong-kind":
				approved["kind"] = "root"
			case "wrong-provider":
				approved["signer_provider"] = "other"
			case "foreign-environment":
				approved["environment"] = "dev"
			case "changed-owner":
				owner := e.owner
				owner.OwnerToken = strings.Repeat("e", 64)
				if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(owner), true); err != nil {
					t.Fatal(err)
				}
			case "changed-durable-journal":
				journal := e.Journal
				journal.Entries = append(append([]environmentCreateJournalEntry(nil), journal.Entries...), environmentCreateJournalEntry{Key: "unknown-write", State: "pending"})
				rel, _ := filepath.Rel(store.Root, e.JournalPath)
				if err := store.write(rel, mustProviderRecoveryJSON(journal), true); err != nil {
					t.Fatal(err)
				}
			case "existing-receipt":
				if err := e.RecordReceipt(key, json.RawMessage(`{"uncertain":"original-native-outcome"}`)); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer isolated-operator" {
					t.Error("recovery must use authenticated reads only")
					http.Error(w, "unexpected mutation", 400)
					return
				}
				reads++
				switch r.URL.Path {
				case "/v1/platform/pki/operations/" + op.ID:
					if mode == "operation-read-fails" {
						http.Error(w, "unavailable", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(live)
				case "/v1/platform/pki/issuers/" + op.IssuerID:
					if mode == "issuer-read-fails" {
						http.Error(w, "unavailable", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(approved)
				default:
					t.Error("unexpected registry path", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "staging", root.ID)
			if err != nil {
				t.Fatal(err)
			}
			client.token = "isolated-operator"
			err = reconcileOriginalServiceAuthorization(t.Context(), e, client, root, op)
			if mode != "approved" {
				if err == nil {
					t.Fatal("unverified or changed authorization was completed")
				}
				if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
					t.Fatal("rejected recovery changed original private state")
				}
				return
			}
			if err != nil || reads != 2 {
				t.Fatalf("exact approved readback did not complete: reads=%d err=%v", reads, err)
			}
			if len(e.Journal.Entries) != 3 || !reflect.DeepEqual(prefix, e.Journal.Entries[:2]) {
				t.Fatal("recovery rewrote history or appended another authorization")
			}
			got := e.Journal.Entries[2]
			var receipt map[string]any
			if got.State != "completed" || got.CompletedAt.IsZero() || got.IntentSHA256 != pending.IntentSHA256 || got.StartedAt != pending.StartedAt || json.Unmarshal(got.Receipt, &receipt) != nil || !reflect.DeepEqual(receipt, approved) {
				t.Fatal("completion lost original intent or exact registry receipt")
			}
			if err := e.Check(t.Context()); err != nil {
				t.Fatal("completion was not durable under original owner", err)
			}
			after := createBootstrapStateSnapshot(t, store.Root)
			rel, _ := filepath.Rel(store.Root, e.JournalPath)
			delete(before, rel)
			delete(after, rel)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("authorization completion changed unrelated private state")
			}
		})
	}
}

func TestServiceAuthorizationEnrollmentSequenceRejectsUnknownWrites(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	stages := []string{"service-enrollment-issuer", "service-issuer-server-enrollment", "service-managed-enrollment-issuer", "service-core-enrollment", "service-managed-core", "application-client-enrollment", "transport-client-enrollment"}
	for _, mode := range []string{"accepted-prefix", "pending-last", "completed-rwo-forward", "unknown-write", "out-of-order", "earlier-pending", "unrecorded-outcome", "pending-with-receipt", "wrong-session", "incomplete-role", "wrong-phase"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			j := environmentCreateJournal{Environment: "staging"}
			add := func(key, phase string) {
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, Phase: phase, State: "completed", Receipt: json.RawMessage(`{"accepted":true}`), CompletedAt: now})
			}
			add("kubernetes/service-credential-secrets", "service-credential-secrets")
			add("openbao-policy-create-"+session+"-cleanup-controller", "bootstrap")
			add("openbao-role-pki-controller-staging-original", "bootstrap")
			add("openbao-role-certissuer-staging-original", "bootstrap")
			for i, stage := range stages {
				add("kubernetes/"+stage, stage)
				if i == 3 && mode == "completed-rwo-forward" {
					add("kubernetes/service-core-enrollment-rwo-original", "service-core-enrollment-rwo-original")
				}
			}
			last := &j.Entries[len(j.Entries)-1]
			switch mode {
			case "pending-last":
				last.State = "pending"
				last.Receipt = nil
				last.CompletedAt = time.Time{}
			case "unknown-write":
				last.Key = "kubernetes/unknown-write"
			case "out-of-order":
				j.Entries[4], j.Entries[5] = j.Entries[5], j.Entries[4]
			case "earlier-pending":
				j.Entries[4].State = "pending"
				j.Entries[4].Receipt = nil
				j.Entries[4].CompletedAt = time.Time{}
			case "unrecorded-outcome":
				last.Receipt = nil
			case "pending-with-receipt":
				last.State = "pending"
				last.CompletedAt = time.Time{}
			case "wrong-session":
				j.Entries[1].Key = "openbao-policy-create-foreign-cleanup-controller"
			case "incomplete-role":
				j.Entries[3].State = "pending"
			case "wrong-phase":
				last.Phase = "unreviewed-phase"
			}
			before := mustProviderRecoveryJSON(j)
			got, err := serviceAuthorizationEnrollmentStages(j, session)
			valid := mode == "accepted-prefix" || mode == "pending-last" || mode == "completed-rwo-forward"
			if valid {
				if err != nil || len(got) != 7 {
					t.Fatal("original enrollment sequence rejected", err)
				}
				for i, entry := range got {
					if entry.Key != "kubernetes/"+stages[i] {
						t.Fatal("lost original enrollment stage order")
					}
				}
			} else if err == nil {
				t.Fatal("unsafe sequence accepted")
			}
			if string(before) != string(mustProviderRecoveryJSON(j)) {
				t.Fatal("qualification mutated original journal")
			}
		})
	}
}

func TestServiceAuthorizationCLIRefusesUnsafeSelectionBeforeMutation(t *testing.T) {
	for _, mode := range []string{"maintenance", "resume-mqtt-maintenance", "resume-hosts-maintenance", "missing-single-owner", "single-owner-without-mqtt", "wrong-environment", "wrong-confirmation", "wrong-parent", "wrong-source", "unbounded-timeout", "unknown-flag", "report-outside-store"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "staging")
			if err := store.write("recovery-state.json", []byte(`{"original":"maintenance-owner"}`), true); err != nil {
				t.Fatal(err)
			}
			args := []string{"--environment=staging", "--confirm=video-cloud-staging", "--parent-plan-sha256=" + strings.Repeat("a", 64), "--controller-source=" + strings.Repeat("b", 40), "--timeout=1m"}
			expected := "explicit staging stack"
			switch mode {
			case "maintenance":
				expected = "maintenance is active"
			case "resume-mqtt-maintenance":
				args = append(args, "--resume-mqtt-root")
				expected = "maintenance is active"
			case "resume-hosts-maintenance":
				args = append(args, "--resume-managed-hosts", "--single-api-owner")
				expected = "maintenance is active"
			case "missing-single-owner":
				args = append(args, "--resume-managed-hosts")
			case "single-owner-without-mqtt":
				args = append(args, "--single-api-owner")
			case "wrong-environment":
				args[0] = "--environment=prod"
			case "wrong-confirmation":
				args[1] = "--confirm=video-cloud-dev"
			case "wrong-parent":
				args[2] = "--parent-plan-sha256=foreign"
			case "wrong-source":
				args[3] = "--controller-source=main"
			case "unbounded-timeout":
				args[4] = "--timeout=91m"
			case "unknown-flag":
				args = append(args, "--unreviewed-write")
				expected = "flag provided but not defined"
			case "report-outside-store":
				args = append(args, "--report="+filepath.Join(t.TempDir(), "report.json"))
				expected = "outside its canonical environment directory"
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := runCreateServiceAuthorizationRecovery(args)
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("unsafe CLI selection missed intended guard: %v", err)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("refused command changed original state")
			}
		})
	}
}
