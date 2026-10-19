package main

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Evaluate the actual namespace+Pod intersection, rather than accepting a
// manifest merely because both expected label strings occur somewhere in it.
func testPostgresPolicyAllows(policy deploymentplan.Object, namespace string, labels map[string]any, protocol string, port float64) bool {
	spec := deploymentplan.Map(policy["spec"])
	for _, raw := range certIssuerObjectList(spec["ingress"]) {
		rule := deploymentplan.Map(raw)
		permittedPort := false
		for _, item := range certIssuerObjectList(rule["ports"]) {
			p := deploymentplan.Map(item)
			if p["protocol"] == protocol && p["port"] == port {
				permittedPort = true
			}
		}
		if !permittedPort {
			continue
		}
		for _, item := range certIssuerObjectList(rule["from"]) {
			peer := deploymentplan.Map(item)
			ns := deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])
			if ns["kubernetes.io/metadata.name"] != namespace {
				continue
			}
			matched := true
			for key, value := range deploymentplan.Map(deploymentplan.Map(peer["podSelector"])["matchLabels"]) {
				matched = matched && labels[key] == value
			}
			if matched {
				return true
			}
		}
	}
	return false
}

func TestPostgresOpenBaoPolicyRestrictsNamespacePodAndPortInEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, _ := completeEnvironmentRenderFixture(environment)
			defs, err := decodeEnvironmentCreateManifests(lkeAllowPostgresOpenBaoNetworkPolicyManifest(env), lkeAllowPostgresClientsNetworkPolicyManifest(env))
			if err != nil || len(defs) != 2 {
				t.Fatal(err)
			}
			policy, application := defs[0].Object, defs[1].Object
			stack := env["CLOUD_STACK_NAME"]
			if deploymentplan.ID(policy) != "NetworkPolicy/"+stack+"-platform/allow-postgres-openbao" {
				t.Fatal("OpenBao database policy escaped its selected stack")
			}
			spec := deploymentplan.Map(policy["spec"])
			if !reflect.DeepEqual(spec["podSelector"], deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "postgresql"}}) || len(certIssuerObjectList(spec["ingress"])) != 1 {
				t.Fatal("policy broadened its destination or rule set")
			}
			postgres, err := decodeEnvironmentCreateManifests(lkePostgresStatefulSetManifest(env))
			if err != nil || len(postgres) != 1 {
				t.Fatal("maintained PostgreSQL producer", err)
			}
			pgLabels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(postgres[0].Object["spec"])["template"])["metadata"])["labels"]
			for key, value := range deploymentplan.Map(deploymentplan.Map(spec["podSelector"])["matchLabels"]) {
				if deploymentplan.Map(pgLabels)[key] != value {
					t.Fatal("policy destination misses the actual maintained PostgreSQL Pod labels")
				}
			}
			var source deploymentplan.Object
			for _, binding := range bindings.Pods {
				if binding.Component == "managed-openbao" {
					source, err = renderEnvironmentOpenBao(env, binding)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if source == nil {
				t.Fatal("maintained OpenBao producer absent")
			}
			sourceNamespace := deploymentplan.String(deploymentplan.Map(source["metadata"])["namespace"])
			sourceLabels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(source["spec"])["template"])["metadata"])["labels"]
			if !testPostgresPolicyAllows(policy, sourceNamespace, deploymentplan.Map(sourceLabels), "TCP", 5432) {
				t.Fatal("policy misses the actual maintained OpenBao namespace/Pod labels")
			}
			rule := deploymentplan.Map(certIssuerObjectList(spec["ingress"])[0])
			if len(certIssuerObjectList(rule["from"])) != 1 || len(certIssuerObjectList(rule["ports"])) != 1 {
				t.Fatal("policy must use a single AND-combined namespace/Pod peer and one port")
			}
			for _, tc := range []struct {
				ns, app, protocol string
				port              float64
				allowed           bool
			}{
				{stack + "-secrets", "openbao", "TCP", 5432, true},
				{stack + "-secrets", "unrelated", "TCP", 5432, false},
				{stack + "-secrets", "", "TCP", 5432, false},
				{stack + "-logger", "openbao", "TCP", 5432, false},
				{stack + "-frontend", "openbao", "TCP", 5432, false},
				{"foreign-secrets", "openbao", "TCP", 5432, false},
				{stack + "-secrets", "openbao", "UDP", 5432, false},
				{stack + "-secrets", "openbao", "TCP", 8200, false},
			} {
				if got := testPostgresPolicyAllows(policy, tc.ns, map[string]any{"app.kubernetes.io/name": tc.app}, tc.protocol, tc.port); got != tc.allowed {
					t.Fatal("incorrect namespace/Pod/port intersection", tc.ns, tc.app, tc.protocol, tc.port)
				}
			}
			// Preserve the existing application policy, including its three
			// original namespaces. The interrupted original CREATE stays valid.
			var peers []any
			for _, suffix := range []string{"video-cloud", "account-manager", "billing"} {
				peers = append(peers, deploymentplan.Object{"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"kubernetes.io/metadata.name": stack + "-" + suffix}}})
			}
			want := deploymentplan.Object{"podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "postgresql"}}, "policyTypes": []any{"Ingress"}, "ingress": []any{deploymentplan.Object{"from": peers, "ports": []any{deploymentplan.Object{"protocol": "TCP", "port": float64(5432)}}}}}
			if !reflect.DeepEqual(application["spec"], want) {
				t.Fatal("existing application policy was rewritten to repair OpenBao")
			}
		})
	}
}

