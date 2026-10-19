package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateBootstrapRequiresOriginalRuntimeBeforeAnyMutation(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	var requests int
	const origin = "http://127.0.0.1:18444"
	httpClient := &http.Client{Transport: consoleRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected bootstrap mutation")
	})}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, "dev", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	client.token = "authenticated-original-fixture"
	client.authenticatedAt = time.Now().UTC()
	runtime := environmentCreateBootstrapRuntime{Execution: execution, ExecutionSHA256: artifact.PlanSHA256, Backend: environmentKubernetesBackend{Store: store}, PKIClient: client, APIOrigin: origin}
	plan := environmentCreateBootstrapPlan{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"]}
	for _, mode := range []string{"missing-runtime", "foreign-store", "foreign-execution", "foreign-api", "unauthenticated", "nil-apply"} {
		t.Run(mode, func(t *testing.T) {
			selected := runtime
			selectedClient := *client
			selected.PKIClient = &selectedClient
			calls := 0
			apply := environmentCreateBootstrapApply(func(context.Context, string, []deploymentplan.Definition) error {
				calls++
				return nil
			})
			switch mode {
			case "foreign-store":
				selected.Backend.Store.Root += "-foreign"
			case "foreign-execution":
				selected.ExecutionSHA256 = deploymentplan.Digest("different-parent")
			case "foreign-api":
				selected.APIOrigin = "http://127.0.0.1:1"
			case "unauthenticated":
				selected.PKIClient.token = ""
			case "nil-apply":
				apply = nil
			}
			ctx := context.Background()
			if mode != "missing-runtime" {
				ctx = withEnvironmentCreateBootstrapRuntime(ctx, selected)
			}
			bindings, err := executeEnvironmentCreateBootstrap(ctx, cfg, plan, environmentRelease{}, store, nil, apply)
			if err == nil || !reflect.DeepEqual(bindings, environmentIdentityBindings{}) || calls != 0 || requests != 0 || len(execution.Journal.Entries) != 0 {
				t.Fatal("bootstrap accepted detached ownership or mutated before complete target validation", err)
			}
		})
	}
}

func TestCreateServiceSeedUsesTheActualFoundationTransportAuthority(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, runtime, service := createServiceRecipeFixture(t, environment)
			runtime.TransportTrustConfigMap = "pki-create-transport-trust"
			definitions, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range definitions {
				if d.Object["kind"] != "Job" {
					continue
				}
				for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
					volume := deploymentplan.Map(raw)
					if volume["name"] == "transport" {
						found = true
						if deploymentplan.Map(volume["configMap"])["name"] != runtime.TransportTrustConfigMap {
							t.Fatal("native seed job cannot mount the actually created transport authority")
						}
					}
				}
			}
			if !found {
				t.Fatal("issuer host enrollment omitted its original transport authority")
			}
		})
	}
}

func TestPreparedOpenBaoTrustInstallsReadyPoolWithoutPrematureProviderAdmission(t *testing.T) {
	cfg, plan, runtime, service := createServiceRecipeFixture(t, "dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("deployment/create/material/openbao-test-ca.crt", []byte(runtime.TransportCAPEM), false); err != nil {
		t.Fatal(err)
	}
	path, err := store.safePath("deployment/create/material/openbao-test-ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	runtime.OpenBao.CACertPath = path
	root := service.Root
	root.Domain, root.Status = "openbao_tls", "ready"
	root.Document = deploymentplan.Clone(root.Document)
	root.Document["trust_domain"], root.Document["status"] = root.Domain, root.Status
	definitions, err := renderEnvironmentCreatePreparedOpenBaoConsumers(context.Background(), cfg, plan, runtime, service, root)
	if err != nil {
		t.Fatal(err)
	}
	trust := createRecipeObject(t, definitions, "ConfigMap/"+plan.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust")
	ordered, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: plan.Stack, Operation: "scoped", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "fixture"}, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	configSeen := false
	for _, resource := range ordered.Resources {
		if resource.ID == deploymentplan.ID(trust) {
			configSeen = true
		}
		if resource.Desired["kind"] == "Deployment" {
			name := deploymentplan.Map(resource.Desired["metadata"])["name"]
			if (name == "certissuer" || name == "pki-controller") && !configSeen {
				t.Fatal("provider consumer starts before its ready trust configuration")
			}
		}
	}
	data := deploymentplan.Map(trust["data"])
	if data["roots.pem"] != root.Certificate+"\n"+runtime.TransportCAPEM {
		t.Fatal("prepared provider pool must contain the native future authority and original current transport authority")
	}
	for _, role := range []string{"pki-controller", "certissuer"} {
		object := createRecipeObject(t, definitions, "Deployment/"+plan.Stack+"-video-cloud/"+role)
		env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0]))
		if env["OPENBAO_SERVER_BUNDLE_ROOT_SHA256"] != root.RootFingerprint || env["OPENBAO_SERVER_BUNDLE_MANIFEST"] == "" || env["OPENBAO_SERVER_PKI_ROOT_SHA256"] != "" || env["OPENBAO_SERVER_CRL_MANIFEST"] != "" {
			t.Fatal("ready trust must earn actual native bundle receipts before the future provider is admitted")
		}
	}
	root.Status = "active"
	root.Document["status"] = root.Status
	intermediate := service.Intermediate
	intermediate.Domain = root.Domain
	intermediate.Document = deploymentplan.Clone(intermediate.Document)
	intermediate.Document["trust_domain"] = root.Domain
	managed, err := applyEnvironmentCreateManagedOpenBaoConsumers(plan, service, map[string]environmentCreateRegisteredIssuer{"openbao_tls": root}, map[string]environmentCreateRegisteredIssuer{"openbao_tls": intermediate}, definitions)
	if err != nil {
		t.Fatal(err)
	}
	trust = createRecipeObject(t, managed, "ConfigMap/"+plan.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust")
	data = deploymentplan.Map(trust["data"])
	if data["roots.pem"] != root.Certificate || data["crls.json"] == "" {
		t.Fatal("completed provider consumer kept temporary authority or omitted native CRL verification")
	}
	for _, role := range []string{"pki-controller", "certissuer"} {
		object := createRecipeObject(t, managed, "Deployment/"+plan.Stack+"-video-cloud/"+role)
		env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0]))
		if env["OPENBAO_SERVER_PKI_ROOT_SHA256"] != root.RootFingerprint || env["OPENBAO_SERVER_CRL_MANIFEST"] == "" {
			t.Fatal("completed provider caller lacks real registry and native CRL admission")
		}
	}
}

