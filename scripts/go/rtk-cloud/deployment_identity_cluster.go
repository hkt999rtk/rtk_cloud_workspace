package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A lost local store is not permission to replace the live transport CA.
func lkeCheckDeploymentIdentityContinuity(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if activeSecretEnvironmentRoot == "" {
		return nil
	}
	type target struct {
		namespace, name, dir string
		files                map[string]string
	}
	var targets []target
	if lkeWorkloadSelected(env, opts, "video-cloud") {
		if err := lkeRequireBaselineIdentityDeployment(env, "video-cloud", "video-cloud-api"); err != nil {
			return err
		}
	}
	if lkeWorkloadSelected(env, opts, "video-cloud") && len(opts.workloads) == 0 {
		if err := lkeRequireBaselineIdentityDeployment(env, "video-cloud", "certissuer"); err != nil {
			return err
		}
		targets = append(targets,
			target{"video-cloud", "certissuer-runtime", sensitiveEnvironmentPath(paths, "certissuer"), map[string]string{"server.crt": "tls.crt", "server.key": "tls.key", "service-ca.crt": "client-ca.crt"}},
			target{"video-cloud", "factoryenroll-certissuer-client", sensitiveEnvironmentPath(paths, "certissuer"), map[string]string{"factory.crt": "client.crt", "factory.key": "client.key", "service-ca.crt": "ca.crt"}},
			target{"secrets", "openbao-tls", firstNonEmpty(os.Getenv("RTK_CLOUD_OPENBAO_STATE_DIR"), sensitiveEnvironmentPath(paths, "openbao")), map[string]string{"tls-ca.crt": "ca.crt", "tls.crt": "tls.crt", "tls.key": "tls.key"}},
			target{"video-cloud", "mqtt-runtime", sensitiveEnvironmentPath(paths, "mqtt-tls"), map[string]string{"server.crt": "tls.crt", "server.key": "tls.key", "ca.crt": "ca.crt"}},
		)
	}
	if lkeWorkloadSelected(env, opts, "account-manager") {
		if err := lkeRequireBaselineIdentityDeployment(env, "account-manager", "account-manager"); err != nil {
			return err
		}
		targets = append(targets, target{"account-manager", "account-manager-certissuer-client", sensitiveEnvironmentPath(paths, "certissuer"), map[string]string{"client.crt": "client.crt", "client.key": "client.key", "service-ca.crt": "ca.crt"}})
	}
	for _, target := range targets {
		live, err := lkeGetOptionalSecret(lkeNamespaceName(env, target.namespace), target.name)
		if err != nil {
			return err
		}
		if live == nil {
			continue
		}
		for file, key := range target.files {
			local, err := os.ReadFile(filepath.Join(target.dir, file))
			if err != nil {
				return fmt.Errorf("existing %s requires local %s; restore this environment's credential before redeployment", target.name, file)
			}
			remote, err := kubernetesSecretBytes(live, key)
			if err != nil {
				return fmt.Errorf("existing %s: %w", target.name, err)
			}
			if target.name == "mqtt-runtime" && file == "server.crt" {
				localChain, le := pemCertificates(local)
				remoteChain, re := pemCertificates(remote)
				if le != nil || re != nil || !bytes.Equal(localChain[0].Raw, remoteChain[0].Raw) {
					return fmt.Errorf("MQTT deployed leaf differs from environment identity; no overwrite")
				}
				continue
			}
			if !bytes.Equal(bytes.TrimSpace(local), bytes.TrimSpace(remote)) {
				return fmt.Errorf("%s/%s differs from environment identity; reconcile before redeployment (no overwrite)", target.name, key)
			}
		}
	}
	return nil
}

// Managed state needs its own renderer/patch path. Detect it before a baseline
// Deployment manifest could remove the owning sidecar, mounts or settings.
func lkeRequireBaselineIdentityDeployment(env map[string]string, namespaceKey, name string) error {
	raw, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, namespaceKey), "get", "deployment", name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect %s identity owner failed", name)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name, Value string
							ValueFrom   any
						}
						VolumeMounts []struct{ MountPath string }
					}
				}
			}
		}
	}
	if json.Unmarshal(raw, &deployment) != nil {
		return fmt.Errorf("invalid %s deployment response", name)
	}
	for _, c := range deployment.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if (strings.HasSuffix(e.Name, "_IDENTITY_STATE") || e.Name == "PKI_CONTROLLER_SOCKET") && (e.Value != "" || e.ValueFrom != nil) {
				return fmt.Errorf("%s has a managed identity owner; use its managed renderer/patch path, baseline replacement is refused", name)
			}
		}
		for _, m := range c.VolumeMounts {
			if strings.HasPrefix(m.MountPath, "/var/lib/account-pki") {
				return fmt.Errorf("%s has managed identity storage; baseline replacement is refused", name)
			}
		}
	}
	return nil
}
