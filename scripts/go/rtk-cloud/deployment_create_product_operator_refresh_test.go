package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productOperatorHTTP struct {
	logins, mutations, rejected                                 int
	token                                                       string
	expired, foreignOperator, refuseMutation, uncertainMutation bool
	events                                                      []string
}

func newProductOperatorHTTP(t *testing.T) (*productOperatorHTTP, *environmentCreatePKIClient, map[string]string) {
	t.Helper()
	f := &productOperatorHTTP{}
	operator := map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-password"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			f.logins++
			f.events = append(f.events, "login")
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || body["password"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "bad isolated credentials", 401)
				return
			}
			id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			if f.foreignOperator {
				id = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			}
			f.token = fmt.Sprintf("isolated-token-%d", f.logins)
			f.expired = false
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": id, "email": body["email"], "signup_pending_verification": false}, "tokens": map[string]string{"access_token": f.token}})
			return
		}
		if r.URL.Path != "/v1/platform/service-workloads" {
			http.NotFound(w, r)
			return
		}
		f.mutations++
		f.events = append(f.events, "mutation")
		if f.expired || f.refuseMutation || r.Header.Get("Authorization") != "Bearer "+f.token {
			f.rejected++
			http.Error(w, "expired isolated access token", 401)
			return
		}
		if f.uncertainMutation {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = c.Close()
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	c, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Authenticate(t.Context(), operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal(err)
	}
	return f, c, operator
}

func TestProductOperatorRefreshAfterLongNativePhaseUsesOriginalIdentityOnce(t *testing.T) {
	f, c, operator := newProductOperatorHTTP(t)
	c.operatorCredentials = operator
	oldToken := c.token
	c.authenticatedAt = time.Now().Add(-16 * time.Minute)
	f.expired = true
	if err := c.call(t.Context(), http.MethodPost, "/v1/platform/service-workloads", "", deploymentplan.Object{"service_id": "mqtt"}, nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || f.mutations != 1 || f.rejected != 0 || c.OperatorID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || c.token == oldToken || !slices.Equal(f.events, []string{"login", "login", "mutation"}) {
		t.Fatal("refresh order, original operator or exact mutation count changed")
	}
	if err := c.call(t.Context(), http.MethodPost, "/v1/platform/service-workloads", "", deploymentplan.Object{"service_id": "ota"}, nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || f.mutations != 2 {
		t.Fatal("fresh calls recursively reauthenticated")
	}
}

func TestProductOperatorRefreshRefusesIdentitySubstitutionAndNeverRetriesMutation(t *testing.T) {
	for _, mode := range []string{"foreign-operator", "definite-401", "unknown-response", "ordinary-unbound-client"} {
		t.Run(mode, func(t *testing.T) {
			f, c, operator := newProductOperatorHTTP(t)
			c.operatorCredentials = operator
			oldToken := c.token
			switch mode {
			case "foreign-operator":
				c.authenticatedAt = time.Now().Add(-16 * time.Minute)
				f.expired = true
				f.foreignOperator = true
			case "definite-401":
				f.refuseMutation = true
			case "unknown-response":
				f.uncertainMutation = true
			case "ordinary-unbound-client":
				c.operatorCredentials = nil
				c.authenticatedAt = time.Now().Add(-16 * time.Minute)
				f.expired = true
			}
			if err := c.call(t.Context(), http.MethodPost, "/v1/platform/service-workloads", "", deploymentplan.Object{"service_id": "mqtt"}, nil); err == nil {
				t.Fatal("expected authentication/outcome refusal")
			}
			if mode == "foreign-operator" {
				if f.logins != 2 || f.mutations != 0 || c.token != oldToken {
					t.Fatal("foreign operator reached mutation or replaced original token")
				}
			} else if f.logins != 1 || f.mutations != 1 {
				t.Fatal("mutation or authentication replayed", f.logins, f.mutations)
			}
		})
	}
}

func TestProductOperatorRefreshWholeFiveProductRecoveryKeepsOriginalPrefix(t *testing.T) {
	f := newProductForwardFixture(t, true)
	start := len(f.e.Journal.Entries)
	operator := map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-password"}
	client := f.runtime.PKIClient
	token := client.token
	expired := false
	logins, rejected, phases := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || body["password"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "unexpected credentials", 401)
				return
			}
			logins++
			token = fmt.Sprintf("isolated-product-jwt-%d", logins)
			expired = false
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": client.OperatorID, "email": body["email"]}, "tokens": map[string]string{"access_token": token}})
			return
		}
		if expired || r.Header.Get("Authorization") != "Bearer "+token {
			rejected++
			http.Error(w, "expired product access", 401)
			return
		}
		f.productProtocolFixture.serve(w, r)
	}))
	defer server.Close()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.Origin, client.HTTP, client.operatorCredentials = origin, server.Client(), operator
	f.runtime.APIOrigin, f.runtime.APIClient = server.URL, server.Client()
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage == "service-session" && f.old != nil {
			if handled, err := executeProductSessionTrustForward(ctx, f.cfg, f.store, f.runtime, f.l, f.apply); handled || err != nil {
				return err
			}
		}
		err := f.apply(ctx, stage, defs)
		if stage == "service-session" && f.old == nil {
			f.old = f.plans["product-identities-"+productIdentityScope(f.l)[:12]+"-service-session"]
			if f.old != nil {
				f.oldJournal, _, _ = f.old.ReadExecution(f.store.Root)
			}
		}
		if err == nil && strings.HasPrefix(stage, "enroll-") {
			f.issue("service:" + strings.TrimPrefix(stage, "enroll-"))
			// Simulate each real native wait crossing the access-JWT lifetime. The
			// next HTTP endpoint actually refuses the old token, not a canned success.
			phases++
			expired = true
			client.authenticatedAt = time.Now().Add(-16 * time.Minute)
		}
		return err
	}
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, apply); err == nil || f.old == nil {
		t.Fatal("genuine initial failure missing", err)
	}
	pending := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/product-identities-"+productIdentityScope(f.l)[:12]+"-service-session")
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, apply); err != nil {
		t.Fatal("whole recovered product protocol", err)
	}
	if phases != 5 || logins != 5 || rejected != 0 || f.minted != 1 {
		t.Fatal("five native boundaries did not refresh exactly once", phases, logins, rejected)
	}
	entries := f.e.Journal.Entries[start:]
	for end := 1; end <= len(entries); end++ {
		view := *f.e
		view.Journal = f.e.Journal
		view.Journal.Entries = slices.Clone(f.e.Journal.Entries[:start+end])
		if createOpenBaoACKCompletionEntry(view.Journal, "kubernetes/"+productSessionForwardStage(f.l, "forward")) == nil {
			for i := range view.Journal.Entries {
				if view.Journal.Entries[i].Key == pending.Key {
					view.Journal.Entries[i] = pending
				}
			}
		}
		n, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, &view, f.l, view.Journal.Entries[start:])
		if err != nil || n != end {
			t.Fatalf("recovered prefix %d/%d consumed%d: %v", end, len(entries), n, err)
		}
	}
	old, _, err := f.old.ReadExecution(f.store.Root)
	if err != nil || old.Accepted || deploymentplan.Digest(old) != deploymentplan.Digest(f.oldJournal) {
		t.Fatal("authentication refresh changed failed original", err)
	}
}
