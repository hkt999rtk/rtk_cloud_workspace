package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductAllocatedIssuerMutationReconcilesByNativeReadWithoutPost(t *testing.T) {
	for _, phase := range []string{"reserve", "authorize", "provision", "import", "activate"} {
		for _, mode := range []string{"pass", "pending", "foreign-operation", "foreign-issuer", "foreign-operator", "changed-native", "native-error", "cancelled"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				_, e, _, _ := freshStageFixture(t)
				request := environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "intermediate", ParentID: "11111111-1111-4111-8111-111111111111", ServiceClientIDs: []string{"service:shadow-worker"}}
				op := environmentCreateIssuerOperation{ID: "22222222-2222-4222-8222-222222222222", IssuerID: "33333333-3333-4333-8333-333333333333", Digest: deploymentplan.Digest(request), Status: "requested"}
				status := map[string]string{"reserve": "requested", "authorize": "approved", "provision": "provisioning", "import": "ready", "activate": "active"}[phase]
				issuer := deploymentplan.Object{"issuer_id": op.IssuerID, "environment": "dev", "trust_domain": "service", "kind": "intermediate", "status": status, "parent_issuer_id": request.ParentID, "service_client_ids": request.ServiceClientIDs, "certificate_pem": "recorded-original-public-certificate"}
				nativeIssuer := deploymentplan.Clone(issuer)
				operation := deploymentplan.Object{"operation_id": op.ID, "issuer_id": op.IssuerID, "request_sha256": op.Digest, "status": status, "created_by": "original-operator", "action": "provision"}
				gets, posts := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						posts++
						w.WriteHeader(405)
						return
					}
					gets++
					if mode == "native-error" {
						w.WriteHeader(503)
						return
					}
					if strings.Contains(r.URL.Path, "/operations/") {
						json.NewEncoder(w).Encode(operation)
					} else {
						json.NewEncoder(w).Encode(nativeIssuer)
					}
				}))
				defer server.Close()
				client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", "original-operator")
				if err != nil {
					t.Fatal(err)
				}
				client.Execution, client.mutationScope, client.token = e, deploymentplan.Digest("exact-product-scope"), "isolated-token"
				key := "pki-reserve-service-intermediate"
				var intent, receipt any = request, op
				if phase != "reserve" {
					if e.BeginEntry(client.mutationKey(key), "bootstrap", mustProviderRecoveryJSON(request), time.Now().UTC()) != nil || e.RecordReceipt(client.mutationKey(key), mustProviderRecoveryJSON(op)) != nil || e.CompleteEntry(client.mutationKey(key), time.Now().UTC()) != nil {
						t.Fatal("reservation fixture")
					}
					key = "pki-" + phase + "-" + op.ID
					intent, receipt = op, issuer
					if phase == "provision" {
						intent = deploymentplan.Object{"operation_id": op.ID, "issuer_id": op.IssuerID, "csr_sha256": environmentPreflightBytesSHA256(nil)}
					} else if phase == "import" {
						intent = deploymentplan.Object{"operation_id": op.ID, "issuer_id": op.IssuerID, "certificate_sha256": environmentPreflightBytesSHA256([]byte(deploymentplan.String(issuer["certificate_pem"])))}
					}
				}
				if e.BeginEntry(client.mutationKey(key), "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()) != nil {
					t.Fatal("original intent fixture")
				}
				if mode != "pending" && e.RecordReceipt(client.mutationKey(key), mustProviderRecoveryJSON(receipt)) != nil {
					t.Fatal("recorded native response fixture")
				}
				entry := createOpenBaoACKCompletionEntry(e.Journal, client.mutationKey(key))
				originalStart, originalReceipt := entry.StartedAt, string(entry.Receipt)
				before, _ := os.ReadFile(e.JournalPath)
				ctx := t.Context()
				switch mode {
				case "foreign-operation":
					operation["operation_id"] = request.ParentID
				case "foreign-issuer":
					nativeIssuer["issuer_id"] = request.ParentID
				case "foreign-operator":
					operation["created_by"] = "another-operator"
				case "changed-native":
					nativeIssuer["status"] = "retired"
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				var result deploymentplan.Object
				found, err := readProductMutation(ctx, client, key, intent, &result)
				after := createOpenBaoACKCompletionEntry(e.Journal, client.mutationKey(key))
				if mode == "pass" {
					if err != nil || !found || gets != 2 || after.State != "completed" || !after.StartedAt.Equal(originalStart) || string(after.Receipt) != originalReceipt {
						t.Fatal("known native response did not reconcile its original entry", err)
					}
				} else {
					current, _ := os.ReadFile(e.JournalPath)
					if err == nil || found || after.State == "completed" || string(before) != string(current) {
						t.Fatal("uncertain native outcome was adopted", err)
					}
				}
				if posts != 0 {
					t.Fatal("native mutation was replayed")
				}
			})
		}
	}
}

