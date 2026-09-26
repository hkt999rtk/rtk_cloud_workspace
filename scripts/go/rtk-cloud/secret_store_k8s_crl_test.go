package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLiveCRLEvidence(t *testing.T, domain string, expired bool) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test " + domain + " CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(14 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3, 4}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(der)
	issuerID := "64062c4a-2bdc-474c-bb11-ab1b61026574"
	thisUpdate, nextUpdate := now.Add(-time.Minute), now.Add(time.Hour)
	if expired {
		thisUpdate, nextUpdate = now.Add(-2*time.Hour), now.Add(-time.Hour)
	}
	crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: thisUpdate, NextUpdate: nextUpdate}, template, key)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := x509.ParseRevocationList(crlDER)
	if err != nil {
		t.Fatal(err)
	}
	crlSum := sha256.Sum256(crlDER)
	entry := []map[string]any{{
		"issuer":     map[string]string{"issuer_id": issuerID, "environment": "dev", "trust_domain": domain, "status": "active", "certificate_fingerprint_sha256": hex.EncodeToString(fingerprint[:]), "certificate_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))},
		"state_path": "/var/lib/pki-state/crls/issuer.json",
	}}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	stateRaw, err := json.Marshal(map[string]any{"issuer_fingerprint": hex.EncodeToString(fingerprint[:]), "crl": map[string]any{"issuer_id": issuerID, "crl_sha256": hex.EncodeToString(crlSum[:]), "crl_number": crl.Number.String(), "crl_pem": string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER})), "this_update": crl.ThisUpdate, "next_update": crl.NextUpdate}})
	if err != nil {
		t.Fatal(err)
	}
	registryRaw, err := json.Marshal(map[string]any{"digest": hex.EncodeToString(crlSum[:]), "number": crl.Number.String(), "ack": true})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), string(stateRaw), string(registryRaw)
}

func testLiveCRLDeployment(t *testing.T, target, container, setting string, readOnly bool) liveDeployment {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": target},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": container, "env": []any{map[string]string{"name": setting, "value": "/run/service-crls/crls.json"}}, "volumeMounts": []any{map[string]any{"name": "crl-manifest", "mountPath": "/run/service-crls", "readOnly": readOnly}, map[string]any{"name": "state", "mountPath": "/var/lib/pki-state"}}}},
			"volumes":    []any{map[string]any{"name": "crl-manifest", "configMap": map[string]string{"name": "service-crls-v2"}}, map[string]any{"name": "state", "persistentVolumeClaim": map[string]string{"claimName": "pki-state"}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deployment liveDeployment
	if err := json.Unmarshal(raw, &deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestLivePKICRLManifestRequiresReviewedMountAndPVCState(t *testing.T) {
	deployment := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	name, key, err := mountedPKICRLConfigMap(deployment, "pkimanagement", "/run/service-crls/crls.json")
	if err != nil || name != "service-crls-v2" || key != "crls.json" {
		t.Fatalf("mount = %q %q, %v", name, key, err)
	}
	valid, _, _ := testLiveCRLEvidence(t, "service", false)
	if err := validateMountedPKICRLManifest(valid, "dev", "service", deployment, "pkimanagement"); err != nil {
		t.Fatal(err)
	}
	if err := validateMountedPKICRLManifest(valid, "staging", "service", deployment, "pkimanagement"); err == nil {
		t.Fatal("accepted wrong environment")
	}
	if err := validateMountedPKICRLManifest(valid, "dev", "openbao_tls", deployment, "pkimanagement"); err == nil {
		t.Fatal("accepted wrong trust domain")
	}
	withoutPVC := deployment
	withoutPVC.Spec.Template.Spec.Volumes = withoutPVC.Spec.Template.Spec.Volumes[:1]
	if err := validateMountedPKICRLManifest(valid, "dev", "service", withoutPVC, "pkimanagement"); err == nil {
		t.Fatal("accepted CRL state without PVC")
	}
	unmounted := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", false)
	if _, _, err := mountedPKICRLConfigMap(unmounted, "pkimanagement", "/run/service-crls/crls.json"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("mutable CRL mount error = %v", err)
	}
}

func TestLivePKICRLManifestChecksConfigMapBeforeAcceptance(t *testing.T) {
	deployment := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	manifest, state, registry := testLiveCRLEvidence(t, "service", false)
	writeSource := func(content, stateContent, registryContent string) {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{"data": map[string]string{"crls.json": content}})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		fixture := filepath.Join(dir, "configmap.json")
		stateFile := filepath.Join(dir, "state.json")
		registryFile := filepath.Join(dir, "registry.json")
		podsFile := filepath.Join(dir, "pods.json")
		kubectl := filepath.Join(dir, "kubectl")
		if err := os.WriteFile(fixture, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		for path, data := range map[string]string{stateFile: stateContent, registryFile: registryContent, podsFile: `{"items":[{"metadata":{"name":"postgresql-0"}}]}`} {
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		script := "#!/bin/sh\ncase \"$*\" in\n  *'get configmap'*) cat '" + fixture + "';;\n  *'get pods'*) cat '" + podsFile + "';;\n  *'exec deployment/account-manager'*) cat '" + stateFile + "';;\n  *'exec postgresql-0'*) cat '" + registryFile + "';;\n  *) exit 1;;\nesac\n"
		if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	}
	writeSource(manifest, state, registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err != nil {
		t.Fatal(err)
	}
	writeSource(`[]`, state, registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("empty ConfigMap manifest error = %v", err)
	}
	writeSource(manifest, ``, registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "state is unreadable") {
		t.Fatalf("empty installed state error = %v", err)
	}
	writeSource(manifest, state, `{"digest":"wrong","number":"1","ack":true}`)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "latest acknowledged") {
		t.Fatalf("registry mismatch error = %v", err)
	}
	writeSource(manifest, state, strings.Replace(registry, `"ack":true`, `"ack":false`, 1))
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "latest acknowledged") {
		t.Fatalf("missing receipt error = %v", err)
	}
	expiredManifest, expiredState, expiredRegistry := testLiveCRLEvidence(t, "service", true)
	writeSource(expiredManifest, expiredState, expiredRegistry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "freshness") {
		t.Fatalf("expired signed CRL error = %v", err)
	}
	var altered map[string]any
	if err := json.Unmarshal([]byte(state), &altered); err != nil {
		t.Fatal(err)
	}
	crlState := altered["crl"].(map[string]any)
	block, _ := pem.Decode([]byte(crlState["crl_pem"].(string)))
	block.Bytes[len(block.Bytes)-1] ^= 1
	crlState["crl_pem"] = string(pem.EncodeToMemory(block))
	alteredRaw, err := json.Marshal(altered)
	if err != nil {
		t.Fatal(err)
	}
	writeSource(manifest, string(alteredRaw), registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("altered signed CRL error = %v", err)
	}
}
