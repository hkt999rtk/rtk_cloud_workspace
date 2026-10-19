package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type controllerIdentityDirectory struct {
	directory, claim, container string
	uid                         int64
}
type identityPermissionRead func(...string) ([]byte, error)

func controllerIdentityPermissionBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.identity-permissions", Resource: resource, Cause: cause, Next: "preserve the original identity PVC and files; repair private directory ownership/mode and the target Pod volume-permission policy through a reviewed update, then repeat full preflight"}
}

func controllerIdentityReadBlocker(resource, cause string, err error) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.identity-permissions", Resource: resource, Cause: secretCheckFailure(err, cause).Error(), Next: "verify selected Kubernetes API access and availability for the original controller metadata and read-only inspection; preserve the original owner, identity PVC and files, then repeat full preflight"}
}

// PKI Controller uses serviceidentity.Store, which rejects group/other access.
// Kubernetes fsGroup volume preparation can widen even an existing 0700 directory.
// An arbitrary initializer is not evidence of a supported permission lifecycle.
func controllerIdentityDirectories(r deploymentplan.Resource) ([]controllerIdentityDirectory, error) {
	if r.Action == "retire" || deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["name"]) != "pki-controller" || r.Desired["kind"] != "Deployment" {
		return nil, nil
	}
	if replicas, specified := deploymentplan.Map(r.Desired["spec"])["replicas"]; specified {
		value, err := strconv.ParseInt(fmt.Sprint(replicas), 10, 64)
		if err != nil || value < 0 {
			return nil, controllerIdentityPermissionBlocker(r.ID, "target PKI Controller replica count is invalid")
		}
		if value == 0 {
			return nil, nil
		}
	}
	if static, err := controllerStaticIdentityProfile(r.Desired); err != nil || static {
		return nil, err
	}
	pod := environmentPKIPod(r.Desired)
	var result []controllerIdentityDirectory
	for _, raw := range identityPermissionList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		if c["name"] != "pki-controller" {
			continue
		}
		security := deploymentplan.Map(pod["securityContext"])
		uidValue := security["runAsUser"]
		if v, ok := deploymentplan.Map(c["securityContext"])["runAsUser"]; ok {
			uidValue = v
		}
		uid, err := strconv.ParseInt(fmt.Sprint(uidValue), 10, 64)
		if err != nil || uid <= 0 {
			return nil, controllerIdentityPermissionBlocker(r.ID, "target PKI Controller has no explicit non-root identity directory owner")
		}
		seen := map[string]bool{}
		for _, rawEnv := range identityPermissionList(c["env"]) {
			setting := deploymentplan.Map(rawEnv)
			if setting["name"] != "PKI_HOST_IDENTITY_STATE" && setting["name"] != "PKI_SERVICE_CLIENT_IDENTITY_STATE" {
				continue
			}
			state := deploymentplan.String(setting["value"])
			if !path.IsAbs(state) || path.Clean(state) != state || path.Dir(state) == "/" {
				return nil, controllerIdentityPermissionBlocker(r.ID, "target identity state path is not an explicit canonical absolute path")
			}
			directory := path.Dir(state)
			var selected deploymentplan.Object
			for _, rawMount := range identityPermissionList(c["volumeMounts"]) {
				mount := deploymentplan.Map(rawMount)
				mountPath := deploymentplan.String(mount["mountPath"])
				if mountPath != "" && (directory == mountPath || strings.HasPrefix(directory, strings.TrimSuffix(mountPath, "/")+"/")) && len(mountPath) > len(deploymentplan.String(selected["mountPath"])) {
					selected = mount
				}
			}
			var claim string
			for _, rawVolume := range identityPermissionList(pod["volumes"]) {
				volume := deploymentplan.Map(rawVolume)
				if volume["name"] == selected["name"] {
					claim = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
				}
			}
			if claim == "" || selected["readOnly"] == true || selected["subPath"] != nil || selected["subPathExpr"] != nil {
				return nil, controllerIdentityPermissionBlocker(r.ID, "identity state requires an unambiguous original writable PVC mount")
			}
			if !seen[directory] {
				result = append(result, controllerIdentityDirectory{directory, claim, "pki-controller", uid})
				seen[directory] = true
			}
		}
	}
	if len(result) == 0 {
		return nil, controllerIdentityPermissionBlocker(r.ID, "target PKI Controller identity directory settings are unavailable")
	}
	return result, nil
}

func verifyControllerIdentityPermissions(ctx context.Context, p *deploymentplan.Plan, store secretStore, release environmentRelease) error {
	runtime := newDeploymentCheckRuntime(ctx, p.Environment)
	return checkControllerIdentityPermissions(p, store.KubeconfigPath(), release.Images["LKE_ALPINE_IMAGE"].Reference, func(args ...string) ([]byte, error) { return runtime.kubectl(false, args...) })
}

