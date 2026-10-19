package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createProviderRuntimeFixture(t *testing.T) (deploymentConfig, secretStore, environmentCreateProviderPlan, environmentCreateProviderResource, map[string]string) {
	t.Helper()
	cfg, operator := createProviderTestFixture(t, "dev")
	createProviderTestAPI(t, nil)
	plan, err := buildEnvironmentCreateProviderPlan(context.Background(), cfg, operator)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	block, err := ssh.MarshalPrivateKey(key, "selected-operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(block), true); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519.pub", []byte(operator["DEPLOYMENT_SSH_PUBLIC_KEY"]+"\n"), true); err != nil {
		t.Fatal(err)
	}
	return cfg, store, plan, plan.Resources[1], operator
}

func TestFreshCreateProviderMaterialPersistsExactHostIdentityOnce(t *testing.T) {
	cfg, store, _, resource, _ := createProviderRuntimeFixture(t)
	sha := strings.Repeat("a", 64)
	first, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(second) {
		t.Fatalf("host identity changed on preparation repeat: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(first["user_data"])
	if err != nil || validateEnvironmentCreateHostIdentityMaterial(raw, first["host_public_key"]) != nil {
		t.Fatal("invalid constrained host key payload")
	}
	if first["root_pass"] != "" || first["host_key_sha256"] != environmentPreflightBytesSHA256([]byte(first["host_public_key"])) {
		t.Fatal("unneeded password or absent host proof")
	}
	path := filepath.Join(store.Root, "deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key")
	for _, name := range []string{path, path + ".pub"} {
		info, err := os.Lstat(name)
		if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
			t.Fatalf("unsafe host identity state: %s %v", name, err)
		}
	}
	if err := os.Remove(path + ".pub"); err != nil {
		t.Fatal(err)
	}
	derived, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(derived) {
		t.Fatal("deriving missing public half replaced private host identity", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store); err == nil {
		t.Fatal("unsafe original host private key was regenerated or accepted")
	}
}

func TestFreshCreatePinnedSSHBindsCanonicalPairNativeIPAndOriginalHostKey(t *testing.T) {
	for _, mode := range []string{"valid", "missing-host", "changed-host", "changed-operator", "unsafe-key", "private-ip", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, plan, resource, _ := createProviderRuntimeFixture(t)
			sha := strings.Repeat("a", 64)
			if _, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			captured := filepath.Join(directory, "arguments")
			binary := filepath.Join(directory, "ssh")
			writeTestFile(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$RTK_CREATE_SSH_ARGUMENTS\"\ncat >/dev/null\nprintf 'verified\\n'\n")
			if err := os.Chmod(binary, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CREATE_SSH_ARGUMENTS", captured)
			t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.99")
			receipt := environmentCreateProviderReceipt{Key: resource.Key, Kind: "instance", NativeID: "123", Name: resource.Name, Region: resource.Region, PublicIP: "198.51.100.12"}
			host := filepath.Join(store.Root, "deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key")
			switch mode {
			case "missing-host":
				os.Remove(host)
			case "changed-host":
				store.write(filepath.Join("deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key.pub"), []byte("ssh-ed25519 changed"), true)
			case "changed-operator":
				store.write("operator/ssh/id_ed25519.pub", []byte("ssh-ed25519 changed"), true)
			case "unsafe-key":
				os.Chmod(filepath.Join(store.Root, "operator", "ssh", "id_ed25519"), 0644)
			case "private-ip":
				receipt.PublicIP = "192.168.1.12"
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			output, err := environmentCreatePinnedSSH(ctx, store, plan, sha, resource, receipt, "echo private-runtime-credential\n")
			if mode == "valid" {
				if err != nil || string(output) != "verified\n" {
					t.Fatal("pinned SSH failed", err)
				}
				args := readTestFile(t, captured)
				if !strings.Contains(args, "StrictHostKeyChecking=yes") || !strings.Contains(args, "root@198.51.100.12") || !strings.Contains(args, filepath.Join(store.Root, "operator", "ssh", "id_ed25519")) || strings.Contains(args, "198.51.100.99") || strings.Contains(args, "private-runtime-credential") {
					t.Fatal("SSH target/key/secret boundary incorrect", args)
				}
				known := readTestFile(t, filepath.Join(filepath.Dir(host), "known_hosts"))
				if !strings.HasPrefix(known, "198.51.100.12 ssh-ed25519 ") {
					t.Fatal("host key pin does not bind original native IP")
				}
			} else {
				if err == nil {
					t.Fatal("SSH accepted invalid original target identity")
				}
				if _, err := os.Lstat(captured); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid target reached SSH", err)
				}
				if mode == "missing-host" {
					if _, err := os.Lstat(host); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("missing allocated host identity was regenerated")
					}
				}
			}
		})
	}
}

func TestFreshCreateCloudInitCannotCarryArbitraryHostCode(t *testing.T) {
	cfg, store, _, resource, _ := createProviderRuntimeFixture(t)
	material, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, strings.Repeat("a", 64), resource, store)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(material["user_data"])
	for _, malformed := range [][]byte{append(append([]byte{}, raw...), []byte("runcmd:\n  - echo unsupported\n")...), []byte("#cloud-config\nssh_keys: {}\n"), []byte("echo not-cloud-init")} {
		if err := validateEnvironmentCreateHostIdentityMaterial(malformed, material["host_public_key"]); err == nil {
			t.Fatal("unsupported private bootstrap payload accepted")
		}
	}
}

