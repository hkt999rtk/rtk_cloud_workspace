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

func TestLivePKICRLManifestRequiresExistingDevConsumers(t *testing.T) {
	accountSetting := "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST"
	serviceSetting := "CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST"
	openbaoSetting := "OPENBAO_SERVER_CRL_MANIFEST"
	for _, tc := range []struct {
		name, target, want string
		change             func(*liveDeploymentList)
	}{
		{"missing account deployment", "account-manager", "account-manager CRL consumer deployment is missing", func(list *liveDeploymentList) { list.Items = nil }},
		{"missing account sidecar", "account-manager", accountSetting + " CRL consumer container is missing", func(list *liveDeploymentList) { list.Items[0].Spec.Template.Spec.Containers = nil }},
		{"missing account setting", "account-manager", accountSetting + " requires exactly one", func(list *liveDeploymentList) { list.Items[0].Spec.Template.Spec.Containers[0].Env = nil }},
		{"empty account path", "account-manager", accountSetting + " CRL manifest path is missing", func(list *liveDeploymentList) { list.Items[0].Spec.Template.Spec.Containers[0].Env[0].Value = "" }},
		{"missing certissuer deployment", "certissuer", "certissuer CRL consumer deployment is missing", func(list *liveDeploymentList) { list.Items = nil }},
		{"missing certissuer container", "certissuer", serviceSetting + " CRL consumer container is missing", func(list *liveDeploymentList) { list.Items[0].Spec.Template.Spec.Containers = nil }},
		{"missing service CRL setting", "certissuer", serviceSetting + " requires exactly one", func(list *liveDeploymentList) {
			list.Items[0].Spec.Template.Spec.Containers[0].Env = list.Items[0].Spec.Template.Spec.Containers[0].Env[1:]
		}},
		{"missing OpenBao CRL setting", "certissuer", openbaoSetting + " requires exactly one", func(list *liveDeploymentList) {
			list.Items[0].Spec.Template.Spec.Containers[0].Env = list.Items[0].Spec.Template.Spec.Containers[0].Env[:1]
		}},
		{"empty OpenBao CRL path", "certissuer", openbaoSetting + " CRL manifest path is missing", func(list *liveDeploymentList) { list.Items[0].Spec.Template.Spec.Containers[0].Env[1].Value = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container, setting := "pkimanagement", accountSetting
			if tc.target == "certissuer" {
				container, setting = "certissuer", serviceSetting
			}
			deployment := testLiveCRLDeployment(t, tc.target, container, setting, true)
			if tc.target == "certissuer" {
				second := deployment.Spec.Template.Spec.Containers[0].Env[0]
				second.Name = openbaoSetting
				deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, second)
			}
			list := liveDeploymentList{Items: []liveDeployment{deployment}}
			tc.change(&list)
			if err := verifyMountedPKICRLManifests("kubeconfig", "namespace", "dev", list, tc.target); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("missing dev CRL consumer error = %v, want %q", err, tc.want)
			}
		})
	}
	for _, target := range []string{"account-manager", "certissuer"} {
		if err := verifyMountedPKICRLManifests("kubeconfig", "namespace", "staging", liveDeploymentList{}, target); err != nil {
			t.Fatalf("unadopted staging %s must remain optional: %v", target, err)
		}
	}
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

