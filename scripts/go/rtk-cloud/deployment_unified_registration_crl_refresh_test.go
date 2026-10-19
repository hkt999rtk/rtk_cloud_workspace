package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func registrationCRLRefreshFixture(t *testing.T) (deploymentConfig, deploymentplan.Object, string) {
	t.Helper()
	cfg, p, s, cert, key, crl, defs := accountRegistrationFixture(t)
	out, err := renderEnvironmentCreateAccountRegistration(cfg, p, s, cert, key, crl, defs)
	if err != nil {
		t.Fatal(err)
	}
	o := deploymentplan.Clone(createRecipeObject(t, out, "Deployment/"+p.Stack+"-account-manager/account-manager"))
	return cfg, o, "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
}
func TestAccountRegistrationCRLRefreshFiniteDefinitionPreservesPrivateBoundary(t *testing.T) {
	cfg, o, image := registrationCRLRefreshFixture(t)
	before := deploymentplan.Digest(o)
	d, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, o, image)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(o) != before {
		t.Fatal("original mutated")
	}
	app := managedNamedField(environmentPKIPod(d.Object)["containers"], "app")
	manager := managedNamedField(environmentPKIPod(d.Object)["containers"], "pkimanagement")
	if mqttFoundationEnv(app, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL") != accountRegistrationCRLBundlePath || mqttFoundationEnv(manager, accountRegistrationCRLExportEnv) != accountRegistrationCRLBundlePath || manager["image"] != image {
		t.Fatal("missing exact export")
	}
	// Reverse only the finite permitted fields; everything else must be literal.
	copy := deploymentplan.Clone(d.Object)
	pod := environmentPKIPod(copy)
	originalPod := environmentPKIPod(o)
	managedNamedField(pod["containers"], "pkimanagement")["image"] = managedNamedField(originalPod["containers"], "pkimanagement")["image"]
	managedNamedField(pod["containers"], "pkimanagement")["env"] = managedNamedField(originalPod["containers"], "pkimanagement")["env"]
	managedNamedField(pod["containers"], "app")["env"] = managedNamedField(originalPod["containers"], "app")["env"]
	pod["initContainers"], pod["volumes"] = originalPod["initContainers"], originalPod["volumes"]
	if deploymentplan.Digest(copy) != before {
		t.Fatal("unrelated change")
	}
	again, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, d.Object, image)
	if err != nil || deploymentplan.Digest(again.Object) != deploymentplan.Digest(d.Object) {
		t.Fatal("profile not idempotent", err)
	}
	seed := managedNamedField(environmentPKIPod(d.Object)["volumes"], accountRegistrationCRLSeedVolume)
	secret := deploymentplan.Map(seed["secret"])
	items := certIssuerObjectList(secret["items"])
	if secret["secretName"] != "account-manager-service-registration-tls" || secret["defaultMode"] != float64(256) || len(items) != 1 || deploymentplan.Digest(items[0]) != deploymentplan.Digest(deploymentplan.Object{"key": "client.crl", "path": "client.crl"}) {
		t.Fatal("public-only Kubernetes key/path projection differs")
	}
	if deploymentplan.Digest(seed) != deploymentplan.Digest(accountRegistrationCRLSeed()) {
		t.Fatal("public projection changed")
	}
}
func TestAccountRegistrationCRLRefreshRefusesForeignOrPrivateProjection(t *testing.T) {
	for _, mode := range []string{"staging", "tag", "foreign", "missing-owner", "extra-container", "private-app-mount", "wrong-socket", "first-trust", "missing-crl-manifest", "extra-seed-key", "unknown-init", "missing-fsgroup", "foreign-user"} {
		t.Run(mode, func(t *testing.T) {
			cfg, o, image := registrationCRLRefreshFixture(t)
			p := environmentPKIPod(o)
			manager := managedNamedField(p["containers"], "pkimanagement")
			switch mode {
			case "staging":
				cfg.Environment = "staging"
			case "tag":
				image = "example.invalid/image:latest"
			case "foreign":
				deploymentplan.Map(o["metadata"])["namespace"] = "foreign"
			case "missing-owner":
				p["containers"] = []any{managedNamedField(p["containers"], "app")}
			case "extra-container":
				p["containers"] = append(certIssuerObjectList(p["containers"]), deploymentplan.Object{"name": "other"})
			case "private-app-mount":
				app := managedNamedField(p["containers"], "app")
				app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), environmentCreateServiceMount("service-state", "/private", true))
			case "wrong-socket":
				registrationCRLSetEnvironment(manager, "PKI_MANAGEMENT_SOCKET", "/foreign.sock")
			case "first-trust":
				registrationCRLSetEnvironment(manager, "PKI_MANAGEMENT_FIRST_TRUST_ONLY", "true")
			case "missing-crl-manifest":
				registrationCRLSetEnvironment(manager, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST", "")
			case "extra-seed-key":
				v := accountRegistrationCRLSeed()
				deploymentplan.Map(v["secret"])["items"] = append(certIssuerObjectList(deploymentplan.Map(v["secret"])["items"]), deploymentplan.Object{"key": "tls.key", "path": "tls.key"})
				p["volumes"] = append(certIssuerObjectList(p["volumes"]), v)
			case "missing-fsgroup":
				delete(deploymentplan.Map(p["securityContext"]), "fsGroup")
			case "foreign-user":
				deploymentplan.Map(p["securityContext"])["runAsUser"] = float64(999)
			case "unknown-init":
				deploymentplan.Map(certIssuerObjectList(p["initContainers"])[0])["command"] = []any{"sh", "-ec", "echo foreign"}
			}
			if _, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, o, image); err == nil {
				t.Fatal("unqualified projection accepted")
			}
		})
	}
}

