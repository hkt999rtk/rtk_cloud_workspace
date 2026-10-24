package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateAccountVerifierBindingPreservesOriginalCredentialAndNamespace(t *testing.T) {
	stack, password := "video-cloud-dev", "fixture:p@ss/word?with%characters"
	dsn := "postgres://" + url.UserPassword("rtk_pki_verifier_login", password).String() + "@postgresql." + stack + "-platform.svc:5432/video_cloud?sslmode=disable"
	original := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-verifier-database", "namespace": stack + "-video-cloud"}, "data": deploymentplan.Object{"url": base64.StdEncoding.EncodeToString([]byte(dsn))}}
	before := deploymentplan.Digest(original)
	d, err := environmentCreateAccountVerifierDefinition(stack, password, original)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.ID(d.Object) != "Secret/"+stack+"-account-manager/pki-verifier-database" || deploymentplan.Map(d.Object["stringData"])["url"] != dsn || deploymentplan.Digest(original) != before {
		t.Fatal("namespace binding changed the original credential or source Secret")
	}
	for _, tc := range []struct {
		name, stack, password string
		original              deploymentplan.Object
	}{
		{"missing-secret", stack, password, nil},
		{"missing-password", stack, "", original},
		{"changed-password", stack, "replacement", original},
		{"other-environment", "video-cloud-staging", password, original},
		{"foreign-source-owner", stack, password, deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-verifier-database", "namespace": stack + "-account-manager"}, "data": original["data"]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := environmentCreateAccountVerifierDefinition(tc.stack, tc.password, tc.original); err == nil {
				t.Fatal("unverified original credential binding was accepted")
			}
		})
	}
}

