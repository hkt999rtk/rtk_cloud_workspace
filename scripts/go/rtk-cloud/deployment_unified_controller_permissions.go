package main

import (
	"errors"
	"path"
	"reflect"
	"sort"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const controllerIdentityInitializerName = "controller-identity-permissions"

// Runs after Kubernetes volume ownership preparation. Existing state is mandatory:
// this recipe never initializes, truncates, renews or replaces identity material.
// Only the existing identity subtree is changed, never the PVC root or CRL data.
const controllerIdentityPreparation = `set -o pipefail
umask 077
for state in "$@"; do
  test -f "$state"
  test ! -L "$state"
  directory=${state%/*}
  test "$directory" = /identity-volume/identity
  test -d "$directory"
  test ! -L "$directory"
  test -z "$(find "$directory" -xdev ! -type d ! -type f -print -quit)"
  test -z "$(find "$directory" -xdev -type f -links +1 -print -quit)"
  before=$(find "$directory" -xdev -type f -exec sha256sum {} \; | sort | sha256sum)
  find "$directory" -xdev -type d -exec chown 10001:10001 {} \; -exec chmod 0700 {} \;
  find "$directory" -xdev -type f -exec chown 10001:10001 {} \; -exec chmod 0600 {} \;
  after=$(find "$directory" -xdev -type f -exec sha256sum {} \; | sort | sha256sum)
  test "$before" = "$after"
done`

func maintainedControllerIdentityInitializer(object deploymentplan.Object, image string) (deploymentplan.Object, error) {
	r := deploymentplan.Resource{ID: deploymentplan.ID(object), Desired: object}
	dirs, err := controllerIdentityDirectories(r)
	if err != nil || len(dirs) != 1 || dirs[0].uid != 10001 || !immutableEnvironmentImage.MatchString(image) {
		return nil, controllerIdentityPermissionBlocker(r.ID, "private identity preparation requires one original identity PVC, runtime UID 10001 and a pinned Alpine image")
	}
	pod := environmentPKIPod(object)
	var container deploymentplan.Object
	for _, raw := range identityPermissionList(pod["containers"]) {
		if c := deploymentplan.Map(raw); c["name"] == "pki-controller" {
			container = c
		}
	}
	var volume, base string
	for _, raw := range identityPermissionList(container["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if dirs[0].directory == deploymentplan.String(m["mountPath"])+"/identity" {
			volume = deploymentplan.String(m["name"])
			base = deploymentplan.String(m["mountPath"])
		}
	}
	if volume == "" || base == "" {
		return nil, controllerIdentityPermissionBlocker(r.ID, "permission initializer must be confined to the original PVC identity subdirectory")
	}
	var states []string
	for _, raw := range identityPermissionList(container["env"]) {
		e := deploymentplan.Map(raw)
		if e["name"] == "PKI_HOST_IDENTITY_STATE" || e["name"] == "PKI_SERVICE_CLIENT_IDENTITY_STATE" {
			state := deploymentplan.String(e["value"])
			if path.Dir(state) != dirs[0].directory {
				return nil, controllerIdentityPermissionBlocker(r.ID, "identity state files must belong to the same original private directory")
			}
			states = append(states, "/identity-volume/identity/"+path.Base(state))
		}
	}
	sort.Strings(states)
	if len(states) != 2 || states[0] == states[1] {
		return nil, controllerIdentityPermissionBlocker(r.ID, "both distinct original host and Service identity state files are required")
	}
	args := []any{"identity-permissions"}
	for _, state := range states {
		args = append(args, state)
	}
	return deploymentplan.Object{
		"name": controllerIdentityInitializerName, "image": image, "command": []any{"/bin/sh", "-ec", controllerIdentityPreparation}, "args": args,
		"volumeMounts":    []any{deploymentplan.Object{"name": volume, "mountPath": "/identity-volume"}},
		"resources":       deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "10m", "memory": "16Mi"}, "limits": deploymentplan.Object{"memory": "64Mi"}},
		"securityContext": deploymentplan.Object{"runAsNonRoot": false, "runAsUser": float64(0), "runAsGroup": float64(0), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}},
	}, nil
}

func applyControllerIdentityPreparation(object deploymentplan.Object, release environmentRelease) error {
	expected, err := maintainedControllerIdentityInitializer(object, release.Images["LKE_ALPINE_IMAGE"].Reference)
	if err != nil {
		return err
	}
	pod := environmentPKIPod(object)
	for _, raw := range identityPermissionList(pod["initContainers"]) {
		if deploymentplan.Map(raw)["name"] != controllerIdentityInitializerName {
			return controllerIdentityPermissionBlocker(deploymentplan.ID(object), "unsupported controller initializer cannot be replaced implicitly")
		}
	}
	pod["initContainers"] = []any{expected}
	deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] = "OnRootMismatch"
	deploymentplan.Map(object["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
	return nil
}

func validateControllerIdentityInitializer(object deploymentplan.Object, image string) error {
	expected, err := maintainedControllerIdentityInitializer(object, image)
	if err != nil {
		return err
	}
	initializers := identityPermissionList(environmentPKIPod(object)["initContainers"])
	if len(initializers) != 1 {
		return controllerIdentityPermissionBlocker(deploymentplan.ID(object), "exact permission-only controller initializer is required")
	}
	actual := deploymentplan.Map(initializers[0])
	// API default fields do not change the recipe; all executable, credential,
	// mount, security and lifecycle fields must still match the maintained body.
	actual = deploymentplan.Clone(actual)
	for _, key := range []string{"imagePullPolicy", "terminationMessagePath", "terminationMessagePolicy"} {
		delete(actual, key)
	}
	if !reflect.DeepEqual(actual, expected) {
		return controllerIdentityPermissionBlocker(deploymentplan.ID(object), "controller initializer differs from the pinned permission-only recipe or scope")
	}
	return nil
}

func validateObservedControllerIdentityInitializer(object, c deploymentplan.Object, release environmentRelease) error {
	image := deploymentplan.String(c["image"])
	if !immutableEnvironmentImage.MatchString(image) || environmentImageRepository(image) != environmentImageRepository(release.Images["LKE_ALPINE_IMAGE"].Reference) {
		return errors.New("deployment.pki.identity-permissions: controller initializer has no supported Alpine image role")
	}
	return validateControllerIdentityInitializer(object, image)
}

func controllerIdentityTargetPolicy(r deploymentplan.Resource, image string) error {
	if _, set := deploymentplan.Map(environmentPKIPod(r.Desired)["securityContext"])["fsGroup"]; set {
		if err := validateControllerIdentityInitializer(r.Desired, image); err != nil {
			return controllerIdentityPermissionBlocker(r.ID, "target fsGroup can widen identity permissions without the exact pinned permission-only initializer")
		}
	}
	if len(identityPermissionList(environmentPKIPod(r.Desired)["initContainers"])) > 0 {
		return validateControllerIdentityInitializer(r.Desired, image)
	}
	return nil
}

// Existing initializer observation imports references only, never live commands.
func controllerIdentityInitializerBindingFields(c deploymentplan.Object) map[string]any {
	return map[string]any{"volumeMounts": c["volumeMounts"]}
}
