package main

import (
	"path"
	"reflect"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const coreIdentityInitializerName = "core-identity-permissions"

// CRL atomic replacement creates its temporary file in the PVC root. Change
// that directory's metadata only; never recursively touch identity/CRL files.
const coreIdentityPreparation = `set -o pipefail
umask 077
state=$1
test -d /identity-volume
test ! -L /identity-volume
test -f "$state"
test "$(stat -c %h "$state")" = 1
directory=$state
while test "$directory" != /identity-volume; do
  test ! -L "$directory"
  directory=${directory%/*}
done
before=$(sha256sum "$state")
chown 10001:10001 /identity-volume
chmod 0700 /identity-volume
test "$(stat -c %a:%u /identity-volume)" = 700:10001
after=$(sha256sum "$state")
test "$before" = "$after"`

// Native creation already owns these directories and state files. Only the
// exact CRL parent metadata is asserted; neither keys nor cached JSON move.
const previousCoreNativeIdentityPreparation = `set -o pipefail
umask 077
state=$1
parent=/identity-volume/private/crl
test -d /identity-volume
test ! -L /identity-volume
test -f "$state"
test "$(stat -c %h "$state")" = 1
test "$(stat -c %a:%u "$state")" = 600:10001
test "$(stat -c %a:%u /identity-volume/private)" = 700:10001
test -d "$parent"
test "$(stat -c %a:%u "$parent")" = 700:10001
for location in "$state" "$parent"; do
  directory=$location
  while test "$directory" != /identity-volume; do
    test ! -L "$directory"
    directory=${directory%/*}
  done
done
hash_files() {
  sha256sum "$state" || return 1
  for file in "$parent"/* "$parent"/.[!.]* "$parent"/..?*; do
    if test ! -e "$file" && test ! -L "$file"; then continue; fi
    test ! -L "$file" || return 1
    test -f "$file" || return 1
    test "$(stat -c %h "$file")" = 1 || return 1
    test "$(stat -c %a:%u "$file")" = 600:10001 || return 1
    sha256sum "$file" || return 1
  done
}
before=$(hash_files)
chown 10001:10001 "$parent"
chmod 0700 "$parent"
test "$(stat -c %a:%u "$parent")" = 700:10001
after=$(hash_files)
test "$before" = "$after"`

// SGID is inherited from an existing volume parent and does not widen directory
// permissions. Keep the literal previous recipe for authenticated old plans.
var coreNativeIdentityPreparation = strings.NewReplacer(
	`test "$(stat -c %a:%u /identity-volume/private)" = 700:10001`,
	`case "$(stat -c %a:%u /identity-volume/private)" in 700:10001|2700:10001) ;; *) exit 1;; esac`,
	`test "$(stat -c %a:%u "$parent")" = 700:10001
for location`,
	`case "$(stat -c %a:%u "$parent")" in 700:10001|2700:10001) ;; *) exit 1;; esac
for location`,
).Replace(previousCoreNativeIdentityPreparation)

func coreIdentityPermissionBlocker(object deploymentplan.Object, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.core-crl-permissions", Resource: deploymentplan.ID(object), Cause: cause, Next: "retain the original Service identity PVC and files; derive the exact pinned permission-only initializer for the CRL persistence parent and runtime UID, then repeat full preflight"}
}

func maintainedCoreIdentityInitializer(object deploymentplan.Object, image string) (deploymentplan.Object, error) {
	pod := environmentPKIPod(object)
	var app deploymentplan.Object
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		if environmentCoreMQTTConsumer(object, c) {
			app = c
		}
	}
	state := mqttFoundationEnv(app, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE")
	if !coreMQTTIdentityFile(object, app, state) || !immutableEnvironmentImage.MatchString(image) {
		return nil, coreIdentityPermissionBlocker(object, "original managed Service identity state, writable PVC or pinned permission image is unavailable")
	}
	if deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
		return nil, coreIdentityPermissionBlocker(object, "fsGroup can widen original private identity permissions")
	}
	// The selected formal API Dockerfile owns UID 10001. Make that contract
	// explicit in the candidate rather than introducing an operator setting.
	for _, security := range []deploymentplan.Object{deploymentplan.Map(pod["securityContext"]), deploymentplan.Map(app["securityContext"])} {
		if uid, set := security["runAsUser"]; set && uid != float64(10001) {
			return nil, coreIdentityPermissionBlocker(object, "core API runtime UID differs from the maintained formal image contract")
		}
	}
	mounts, _ := environmentPKINamedFields(app["volumeMounts"])
	base := deploymentplan.String(mounts["service-identity"]["mountPath"])
	if base == "/" {
		return nil, coreIdentityPermissionBlocker(object, "identity permission preparation cannot own the container filesystem root")
	}
	relative, valid := managedMQTTPVCRelativeFile(object, app, "service-identity", state)
	if !valid {
		return nil, coreIdentityPermissionBlocker(object, "original Service state has no unambiguous original-PVC relative path")
	}
	preparation := coreIdentityPreparation
	if nativeCoreCRLDirectory(object, app, relative) {
		preparation = coreNativeIdentityPreparation
	}
	return deploymentplan.Object{
		"name": coreIdentityInitializerName, "image": image,
		"command":         []any{"/bin/sh", "-ec", preparation},
		"args":            []any{"identity-permissions", "/identity-volume/" + relative},
		"volumeMounts":    []any{deploymentplan.Object{"name": "service-identity", "mountPath": "/identity-volume"}},
		"resources":       deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "10m", "memory": "16Mi"}, "limits": deploymentplan.Object{"memory": "64Mi"}},
		"securityContext": deploymentplan.Object{"runAsNonRoot": false, "runAsUser": float64(0), "runAsGroup": float64(0), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}},
	}, nil
}

