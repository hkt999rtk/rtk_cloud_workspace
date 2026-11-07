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

func TestCoreIdentityPermissionBindsWriterGroupWithoutChangingIdentity(t *testing.T) {
	for _, group := range []string{"implicit-image-default", "legacy-999", "maintained-10001"} {
		t.Run(group, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, "staging")
			if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
				t.Fatal(err)
			}
			object := defs[0].Object
			pod := environmentPKIPod(object)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			security := deploymentplan.Map(app["securityContext"])
			if security == nil {
				security = deploymentplan.Object{}
				app["securityContext"] = security
			}
			switch group {
			case "legacy-999":
				security["runAsGroup"] = float64(999)
			case "maintained-10001":
				security["runAsGroup"] = float64(10001)
			default:
				delete(security, "runAsGroup")
			}
			beforeApp := deploymentplan.Clone(app)
			beforeVolumes := deploymentplan.Digest(pod["volumes"])
			beforePodSecurity := deploymentplan.Digest(pod["securityContext"])
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			security = deploymentplan.Map(app["securityContext"])
			if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) {
				t.Fatalf("API cannot write original group-owned CRL directory: %v", security)
			}
			if deploymentplan.Digest(pod["volumes"]) != beforeVolumes || deploymentplan.Digest(pod["securityContext"]) != beforePodSecurity {
				t.Fatal("writer correction changed original PVC or added recursive fsGroup mutation")
			}
			// Every application field other than the explicit runtime identity remains
			// exact, including image, identity-state paths, trust inputs and PVC mounts.
			afterApp := deploymentplan.Clone(app)
			delete(beforeApp, "securityContext")
			delete(afterApp, "securityContext")
			if !reflect.DeepEqual(beforeApp, afterApp) {
				t.Fatal("writer correction changed original application identity, image or mounts")
			}
			if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			first := deploymentplan.Digest(object)
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(object) != first {
				t.Fatal("writer correction is not idempotent")
			}
		})
	}
}

func TestCoreIdentityPermissionRejectsForeignWriterGroup(t *testing.T) {
	for _, target := range []string{"pod", "container"} {
		t.Run(target, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, "staging")
			pod := environmentPKIPod(defs[0].Object)
			owner := pod
			if target == "container" {
				owner = deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			}
			security := deploymentplan.Map(owner["securityContext"])
			if security == nil {
				security = deploymentplan.Object{}
				owner["securityContext"] = security
			}
			security["runAsGroup"] = float64(0)
			before := deploymentplan.Digest(defs[0].Object)
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err == nil {
				t.Fatal("foreign explicit runtime group silently replaced")
			}
			if deploymentplan.Digest(defs[0].Object) != before {
				t.Fatal("rejected group changed original target")
			}
		})
	}
}

func TestCoreIdentityPermissionValidatorRequiresExplicitWriterGroup(t *testing.T) {
	for _, group := range []string{"missing", "image-default-999"} {
		t.Run(group, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, "staging")
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			security := deploymentplan.Map(app["securityContext"])
			if group == "missing" {
				delete(security, "runAsGroup")
			} else {
				security["runAsGroup"] = float64(999)
			}
			if err := validateCoreIdentityInitializer(defs[0].Object, identityPermissionAlpine); err == nil {
				t.Fatal("candidate with unwritable original MQTT directory passed validation")
			}
		})
	}
}
