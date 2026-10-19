package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestQualifiedInitialReservationRecoveryPreservesOriginalIntentAndIdempotency(t *testing.T) {
	for _, mode := range []string{"verified-original-request", "changed-original-intent", "native-rejection"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, operatorSHA, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			request := environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"}
			original := request
			if mode == "changed-original-intent" {
				original.Environment = "staging"
			}
			raw, _ := json.Marshal(original)
			if err := e.BeginEntry("pki-reserve-service-root", "bootstrap", raw, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			intentSHA := e.Journal.Entries[0].IntentSHA256
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/platform/pki/operations" || r.Header.Get("Idempotency-Key") != "original-parent-service-root" {
					t.Error("reservation changed original method, route or idempotency key")
				}
				if mode == "native-rejection" {
					w.WriteHeader(503)
					return
				}
				fmt.Fprintf(w, `{"operation_id":"11111111-1111-4111-8111-111111111111","issuer_id":"22222222-2222-4222-8222-222222222222","request_sha256":%q,"status":"requested"}`, deploymentplan.Digest(request))
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", "33333333-3333-4333-8333-333333333333")
			if err != nil {
				t.Fatal(err)
			}
			client.token = "isolated-authenticated-token"
			client.Execution = e
			client.ReservationRecoveryReview = strings.Repeat("f", 64)
			_, err = client.ReserveIssuer(context.Background(), "original-parent-service-root", request)
			if mode == "verified-original-request" {
				if err != nil || calls != 1 || e.Journal.Entries[0].State != "completed" || e.Journal.Entries[0].IntentSHA256 != intentSHA || len(e.Journal.Entries) != 2 || e.Journal.Entries[1].State != "completed" {
					t.Fatal("genuine recovery lost original intent or completion", err)
				}
			} else {
				if err == nil || e.Journal.Entries[0].State != "pending" || e.Journal.Entries[0].IntentSHA256 != intentSHA {
					t.Fatal("uncertain or changed request fabricated completion")
				}
				if mode == "changed-original-intent" && calls != 0 {
					t.Fatal("changed intent reached the native API")
				}
			}
		})
	}
}
