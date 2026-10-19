package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestManagedLateCompletedAccountExportKeepsOriginalNativeOwner(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, s)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-account-manager/account-manager")
	name := "account-manager." + p.Stack + "-account-manager.svc.cluster.local"
	check := func(o deploymentplan.Object) bool {
		return managedLateCompletedAccountExportRecipe(o, "account-manager-service-state", environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", name, s.Root.RootFingerprint)
	}
	if !check(original) {
		t.Fatal("maintained permanent owner could not perform read-only same-state export")
	}
	before := deploymentplan.Digest(original)
	for _, mode := range []string{"mutable-image", "foreign-command", "client-path", "server-path", "root", "name", "foreign-claim", "readonly-state", "subpath", "duplicate-sidecar", "duplicate-volume", "missing-state"} {
		t.Run(mode, func(t *testing.T) {
			o := deploymentplan.Clone(original)
			pod := environmentPKIPod(o)
			c := managedNamedField(pod["containers"], "pkimanagement")
			switch mode {
			case "mutable-image":
				c["image"] = "image:latest"
			case "foreign-command":
				c["command"] = []any{"/bin/sh", "-ec", "/app/pkimanagement"}
			case "client-path":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_MANAGEMENT_IDENTITY_STATE": "/foreign/client.json"}))
			case "server-path":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE": "/foreign/server.json"}))
			case "root":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256": strings.Repeat("f", 64)}))
			case "name":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_MANAGEMENT_ACCOUNT_HOST_NAME": "foreign"}))
			case "foreign-claim":
				deploymentplan.Map(managedNamedField(pod["volumes"], "service-state")["persistentVolumeClaim"])["claimName"] = "foreign"
			case "readonly-state":
				managedNamedField(c["volumeMounts"], "service-state")["readOnly"] = true
			case "subpath":
				managedNamedField(c["volumeMounts"], "service-state")["subPath"] = "other"
			case "duplicate-sidecar":
				pod["containers"] = append(certIssuerObjectList(pod["containers"]), c)
			case "duplicate-volume":
				pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), managedNamedField(pod["volumes"], "service-state"))
			case "missing-state":
				pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "service-state")
			}
			if check(o) {
				t.Fatal("changed permanent native export owner was accepted")
			}
		})
	}
	if deploymentplan.Digest(original) != before {
		t.Fatal("read-only export recipe validation changed original owner")
	}
}
