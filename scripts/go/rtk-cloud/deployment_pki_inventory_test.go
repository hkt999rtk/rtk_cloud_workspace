package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestServiceInventoryRequiresExactBindingFreshSnapshotAndExplicitCounters(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "foreign-environment", "foreign-domain", "foreign-issuer", "foreign-root", "missing-time", "stale", "future", "missing-counter", "negative-counter", "pending", "unpublished", "missing-ack", "invalid", "empty-registry", "inconsistent-revoked", "counter-overflow"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				now := time.Now().UTC()
				issuer, root := "12345678-1234-1234-1234-123456789abc", strings.Repeat("a", 64)
				values := map[string]any{"status": "service-client-registry-inventory-checked", "environment": environment, "domain": "service", "issuer_id": issuer, "root_fingerprint": root, "checked_at": now, "issuances": 2, "pending_issuances": 0, "invalid_records": 0, "revoked_certificates": 1, "unpublished_revocations": 0, "missing_acknowledgments": 0}
				switch mode {
				case "foreign-environment":
					values["environment"] = "other"
				case "foreign-domain":
					values["domain"] = "mqtt"
				case "foreign-issuer":
					values["issuer_id"] = "22345678-1234-1234-1234-123456789abc"
				case "foreign-root":
					values["root_fingerprint"] = strings.Repeat("b", 64)
				case "missing-time":
					delete(values, "checked_at")
				case "stale":
					values["checked_at"] = now.Add(-6 * time.Minute)
				case "future":
					values["checked_at"] = now.Add(time.Minute)
				case "missing-counter":
					delete(values, "missing_acknowledgments")
				case "negative-counter":
					values["invalid_records"] = -1
				case "pending":
					values["pending_issuances"] = 1
				case "unpublished":
					values["unpublished_revocations"] = 1
				case "missing-ack":
					values["missing_acknowledgments"] = 1
				case "invalid":
					values["invalid_records"] = 1
				case "empty-registry":
					values["issuances"], values["revoked_certificates"] = 0, 0
				case "inconsistent-revoked":
					values["revoked_certificates"] = 3
				case "counter-overflow":
					values["pending_issuances"], values["invalid_records"], values["unpublished_revocations"] = int(^uint(0)>>1), int(^uint(0)>>1), 2
				}
				raw, _ := json.Marshal(values)
				var report serviceClientInventoryReport
				if err := decodeEnvironmentJSON(raw, &report); err != nil {
					t.Fatal(err)
				}
				err := validateServiceClientInventoryReport(report, "video-cloud-"+environment+"-video-cloud", issuer, root, now)
				if (err == nil) != (mode == "valid") {
					t.Fatal("unqualified Service inventory accepted", err)
				}
			})
		}
	}
}
