package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// The accepted native Service recipe serves from its own host identity Store.
// Static-TLS defaults cannot resurrect a different Secret-backed serving mode.
func applyEnvironmentControllerNativeIdentity(object deploymentplan.Object, binding environmentPodBindings) error {
	bad := func() error {
		return environmentPKITransitionBlocker(deploymentplan.ID(object), "native Controller serving identity requires the original maintained Service state, root and trust mounts")
	}
	if binding.Component != "pki-controller" || binding.Kind != "Deployment" || binding.NamespaceSuffix != "video-cloud" || binding.Name != "pki-controller" || len(binding.Containers) != 1 || binding.Containers[0].Name != "pki-controller" {
		return bad()
	}
	bound := binding.Containers[0]
	settings, err := environmentPKISettings(bound.Fields["env"])
	if err != nil {
		return bad()
	}
	value := func(name string) string { return deploymentplan.String(settings[name]["value"]) }
	if settings["PKI_HOST_IDENTITY_STATE"] == nil && settings["PKI_SERVICE_CLIENT_IDENTITY_STATE"] == nil {
		return nil // Existing static-TLS profiles retain their original defaults.
	}
	paths := map[string]string{
		"PKI_ACCOUNT_MANAGER_PUBLIC_KEY":         "/run/platform-key/public.pem",
		"PKI_HOST_IDENTITY_STATE":                environmentCreateServiceOwnerIdentityPath("pki-controller", "server.json"),
		"PKI_SERVICE_CLIENT_IDENTITY_STATE":      environmentCreateServiceOwnerIdentityPath("pki-controller", "client.json"),
		"PKI_CLIENT_CA":                          environmentCreateServiceTrust + "/root.pem",
		"PKI_SERVICE_CLIENT_BUNDLE_MANIFEST":     environmentCreateServiceTrust + "/bundles.json",
		"PKI_SERVICE_CLIENT_SERVER_CRL_MANIFEST": environmentCreateServiceTrust + "/listener-crls.json",
	}
	for name, expected := range paths {
		if value(name) != expected || len(settings[name]) != 1 {
			return bad()
		}
	}
	root := value("PKI_HOST_ROOT_SHA256")
	if !environmentPreflightDigest.MatchString(root) || value("PKI_SERVICE_CLIENT_ROOT_SHA256") != root || settings["PKI_SERVER_CERT"] != nil || settings["PKI_SERVER_KEY"] != nil {
		return bad()
	}
	volumes, vErr := environmentPKINamedFields(binding.PodFields["volumes"])
	mounts, mErr := environmentPKINamedFields(bound.Fields["volumeMounts"])
	if vErr != nil || mErr != nil || volumes["pki-tls"] != nil || mounts["pki-tls"] != nil || volumes["account-key"] != nil || mounts["account-key"] != nil {
		return bad()
	}
	for _, expected := range []struct {
		name, kind, source, path string
		readOnly                 bool
	}{
		{"service-state", "persistentVolumeClaim", environmentCreateServiceOwnerClaim("pki-controller"), environmentCreateServiceState, false},
		{"service-trust", "configMap", "pki-create-service-trust", environmentCreateServiceTrust, true},
		{"platform-key", "secret", "pki-platform-key", "/run/platform-key", true},
	} {
		volume, mount := volumes[expected.name], mounts[expected.name]
		wanted := environmentCreateServiceVolume(expected.name, expected.kind, expected.source)
		if volume == nil || mount == nil || deploymentplan.Digest(environmentPKIVolumeSource(volume)) != deploymentplan.Digest(environmentPKIVolumeSource(deploymentplan.Map(wanted))) || mount["mountPath"] != expected.path || (mount["readOnly"] == true) != expected.readOnly || mount["subPath"] != nil || mount["subPathExpr"] != nil {
			return bad()
		}
	}
	pod := environmentPKIPod(object)
	containers, err := environmentPKIPodContainers(pod)
	if err != nil || containers["containers/pki-controller"] == nil {
		return bad()
	}
	container := containers["containers/pki-controller"]
	// No authenticated source references this legacy volume. Remove only the
	// renderer's static serving defaults, leaving every bound lifecycle input.
	container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_SERVER_CERT", "PKI_SERVER_KEY")
	container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "pki-tls")
	pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "pki-tls")
	container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "account-key")
	pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "account-key")
	return nil
}
