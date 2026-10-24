package main

import (
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCoreIdentityPermissionProfileAndAdoption(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			defs, live := coreMQTTFixture(t, environment)
			original := deploymentplan.Clone(defs[0].Object)
			if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
				t.Fatal(err)
			}
			if err := validateCoreIdentityInitializer(defs[0].Object, identityPermissionAlpine); err == nil {
				t.Fatal("original admission-only candidate missed the CRL permission defect")
			}
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			object := defs[0].Object
			if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(environmentPKIPod(original)["volumes"], environmentPKIPod(object)["volumes"]) {
				t.Fatal("original identity/trust/CRL sources changed")
			}
			app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
			release := environmentRelease{Environment: environment, Stack: "video-cloud-" + environment, Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}, "LKE_VIDEO_CLOUD_IMAGE": {Reference: deploymentplan.String(app["image"])}}}
			bindings, err := observeEnvironmentBindings(environment, release, append(live, object))
			if err != nil {
				t.Fatal(err)
			}
			if len(bindings.Pods[0].InitContainers) != 1 || len(bindings.Pods[0].InitContainers[0].Fields) != 1 {
				t.Fatal("initializer observation imported executable commands or lost original references")
			}
			if err := applyEnvironmentPodBindings(original, bindings, release); err != nil {
				t.Fatal("next upgrade cannot consume its own original initializer", err)
			}
			defs[0].Object = original
			if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
				t.Fatal(err)
			}
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentPKITransition(defs, []deploymentplan.Object{object}); err != nil {
				t.Fatal("subsequent update did not preserve original lifecycle bindings", err)
			}
		})
	}
}

func TestCoreIdentityPermissionRejectsUnsafeTargets(t *testing.T) {
	for _, problem := range []string{"recipe", "image", "mount", "privilege", "uid", "fsGroup", "rolling", "duplicate", "state-shadow"} {
		t.Run(problem, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, "dev")
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			o := defs[0].Object
			pod := environmentPKIPod(o)
			init := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			switch problem {
			case "recipe":
				init["command"] = []any{"sh", "-ec", "chown -R 10001 /identity-volume"}
			case "image":
				init["image"] = "alpine:latest"
			case "mount":
				deploymentplan.Map(certIssuerObjectList(init["volumeMounts"])[0])["subPath"] = "other"
			case "privilege":
				deploymentplan.Map(init["securityContext"])["privileged"] = true
			case "uid":
				deploymentplan.Map(app["securityContext"])["runAsUser"] = float64(10002)
			case "fsGroup":
				pod["securityContext"] = deploymentplan.Object{"fsGroup": float64(10001)}
			case "rolling":
				deploymentplan.Map(o["spec"])["strategy"] = deploymentplan.Object{"type": "RollingUpdate"}
			case "duplicate":
				pod["initContainers"] = append(certIssuerObjectList(pod["initContainers"]), deploymentplan.Clone(init))
			case "state-shadow":
				app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "shadow", "mountPath": "/identity/current.json"})
			}
			if err := validateCoreIdentityInitializer(o, identityPermissionAlpine); err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("unsafe candidate accepted: %s %v", problem, err)
			}
		})
	}
}