func createServiceRecipeFixture(t *testing.T, environment string) (deploymentConfig, environmentCreateBootstrapPlan, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony) {
	t.Helper()
	values, _, release := completeEnvironmentRenderFixture(environment)
	values["CLOUD_ENV_NAME"] = environment
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: values}
	p := environmentCreateBootstrapPlan{IngressAdmissionProfile: environmentCreateIngressAdmissionProfile, Environment: environment, Stack: values["CLOUD_STACK_NAME"], Image: release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, AccountManagerImage: release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference, Domains: []environmentCreateIssuerPolicy{{Domain: "service", BundleConsumers: []string{"account-manager", "certissuer", "pki-controller"}, Consumers: []string{"account-manager", "certissuer", "pki-controller", "factory-enroll", "video-cloud-api", "video-cloud-logingester"}, ServerDNSNames: []string{"certissuer." + values["CLOUD_STACK_NAME"] + "-video-cloud.svc", "pki-controller." + values["CLOUD_STACK_NAME"] + "-video-cloud.svc", "account-manager." + values["CLOUD_STACK_NAME"] + "-account-manager.svc.cluster.local"}, ServiceClientIDs: []string{"service:account-manager", "service:certissuer", "service:pki-controller", "service:deployment-bootstrap"}}}}
	p.Images = map[string]string{}
	for role, image := range release.Images {
		p.Images[role] = image.Reference
	}
	now := time.Now().UTC().Add(-time.Minute)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	makeCA := func(id, kind string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) environmentCreateRegisteredIssuer {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "fixture-" + kind}, NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		if parent == nil {
			parent = template
			parentKey = k
			key = k
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &k.PublicKey, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		pemValue := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		fingerprint := environmentPreflightBytesSHA256(der)
		doc := map[string]any{"issuer_id": id, "issuer_version": float64(1), "environment": environment, "trust_domain": "service", "kind": kind, "status": "active", "signer_provider": "openbao", "signer_reference": "fixture-issuer/1", "certificate_pem": pemValue, "chain_pem": pemValue, "certificate_fingerprint_sha256": fingerprint, "trust_bundle_version": environmentPreflightBytesSHA256([]byte(pemValue)), "not_before": now.Format(time.RFC3339), "not_after": now.Add(time.Hour).Format(time.RFC3339)}
		i, err := environmentCreateRegisteredIssuerFromMap(doc, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	root := makeCA("11111111-1111-4111-8111-111111111111", "root", nil, nil)
	block, _ := pem.Decode([]byte(root.Certificate))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	intermediate := makeCA("22222222-2222-4222-8222-222222222222", "intermediate", cert, key)
	intermediate.RootFingerprint = root.RootFingerprint
	intermediate.ChainPEM += root.Certificate
	intermediate.BundleVersion = environmentPreflightBytesSHA256([]byte(intermediate.ChainPEM))
	intermediate.Document["chain_pem"] = intermediate.ChainPEM
	intermediate.Document["trust_bundle_version"] = intermediate.BundleVersion
	intermediate.Document["parent_issuer_id"] = root.ID
	transport := makeCA("99999999-9999-4999-8999-999999999999", "root", nil, nil)
	initial, err := renderEnvironmentCreateInitialPKIListeners(cfg, release, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", map[string]string{"PKI_OPERATOR_SIGNER_REF": "fixture-operator-key"})
	if err != nil {
		t.Fatal(err)
	}
	store := makeIsolatedTestSecretStore(t, environment)
	for _, name := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
		if err := store.write("runtime/"+name, []byte("isolated-original-"+name), false); err != nil {
			t.Fatal(err)
		}
	}
	runtime := environmentCreateBootstrapRuntime{Backend: environmentKubernetesBackend{Store: store}, InitialListeners: initial, TransportCAPEM: transport.Certificate, TransportRootSHA256: transport.RootFingerprint, TransportTrustConfigMap: "pki-create-management-ca", TransportSecrets: map[string]string{"account-manager": "pki-create-account-manager-transport", "certissuer": "pki-create-certissuer-transport", "pki-controller": "pki-create-pki-controller-transport"}}
	session := "33333333-3333-4333-8333-333333333333"
	s := environmentCreateServiceCeremony{Root: root, Intermediate: intermediate, BootstrapSessionID: session, BootstrapCredentialSecret: "service-bootstrap-" + session, BootstrapCredentialNamespace: p.Stack + "-video-cloud", BootstrapCredentialNamespaces: []string{p.Stack + "-video-cloud", p.Stack + "-account-manager"}, RootCRLSecret: "pki-create-service-root-crl", RootCRLPEM: "fixture-native-root-crl", CleanupBinding: environmentBootstrapCleanupBinding{Version: 1, Environment: environment, SessionID: session, NamespaceUID: "44444444-4444-4444-8444-444444444444", SecretUID: "55555555-5555-4555-8555-555555555555", PolicySHA256: strings.Repeat("a", 64), KubernetesCA: base64.StdEncoding.EncodeToString([]byte(transport.Certificate))}}
	return cfg, p, runtime, s
}

func createRecipeObject(t *testing.T, defs []deploymentplan.Definition, id string) deploymentplan.Object {
	t.Helper()
	for _, d := range defs {
		if deploymentplan.ID(d.Object) == id {
			return d.Object
		}
	}
	t.Fatal("missing actual declared recipe", id)
	return nil
}

func TestCreateManagedCoreUsesSelectedEnvironmentForCleanup(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		cfg, p, runtime, s := createServiceRecipeFixture(t, environment)
		delete(cfg.Values, "CLOUD_ENV_NAME") // maintained configuration carries Environment separately
		before := deploymentplan.Digest(cfg.Values)
		definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s)
		if err != nil {
			t.Fatal(err)
		}
		binding := createRecipeObject(t, definitions, "ConfigMap/"+p.Stack+"-video-cloud/pki-bootstrap-cleanup-binding")
		if !strings.Contains(deploymentplan.String(deploymentplan.Map(binding["data"])["binding.json"]), `"environment":"`+environment+`"`) || before != deploymentplan.Digest(cfg.Values) {
			t.Fatal("selected cleanup environment or maintained configuration changed")
		}
		s.CleanupBinding.Environment = "foreign"
		if _, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s); err == nil {
			t.Fatal("foreign cleanup owner accepted")
		}
	}
}

