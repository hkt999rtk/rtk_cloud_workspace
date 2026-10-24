package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestFrozenDeploymentChecksKeepQualifiedIdentityAfterCanonicalFileChanges(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			kube := frozenKubernetesFixture(t)
			store := makeIsolatedTestSecretStore(t, environment)
			raw, err := yaml.Marshal(kube)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
			if err != nil {
				t.Fatal(err)
			}
			runtime := newFrozenDeploymentCheckRuntime(context.Background(), environment, backend)
			if err := store.write("kube/kubeconfig.yaml", []byte("foreign-canonical-authentication-canary"), true); err != nil {
				t.Fatal(err)
			}
			calls := deploymentRuntimeTestKubectl(t, `
printf '%s\n' "$*" >> "$RTK_FROZEN_RUNTIME_ARGUMENTS"
cp "$2" "$RTK_FROZEN_RUNTIME_CAPTURE"
printf '{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"policy"},"data":{"state":"observed"}}'
`)
			directory := filepath.Dir(calls)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_FROZEN_RUNTIME_ARGUMENTS", filepath.Join(directory, "arguments"))
			t.Setenv("RTK_FROZEN_RUNTIME_CAPTURE", filepath.Join(directory, "capture"))
			for index, args := range [][]string{
				{"--kubeconfig", store.KubeconfigPath(), "--context", "selected", "-n", "video-cloud-" + environment + "-platform", "get", "configmap", "policy", "-o", "json"},
				{"--kubeconfig=" + store.KubeconfigPath(), "--context=selected", "-n", "video-cloud-" + environment + "-platform", "exec", "original", "--", "psql", "-At"},
			} {
				if _, err := runtime.kubectl(false, args...); err != nil {
					t.Fatal("frozen native read was rejected", err)
				}
				captured, err := os.ReadFile(filepath.Join(directory, "capture"))
				if err != nil || !bytes.Equal(captured, backend.KubeconfigContent) || strings.Contains(string(captured), "foreign-canonical") {
					t.Fatal("native read used replaced canonical authentication", index, err)
				}
			}
			arguments, _ := os.ReadFile(filepath.Join(directory, "arguments"))
			if strings.Contains(string(arguments), store.KubeconfigPath()) || strings.Count(string(arguments), "--kubeconfig") != 2 || !strings.Contains(string(arguments), "PGOPTIONS=-c statement_timeout=10000") {
				t.Fatal("frozen command adapter retained canonical identity flags or dropped SQL deadline")
			}
			before := deploymentRuntimeCallCount(t, calls)
			for _, args := range [][]string{
				{"--context", "foreign-private-context-canary", "get", "configmap", "policy", "-o", "json"},
				{"--context=foreign-private-context-canary", "get", "configmap", "policy", "-o", "json"},
				{"--kubeconfig"},
				{"--server=https://foreign-private-context-canary", "get", "configmap", "policy"},
				{"--token", "foreign-private-context-canary", "get", "configmap", "policy"},
				{"-s", "https://foreign-private-context-canary", "get", "configmap", "policy"},
				{"-s=https://foreign-private-context-canary", "get", "configmap", "policy"},
			} {
				if _, err := runtime.kubectl(false, args...); err == nil || strings.Contains(err.Error(), "private-context-canary") {
					t.Fatal("foreign context or incomplete identity override reached the native command", err)
				}
			}
			if deploymentRuntimeCallCount(t, calls) != before {
				t.Fatal("foreign context bypassed the frozen command fence through a cached read")
			}
		})
	}
}

func TestFrozenDeploymentCommandNormalizationKeepsRemoteArguments(t *testing.T) {
	runtime := &deploymentCheckRuntime{frozenBackend: &environmentKubernetesBackend{}, frozenContext: "selected"}
	args, err := runtime.frozenArguments([]string{"--kubeconfig", "canonical", "exec", "original", "--", "app", "--context=remote-argument", "--kubeconfig", "remote-argument"})
	if err != nil || strings.Join(args, " ") != "exec original -- app --context=remote-argument --kubeconfig remote-argument" {
		t.Fatal("Kubernetes normalization changed remote executable arguments", err, args)
	}
}

