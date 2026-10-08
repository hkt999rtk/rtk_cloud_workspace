package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceInventoryRequiresExactBindingFreshSnapshotAndExplicitCounters(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "foreign-environment", "foreign-domain", "foreign-issuer", "foreign-root", "missing-time", "stale", "future", "stale-boundary", "future-boundary", "stale-beyond-boundary", "future-beyond-boundary", "missing-counter", "negative-counter", "pending", "unpublished", "missing-ack", "invalid", "empty-registry", "inconsistent-revoked", "counter-overflow"} {
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
				case "stale-boundary":
					values["checked_at"] = now.Add(-5 * time.Minute)
				case "future-boundary":
					values["checked_at"] = now.Add(5 * time.Second)
				case "stale-beyond-boundary":
					values["checked_at"] = now.Add(-5*time.Minute - time.Nanosecond)
				case "future-beyond-boundary":
					values["checked_at"] = now.Add(5*time.Second + time.Nanosecond)
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
				if (err == nil) != (mode == "valid" || mode == "stale-boundary" || mode == "future-boundary") {
					t.Fatal("unqualified Service inventory accepted", err)
				}
				if mode == "future" || mode == "future-beyond-boundary" || mode == "stale" || mode == "stale-beyond-boundary" {
					id := "deployment.pki.clock"
					if strings.HasPrefix(mode, "stale") {
						id = "deployment.pki.registry.stale"
					}
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != id || blocker.Resource != "Deployment/video-cloud-"+environment+"-video-cloud/pki-controller" || blocker.Next == "" {
						t.Fatal("registry freshness diagnosis lost its clock or resource scope", err)
					}
				}
			})
		}
	}
}

func TestLiveServiceClientRegistryPreservesSafeClockDiagnosisAndSuppressesCommandOutput(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "future", "stale", "foreign-root", "command-error", "malformed"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				issuer, root := "12345678-1234-1234-1234-123456789abc", strings.Repeat("a", 64)
				var deployments liveDeploymentList
				raw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": "pki-controller"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"env": []any{map[string]any{"name": "RTK_DEPLOYMENT_SERVICE_ISSUER_ID", "value": issuer}, map[string]any{"name": "PKI_SERVICE_CLIENT_ROOT_SHA256", "value": root}, map[string]any{"name": "PKI_REQUIRED_CONSUMERS_SERVICE", "value": "controller"}}}}}}}}}})
				if err := json.Unmarshal(raw, &deployments); err != nil {
					t.Fatal(err)
				}
				checked := time.Now().UTC()
				if mode == "future" || mode == "command-error" {
					checked = checked.Add(time.Minute)
				}
				if mode == "stale" {
					checked = checked.Add(-6 * time.Minute)
				}
				values := map[string]any{"status": "service-client-registry-inventory-checked", "environment": environment, "domain": "service", "issuer_id": issuer, "root_fingerprint": root, "checked_at": checked, "issuances": 2, "pending_issuances": 0, "invalid_records": 0, "revoked_certificates": 0, "unpublished_revocations": 0, "missing_acknowledgments": 0}
				if mode == "foreign-root" {
					values["root_fingerprint"] = "private-command-canary"
				}
				report, _ := json.Marshal(values)
				if mode == "malformed" {
					report = []byte("private-command-canary: malformed report")
				}
				file := filepath.Join(t.TempDir(), "inventory.json")
				if err := os.WriteFile(file, report, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("RTK_SERVICE_REGISTRY_REPORT", file)
				t.Setenv("RTK_SERVICE_REGISTRY_EXIT", "0")
				if mode == "command-error" {
					t.Setenv("RTK_SERVICE_REGISTRY_EXIT", "1")
				}
				deploymentRuntimeTestKubectl(t, `case "$*" in
  *'get secrets --all-namespaces -o json'*) printf '%s' '{"items":[]}' ;;
  *'-account-manager get deployments -o json'*) printf '%s' '{"items":[]}' ;;
  *'get deployments -o json'*) cat "$RTK_REGISTRY_DEPLOYMENTS" ;;
  *'get pods -l app.kubernetes.io/name=pki-controller -o json'*) printf '%s' '{"items":[{"metadata":{"name":"controller-fixture"}}]}' ;;
  *'get pods -l app.kubernetes.io/name=postgresql -o json'*) printf '%s' '{"items":[{"metadata":{"name":"postgresql-fixture"}}]}' ;;
  *'pki_deployment_bootstrap_sessions'*) printf '%s' '{"sessions":[],"pending_issuances":[],"pending_count":0,"active_caller_index":true,"legacy_caller_constraint":false}' ;;
  *'recovery-inventory-service-client'*) printf 'private-command-canary\n' >&2; cat "$RTK_SERVICE_REGISTRY_REPORT"; exit "$RTK_SERVICE_REGISTRY_EXIT" ;;
  *) exit 99 ;;
esac
`)
				err := verifyLiveServiceClientRegistry("fixture-kubeconfig", "video-cloud-"+environment+"-video-cloud", deployments)
				if (err == nil) != (mode == "valid") || err != nil && strings.Contains(err.Error(), "private-command-canary") {
					t.Fatal("registry boundary accepted invalid evidence or exposed private output", err)
				}
				var blocker *deploymentplan.Blocker
				if mode == "future" || mode == "stale" {
					id := "deployment.pki.clock"
					if mode == "stale" {
						id = "deployment.pki.registry.stale"
					}
					if !errors.As(err, &blocker) || blocker.ID != id || blocker.Resource != "Deployment/video-cloud-"+environment+"-video-cloud/pki-controller" || blocker.Next == "" {
						t.Fatal("public registry flow lost safe timestamp diagnosis", err)
					}
					store := makeIsolatedTestSecretStore(t, environment)
					if err := store.write("kube/kubeconfig.yaml", []byte("fixture-kubeconfig"), true); err != nil {
						t.Fatal(err)
					}
					deploymentFile := filepath.Join(t.TempDir(), "deployments.json")
					t.Setenv("RTK_REGISTRY_DEPLOYMENTS", deploymentFile)
					deployments.Items[0].Metadata.Generation = 1
					deployments.Items[0].Status.ObservedGeneration = 1
					deployments.Items[0].Status.UpdatedReplicas = 1
					for _, additionalFailure := range []bool{false, true} {
						deployments.Items[0].Status.AvailableReplicas = 1
						if additionalFailure {
							deployments.Items[0].Status.AvailableReplicas = 0
						}
						raw, _ := json.Marshal(deployments)
						if err := os.WriteFile(deploymentFile, raw, 0600); err != nil {
							t.Fatal(err)
						}
						aggregateErr := verifySecretStoreK8SRuntime(store, time.Now())
						var aggregateBlocker *deploymentplan.Blocker
						if !errors.As(aggregateErr, &aggregateBlocker) || aggregateBlocker.ID != id || strings.Contains(aggregateErr.Error(), "private-command-canary") || strings.Contains(aggregateErr.Error(), "not proven") != additionalFailure {
							t.Fatal("runtime aggregate lost the original typed diagnosis or other scoped failures", aggregateErr)
						}
					}
				} else if errors.As(err, &blocker) {
					t.Fatal("command failure or identity mismatch was misclassified as clock skew", err)
				}
			})
		}
	}
}