func TestCreateServiceCoreRecoveryDoesNotReplayEnrollment(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	delete(cfg.Values, "CLOUD_ENV_NAME")
	runtime.ServiceSessionRecovery = &createServiceSessionRecovery{CoreAccepted: true, Ceremony: s}
	calls := 0
	actual, err := executeEnvironmentCreateServiceBootstrap(context.Background(), cfg, p, secretStore{}, nil, runtime, func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		calls++
		if stage != "service-managed-core" || len(definitions) == 0 {
			t.Fatal("replayed an accepted enrollment or renewal", stage)
		}
		for _, d := range definitions {
			if d.Object["kind"] == "Job" {
				t.Fatal("accepted enrollment replayed")
			}
		}
		return nil
	})
	if err != nil || calls != 1 || deploymentplan.Digest(actual) != deploymentplan.Digest(s) {
		t.Fatal("core continuation lost original ceremony", err)
	}
}

func TestCreateManagedCoreOrderRequiresControllerThenIssuerThenAccount(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, r := range plan.Resources {
		positions[r.ID] = i
	}
	controller := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
	issuer := "Deployment/" + p.Stack + "-video-cloud/certissuer"
	account := "Deployment/" + p.Stack + "-account-manager/account-manager"
	if deploymentplan.Map(deploymentplan.Map(plan.Resources[positions[account]].Desired["spec"])["strategy"])["type"] != "Recreate" {
		t.Fatal("Account Manager would overlap owners of the process-locked identity PVC")
	}
	accountMeta := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(plan.Resources[positions[account]].Desired["spec"])["template"])["metadata"])
	if deploymentplan.Map(accountMeta["annotations"])["rtk.realtek.com/pki-identity-owner-mode"] != "exclusive" {
		t.Fatal("interrupted rolling ReplicaSet would not be replaced")
	}
	if !(positions[controller] < positions[issuer] && positions[issuer] < positions[account]) {
		t.Fatal("managed TLS hosts would start before their Service controller", positions)
	}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: account, Owners: map[string]string{}}
	for _, r := range plan.Resources {
		if r.ID == controller || r.ID == issuer {
			continue
		}
		j.Owners[r.ID] = "original-owner-" + r.ID
		if r.ID != account {
			j.Completed = append(j.Completed, r.ID)
		}
	}
	if !knownManagedCoreOrderFailure(plan, j, p.Stack) {
		t.Fatal("exact known partial writes rejected")
	}
	for _, mode := range []string{"accepted", "in-flight", "acceptance", "missing-owner", "wrong-failure", "extra-completion"} {
		copy := j
		copy.Owners = map[string]string{}
		for k, v := range j.Owners {
			copy.Owners[k] = v
		}
		copy.Completed = slices.Clone(j.Completed)
		switch mode {
		case "accepted":
			copy.Accepted = true
		case "in-flight":
			copy.InFlight = account
		case "acceptance":
			copy.AcceptanceStarted = true
		case "missing-owner":
			delete(copy.Owners, account)
		case "wrong-failure":
			copy.Failed = controller
		case "extra-completion":
			copy.Completed = append(copy.Completed, account)
		}
		if knownManagedCoreOrderFailure(plan, copy, p.Stack) {
			t.Fatal("uncertain partial writes admitted", mode)
		}
	}
}
func createRecipeEnv(container deploymentplan.Object) map[string]string {
	out := map[string]string{}
	for _, e := range certIssuerObjectList(container["env"]) {
		m := deploymentplan.Map(e)
		out[deploymentplan.String(m["name"])] = deploymentplan.String(m["value"])
	}
	return out
}

