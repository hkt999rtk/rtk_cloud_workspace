package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the offline container is simulated. The adapter calls the maintained
// ceremony runner, and this process reads its exact mounted request and original
// encrypted key. No signed output exists until that invocation succeeds.
func installHistoricalOfflineCRLSigner(t *testing.T, store secretStore) *atomic.Int64 {
	t.Helper()
	original, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args []string
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&args) != nil {
			http.Error(w, "invalid isolated offline invocation", http.StatusBadRequest)
			return
		}
		if err := signHistoricalOfflineCRL(store, args); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys,urllib.request
original=%q
endpoint=%q
args=sys.argv[1:]
if args[:1]!=['run'] or 'crl' not in args:
 os.execv(original,[original]+args)
request=urllib.request.Request(endpoint,data=json.dumps(args).encode(),method='POST',headers={'Content-Type':'application/json'})
with urllib.request.urlopen(request,timeout=15) as response:
 if response.status!=204:sys.exit(1)
`, original, server.URL)
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func signHistoricalOfflineCRL(store secretStore, args []string) error {
	value := func(flag string) string {
		at := slices.Index(args, flag)
		if at < 0 || at+1 >= len(args) || slices.Index(args[at+1:], flag) >= 0 {
			return ""
		}
		return args[at+1]
	}
	if len(args) == 0 || args[0] != "run" || value("--network") != "none" || value("--entrypoint") != "/app/pkiceremony" || value("--cap-drop") != "ALL" || value("--security-opt") != "no-new-privileges" || !slices.Contains(args, "--read-only") || value("--mount") != "type=bind,src="+store.Root+",dst=/store" || !slices.Contains(args, "crl") {
		return fmt.Errorf("offline signing must retain the exact private, network-isolated mount")
	}
	path := func(flag string) (string, error) {
		v := value(flag)
		if !strings.HasPrefix(v, "/store/") {
			return "", fmt.Errorf("offline material is outside the selected private store")
		}
		return store.safePath(strings.TrimPrefix(v, "/store/"))
	}
	read := func(flag string) ([]byte, error) {
		p, err := path(flag)
		if err != nil {
			return nil, err
		}
		return readBoundedEnvironmentFile(p, true)
	}
	requestRaw, err := read("--crl-request")
	var request createRootCRLRequest
	if err != nil || decodeEnvironmentJSON(requestRaw, &request) != nil || deploymentplan.Digest(request) != value("--expected-request-sha256") {
		return fmt.Errorf("offline request differs from the qualified digest")
	}
	issuerRaw, err := read("--issuer")
	var doc deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(issuerRaw, &doc) != nil {
		return fmt.Errorf("original issuer document unavailable")
	}
	issuer, err := environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil || issuer.ID != request.IssuerID || issuer.RootFingerprint != request.Fingerprint || issuer.Kind != "root" || issuer.Status != "active" {
		return fmt.Errorf("offline request changed its original issuer")
	}
	cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil || request.NextUpdate.After(cert.NotAfter) {
		return fmt.Errorf("offline request exceeds its original certificate")
	}
	keyRaw, err := read("--key")
	block, _ := pem.Decode(keyRaw)
	if err != nil || block == nil || !x509.IsEncryptedPEMBlock(block) {
		return fmt.Errorf("original encrypted signing key unavailable")
	}
	passphrase, err := read("--passphrase-file")
	if err != nil {
		return err
	}
	der, err := x509.DecryptPEMBlock(block, passphrase)
	if err != nil {
		return fmt.Errorf("original encrypted key could not be opened")
	}
	key, err := x509.ParseECPrivateKey(der)
	public, valid := cert.PublicKey.(*ecdsa.PublicKey)
	if err != nil || !valid || !public.Equal(&key.PublicKey) {
		return fmt.Errorf("original signing key differs from the issuer")
	}
	number, ok := new(big.Int).SetString(request.Number, 10)
	if !ok || number.Sign() <= 0 {
		return fmt.Errorf("invalid offline CRL number")
	}
	var entries []x509.RevocationListEntry
	for _, item := range request.Entries {
		serial, ok := new(big.Int).SetString(item.Serial, 16)
		if !ok || serial.Sign() <= 0 {
			return fmt.Errorf("invalid offline revoked serial")
		}
		entries = append(entries, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: item.RevokedAt, ReasonCode: item.Reason})
	}
	der, err = x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: number, ThisUpdate: request.ThisUpdate, NextUpdate: request.NextUpdate, RevokedCertificateEntries: entries}, cert, key)
	if err != nil {
		return err
	}
	out, err := path("--out")
	if err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("offline output already exists or its private parent is unavailable")
	}
	file, err := os.OpenFile(filepath.Join(out, "revocations.pem"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func TestHistoricalRootCRLCandidatesUseOriginalOfflineCustodyOnce(t *testing.T) {
	f := newAcceptedCoreExecutionContractWithListeners(t, false, true)
	installManagedCoreNativeAuthenticationProtocol(t, f)
	calls := installHistoricalOfflineCRLSigner(t, f.store)
	for index, domain := range []string{"mqtt", "openbao_tls"} {
		t.Run(domain, func(t *testing.T) {
			id := "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
			if domain == "openbao_tls" {
				id = "734487b8-abcf-40be-a813-8b52c64f7954"
			}
			keys := map[string]*ecdsa.PrivateKey{}
			root := f.trustAuthorityWithID(t, domain, index+20, id, true, keys)
			record := historicalNativeSignedCRL(t, root, keys[root.ID])
			snapshot := createRootCRLNativeSnapshot{Prior: createRootCRLRecord{record.IssuerID, record.Digest, record.Number, record.PEM, record.ThisUpdate, record.NextUpdate}}
			material, err := createMQTTRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
			prepare := prepareMQTTRootCRLCandidate
			if domain == "openbao_tls" {
				material, err = createOpenBaoTLSRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
				prepare = prepareOpenBaoTLSRootCRLCandidate
			}
			if err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			count := calls.Load()
			candidate, request, signed, err := prepare(t.Context(), f.store, f.e, f.p.Plan.Bootstrap.Image, root, snapshot, material)
			if err != nil {
				t.Fatal("actual native offline candidate signing", err)
			}
			if signed.Number != "2" || !request.NextUpdate.Equal(request.ThisUpdate.Add(6*24*time.Hour)) || calls.Load() != count+1 {
				t.Fatal("offline signer did not produce one original-root bounded CRL")
			}
			again, requestAgain, signedAgain, err := prepare(t.Context(), f.store, f.e, f.p.Plan.Bootstrap.Image, root, snapshot, material)
			if err != nil || again != candidate || deploymentplan.Digest(requestAgain) != deploymentplan.Digest(request) || deploymentplan.Digest(signedAgain) != deploymentplan.Digest(signed) || calls.Load() != count+1 {
				t.Fatal("candidate retry changed original signature or repeated offline signing", err)
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, digest := range before {
				if strings.HasPrefix(path, "pki/") && after[path] != digest {
					t.Fatal("candidate replaced original authority custody", path)
				}
			}
			exerciseHistoricalRootCRLNativeImport(t, f, root, snapshot.Prior, signed, request)
			output := filepath.Join(f.store.Root, candidate, "signed/revocations.pem")
			if err := os.WriteFile(output, []byte("interrupted invalid output"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := prepare(t.Context(), f.store, f.e, f.p.Plan.Bootstrap.Image, root, snapshot, material); err == nil || calls.Load() != count+1 {
				t.Fatal("invalid retained output was replaced or granted authority", err)
			}
			if err := os.WriteFile(output, []byte(signed.PEM), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The connected MQTT and TLS repairs install this adapter twice. Each wrapper
// must keep its own signer and immutable predecessor, including for commands it
// does not handle; a mutable shared fallback can recurse into the first script.
func TestHistoricalOfflineCRLSignerReinstallationPreservesSignerAndFallback(t *testing.T) {
	f := newAcceptedCoreExecutionContractWithListeners(t, false, true)
	dir := t.TempDir()
	marker := filepath.Join(dir, "refused-docker-requests")
	predecessor := fmt.Sprintf("#!/usr/bin/env python3\nimport sys\nwith open(%q,'a') as out:out.write('native-refusal\\n')\nsys.stderr.write('original Docker predecessor refused unselected request\\n')\nsys.exit(39)\n", marker)
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(predecessor), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	first := installHistoricalOfflineCRLSigner(t, f.store)
	firstPath, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	var mqttRoot environmentCreateRegisteredIssuer
	var mqttSnapshot createRootCRLNativeSnapshot
	var mqttRequest createRootCRLRequest
	var mqttCandidate string
	var second *atomic.Int64
	for index, domain := range []string{"mqtt", "openbao_tls"} {
		if index == 1 {
			second = installHistoricalOfflineCRLSigner(t, f.store)
		}
		id := "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
		if domain == "openbao_tls" {
			id = "734487b8-abcf-40be-a813-8b52c64f7954"
		}
		keys := map[string]*ecdsa.PrivateKey{}
		root := f.trustAuthorityWithID(t, domain, index+20, id, true, keys)
		record := historicalNativeSignedCRL(t, root, keys[root.ID])
		snapshot := createRootCRLNativeSnapshot{Prior: createRootCRLRecord{record.IssuerID, record.Digest, record.Number, record.PEM, record.ThisUpdate, record.NextUpdate}}
		material, err := createMQTTRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
		prepare := prepareMQTTRootCRLCandidate
		if domain == "openbao_tls" {
			material, err = createOpenBaoTLSRootCRLMaterial(f.store, f.e.Journal.PlanSHA256, root)
			prepare = prepareOpenBaoTLSRootCRLCandidate
		}
		if err != nil {
			t.Fatal(err)
		}
		candidate, request, signed, err := prepare(t.Context(), f.store, f.e, f.p.Plan.Bootstrap.Image, root, snapshot, material)
		if err != nil || signed.Number != "2" || !request.NextUpdate.Equal(request.ThisUpdate.Add(6*24*time.Hour)) {
			t.Fatal("actual same-root offline signing after selected installation", domain, err)
		}
		if index == 0 {
			mqttRoot, mqttSnapshot, mqttRequest, mqttCandidate = root, snapshot, request, candidate
		}
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("paired native signing reached the wrong original endpoint", first.Load(), second.Load())
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	// Invoke the first wrapper again after the second has been installed. This
	// signs the same qualified request to a separate diagnostic output; neither
	// immutable candidate nor its existing output is changed or replayed.
	t.Run("first-signer-retained", func(t *testing.T) {
		t.Setenv("PATH", filepath.Dir(firstPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
		out := mqttCandidate + "/signer-reinstallation-check"
		err := runEnvironmentCreatePKICeremony(t.Context(), f.store, f.p.Plan.Bootstrap.Image, "crl", "--issuer", "/store/"+mqttCandidate+"/issuer.json", "--crl-request", "/store/"+mqttCandidate+"/request.json", "--key", "/store/pki/custody/roots/"+mqttRoot.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/mqtt-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(mqttRequest), "--out", "/store/"+out)
		if err != nil {
			t.Fatal("retained original signing endpoint", err)
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, out, "revocations.pem"), true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateCreateRootCRLSignedForDomain(mqttRoot, mqttSnapshot, mqttRequest, raw, "mqtt", time.Now().UTC()); err != nil {
			t.Fatal("retained signer changed original authority/request", err)
		}
		if first.Load() != 2 || second.Load() != 1 {
			t.Error("first wrapper was redirected to the later signer", first.Load(), second.Load())
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "original Docker predecessor refused unselected request") {
		t.Fatal("double-installed signer did not terminate at its refusing predecessor", ctx.Err(), err)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 39 || readTestFile(t, marker) != "native-refusal\n" {
		t.Fatal("unselected Docker request lost original refusal or reached it repeatedly")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, digest := range before {
		if after[path] != digest {
			t.Fatal("signer reinstallation replaced original custody or retained evidence", path)
		}
	}
}
