package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCustodyInspectionUsesOriginalScopeWithoutPrivateStorageOrControllerBoot(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "60", "600"
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	owner := binding.Owners[0]
	path := environmentCreateServiceOwnerIdentityPath(owner.ID, "client.json")
	ref, err := newEnvironmentCustodyStateReference(cfg, binding, owner.Namespace, owner.StorageAliases[0], path, service.Root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	var source deploymentplan.Object
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == "Deployment/"+owner.Namespace+"/"+owner.ID {
			source = deploymentplan.Clone(environmentPKIPod(d.Object))
		}
	}
	if source == nil {
		t.Fatal("original source recipe unavailable")
	}
	deploymentplan.Map(source["securityContext"])["runAsUser"] = float64(4242)
	original := deploymentplan.Digest(source)
	job, err := environmentCustodyInspectionJob(cfg, binding, ref, source, p.Image, "", "", "service:"+owner.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(job)
	if pod["serviceAccountName"] != owner.ServiceAccount || deploymentplan.Map(pod["securityContext"])["runAsUser"] != float64(4242) {
		t.Fatal("inspection substituted scope or execution settings")
	}
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if container["image"] != p.Image || mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX") != "dev/"+binding.Generation+"/"+owner.ID || mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID") != "" {
		t.Fatal("inspection uses a different original scope or bootstrap session")
	}
	if deploymentplan.Digest(container["command"]) != deploymentplan.Digest([]string{"/app/serviceidentity-bootstrap", "inspect-admitted", path, "-", "service:" + owner.ID, ref.RootSHA256, "-"}) {
		t.Fatal("inspection did not select native read-only admission")
	}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(raw)["persistentVolumeClaim"] != nil || deploymentplan.Map(raw)["secret"] != nil {
			t.Fatal("inspection depends on private storage or key mirrors")
		}
	}
	if deploymentplan.Digest(source) != original {
		t.Fatal("inspection changed original workload recipe")
	}
	foreign := *ref
	foreign.RecordKey = "foreign/record"
	if _, err := environmentCustodyInspectionJob(cfg, binding, &foreign, source, p.Image, "", "", "service:"+owner.ID, nil); err == nil {
		t.Fatal("inspection accepted foreign KV scope")
	}
}
