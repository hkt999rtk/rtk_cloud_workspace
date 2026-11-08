package main

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCustodyHealthContextUsesSelectedImageAndOneFrozenEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, environment)
			cfg.Values["PKI_STATE_BACKEND"] = "openbao"
			image := "ghcr.io/fixture/native@sha256:" + strings.Repeat("a", 64)
			if err := store.write("operator/env/LKE_PKI_CONTROLLER_IMAGE", []byte(image), true); err != nil {
				t.Fatal(err)
			}
			kube, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write("kube/kubeconfig.yaml", kube, true); err != nil {
				t.Fatal(err)
			}
			runtime := newEnvironmentCheckRuntime(t.Context(), cfg, store, nil, "")
			if runtime.custodyError != nil || runtime.custodyReader == nil || runtime.custodyReader.image != image || runtime.custodyReader.store.Root != store.Root {
				t.Fatal("health did not consume selected environment image/store", runtime.custodyError)
			}
			if runtime.frozenBackend == nil || !bytes.Equal(runtime.frozenBackend.KubeconfigContent, runtime.custodyReader.backend.KubeconfigContent) {
				t.Fatal("ordinary health reads and custody helper can use different Kubernetes identities")
			}
			original := append([]byte(nil), runtime.frozenBackend.KubeconfigContent...)
			if err := store.write("kube/kubeconfig.yaml", []byte("foreign-replacement-canary"), true); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(runtime.frozenBackend.KubeconfigContent, original) || !bytes.Equal(runtime.custodyReader.backend.KubeconfigContent, original) {
				t.Fatal("checker switched to replacement canonical authentication")
			}
		})
	}
}
