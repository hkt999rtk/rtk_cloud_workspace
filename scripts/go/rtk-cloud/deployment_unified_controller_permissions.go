package main

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

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

// A retained static serving profile has no private identity directory to
// prepare or inspect. Absence alone is not evidence: require the maintained
// read-only Secret mapping and reject residual native state or PVCs.
func controllerStaticIdentityProfile(object deploymentplan.Object) (bool, error) {
	bad := func() (bool, error) {
		return false, controllerIdentityPermissionBlocker(deploymentplan.ID(object), "static Controller requires complete read-only serving Secret references without private identity state or initializers")
	}
	pod := environmentPKIPod(object)
	containers, err := environmentPKINamedFields(pod["containers"])
	if err != nil || containers["pki-controller"] == nil {
		return bad()
	}
	container := containers["pki-controller"]
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return bad()
	}
	if settings["PKI_HOST_IDENTITY_STATE"] != nil || settings["PKI_SERVICE_CLIENT_IDENTITY_STATE"] != nil {
		if settings["PKI_SERVER_CERT"] != nil || settings["PKI_SERVER_KEY"] != nil {
			return bad()
		}
		return false, nil // Even a partial native declaration retains strict preparation.
	}
	if inherited := container["envFrom"]; inherited != nil {
		sources, ok := inherited.([]any)
		if !ok || len(sources) != 0 {
			return bad() // Inherited settings cannot prove absence of native identity state.
		}
	}
	for name := range settings {
		if strings.HasPrefix(name, "PKI_HOST_") || strings.HasPrefix(name, "PKI_SERVICE_CLIENT_") {
			return bad()
		}
	}
	uid := deploymentplan.Map(pod["securityContext"])["runAsUser"]
	if value, ok := deploymentplan.Map(container["securityContext"])["runAsUser"]; ok {
		uid = value
	}
	runtimeUID, uidErr := strconv.ParseInt(fmt.Sprint(uid), 10, 64)
	initializers, initErr := environmentPKINamedFields(pod["initContainers"])
	if uidErr != nil || runtimeUID != 10001 || initErr != nil || len(initializers) != 0 {
		return bad()
	}
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return bad()
	}
	for _, volume := range volumes {
		if volume["persistentVolumeClaim"] != nil {
			return bad()
		}
	}
	mounts, err := environmentPKINamedFields(container["volumeMounts"])
	if err != nil {
		return bad()
	}
	volume, mount := volumes["pki-tls"], mounts["pki-tls"]
	if volume == nil || deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"]) == "" || mount == nil || mount["mountPath"] != "/run/pki-tls" || mount["readOnly"] != true || mount["subPath"] != nil || mount["subPathExpr"] != nil {
		return bad()
	}
	for _, other := range mounts {
		mountPath := deploymentplan.String(other["mountPath"])
		if other["name"] != "pki-tls" && (mountPath == "/run/pki-tls" || strings.HasPrefix(mountPath, "/run/pki-tls/")) {
			return bad()
		}
	}
	for name, value := range map[string]string{"PKI_SERVER_CERT": "/run/pki-tls/tls.crt", "PKI_SERVER_KEY": "/run/pki-tls/tls.key", "PKI_CLIENT_CA": "/run/pki-tls/ca.crt"} {
		setting := settings[name]
		if setting == nil || setting["value"] != value || setting["valueFrom"] != nil {
			return bad()
		}
	}
	return true, nil
}

func applyControllerIdentityPreparation(object deploymentplan.Object, release environmentRelease) error {
	if static, err := controllerStaticIdentityProfile(object); err != nil || static {
		return err
	}
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
	if static, err := controllerStaticIdentityProfile(r.Desired); err != nil || static {
		return err
	}
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