func TestCreateServiceFirstTrustInstallsActualBundlesBeforeManagedState(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, runtime, s := createServiceRecipeFixture(t, environment)
			original := deploymentplan.Digest(runtime.InitialListeners)
			s.Root.Status = "ready"
			s.Root.Document["status"] = "ready"
			first, err := renderEnvironmentCreateFirstTrustDefinitions(context.Background(), cfg, p, runtime, s.Root)
			if err != nil {
				t.Fatal(err)
			}
			s.Intermediate.Status = "ready"
			s.Intermediate.Document["status"] = "ready"
			second, err := renderEnvironmentCreateFirstTrustDefinitions(context.Background(), cfg, p, runtime, s.Root, s.Intermediate)
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"pki-controller", "certissuer"} {
				id := "Deployment/" + p.Stack + "-video-cloud/" + role
				o := createRecipeObject(t, second, id)
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(o)["containers"])[0])
				env := createRecipeEnv(c)
				if env["PKI_SERVICE_CLIENT_ROOT_SHA256"] != s.Root.RootFingerprint || env["PKI_SERVICE_CLIENT_BUNDLE_MANIFEST"] == "" || env["PKI_SERVICE_CLIENT_MANAGEMENT_CERT"] != "/run/pki-create/tls.crt" || env["PKI_SERVER_CRL_DOMAIN"] != "" {
					t.Fatal("ready bundles were skipped or active CRL work began prematurely")
				}
				if reflect.DeepEqual(deploymentplan.Map(deploymentplan.Map(createRecipeObject(t, first, id)["spec"])["template"])["metadata"], deploymentplan.Map(deploymentplan.Map(o["spec"])["template"])["metadata"]) {
					t.Fatal("new bundle did not force an actual listener pool installation")
				}
			}
			account := createRecipeObject(t, second, "Deployment/"+p.Stack+"-account-manager/account-manager")
			containers := certIssuerObjectList(environmentPKIPod(account)["containers"])
			if len(containers) != 2 {
				t.Fatal("native sidecar missing")
			}
			main, side := deploymentplan.Map(containers[0]), deploymentplan.Map(containers[1])
			env := createRecipeEnv(side)
			if env["PKI_MANAGEMENT_FIRST_TRUST_ONLY"] != "true" || env["PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_ROOT_SHA256"] != runtime.TransportRootSHA256 || env["PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256"] != s.Root.RootFingerprint || env["PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST"] != "" {
				t.Fatal("initial transport and actual ServiceRoot were conflated")
			}
			for _, mount := range certIssuerObjectList(main["volumeMounts"]) {
				if slices.Contains([]string{"service-state", "pki-create-transport"}, deploymentplan.String(deploymentplan.Map(mount)["name"])) {
					t.Fatal("public account process gained private Service identity")
				}
			}
			if deploymentplan.Digest(runtime.InitialListeners) != original {
				t.Fatal("rendering mutated frozen initial foundation")
			}
		})
	}
}