func checkControllerIdentityPermissions(p *deploymentplan.Plan, kubeconfig, image string, read identityPermissionRead) error {
	for _, r := range p.Resources {
		directories, err := controllerIdentityDirectories(r)
		if err != nil {
			return err
		}
		if len(directories) == 0 {
			continue
		}
		if err := controllerIdentityTargetPolicy(r, image); err != nil {
			return err
		}
		if r.UID == "" {
			return controllerIdentityPermissionBlocker(r.ID, "original controller owner is unavailable for read-only identity inspection")
		}
		namespace := deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["namespace"])
		base := []string{"--kubeconfig", kubeconfig, "-n", namespace}
		query := func(args ...string) (deploymentplan.Object, error) {
			raw, err := read(append(append([]string{}, base...), args...)...)
			if err != nil {
				return nil, err
			}
			var object deploymentplan.Object
			err = json.Unmarshal(raw, &object)
			if err != nil {
				return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "original controller metadata response is invalid"}
			}
			return object, nil
		}
		// Follow controller UIDs, not labels alone, so an unrelated Pod cannot prove
		// the original PVC's permissions. Never create a helper Pod or read state files.
		replicas, err := query("get", "replicasets", "-o", "json")
		if err != nil {
			return controllerIdentityReadBlocker(r.ID, "original ReplicaSet ownership cannot be inspected", err)
		}
		owners := map[string]bool{}
		for _, raw := range identityPermissionList(replicas["items"]) {
			rs := deploymentplan.Map(raw)
			meta := deploymentplan.Map(rs["metadata"])
			if identityPermissionOwnedBy(meta, "Deployment", r.UID) {
				owners[deploymentplan.String(meta["uid"])] = true
			}
		}
		pods, err := query("get", "pods", "-o", "json")
		if err != nil {
			return controllerIdentityReadBlocker(r.ID, "original identity consumer Pods cannot be inspected", err)
		}
		var candidate deploymentplan.Object
		for _, raw := range identityPermissionList(pods["items"]) {
			pod := deploymentplan.Map(raw)
			meta := deploymentplan.Map(pod["metadata"])
			if meta["deletionTimestamp"] != nil {
				continue
			}
			owned := false
			for owner := range owners {
				owned = owned || identityPermissionOwnedBy(meta, "ReplicaSet", owner)
			}
			if !owned {
				continue
			}
			for _, rawStatus := range identityPermissionList(deploymentplan.Map(pod["status"])["containerStatuses"]) {
				status := deploymentplan.Map(rawStatus)
				if status["name"] == "pki-controller" && deploymentplan.Map(status["state"])["running"] != nil {
					candidate = pod
				}
			}
		}
		if candidate == nil {
			return controllerIdentityPermissionBlocker(r.ID, "original PKI identity directory metadata cannot be read from its existing container")
		}
		for _, directory := range directories {
			// Confirm the live container maps the same path to the same retained PVC.
			live := r
			live.Desired = deploymentplan.Object{"kind": "Deployment", "metadata": r.Desired["metadata"], "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": candidate["spec"]}}}
			livePod := environmentPKIPod(live.Desired)
			// Only the live mount/path mapping is needed here. Its fsGroup is checked
			// against actual directory metadata, separately from the target policy.
			security := deploymentplan.Map(livePod["securityContext"])
			clone := deploymentplan.Object{}
			for k, v := range security {
				if k != "fsGroup" {
					clone[k] = v
				}
			}
			livePod["securityContext"] = clone
			liveDirs, err := controllerIdentityDirectories(live)
			matched := false
			for _, d := range liveDirs {
				matched = matched || d == directory
			}
			if err != nil || !matched {
				return controllerIdentityPermissionBlocker(r.ID, "original Pod does not prove the target identity path and PVC mapping")
			}
			raw, err := read(append(append([]string{}, base...), "exec", deploymentplan.String(deploymentplan.Map(candidate["metadata"])["name"]), "-c", directory.container, "--", "stat", "-c", "%a:%u", directory.directory)...)
			// stat does not follow a directory symlink; only mode and owner are returned.
			if err != nil {
				return controllerIdentityReadBlocker(r.ID, "original identity directory metadata inspection could not complete", err)
			}
			if strings.TrimSpace(string(raw)) != fmt.Sprintf("700:%d", directory.uid) {
				return controllerIdentityPermissionBlocker(r.ID, "original identity directory mode/owner is unavailable or differs from 0700 and the target runtime UID")
			}
			after, err := query("get", "pod", deploymentplan.String(deploymentplan.Map(candidate["metadata"])["name"]), "-o", "json")
			if err != nil {
				return controllerIdentityReadBlocker(r.ID, "original identity inspection Pod cannot be rechecked", err)
			}
			if deploymentplan.Map(after["metadata"])["uid"] != deploymentplan.Map(candidate["metadata"])["uid"] || deploymentplan.Map(after["metadata"])["deletionTimestamp"] != nil {
				return controllerIdentityPermissionBlocker(r.ID, "original identity inspection Pod changed during the read-only permission check")
			}
		}
	}
	return nil
}

func identityPermissionOwnedBy(meta deploymentplan.Object, kind, uid string) bool {
	for _, raw := range identityPermissionList(meta["ownerReferences"]) {
		owner := deploymentplan.Map(raw)
		if uid != "" && owner["kind"] == kind && owner["uid"] == uid && owner["controller"] == true {
			return true
		}
	}
	return false
}

func identityPermissionList(value any) []any { result, _ := value.([]any); return result }
