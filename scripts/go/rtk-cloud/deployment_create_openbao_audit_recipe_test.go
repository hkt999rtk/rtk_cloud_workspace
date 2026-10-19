package main

import (
	"context"
	"maps"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedOpenBaoRecipeRetainsOriginalDeclarativeRedactedAudit(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	foundationConfig := cfg
	foundationConfig.Values = maps.Clone(cfg.Values)
	foundationConfig.Values["OPENBAO_STORAGE_BACKEND"], foundationConfig.Values["OPENBAO_EFFECTIVE_REPLICAS"] = "file", "1"
	foundationConfig.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	foundation, err := renderEnvironmentCreateOpenBaoFoundation(foundationConfig, release, "original-transport", "original-ca")
	if err != nil {
		t.Fatal(err)
	}
	originalHCL := deploymentplan.String(deploymentplan.Map(createRecipeObject(t, foundation, "ConfigMap/"+p.Stack+"-secrets/openbao-file")["data"])["file.hcl"])
	auditAt := strings.Index(originalHCL, `audit "file" "deployment-audit"`)
	if auditAt < 0 {
		t.Fatal("original declarative audit policy is absent")
	}
	originalAudit := originalHCL[auditAt:]
	definitions, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	hcl := deploymentplan.String(deploymentplan.Map(createRecipeObject(t, definitions, "ConfigMap/"+p.Stack+"-secrets/openbao-file")["data"])["file.hcl"])
	if !strings.HasSuffix(hcl, originalAudit) || strings.Count(hcl, `audit "`) != 1 || strings.Contains(hcl, "unsafe_allow_api_audit_creation") || !strings.Contains(hcl, `path = "/openbao/data"`) || !strings.Contains(hcl, `tls_cert_file = "/run/openbao-pki/private/current/chain.pem"`) || !strings.Contains(hcl, `tls_key_file = "/run/openbao-pki/private/current/key.pem"`) {
		t.Fatal("managed provider dropped original dedicated redacted audit policy or changed storage/TLS identity paths")
	}
	openbao := createRecipeObject(t, definitions, "StatefulSet/"+p.Stack+"-secrets/openbao")
	main := managedNamedField(environmentPKIPod(openbao)["containers"], "openbao")
	audit := managedNamedField(main["volumeMounts"], "audit")
	if audit["mountPath"] != "/openbao/audit" || audit["readOnly"] != false {
		t.Fatal("managed provider changed the original audit PVC mount")
	}
	claims := map[string]bool{}
	for _, value := range certIssuerObjectList(deploymentplan.Map(openbao["spec"])["volumeClaimTemplates"]) {
		claims[deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["metadata"])["name"])] = true
	}
	if len(claims) != 2 || !claims["audit"] || !claims["data"] {
		t.Fatal("managed provider omitted its original independent audit/data claim templates")
	}
}
