package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestBillingKeyProjectionPreservesNativeAndHistoricalProfiles(t *testing.T) {
	seed := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 24)))
	native := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("n", 32)))
	for _, profile := range []string{"", environmentBillingRuntimeLegacyProfile} {
		got, err := environmentBillingPaymentReferenceKey(seed, profile)
		if err != nil || got != seed {
			t.Fatal("historical raw recipe changed")
		}
	}
	got, err := environmentBillingPaymentReferenceKey(native, environmentBillingRuntimeKeyProfile)
	if err != nil || got != native {
		t.Fatal("valid native key changed")
	}
	hash := sha256.Sum256([]byte(seed))
	want := base64.StdEncoding.EncodeToString(hash[:])
	got, err = environmentBillingPaymentReferenceKey(seed, environmentBillingRuntimeKeyProfile)
	if err != nil || got != want {
		t.Fatal("legacy deterministic projection differs")
	}
	again, err := environmentBillingPaymentReferenceKey(got, environmentBillingRuntimeKeyProfile)
	if err != nil || again != got {
		t.Fatal("native projection not idempotent")
	}
	for _, value := range []string{"", "short", strings.Repeat("x", 64), base64.RawURLEncoding.EncodeToString(make([]byte, 32)), seed + "\n"} {
		_, err := environmentBillingPaymentReferenceKey(value, environmentBillingRuntimeKeyProfile)
		var b *deploymentplan.Blocker
		if !errors.As(err, &b) || b.Resource != "runtime/payment-reference-encryption" {
			t.Fatal("unknown encoding admitted")
		}
	}
	if _, err := environmentBillingPaymentReferenceKey(native, "foreign"); err == nil {
		t.Fatal("foreign profile admitted")
	}
}
func TestBillingKeyGeneratorProducesNativeAES256(t *testing.T) {
	a, err := generateCatalogRuntimeSecret("payment-reference-encryption")
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateCatalogRuntimeSecret("payment-reference-encryption")
	if err != nil || a == b {
		t.Fatal("independent new key generation failed")
	}
	for _, key := range []string{a, b} {
		decoded, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(decoded) != 32 {
			t.Fatal("new key does not meet native AES256 contract")
		}
	}
}
func TestBillingKeyCorrectionChangesOnlyKeyAndChecksums(t *testing.T) {
	cfg, store, release, snapshot := billingConfigurationFixture(t, "dev")
	seed := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 24)))
	snapshot.Secrets["payment-reference-encryption"] = seed
	original := billingConfigurationOriginal(t, cfg, store, release, snapshot)
	before := deploymentplan.Digest(original)
	legacy, err := environmentBillingRuntimeCorrectionDefinitionsForProfile(cfg, store, release, snapshot, original, environmentBillingRuntimeLegacyProfile)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := environmentBillingRuntimeCorrectionDefinitionsForProfile(cfg, store, release, snapshot, original, environmentBillingRuntimeKeyProfile)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(original) != before {
		t.Fatal("original input mutated")
	}
	oldData, newData := billingRuntimeData(t, legacy), billingRuntimeData(t, fixed)
	if oldData["PAYMENT_REFERENCE_ENCRYPTION_KEY"] != seed || newData["PAYMENT_REFERENCE_ENCRYPTION_KEY"] == seed {
		t.Fatal("versioned raw/key projection missing")
	}
	for key, value := range oldData {
		if key != "PAYMENT_REFERENCE_ENCRYPTION_KEY" && newData[key] != value {
			t.Fatal("unrelated Billing data changed", key)
		}
	}
	if len(newData) != len(oldData) {
		t.Fatal("Billing data fields dropped")
	}
	for i := range legacy {
		a, b := deploymentplan.Clone(legacy[i].Object), deploymentplan.Clone(fixed[i].Object)
		if a["kind"] == "Secret" {
			deploymentplan.Map(b["data"])["PAYMENT_REFERENCE_ENCRYPTION_KEY"] = deploymentplan.Map(a["data"])["PAYMENT_REFERENCE_ENCRYPTION_KEY"]
		} else {
			ta := deploymentplan.Map(deploymentplan.Map(a["spec"])["template"])
			tb := deploymentplan.Map(deploymentplan.Map(b["spec"])["template"])
			deploymentplan.Map(deploymentplan.Map(tb["metadata"])["annotations"])["rtk.realtek.com/runtime-checksum"] = deploymentplan.Map(deploymentplan.Map(ta["metadata"])["annotations"])["rtk.realtek.com/runtime-checksum"]
		}
		if deploymentplan.Digest(a) != deploymentplan.Digest(b) {
			t.Fatal("non-key/non-checksum object changed", deploymentplan.ID(a))
		}
	}
	snapshot.Secrets["payment-reference-encryption"] = "unknown-invalid-key"
	defs, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
	var blocker *deploymentplan.Blocker
	if len(defs) != 0 || !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.billing" {
		t.Fatal("invalid key format escaped common preflight/render")
	}
}

func TestBillingKeyProjectedMirrorsVerifyAndSyncWithoutChangingSeed(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	seed := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 24)))
	key, err := environmentBillingPaymentReferenceKey(seed, environmentBillingRuntimeKeyProfile)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		value := "canonical"
		if entry.ID == "payment-reference-encryption" {
			value = seed
		}
		if err := store.write("runtime/"+entry.ID, []byte(value+"\n"), true); err != nil {
			t.Fatal(err)
		}
		for _, binding := range append(append([]secretK8SBinding(nil), entry.K8SBinding...), managedMQTTMirrorTestBindings(entry)...) {
			projected := value
			if entry.ID == "payment-reference-encryption" {
				projected = key
			}
			data[binding.Key] = base64.StdEncoding.EncodeToString([]byte(projected))
		}
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	payload, _ := json.Marshal(map[string]any{"data": data})
	response := filepath.Join(dir, "response.json")
	if err := os.WriteFile(response, payload, 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *'get deployment mqtt-pki'*) printf '%%s' '%s'; exit 0;; esac\ncat '%s'\n", managedMQTTMirrorTestConsumer(store.Environment), response)
	if err := os.WriteFile(kubectl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	if err := verifySecretStoreK8SBindings(store); err != nil {
		t.Fatal("projected live mirror rejected", err)
	}
	var out bytes.Buffer
	if err := syncMissingSecretBindings(&out, store, true); err != nil || out.Len() != 0 {
		t.Fatal("existing projected mirror incorrectly needs sync", err)
	}
	raw, err := store.readRuntime("payment-reference-encryption")
	if err != nil || raw != seed {
		t.Fatal("binding check/sync changed canonical seed")
	}
	for _, wrong := range []string{seed, base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		data["PAYMENT_REFERENCE_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString([]byte(wrong))
		payload, _ := json.Marshal(map[string]any{"data": data})
		os.WriteFile(response, payload, 0600)
		if err := verifySecretStoreK8SBindings(store); err == nil {
			t.Fatal("wrong derived/raw mirror passed health")
		}
		if err := syncMissingSecretBindings(&out, store, true); err == nil {
			t.Fatal("sync would accept wrong existing mirror")
		}
	}
}