func TestCreateDataPlaneUpstreamsHonorSelectedMQTTPlacement(t *testing.T) {
	t.Setenv("MQTT_NODE_CLASS", "unrelated-ambient-class")
	for _, selected := range []string{"general", "broker"} {
		t.Run(selected, func(t *testing.T) {
			values := map[string]string{"MQTT_NODE_CLASS": selected, "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}
			raw := []byte(`{"items":[{"metadata":{"name":"broker-node","labels":{"rtk.io/node-class":"broker"}},"status":{"addresses":[{"type":"ExternalIP","address":"198.51.100.2"},{"type":"InternalIP","address":"192.168.1.2"}]}},{"metadata":{"name":"general-node","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.1"}]}}]}`)
			upstreams, err := environmentCreateDataPlaneUpstreams(values, raw)
			if err != nil || len(upstreams) != 3 {
				t.Fatal("selected placement did not retain HTTPS and MQTT routes", err)
			}
			mqtt := upstreams[2]
			if mqtt.Kind != "mqtts" || mqtt.Name != selected+"-node" || mqtt.Port != 31883 || mqtt.Address != map[string]string{"general": "192.168.1.1", "broker": "192.168.1.2"}[selected] {
				t.Fatal("MQTT route differs from selected original node class")
			}
			config := renderLKEEdgeHAProxyConfig(lkeEdgeHAProxyPlan{Upstreams: upstreams})
			if !strings.Contains(config, "server "+selected+"-node "+mqtt.Address+":31883 check") {
				t.Fatal("actual edge target has no selected MQTT backend")
			}
		})
	}
	// This original dev generation has one general node and no broker pool.
	upstreams, err := environmentCreateDataPlaneUpstreams(map[string]string{"MQTT_NODE_CLASS": "general", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}, []byte(`{"items":[{"metadata":{"name":"original-dev-general","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.153.10"}]}}]}`))
	if err != nil || len(upstreams) != 2 || upstreams[1].Kind != "mqtts" {
		t.Fatal("original single-general-node dev cannot serve managed MQTT", err)
	}
}

func TestCreateDataPlaneRejectsEmptyOrUnqualifiedMQTTBackend(t *testing.T) {
	for _, mode := range []string{"no-matching-node", "missing-class", "missing-label-key", "public-upstream", "empty-inventory", "invalid-inventory"} {
		t.Run(mode, func(t *testing.T) {
			values := map[string]string{"MQTT_NODE_CLASS": "broker", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}
			raw := []byte(`{"items":[{"metadata":{"name":"general-node","labels":{"rtk.io/node-class":"general"}},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.1"}]}}]}`)
			switch mode {
			case "missing-class":
				delete(values, "MQTT_NODE_CLASS")
			case "missing-label-key":
				delete(values, "NODE_CLASS_LABEL_KEY")
			case "public-upstream":
				values["MQTT_NODE_CLASS"] = "general"
				raw = []byte(strings.ReplaceAll(string(raw), "192.168.1.1", "198.51.100.1"))
			case "empty-inventory":
				raw = []byte(`{"items":[]}`)
			case "invalid-inventory":
				raw = []byte(`not-json`)
			}
			if upstreams, err := environmentCreateDataPlaneUpstreams(values, raw); err == nil || len(upstreams) != 0 {
				t.Fatal("empty/unqualified MQTT target reached edge installation")
			}
		})
	}
}

func TestCreateDataPlaneNetworkQualificationUsesReadOnlySelectedNodes(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("kube/kubeconfig.yaml", []byte("isolated-original-kubeconfig"), true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "kubectl")
	arguments := filepath.Join(dir, "arguments")
	writeTestFile(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$RTK_CREATE_NODE_ARGUMENTS\"\nprintf '%s\\n' '{\"items\":[{\"metadata\":{\"name\":\"original-dev-general\",\"labels\":{\"rtk.io/node-class\":\"general\"}},\"status\":{\"addresses\":[{\"type\":\"InternalIP\",\"address\":\"192.168.153.10\"}]}}]}'\n")
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CREATE_NODE_ARGUMENTS", arguments)
	t.Setenv("MQTT_NODE_CLASS", "unrelated-ambient-class")
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "MQTT_NODE_CLASS": "general", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}}
	backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-original-kubeconfig")}
	upstreams, err := environmentCreateDataPlaneNodeUpstreams(context.Background(), cfg, backend)
	if err != nil || len(upstreams) != 2 || upstreams[1].Kind != "mqtts" {
		t.Fatal("network qualification differs from original dev routing", err)
	}
	args := readTestFile(t, arguments)
	if !strings.Contains(args, filepath.Join(store.Root, "deployment", "tmp", "kubernetes-api-")) || !strings.Contains(args, "get\nnodes\n-o\njson\n") || strings.Contains(args, "apply") || strings.Contains(args, "patch") {
		t.Fatal("network gate used another identity or performed a mutation")
	}
	cfg.Values["MQTT_NODE_CLASS"] = "broker"
	if _, err := environmentCreateDataPlaneNodeUpstreams(context.Background(), cfg, backend); err == nil {
		t.Fatal("network gate qualified an empty selected MQTT backend")
	}
}
