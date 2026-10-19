package main

import (
	"errors"
	"path"
	"regexp"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These are observed lifecycle references, not a private desired Pod spec.
// The mounted provider configuration (including seal) is retained and fenced.
type environmentOpenBaoProfile struct {
	Backend             string `json:"backend"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	ConfigKey           string `json:"config_key"`
	RuntimeDir          string `json:"runtime_dir,omitempty"`
	PIDFile             string `json:"pid_file,omitempty"`
}

func openBaoCompatibilityBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.openbao", Resource: id, Cause: cause, Next: "align the shared OpenBao backend, storage sizes and StatefulSet policy with the existing installation; preserve configuration, seal, TLS identities and original PVCs; backend or state remapping requires a separately verified migration"}
}

// Only the supported mounted HCL profile is recognized. It remains untouched;
// an unfamiliar or ambiguous configuration is not guessed into a new backend.
var openBaoStorageBlock = regexp.MustCompile(`(?m)^\s*storage\s+"(file|raft)"\s*\{`)
var openBaoStorageDeclaration = regexp.MustCompile(`(?m)^\s*storage\b`)
var openBaoConfigKey = regexp.MustCompile(`^[a-zA-Z0-9_.-]+\.hcl$`)

func observeOpenBaoProfile(object deploymentplan.Object, live []deploymentplan.Object) (*environmentOpenBaoProfile, error) {
	id := deploymentplan.ID(object)
	bad := func(cause string) (*environmentOpenBaoProfile, error) {
		return nil, openBaoCompatibilityBlocker(id, cause)
	}
	if object["kind"] != "StatefulSet" || !strings.HasSuffix(deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"]), "-secrets") {
		return bad("OpenBao requires its existing secrets StatefulSet owner")
	}
	pod := environmentPKIPod(object)
	containers, err := environmentPKIPodContainers(pod)
	if err != nil || containers["containers/openbao"] == nil {
		return bad("OpenBao primary container identity is missing or ambiguous")
	}
	main := containers["containers/openbao"]
	mounts, err := environmentPKINamedFields(main["volumeMounts"])
	if err != nil {
		return bad("OpenBao configuration mounts are ambiguous")
	}
	volumeName := ""
	for name, mount := range mounts {
		if mount["mountPath"] == "/openbao/config" && mount["subPath"] == nil && mount["subPathExpr"] == nil {
			if volumeName != "" {
				return bad("multiple OpenBao configuration mounts exist")
			}
			volumeName = name
		}
	}
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return bad("OpenBao volume references are ambiguous")
	}
	configName := deploymentplan.String(deploymentplan.Map(volumes[volumeName]["configMap"])["name"])
	profile := &environmentOpenBaoProfile{}
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	for _, resource := range live {
		if configName == "" || deploymentplan.ID(resource) != "ConfigMap/"+ns+"/"+configName {
			continue
		}
		data := deploymentplan.Map(resource["data"])
		profile.ConfigurationSHA256 = deploymentplan.Digest(data)
		if len(data) != 1 {
			return bad("OpenBao requires one retained HCL configuration key")
		}
		for key, value := range data {
			hcl := deploymentplan.String(value)
			blocks := openBaoStorageBlock.FindAllStringSubmatch(hcl, -1)
			if !openBaoConfigKey.MatchString(key) || len(blocks) != 1 || len(openBaoStorageDeclaration.FindAllString(hcl, -1)) != 1 || strings.Contains(hcl, "/*") || strings.Contains(hcl, "${") {
				return bad("OpenBao storage configuration is unsupported or ambiguous")
			}
			profile.Backend, profile.ConfigKey = blocks[0][1], key
			// Old chart substitutions are not imported as launch behavior.
			for _, token := range []string{"HOST_IP", "POD_IP", "HOSTNAME", "API_ADDR", "TRANSIT_ADDR", "RAFT_ADDR"} {
				if strings.Contains(hcl, token) {
					return bad("OpenBao configuration still requires legacy chart substitutions")
				}
			}
		}
	}
	if items := certIssuerObjectList(deploymentplan.Map(volumes[volumeName]["configMap"])["items"]); len(items) != 0 {
		if len(items) != 1 || deploymentplan.Map(items[0])["key"] != profile.ConfigKey || deploymentplan.Map(items[0])["path"] != profile.ConfigKey {
			return bad("OpenBao configuration key projection differs from the supported mounted path")
		}
	}
	if profile.Backend == "file" && envObjectReplicas(object) != 1 {
		return bad("existing file storage must have exactly one writer")
	}
	if profile.Backend == "" {
		return bad("OpenBao mounted configuration cannot be verified")
	}
	if sidecar := containers["containers/openbao-pki"]; sidecar != nil {
		init := containers["initContainers/openbao-pki-install"]
		if init == nil || len(containers) != 3 || pod["shareProcessNamespace"] != true || profile.Backend != "file" {
			return bad("managed OpenBao transport requires its installer, sidecar and shared process namespace")
		}
		settings, e := environmentPKINamedFields(sidecar["env"])
		initial, ie := environmentPKINamedFields(init["env"])
		if e != nil || ie != nil {
			return bad("OpenBao transport settings are ambiguous")
		}
		profile.RuntimeDir = deploymentplan.String(settings["OPENBAO_PKI_RUNTIME_DIR"]["value"])
		profile.PIDFile = deploymentplan.String(settings["OPENBAO_PKI_PROCESS_PID_FILE"]["value"])
		if !path.IsAbs(profile.RuntimeDir) || !path.IsAbs(profile.PIDFile) || profile.RuntimeDir != path.Clean(profile.RuntimeDir) || !strings.HasPrefix(profile.PIDFile, profile.RuntimeDir+"/") || initial["OPENBAO_PKI_RUNTIME_DIR"]["value"] != profile.RuntimeDir || initial["OPENBAO_PKI_PROCESS_PID_FILE"]["value"] != profile.PIDFile {
			return bad("OpenBao installer and reload sidecar have inconsistent runtime/PID paths")
		}
	} else if len(containers) != 1 {
		return bad("OpenBao contains an unsupported sidecar or init container")
	}
	return profile, nil
}

func validateOpenBaoTargetProfile(env map[string]string, binding environmentPodBindings) error {
	backend := env["OPENBAO_STORAGE_BACKEND"]
	if backend != "file" && backend != "raft" {
		return openBaoCompatibilityBlocker(binding.Name, "shared OPENBAO_STORAGE_BACKEND must explicitly select file or raft")
	}
	if binding.OpenBao != nil && binding.OpenBao.Backend != backend {
		return openBaoCompatibilityBlocker(binding.Name, "existing OpenBao backend differs from shared OPENBAO_STORAGE_BACKEND")
	}
	if backend == "raft" {
		if binding.OpenBao != nil && (binding.OpenBao.ConfigKey != "raft.hcl" || binding.OpenBao.ConfigurationSHA256 != deploymentplan.Digest(environmentOpenBaoRaftDependencies(env)[0]["data"])) {
			return openBaoCompatibilityBlocker(binding.Name, "existing Raft configuration differs from the maintained Raft profile")
		}
		return validateOpenBaoBindingProfile(binding)
	}
	if binding.OpenBao == nil || binding.OpenBao.ConfigKey == "" {
		return openBaoCompatibilityBlocker(binding.Name, "file storage requires the observed original provider configuration")
	}
	if binding.OpenBao.RuntimeDir != "" && !immutableEnvironmentImage.MatchString(env["LKE_OPENBAO_PKI_IMAGE"]) {
		return &deploymentplan.Blocker{ID: "deployment.release.openbao-pki", Cause: "enabled OpenBao transport installer/sidecar has no exact-source formal CI image", Next: "publish video-cloud-openbao-pki in the selected video-cloud image-manifest.json, then repeat full preflight; do not reuse a local dev image"}
	}
	if env["OPENBAO_EFFECTIVE_REPLICAS"] != "1" || env["DEPLOYMENT_PROTECTION_PROFILE"] == "production" || env["CLOUD_ENV_NAME"] == "prod" {
		return openBaoCompatibilityBlocker(binding.Name, "file storage supports one existing writer; production activation requires a separately qualified HA profile")
	}
	return nil
}

func environmentOpenBaoDependencies(env map[string]string) []deploymentplan.Object {
	dependencies := environmentOpenBaoRaftDependencies(env)
	if env["OPENBAO_STORAGE_BACKEND"] == "file" {
		// Keep the original mounted configuration as a retained dependency. Do
		// not replace its backend, seal or provider auth with a generated file.
		dependencies = dependencies[1:]
	}
	return dependencies
}

func renderEnvironmentOpenBao(env map[string]string, binding environmentPodBindings) (deploymentplan.Object, error) {
	if err := validateOpenBaoTargetProfile(env, binding); err != nil {
		return nil, err
	}
	object, err := renderEnvironmentOpenBaoRaft(env, binding)
	if err != nil {
		return nil, err
	}
	pod := environmentPKIPod(object)
	// Both provider and native host images use the selected namespace's already
	// installed registry identity. Ambient process settings are not authority.
	pod["imagePullSecrets"] = []any{deploymentplan.Object{"name": environmentOpenBaoImagePullSecretName(env)}}
	if env["OPENBAO_STORAGE_BACKEND"] == "raft" {
		return object, nil
	}
	delete(pod, "affinity")
	delete(pod, "volumes") // All file backend references come from observed lifecycle owners.
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	delete(main, "volumeMounts")
	delete(main, "env")
	profile := binding.OpenBao
	launch := "exec bao server -config=" + shellQuote("/openbao/config/"+profile.ConfigKey)
	ready := "bao status -address=https://127.0.0.1:8200 -tls-skip-verify"
	if profile.RuntimeDir != "" {
		// Kubernetes expands $$ to $ before the shell sees this command. Escape
		// both shell PID dollars so the marker names the process that execs bao.
		launch = "umask 077; printf '%s\\n' \"$$$$\" > " + shellQuote(profile.PIDFile) + "; " + launch
		ready = "test -f " + shellQuote(profile.RuntimeDir+"/ready") + " && " + ready
		pod["shareProcessNamespace"] = true
		sidecar := func(name string, install bool) map[string]any {
			command := []any{"/usr/local/bin/openbaopkihost"}
			if install {
				command = []any{"/bin/sh", "-ec", "umask 077; mkdir -p " + environmentCreateServiceState + "/private/crl; chmod 700 " + environmentCreateServiceState + "/private " + environmentCreateServiceState + "/private/crl; exec /usr/local/bin/openbaopkihost install"}
			}
			return map[string]any{"name": name, "image": env["LKE_OPENBAO_PKI_IMAGE"], "command": command, "securityContext": map[string]any{"runAsNonRoot": true, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
		}
		pod["containers"] = append(certIssuerObjectList(pod["containers"]), sidecar("openbao-pki", false))
		pod["initContainers"] = []any{sidecar("openbao-pki-install", true)}
	}
	main["command"] = []any{"/bin/sh", "-ec", launch}
	main["readinessProbe"] = map[string]any{"exec": map[string]any{"command": []any{"/bin/sh", "-ec", ready}}, "periodSeconds": float64(5), "timeoutSeconds": float64(3)}
	return object, nil
}

// Retain only the StatefulSet's immutable allocation, never its mutable desired
// workload spec. Any maintained size/policy mismatch remains a migration fence.
func bindOpenBaoAllocations(definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	for _, definition := range definitions {
		want := definition.Object
		if want["kind"] != "StatefulSet" || deploymentplan.Map(want["metadata"])["name"] != "openbao" {
			continue
		}
		for _, current := range live {
			if deploymentplan.ID(current) != deploymentplan.ID(want) {
				continue
			}
			spec, original := deploymentplan.Map(want["spec"]), deploymentplan.Map(current["spec"])
			if spec["serviceName"] != original["serviceName"] {
				return openBaoCompatibilityBlocker(deploymentplan.ID(want), "maintained OpenBao headless service differs from the existing immutable binding")
			}
			if spec["podManagementPolicy"] != original["podManagementPolicy"] {
				return openBaoCompatibilityBlocker(deploymentplan.ID(want), "shared pod management policy differs from the immutable existing policy")
			}
			claims := map[string]any{}
			for _, value := range certIssuerObjectList(original["volumeClaimTemplates"]) {
				claim := deploymentplan.Map(value)
				name := deploymentplan.String(deploymentplan.Map(claim["metadata"])["name"])
				if claims[name] != nil {
					return errors.New("duplicate OpenBao claim allocation")
				}
				claims[name] = deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(claim["spec"])["resources"])["requests"])["storage"]
			}
			if len(claims) != 2 {
				return openBaoCompatibilityBlocker(deploymentplan.ID(want), "existing OpenBao claim allocation is unsupported")
			}
			for _, value := range certIssuerObjectList(spec["volumeClaimTemplates"]) {
				claim := deploymentplan.Map(value)
				name := deploymentplan.String(deploymentplan.Map(claim["metadata"])["name"])
				size := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(claim["spec"])["resources"])["requests"])["storage"]
				if size != claims[name] {
					return openBaoCompatibilityBlocker(deploymentplan.ID(want), "shared OpenBao claim size differs from the existing allocation")
				}
			}
			for _, field := range []string{"selector", "serviceName", "volumeClaimTemplates"} {
				spec[field] = deploymentplan.Clone(current)["spec"].(map[string]any)[field]
			}
			labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(spec["template"])["metadata"])["labels"])
			for key, value := range deploymentplan.Map(deploymentplan.Map(original["selector"])["matchLabels"]) {
				labels[key] = value
			}
		}
	}
	return nil
}
