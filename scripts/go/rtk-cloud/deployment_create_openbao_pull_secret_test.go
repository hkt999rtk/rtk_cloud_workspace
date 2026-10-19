package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedOpenBaoPullSecretReferenceSurvivesStageAndFinalRendering(t *testing.T) {
	t.Setenv("LKE_IMAGE_PULL_SECRET_NAME", "unrelated-ambient-registry-owner")
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	defs, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	object := createRecipeObject(t, defs, "StatefulSet/"+p.Stack+"-secrets/openbao")
	if refs := certIssuerObjectList(environmentPKIPod(object)["imagePullSecrets"]); len(refs) != 1 || deploymentplan.Map(refs[0])["name"] != "ghcr-pull" {
		t.Fatal("managed host omitted selected existing registry identity")
	}
	for _, selected := range []string{"ghcr-pull", "original-selected-pull"} {
		values, formal, live := existingFileOpenBaoFixture("dev", true)
		values["LKE_IMAGE_PULL_SECRET_NAME"] = selected
		bindings, err := observeEnvironmentBindings("dev", formal, live)
		if err != nil {
			t.Fatal(err)
		}
		final, err := renderEnvironmentOpenBao(values, bindings.Pods[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := applyEnvironmentPodBindings(final, bindings, formal); err != nil {
			t.Fatal(err)
		}
		refs := certIssuerObjectList(environmentPKIPod(final)["imagePullSecrets"])
		if len(refs) != 1 || deploymentplan.Map(refs[0])["name"] != selected {
			t.Fatal("final renderer dropped or borrowed selected registry identity")
		}
		found := false
		for _, reference := range environmentResourceReferences(final) {
			if reference.ID == "Secret/"+p.Stack+"-secrets/"+selected {
				found = true
			}
		}
		if !found {
			t.Fatal("actual image pull dependency was not recognized by planning")
		}
		for _, group := range []string{"containers", "initContainers"} {
			for _, v := range certIssuerObjectList(environmentPKIPod(final)[group]) {
				c := deploymentplan.Map(v)
				want := formal.Images["LKE_OPENBAO_PKI_IMAGE"].Reference
				if c["name"] == "openbao" {
					want = formal.Images["LKE_OPENBAO_IMAGE"].Reference
				}
				if c["image"] != want {
					t.Fatal("registry reference repair changed an immutable image digest")
				}
			}
		}
	}
}

func TestManagedOpenBaoPullSecretQualificationBindsAcceptedOwnerAndType(t *testing.T) {
	for _, mode := range []string{"valid", "omitted-reference", "other-reference", "duplicate-reference", "missing-owner", "wrong-type", "wrong-namespace", "unaccepted-foundation"} {
		t.Run(mode, func(t *testing.T) {
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
			id := "Secret/video-cloud-dev-secrets/ghcr-pull"
			object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-secrets", "name": "openbao"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"imagePullSecrets": []any{deploymentplan.Object{"name": "ghcr-pull"}}}}}}
			secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-secrets", "name": "ghcr-pull"}, "type": "kubernetes.io/dockerconfigjson", "data": deploymentplan.Object{".dockerconfigjson": "docker-credential-marker"}}
			candidate := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(object), Desired: object}}}
			foundation := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: id, Desired: secret}}}
			accepted := deploymentplan.Journal{PlanSHA256: foundation.SHA256(), Accepted: true, Owners: map[string]string{id: "original-pull-owner"}}
			switch mode {
			case "omitted-reference":
				delete(environmentPKIPod(object), "imagePullSecrets")
			case "other-reference":
				environmentPKIPod(object)["imagePullSecrets"] = []any{deploymentplan.Object{"name": "another-registry-owner"}}
			case "duplicate-reference":
				environmentPKIPod(object)["imagePullSecrets"] = []any{deploymentplan.Object{"name": "ghcr-pull"}, deploymentplan.Object{"name": "ghcr-pull"}}
			case "missing-owner":
				delete(accepted.Owners, id)
			case "wrong-type":
				secret["type"] = "Opaque"
				accepted.PlanSHA256 = foundation.SHA256()
			case "wrong-namespace":
				deploymentplan.Map(secret["metadata"])["namespace"] = "video-cloud-dev-video-cloud"
				accepted.PlanSHA256 = foundation.SHA256()
			case "unaccepted-foundation":
				accepted.Accepted = false
			}
			err := validateManagedOpenBaoImagePullSecret(cfg, candidate, foundation, accepted)
			if (mode == "valid") != (err == nil) {
				t.Fatal("candidate registry reference bypassed original owner/type qualification", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "docker-credential-marker") {
				t.Fatal("registry credentials leaked")
			}
		})
	}
}