func TestLivePKICRLManifestRejectsUntrustedMountAndIssuerSources(t *testing.T) {
	deployment := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	checkMount := func(name string, current liveDeployment, container, path, want string) {
		t.Helper()
		if _, _, err := mountedPKICRLConfigMap(current, container, path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: mount error = %v, want %q", name, err, want)
		}
	}
	checkMount("noncanonical path", deployment, "pkimanagement", "/run/service-crls/../service-crls/crls.json", "absolute crls.json")
	checkMount("wrong container", deployment, "other", "/run/service-crls/crls.json", "no matching")
	missingMount := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	missingMount.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath = "/run/other"
	checkMount("wrong mount", missingMount, "pkimanagement", "/run/service-crls/crls.json", "no matching")
	withoutConfigMap := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	withoutConfigMap.Spec.Template.Spec.Volumes[0].ConfigMap.Name = ""
	checkMount("non-ConfigMap volume", withoutConfigMap, "pkimanagement", "/run/service-crls/crls.json", "not backed by a ConfigMap")

	fromSecret := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	fromSecret.Spec.Template.Spec.Containers[0].Env[0].Value = ""
	fromSecret.Spec.Template.Spec.Containers[0].Env[0].ValueFrom = map[string]any{"secretKeyRef": map[string]string{"name": "path"}}
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{fromSecret}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "must be explicit") {
		t.Fatalf("Secret-derived CRL manifest path error = %v", err)
	}

	valid, _, _ := testLiveCRLEvidence(t, "service", false)
	if err := validateMountedPKICRLManifest("", "dev", "service", deployment, "pkimanagement"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing manifest error = %v", err)
	}
	mutateManifest := func(change func(map[string]any)) string {
		t.Helper()
		var entries []map[string]any
		if err := json.Unmarshal([]byte(valid), &entries); err != nil {
			t.Fatal(err)
		}
		change(entries[0]["issuer"].(map[string]any))
		raw, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	invalidCertificate := mutateManifest(func(issuer map[string]any) { issuer["certificate_pem"] = "not a certificate" })
	if err := validateMountedPKICRLManifest(invalidCertificate, "dev", "service", deployment, "pkimanagement"); err == nil || !strings.Contains(err.Error(), "certificate is invalid") {
		t.Fatalf("invalid issuer certificate error = %v", err)
	}
	wrongFingerprint := mutateManifest(func(issuer map[string]any) { issuer["certificate_fingerprint_sha256"] = strings.Repeat("0", 64) })
	if err := validateMountedPKICRLManifest(wrongFingerprint, "dev", "service", deployment, "pkimanagement"); err == nil || !strings.Contains(err.Error(), "fingerprint differs") {
		t.Fatalf("wrong issuer fingerprint error = %v", err)
	}
}

func TestLivePKICRLManifestChecksConfigMapBeforeAcceptance(t *testing.T) {
	deployment := testLiveCRLDeployment(t, "account-manager", "pkimanagement", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", true)
	manifest, state, registry := testLiveCRLEvidence(t, "service", false)
	writeSource := func(content, stateContent, registryContent string, podContents ...string) {
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
		pods := `{"items":[{"metadata":{"name":"postgresql-0"}}]}`
		if len(podContents) > 0 {
			pods = podContents[0]
		}
		for path, data := range map[string]string{stateFile: stateContent, registryFile: registryContent, podsFile: pods} {
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
	mutateState := func(change func(map[string]any)) string {
		t.Helper()
		var current map[string]any
		if err := json.Unmarshal([]byte(state), &current); err != nil {
			t.Fatal(err)
		}
		change(current)
		raw, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	wrongIssuerState := mutateState(func(current map[string]any) { current["issuer_fingerprint"] = strings.Repeat("0", 64) })
	writeSource(manifest, wrongIssuerState, registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "pinned issuer") {
		t.Fatalf("wrong PVC issuer error = %v", err)
	}
	wrongNumberState := mutateState(func(current map[string]any) { current["crl"].(map[string]any)["crl_number"] = "2" })
	writeSource(manifest, wrongNumberState, registry)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "metadata differs") {
		t.Fatalf("wrong signed CRL number error = %v", err)
	}
	writeSource(manifest, state, registry, `{"items":[]}`)
	if err := verifyMountedPKICRLManifests("kubeconfig", "video-cloud-dev-account-manager", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "account-manager"); err == nil || !strings.Contains(err.Error(), "PostgreSQL Pod") {
		t.Fatalf("missing PostgreSQL receipt source error = %v", err)
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
