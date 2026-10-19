package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createDependencyContractFixture(t *testing.T) (deploymentConfig, secretStore, environmentRelease, map[string]string, environmentCreateBootstrapPlan) {
	t.Helper()
	cfg, store, release, snapshot := runtimeDependencyFixture(t, "dev")
	cfg.Workspace = mustWorkspaceRoot(t)
	baseline, err := readStrictEnv(filepath.Join(cfg.Workspace, "cloud_deploy/architectures/kubernetes/architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Values = appendMap(baseline, cfg.Values)
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"], cfg.Values["OPENBAO_STORAGE_BACKEND"], cfg.Values["OPENBAO_EFFECTIVE_REPLICAS"] = "standard", "file", "1"
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], cfg.Values["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
	for _, flag := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "SHADOW_WORKER_REGISTRATION_ENABLED", "OTA_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "WEBRTC_SERVICE_REGISTRATION_ENABLED"} {
		cfg.Values[flag], cfg.Values["LKE_"+flag] = "true", "true"
	}
	for _, role := range environmentProviderImageKeys {
		if strings.HasPrefix(role, "LKE_ZAMMAD_") {
			release.Images[role] = environmentReleaseImage{Reference: cfg.Values[environmentProviderImageConfigKey(role)]}
		}
	}
	for _, purpose := range []string{"MEDIA", "OTA"} {
		snapshot.Operator["LINODE_"+purpose+"_OBJ_ACCESS_KEY_ID"] = "isolated-" + purpose + "-access"
		snapshot.Operator["LINODE_"+purpose+"_OBJ_SECRET_ACCESS_KEY"] = "isolated-" + purpose + "-secret"
	}
	for name, value := range snapshot.Secrets {
		if err := store.write(filepath.Join("runtime", name), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	p, err := buildEnvironmentCreateBootstrapPlan(context.Background(), cfg, release, store, snapshot.Operator)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, store, release, snapshot.Operator, p
}

func TestCreateDependencyContractUsesMaintainedFullCatalogBeforeNativeCreate(t *testing.T) {
	cfg, store, release, operator, p := createDependencyContractFixture(t)
	if len(p.ProductIdentities) != 5 {
		t.Fatal("fixture lacks all five declared product producers")
	}
	allocations, err := qualifyEnvironmentCreateDependencyContract(context.Background(), cfg, release, store, operator, p)
	if err != nil {
		t.Fatal("fresh full catalog cannot qualify declared bootstrap outputs", err)
	}
	if allocations <= len(p.Claims) {
		t.Fatal("full catalog allocations omitted maintained workload claims")
	}
	for _, path := range []string{"kube/kubeconfig.yaml", "pki/custody", "deployment/create/material"} {
		if _, err := os.Lstat(filepath.Join(store.Root, path)); !os.IsNotExist(err) {
			t.Fatal("static dependency validation created native identity or allocation state", path, err)
		}
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		t.Fatal(err)
	}
	err = withEnvironmentRenderValues(store, release, environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}, cfg.Values, func(values map[string]string) error {
		_, err := renderEnvironmentResources(values, environmentIdentityBindings{Environment: cfg.Environment, Stack: p.Stack}, release, []deploymentplan.Definition{{Object: deploymentplan.Object{"kind": "ConfigMap"}}})
		return err
	})
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.target.identity" {
		t.Fatal("ordinary renderer bypassed genuine identity bindings", err)
	}
	p.ProductIdentities = p.ProductIdentities[:4]
	if _, err := qualifyEnvironmentCreateDependencyContract(context.Background(), cfg, release, store, operator, p); err == nil {
		t.Fatal("enabled consumer with no declared producer qualified")
	}
}

func TestCreateDependencyContractAggregatesMissingAndNeverInventsOutputs(t *testing.T) {
	stack := "video-cloud-dev"
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}
	ns := stack + "-video-cloud"
	namespace := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}, Phase: "namespace"}
	pod := func(name string) deploymentplan.Definition {
		return deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "example.invalid/app@sha256:" + strings.Repeat("a", 64), "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": name + "-tls"}}}}}}}, Phase: "workload"}
	}
	defs := []deploymentplan.Definition{namespace, pod("mqtt"), pod("shadow")}
	bindings := environmentIdentityBindings{Environment: "dev", Stack: stack, Owners: map[string]string{}}
	before := deploymentplan.Digest(defs)
	_, err := validateEnvironmentDeploymentDependencyContract(inputs, defs, nil, bindings, nil)
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.dependency.missing" || !strings.Contains(blocker.Resource, "/mqtt-tls") || !strings.Contains(blocker.Resource, "/shadow-tls") {
		t.Fatal("full missing references were not aggregated", err)
	}
	outputs := map[string]string{"Secret/" + ns + "/mqtt-tls": deploymentplan.Digest("mqtt-native-producer"), "Secret/" + ns + "/shadow-tls": deploymentplan.Digest("shadow-native-producer")}
	got, err := validateEnvironmentDeploymentDependencyContract(inputs, defs, nil, bindings, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(defs) != before || len(got) != len(defs) {
		t.Fatal("validation mutated consumer intent or invented an output object")
	}
	for _, d := range got {
		if d.Object["kind"] == "Pod" && !slices.Contains(d.Dependencies, "Secret/"+ns+"/"+deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])+"-tls") {
			t.Fatal("returned executable intent lost its genuine producer edge")
		}
	}
	if _, err := deploymentplan.Compile(inputs, got, nil); err == nil {
		t.Fatal("static validation projection escaped as an executable plan")
	}
	for _, mode := range []string{"foreign-output", "invalid-digest", "cycle", "later-phase", "missing-output"} {
		t.Run(mode, func(t *testing.T) {
			d := slices.Clone(defs)
			o := map[string]string{}
			for k, v := range outputs {
				o[k] = v
			}
			switch mode {
			case "foreign-output":
				o["Secret/foreign/mqtt-tls"] = o["Secret/"+ns+"/mqtt-tls"]
			case "invalid-digest":
				o["Secret/"+ns+"/mqtt-tls"] = "not-a-contract"
			case "cycle":
				d[1].Dependencies = []string{deploymentplan.ID(d[2].Object)}
				d[2].Dependencies = []string{deploymentplan.ID(d[1].Object)}
			case "later-phase":
				d[0].Dependencies = []string{deploymentplan.ID(d[1].Object)}
			case "missing-output":
				delete(o, "Secret/"+ns+"/mqtt-tls")
			}
			if _, err := validateEnvironmentDeploymentDependencyContract(inputs, d, nil, bindings, o); err == nil {
				t.Fatal("invalid graph/output qualified")
			}
		})
	}
}

