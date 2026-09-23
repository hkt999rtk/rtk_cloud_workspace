package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type missingSecretBinding struct {
	namespace, secret string
	data              map[string]string
}

// syncMissingSecretBindings is a narrow repair path for a deployed stack whose
// canonical SecretStore has gained bindings since its Kubernetes Secrets were
// created. It never replaces an existing value or creates a missing Secret.
func syncMissingSecretBindings(out io.Writer, store secretStore, dryRun bool) error {
	if err := verifySecretStoreContents(store); err != nil {
		return err
	}
	kubeconfig := store.KubeconfigPath()
	if info, err := os.Stat(kubeconfig); err != nil || !info.Mode().IsRegular() {
		return errors.New("environment kubeconfig is unavailable")
	}
	stack := "video-cloud-" + store.Environment
	bindings := map[string]*missingSecretBinding{}
	seen := map[string]map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		if len(entry.K8SBinding) == 0 {
			continue
		}
		canonical, err := store.readRuntime(entry.ID)
		if err != nil || canonical == "" {
			return fmt.Errorf("canonical runtime secret %s is unavailable", entry.ID)
		}
		for _, binding := range entry.K8SBinding {
			namespace := stack + binding.NamespaceSuffix
			id := namespace + "/" + binding.Secret
			current, ok := seen[id]
			if !ok {
				raw, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", binding.Secret, "-o", "json").Output()
				if err != nil {
					return fmt.Errorf("existing Kubernetes Secret %s is unavailable", id)
				}
				var secret struct {
					Data map[string]string `json:"data"`
				}
				if json.Unmarshal(raw, &secret) != nil || secret.Data == nil {
					return fmt.Errorf("existing Kubernetes Secret %s has invalid metadata", id)
				}
				current = secret.Data
				seen[id] = current
			}
			if encoded := current[binding.Key]; encoded != "" {
				value, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil || strings.TrimSpace(string(value)) != canonical {
					return fmt.Errorf("existing Kubernetes binding %s:%s differs from canonical %s; automatic replacement is refused", id, binding.Key, entry.ID)
				}
				continue
			}
			if bindings[id] == nil {
				bindings[id] = &missingSecretBinding{namespace: namespace, secret: binding.Secret, data: map[string]string{}}
			}
			bindings[id].data[binding.Key] = base64.StdEncoding.EncodeToString([]byte(canonical))
		}
	}
	ids := make([]string, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		binding := bindings[id]
		keys := make([]string, 0, len(binding.data))
		for key := range binding.data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(out, "%s missing: %s\n", id, strings.Join(keys, ", "))
	}
	if dryRun || len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		binding := bindings[id]
		payload, err := json.Marshal(map[string]any{"data": binding.data})
		if err != nil {
			return err
		}
		cmd := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", binding.namespace, "patch", "secret", binding.secret, "--type=merge", "--patch-file=/dev/stdin")
		cmd.Stdin = bytes.NewReader(payload)
		if _, err := cmd.Output(); err != nil {
			return fmt.Errorf("patch missing bindings in %s failed", id)
		}
	}
	if err := verifySecretStoreK8SBindings(store); err != nil {
		return fmt.Errorf("Kubernetes binding verification after sync failed: %w", err)
	}
	fmt.Fprintf(out, "synchronized %d Kubernetes Secret(s) from canonical SecretStore\n", len(ids))
	return nil
}