func applyCoreIdentityPreparation(definitions []deploymentplan.Definition, image string) error {
	for _, d := range definitions {
		pod := environmentPKIPod(d.Object)
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(raw)
			if !environmentCoreMQTTConsumer(d.Object, c) || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") == "" {
				continue
			}
			expected, err := maintainedCoreIdentityInitializer(d.Object, image)
			if err != nil {
				return err
			}
			var initializers []any
			for _, existing := range certIssuerObjectList(pod["initContainers"]) {
				if deploymentplan.Map(existing)["name"] != coreIdentityInitializerName {
					initializers = append(initializers, existing)
				}
			}
			pod["initContainers"] = append(initializers, expected)
			security := deploymentplan.Map(c["securityContext"])
			if security == nil {
				security = deploymentplan.Object{}
				c["securityContext"] = security
			}
			security["runAsUser"], security["runAsNonRoot"] = float64(10001), true
			deploymentplan.Map(d.Object["spec"])["strategy"] = deploymentplan.Object{"type": "Recreate"}
		}
	}
	return nil
}

func validateCoreIdentityInitializer(object deploymentplan.Object, image string) error {
	expected, err := maintainedCoreIdentityInitializer(object, image)
	if err != nil {
		return err
	}
	for _, raw := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
		c := deploymentplan.Map(raw)
		if environmentCoreMQTTConsumer(object, c) && deploymentplan.Map(c["securityContext"])["runAsUser"] != float64(10001) {
			return coreIdentityPermissionBlocker(object, "candidate core API must explicitly retain runtime UID 10001")
		}
	}
	if deploymentplan.Map(deploymentplan.Map(object["spec"])["strategy"])["type"] != "Recreate" {
		return coreIdentityPermissionBlocker(object, "identity permission preparation requires a single original-PVC writer during rollout")
	}
	var found int
	for _, raw := range certIssuerObjectList(environmentPKIPod(object)["initContainers"]) {
		actual := deploymentplan.Map(raw)
		if actual["name"] != coreIdentityInitializerName {
			continue
		}
		found++
		actual = deploymentplan.Clone(actual)
		for _, key := range []string{"imagePullPolicy", "terminationMessagePath", "terminationMessagePolicy"} {
			delete(actual, key)
		}
		if !reflect.DeepEqual(actual, expected) {
			legacy := deploymentplan.Clone(expected)
			if reflect.DeepEqual(expected["command"], []any{"/bin/sh", "-ec", coreNativeIdentityPreparation}) {
				legacy["command"] = []any{"/bin/sh", "-ec", previousCoreNativeIdentityPreparation}
			}
			if !reflect.DeepEqual(actual, legacy) {
				return coreIdentityPermissionBlocker(object, "core identity initializer differs from the reviewed directory-only recipe")
			}
		}
	}
	if found != 1 {
		return coreIdentityPermissionBlocker(object, "CRL persistence requires the exact pinned original-PVC directory permission initializer")
	}
	return nil
}

func coreCRLParentPrepared(object, c deploymentplan.Object, state string) bool {
	relative, valid := managedMQTTPVCRelativeFile(object, c, "service-identity", state)
	if !valid {
		return false
	}
	identityRelative, valid := managedMQTTPVCRelativeFile(object, c, "service-identity", mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"))
	if !valid {
		return false
	}
	parent := "."
	if nativeCoreCRLDirectory(object, c, identityRelative) {
		parent = "private/crl"
	}
	return path.Dir(relative) == parent
}

func nativeCoreCRLDirectory(object, c deploymentplan.Object, identityRelative string) bool {
	if identityRelative != "private/client.json" && identityRelative != "private/identity.json" {
		return false
	}
	volumes, err := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
	if err != nil {
		return false
	}
	mounts, err := environmentPKINamedFields(c["volumeMounts"])
	if err != nil {
		return false
	}
	claim, _, valid := managedMQTTFullPVCMount(volumes["service-identity"], mounts["service-identity"])
	if !valid {
		return false
	}
	aliasClaim, aliasBase, valid := managedMQTTFullPVCMount(volumes["service-state"], mounts["service-state"])
	return valid && aliasClaim == claim && aliasBase == "/var/lib/pki-service"
}
