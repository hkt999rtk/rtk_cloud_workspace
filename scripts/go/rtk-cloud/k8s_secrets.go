package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
)

func newK8SSecretObject(namespace, name string, stringData map[string]string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"type":       "Opaque",
		"stringData": stringData,
	}
}

func applyKubernetesObjectJSON(obj any) error {
	payload, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	out, err := kubectlCombinedOutput(bytes.NewReader(payload), "apply", "-f", "-")
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	return err
}

func newK8SDockerConfigSecretObject(namespace, name, username, token string) (map[string]any, error) {
	config, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			"ghcr.io": map[string]string{
				"auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + token)),
			},
		},
	})
	if err != nil {
		return nil, err
	}
	secret := newK8SSecretObject(namespace, name, map[string]string{".dockerconfigjson": string(config)})
	secret["type"] = "kubernetes.io/dockerconfigjson"
	return secret, nil
}
