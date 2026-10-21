package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This process implements only the external offline ceremony boundary. Its
// private root is explicitly selected by the parent and every /store path is
// constrained to that synthetic root. Production candidate validation remains
// responsible for signatures, immutable input hashes, and retry behavior.
func TestCRLExecutionContractCeremonyProcess(t *testing.T) {
	if os.Getenv("RTK_CRL_CEREMONY_HELPER") != "1" {
		t.Skip("external ceremony subprocess helper")
	}
	storeRoot := os.Getenv("RTK_CRL_CEREMONY_STORE")
	args := os.Args
	value := func(flag string) string {
		t.Helper()
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		t.Fatal("missing protocol argument", flag)
		return ""
	}
	resolve := func(flag string) string {
		t.Helper()
		v := value(flag)
		if !strings.HasPrefix(v, "/store/") {
			t.Fatal("nonisolated ceremony path")
		}
		rel := strings.TrimPrefix(v, "/store/")
		if filepath.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
			t.Fatal("escaping ceremony path")
		}
		return filepath.Join(storeRoot, rel)
	}
	if !filepath.IsAbs(storeRoot) || value("--mount") != "type=bind,src="+storeRoot+",dst=/store" || value("--network") != "none" || value("--entrypoint") != "/app/pkiceremony" {
		t.Fatal("offline isolated ceremony protocol changed")
	}
	raw, err := os.ReadFile(resolve("--crl-request"))
	if err != nil {
		t.Fatal(err)
	}
	var request createRootCRLRequest
	if json.Unmarshal(raw, &request) != nil || deploymentplan.Digest(request) != value("--expected-request-sha256") {
		t.Fatal("request digest mismatch")
	}
	raw, err = os.ReadFile(os.Getenv("RTK_CRL_CEREMONY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	var root environmentCreateRegisteredIssuer
	if json.Unmarshal(raw, &root) != nil {
		t.Fatal("root fixture")
	}
	encrypted, err := os.ReadFile(resolve("--key"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(resolve("--passphrase-file"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(encrypted)
	if block == nil {
		t.Fatal("encrypted original key")
	}
	der, err := x509.DecryptPEMBlock(block, password)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("original signing key type")
	}
	dir := resolve("--out")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	calls := os.Getenv("RTK_CRL_CEREMONY_CALLS")
	file, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("sign\n")
	file.Close()
	if os.Getenv("RTK_CRL_CEREMONY_MODE") == "partial" {
		os.Exit(8)
	}
	signed := signedRootCRLFixture(t, root, key, request)
	if err := os.WriteFile(filepath.Join(dir, "revocations.pem"), []byte(signed.PEM), 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("RTK_CRL_CEREMONY_MODE") == "response-lost" {
		os.Exit(9)
	}
}

func TestCRLExecutionContractStableOfflineCandidate(t *testing.T) {
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		for _, mode := range []string{"replay", "response-lost", "partial", "changed-request", "changed-descriptor", "changed-signature"} {
			t.Run(domain+"/"+mode, func(t *testing.T) {
				f := newCRLExecutorFixture(t)
				root, key, snapshot, _ := rootCRLFixture(t)
				root.Domain = domain
				root.ID = "22222222-2222-4222-8222-222222222222"
				if domain == "openbao_tls" {
					root.ID = "734487b8-abcf-40be-a813-8b52c64f7954"
				}
				snapshot.Prior.IssuerID = root.ID
				private, err := x509.MarshalPKCS8PrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				password := []byte("isolated-custody-password")
				encrypted, err := x509.EncryptPEMBlock(rand.Reader, "PRIVATE KEY", private, password, x509.PEMCipherAES256)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"manifest.json", "ca-key.encrypted.pem", "certificate.pem", "csr.pem"} {
					raw := []byte("isolated custody metadata: " + name)
					if name == "ca-key.encrypted.pem" {
						raw = pem.EncodeToMemory(encrypted)
					}
					if name == "certificate.pem" {
						raw = []byte(root.Certificate)
					}
					if err := f.store.write(filepath.Join("pki/custody/roots", root.ID, name), raw, false); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.store.write("pki/custody/"+domain+"-root-v1-key-passphrase", password, false); err != nil {
					t.Fatal(err)
				}
				if err := f.store.write(filepath.Join("deployment/create", f.execution.Journal.PlanSHA256, "bootstrap", domain, "root-active.json"), mustProviderRecoveryJSON(root), false); err != nil {
					t.Fatal(err)
				}
				var material map[string]string
				if domain == "mqtt" {
					material, err = createMQTTRootCRLMaterial(f.store, f.execution.Journal.PlanSHA256, root)
				} else {
					material, err = createOpenBaoTLSRootCRLMaterial(f.store, f.execution.Journal.PlanSHA256, root)
				}
				if err != nil || len(material) != 6 {
					t.Fatal("original custody unavailable", err)
				}
				dir := t.TempDir()
				rootPath := filepath.Join(dir, "root.json")
				callsPath := filepath.Join(dir, "calls")
				if err := os.WriteFile(rootPath, mustProviderRecoveryJSON(root), 0600); err != nil {
					t.Fatal(err)
				}
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("RTK_CRL_CEREMONY_HELPER", "1")
				t.Setenv("RTK_CRL_CEREMONY_BINARY", binary)
				t.Setenv("RTK_CRL_CEREMONY_STORE", f.store.Root)
				t.Setenv("RTK_CRL_CEREMONY_ROOT", rootPath)
				t.Setenv("RTK_CRL_CEREMONY_CALLS", callsPath)
				t.Setenv("RTK_CRL_CEREMONY_MODE", mode)
				if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexec \"$RTK_CRL_CEREMONY_BINARY\" -test.run='^TestCRLExecutionContractCeremonyProcess$' -- \"$@\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				prepare := func() (string, createRootCRLRequest, createRootCRLRecord, error) {
					image := "ghcr.io/rtk-cloud/pkiceremony@sha256:" + deploymentplan.Digest("isolated immutable native ceremony")
					if domain == "mqtt" {
						return prepareMQTTRootCRLCandidate(t.Context(), f.store, f.execution, image, root, snapshot, material)
					}
					return prepareOpenBaoTLSRootCRLCandidate(t.Context(), f.store, f.execution, image, root, snapshot, material)
				}
				candidate, request, signed, err := prepare()
				if mode == "response-lost" || mode == "partial" {
					if err == nil {
						t.Fatal("uncertain ceremony marked successful")
					}
				} else if err != nil {
					t.Fatal("genuine candidate creation", err)
				}
				descriptorPath := filepath.Join(f.store.Root, candidate, "candidate.json")
				before, err := os.ReadFile(descriptorPath)
				if err != nil {
					t.Fatal(err)
				}
				signaturePath := filepath.Join(f.store.Root, candidate, "signed/revocations.pem")
				signature, _ := os.ReadFile(signaturePath)
				switch mode {
				case "changed-request":
					if err := os.WriteFile(filepath.Join(f.store.Root, candidate, "request.json"), []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				case "changed-descriptor":
					var descriptor map[string]any
					if json.Unmarshal(before, &descriptor) != nil {
						t.Fatal("descriptor")
					}
					descriptor["SourceOwnerSHA"] = deploymentplan.Digest("foreign owner")
					if err := os.WriteFile(descriptorPath, mustProviderRecoveryJSON(descriptor), 0600); err != nil {
						t.Fatal(err)
					}
				case "changed-signature":
					if err := os.WriteFile(signaturePath, []byte("foreign signed data"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("RTK_CRL_CEREMONY_MODE", "replay")
				candidate2, request2, signed2, err := prepare()
				if mode == "replay" || mode == "response-lost" {
					if err != nil || candidate2 != candidate || deploymentplan.Digest(request) != deploymentplan.Digest(request2) || string(signature) != signed2.PEM {
						t.Fatal("original signature not reused after retry", err)
					}
					if mode == "replay" && deploymentplan.Digest(signed) != deploymentplan.Digest(signed2) {
						t.Fatal("replay changed signed result")
					}
					raw, _ := os.ReadFile(descriptorPath)
					if string(raw) != string(before) {
						t.Fatal("immutable descriptor rewritten")
					}
				} else if err == nil {
					t.Fatal("partial or modified candidate was overwritten")
				}
				calls, err := os.ReadFile(callsPath)
				if err != nil || string(calls) != "sign\n" {
					t.Fatal("retry invoked another offline signature", string(calls), err)
				}
				for path, sha := range material {
					raw, err := os.ReadFile(filepath.Join(f.store.Root, path))
					if err != nil || environmentPreflightBytesSHA256(raw) != sha {
						t.Fatal("original custody changed", path, err)
					}
				}
			})
		}
	}
}
