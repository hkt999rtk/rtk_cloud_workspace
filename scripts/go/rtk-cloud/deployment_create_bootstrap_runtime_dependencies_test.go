package main

import (
	"context"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateManagedControllerRunsNativeCRLWorkersBeforeFutureIssuerCreation(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	before := deploymentplan.Digest(runtime.InitialListeners)
	initial := createRecipeObject(t, runtime.InitialListeners, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
	initialEnv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(initial)["containers"])[0]))
	if initialEnv["PKI_APP_CRL_WORKER_ENABLED"] != "false" || initialEnv["PKI_SERVER_CRL_DOMAIN"] != "" {
		t.Fatal("fixture must model the actual worker-disabled foundation")
	}
	managed, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	controller := createRecipeObject(t, managed, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
	env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0]))
	if env["PKI_APP_CRL_WORKER_ENABLED"] != "true" || env["PKI_SERVER_CRL_DOMAIN"] != "service,mqtt,openbao_tls" {
		t.Fatal("registered future issuers cannot receive native initial signed CRLs", env)
	}
	if deploymentplan.Digest(runtime.InitialListeners) != before {
		t.Fatal("worker activation changed the frozen original foundation")
	}
	issuer := createRecipeObject(t, managed, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	issuerEnv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0]))
	if issuerEnv["CERT_ISSUER_REGISTRY_ONLY_ENABLED"] != "true" || issuerEnv["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID || issuerEnv["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER"] != "service:deployment-bootstrap" || issuerEnv["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS"] == "" {
		t.Fatal("later client enrollment lost its native original session authorizer", issuerEnv)
	}
}

func TestCreateRetainedTransportHostsAcknowledgeOriginalSessionWithoutJobAttestation(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	definitions, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct {
		id    string
		names []string
	}{
		{"StatefulSet/" + p.Stack + "-secrets/openbao", []string{"openbao-pki"}},
		{"Deployment/" + p.Stack + "-video-cloud/mqtt-pki", []string{"mqtt", "pkibroker"}},
	} {
		object := createRecipeObject(t, definitions, owner.id)
		found := map[string]bool{}
		for _, value := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
			container := deploymentplan.Map(value)
			for _, name := range owner.names {
				if container["name"] != name {
					continue
				}
				found[name] = true
				if createRecipeEnv(container)["PKI_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID {
					t.Fatal("retained native host cannot attest its original installed Service subject", name)
				}
			}
		}
		for _, name := range owner.names {
			if !found[name] {
				t.Fatal("missing retained host", name)
			}
		}
	}
	jobs, err := renderEnvironmentCreateTransportServerDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range jobs {
		if definition.Object["kind"] != "Job" {
			continue
		}
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
		if createRecipeEnv(container)["PKI_BOOTSTRAP_SESSION_ID"] != "" {
			t.Fatal("temporary enrollment Job acknowledged a retained runtime installation")
		}
	}
}

