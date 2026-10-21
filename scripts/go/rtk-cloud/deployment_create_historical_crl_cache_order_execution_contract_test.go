package main

import (
	"crypto/ecdsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalTLSCRLCacheRequiresActualCoreManifestPublication(t *testing.T) {
	core := newCoreExecutionContractAtBoundary(t, false, true, false, historicalCompleteDomainPolicy(t))
	core.runtime.OpenBao.CACertPath = filepath.Join(core.store.Root, "deployment/create/material/cache-order-openbao-ca.crt")
	if err := core.store.write("deployment/create/material/cache-order-openbao-ca.crt", []byte(core.runtime.TransportCAPEM), false); err != nil {
		t.Fatal(err)
	}
	keys := map[string]*ecdsa.PrivateKey{}
	ready := core.trustAuthority(t, "openbao_tls", 92, false, keys)
	root, ica := core.activateHistoricalTrustAuthority(t, ready, 92, keys)
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{"openbao_tls": root}, map[string]environmentCreateRegisteredIssuer{"openbao_tls": ica}
	f := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: &postServiceMQTTExecutionContract{acceptedCoreExecutionContract: core}, authorityKeys: keys}
	rootCRL, icaCRL := historicalNativeSignedCRL(t, root, keys[root.ID]), historicalNativeSignedCRL(t, ica, keys[ica.ID], 3)
	core.protocol["historical_crls"] = map[string]managedLateCRLRecord{root.ID: rootCRL, ica.ID: icaCRL}
	core.protocol["historical_crl_history"] = map[string][]managedLateCRLRecord{root.ID: {rootCRL}, ica.ID: {icaCRL}}
	core.protocol["historical_crl_caches"] = map[string]map[string]any{}
	core.protocol["historical_crl_acks"] = []managedLatePriorACK{}
	prepared, err := renderEnvironmentCreatePreparedOpenBaoConsumers(t.Context(), core.cfg, core.p.Plan.Bootstrap, core.runtime, core.service, root)
	if err != nil {
		t.Fatal(err)
	}
	core.acceptedHistoricalStage(t, "cache-order-prepared-openbao", prepared)
	cmID := "ConfigMap/" + core.p.Plan.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	before := deploymentplan.Clone(core.native.objects[cmID])
	cacheDir := t.TempDir()
	capture := func() error {
		return writeHistoricalPriorCRLConsumerCaches(f, roots, intermediates, cacheDir, []string{"openbao_tls"})
	}
	if err := capture(); err == nil || !strings.Contains(err.Error(), "native original TLS manifest differs") {
		t.Fatal("root-only publication must refuse before any native CRL receipt", err)
	}
	if len(core.protocol["historical_crl_caches"].(map[string]map[string]any)) != 0 || len(core.protocol["historical_crl_acks"].([]managedLatePriorACK)) != 0 {
		t.Fatal("Root-only consumer published a false Root-plus-ICA acknowledgment")
	}
	files, err := os.ReadDir(cacheDir)
	if err != nil || len(files) != 0 {
		t.Fatal("refused consumer wrote a native cache", err)
	}
	managed, err := applyEnvironmentCreateManagedOpenBaoConsumers(core.p.Plan.Bootstrap, core.service, roots, intermediates, prepared)
	if err != nil {
		t.Fatal(err)
	}
	plan := core.acceptedHistoricalStage(t, "cache-order-managed-core", managed)
	found := false
	for _, resource := range plan.Resources {
		if resource.ID == cmID {
			found = true
			if resource.Action != "update" || resource.UID != deploymentplan.Map(before["metadata"])["uid"] || !deploymentplan.Matches(resource.Baseline, before) {
				t.Fatalf("Core did not update the retained original trust owner: action=%s uid=%s original=%s", resource.Action, resource.UID, deploymentplan.String(deploymentplan.Map(before["metadata"])["uid"]))
			}
		}
	}
	if !found || validateOpenBaoTLSRootCRLManifest(deploymentplan.Resource{Desired: deploymentplan.Projection(core.native.objects[cmID])}, root, ica) != nil {
		t.Fatal("actual Core did not publish the complete Root-plus-ICA manifest")
	}
	if err := capture(); err != nil {
		t.Fatal("current Core publication cannot install original signed CRLs", err)
	}
	caches := core.protocol["historical_crl_caches"].(map[string]map[string]any)
	acks := core.protocol["historical_crl_acks"].([]managedLatePriorACK)
	if len(caches) != 4 || len(acks) != 4 {
		t.Fatalf("both actual TLS consumers must install Root+ICA: caches=%d ACKs=%d", len(caches), len(acks))
	}
	for _, cache := range caches {
		var body struct {
			IssuerFingerprint string               `json:"issuer_fingerprint"`
			CRL               managedLateCRLRecord `json:"crl"`
		}
		if readExecutionJSON(cache["file"].(string), &body) != nil || body.CRL.Digest != cache["digest"] {
			t.Fatal("native cache lacks exact signed readback")
		}
	}
	original := deploymentplan.Clone(core.native.objects[cmID])
	beforeACKs := deploymentplan.Digest(acks)
	deploymentplan.Map(core.native.objects[cmID]["data"])["crls.json"] = "[]"
	if err := capture(); err == nil || !strings.Contains(err.Error(), "manifest differs") {
		t.Fatal("later manifest drift was hidden by existing caches", err)
	}
	if deploymentplan.Digest(core.protocol["historical_crl_acks"]) != beforeACKs || len(caches) != 4 {
		t.Fatal("drift changed existing original receipts")
	}
	core.native.objects[cmID] = original
	if err := capture(); err != nil || deploymentplan.Digest(core.protocol["historical_crl_acks"]) != beforeACKs {
		t.Fatal("unchanged native observation duplicated old ACKs", err)
	}
	successor := publishHistoricalOpenBaoTLSICASuccessor(t, f, ica)
	history := core.protocol["historical_crl_history"].(map[string][]managedLateCRLRecord)[ica.ID]
	if len(history) != 2 || history[0].Number != "3" || history[1].Number != "4" || history[1].Digest != successor.Digest || core.protocol["historical_crls"].(map[string]managedLateCRLRecord)[ica.ID].Digest != successor.Digest {
		t.Fatal("automatic same-key ICA successor did not advance native registry history")
	}
	updatedCaches := 0
	for _, cache := range caches {
		var body struct {
			IssuerFingerprint string              `json:"issuer_fingerprint"`
			CRL               createRootCRLRecord `json:"crl"`
		}
		raw, readErr := os.ReadFile(cache["file"].(string))
		if readErr == nil && decodeEnvironmentJSON(raw, &body) == nil && body.CRL.IssuerID == ica.ID {
			if body.CRL.Digest != successor.Digest || cache["digest"] != successor.Digest {
				t.Fatal("automatic same-key ICA successor did not update exact durable cache")
			}
			updatedCaches++
		}
	}
	updatedACKs := 0
	for _, ack := range core.protocol["historical_crl_acks"].([]managedLatePriorACK) {
		if ack.IssuerID == ica.ID {
			if ack.Digest != successor.Digest {
				t.Fatal("automatic same-key ICA successor did not update exact acknowledgment")
			}
			updatedACKs++
		}
	}
	if updatedCaches != 2 || updatedACKs != 2 {
		t.Fatalf("automatic same-key ICA successor requires two caches and ACKs: caches=%d ACKs=%d", updatedCaches, updatedACKs)
	}
}