func TestProductAllocatedObservationNeedsFreshExactProof(t *testing.T) {
	for _, mode := range []string{"pass", "missing-proof", "foreign-proof", "pending"} {
		t.Run(mode, func(t *testing.T) {
			_, e, _, _ := freshStageFixture(t)
			c := &environmentCreatePKIClient{Execution: e, mutationScope: deploymentplan.Digest("product-scope")}
			intent, receipt := deploymentplan.Object{"owner_uid": "original"}, deploymentplan.Object{"observation": "verified-native-original"}
			key := c.mutationKey("observe-shadow-worker")
			if e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()) != nil || mode != "pending" && e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)) != nil {
				t.Fatal("fixture")
			}
			var proof []func(context.Context) error
			if mode != "missing-proof" {
				proof = append(proof, func(ctx context.Context) error {
					if mode == "foreign-proof" {
						return errors.New("fresh native state differs")
					}
					return e.Check(ctx)
				})
			}
			var out deploymentplan.Object
			found, err := readProductMutation(t.Context(), c, "observe-shadow-worker", intent, &out, proof...)
			if (err == nil && found) != (mode == "pass") || (createOpenBaoACKCompletionEntry(e.Journal, key).State == "completed") != (mode == "pass") {
				t.Fatal("known observation bypassed fresh proof or replayed native work", err)
			}
		})
	}
}

func TestProductApprovalReconciliationReadsOnlyExactOriginalRegistryRow(t *testing.T) {
	for _, mode := range []string{"pass", "revoked", "foreign-owner", "foreign-option", "ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			input, _ := json.Marshal(frozenKubernetesFixture(t))
			store, e, _, _ := freshStageFixture(t)
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
			b, err := newEnvironmentKubernetesBackend(string(input), "dev")
			if err != nil {
				t.Fatal(err)
			}
			b.Store = store
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, PKIClient: &environmentCreatePKIClient{OperatorID: "original-operator"}}
			intent := deploymentplan.Object{"service_id": "shadow", "instance_id": "shadow-dev", "certificate_subject": "service:shadow-worker", "issuer_fingerprint_sha256": deploymentplan.Digest("original-issuer"), "allowed_option_codes": []string{"shadow"}}
			row := deploymentplan.Object{}
			for k, v := range intent {
				row[k] = v
			}
			row["approved_by"], row["revoked"] = "original-operator", false
			rows := []deploymentplan.Object{row}
			switch mode {
			case "revoked":
				row["revoked"] = true
			case "foreign-owner":
				row["approved_by"] = "foreign"
			case "foreign-option":
				row["allowed_option_codes"] = []string{"unreviewed"}
			case "ambiguous":
				rows = append(rows, row)
			case "missing":
				rows = nil
			}
			dir := t.TempDir()
			if os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\nset -eu\ncase \"$*\" in *default_transaction_read_only=on*rtk_account_manager*) ;; *) exit 1;; esac\nprintf '%s' \"$RTK_TEST_PRODUCT_APPROVAL_JSON\"\n"), 0700) != nil {
				t.Fatal("stub")
			}
			data, _ := json.Marshal(rows)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_PRODUCT_APPROVAL_JSON", string(data))
			err = verifyProductWorkloadApproval(t.Context(), cfg, runtime, intent)
			if (err == nil) != (mode == "pass") {
				t.Fatal("native approval row was accepted outside exact original scope", err)
			}
		})
	}
}
