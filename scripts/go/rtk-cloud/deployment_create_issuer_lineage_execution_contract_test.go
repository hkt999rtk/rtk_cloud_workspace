package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
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

// Exercise the lineage continuation boundary with its existing READY Root.
// Registry, offline signing and native ACKs are external protocols; the real
// lineage executor performs distribution, activation and durable mutations.
func TestInitialIssuerLineageExecutionContractDistributesBeforeActivation(t *testing.T) {
	for _, mode := range []string{"original", "root-drift", "distribution-failure", "activation-drift", "missing-ACK", "signing-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductProtocolFixture(t, true)
			f.unscoped = true
			f.l.Root.Document = deploymentplan.Clone(f.l.Root.Document)
			f.l.Root.Document["status"], f.l.Root.Document["signer_provider"] = "ready", "offline"
			ready, err := environmentCreateRegisteredIssuerFromMap(f.l.Root.Document, "")
			if err != nil {
				t.Fatal(err)
			}
			rootOperation := environmentCreateIssuerOperation{ID: "77777777-7777-4777-8777-777777777777", IssuerID: ready.ID, Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"}), Status: "ready"}
			f.runtime.ServiceRootReady, f.runtime.ServiceRootOperation = &ready, &rootOperation
			f.next.Document["status"] = "requested"
			f.next.Document["parent_issuer_id"] = ready.ID
			policy := f.l.Policy
			published := map[string]environmentCreateRegisteredIssuer{}
			activations, signs, crls := 0, 0, 0
			base := f.server.Config.Handler
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respond := func(raw []byte, code int) {
					_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(raw), "code": code})
				}
				if r.URL.Path == "/boundary/docker" || r.URL.Path == "/boundary/kubectl" {
					var request struct {
						Args  []string `json:"args"`
						Input string   `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						respond(nil, 1)
						return
					}
					if r.URL.Path == "/boundary/kubectl" && slices.Contains(request.Args, "registry-read") && strings.Contains(request.Args[len(request.Args)-1], "pki_bundle_acknowledgments") {
						query := request.Args[len(request.Args)-1]
						matched := false
						for id, issuer := range published {
							if strings.Contains(query, "issuer_id='"+id+"'") && strings.Contains(query, "bundle_version='"+issuer.BundleVersion+"'") {
								matched = true
							}
						}
						if !matched {
							t.Error("native ACK query preceded exact public bundle distribution")
							respond(nil, 1)
							return
						}
						if mode == "missing-ACK" {
							cancel()
							respond([]byte(`[]`), 0)
							return
						}
						respond(mustProviderRecoveryJSON(policy.BundleConsumers), 0)
						return
					}
					if r.URL.Path == "/boundary/docker" {
						at := slices.Index(request.Args, "--out")
						if at < 0 || !slices.Contains(request.Args, "none") {
							t.Error("offline operation escaped its fixed output or network boundary")
							respond(nil, 1)
							return
						}
						var name string
						var raw []byte
						if slices.Contains(request.Args, "crl") {
							now := time.Now().UTC().Truncate(time.Second)
							der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, f.root, f.rootKey)
							if err != nil {
								t.Error(err)
								respond(nil, 1)
								return
							}
							name, raw = "revocations.pem", pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
							crls++
						} else if slices.Contains(request.Args, "sign") {
							if mode == "signing-failure" {
								respond(nil, 1)
								return
							}
							name, raw = "certificate.pem", []byte(f.next.Certificate)
							signs++
						} else {
							t.Error("unexpected offline operation")
							respond(nil, 1)
							return
						}
						path := filepath.Join(f.store.Root, strings.TrimPrefix(request.Args[at+1], "/store/"), name)
						if ensurePrivateDirectory(filepath.Dir(path)) != nil || os.WriteFile(path, raw, 0600) != nil {
							t.Error("offline output unavailable")
							respond(nil, 1)
							return
						}
						respond(nil, 0)
						return
					}
					body, _ := json.Marshal(request)
					r.Body = io.NopCloser(bytes.NewReader(body))
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/activate") {
					activations++
					id := f.next.ID
					if strings.Contains(r.URL.Path, rootOperation.ID) {
						id = ready.ID
					}
					if _, exists := published[id]; !exists {
						t.Error("issuer activated before its native bundle distribution")
					}
					if id == ready.ID {
						f.l.Root.Document["status"] = "active"
						w.WriteHeader(204)
						return
					}
					base.ServeHTTP(w, r)
					if mode == "activation-drift" {
						f.next.Document["trust_bundle_version"] = deploymentplan.Digest("foreign bundle")
					}
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/v1/platform/pki/issuers/"+ready.ID+"/crl" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					block, _ := pem.Decode([]byte(body["crl_pem"]))
					if block == nil {
						t.Error("unsigned CRL import")
						http.Error(w, "invalid", 400)
						return
					}
					list, err := x509.ParseRevocationList(block.Bytes)
					if err != nil || list.CheckSignatureFrom(f.root) != nil || list.Number.Int64() != 1 {
						t.Error("initial CRL changed original Root")
						http.Error(w, "invalid", 400)
						return
					}
					_ = json.NewEncoder(w).Encode(deploymentplan.Object{"issuer_id": ready.ID, "crl_sha256": environmentPreflightBytesSHA256(block.Bytes)})
					return
				}
				base.ServeHTTP(w, r)
			})
			if mode == "root-drift" {
				f.l.Root.Document["certificate_pem"] = "foreign"
			}
			distribute := func(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) error {
				if mode == "distribution-failure" {
					return errors.New("isolated public distribution failure")
				}
				manifest, err := environmentCreateServiceManifest(root, additional...)
				if err != nil {
					return err
				}
				// The callback owns public distribution. Apply its exact signed
				// bundle through the real stage executor before modeling the native ACK.
				definitions := []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "lineage-distribution", "namespace": f.p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"root.pem": root.Certificate, "bundles.json": manifest}}}}
				stage := "service-root-trust"
				if len(additional) > 0 {
					stage = "service-intermediate-trust"
				}
				if err := f.apply(ctx, stage, definitions); err != nil {
					return err
				}
				for _, definition := range definitions {
					actual := f.b.objects[deploymentplan.ID(definition.Object)]
					if !deploymentplan.Matches(deploymentplan.Projection(definition.Object), actual) {
						return errors.New("distributed public trust readback changed")
					}
				}
				published[root.ID] = root
				for _, intermediate := range additional {
					published[intermediate.ID] = intermediate
				}
				return nil
			}
			root, intermediate, crlPath, err := executeEnvironmentCreateIssuerLineage(ctx, f.cfg, f.p, f.store, nil, f.runtime, policy, distribute)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected existing Root lineage continuation", err)
			}
			if mode == "original" {
				if root.ID != ready.ID || root.Status != "active" || intermediate.ID != f.next.ID || intermediate.Status != "active" || activations != 2 || crls != 1 || signs != 1 {
					t.Fatal("lineage replaced identities or repeated a native mutation")
				}
				if _, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, crlPath), true); err != nil {
					t.Fatal(err)
				}
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-initial-root-crl-service")
				if entry == nil || entry.State != "completed" || len(entry.Receipt) == 0 {
					t.Fatal("published initial CRL has no original durable completion")
				}
			}
			if (mode == "root-drift" || mode == "distribution-failure" || mode == "missing-ACK") && activations != 0 {
				t.Fatal("refused source or incomplete distribution activated an issuer")
			}
		})
	}
}
