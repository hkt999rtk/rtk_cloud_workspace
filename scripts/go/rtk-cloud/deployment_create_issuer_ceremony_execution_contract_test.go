package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitialIntermediateCeremonyExecutionContractKeepsOriginalCSRAndParent(t *testing.T) {
	for _, mode := range []string{"original", "inactive-parent", "foreign-parent", "foreign-provider", "invalid-CSR", "offline-signing-failed", "imported-certificate-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductProtocolFixture(t, true)
			f.next.Document["parent_issuer_id"] = f.l.Root.ID
			f.next.Document["signer_provider"] = "openbao"
			f.next.Document["status"] = "requested"
			root := deploymentplan.Clone(f.l.Root.Document)
			root["status"], root["signer_provider"] = "active", "offline"
			if mode == "inactive-parent" {
				root["status"] = "ready"
			}
			base := f.server.Config.Handler
			before := deploymentplan.Digest(f.l.Root)
			signs := 0
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/boundary/docker" {
					var request struct {
						Args []string `json:"args"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						http.Error(w, "invalid offline invocation", 400)
						return
					}
					respond := func(raw []byte, code int) {
						_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(raw), "code": code})
					}
					out := slices.Index(request.Args, "--out")
					if out < 0 || !slices.Contains(request.Args, "sign") || !slices.Contains(request.Args, "--network") || !slices.Contains(request.Args, "none") {
						t.Error("intermediate signer did not use bounded offline ceremony")
						respond(nil, 1)
						return
					}
					if mode == "offline-signing-failed" {
						respond(nil, 1)
						return
					}
					issuerArg := slices.Index(request.Args, "--issuer")
					var issuer map[string]any
					if issuerArg < 0 || readExecutionJSON(filepath.Join(f.store.Root, strings.TrimPrefix(request.Args[issuerArg+1], "/store/")), &issuer) != nil {
						t.Error("signer received no actual registered provisioning document")
						respond(nil, 1)
						return
					}
					block, _ := pem.Decode([]byte(deploymentplan.String(issuer["csr_pem"])))
					if block == nil {
						t.Error("signer received no original CSR")
						respond(nil, 1)
						return
					}
					csr, err := x509.ParseCertificateRequest(block.Bytes)
					if err != nil || csr.CheckSignature() != nil {
						t.Error("signer received invalid CSR")
						respond(nil, 1)
						return
					}
					certBlock, _ := pem.Decode([]byte(f.next.Certificate))
					cert, _ := x509.ParseCertificate(certBlock.Bytes)
					csrKey, _ := x509.MarshalPKIXPublicKey(csr.PublicKey)
					if !bytes.Equal(csrKey, cert.RawSubjectPublicKeyInfo) || cert.CheckSignatureFrom(f.root) != nil {
						t.Error("signed certificate replaced CSR key or original Root")
						respond(nil, 1)
						return
					}
					path := filepath.Join(f.store.Root, strings.TrimPrefix(request.Args[out+1], "/store/"), "certificate.pem")
					if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
						t.Error(err)
						respond(nil, 1)
						return
					}
					if err := os.WriteFile(path, []byte(f.next.Certificate), 0600); err != nil {
						t.Error(err)
						respond(nil, 1)
						return
					}
					signs++
					respond(nil, 0)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/authorize") && (mode == "foreign-parent" || mode == "foreign-provider") {
					base.ServeHTTP(w, r)
					if mode == "foreign-parent" {
						f.next.Document["parent_issuer_id"] = "99999999-9999-4999-8999-999999999999"
					} else {
						f.next.Document["signer_provider"] = "foreign-provider"
					}
					return
				}
				if strings.HasSuffix(r.URL.Path, "/provision") && mode == "invalid-CSR" {
					f.next.Document["status"], f.next.Document["csr_pem"] = "provisioning", "not-a-signed-CSR"
					_ = json.NewEncoder(w).Encode(f.next.Document)
					return
				}
				base.ServeHTTP(w, r)
				if strings.HasSuffix(r.URL.Path, "/import") && mode == "imported-certificate-drift" {
					f.next.Document["certificate_pem"] = "foreign certificate"
				}
			})
			policy := f.l.Policy
			operation, actual, err := executeEnvironmentCreateIntermediateCeremony(t.Context(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, root, policy)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected original intermediate ceremony result", err)
			}
			if deploymentplan.Digest(f.l.Root) != before {
				t.Fatal("intermediate creation changed permanent Root")
			}
			if mode == "original" {
				if actual["status"] != "ready" || actual["certificate_pem"] != f.next.Certificate || operation.IssuerID != f.next.ID || signs != 1 {
					t.Fatal("native ready intermediate not bound to original CSR and one signing operation")
				}
				for _, prefix := range []string{"pki-authorize-", "pki-provision-", "pki-import-"} {
					entry := createOpenBaoACKCompletionEntry(f.e.Journal, f.runtime.PKIClient.mutationKey(prefix+operation.ID))
					if entry == nil || entry.State != "completed" || len(entry.Receipt) == 0 {
						t.Fatal("native operation lost original durable receipt", prefix)
					}
				}
			}
			if mode == "inactive-parent" && len(f.posts) != 0 {
				t.Fatal("inactive parent reached registry mutation")
			}
		})
	}
}

func TestInitialRootCRLExecutionContractPublishesOnlyItsActualSignedEmptyCRL(t *testing.T) {
	for _, mode := range []string{"original", "wrong-authority", "malformed", "revoked-entry", "wrong-receipt", "command-failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductProtocolFixture(t, true)
			issuer := deploymentplan.Clone(f.l.Root.Document)
			issuer["signer_provider"] = "offline"
			if mode == "wrong-authority" {
				issuer["kind"] = "intermediate"
			}
			base := f.server.Config.Handler
			var signed []byte
			imports := 0
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/boundary/docker" {
					var request struct {
						Args []string `json:"args"`
					}
					_ = json.NewDecoder(r.Body).Decode(&request)
					code := 0
					if mode == "command-failed" {
						code = 1
					} else {
						now := time.Now().UTC().Truncate(time.Second)
						list := &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(24 * time.Hour)}
						if mode == "revoked-entry" {
							list.RevokedCertificateEntries = []x509.RevocationListEntry{{SerialNumber: big.NewInt(7), RevocationTime: now.Add(-time.Minute)}}
						}
						der, err := x509.CreateRevocationList(rand.Reader, list, f.root, f.rootKey)
						if err != nil {
							t.Error(err)
							code = 1
						} else {
							signed = pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
						}
						if mode == "malformed" {
							signed = []byte("not-a-signed-CRL")
						}
						at := slices.Index(request.Args, "--out")
						if at < 0 {
							t.Error("offline CRL output absent")
							code = 1
						} else {
							path := filepath.Join(f.store.Root, strings.TrimPrefix(request.Args[at+1], "/store/"), "revocations.pem")
							if ensurePrivateDirectory(filepath.Dir(path)) != nil || os.WriteFile(path, signed, 0600) != nil {
								t.Error("offline CRL could not be saved")
								code = 1
							}
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"output": "", "code": code})
					return
				}
				if r.URL.Path == "/v1/platform/pki/issuers/"+f.l.Root.ID+"/crl" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["crl_pem"] != string(signed) || r.Header.Get("Authorization") != "Bearer isolated-token" {
						t.Error("registry import changed signed CRL or bypassed authenticated session")
					}
					imports++
					block, _ := pem.Decode(signed)
					sha := environmentPreflightBytesSHA256(block.Bytes)
					if mode == "wrong-receipt" {
						sha = deploymentplan.Digest("foreign CRL")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"issuer_id": f.l.Root.ID, "crl_sha256": sha})
					return
				}
				base.ServeHTTP(w, r)
			})
			path, err := executeEnvironmentCreateRootCRL(context.Background(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, issuer)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected initial signed CRL publication", err)
			}
			if mode == "original" {
				raw, readErr := readBoundedEnvironmentFile(filepath.Join(f.store.Root, path), true)
				if readErr != nil || !bytes.Equal(raw, signed) || imports != 1 {
					t.Fatal("actual signed CRL or exactly one import missing", readErr)
				}
			} else if mode != "wrong-receipt" && imports != 0 {
				t.Fatal("invalid or unavailable CRL reached registry import")
			}
		})
	}
}
