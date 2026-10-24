package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCommonBootstrapCleanupRendererRestrictsEveryEnvironmentToRecordedOwners(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			binding := environmentBootstrapCleanupBinding{Version: 1, Environment: environment, SessionID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", NamespaceUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", SecretUID: "cccccccc-cccc-cccc-cccc-cccccccccccc", PolicySHA256: strings.Repeat("d", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			bindings.BootstrapCleanup = &binding
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: binding.KubernetesCA}
			if err := validateEnvironmentIdentityBindings(bindings, environment); err != nil {
				t.Fatal(err)
			}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			foundRole, foundNamespaceRole, foundRecord, foundPod := false, false, false, false
			for _, definition := range definitions {
				object := definition.Object
				meta := deploymentplan.Map(object["metadata"])
				if meta["name"] == "bootstrap-cleanup" && object["kind"] == "Role" {
					foundRole = true
					rules := certIssuerObjectList(object["rules"])
					if len(rules) != 1 || deploymentplan.Digest(deploymentplan.Map(rules[0])["resourceNames"]) != deploymentplan.Digest([]any{"service-bootstrap-" + binding.SessionID}) || deploymentplan.Digest(deploymentplan.Map(rules[0])["verbs"]) != deploymentplan.Digest([]any{"get", "delete"}) {
						t.Fatal("cleanup RBAC has broad Secret authority")
					}
				}
				if object["kind"] == "ClusterRole" && meta["name"] == bindings.Stack+"-bootstrap-cleanup" {
					foundNamespaceRole = true
					rule := deploymentplan.Map(certIssuerObjectList(object["rules"])[0])
					if deploymentplan.Digest(rule["resourceNames"]) != deploymentplan.Digest([]any{bindings.Stack + "-video-cloud"}) || deploymentplan.Digest(rule["verbs"]) != deploymentplan.Digest([]any{"get"}) {
						t.Fatal("namespace ownership probe has broad cluster authority")
					}
				}
				if object["kind"] == "ConfigMap" && meta["name"] == "pki-bootstrap-cleanup-binding" {
					foundRecord = true
					var record map[string]any
					if object["immutable"] != true || json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(object["data"])["binding.json"])), &record) != nil || len(record) != 6 || record["secret_uid"] != binding.SecretUID || record["namespace_uid"] != binding.NamespaceUID || record["session_id"] != binding.SessionID {
						t.Fatal("public owner record was lost or mutable")
					}
				}
				if object["kind"] == "Deployment" && meta["name"] == "pki-controller" {
					foundPod = true
					pod := durablePod(object)
					apiToken := false
					for _, value := range certIssuerObjectList(pod["volumes"]) {
						volume := deploymentplan.Map(value)
						if volume["name"] == "bootstrap-cleanup-api" {
							apiToken = true
							projection := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(volume["projected"])["sources"])[0])
							if deploymentplan.Map(projection["serviceAccountToken"])["audience"] != nil {
								t.Fatal("OpenBao audience was reused for Kubernetes")
							}
						}
						if volume["name"] == "bootstrap-cleanup" {
							if deploymentplan.Map(volume["configMap"])["name"] != "pki-bootstrap-cleanup-binding" {
								t.Fatal("wrong ownership record mounted")
							}
						}
					}
					if !apiToken || pod["automountServiceAccountToken"] != false {
						t.Fatal("cleanup does not use its dedicated projected identity")
					}
				}
			}
			if !foundRole || !foundNamespaceRole || !foundRecord || !foundPod {
				t.Fatal("common bootstrap cleanup dependencies are incomplete")
			}
			bindings.BootstrapCleanup.Environment = "other"
			if validateEnvironmentIdentityBindings(bindings, environment) == nil {
				t.Fatal("another environment's cleanup authority accepted")
			}
		})
	}
}
