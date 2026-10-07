package main

import (
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original allocated relay endpoint is a public lifecycle reference, not
// an imported command or Pod spec. SSH behavior comes from this common renderer.
type environmentTurnProfile struct {
	SSHDestination string `json:"ssh_destination"`
}

func turnDeploymentBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.pkiturn", Resource: id, Cause: cause, Next: "retain the existing TURN CLI/SSH Secrets, verifier reference, App trust PVC and restricted SSH endpoint; align the workload with the maintained pkiturn profile without replacing credentials or forwarding to another relay"}
}

var turnSSHHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

func validTurnSSHReference(destination string) bool {
	user, host, ok := strings.Cut(destination, "@")
	return ok && secretEnvironmentPattern.MatchString(user) && (net.ParseIP(host) != nil || turnSSHHost.MatchString(host)) && !strings.ContainsAny(destination, "\r\n\x00")
}

func turnSSHCommand(destination string) []any {
	return []any{"/usr/bin/ssh", "-NT", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=/run/pkiturn-ssh/known_hosts", "-o", "GlobalKnownHostsFile=/dev/null", "-i", "/run/pkiturn-ssh/id_ed25519", "-L", "127.0.0.1:5766:127.0.0.1:5766", destination}
}

// Force replacement of the ephemeral 0400 copies so a non-root initializer
// can retry in the same Pod. Source Secrets and persistent trust data are retained.
const turnPrivatePreparation = "umask 077; mkdir -p /private/cli /private/ssh /run/pki-state/app; cp -f /source/cli/password /private/cli/password; cp -f /source/ssh/id_ed25519 /private/ssh/id_ed25519; cp -f /source/ssh/known_hosts /private/ssh/known_hosts; chmod 400 /private/cli/password /private/ssh/id_ed25519; chmod 444 /private/ssh/known_hosts; chmod 700 /run/pki-state/app; find /run/pki-state/app -type f -exec chmod 600 {} +"

func observeEnvironmentTurnProfile(object deploymentplan.Object, release environmentRelease) (*environmentTurnProfile, error) {
	id := deploymentplan.ID(object)
	bad := func(cause string) (*environmentTurnProfile, error) { return nil, turnDeploymentBlocker(id, cause) }
	pod := environmentPKIPod(object)
	containers, err := environmentPKIPodContainers(pod)
	if err != nil || len(containers) != 3 || containers["containers/pkiturn"] == nil || containers["containers/coturn-cli-tunnel"] == nil || containers["initContainers/prepare-pkiturn-private"] == nil {
		return bad("pkiturn requires its main consumer, restricted SSH tunnel and private-file initializer")
	}
	tunnel := containers["containers/coturn-cli-tunnel"]
	command := certIssuerObjectList(tunnel["command"])
	if len(command) == 0 {
		return bad("existing SSH relay endpoint cannot be verified")
	}
	destination := deploymentplan.String(command[len(command)-1])
	if !validTurnSSHReference(destination) || !reflect.DeepEqual(command, turnSSHCommand(destination)) || len(certIssuerObjectList(tunnel["args"])) != 0 {
		return bad("SSH tunnel must retain strict host pins and forward only the loopback Coturn CLI")
	}
	main, init := containers["containers/pkiturn"], containers["initContainers/prepare-pkiturn-private"]
	legacyPreparation := strings.ReplaceAll(turnPrivatePreparation, "; cp -f ", "; cp ")
	if !reflect.DeepEqual(main["command"], []any{"/app/pkiturn", "watch"}) || len(certIssuerObjectList(main["args"])) != 0 || !reflect.DeepEqual(init["command"], []any{"/bin/sh", "-ec"}) || !reflect.DeepEqual(init["args"], []any{turnPrivatePreparation}) && !reflect.DeepEqual(init["args"], []any{legacyPreparation}) {
		return bad("existing TURN launch or private-file preparation differs from the maintained profile")
	}
	settings, err := environmentPKINamedFields(main["env"])
	if err != nil || settings["PKI_TURN_DEDICATED_RELAY"]["value"] != "true" || settings["PKI_TURN_CLI_ADDR"]["value"] != "127.0.0.1:5766" || settings["PKI_TURN_CLI_PASSWORD_FILE"]["value"] != "/run/pkiturn-cli/password" || settings["PKI_TURN_HEALTH_FILE"]["value"] != "/run/pkiturn-health/health.json" {
		return bad("existing dedicated relay and private runtime paths differ from the maintained TURN profile")
	}
	ttl, err := time.ParseDuration(deploymentplan.String(settings["PKI_TURN_CREDENTIAL_TTL"]["value"]))
	database := deploymentplan.Map(deploymentplan.Map(settings["PKI_DATABASE_URL"]["valueFrom"])["secretKeyRef"])
	if err != nil || ttl <= 0 || ttl > time.Minute || database["name"] == nil || database["key"] == nil || deploymentplan.String(settings["PKI_TURN_REDIS_ADDR"]["value"]) == "" {
		return bad("existing verifier Secret reference, signaling Redis or bounded credential TTL is missing")
	}
	for name, container := range containers {
		repository := environmentImageRepository(deploymentplan.String(container["image"]))
		if repository != environmentImageRepository(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) && !(name == "initContainers/prepare-pkiturn-private" && repository == "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api") {
			return bad("existing TURN container has an unsupported image role")
		}
		security := deploymentplan.Map(container["securityContext"])
		if security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["runAsNonRoot"] != true {
			return bad("private files and trust state require the existing non-root UID/GID 10001")
		}
	}
	return &environmentTurnProfile{SSHDestination: destination}, nil
}

func validateEnvironmentTurnBinding(binding environmentPodBindings) error {
	bad := func(cause string) error { return turnDeploymentBlocker(binding.Name, cause) }
	if binding.Kind != "Deployment" || binding.Name != "pkiturn" || binding.NamespaceSuffix != "video-cloud" || binding.Turn == nil || !validTurnSSHReference(binding.Turn.SSHDestination) || len(binding.Containers) != 2 || len(binding.InitContainers) != 1 {
		return bad("complete original TURN consumer and relay references are required")
	}
	if !keySet("pkiturn", "coturn-cli-tunnel")[binding.Containers[0].Name] || !keySet("pkiturn", "coturn-cli-tunnel")[binding.Containers[1].Name] || binding.Containers[0].Name == binding.Containers[1].Name || binding.InitContainers[0].Name != "prepare-pkiturn-private" {
		return bad("TURN container identities differ from the maintained profile")
	}
	volumes, err := environmentPKINamedFields(binding.PodFields["volumes"])
	if err != nil {
		return bad("original TURN volume references are ambiguous")
	}
	for name, kind := range map[string]string{"cli-source": "secret", "ssh-source": "secret", "cli-private": "emptyDir", "ssh-private": "emptyDir", "health": "emptyDir", "pki-state": "persistentVolumeClaim"} {
		if volumes[name][kind] == nil {
			return bad("original TURN credential or persistent trust volume is absent")
		}
	}
	for _, container := range append(append([]environmentContainerBindings{}, binding.Containers...), binding.InitContainers...) {
		mounts, err := environmentPKINamedFields(container.Fields["volumeMounts"])
		if err != nil {
			return bad("original TURN file mounts are ambiguous")
		}
		paths := map[string]string{}
		switch container.Name {
		case "pkiturn":
			paths = map[string]string{"cli-private": "/run/pkiturn-cli", "health": "/run/pkiturn-health", "pki-state": "/run/pki-state"}
		case "coturn-cli-tunnel":
			paths = map[string]string{"ssh-private": "/run/pkiturn-ssh"}
		case "prepare-pkiturn-private":
			paths = map[string]string{"cli-source": "/source/cli", "ssh-source": "/source/ssh", "cli-private": "/private/cli", "ssh-private": "/private/ssh", "pki-state": "/run/pki-state"}
		}
		for name, path := range paths {
			mount := mounts[name]
			readOnly := name == "cli-source" || name == "ssh-source" || container.Name == "pkiturn" && name == "cli-private" || container.Name == "coturn-cli-tunnel"
			if mount["mountPath"] != path || (mount["readOnly"] == true) != readOnly || mount["subPath"] != nil || mount["subPathExpr"] != nil {
				return bad("TURN private-file initialization or consumer mount would remap an original credential/state path")
			}
		}
	}
	return nil
}

func renderEnvironmentTurn(env map[string]string, binding environmentPodBindings) (deploymentplan.Object, error) {
	if err := validateEnvironmentTurnBinding(binding); err != nil {
		return nil, err
	}
	security := func() map[string]any {
		return map[string]any{"runAsUser": float64(10001), "runAsGroup": float64(10001), "runAsNonRoot": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	}
	resources := func(cpu, memory, limit string) map[string]any {
		return map[string]any{"requests": map[string]any{"cpu": cpu, "memory": memory}, "limits": map[string]any{"memory": limit}}
	}
	probe := func(failures float64) map[string]any {
		return map[string]any{"exec": map[string]any{"command": []any{"/app/pkiturn", "health"}}, "periodSeconds": float64(10), "timeoutSeconds": float64(3), "failureThreshold": failures}
	}
	startup := probe(24)
	startup["periodSeconds"] = float64(5)
	labels := map[string]any{"app.kubernetes.io/name": "pkiturn", "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}
	pod := map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"fsGroup": float64(10001), "fsGroupChangePolicy": "OnRootMismatch"}, "containers": []any{
		map[string]any{"name": "pkiturn", "command": []any{"/app/pkiturn", "watch"}, "securityContext": security(), "resources": resources("50m", "64Mi", "256Mi"), "startupProbe": startup, "readinessProbe": probe(2), "livenessProbe": probe(3)},
		map[string]any{"name": "coturn-cli-tunnel", "command": turnSSHCommand(binding.Turn.SSHDestination), "securityContext": security(), "resources": resources("10m", "16Mi", "64Mi")},
	}, "initContainers": []any{map[string]any{"name": "prepare-pkiturn-private", "command": []any{"/bin/sh", "-ec"}, "args": []any{turnPrivatePreparation}, "securityContext": security(), "resources": resources("25m", "32Mi", "64Mi")}}}
	return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "pkiturn", "namespace": lkeNamespaceName(env, "video-cloud")}, "spec": map[string]any{"replicas": float64(1), "strategy": map[string]any{"type": "Recreate"}, "selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "pkiturn"}}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": pod}}}, nil
}
