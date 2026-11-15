package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateOuterAndCompiledBindingsUseSameSelectedImageRoles(t *testing.T) {
	cfg, object, image := registrationCRLRefreshFixture(t)
	_, _, release := completeEnvironmentRenderFixture("dev")
	delete(release.Images, "LKE_PKI_MANAGEMENT_IMAGE")
	deploymentplan.Map(object["metadata"])["uid"] = "01000000-0000-4000-8000-000000000001"
	pod := environmentPKIPod(object)
	managedNamedField(pod["containers"], "app")["image"] = release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference
	managedNamedField(pod["containers"], "pkimanagement")["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	managedNamedField(pod["initContainers"], "management-socket-directory")["image"] = release.Images["LKE_ALPINE_IMAGE"].Reference
	operator := map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": image}
	live := []deploymentplan.Object{object}
	selected, err := accountRegistrationCRLRefreshRelease(cfg, release, operator)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := observeEnvironmentTargetBindings(t.Context(), cfg, selected, "create", live)
	if err != nil {
		t.Fatal("compiler observation", err)
	}
	actual, err := observeEnvironmentCreateTargetBindings(t.Context(), cfg, release, operator, live)
	if err != nil {
		t.Fatal("outer observation", err)
	}
	if deploymentplan.Digest(actual) != deploymentplan.Digest(wanted) {
		t.Fatal("same original resource acquired different bindings solely from the selected image role")
	}
	if actual.Owners[deploymentplan.ID(object)] != deploymentplan.Map(object["metadata"])["uid"] {
		t.Fatal("original owner lost")
	}
	for _, c := range actual.Pods[0].Containers {
		if c.Name == "pkimanagement" && c.ImageKey != "LKE_PKI_MANAGEMENT_IMAGE" {
			t.Fatal("environment-selected management role was not used")
		}
	}
	changed := deploymentplan.Clone(object)
	deploymentplan.Map(changed["metadata"])["uid"] = "02000000-0000-4000-8000-000000000002"
	foreign, err := observeEnvironmentCreateTargetBindings(t.Context(), cfg, release, operator, []deploymentplan.Object{changed})
	if err != nil || deploymentplan.Digest(foreign) == deploymentplan.Digest(actual) {
		t.Fatal("original strict binding fence no longer detects replaced owners", err)
	}
}

func TestCreateReadbackAndCompilerPreferCanonicalEnvironmentRuntimeRoles(t *testing.T) {
	for _, preselected := range []bool{false, true} {
		t.Run(strings.TrimSpace(map[bool]string{false: "original", true: "compiled"}[preselected]), func(t *testing.T) {
			cfg, object, old := registrationCRLRefreshFixture(t)
			_, _, release := completeEnvironmentRenderFixture("dev")
			delete(release.Images, "LKE_PKI_MANAGEMENT_IMAGE")
			selected := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("9", 64), SourceRevision: strings.Repeat("8", 40)}
			selection := environmentImageSelection{Environment: "dev", Images: map[string]environmentReleaseImage{"LKE_PKI_MANAGEMENT_IMAGE": selected, "LKE_PKI_CONTROLLER_IMAGE": selected}}
			operator := map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": old, "PKI_CONTROLLER_IMAGE": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, environmentImagesKey: string(mustProviderRecoveryJSON(selection))}
			deploymentplan.Map(object["metadata"])["uid"] = "01000000-0000-4000-8000-000000000001"
			pod := environmentPKIPod(object)
			managedNamedField(pod["containers"], "app")["image"] = release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference
			managedNamedField(pod["containers"], "pkimanagement")["image"] = selected.Reference
			managedNamedField(pod["initContainers"], "management-socket-directory")["image"] = release.Images["LKE_ALPINE_IMAGE"].Reference
			if preselected {
				for role, image := range selection.Images {
					release.Images[role] = image
				}
			}
			before, opBefore := deploymentplan.Digest(release), deploymentplan.Digest(operator)
			actual, err := observeEnvironmentCreateTargetBindings(t.Context(), cfg, release, operator, []deploymentplan.Object{object})
			if err != nil {
				t.Fatal("public-TLS readback must use canonical environment selection", err)
			}
			next, err := accountRegistrationCRLRefreshRelease(cfg, release, operator)
			if err != nil || deploymentplan.Digest(next.Images["LKE_PKI_MANAGEMENT_IMAGE"]) != deploymentplan.Digest(selected) || deploymentplan.Digest(next.Images["LKE_PKI_CONTROLLER_IMAGE"]) != deploymentplan.Digest(selected) {
				t.Fatal("compiler changed or rejected selected role evidence", err)
			}
			for _, c := range actual.Pods[0].Containers {
				if c.Name == "pkimanagement" && c.ImageKey != "LKE_PKI_MANAGEMENT_IMAGE" {
					t.Fatal("selected role lost")
				}
			}
			if deploymentplan.Digest(release) != before || deploymentplan.Digest(operator) != opBefore {
				t.Fatal("source environment/release mutated")
			}
		})
	}
}

func TestCanonicalPKIRolesDoNotEnableLegacyRegistrationProfile(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			cfg := deploymentConfig{Environment: env}
			image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("9", 64), SourceRevision: strings.Repeat("8", 40)}
			roles := map[string]environmentReleaseImage{"LKE_PKI_MANAGEMENT_IMAGE": image, "LKE_PKI_CONTROLLER_IMAGE": image}
			op := map[string]string{environmentImagesKey: string(mustProviderRecoveryJSON(environmentImageSelection{Environment: env, Images: roles}))}
			before := deploymentplan.Digest(op)
			release := environmentRelease{Environment: env, Stack: "video-cloud-" + env, Images: map[string]environmentReleaseImage{}}
			for i := 0; i < 2; i++ {
				var err error
				release, err = accountRegistrationCRLRefreshRelease(cfg, release, op)
				if err != nil || deploymentplan.Digest(release.Images) != deploymentplan.Digest(roles) {
					t.Fatal("selected role evidence not idempotent", err)
				}
			}
			values := map[string]string{}
			if err := projectAccountRegistrationCRLRefreshInputs(env, op, values); err != nil || values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE"] != "" || values["PKI_MANAGEMENT_IMAGE"] != "" || values["PKI_CONTROLLER_IMAGE"] != image.Reference {
				t.Fatal("role selection enabled optional feature or changed controller", err)
			}
			if deploymentplan.Digest(op) != before {
				t.Fatal("operator settings changed")
			}
		})
	}
}