func TestCreateFinalIssuerUsesMaintainedProductAdmissionAndAllCRLWorkers(t *testing.T) {
	cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	final, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	controller := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
	env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0]))
	if env["PKI_APP_CRL_WORKER_ENABLED"] != "true" || env["PKI_SERVER_CRL_DOMAIN"] != "service,mqtt,openbao_tls" {
		t.Fatal("final rendering disabled native publication after provider cutover")
	}
	issuer := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0])
	env = createRecipeEnv(container)
	if env["CERT_ISSUER_REGISTRY_ONLY_ENABLED"] != "true" || env["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID"] != "" || env["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER"] != "" || env["CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN"] != "^$" {
		t.Fatal("final native issuer retained bootstrap issuance or needs legacy fixed CA material")
	}
	if env["CERT_ISSUER_PRODUCT_PKI_ENABLED"] != "true" || env["CERT_ISSUER_APP_PKI_ENABLED"] != "true" || env["CERT_ISSUER_APP_OPENBAO_PKI_MOUNT"] != intermediates["app"].Reference || env["CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256"] != roots["service"].RootFingerprint {
		t.Fatal("final issuer lost actual active native domains and caller pin")
	}
	for name, key := range map[string]string{"CERT_ISSUER_PRODUCTION_JWT_SECRET": "FACTORY_ENROLL_PRODUCTION_JWT_SECRET", "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE": "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE"} {
		setting := managedNamedField(container["env"], name)
		reference := deploymentplan.Map(deploymentplan.Map(setting["valueFrom"])["secretKeyRef"])
		if reference["name"] != "factoryenroll-runtime" || reference["key"] != key {
			t.Fatal("native Product admission differs from maintained factory authority", name)
		}
		if _, literal := setting["value"]; literal {
			t.Fatal("private Product authority was rendered as literal configuration")
		}
	}
	app := intermediates["app"]
	app.RootFingerprint = roots["service"].ID
	intermediates["app"] = app
	if _, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions); err == nil {
		t.Fatal("App signing mount with another Root entered native final runtime")
	}
}

func TestCreateFinalIssuerRetiresOnlyItsCompletedPermissionInitializer(t *testing.T) {
	cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	pod := environmentPKIPod(original)
	privateMounts := deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["volumeMounts"])
	claims := deploymentplan.Digest(pod["volumes"])
	final, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	issuer := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	finalPod := environmentPKIPod(issuer)
	if len(certIssuerObjectList(finalPod["initContainers"])) != 0 || deploymentplan.Map(finalPod["securityContext"])["fsGroupChangePolicy"] != "OnRootMismatch" {
		t.Fatal("completed permission preparation survived final adoption or mounted private files can be widened")
	}
	if deploymentplan.Digest(finalPod["volumes"]) != claims || deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(finalPod["containers"])[0])["volumeMounts"]) != privateMounts {
		t.Fatal("final conversion changed original native private-state custody")
	}
	if len(certIssuerObjectList(pod["initContainers"])) != 1 {
		t.Fatal("final conversion mutated the original completed stage recipe")
	}
	initializer := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
	initializer["command"] = []any{"/bin/sh", "-ec", "unexpected preparation"}
	if _, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions); err == nil {
		t.Fatal("different initializer silently discarded as completed preparation")
	}
	other := deploymentplan.Object{"name": "other-original-initializer", "image": p.Image, "command": []any{"/app/other"}}
	pod["initContainers"] = []any{environmentCreateServicePrivateInit(p.Image, false), other}
	final, err = configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	finalPod = environmentPKIPod(createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer"))
	if deploymentplan.Digest(finalPod["initContainers"]) != deploymentplan.Digest([]any{other}) {
		t.Fatal("final conversion removed an unrelated original initializer")
	}
}

