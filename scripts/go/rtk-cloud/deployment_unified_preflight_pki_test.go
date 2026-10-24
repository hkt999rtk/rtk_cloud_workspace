package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPreflightPKIPrerequisitesDoNotRequireRunningWorkload(t *testing.T) {
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		for _, action := range []string{"update", "retain"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				r := newDeploymentCheckRuntime(context.Background(), "staging")
				id := "Deployment/video-cloud-staging-video-cloud/" + name
				r.preflightPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "staging", Stack: "video-cloud-staging", Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: id, Action: action, UID: "original"}}}
				var d liveDeployment
				d.Metadata.Name, d.Metadata.UID = name, "original"
				if err := r.checkPKIWorkloadPhase("video-cloud-staging-video-cloud", d); err != nil {
					t.Fatal("stopped workload blocked candidate prerequisites", err)
				}
				if ids := r.pendingRolloutHealth(); len(ids) != 1 || ids[0] != id {
					t.Fatal("pending rollout health was not reported")
				}
				d.Metadata.UID = "foreign"
				if err := r.checkPKIWorkloadPhase("video-cloud-staging-video-cloud", d); err == nil {
					t.Fatal("preflight accepted a changed original owner")
				}
			})
		}
	}
}

func TestPreflightPKIStillValidatesCertificatesAndHealthStillRequiresReadiness(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := store.write("kube/kubeconfig.yaml", []byte("isolated"), true); err != nil {
		t.Fatal(err)
	}
	id := "Deployment/video-cloud-staging-video-cloud/video-cloud-logingester"
	d := deploymentplan.Object{"metadata": deploymentplan.Object{"name": "video-cloud-logingester", "uid": "original"}, "spec": deploymentplan.Object{"replicas": 1, "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "value": strings.Repeat("a", 64)}}}}}}}}
	deployments, _ := json.Marshal(deploymentplan.Object{"items": []any{d}})
	for _, mode := range []string{"preflight", "health", "bad-certificate", "foreign-owner", "unplanned"} {
		t.Run(mode, func(t *testing.T) {
			secrets := []byte(`{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"isolated-fixture"},"data":{"value":"aXNvbGF0ZWQ="}}]}`)
			if mode == "bad-certificate" {
				secrets = []byte(`{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"original-tls"},"data":{"tls.crt":"invalid","tls.key":"invalid"}}]}`)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", writeSecretRuntimeKubectl(t, secrets, deployments))
			r := newDeploymentCheckRuntime(context.Background(), "staging")
			if mode != "health" {
				r.preflightPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "staging", Stack: "video-cloud-staging", Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: id, Action: "update", UID: "original"}}}
			}
			if mode == "foreign-owner" {
				r.preflightPlan.Resources[0].UID = "foreign"
			}
			if mode == "unplanned" {
				r.preflightPlan.Resources = nil
			}
			store.checkRuntime = r
			err := verifySecretStoreK8SRuntime(store, time.Now())
			if (err == nil) != (mode == "preflight") {
				t.Fatalf("incorrect phase or certificate validation: %v", err)
			}
		})
	}
}

