package main

import (
	"context"
	"encoding/base64"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeAccountCanonicalRenderingKeepsSeparateRegistrationAndManagedAPIPorts(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-canonical-account")
			makeIsolatedTestSecretStore(t, environment)
			cfg, p, release, runtime, service, _, _, _ := createApplicationFinalizeFixture(t, environment)
			fresh, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			current := deploymentplan.Clone(createRecipeObject(t, fresh, "Deployment/"+p.Stack+"-account-manager/account-manager"))
			currentService := deploymentplan.Clone(createRecipeObject(t, fresh, "Service/"+p.Stack+"-account-manager/account-manager"))
			deploymentplan.Map(current["metadata"])["uid"], deploymentplan.Map(currentService["metadata"])["uid"] = "original-native-account", "original-native-account-service"
			before := deploymentplan.Digest([]deploymentplan.Object{current, currentService})
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current, currentService})
			if err != nil {
				t.Fatal(err)
			}
			env, bindings, _ := completeEnvironmentRenderFixture(environment)
			env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "true", "true", "9444"
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			bindings.Pods = append(bindings.Pods, observed.Pods...)
			for id, uid := range observed.Owners {
				bindings.Owners[id] = uid
			}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			final, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			account := createRecipeObject(t, final, "Deployment/"+p.Stack+"-account-manager/account-manager")
			main := mqttFoundationPrimary(account)
			manager := managedNamedField(environmentPKIPod(account)["containers"], "pkimanagement")
			if managedNamedField(main["ports"], "service-reg")["containerPort"] != float64(9444) || managedNamedField(manager["ports"], "service-mtls")["containerPort"] != float64(18443) || mqttFoundationEnv(manager, "PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR") != ":18443" {
				t.Fatal("registration and managed API listeners collided during canonical adoption")
			}
			finalService := createRecipeObject(t, final, "Service/"+p.Stack+"-account-manager/account-manager")
			ports := deploymentplan.Map(finalService["spec"])["ports"]
			registration, managed := managedNamedField(ports, "service-reg"), managedNamedField(ports, "service-mtls")
			if registration["port"] != float64(8443) || registration["targetPort"] != "service-reg" || managed["port"] != float64(18443) || managed["targetPort"] != "service-mtls" {
				t.Fatal("canonical Service replaced an original registration or managed API target", ports)
			}
			if deploymentplan.Digest([]deploymentplan.Object{current, currentService}) != before {
				t.Fatal("canonical rendering changed original Account owners")
			}
			bad := deploymentplan.Clone(finalService)
			managedNamedField(deploymentplan.Map(bad["spec"])["ports"], "service-mtls")["targetPort"] = "service-reg"
			if err := applyAccountManagerServicePorts(bad, bindings); err == nil {
				t.Fatal("native managed Service silently accepted a registration target collision")
			}
		})
	}
}
