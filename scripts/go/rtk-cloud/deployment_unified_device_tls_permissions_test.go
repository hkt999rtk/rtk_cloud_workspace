package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestDeviceTLSCandidateUsesOriginalProjectionWithoutFSGroupAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			object := deviceCallbackFixture(t, environment)
			pod := environmentPKIPod(object)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			originalVolumes, originalMounts, originalSettings := pod["volumes"], app["volumeMounts"], app["env"]
			before, _ := json.Marshal([]any{originalVolumes, originalMounts, originalSettings, pod["initContainers"]})
			if err := validateEnvironmentDeviceTLSPermissions([]deploymentplan.Definition{{Object: object}}); err != nil {
				t.Fatal(err)
			}
			security := deploymentplan.Map(app["securityContext"])
			if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["runAsNonRoot"] != true || !reflect.DeepEqual(pod["securityContext"], deploymentplan.Object{"supplementalGroups": []any{float64(0)}}) {
				t.Fatal("candidate changed the maintained non-root Secret reader or introduced fsGroup", security, pod["securityContext"])
			}
			// Exercise the generic rollout declaration checker too: it must accept
			// this exact original root:root projection without recursive ownership.
			raw, _ := json.Marshal(object)
			if check := checkRolloutMountsJSON(raw, "isolated Device TLS declaration", true); !check.Passed {
				t.Fatal("generic Secret readability checker rejected the maintained profile", check.Detail)
			}
			after, _ := json.Marshal([]any{pod["volumes"], app["volumeMounts"], app["env"], pod["initContainers"]})
			if string(before) != string(after) {
				t.Fatal("candidate-only validation modified original Secret/PVC/identity mappings or initializer")
			}
			// Original Secret item paths remain relative to their original mount;
			// per-item 0440 overrides qualify without replacing Secret data.
			source := deploymentplan.Map(managedNamedField(pod["volumes"], "pki-server")["secret"])
			source["items"] = []any{deploymentplan.Object{"key": "tls.crt", "path": "server/cert.pem", "mode": float64(0o440)}, deploymentplan.Object{"key": "tls.key", "path": "server/key.pem", "mode": float64(0o440)}}
			managedNamedField(app["env"], "VIDEO_CLOUD_API_TLS_CERT_PATH")["value"] = "/run/pki-server/server/cert.pem"
			managedNamedField(app["env"], "VIDEO_CLOUD_API_TLS_KEY_PATH")["value"] = "/run/pki-server/server/key.pem"
			if err := validateEnvironmentDeviceTLSPermissions([]deploymentplan.Definition{{Object: object}}); err != nil {
				t.Fatal("original Secret item mapping was not respected", err)
			}
		})
	}
}

func TestDeviceTLSCandidateRejectsUnreadableOrMutatingProfiles(t *testing.T) {
	for _, fault := range []string{"original-unreadable", "missing-uid", "wrong-uid", "root-uid", "wrong-gid", "nonroot-disabled", "missing-group", "foreign-group", "extra-group", "fsGroup", "fsGroup-policy", "owner-only", "world-readable", "item-owner-only", "invalid-item", "writable-mount", "shadow-mount"} {
		t.Run(fault, func(t *testing.T) {
			object := deviceCallbackFixture(t, "dev")
			defs := []deploymentplan.Definition{{Object: object}}
			if err := deriveEnvironmentDeviceMQTTCallback(nil, defs); err != nil {
				t.Fatal(err)
			}
			pod := environmentPKIPod(object)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			security, podSecurity := deploymentplan.Map(app["securityContext"]), deploymentplan.Map(pod["securityContext"])
			source := deploymentplan.Map(managedNamedField(pod["volumes"], "pki-server")["secret"])
			switch fault {
			case "original-unreadable":
				delete(pod, "securityContext")
				delete(app, "securityContext")
			case "missing-uid":
				delete(security, "runAsUser")
			case "wrong-uid":
				security["runAsUser"] = float64(10002)
			case "root-uid":
				security["runAsUser"] = float64(0)
			case "wrong-gid":
				security["runAsGroup"] = float64(0)
			case "nonroot-disabled":
				security["runAsNonRoot"] = false
			case "missing-group":
				delete(podSecurity, "supplementalGroups")
			case "foreign-group":
				podSecurity["supplementalGroups"] = []any{float64(10001)}
			case "extra-group":
				podSecurity["supplementalGroups"] = []any{float64(0), float64(10002)}
			case "fsGroup":
				podSecurity["fsGroup"] = float64(10001)
			case "fsGroup-policy":
				podSecurity["fsGroupChangePolicy"] = "OnRootMismatch"
			case "owner-only":
				source["defaultMode"] = float64(0o400)
			case "world-readable":
				source["defaultMode"] = float64(0o444)
			case "item-owner-only":
				source["items"] = []any{deploymentplan.Object{"key": "tls.crt", "path": "tls.crt"}, deploymentplan.Object{"key": "tls.key", "path": "tls.key", "mode": float64(0o400)}}
			case "invalid-item":
				source["items"] = []any{deploymentplan.Object{"key": "tls.crt", "path": "../private-canary"}}
			case "writable-mount":
				managedNamedField(app["volumeMounts"], "pki-server")["readOnly"] = false
			case "shadow-mount":
				app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "shadow", "mountPath": "/run/pki-server/tls.key"})
			}
			err := validateEnvironmentDeviceTLSPermissions(defs)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.device-tls-permissions" || blocker.Resource == "" || blocker.Next == "" || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe or unreadable candidate had no safe owned permission diagnosis", err)
			}
		})
	}
}

func TestDeviceTLSPreflightCallbackRejectsPermissionFailureWithoutRunningAPI(t *testing.T) {
	object := deviceCallbackFixture(t, "dev")
	if err := deriveEnvironmentDeviceMQTTCallback(nil, []deploymentplan.Definition{{Object: object}}); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(environmentPKIPod(object)["securityContext"])["supplementalGroups"] = nil
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(object), Desired: object, Action: "update"}}}
	err := checkEnvironmentDeviceMQTTCallback(context.Background(), plan, func(context.Context, string) (deploymentplan.Object, error) {
		t.Fatal("permission qualification consulted a future API, filesystem or live readiness")
		return nil, nil
	})
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.device-tls-permissions" {
		t.Fatal("execution qualification missed the candidate permission blocker", err)
	}
}

func TestDeviceTLSProfileDoesNotChangeOtherAPIs(t *testing.T) {
	for _, name := range []string{"video-cloud-api", "video-cloud-api-app-pki", "video-cloud-logingester"} {
		object := deviceCallbackFixture(t, "dev")
		deploymentplan.Map(object["metadata"])["name"] = name
		delete(environmentPKIPod(object), "securityContext")
		before := deploymentplan.Digest(object)
		if err := validateEnvironmentDeviceTLSPermissions([]deploymentplan.Definition{{Object: object}}); err != nil || before != deploymentplan.Digest(object) {
			t.Fatal("Device permission guard changed or blocked another working profile", name, err)
		}
	}
}