func TestPreflightServiceRegistryDefersOnlyExecutionDependentAcknowledgments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"preflight", "retain", "health", "foreign-owner", "unplanned", "unknown-consumer", "foreign-root", "stale", "invalid", "pending-issuance", "unpublished-revocation", "incomplete", "excess-acks", "preflight-account-manager", "account-manager-foreign-owner", "account-manager-read-denied"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				issuer, root := "12345678-1234-1234-1234-123456789abc", strings.Repeat("a", 64)
				namespace := "video-cloud-" + environment + "-video-cloud"
				consumer, owner := "video-cloud-api", "video-cloud-api-pki"
				ownerNamespace := namespace
				if strings.Contains(mode, "account-manager") {
					consumer, owner, ownerNamespace = "account-manager", "account-manager", "video-cloud-"+environment+"-account-manager"
				}
				if mode == "unknown-consumer" {
					consumer = "unreviewed-consumer"
				}
				var deployments liveDeploymentList
				raw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": "pki-controller"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"env": []any{map[string]any{"name": "RTK_DEPLOYMENT_SERVICE_ISSUER_ID", "value": issuer}, map[string]any{"name": "PKI_SERVICE_CLIENT_ROOT_SHA256", "value": root}, map[string]any{"name": "PKI_REQUIRED_CONSUMERS_SERVICE", "value": consumer}}}}}}}}, map[string]any{"metadata": map[string]any{"name": owner, "uid": "original"}}}})
				if err := json.Unmarshal(raw, &deployments); err != nil {
					t.Fatal(err)
				}
				values := map[string]any{"status": "service-client-registry-inventory-blocked", "environment": environment, "domain": "service", "issuer_id": issuer, "root_fingerprint": root, "checked_at": time.Now().UTC(), "issuances": 2, "pending_issuances": 0, "invalid_records": 0, "revoked_certificates": 0, "unpublished_revocations": 0, "missing_acknowledgments": 2}
				switch mode {
				case "foreign-root":
					values["root_fingerprint"] = strings.Repeat("b", 64)
				case "stale":
					values["checked_at"] = time.Now().UTC().Add(-6 * time.Minute)
				case "invalid":
					values["invalid_records"] = 1
				case "pending-issuance":
					values["pending_issuances"] = 1
				case "unpublished-revocation":
					values["unpublished_revocations"] = 1
				case "incomplete":
					values["status"] = "service-client-registry-inventory-incomplete"
				case "excess-acks":
					values["missing_acknowledgments"] = 3
				}
				report, _ := json.Marshal(values)
				accountRead := "printf '%s' '{\"items\":[{\"metadata\":{\"name\":\"account-manager\",\"uid\":\"original\"}}]}'"
				if mode == "account-manager-foreign-owner" {
					accountRead = strings.ReplaceAll(accountRead, "original", "foreign")
				}
				if mode == "account-manager-read-denied" {
					accountRead = "printf 'Forbidden private-error-canary' >&2; exit 1"
				}
				deploymentRuntimeTestKubectl(t, fmt.Sprintf("case \"$*\" in\n *'get pods'*) printf '%%s' '{\"items\":[{\"metadata\":{\"name\":\"original-controller-pod\"}}]}' ;;\n *'get deployments'*) %s ;;\n *'recovery-inventory-service-client'*) printf 'private-error-canary' >&2; printf '%%s' '%s'; exit 1 ;;\n *) exit 99 ;;\nesac", accountRead, report))
				r := newDeploymentCheckRuntime(context.Background(), environment)
				resource := deploymentplan.Resource{ID: "Deployment/" + ownerNamespace + "/" + owner, Action: "update", UID: "original"}
				if mode == "retain" {
					resource.Action = "retain"
				}
				if mode == "foreign-owner" {
					resource.UID = "foreign"
				}
				if mode != "health" {
					r.preflightPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}, Resources: []deploymentplan.Resource{resource}}
					if mode == "unplanned" {
						r.preflightPlan.Resources = nil
					}
				}
				err := verifyLiveServiceClientRegistry("isolated-fixture", namespace, deployments, r)
				pass := mode == "preflight" || mode == "retain" || mode == "preflight-account-manager"
				if (err == nil) != pass || err != nil && strings.Contains(err.Error(), "private-error-canary") {
					t.Fatal("registry phase boundary or evidence validation failed", err)
				}
				if pass && (len(r.pendingRolloutHealth()) != 1 || r.pendingRolloutHealth()[0] != resource.ID) {
					t.Fatal("deferred acknowledgment has no exact planned rollout acceptance")
				}
			})
		}
	}
}

func TestPreflightServiceRegistryKeepsOriginalInventoryAcrossNamespaces(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			namespace := "video-cloud-" + environment + "-video-cloud"
			var deployments liveDeploymentList
			if err := json.Unmarshal([]byte(`{"items":[{"metadata":{"name":"certissuer","uid":"original-certissuer"}}]}`), &deployments); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(deployments)
			deploymentRuntimeTestKubectl(t, `printf '%s' '{"items":[{"metadata":{"name":"account-manager","uid":"original-account-manager"}}]}'`)
			r := newDeploymentCheckRuntime(context.Background(), environment)
			r.preflightPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}, Resources: []deploymentplan.Resource{
				{ID: "Deployment/video-cloud-" + environment + "-account-manager/account-manager", UID: "original-account-manager", Action: "update"},
				{ID: "Deployment/" + namespace + "/certissuer", UID: "original-certissuer", Action: "update"},
			}}
			zero, one := 0, 1
			issuer, root := "12345678-1234-1234-1234-123456789abc", strings.Repeat("a", 64)
			report := serviceClientInventoryReport{Status: "service-client-registry-inventory-blocked", Environment: environment, Domain: "service", IssuerID: issuer, RootFingerprint: root, CheckedAt: time.Now().UTC(), Issuances: &one, Pending: &zero, Invalid: &zero, Revoked: &zero, Unpublished: &zero, MissingAcknowledgments: &one}
			deferred, err := r.deferServiceRegistryAcknowledgments(report, "isolated-fixture", namespace, issuer, root, "account-manager,certissuer", deployments)
			after, _ := json.Marshal(deployments)
			if !deferred || err != nil || string(after) != string(before) || len(r.pendingRolloutHealth()) != 2 {
				t.Fatal("reading Account Manager ownership changed the original CertIssuer inventory", deferred, err, string(after))
			}
		})
	}
}