func TestCreateDependencyPublicTransportProjectsOnlyDeclaredNativeEdges(t *testing.T) {
	cfg, _, _, _, p := createDependencyContractFixture(t)
	env := deploymentEndpointValues(cfg)
	intent, err := environmentCreateDependencyOutputs(cfg, p, env)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := decodeEnvironmentCreateManifests(lkeCertIssuerServiceManifest(env), lkeCertIssuerPassthroughIngressManifest(env, lkeResolveCertIssuerTLSConfig(env).PublicRoute()))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindEnvironmentCreateDependencyPublicTransport(env, definitions, intent)
	if err != nil {
		t.Fatal("declared native public graph", err)
	}
	if len(bound) != 3 {
		t.Fatal("static projection invented objects beyond Service, Ingress and NetworkPolicy")
	}
	service := createRecipeObject(t, bound, "Service/"+p.Stack+"-video-cloud/certissuer")
	ports := certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"])
	if len(ports) != 1 || deploymentplan.Map(ports[0])["port"] != float64(18445) || deploymentplan.Map(ports[0])["targetPort"] != float64(18445) {
		t.Fatal("static edges differ from the maintained native bootstrap listener")
	}
	native := deploymentplan.Object{"env": environmentCreateServiceIssuerEnv(cfg, p, environmentCreateServiceCeremony{})}
	if mqttFoundationEnv(native, "CERT_ISSUER_LISTEN_ADDR") != ":18445" {
		t.Fatal("static projection drifted from the maintained native issuer recipe")
	}
	for _, d := range bound {
		if d.Object["kind"] == "Deployment" || d.Object["kind"] == "Secret" || deploymentplan.Map(d.Object["metadata"])["uid"] != nil || d.Object["status"] != nil {
			t.Fatal("static public projection fabricated an identity, owner or readiness")
		}
	}
	host := lkeResolveCertIssuerTLSConfig(env).PublicHost
	ingress := createRecipeObject(t, bound, "Ingress/"+p.Stack+"-video-cloud/certissuer-public-mtls")
	if err := environmentCertIssuerPublicIngress(ingress, host, 18445, false); err != nil {
		t.Fatal(err)
	}
	policy := createRecipeObject(t, bound, "NetworkPolicy/"+p.Stack+"-video-cloud/allow-certissuer-public-mtls")
	if deploymentplan.Digest(policy["spec"]) != deploymentplan.Digest(environmentCertIssuerPublicPolicy(env, 18445).Object["spec"]) {
		t.Fatal("static public route lost its exact ingress controller policy")
	}
	for _, check := range []func() error{
		func() error { _, err := bindEnvironmentCertIssuerPublicTransport(env, bound); return err },
		func() error { return validateEnvironmentCertIssuerPublicTransport(env, bound) },
	} {
		var blocker *deploymentplan.Blocker
		if err := check(); !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.certissuer-public" || blocker.Resource != "Deployment/"+p.Stack+"-video-cloud/certissuer" {
			t.Fatal("executable public admission accepted a static graph without its native owner", err)
		}
	}
	p.PublicCertIssuerHost = "foreign.example.invalid"
	if _, err := environmentCreateDependencyOutputs(cfg, p, env); err == nil {
		t.Fatal("fresh graph accepted a different declared bootstrap host")
	}
}