func TestAccountRegistrationCRLRefreshInitializerObservationUsesExactKnownRecipe(t *testing.T) {
	cfg, o, image := registrationCRLRefreshFixture(t)
	d, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, o, image)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(d.Object)
	app := managedNamedField(pod["containers"], "app")
	manager := managedNamedField(pod["containers"], "pkimanagement")
	initializer := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
	_, _, release := completeEnvironmentRenderFixture("dev")
	v := release.Images["LKE_ALPINE_IMAGE"]
	v.Reference = deploymentplan.String(initializer["image"])
	release.Images["LKE_ALPINE_IMAGE"] = v
	if err := validateObservedAccountManagerSocketInitializer(deploymentplan.ID(d.Object), initializer, release); err != nil {
		t.Fatal(err)
	}
	binding := environmentPodBindings{Name: "account-manager", PodFields: map[string]any{"volumes": pod["volumes"]}, Containers: []environmentContainerBindings{{Name: "app", Fields: map[string]any{"env": app["env"], "volumeMounts": app["volumeMounts"]}}, {Name: "pkimanagement", Fields: map[string]any{"env": manager["env"], "volumeMounts": manager["volumeMounts"]}}}, InitContainers: []environmentContainerBindings{{Name: "management-socket-directory", ImageKey: "LKE_ALPINE_IMAGE", Fields: map[string]any{"volumeMounts": initializer["volumeMounts"]}}}}
	if err := applyAccountManagerSocketProfile(d.Object, binding, true, release); err != nil {
		t.Fatal(err)
	}
	current := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["initContainers"])[0])
	if deploymentplan.Digest(current["command"]) != deploymentplan.Digest([]any{"/bin/sh", "-ec", accountManagerPublicCRLPreparation}) {
		t.Fatal("new profile rendered legacy unseeded initializer")
	}
	bad := deploymentplan.Clone(initializer)
	bad["command"] = []any{"sh", "-ec", accountManagerPublicCRLPreparation + "\ncat /private/key"}
	if validateObservedAccountManagerSocketInitializer(deploymentplan.ID(d.Object), bad, release) == nil {
		t.Fatal("arbitrary init recipe accepted")
	}
}
