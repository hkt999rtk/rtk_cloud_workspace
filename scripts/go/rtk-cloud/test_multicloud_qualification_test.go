package main

import "testing"

func TestMulticloudQualificationSpecsMatchCatalogWithoutOverclaimingWorkflows(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	caseByID := map[string]testCatalogCase{}
	for _, tc := range catalog.Cases {
		caseByID[tc.ID] = tc
	}
	for _, spec := range multicloudQualificationSpecs {
		t.Run(spec.TestID, func(t *testing.T) {
			tc, ok := caseByID[spec.TestID]
			if !ok || tc.Status != "active" || tc.Layer != "integration" {
				t.Fatalf("catalog case is not an active integration case: %+v", tc)
			}
			targets, err := authorizationQualificationTargets(spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := authorizationQualificationSelector(spec, targets); got != tc.Selector {
				t.Fatalf("selector=%q, want %q", got, tc.Selector)
			}
			if err := validateAuthorizationQualificationAssertions(tc, spec); err != nil {
				t.Fatal(err)
			}
			if len(spec.Workflows) != 0 {
				t.Fatal("granular CI evidence must not claim a complete canonical workflow")
			}
		})
	}
}
