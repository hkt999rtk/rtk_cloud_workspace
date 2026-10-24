package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCertIssuerPrepareAliasIsConfinedToNativeOriginalWorkloadCatalog(t *testing.T) {
	if !environmentManagedImageRoleAllowed("certissuer", "certissuer", "certissuer", 0, "LKE_CERTISSUER_IMAGE") || !environmentManagedImageRoleAllowed("certissuer", "certissuer", "certissuer", 0, "LKE_VIDEO_CLOUD_IMAGE") {
		t.Fatal("maintained issuer roles rejected")
	}
	for _, v := range []struct {
		component, workload, container string
		index                          int
	}{{"pki-controller", "pki-controller", "pki-controller", 0}, {"managed-api", "video-cloud-api", "app", 0}, {"certissuer", "factoryenroll", "certissuer", 0}, {"certissuer", "certissuer", "sidecar", 1}, {"existing-workload", "certissuer", "certissuer", 0}, {"certissuer", "certissuer", "certissuer", 1}} {
		if environmentManagedImageRoleAllowed(v.component, v.workload, v.container, v.index, "LKE_CERTISSUER_IMAGE") {
			t.Fatal("repair alias escaped sole native main", v)
		}
	}
}

func TestCertIssuerPrepareAdoptionResolvesTwoFormalAPIRepoDigestsWithoutChangingBusinessRole(t *testing.T) {
	for _, same := range []bool{false, true} {
		env, release, issuer, secrets := registryIssuerCanonicalFixture(t, "dev")
		old := release.Images["LKE_VIDEO_CLOUD_IMAGE"]
		selected := old
		if !same {
			selected.Reference = environmentImageRepository(old.Reference) + "@sha256:" + strings.Repeat("e", 64)
		}
		release.Images["LKE_CERTISSUER_IMAGE"] = selected
		mqttFoundationPrimary(issuer)["image"] = selected.Reference
		business := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "unrelated-api", "namespace": release.Stack + "-video-cloud", "uid": "original-business-owner"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": old.Reference}}}}}}
		observed, err := observeEnvironmentBindings("dev", release, append([]deploymentplan.Object{issuer, business}, secrets...))
		if err != nil {
			t.Fatal(err)
		}
		if err = validateEnvironmentIdentityBindings(observed, "dev"); err != nil {
			t.Fatal(err)
		}
		var chosen environmentPodBindings
		seen := 0
		for _, b := range observed.Pods {
			if b.Name == "certissuer" {
				chosen = b
				if b.Containers[0].ImageKey != "LKE_CERTISSUER_IMAGE" {
					t.Fatal("issuer lost formal repair role")
				}
				seen++
			}
			if b.Name == "unrelated-api" {
				if b.Containers[0].ImageKey != "LKE_VIDEO_CLOUD_IMAGE" {
					t.Fatal("business stole issuer alias")
				}
				seen++
			}
		}
		if seen != 2 {
			t.Fatal("native owner inventory missing")
		}
		rendered, err := renderManagedEnvironmentComponent(env, chosen)
		if err != nil {
			t.Fatal(err)
		}
		if err = applyEnvironmentPodBindings(rendered, observed, release); err != nil {
			t.Fatal(err)
		}
		if mqttFoundationPrimary(rendered)["image"] != selected.Reference {
			t.Fatal("full renderer reverted accepted issuer image")
		}
	}
}
