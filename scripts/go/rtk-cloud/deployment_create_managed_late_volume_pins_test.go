package main

import (
	"maps"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateVolumePinsFixture() map[string]string {
	materials := map[string]string{}
	for _, id := range []string{
		"PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-data",
		"PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-trust",
		"PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-host-identity",
		"PersistentVolumeClaim/video-cloud-dev-secrets/openbao-pki-identity",
		"PersistentVolumeClaim/video-cloud-dev-secrets/data-openbao-0",
		"PersistentVolumeClaim/video-cloud-dev-secrets/audit-openbao-0",
	} {
		materials["accepted-service-core-managed-partial-original-volume-"+id] = deploymentplan.Digest(id)
	}
	for _, id := range []string{
		"PersistentVolumeClaim/video-cloud-dev-video-cloud/video-cloud-api-app-pki-trust",
		"PersistentVolumeClaim/video-cloud-dev-video-cloud/video-cloud-api-pki-trust",
	} {
		materials["accepted-service-core-managed-application-new-trust-volume-"+id] = deploymentplan.Digest(id)
	}
	materials["accepted-service-core-managed-partial-original-volume-ledger"] = strings.Repeat("a", 64)
	materials["accepted-service-core-managed-partial-original-volume-ledger-active"] = strings.Repeat("b", 64)
	materials["accepted-service-core-managed-complete-current-native-volumes"] = strings.Repeat("c", 64)
	return materials
}

func TestManagedLateOriginalVolumePinsExcludesAggregateMetadataWithoutWeakeningPins(t *testing.T) {
	materials := managedLateVolumePinsFixture()
	before := deploymentplan.Digest(materials)
	legacy := 0
	for key := range materials {
		if strings.Contains(key, "original-volume-") || strings.Contains(key, "new-trust-volume-") {
			legacy++
		}
	}
	if legacy != 10 {
		t.Fatal("fixture did not reproduce original aggregate metadata false block")
	}
	pins, err := managedLateOriginalVolumePins(materials)
	if err != nil || len(pins) != 8 {
		t.Fatal("six original +two genuine trust allocations refused", err)
	}
	for key, value := range pins {
		if materials[key] != value || strings.HasSuffix(key, "-ledger") || strings.HasSuffix(key, "-ledger-active") {
			t.Fatal("metadata became a claimed volume or owner digest changed")
		}
	}
	if deploymentplan.Digest(materials) != before {
		t.Fatal("pin selection changed admitted/native evidence")
	}
	changed := maps.Clone(materials)
	key := "accepted-service-core-managed-partial-original-volume-PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-data"
	changed[key] = strings.Repeat("d", 64)
	observed, err := managedLateOriginalVolumePins(changed)
	if err != nil || observed[key] != changed[key] || deploymentplan.Digest(observed) == deploymentplan.Digest(pins) {
		t.Fatal("changed valid native owner hash vanished from proof")
	}
	// The all18 native aggregate is separate required evidence; it is not one
	// of these eight per-claim pins and must not be overwritten by selection.
	if changed["accepted-service-core-managed-complete-current-native-volumes"] != materials["accepted-service-core-managed-complete-current-native-volumes"] {
		t.Fatal("full native18 aggregate modified")
	}
}

func TestManagedLateOriginalVolumePinsRejectsMissingExtraAndMalformedClaims(t *testing.T) {
	host := "accepted-service-core-managed-partial-original-volume-PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-data"
	trust := "accepted-service-core-managed-application-new-trust-volume-PersistentVolumeClaim/video-cloud-dev-video-cloud/video-cloud-api-pki-trust"
	for _, mode := range []string{"nil", "missing-original", "missing-trust", "metadata-cannot-fill-two-originals", "extra-original", "extra-trust", "bad-host-sha", "empty-trust-sha", "host-no-namespace", "trust-no-name", "extra-id-segment", "wrong-kind", "metadata-only"} {
		t.Run(mode, func(t *testing.T) {
			materials := managedLateVolumePinsFixture()
			switch mode {
			case "nil":
				materials = nil
			case "missing-original":
				delete(materials, host)
			case "missing-trust":
				delete(materials, trust)
			case "metadata-cannot-fill-two-originals":
				delete(materials, host)
				delete(materials, "accepted-service-core-managed-partial-original-volume-PersistentVolumeClaim/video-cloud-dev-video-cloud/mqtt-pki-trust")
			case "extra-original":
				materials["accepted-service-core-managed-partial-original-volume-PersistentVolumeClaim/foreign/extra"] = strings.Repeat("e", 64)
			case "extra-trust":
				materials["accepted-service-core-managed-application-new-trust-volume-PersistentVolumeClaim/foreign/extra"] = strings.Repeat("e", 64)
			case "bad-host-sha":
				materials[host] = "changed-owner"
			case "empty-trust-sha":
				materials[trust] = ""
			case "host-no-namespace":
				materials["accepted-service-core-managed-partial-original-volume-PersistentVolumeClaim//mqtt-pki-data"] = materials[host]
				delete(materials, host)
			case "trust-no-name":
				materials["accepted-service-core-managed-application-new-trust-volume-PersistentVolumeClaim/video-cloud-dev-video-cloud/"] = materials[trust]
				delete(materials, trust)
			case "extra-id-segment":
				materials[host+"/foreign"] = materials[host]
				delete(materials, host)
			case "wrong-kind":
				materials[strings.Replace(host, "PersistentVolumeClaim/", "PersistentVolume/", 1)] = materials[host]
				delete(materials, host)
			case "metadata-only":
				for key := range materials {
					if strings.Contains(key, "PersistentVolumeClaim/") {
						delete(materials, key)
					}
				}
			}
			before := deploymentplan.Digest(materials)
			if _, err := managedLateOriginalVolumePins(materials); err == nil {
				t.Fatal("incomplete, extra or malformed claim proof admitted")
			}
			if deploymentplan.Digest(materials) != before {
				t.Fatal("refusal changed captured evidence")
			}
		})
	}
}
