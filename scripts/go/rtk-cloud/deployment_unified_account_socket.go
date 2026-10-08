package main

import (
	"path"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// Socket ownership is an existing identity reference, not a static client leaf.
func applyAccountManagerSocketProfile(object deploymentplan.Object, binding environmentPodBindings, owned bool) error {
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
	found, originalStatic := false, false
	for _, v := range certIssuerObjectList(binding.PodFields["volumes"]) {
		m := deploymentplan.Map(v)
		if m["name"] == volume {
			found = true
		}
		if m["name"] == "account-manager-certissuer-client" {
			originalStatic = true
		}
	}
	if !found {
		return block()
	}
	if originalStatic {
		return nil
	}
	pod := environmentPKIPod(object)
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
