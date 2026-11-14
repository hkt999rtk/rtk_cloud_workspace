package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initialPKIRetryTransport func(*http.Request) (*http.Response, error)

func (f initialPKIRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInitialPKIRetriesOnlySafeTransientRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, fault string
		auth                bool
		calls               int
		failure             bool
	}{
		{"read-eof", "GET", "eof", false, 2, false},
		{"read-unavailable", "GET", "503", false, 2, false},
		{"login-eof", "POST", "eof", true, 2, false},
		{"login-unavailable", "POST", "503", true, 2, false},
		{"read-rate-limited", "GET", "429", false, 2, false},
		{"read-partial-response", "GET", "body-eof", false, 2, false},
		{"read-bounded", "GET", "always-eof", false, 2, true},
		{"read-canceled", "GET", "canceled", false, 1, true},
		{"mutation-eof", "POST", "eof", false, 1, true},
		{"mutation-partial-response", "POST", "body-eof", false, 1, true},
		{"mutation-unavailable", "POST", "503", false, 1, true},
		{"read-unauthorized", "GET", "401", false, 1, true},
		{"read-forbidden", "GET", "403", false, 1, true},
		{"read-owner-changed", "GET", "owner", false, 1, true},
		{"read-invalid-public-record", "GET", "invalid", false, 1, true},
		{"login-wrong-identity", "POST", "wrong-user", true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const user = "11111111-1111-4111-8111-111111111111"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: initialPKIRetryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.fault == "canceled" {
					cancel()
					return nil, io.EOF
				}
				if r.Method != tc.method {
					t.Fatal("request method changed")
				}
				if r.Body != nil {
					defer r.Body.Close()
				}
				if !tc.auth && tc.method == "POST" && r.Header.Get("Idempotency-Key") != "original-operation" {
					t.Fatal("operation key changed")
				}
				if tc.fault == "always-eof" || calls == 1 && tc.fault == "eof" {
					return nil, fmt.Errorf("private-transport-canary: %w", io.EOF)
				}
				if tc.fault == "owner" {
					return nil, &deploymentplan.Blocker{ID: "deployment.ownership", Cause: "original owner changed"}
				}
				code, body := 200, `{"issuer_id":"`+user+`","environment":"dev"}`
				if tc.auth {
					body = `{"user":{"id":"` + user + `","email":"original@example.test"},"tokens":{"access_token":"isolated-token"}}`
				}
				if calls == 1 {
					switch tc.fault {
					case "503":
						code = 503
					case "429":
						code = 429
					case "401":
						code = 401
					case "403":
						code = 403
					case "invalid":
						body = `{`
					case "wrong-user":
						body = strings.ReplaceAll(body, user, "22222222-2222-4222-8222-222222222222")
					}
				}
				var response io.ReadCloser = io.NopCloser(strings.NewReader(body))
				if calls == 1 && tc.fault == "body-eof" {
					response = io.NopCloser(initialPKIRetryBrokenReader{})
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: response}, nil
			})}
			c, err := newEnvironmentCreatePKIClient("http://127.0.0.1:18444", client, "dev", user)
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth {
				_, err = c.Authenticate(ctx, "original@example.test", "isolated-password")
			} else if tc.method == "GET" {
				_, err = c.ReadIssuer(ctx, user)
			} else {
				err = c.call(ctx, "POST", "/v1/platform/pki/operations/"+user+"/authorize", "original-operation", map[string]string{"request_sha256": "same-request"}, nil)
			}
			if (err != nil) != tc.failure || calls != tc.calls {
				t.Fatalf("incorrect safe retry/outcome: calls=%d error=%v", calls, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-transport-canary") {
				t.Fatal("private transport error escaped")
			}
			if tc.fault == "always-eof" {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.bootstrap.pki" {
					t.Fatal("safe transport failure lost its public check ID")
				}
			}
		})
	}
}

type initialPKIRetryBrokenReader struct{}

func (initialPKIRetryBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