func TestCreateEveryRetainedEnrollmentClaimUsesItsFinalFilesystemGroup(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	// This test exercises the complete policy, beyond the fixture's minimal
	// three-core-owner ceremony used by individual Service recipe tests.
	p.Domains[0].ServiceClientIDs = append(p.Domains[0].ServiceClientIDs, "service:factory-enroll", "service:video-cloud-api", "service:video-cloud-logingester", "service:pkibroker", "service:emqx-pki", "service:openbao")
	service.BootstrapCredentialNamespaces = append(service.BootstrapCredentialNamespaces, p.Stack+"-secrets")
	serviceJobs, err := renderEnvironmentCreateServiceEnrollmentDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	issuerSeed, err := renderEnvironmentCreateManagedServiceIssuerDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	serviceOwners, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, definitions := range [][]deploymentplan.Definition{serviceJobs, issuerSeed, serviceOwners} {
		for _, definition := range definitions {
			if definition.Object["kind"] != "Job" && definition.Object["kind"] != "Deployment" {
				continue
			}
			security := deploymentplan.Map(environmentPKIPod(definition.Object)["securityContext"])
			if security["fsGroup"] != float64(10001) || security["fsGroupChangePolicy"] != "OnRootMismatch" {
				t.Fatal("same-owner Service remount can widen native private state", deploymentplan.ID(definition.Object), security)
			}
		}
	}
	applicationJobs, err := renderEnvironmentCreateApplicationEnrollment(cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range applicationJobs {
		if definition.Object["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(definition.Object)
		security := deploymentplan.Map(pod["securityContext"])
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		group := float64(10001)
		if createRecipeEnv(container)["PKI_CLIENT_IDENTITY_STATE"] == environmentCreateBrokerIdentity {
			group = 1000
		}
		if security["runAsGroup"] != group || security["fsGroup"] != group || security["fsGroupChangePolicy"] != "OnRootMismatch" {
			t.Fatal("application client state differs from its final owner mount group", security)
		}
	}
	transportJobs, err := renderEnvironmentCreateTransportClientDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates)
	if err != nil {
		t.Fatal(err)
	}
	serverJobs, err := renderEnvironmentCreateTransportServerDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates)
	if err != nil {
		t.Fatal(err)
	}
	transports, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, definitions := range [][]deploymentplan.Definition{transportJobs, serverJobs, transports} {
		for _, definition := range definitions {
			if definition.Object["kind"] != "Job" && definition.Object["kind"] != "Deployment" && definition.Object["kind"] != "StatefulSet" {
				continue
			}
			security := deploymentplan.Map(environmentPKIPod(definition.Object)["securityContext"])
			group := float64(1000)
			if deploymentplan.ID(definition.Object) == "Deployment/"+p.Stack+"-video-cloud/pki-controller" {
				// The host stage retains the already enrolled Controller while
				// extending its Service CRL consumer policy.
				group = 10001
			}
			if security["fsGroup"] != group || security["fsGroupChangePolicy"] != "OnRootMismatch" {
				t.Fatal("retained transport native files can be widened between enrollment and final host", deploymentplan.ID(definition.Object), security)
			}
		}
	}
}

func TestCreateFinalIssuerDropsOnlyItsOriginalRetiredSessionSecretReferences(t *testing.T) {
	cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	issuer := createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	pod := environmentPKIPod(issuer)
	pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), environmentCreateServiceVolume("bootstrap", "secret", service.BootstrapCredentialSecret))
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	container["volumeMounts"] = append(certIssuerObjectList(container["volumeMounts"]), environmentCreateServiceMount("bootstrap", "/run/pki-bootstrap", true))
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT": "/run/pki-bootstrap/tls.crt", "CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY": "/run/pki-bootstrap/tls.key"}))
	final, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	finalIssuer := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	for _, reference := range environmentResourceReferences(finalIssuer) {
		if reference.Kind == "Secret" && reference.Name == service.BootstrapCredentialSecret {
			t.Fatal("final runtime references its deleted original session Secret")
		}
	}
	finalPod := environmentPKIPod(finalIssuer)
	for _, group := range []string{"containers", "initContainers"} {
		for _, raw := range certIssuerObjectList(finalPod[group]) {
			for _, value := range certIssuerObjectList(deploymentplan.Map(raw)["volumeMounts"]) {
				if deploymentplan.Map(value)["name"] == "bootstrap" {
					t.Fatal("final native owner retained an unbound seed mount")
				}
			}
		}
	}
	finalEnv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(finalPod["containers"])[0]))
	if finalEnv["PKI_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID || finalEnv["CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE"] != environmentCreateServiceState+"/private/client.json" || finalEnv["CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT"] != "" || finalEnv["CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY"] != "" {
		t.Fatal("seed retirement changed original authenticated client custody or installation session")
	}
	volumes, _ := environmentPKINamedFields(pod["volumes"])
	deploymentplan.Map(volumes["bootstrap"]["secret"])["secretName"] = "service-bootstrap-another-session"
	if _, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions); err == nil {
		t.Fatal("changed session seed was retired under another owner")
	}
}
