package main

import (
	"crypto/sha256"
	"fmt"
	"net"
	"path"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

const accountManagerSocketPreparation = "umask 077; mkdir -p /socket/private; chmod 700 /socket/private"

func accountManagerSocketConsumer(component, name string) bool {
	return component == "existing-workload" && name == "account-manager"
}

func accountManagerSocketInitializerBlocker(id string) error {
	return &deploymentplan.Blocker{ID: "deployment.inventory.account-initializer", Resource: id, Cause: "Account Manager socket initializer has an unsupported name, permission recipe, mount or image role", Next: "retain the original shared socket volume and UID; qualify the maintained permission-only directory initializer and immutable ALPINE_IMAGE, then repeat preflight"}
}

func validateObservedAccountManagerSocketInitializer(id string, c map[string]any, release environmentRelease) error {
	if c["name"] != "management-socket-directory" {
		return accountManagerSocketInitializerBlocker(id)
	}
	command, args := certIssuerObjectList(c["command"]), certIssuerObjectList(c["args"])
	if len(command) < 2 || len(command) > 3 || len(command)+len(args) != 3 || (command[0] != "sh" && command[0] != "/bin/sh") || (command[1] != "-c" && command[1] != "-ec") {
		return accountManagerSocketInitializerBlocker(id)
	}
	parts := []string{}
	for _, value := range append(command[2:], args...) {
		parts = append(parts, deploymentplan.String(value))
	}
	script := strings.Join(parts, "\n")
	// The original strict-mode recipe only creates /socket/private and sets
	// 0700. Keep its digest as evidence; never import a live command into intent.
	if script != accountManagerPublicCRLPreparation && script != accountManagerSocketPreparation && fmt.Sprintf("%x", sha256.Sum256([]byte(script))) != "c5fce41047873694c256411b06c258c56a1f58c9528ff75655f68b24d6b1e4d8" {
		return accountManagerSocketInitializerBlocker(id)
	}
	repository := environmentImageRepository(deploymentplan.String(c["image"]))
	if repository != environmentImageRepository(release.Images["LKE_ALPINE_IMAGE"].Reference) && repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && repository != "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api" {
		return accountManagerSocketInitializerBlocker(id)
	}
	return nil
}

func maintainedAccountManagerSocketInitializer(bound environmentContainerBindings, release environmentRelease, volume, id string) (map[string]any, error) {
	image := release.Images["LKE_ALPINE_IMAGE"].Reference
	mounts := certIssuerObjectList(bound.Fields["volumeMounts"])
	if bound.Name != "management-socket-directory" || bound.ImageKey != "LKE_ALPINE_IMAGE" || !immutableEnvironmentImage.MatchString(image) || (len(mounts) != 1 && len(mounts) != 2) {
		return nil, accountManagerSocketInitializerBlocker(id)
	}
	mount := deploymentplan.Map(mounts[0])
	if mount["name"] != volume || mount["mountPath"] != "/socket" || mount["readOnly"] == true || deploymentplan.String(mount["subPath"]) != "" || deploymentplan.String(mount["subPathExpr"]) != "" {
		return nil, accountManagerSocketInitializerBlocker(id)
	}
	script := accountManagerSocketPreparation
	if len(mounts) == 2 {
		if deploymentplan.Digest(mounts[1]) != deploymentplan.Digest(accountRegistrationCRLSeedMount()) {
			return nil, accountManagerSocketInitializerBlocker(id)
		}
		script = accountManagerPublicCRLPreparation
	}
	c := map[string]any{"name": bound.Name, "image": image, "command": []any{"/bin/sh", "-ec", script}, "resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]any{"memory": "64Mi"}}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
	for field, value := range bound.Fields {
		c[field] = value
	}
	return c, nil
}

// Socket ownership is an existing identity reference, not a static client leaf.
func applyAccountManagerSocketProfile(object deploymentplan.Object, binding environmentPodBindings, owned bool, release environmentRelease) error {
	if !owned || binding.Name != "account-manager" {
		return nil
	}
	var app, manager environmentContainerBindings
	for _, c := range binding.Containers {
		if c.Name == "app" {
			app = c
		}
		if c.Name == "pkimanagement" {
			manager = c
		}
	}
	socket := mqttFoundationEnv(app.Fields, "APP_CERT_ISSUER_SOCKET")
	if socket == "" {
		if len(binding.InitContainers) != 0 {
			return accountManagerSocketInitializerBlocker(deploymentplan.ID(object))
		}
		return nil
	}
	block := func() error {
		return &deploymentplan.Blocker{ID: "deployment.pki.account-socket", Resource: deploymentplan.ID(object), Cause: "original App issuer socket and its managed owner cannot be matched", Next: "retain the original management sidecar, shared socket and durable identity references; do not create a static client identity to replace the managed owner; repeat preflight"}
	}
	if !path.IsAbs(socket) || path.Clean(socket) != socket || mqttFoundationEnv(manager.Fields, "PKI_MANAGEMENT_SOCKET") != socket {
		return block()
	}
	volumeFor := func(c environmentContainerBindings) string {
		for _, v := range certIssuerObjectList(c.Fields["volumeMounts"]) {
			m := deploymentplan.Map(v)
			base := deploymentplan.String(m["mountPath"])
			if base != "" && strings.HasPrefix(socket, base+"/") && m["readOnly"] != true {
				return deploymentplan.String(m["name"])
			}
		}
		return ""
	}
	volume := volumeFor(app)
	if volume == "" || volumeFor(manager) != volume {
		return block()
	}
	if deploymentplan.Map(object["spec"])["replicas"] != float64(1) {
		return block()
	}
	deploymentplan.Map(object["spec"])["strategy"] = map[string]any{"type": "Recreate"}
	found, originalStatic, ephemeralSocket := false, false, false
	for _, v := range certIssuerObjectList(binding.PodFields["volumes"]) {
		m := deploymentplan.Map(v)
		if m["name"] == volume {
			found = true
			ephemeralSocket = m["emptyDir"] != nil
		}
		if m["name"] == "account-manager-certissuer-client" {
			originalStatic = true
		}
	}
	if !found {
		return block()
	}
	if ephemeralSocket {
		for _, consumer := range []environmentContainerBindings{app, manager} {
			matched := false
			for _, value := range certIssuerObjectList(consumer.Fields["volumeMounts"]) {
				mount := deploymentplan.Map(value)
				if mount["name"] == volume && path.Dir(socket) == deploymentplan.String(mount["mountPath"])+"/private" && deploymentplan.String(mount["subPath"]) == "" && deploymentplan.String(mount["subPathExpr"]) == "" && mount["readOnly"] != true {
					matched = true
				}
			}
			if !matched {
				return accountManagerSocketInitializerBlocker(deploymentplan.ID(object))
			}
		}
	}
	pod := environmentPKIPod(object)
	export := mqttFoundationEnv(manager.Fields, accountRegistrationCRLExportEnv)
	if export != "" && (export != accountRegistrationCRLBundlePath || mqttFoundationEnv(app.Fields, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL") != export) {
		return accountManagerSocketInitializerBlocker(deploymentplan.ID(object))
	}
	var initializers []any
	for _, bound := range binding.InitContainers {
		c, err := maintainedAccountManagerSocketInitializer(bound, release, volume, deploymentplan.ID(object))
		if err != nil {
			return err
		}
		script := deploymentplan.String(certIssuerObjectList(c["command"])[2])
		if (export != "") != (script == accountManagerPublicCRLPreparation) {
			return accountManagerSocketInitializerBlocker(deploymentplan.ID(object))
		}
		initializers = append(initializers, c)
	}
	if len(initializers) > 1 || ephemeralSocket && len(initializers) != 1 {
		return accountManagerSocketInitializerBlocker(deploymentplan.ID(object))
	}
	if len(initializers) > 0 {
		pod["initContainers"] = initializers
	}
	if originalStatic {
		return nil
	}
	var volumes []any
	for _, v := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(v)["name"] != "account-manager-certissuer-client" {
			volumes = append(volumes, v)
		}
	}
	pod["volumes"] = volumes
	for _, v := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(v)
		var mounts []any
		for _, m := range certIssuerObjectList(c["volumeMounts"]) {
			if deploymentplan.Map(m)["name"] != "account-manager-certissuer-client" {
				mounts = append(mounts, m)
			}
		}
		c["volumeMounts"] = mounts
	}
	// One managed identity writer must stop before its replacement starts.
	deploymentplan.Map(object["spec"])["strategy"] = map[string]any{"type": "Recreate"}
	return nil
}

// The original internal Service targets this named port. Declaration alone
// does not enable a listener; only an already configured owner receives it.
func applyAccountManagerListenerPort(binding environmentPodBindings, container deploymentplan.Object) error {
	if binding.Name != "account-manager" || container["name"] != "pkimanagement" {
		return nil
	}
	address := mqttFoundationEnv(container, "PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR")
	if address == "" {
		return nil
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil || port != "8443" && port != "18443" {
		return &deploymentplan.Blocker{ID: "deployment.pki.account-listener", Resource: binding.Name, Cause: "original managed account listener differs from the shared internal Service port", Next: "reconcile the original account listener and owned internal Service before repeating preflight"}
	}
	name, number := "account-service", 8443
	if port == "18443" {
		pin := mqttFoundationEnv(container, "PKI_MANAGEMENT_IDENTITY_ROOT_SHA256")
		client, host := mqttFoundationEnv(container, "PKI_MANAGEMENT_IDENTITY_STATE"), mqttFoundationEnv(container, "PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE")
		if !environmentPreflightDigest.MatchString(pin) || mqttFoundationEnv(container, "PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256") != pin || mqttFoundationEnv(container, "PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256") != pin || !path.IsAbs(client) || !path.IsAbs(host) || path.Clean(client) != client || path.Clean(host) != host || client == host {
			return &deploymentplan.Blocker{ID: "deployment.pki.account-listener", Resource: binding.Name, Cause: "native account listener lost its original managed client or host lineage", Next: "retain exact managed Account identity states and independent Root pins before repeating preflight"}
		}
		name, number = "service-mtls", 18443
		container["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": name}, "periodSeconds": float64(5)}
	}
	container["ports"] = mergeEnvironmentNamedFields(container["ports"], []any{deploymentplan.Object{"name": name, "containerPort": float64(number), "protocol": "TCP"}})
	return nil
}

// A finite native listener profile adds the matching Service target while the
// maintained service-reg port continues to select the original main listener.
func applyAccountManagerServicePorts(object deploymentplan.Object, bindings environmentIdentityBindings) error {
	if object["kind"] != "Service" || deploymentplan.Map(object["metadata"])["name"] != "account-manager" || deploymentplan.Map(object["metadata"])["namespace"] != bindings.Stack+"-account-manager" {
		return nil
	}
	for _, binding := range bindings.Pods {
		if binding.Name != "account-manager" || binding.NamespaceSuffix != "account-manager" {
			continue
		}
		for _, owner := range binding.Containers {
			if owner.Name != "pkimanagement" {
				continue
			}
			_, port, err := net.SplitHostPort(mqttFoundationEnv(owner.Fields, "PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR"))
			if err != nil || port != "18443" {
				return nil // Existing legacy Account socket profiles keep their catalog.
			}
			container := deploymentplan.Object{"name": owner.Name, "env": owner.Fields["env"]}
			if err := applyAccountManagerListenerPort(binding, container); err != nil {
				return err
			}
			if bindings.Owners[deploymentplan.ID(object)] == "" {
				return &deploymentplan.Blocker{ID: "deployment.pki.account-listener", Resource: deploymentplan.ID(object), Cause: "native Account listener Service has no original owner", Next: "observe its exact original Service UID before rendering the retained managed listener"}
			}
			spec := deploymentplan.Map(object["spec"])
			for _, value := range certIssuerObjectList(spec["ports"]) {
				p := deploymentplan.Map(value)
				if p["name"] == "service-mtls" || p["port"] == float64(18443) {
					if p["name"] != "service-mtls" || p["port"] != float64(18443) || p["targetPort"] != "service-mtls" {
						return &deploymentplan.Blocker{ID: "deployment.pki.account-listener", Resource: deploymentplan.ID(object), Cause: "native Account Service port collides with another maintained consumer", Next: "restore the original distinct registration and managed API Service targets"}
					}
				}
			}
			spec["ports"] = mergeEnvironmentNamedFields(spec["ports"], []any{deploymentplan.Object{"name": "service-mtls", "port": float64(18443), "targetPort": "service-mtls", "protocol": "TCP"}})
		}
	}
	return nil
}
