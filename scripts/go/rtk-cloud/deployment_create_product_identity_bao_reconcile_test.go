package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductAllocatedOpenBaoResponseUsesReadbackWithoutRewrite(t *testing.T) {
	for _, kind := range []string{"policy", "role"} {
		for _, mode := range []string{"pass", "changed-native", "pending"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newProductProtocolFixture(t, true)
				writes := 0
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, "fixture request", 500)
						return
					}
					var request struct {
						Args []string `json:"args"`
					}
					if json.Unmarshal(body, &request) == nil {
						command := strings.Join(request.Args, " ")
						if strings.Contains(command, "bao policy write ") || strings.Contains(command, "bao write auth/kubernetes/role/") {
							writes++
						}
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					f.serve(w, r)
				}))
				defer proxy.Close()
				t.Setenv("PRODUCT_PROTOCOL_BOUNDARY", proxy.URL+"/boundary")
				approved := deploymentplan.Clone(f.next.Document)
				approved["status"] = "approved"
				if _, err := configureEnvironmentCreateIssuerPolicies(t.Context(), f.cfg, f.runtime, approved); err != nil {
					t.Fatal("actual original policy installation", err)
				}
				key := "openbao-policy-create-" + f.next.ID + "-controller"
				policies := append([]string(nil), f.roles["pki-controller-dev"]...)
				if kind == "role" {
					for _, entry := range f.e.Journal.Entries {
						if strings.HasPrefix(entry.Key, "openbao-role-pki-controller-dev-") {
							key = entry.Key
						}
					}
				}
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
				if entry == nil || entry.State != "completed" {
					t.Fatal("original native response missing")
				}
				originalReceipt, originalStart := string(entry.Receipt), entry.StartedAt
				entry.State, entry.CompletedAt = "allocated", time.Time{}
				if mode == "pending" {
					entry.State, entry.Receipt = "pending", nil
				}
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
				if mode == "changed-native" {
					if kind == "policy" {
						f.policies["create-"+f.next.ID+"-controller"] = "foreign-policy"
					} else {
						f.roles["pki-controller-dev"] = []string{"foreign-policy"}
					}
				}
				writes = 0
				var err error
				if kind == "policy" {
					_, err = configureEnvironmentCreateIssuerPolicies(t.Context(), f.cfg, f.runtime, approved)
				} else {
					err = configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, "pki-controller-dev", []string{"pki-controller"}, policies)
				}
				current := createOpenBaoACKCompletionEntry(f.e.Journal, key)
				if (err == nil) != (mode == "pass") || writes != 0 || (current.State == "completed") != (mode == "pass") || !current.StartedAt.Equal(originalStart) || mode == "pass" && string(current.Receipt) != originalReceipt {
					t.Fatal("known native readback rewrote policy/role or adopted drift", err, writes)
				}
			})
		}
	}
}
