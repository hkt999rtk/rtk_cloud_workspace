package main

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func nativeControllerAcceptedRecipeFixture(t *testing.T) (map[string]string, environmentIdentityBindings, environmentRelease, deploymentplan.Object) {
	t.Helper()
	env, bindings, release := completeEnvironmentRenderFixture("dev")
	cfg, p, runtime, ceremony := createServiceRecipeFixture(t, "dev")
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, ceremony)
	if err != nil {
		t.Fatal(err)
	}
	id := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
	current := deploymentplan.Clone(createRecipeObject(t, definitions, id))
	deploymentplan.Map(current["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	observed, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{current})
	if err != nil || len(observed.Pods) != 1 {
		t.Fatal("accepted native Controller references could not be observed", err)
	}
	for i := range bindings.Pods {
		if bindings.Pods[i].Component == "pki-controller" {
			bindings.Pods[i] = observed.Pods[0]
		}
	}
	bindings.Owners[id] = observed.Owners[id]
	bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	return env, bindings, release, current
}

func TestNativeControllerFullRendererPreservesAcceptedServingMode(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-native-controller-render")
	env, bindings, release, current := nativeControllerAcceptedRecipeFixture(t)
	restore := installAllCredentialEnvironment(env)
	defer restore()
	before, boundBefore := deploymentplan.Digest(current), deploymentplan.Digest(bindings)
	definitions, err := renderEnvironmentResources(env, bindings, release)
	if err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(current)
	wanted := createRecipeObject(t, definitions, id)
	if err := validateEnvironmentPKITransition(definitions, []deploymentplan.Object{current}); err != nil {
		t.Fatal("complete target changed the accepted native serving identity", err)
	}
	pod := environmentPKIPod(wanted)
	container := managedNamedField(pod["containers"], "pki-controller")
	for _, name := range []string{"PKI_SERVER_CERT", "PKI_SERVER_KEY"} {
		if managedNamedField(container["env"], name) != nil {
			t.Fatal("legacy static serving credential was reintroduced")
		}
	}
	if managedNamedField(pod["volumes"], "pki-tls") != nil || managedNamedField(container["volumeMounts"], "pki-tls") != nil || managedNamedField(pod["volumes"], "account-key") != nil || managedNamedField(container["volumeMounts"], "account-key") != nil {
		t.Fatal("complete target invented an unauthenticated static TLS Secret mount")
	}
	for _, definition := range definitions {
		for _, dependency := range definition.Dependencies {
			if dependency == "Secret/"+bindings.Stack+"-video-cloud/pki-controller-tls" || dependency == "ConfigMap/"+bindings.Stack+"-video-cloud/pki-account-manager-public-key" {
				t.Fatal("complete target kept an orphan legacy serving Secret dependency")
			}
		}
	}
	if container["command"].([]any)[0] != "/app/pkicontroller" || managedNamedField(container["env"], "PKI_APP_CRL_WORKER_ENABLED")["value"] != "true" {
		t.Fatal("identity preservation imported old process behavior")
	}
	if deploymentplan.Digest(current) != before || deploymentplan.Digest(bindings) != boundBefore {
		t.Fatal("rendering changed the accepted desired source or bindings")
	}
	// The same security predicate still rejects a changed permanent identity.
	for _, mode := range []string{"state", "root", "claim", "trust", "static-credential"} {
		t.Run(mode, func(t *testing.T) {
			changed := deploymentplan.Clone(wanted)
			c := managedNamedField(environmentPKIPod(changed)["containers"], "pki-controller")
			switch mode {
			case "state":
				managedNamedField(c["env"], "PKI_HOST_IDENTITY_STATE")["value"] = "/different/server.json"
			case "root":
				managedNamedField(c["env"], "PKI_HOST_ROOT_SHA256")["value"] = strings.Repeat("c", 64)
			case "claim":
				deploymentplan.Map(managedNamedField(environmentPKIPod(changed)["volumes"], "service-state")["persistentVolumeClaim"])["claimName"] = "foreign-state"
			case "trust":
				deploymentplan.Map(managedNamedField(environmentPKIPod(changed)["volumes"], "service-trust")["configMap"])["name"] = "foreign-trust"
			case "static-credential":
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "PKI_SERVER_KEY", "value": "/foreign/key.pem"})
			}
			if validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: changed}}, []deploymentplan.Object{current}) == nil {
				t.Fatal("native serving adaptation weakened the identity transition guard")
			}
		})
	}
}

func TestNativeControllerRendererRefusesUnqualifiedStateProfile(t *testing.T) {
	_, bindings, release, _ := nativeControllerAcceptedRecipeFixture(t)
	var source environmentPodBindings
	for _, binding := range bindings.Pods {
		if binding.Component == "pki-controller" {
			source = binding
		}
	}
	for _, mode := range []string{"host-path", "client-path", "root", "claim", "trust", "readonly-state", "writable-trust", "subpath", "static-setting", "source-static-volume", "duplicate-env"} {
		t.Run(mode, func(t *testing.T) {
			bound := source
			bound.PodFields = deploymentplan.Clone(source.PodFields)
			bound.Containers = append([]environmentContainerBindings(nil), source.Containers...)
			bound.Containers[0].Fields = deploymentplan.Clone(source.Containers[0].Fields)
			fields := bound.Containers[0].Fields
			switch mode {
			case "host-path":
				managedNamedField(fields["env"], "PKI_HOST_IDENTITY_STATE")["value"] = "/foreign/server.json"
			case "client-path":
				managedNamedField(fields["env"], "PKI_SERVICE_CLIENT_IDENTITY_STATE")["value"] = "/foreign/client.json"
			case "root":
				managedNamedField(fields["env"], "PKI_HOST_ROOT_SHA256")["value"] = strings.Repeat("c", 64)
			case "claim":
				deploymentplan.Map(managedNamedField(bound.PodFields["volumes"], "service-state")["persistentVolumeClaim"])["claimName"] = "foreign-state"
			case "trust":
				deploymentplan.Map(managedNamedField(bound.PodFields["volumes"], "service-trust")["configMap"])["name"] = "foreign-trust"
			case "readonly-state":
				managedNamedField(fields["volumeMounts"], "service-state")["readOnly"] = true
			case "writable-trust":
				managedNamedField(fields["volumeMounts"], "service-trust")["readOnly"] = false
			case "subpath":
				managedNamedField(fields["volumeMounts"], "service-state")["subPath"] = "different"
			case "static-setting":
				fields["env"] = append(certIssuerObjectList(fields["env"]), deploymentplan.Object{"name": "PKI_SERVER_CERT", "value": "/run/pki-tls/tls.crt"})
			case "source-static-volume":
				bound.PodFields["volumes"] = append(certIssuerObjectList(bound.PodFields["volumes"]), deploymentplan.Object{"name": "pki-tls", "secret": deploymentplan.Object{"secretName": "original-static"}})
			case "duplicate-env":
				fields["env"] = append(certIssuerObjectList(fields["env"]), managedNamedField(fields["env"], "PKI_HOST_IDENTITY_STATE"))
			}
			env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": bindings.Stack}
			object, err := renderManagedEnvironmentComponent(env, bound)
			if err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest(bound)
			b := environmentIdentityBindings{Stack: bindings.Stack, Pods: []environmentPodBindings{bound}, Owners: bindings.Owners}
			if applyEnvironmentPodBindings(object, b, release) == nil {
				t.Fatal("unqualified native serving profile accepted")
			}
			if deploymentplan.Digest(bound) != before {
				t.Fatal("profile refusal changed the source references")
			}
		})
	}
}
