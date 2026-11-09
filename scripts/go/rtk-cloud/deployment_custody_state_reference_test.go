package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyStateReferenceBindsOriginalLogicalRecordWithoutPVCUID(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api-identity", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api", StorageAliases: []string{"api-state"}}}}
	ref, err := newEnvironmentCustodyStateReference(cfg, binding, "video-cloud-dev-video-cloud", "api-state", "/identity/private/client.json", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Backend != "openbao-kv-v2" || ref.Mount != "identities" || ref.Generation != binding.Generation || ref.Owner != "api-identity" || ref.RecordKey != environmentCustodyRecordKey("dev/"+binding.Generation+"/api-identity", "/identity/private/client.json") {
		t.Fatal("KV proof did not name the original logical record")
	}
	if environmentCustodyLeaseName("dev/"+binding.Generation+"/api-identity", ref.StatePath) != "pki-state-b39c2fe9345ed9d9b6fdb41c858f540d" {
		t.Fatal("record/Lease wire mapping changed")
	}
	before := deploymentplan.Digest(ref)
	for _, change := range []func(*environmentCustodyStateReference){
		func(r *environmentCustodyStateReference) { r.RecordKey = "foreign/generation/key" },
		func(r *environmentCustodyStateReference) { r.Generation = strings.Repeat("d", 64) },
		func(r *environmentCustodyStateReference) { r.RootSHA256 = binding.OpenBaoRootSHA256 },
		func(r *environmentCustodyStateReference) { r.Namespace = "video-cloud-staging-video-cloud" },
	} {
		changed := *ref
		change(&changed)
		if err := validateEnvironmentCustodyStateReference(&changed, cfg, binding, "video-cloud-dev-video-cloud", "api-state", ref.StatePath, strings.Repeat("c", 64)); err == nil {
			t.Fatal("foreign KV proof accepted")
		}
	}
	if before != deploymentplan.Digest(ref) {
		t.Fatal("validation changed original reference")
	}
	if _, err := newEnvironmentCustodyStateReference(cfg, binding, "video-cloud-dev-video-cloud", "business-data", ref.StatePath, ref.RootSHA256); err == nil {
		t.Fatal("unbound business storage became an identity reference")
	}
	if _, err := newEnvironmentCustodyStateReference(cfg, binding, "video-cloud-dev-video-cloud", "api-state", "/identity/../other.json", ref.RootSHA256); err == nil {
		t.Fatal("noncanonical state path accepted")
	}
}