func TestCreateDependencyPublicTransportRejectsUndeclaredOrBrokenEdges(t *testing.T) {
	cfg, _, _, _, p := createDependencyContractFixture(t)
	env := deploymentEndpointValues(cfg)
	owner := "Deployment/" + p.Stack + "-video-cloud/certissuer"
	for _, mode := range []string{"missing-intent", "missing-producer", "invalid-contract", "foreign-host", "fabricated-owner", "missing-service", "missing-ingress", "terminating-route", "foreign-backend"} {
		t.Run(mode, func(t *testing.T) {
			intent, err := environmentCreateDependencyOutputs(cfg, p, env)
			if err != nil {
				t.Fatal(err)
			}
			definitions, err := decodeEnvironmentCreateManifests(lkeCertIssuerServiceManifest(env), lkeCertIssuerPassthroughIngressManifest(env, lkeResolveCertIssuerTLSConfig(env).PublicRoute()))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-intent":
				intent = nil
			case "missing-producer":
				delete(intent.outputs, owner)
			case "invalid-contract":
				intent.outputs[owner] = "not-a-producer-contract"
			case "foreign-host":
				intent.publicCertIssuerHost = "foreign.example.invalid"
			case "fabricated-owner":
				definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"namespace": p.Stack + "-video-cloud", "name": "certissuer"}}})
			case "missing-service":
				definitions = definitions[1:]
			case "missing-ingress":
				definitions = definitions[:1]
			case "terminating-route":
				deploymentplan.Map(deploymentplan.Map(definitions[1].Object["metadata"])["annotations"])["nginx.ingress.kubernetes.io/ssl-passthrough"] = "false"
			case "foreign-backend":
				rule := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(definitions[1].Object["spec"])["rules"])[0])
				path := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rule["http"])["paths"])[0])
				deploymentplan.Map(deploymentplan.Map(path["backend"])["service"])["name"] = "foreign"
			}
			if _, err := bindEnvironmentCreateDependencyPublicTransport(env, definitions, intent); err == nil {
				t.Fatal("undeclared or broken native public transport qualified")
			}
		})
	}
}
