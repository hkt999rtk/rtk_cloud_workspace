package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func staticControllerPermissionFixture(t *testing.T, environment string) deploymentplan.Object {
	t.Helper()
	object, err := renderManagedEnvironmentComponent(map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment}, environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "pki-controller", Component: "pki-controller", Containers: []environmentContainerBindings{{Name: "pki-controller", ImageKey: "LKE_VIDEO_CLOUD_IMAGE"}}})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func TestControllerIdentityStaticServingPreservesSecretAndSkipsPrivateInspection(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			object := staticControllerPermissionFixture(t, environment)
			before := deploymentplan.Digest(object)
			release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}}}
			if err := applyControllerIdentityPreparation(object, release); err != nil {
				t.Fatal(err)
			}
			resource := deploymentplan.Resource{ID: deploymentplan.ID(object), UID: "original-static-controller", Action: "update", Desired: object}
			if err := controllerIdentityTargetPolicy(resource, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			calls := 0
			if err := checkControllerIdentityPermissions(&deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}, "isolated-kubeconfig", identityPermissionAlpine, func(...string) ([]byte, error) {
				calls++
				t.Fatal("static serving profile attempted private identity inspection")
				return nil, nil
			}); err != nil || calls != 0 {
				t.Fatal("static serving profile needs no identity directory", calls, err)
			}
			if deploymentplan.Digest(object) != before {
				t.Fatal("static Secret, mounts, credentials or security policy changed")
			}
		})
	}
}

func TestControllerIdentityStaticServingRejectsIncompleteOrMixedProfiles(t *testing.T) {
	for _, mode := range []string{"missing-cert", "missing-key", "missing-ca", "changed-key-path", "writable-mount", "subpath", "shadowing-mount", "duplicate-env", "duplicate-mount", "duplicate-volume", "residual-pvc", "residual-native-root", "host-state-only", "client-state-only", "root-uid", "unexpected-initializer", "malformed-initializers", "missing-secret", "inherited-env", "malformed-inherited-env"} {
		t.Run(mode, func(t *testing.T) {
			object := staticControllerPermissionFixture(t, "prod")
			pod := environmentPKIPod(object)
			container := managedNamedField(pod["containers"], "pki-controller")
			mount := managedNamedField(container["volumeMounts"], "pki-tls")
			switch mode {
			case "missing-cert", "missing-key", "missing-ca":
				key := map[string]string{"missing-cert": "PKI_SERVER_CERT", "missing-key": "PKI_SERVER_KEY", "missing-ca": "PKI_CLIENT_CA"}[mode]
				container["env"] = environmentCreateServiceWithoutEnv(container["env"], key)
			case "changed-key-path":
				managedNamedField(container["env"], "PKI_SERVER_KEY")["value"] = "/foreign/key.pem"
			case "writable-mount":
				mount["readOnly"] = false
			case "subpath":
				mount["subPath"] = "other"
			case "shadowing-mount":
				container["volumeMounts"] = append(certIssuerObjectList(container["volumeMounts"]), deploymentplan.Object{"name": "shadowing-volume", "mountPath": "/run/pki-tls/tls.key", "readOnly": true})
			case "duplicate-env":
				container["env"] = append(certIssuerObjectList(container["env"]), managedNamedField(container["env"], "PKI_SERVER_KEY"))
			case "duplicate-mount":
				container["volumeMounts"] = append(certIssuerObjectList(container["volumeMounts"]), mount)
			case "duplicate-volume":
				pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), managedNamedField(pod["volumes"], "pki-tls"))
			case "residual-pvc":
				pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), environmentCreateServiceVolume("service-state", "persistentVolumeClaim", "original-controller-identity"))
			case "residual-native-root":
				container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": "PKI_HOST_ROOT_SHA256", "value": strings.Repeat("a", 64)})
			case "host-state-only", "client-state-only":
				key := "PKI_HOST_IDENTITY_STATE"
				if mode == "client-state-only" {
					key = "PKI_SERVICE_CLIENT_IDENTITY_STATE"
				}
				container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": key, "value": "/var/lib/pki-service/identity/server.json"})
			case "root-uid":
				deploymentplan.Map(pod["securityContext"])["runAsUser"] = float64(0)
			case "unexpected-initializer":
				pod["initContainers"] = []any{deploymentplan.Object{"name": "unknown", "image": identityPermissionAlpine}}
			case "malformed-initializers":
				pod["initContainers"] = "invalid"
			case "inherited-env":
				container["envFrom"] = []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "uninspected-identity-settings"}}}
			case "malformed-inherited-env":
				container["envFrom"] = "invalid"
			case "missing-secret":
				delete(managedNamedField(pod["volumes"], "pki-tls"), "secret")
			}
			before := deploymentplan.Digest(object)
			if err := applyControllerIdentityPreparation(object, environmentRelease{}); err == nil {
				t.Fatal("unqualified static profile skipped private identity requirements")
			}
			resource := deploymentplan.Resource{ID: deploymentplan.ID(object), UID: "original-controller", Action: "update", Desired: object}
			if err := checkControllerIdentityPermissions(&deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}, "isolated-kubeconfig", identityPermissionAlpine, func(...string) ([]byte, error) {
				t.Fatal("unqualified profile attempted a native read")
				return nil, nil
			}); err == nil {
				t.Fatal("unqualified static profile bypassed identity permission gate")
			}
			if deploymentplan.Digest(object) != before {
				t.Fatal("profile rejection changed original references")
			}
		})
	}
}

func TestControllerIdentityNativePartialStateCannotSelectStaticServing(t *testing.T) {
	for _, missing := range []string{"PKI_HOST_IDENTITY_STATE", "PKI_SERVICE_CLIENT_IDENTITY_STATE"} {
		t.Run(missing, func(t *testing.T) {
			object := controllerPermissionFixture("prod")
			container := managedNamedField(environmentPKIPod(object)["containers"], "pki-controller")
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], missing)
			if static, err := controllerStaticIdentityProfile(object); err != nil || static {
				t.Fatal("partial native state must retain the managed preparation path", static, err)
			}
			if err := applyControllerIdentityPreparation(object, environmentRelease{Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}}}); err == nil {
				t.Fatal("partial native state was accepted without both original files")
			}
		})
	}
}
