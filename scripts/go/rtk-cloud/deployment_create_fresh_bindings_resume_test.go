package main

import (
	"strings"
	"testing"
)

func TestFreshBootstrapBindingsResumePreservesOriginalIdentityAndAllowsNewResources(t *testing.T) {
	parent, scope := strings.Repeat("a", 64), strings.Repeat("b", 64)
	old := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Owners: map[string]string{
		"StatefulSet/video-cloud-dev-secrets/openbao":       "original-openbao",
		"Deployment/video-cloud-dev-video-cloud/certissuer": "original-issuer",
		"Secret/video-cloud-dev-video-cloud/product-mirror": "original-mirror",
		"Job/video-cloud-dev-video-cloud/temporary-job":     "old-job",
	}, ProductIdentities: &environmentFreshProductBinding{ParentSHA256: parent, ScopeSHA256: scope, SessionID: "11111111-1111-4111-8111-111111111111", IssuerID: "22222222-2222-4222-8222-222222222222", EnrollmentSessionID: "33333333-3333-4333-8333-333333333333"}, Custody: &environmentCustodyBinding{Generation: parent, OpenBaoRootSHA256: strings.Repeat("c", 64), ServiceRootSHA256: strings.Repeat("d", 64), Owners: []environmentCustodyOwnerBinding{{ID: "certissuer", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "certissuer"}}}}
	outputs := []environmentCreateProductIdentity{{ID: "Secret/video-cloud-dev-video-cloud/product-mirror"}}
	for _, mode := range []string{"unchanged", "new-resources", "renewed-session", "replaced-openbao", "replaced-owner", "replaced-mirror", "foreign-scope", "foreign-custody"} {
		t.Run(mode, func(t *testing.T) {
			var current environmentIdentityBindings
			if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(old), &current); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "new-resources":
				current.Owners["Secret/video-cloud-dev-ingress/public-tls"] = "new-tls"
				delete(current.Owners, "Job/video-cloud-dev-video-cloud/temporary-job")
			case "renewed-session":
				current.ProductIdentities.SessionID = "44444444-4444-4444-8444-444444444444"
			case "replaced-openbao":
				current.Owners["StatefulSet/video-cloud-dev-secrets/openbao"] = "replacement"
			case "replaced-owner":
				current.Owners["Deployment/video-cloud-dev-video-cloud/certissuer"] = "replacement"
			case "replaced-mirror":
				current.Owners["Secret/video-cloud-dev-video-cloud/product-mirror"] = "replacement"
			case "foreign-scope":
				current.ProductIdentities.ScopeSHA256 = strings.Repeat("e", 64)
			case "foreign-custody":
				current.Custody.OpenBaoRootSHA256 = strings.Repeat("f", 64)
			}
			err := validateFreshBootstrapBindingsContinuity("dev", parent, scope, outputs, old, current)
			valid := mode == "unchanged" || mode == "new-resources" || mode == "renewed-session"
			if (err == nil) != valid {
				t.Fatal("unexpected original binding continuity", mode, err)
			}
		})
	}
}