func TestQualifiedPublicProbeReceivesFrozenPrivateKubeconfigAndCleansIt(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			kube := frozenKubernetesFixture(t)
			store := makeIsolatedTestSecretStore(t, environment)
			raw, err := yaml.Marshal(kube)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
			if err != nil {
				t.Fatal(err)
			}
			store.checkRuntime = newFrozenDeploymentCheckRuntime(context.Background(), environment, backend)
			if err := store.write("kube/kubeconfig.yaml", []byte("foreign-public-probe-authentication-canary"), true); err != nil {
				t.Fatal(err)
			}
			cfg := deploymentConfig{Environment: environment, Workspace: t.TempDir(), Values: map[string]string{"CLOUD_DNS_ROOT_DOMAIN": "example.test"}}
			list := deploymentRequiredIngressFixture(t)
			list.Items[0].Metadata.Namespace = "video-cloud-" + environment + "-video-cloud"
			list.Items[0].Spec.Rules[0].Host = "certissuer.video-cloud-" + environment + ".example.test"
			inventory, _ := json.Marshal(list)
			calls := deploymentRuntimeTestKubectl(t, `case " $* " in
 *" get deployments "*) printf '%s' '{"items":[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"certissuer","namespace":"video-cloud-`+environment+`-video-cloud"},"spec":{"template":{"spec":{"containers":[{"name":"certissuer","env":[{"name":"CERT_ISSUER_LISTEN_ADDR","value":":9443"}],"ports":[{"containerPort":9443,"name":"https"}]}]}}}}]}';;
 *" get service "*) printf '%s' '{"apiVersion":"v1","kind":"Service","metadata":{"name":"certissuer","namespace":"video-cloud-`+environment+`-video-cloud"},"spec":{"ports":[{"port":9443,"targetPort":"https","protocol":"TCP"}]}}';;
 *) cat "$RTK_FROZEN_PUBLIC_INVENTORY" ;;
esac`)
			directory := filepath.Dir(calls)
			inventoryPath := filepath.Join(directory, "inventory.json")
			writeTestFile(t, inventoryPath, string(inventory))
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_FROZEN_PUBLIC_INVENTORY", inventoryPath)
			t.Setenv("RTK_FROZEN_PUBLIC_CAPTURE", filepath.Join(directory, "capture"))
			t.Setenv("RTK_FROZEN_PUBLIC_PATH", filepath.Join(directory, "path"))
			writeTestFile(t, filepath.Join(cfg.Workspace, "scripts", "check-certissuer-app-mtls.sh"), `#!/bin/sh
cp "$RTK_CLOUD_KUBECONFIG" "$RTK_FROZEN_PUBLIC_CAPTURE"
printf '%s' "$RTK_CLOUD_KUBECONFIG" > "$RTK_FROZEN_PUBLIC_PATH"
printf '%s\n' 'PASS: public CertIssuer mTLS validation and anonymous TLS denial verified'
`)
			if err := verifyDeploymentPublicCertIssuer(context.Background(), cfg, store); err != nil {
				t.Fatal("qualified public probe failed", err)
			}
			captured, err := os.ReadFile(filepath.Join(directory, "capture"))
			if err != nil || !bytes.Equal(captured, backend.KubeconfigContent) {
				t.Fatal("public probe consumed the replaced canonical identity", err)
			}
			pathRaw, _ := os.ReadFile(filepath.Join(directory, "path"))
			path := string(pathRaw)
			if path == store.KubeconfigPath() || !strings.HasPrefix(path, filepath.Join(store.Root, "deployment", "tmp")+string(filepath.Separator)) {
				t.Fatal("public probe identity escaped the selected private temporary store")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("public probe retained a temporary identity", err)
			}
		})
	}
}

func TestFrozenPKIChecksCannotUseMissingCanonicalFileAsPassingEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, check := range []string{"bindings", "runtime"} {
			t.Run(environment+"/"+check, func(t *testing.T) {
				kube := frozenKubernetesFixture(t)
				store := makeIsolatedTestSecretStore(t, environment)
				raw, _ := yaml.Marshal(kube)
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				store.checkRuntime = newFrozenDeploymentCheckRuntime(context.Background(), environment, backend)
				calls := deploymentRuntimeTestKubectl(t, "printf 'Forbidden private-missing-kubeconfig-canary' >&2; exit 1")
				t.Setenv("PATH", filepath.Dir(calls)+string(os.PathListSeparator)+os.Getenv("PATH"))
				if _, err := os.Lstat(store.KubeconfigPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("test unexpectedly supplied a canonical kubeconfig", err)
				}
				if check == "bindings" {
					err = verifySecretStoreK8SBindings(store)
				} else {
					err = verifySecretStoreK8SRuntime(store, time.Now().UTC())
				}
				if err == nil || strings.Contains(err.Error(), "private-missing-kubeconfig-canary") || deploymentRuntimeCallCount(t, calls) != 1 {
					t.Fatal("missing canonical file skipped actual frozen native PKI evidence or leaked private output", err)
				}
			})
		}
	}
}