func TestCreateServiceEnrollmentAndOwnerSwitchKeepNativeIdentityOwnership(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, runtime, s := createServiceRecipeFixture(t, environment)
			static, err := renderEnvironmentCreateServiceIssuerDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			issuer := createRecipeObject(t, static, "Deployment/"+p.Stack+"-video-cloud/certissuer")
			env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0]))
			if env["CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY"] != "true" || env["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID"] != s.BootstrapSessionID || env["CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN"] != "^service:deployment-bootstrap$" {
				t.Fatal("first issuer lacks actual sealed authorization")
			}
			seed, err := renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			job := seed[1].Object
			jobenv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0]))
			if jobenv["PKI_CLIENT_ISSUER_TLS_CA"] != "/run/pki-create/ca.crt" || jobenv["PKI_SERVER_ROOT_SHA256"] != s.Root.RootFingerprint {
				t.Fatal("temporary transport substituted for server identity authority")
			}
			managedIssuer, err := renderEnvironmentCreateManagedServiceIssuerDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			issuer = createRecipeObject(t, managedIssuer, "Deployment/"+p.Stack+"-video-cloud/certissuer")
			env = createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0]))
			if env["CERT_ISSUER_HOST_IDENTITY_STATE"] != environmentCreateServiceState+"/private/server.json" || env["CERT_ISSUER_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256"] != s.Root.RootFingerprint {
				t.Fatal("native server handoff did not use current Service issuer")
			}
			jobs, err := renderEnvironmentCreateServiceEnrollmentDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, d := range jobs {
				if d.Object["kind"] != "Job" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				e := createRecipeEnv(c)
				if e["PKI_BOOTSTRAP_SESSION_ID"] != "" || e["PKI_CLIENT_ISSUER_TLS_CA"] != environmentCreateServiceTrust+"/root.pem" {
					t.Fatal("Job emitted a workload receipt or used unregistered issuer transport")
				}
				claim := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])["persistentVolumeClaim"])["claimName"])
				seen[claim] = true
			}
			if len(seen) != 3 {
				t.Fatal("native owners shared private client/server state")
			}
			owners, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range owners {
				if d.Object["kind"] != "Deployment" {
					continue
				}
				pod := environmentPKIPod(d.Object)
				for _, c := range certIssuerObjectList(pod["containers"]) {
					for name, value := range createRecipeEnv(deploymentplan.Map(c)) {
						if (strings.Contains(name, "BOOTSTRAP_CERT") || strings.Contains(name, "BOOTSTRAP_KEY") || name == "PKI_MANAGEMENT_FIRST_TRUST_ONLY" || name == "CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY") && value != "" {
							t.Fatal("ordinary owner retained temporary enrollment mode", name)
						}
					}
				}
				for _, v := range certIssuerObjectList(pod["volumes"]) {
					if slices.Contains([]string{"bootstrap", "transport", "pki-create-transport"}, deploymentplan.String(deploymentplan.Map(v)["name"])) {
						t.Fatal("ordinary owner retained temporary credential mount")
					}
				}
			}
			binding := createRecipeObject(t, owners, "ConfigMap/"+p.Stack+"-video-cloud/pki-bootstrap-cleanup-binding")
			if !strings.Contains(deploymentplan.String(deploymentplan.Map(binding["data"])["binding.json"]), s.CleanupBinding.SecretUID) {
				t.Fatal("actual cleanup owner UID was omitted")
			}
			trust := createRecipeObject(t, owners, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-service-trust")
			if !strings.Contains(deploymentplan.String(deploymentplan.Map(trust["data"])["listener-crls.json"]), s.Intermediate.ID) {
				t.Fatal("bootstrap native intermediate CRL installation omitted")
			}
		})
	}
}

func TestCreateServiceRecipesRejectWrongTargetAndUnownedCredential(t *testing.T) {
	for _, mode := range []string{"production", "wrong-root", "wrong-transport", "wrong-namespace", "wrong-session", "missing-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
			switch mode {
			case "production":
				cfg.Environment = "prod"
			case "wrong-root":
				s.Root.RootFingerprint = strings.Repeat("a", 64)
			case "wrong-transport":
				runtime.TransportRootSHA256 = s.Root.RootFingerprint
			case "wrong-namespace":
				s.BootstrapCredentialNamespaces = []string{p.Stack + "-video-cloud"}
			case "wrong-session":
				s.BootstrapCredentialSecret = "different-session"
			case "missing-cleanup":
				s.CleanupBinding.SecretUID = ""
			}
			var err error
			if mode == "wrong-namespace" {
				_, err = environmentCreateClientEnrollmentDefinition(cfg, p, runtime, s, p.Stack+"-account-manager", "account-manager", "account-manager-service-state")
			} else {
				_, err = renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s)
			}
			if err == nil {
				t.Fatal("unqualified target or credential admitted")
			}
		})
	}
}