func TestCreateControllerIdentityMatchesCanonicalEnrollmentAndPermissionLifecycle(t *testing.T) {
	cfg, plan, runtime, service := createServiceRecipeFixture(t, "dev")
	jobs, err := renderEnvironmentCreateServiceEnrollmentDefinitions(context.Background(), cfg, plan, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	controllerFiles := map[string]bool{}
	for _, d := range jobs {
		if d.Object["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		env := createRecipeEnv(c)
		for _, key := range []string{"PKI_CLIENT_IDENTITY_STATE", "PKI_SERVER_IDENTITY_STATE"} {
			if env[key] == environmentCreateServiceState+"/identity/client.json" || env[key] == environmentCreateServiceState+"/identity/server.json" {
				controllerFiles[env[key]] = true
			}
		}
	}
	if len(controllerFiles) != 2 {
		t.Fatal("controller enrollment did not generate both original files in the canonical owner subtree")
	}
	owners, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, plan, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	controller := createRecipeObject(t, owners, "Deployment/"+plan.Stack+"-video-cloud/pki-controller")
	if err := validateControllerIdentityInitializer(controller, plan.Images["LKE_ALPINE_IMAGE"]); err != nil {
		t.Fatal("fresh controller cannot pass the maintained permission/adoption lifecycle", err)
	}
	pod := environmentPKIPod(controller)
	init := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
	if init["name"] != controllerIdentityInitializerName || init["image"] != plan.Images["LKE_ALPINE_IMAGE"] || deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] != "OnRootMismatch" {
		t.Fatal("fresh controller used a separate initializer or changed the original directory permission contract")
	}
	account := createRecipeObject(t, owners, "Deployment/"+plan.Stack+"-account-manager/account-manager")
	sidecar := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(account)["containers"])[1])
	if createRecipeEnv(sidecar)["PKI_MANAGEMENT_IDENTITY_STATE"] != environmentCreateServiceState+"/private/client.json" {
		t.Fatal("controller path normalization changed the independent Account Manager owner")
	}
}

func TestCreateAccountBootstrapDoesNotRequireFutureStaticTLSOwners(t *testing.T) {
	cfg, plan, runtime, service := createServiceRecipeFixture(t, "dev")
	cfg.Values["DEPLOYMENT_ADAPTER"] = "lke"
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "9444"
	_, _, release := completeEnvironmentRenderFixture("dev")
	initial, err := renderEnvironmentCreateAccountFoundation(cfg, release, nil)
	if err != nil {
		t.Fatal(err)
	}
	account := createRecipeObject(t, initial, "Deployment/"+plan.Stack+"-account-manager/account-manager")
	pod := environmentPKIPod(account)
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	env := createRecipeEnv(main)
	for _, key := range []string{"APP_CERT_ISSUER_BASE_URL", "APP_CERT_ISSUER_CLIENT_CERT", "APP_CERT_ISSUER_CLIENT_KEY", "APP_CERT_ISSUER_CA_FILE", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_KEY"} {
		if value, exists := env[key]; !exists || value != "" {
			t.Fatal("initial password login requires a TLS owner which has not been created", key)
		}
	}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		name := deploymentplan.Map(raw)["name"]
		if name == "account-manager-certissuer-client" || name == "account-manager-service-registration-tls" {
			t.Fatal("initial login waits for a future static TLS Secret")
		}
	}
	managed, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, plan, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	account = createRecipeObject(t, managed, "Deployment/"+plan.Stack+"-account-manager/account-manager")
	pod = environmentPKIPod(account)
	main = deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	env = createRecipeEnv(main)
	if env["APP_CERT_ISSUER_SOCKET"] != "/run/account-pki/private/controller.sock" || env["APP_CERT_ISSUER_CLIENT_KEY"] != "" {
		t.Fatal("managed Account App issuer did not use its actual owner socket")
	}
	sidecar := deploymentplan.Map(certIssuerObjectList(pod["containers"])[1])
	if createRecipeEnv(sidecar)["PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR"] != ":18443" {
		t.Fatal("managed Account listener collides with maintained registration routing")
	}
	accountService := createRecipeObject(t, managed, "Service/"+plan.Stack+"-account-manager/account-manager")
	ports := map[string]float64{}
	for _, raw := range certIssuerObjectList(deploymentplan.Map(accountService["spec"])["ports"]) {
		v := deploymentplan.Map(raw)
		ports[deploymentplan.String(v["name"])], _ = v["port"].(float64)
	}
	if ports["service-mtls"] != 18443 || ports["service-reg"] != 8443 {
		t.Fatal("managed API and registration did not keep separate Service ports", ports)
	}
}
