package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCreateKubeAccessExecutionContractPublishesOnlyExactOriginalCluster(t *testing.T) {
	for _, mode := range []string{"original", "different-native-id", "different-label", "different-region", "invalid-version", "changed-existing-kubeconfig", "changed-provider-state", "unready-deadline", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			cluster := environmentCreateProviderReceipt{Key: "lke", Kind: "lke", NativeID: "901", Name: "video-cloud-dev-lke", Region: "us-sea"}
			kube := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "original", "contexts": []any{map[string]any{"name": "original", "context": map[string]any{"cluster": "original", "user": "operator"}}}, "clusters": []any{map[string]any{"name": "original", "cluster": map[string]any{"server": "https://original.invalid:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}}, "users": []any{map[string]any{"name": "operator", "user": map[string]any{"token": "isolated-original-cluster-token"}}}}
			raw, err := yaml.Marshal(kube)
			if err != nil {
				t.Fatal(err)
			}
			metadata := map[string]any{"id": 901, "label": cluster.Name, "region": cluster.Region, "k8s_version": "1.34"}
			switch mode {
			case "different-native-id":
				metadata["id"] = 902
			case "different-label":
				metadata["label"] = "foreign-lke"
			case "different-region":
				metadata["region"] = "us-lax"
			case "invalid-version":
				metadata["k8s_version"] = "changing-latest"
			case "changed-existing-kubeconfig":
				if err := store.write("kube/kubeconfig.yaml", []byte("retained-original-private-kubeconfig"), false); err != nil {
					t.Fatal(err)
				}
			case "changed-provider-state":
				if err := writeLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"], map[string]string{"LKE_CLUSTER_ID": "902", "LKE_CLUSTER_LABEL": cluster.Name, "LKE_CLUSTER_REGION": cluster.Region, "LKE_CLUSTER_VERSION": "1.34"}); err != nil {
					t.Fatal(err)
				}
			}
			requests := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer isolated-provider-token" {
					t.Error("access publication mutated provider or changed its selected credential")
					w.WriteHeader(403)
					return
				}
				requests = append(requests, r.URL.Path)
				switch r.URL.Path {
				case "/lke/clusters/901/kubeconfig":
					_ = json.NewEncoder(w).Encode(map[string]string{"kubeconfig": base64.StdEncoding.EncodeToString(raw)})
				case "/lke/clusters/901":
					_ = json.NewEncoder(w).Encode(metadata)
				default:
					t.Error("unselected native cluster read", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			script := "#!/bin/sh\ncase \"$*\" in *'get --raw=/readyz'*) printf '%s\\n' \"$*\" >> \"$RTK_KUBE_ACCESS_CALLS\"; [ \"$RTK_KUBE_ACCESS_MODE\" != unready-deadline ] || exit 1; printf 'ok\\n';; *) exit 2;; esac\n"
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_KUBE_ACCESS_CALLS", log)
			t.Setenv("RTK_KUBE_ACCESS_MODE", mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "unready-deadline" {
				var bounded context.CancelFunc
				ctx, bounded = context.WithTimeout(ctx, 200*time.Millisecond)
				defer bounded()
			}
			if mode == "cancelled" {
				cancel()
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err = installEnvironmentCreateKubeconfig(ctx, cfg, store, cluster, map[string]string{"LINODE_TOKEN": "isolated-provider-token"}, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected original cluster access publication", err)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "provider-kube-access")
			if entry == nil {
				t.Fatal("access operation lacks original pending intent")
			}
			if mode != "original" {
				if entry.State != "pending" || len(entry.Receipt) != 0 {
					t.Fatal("unverified cluster became a completed allocation")
				}
				for path, value := range before {
					if strings.Contains(path, "/journal.json") || path == "deployment" {
						continue
					}
					if createBootstrapStateSnapshot(t, store.Root)[path] != value {
						t.Fatal("failed publication replaced original private input", path)
					}
				}
				if mode == "cancelled" && len(requests) != 0 {
					t.Fatal("cancelled publication contacted provider")
				}
				return
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				t.Fatal("ready allocated cluster lacks completion")
			}
			var proof map[string]string
			if json.Unmarshal(entry.Receipt, &proof) != nil || proof["cluster_id"] != cluster.NativeID || proof["kubeconfig_sha256"] != environmentPreflightBytesSHA256(raw) || !environmentPreflightDigest.MatchString(proof["provider_state_sha256"]) {
				t.Fatal("access receipt lost exact native identity")
			}
			stored, err := os.ReadFile(filepath.Join(store.Root, "kube/kubeconfig.yaml"))
			if err != nil || !bytes.Equal(stored, raw) {
				t.Fatal("published Kubernetes identity differs", err)
			}
			state, err := readLKEProviderState("dev", "video-cloud-dev")
			if err != nil || state["LKE_CLUSTER_ID"] != "901" || state["LKE_CLUSTER_VERSION"] != "1.34" {
				t.Fatal("allocated provider state differs", err)
			}
			if !reflect.DeepEqual(requests, []string{"/lke/clusters/901/kubeconfig", "/lke/clusters/901"}) {
				t.Fatal("access used unrecorded provider lookups", requests)
			}
			calls, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(calls), "--raw=/readyz") || strings.Contains(string(calls), "isolated-original-cluster-token") {
				t.Fatal("readiness did not use private frozen Kubernetes access", err)
			}
		})
	}
}