func TestPostgresNetworkPoliciesCoverMaintainedFullCatalogAndNativeOpenBao(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-network-contract")
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal("maintained full catalog", err)
			}
			definitions, err = bindEnvironmentInitialSchemaOwner(definitions, environment, env["CLOUD_STACK_NAME"], "create", release, lkeImagePullSecretName(env), bindings.Owners)
			if err != nil {
				t.Fatal("maintained first-create migration producer", err)
			}
			byID := map[string]deploymentplan.Object{}
			for _, d := range definitions {
				byID[deploymentplan.ID(d.Object)] = d.Object
			}
			stack := env["CLOUD_STACK_NAME"]
			application := byID["NetworkPolicy/"+stack+"-platform/allow-postgres-clients"]
			openbao := byID["NetworkPolicy/"+stack+"-platform/allow-postgres-openbao"]
			if application == nil || openbao == nil {
				t.Fatal("complete maintained target omitted a database policy producer")
			}
			// Use the real managed installer and sidecar recipes, not a host
			// stub with no PKI_DATABASE_URL (which missed this live failure).
			cfg, bootstrap, nativeRelease, runtime, service, roots, issuers, _ := createApplicationFinalizeFixture(t, environment)
			transport, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, bootstrap, runtime, service, roots, issuers, nativeRelease)
			if err != nil {
				t.Fatal(err)
			}
			native := createRecipeObject(t, transport, "StatefulSet/"+stack+"-secrets/openbao")
			labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(native["spec"])["template"])["metadata"])["labels"]
			checked := map[string]bool{}
			for _, field := range []string{"containers", "initContainers"} {
				for _, raw := range certIssuerObjectList(environmentPKIPod(native)[field]) {
					c := deploymentplan.Map(raw)
					setting := managedNamedField(c["env"], "PKI_DATABASE_URL")
					if setting == nil {
						continue
					}
					ref := deploymentplan.Map(deploymentplan.Map(setting["valueFrom"])["secretKeyRef"])
					if ref["name"] != "pki-verifier-database" || ref["key"] != "url" || !testPostgresPolicyAllows(openbao, stack+"-secrets", deploymentplan.Map(labels), "TCP", 5432) || testPostgresPolicyAllows(application, stack+"-secrets", deploymentplan.Map(labels), "TCP", 5432) {
						t.Fatal("native OpenBao registry client has no exact additive database path", c["name"])
					}
					checked[deploymentplan.String(c["name"])] = true
				}
			}
			if !checked["openbao-pki"] || !checked["openbao-pki-install"] || len(checked) != 2 {
				t.Fatal("installer/running native DB clients were not both exercised")
			}
			clients := map[string]bool{}
			for _, d := range definitions {
				ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
				pod := environmentPKIPod(d.Object)
				for _, raw := range append(certIssuerObjectList(pod["containers"]), certIssuerObjectList(pod["initContainers"])...) {
					c := deploymentplan.Map(raw)
					central := false
					for _, rawEnv := range certIssuerObjectList(c["env"]) {
						e := deploymentplan.Map(rawEnv)
						value := deploymentplan.String(e["value"])
						if strings.Contains(value, "@postgresql."+stack+"-platform.svc") {
							central = true
							continue
						}
						ref := deploymentplan.Map(deploymentplan.Map(e["valueFrom"])["secretKeyRef"])
						if e["name"] == "PKI_DATABASE_URL" && (ref["name"] == "pki-verifier-database" || ref["name"] == "pki-controller-database") && ref["key"] == "url" || e["name"] == "DATABASE_URL" && ref["name"] == "billing-runtime" && ref["key"] == "DATABASE_URL" {
							central = true
						}
					}
					for _, rawFrom := range certIssuerObjectList(c["envFrom"]) {
						name := deploymentplan.Map(deploymentplan.Map(rawFrom)["secretRef"])["name"]
						if name == "account-manager-runtime" || name == "billing-runtime" {
							central = true
						}
					}
					if central {
						if !testPostgresPolicyAllows(application, ns, nil, "TCP", 5432) {
							t.Fatal("maintained central database client lost network access", deploymentplan.ID(d.Object), c["name"])
						}
						clients[ns] = true
					}
				}
			}
			for _, suffix := range []string{"video-cloud", "account-manager", "billing"} {
				if !clients[stack+"-"+suffix] {
					t.Fatal("full catalog failed to exercise a maintained DB namespace", suffix)
				}
			}
			logger := byID["Deployment/"+stack+"-logger/cloud-logger"]
			if logger == nil || !strings.Contains(string(mustProviderRecoveryJSON(environmentPKIPod(logger))), "/var/lib/rtk-cloud-logger/billing-inbox.db") {
				t.Fatal("logger's actual SQLite inbox contract changed")
			}
			if testPostgresPolicyAllows(openbao, stack+"-logger", map[string]any{"app.kubernetes.io/name": "openbao"}, "TCP", 5432) || testPostgresPolicyAllows(application, stack+"-logger", nil, "TCP", 5432) {
				t.Fatal("non-Postgres logger gained central database access")
			}
		})
	}
}
