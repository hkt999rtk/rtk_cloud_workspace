package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Produce all five identities first, then let the native finalizer seal the
// actual session while its CRL receipts are still arriving. Only the external
// API/SQL response is modeled; the complete producer, finalizer, custody cleanup,
// exact ACK checks and durable original completion all run unchanged.
func TestFinalizationConflictExecutionContractResumesOnlyConfirmedSealedProgress(t *testing.T) {
	for _, mode := range []string{"sealed-progress", "still-active", "future-expired", "wrong-issuer", "wrong-root", "incomplete-installations", "repeated-conflict", "denied", "unknown-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductProtocolFixture(t, true)
			if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err != nil {
				t.Fatal("complete native product production", err)
			}
			installed, err := readProductInstalled(f.store, f.l)
			if err != nil || len(installed.Outputs) != 5 {
				t.Fatal("original installed products unavailable", err)
			}
			f.installed = slices.Clone(f.subjects)
			originalKey, originalCSR := f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.csr"]
			finalizations, observations, acks := 0, 0, 0
			consumers, err := productIdentityCRLConsumers(f.l)
			if err != nil {
				t.Fatal("original frozen CRL consumer roster", err)
			}
			var publishedDigest string
			base := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/crl") {
					response := httptest.NewRecorder()
					base.ServeHTTP(response, r)
					var publication struct {
						Issuer string `json:"issuer_id"`
						Digest string `json:"crl_sha256"`
						PEM    string `json:"crl_pem"`
					}
					var block *pem.Block
					if json.Unmarshal(response.Body.Bytes(), &publication) == nil {
						block, _ = pem.Decode([]byte(publication.PEM))
					}
					if response.Code != http.StatusOK || block == nil || publication.Issuer != installed.Ceremony.Intermediate.ID || publication.Digest != environmentPreflightBytesSHA256(block.Bytes) {
						t.Error("native worker did not publish the original intermediate CRL")
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					signed, parseErr := x509.ParseRevocationList(block.Bytes)
					if parseErr != nil || signed.CheckSignatureFrom(f.ica) != nil {
						t.Error("native worker CRL signature changed", parseErr)
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					publishedDigest = publication.Digest
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize") {
					finalizations++
					if mode == "denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if mode == "unknown-response" {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
						return
					}
					if finalizations == 1 || mode == "repeated-conflict" {
						f.status = "sealed"
						w.WriteHeader(http.StatusConflict)
						return
					}
				}
				if r.URL.Path == "/boundary/kubectl" {
					var request struct {
						Args  []string `json:"args"`
						Input string   `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					joined := strings.Join(request.Args, " ")
					if strings.Contains(joined, "'complete',subjects <@ installed_subjects") {
						observations++
						state := deploymentplan.Object{"status": "sealed", "issuer_id": installed.Ceremony.Intermediate.ID, "root_sha256": installed.Ceremony.Root.RootFingerprint, "complete": true, "expires_at": time.Now().UTC().Add(time.Hour)}
						switch mode {
						case "still-active":
							state["status"] = "active"
						case "future-expired":
							state["status"] = "expired"
						case "wrong-issuer":
							state["issuer_id"] = "99999999-9999-4999-8999-999999999999"
						case "wrong-root":
							state["root_sha256"] = deploymentplan.Digest("foreign root")
						case "incomplete-installations":
							state["complete"] = false
						}
						_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(mustProviderRecoveryJSON(state)), "code": 0})
						return
					}
					if strings.Contains(joined, "pki_crl_acknowledgments") {
						acks++
						if publishedDigest == "" || !strings.Contains(joined, "issuer_id='"+installed.Ceremony.Intermediate.ID+"'") || !strings.Contains(joined, "digest='"+publishedDigest+"'") || len(f.installed) != len(f.subjects) {
							t.Error("native ACK lookup did not select the newly signed CRL and installed session")
							w.WriteHeader(http.StatusConflict)
							return
						}
						// The native consumer observation follows the completed
						// credential/producer stage. Its workloads legitimately
						// advanced beyond the earlier successor-trust stage, so
						// do not pretend those historical PodTemplates remain
						// the current native body. The frozen controller roster
						// and exact just-published signed digest are checked.
						_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(mustProviderRecoveryJSON(consumers)), "code": 0})
						return
					}
					raw, _ := json.Marshal(request)
					r.Body = io.NopCloser(bytes.NewReader(raw))
				}
				base.ServeHTTP(w, r)
			})
			err = completeProductIdentitySession(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, installed, nil)
			if (err == nil) != (mode == "sealed-progress") {
				t.Fatal("unexpected native finalization reconciliation", err)
			}
			key := environmentCreateServiceFinalizationKey(f.runtime)
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
			if entry == nil {
				t.Fatal("native finalization lacks original intent")
			}
			if mode == "sealed-progress" {
				if finalizations != 2 || observations != 1 || acks != 1 || entry.State != "completed" || entry.CompletedAt.IsZero() {
					t.Fatal("confirmed native progress did not complete exactly once", finalizations, observations, acks)
				}
				if f.bootstrap["bootstrap.key"] != "" || f.bootstrap["bootstrap.csr"] != "" {
					t.Fatal("native tombstone did not retire original temporary custody")
				}
				if _, err := verifyProductFinalization(t.Context(), f.cfg, f.runtime, f.l, installed); err != nil {
					t.Fatal("native completion was not independently verifiable", err)
				}
			} else {
				calls := 1
				if mode == "repeated-conflict" {
					calls = 2
				}
				if finalizations != calls || entry.State != "pending" || len(entry.Receipt) != 0 || f.bootstrap["bootstrap.key"] != originalKey || f.bootstrap["bootstrap.csr"] != originalCSR {
					t.Fatal("unconfirmed finalization retried or changed original custody", finalizations, entry.State)
				}
				if (mode == "denied" || mode == "unknown-response") && (observations != 0 || acks != 0) {
					t.Fatal("ambiguous or denied mutation entered native retry")
				}
			}
		})
	}
}
