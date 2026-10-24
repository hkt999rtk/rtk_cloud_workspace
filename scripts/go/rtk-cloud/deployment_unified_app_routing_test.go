package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The declaration and receipt describe an isolated completed allocation. They
// are not future UIDs supplied to fresh preflight or generated adoption state.
func nativeAppRoutingFixture(t *testing.T) (environmentIdentityBindings, deploymentplan.Object, deploymentplan.Object, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	b := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Owners: map[string]string{}, Pods: []environmentPodBindings{{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api-app-pki", Component: "managed-app-api", NativeTrustInitialization: true}}}
	r := environmentAppRoutingBinding{Schema: "rtk-cloud.native-app-routing/v1", Environment: b.Environment, Stack: b.Stack, Mode: "internal-service", ServiceUID: "allocated-original-app-service", DeploymentUID: "allocated-original-app-listener", CreatePlanSHA256: strings.Repeat("a", 64), DeclarationUID: "allocated-original-app-declaration"}
	declaration := environmentAppRoutingDefinition(r).Object
	deploymentplan.Map(declaration["metadata"])["uid"] = r.DeclarationUID
	service := environmentAppInternalServiceDependencies(map[string]string{"CLOUD_STACK_NAME": b.Stack})[0].Object
	deploymentplan.Map(service["metadata"])["uid"] = r.ServiceUID
	b.Owners[deploymentplan.ID(declaration)] = r.DeclarationUID
	b.Owners[deploymentplan.ID(service)] = r.ServiceUID
	b.Owners["Deployment/"+b.Stack+"-video-cloud/video-cloud-api-app-pki"] = r.DeploymentUID
	relative := filepath.Join("pki", "app", "routing.json")
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, r); err != nil {
		t.Fatal(err)
	}
	return b, declaration, service, filepath.Join(store.Root, relative)
}

func TestNativeAppRoutingPreservesExplicitOriginalInternalService(t *testing.T) {
	b, declaration, service, receipt := nativeAppRoutingFixture(t)
	if err := observeEnvironmentAppRouting(&b, declaration, []deploymentplan.Object{service}); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironmentAppRoutingBinding(b); err != nil {
		t.Fatal(err)
	}
	definitions, err := environmentAppDependencies(map[string]string{"CLOUD_STACK_NAME": b.Stack}, b)
	if err != nil || len(definitions) != 3 {
		t.Fatal("explicit internal App dependencies could not be retained", err)
	}
	for _, definition := range definitions {
		if definition.Object["kind"] == "Ingress" {
			t.Fatal("internal route invented a public ingress")
		}
	}
	if !deploymentplan.Matches(deploymentplan.Projection(declaration), deploymentplan.Projection(definitions[2].Object)) {
		t.Fatal("canonical target changed the original immutable routing declaration")
	}
	info, err := os.Stat(receipt)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("routing allocation evidence is not a private canonical file", err)
	}
	b.AppRouting = nil
	if _, err := environmentAppDependencies(map[string]string{"CLOUD_STACK_NAME": b.Stack}, b); err == nil {
		t.Fatal("missing public ingress inferred permission for internal routing")
	}
}

func TestNativeAppRoutingRejectsChangedOrMissingOriginalAllocation(t *testing.T) {
	for _, scenario := range []string{"mutable-declaration", "foreign-environment", "replaced-declaration-uid", "missing-receipt", "public-receipt", "foreign-receipt", "service-uid", "missing-service", "duplicate-service", "changed-service-port", "deployment-uid", "missing-native-profile", "public-ingress-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			b, declaration, service, receipt := nativeAppRoutingFixture(t)
			live := []deploymentplan.Object{service}
			switch scenario {
			case "mutable-declaration":
				declaration["immutable"] = false
			case "foreign-environment":
				b.Environment = "staging"
			case "replaced-declaration-uid":
				deploymentplan.Map(declaration["metadata"])["uid"] = "replacement-declaration"
			case "missing-receipt":
				if err := os.Remove(receipt); err != nil {
					t.Fatal(err)
				}
			case "public-receipt":
				if err := os.Chmod(receipt, 0644); err != nil {
					t.Fatal(err)
				}
			case "foreign-receipt":
				if err := os.WriteFile(receipt, []byte(`{"schema":"rtk-cloud.native-app-routing/v1","environment":"staging"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "service-uid":
				deploymentplan.Map(service["metadata"])["uid"] = "replacement-service"
			case "missing-service":
				live = nil
			case "duplicate-service":
				live = append(live, deploymentplan.Clone(service))
			case "changed-service-port":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"])[0])["targetPort"] = float64(9443)
			case "deployment-uid":
				b.Owners["Deployment/"+b.Stack+"-video-cloud/video-cloud-api-app-pki"] = "replacement-listener"
			case "missing-native-profile":
				b.Pods[0].NativeTrustInitialization = false
			case "public-ingress-conflict":
				b.AppIngress = &environmentAppIngressBinding{Name: "original-public-app", Host: "original.example.invalid"}
			}
			err := observeEnvironmentAppRouting(&b, declaration, live)
			if err == nil {
				err = validateEnvironmentAppRoutingBinding(b)
			}
			if err == nil {
				t.Fatal("changed original internal App routing was accepted")
			}
			if scenario == "missing-receipt" {
				if _, err := os.Stat(receipt); !os.IsNotExist(err) {
					t.Fatal("observation regenerated missing allocation evidence")
				}
			}
		})
	}
}
