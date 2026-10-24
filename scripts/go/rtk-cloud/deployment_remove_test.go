package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func deploymentRemovalFixture(t *testing.T, environment string, kube bool) (deploymentConfig, secretStore) {
	t.Helper()
	workspace := writeDeploymentFixture(t, environment, "lke")
	store := makeIsolatedTestSecretStore(t, environment)
	cfg, err := resolveDeploymentConfig(workspace, environment, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	owned, _ := json.Marshal(dnsOwnership{Adapter: cfg.DNSAdapter, RootDomain: cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], Environment: environment, Records: []dnsRecordSet{}})
	if err := store.write("deployment/dns/"+cfg.DNSAdapter+"/ownership.json", owned, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINODE_TOKEN", "isolated-removal-token")
	if kube {
		raw, _ := json.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "Config", "current-context": "selected",
			"contexts": []any{map[string]any{"name": "selected", "context": map[string]any{"cluster": "selected", "user": "operator"}}},
			"clusters": []any{map[string]any{"name": "selected", "cluster": map[string]any{"server": "https://isolated.example:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}},
			"users":    []any{map[string]any{"name": "operator", "user": map[string]any{"token": "isolated-kubernetes-token"}}},
		})
		if err := store.write("kube/kubeconfig.yaml", raw, true); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, store
}

func fakeDeploymentRemovalKubectl(t *testing.T, pvJSON, failure string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "kubectl.log")
	pv := filepath.Join(dir, "pv.json")
	writeTestFile(t, pv, pvJSON)
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s\n' "${KUBECONFIG:-}" "$*" >> ` + shellSingleQuote(log) + `
kube="${KUBECONFIG:-}"
previous=""
for arg in "$@"; do
  if [[ "$previous" == --kubeconfig ]]; then kube="$arg"; fi
  previous="$arg"
done
if [[ -n "${RTK_REMOVE_REPLACEMENT_SOURCE:-}" && "$*" == *"${RTK_REMOVE_REPLACEMENT_PHASE}"* ]]; then
  cp "$RTK_REMOVE_REPLACEMENT_SOURCE" "$RTK_REMOVE_REPLACEMENT_TARGET"
fi
cp "$kube" ` + shellSingleQuote(log+".kube") + `
if [[ "$*" == *"get persistentvolume"* ]]; then
  if [[ ` + shellSingleQuote(failure) + ` == capture ]]; then
    printf 'private-authentication-canary\n' >&2
    exit 1
  fi
  cat ` + shellSingleQuote(pv) + `
elif [[ "$*" == *delete* && ` + shellSingleQuote(failure) + ` == purge ]]; then
  exit 1
else
  printf '{"items":[]}'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func emptyRemovalProvider(t *testing.T) string {
	t.Helper()
	return fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[]}`,
		"/linode/instances?page_size=500":       `{"data":[]}`,
		"/networking/firewalls?page_size=500":   `{"data":[]}`,
		"/vpcs?page_size=500":                   `{"data":[]}`,
		"/object-storage/buckets?page_size=500": `{"data":[]}`,
		"/volumes?page_size=500":                `{"data":[]}`,
	})
}

func assertRemovalProviderNotCalled(t *testing.T, log string) {
	t.Helper()
	if body, err := os.ReadFile(log); err == nil && len(body) != 0 {
		t.Fatalf("provider was called before prerequisite cleanup succeeded: %s", body)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDeploymentRemovalUsesCanonicalEnvironmentKubeconfig(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store := deploymentRemovalFixture(t, environment, true)
			// Resolver Values omit CLOUD_ENV_NAME. DNS cleanup must bind the
			// selected environment explicitly, even with a foreign overlay.
			delete(cfg.Values, "CLOUD_ENV_NAME")
			cfg.DNSValues["CLOUD_ENV_NAME"] = "foreign-environment"
			kubeLog := fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "")
			providerLog := emptyRemovalProvider(t)
			foreign := filepath.Join(t.TempDir(), "foreign-kubeconfig")
			writeTestFile(t, foreign, "must-not-be-read")
			writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml"), "must-not-be-read")
			for _, key := range []string{"KUBECONFIG", "RTK_CLOUD_KUBECONFIG", "RTK_CLOUD_LKE_KUBECONFIG", "CLOUD_STAGING_K8S_KUBECONFIG"} {
				t.Setenv(key, foreign)
			}
			if err := cleanupDeploymentEnvironment(cfg); err != nil {
				t.Fatal(err)
			}
			calls := readTestFile(t, kubeLog)
			frozenPrefix := filepath.Join(store.Root, "deployment", "tmp", "kubernetes-api-")
			if !strings.Contains(calls, "--kubeconfig "+frozenPrefix) || !strings.Contains(calls, "|-n video-cloud-"+environment+"-platform delete pvc") {
				t.Fatalf("canonical capture/purge missing: %s", calls)
			}
			for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
				if !strings.Contains(line, "--kubeconfig "+frozenPrefix) && !strings.HasPrefix(line, frozenPrefix) {
					t.Fatalf("purge used a foreign kubeconfig: %s", line)
				}
			}
			backend, _, err := deploymentRemovalKubeconfig(cfg)
			if err != nil || readTestFile(t, kubeLog+".kube") != string(backend.KubeconfigContent) {
				t.Fatalf("frozen API identity differs from selected canonical identity: %v", err)
			}
			if directories, _ := filepath.Glob(frozenPrefix + "*"); len(directories) != 0 {
				t.Fatalf("private API identity copies were not cleaned: %v", directories)
			}
			if !strings.Contains(readTestFile(t, providerLog), "GET /lke/clusters") {
				t.Fatal("provider cleanup was not reached")
			}
			proof, known, err := readDeploymentRemovalVolumes(cfg)
			if err != nil || !known || len(proof.VolumeIDs) != 0 {
				t.Fatalf("empty original volume inventory was not recorded: %v %v %v", proof.VolumeIDs, known, err)
			}
		})
	}
}

func TestDeploymentRemovalStopsBeforeProviderWhenPrerequisitesFail(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, failure := range []string{"capture", "ownership", "missing-dns-ownership", "dns", "purge"} {
			t.Run(environment+"/"+failure, func(t *testing.T) {
				cfg, store := deploymentRemovalFixture(t, environment, true)
				fakeDeploymentRemovalKubectl(t, `{"items":[]}`, failure)
				providerLog := emptyRemovalProvider(t)
				if failure == "missing-dns-ownership" {
					if err := os.Remove(filepath.Join(store.Root, "deployment", "dns", cfg.DNSAdapter, "ownership.json")); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "ownership" || failure == "dns" {
					owned := dnsOwnership{Adapter: cfg.DNSAdapter, RootDomain: cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], Environment: environment, Records: []dnsRecordSet{}}
					if failure == "ownership" {
						owned.Environment = "foreign-environment"
					} else {
						owned.Records = []dnsRecordSet{{Name: "owned.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}}
						for _, key := range []string{"GODADDY_KEY", "GODADDY_SECRET"} {
							t.Setenv(key, "")
						}
					}
					raw, _ := json.Marshal(owned)
					if err := store.write("deployment/dns/"+cfg.DNSAdapter+"/ownership.json", raw, true); err != nil {
						t.Fatal(err)
					}
				}
				err := cleanupDeploymentEnvironment(cfg)
				if err == nil || strings.Contains(err.Error(), "private-authentication-canary") {
					t.Fatalf("prerequisite error was lost or leaked private output: %v", err)
				}
				assertRemovalProviderNotCalled(t, providerLog)
			})
		}
	}
}

func TestDeploymentRemovalCapturesAndRetainsOriginalVolumeIDs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store := deploymentRemovalFixture(t, environment, true)
			body := fmt.Sprintf(`{"items":[{"spec":{"claimRef":{"namespace":"video-cloud-%s-platform"},"csi":{"driver":"linodebs.csi.linode.com","volumeHandle":"102-pvc-original"}}},{"spec":{"claimRef":{"namespace":"video-cloud-foreign-platform"},"csi":{"volumeHandle":"999"}}}]}`, environment)
			fakeDeploymentRemovalKubectl(t, body, "")
			ids, err := deploymentPersistentVolumeIDs(cfg)
			if err != nil || !reflect.DeepEqual(ids, []string{"102"}) {
				t.Fatalf("original volume IDs = %v, %v", ids, err)
			}
			path := filepath.Join(store.Root, "deployment", "remove-volume-ownership.json")
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("volume ownership file permissions: %v %v", info, err)
			}
			// A partial retry sees no remaining PVC, but may still need to
			// delete a volume captured before the namespace disappeared.
			fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "")
			ids, err = deploymentPersistentVolumeIDs(cfg)
			if err != nil || !reflect.DeepEqual(ids, []string{"102"}) {
				t.Fatalf("retry erased original volume ownership: %v %v", ids, err)
			}
			if err := os.Remove(store.KubeconfigPath()); err != nil {
				t.Fatal(err)
			}
			emptyRemovalProvider(t)
			ids, err = deploymentPersistentVolumeIDs(cfg)
			if err != nil || !reflect.DeepEqual(ids, []string{"102"}) {
				t.Fatalf("absent-cluster retry did not consume original volume ownership: %v %v", ids, err)
			}
		})
	}
}

func TestDeploymentRemovalMissingKubeconfigRequiresLiveClusterAbsence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, _ := deploymentRemovalFixture(t, environment, false)
			writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml"), "legacy-must-not-be-used")
			log := fakeLinodeCurl(t, map[string]string{
				"/lke/clusters?page_size=500": fmt.Sprintf(`{"data":[{"id":123,"label":"video-cloud-%s-lke"}]}`, environment),
			})
			err := cleanupDeploymentEnvironment(cfg)
			if err == nil || !strings.Contains(err.Error(), "canonical kubeconfig is missing") {
				t.Fatalf("missing canonical kubeconfig did not block live cluster removal: %v", err)
			}
			if calls := readTestFile(t, log); strings.Contains(calls, "DELETE") || strings.Count(calls, "GET") != 1 {
				t.Fatalf("unexpected provider mutation: %s", calls)
			}
		})
	}
}

func TestDeploymentRemovalPartialRetryDoesNotInventVolumeEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store := deploymentRemovalFixture(t, environment, false)
			log := emptyRemovalProvider(t)
			err := cleanupDeploymentEnvironment(cfg)
			if err == nil || !strings.Contains(err.Error(), "persistent volume cleanup cannot be verified") {
				t.Fatalf("absent cluster without volume ownership was called complete: %v", err)
			}
			if !strings.Contains(readTestFile(t, log), "GET /linode/instances") {
				t.Fatal("partial retry did not inspect remaining provider resources")
			}
			if _, err := os.Stat(filepath.Join(store.Root, "deployment", "remove-volume-ownership.json")); !os.IsNotExist(err) {
				t.Fatalf("absent-cluster retry invented volume evidence: %v", err)
			}
		})
	}
}

func TestDeploymentRemovalRejectsUnsupportedOrForeignVolumeEvidence(t *testing.T) {
	for _, handle := range []string{"", "0", "-123", "not-a-volume"} {
		body := fmt.Sprintf(`{"items":[{"spec":{"claimRef":{"namespace":"video-cloud-dev-platform"},"csi":{"volumeHandle":%q}}}]}`, handle)
		if _, err := persistentVolumeIDsForStack([]byte(body), "video-cloud-dev"); err == nil {
			t.Fatalf("unsupported selected-stack volume handle %q was ignored", handle)
		}
	}
	cfg, store := deploymentRemovalFixture(t, "dev", true)
	raw, _ := json.Marshal(deploymentRemovalVolumes{Schema: 1, Environment: "staging", Stack: "video-cloud-staging", VolumeIDs: []string{"999"}})
	if err := store.write("deployment/remove-volume-ownership.json", raw, true); err != nil {
		t.Fatal(err)
	}
	providerLog := emptyRemovalProvider(t)
	if err := cleanupDeploymentEnvironment(cfg); err == nil || !strings.Contains(err.Error(), "selected environment and stack") {
		t.Fatalf("foreign volume ownership was accepted: %v", err)
	}
	assertRemovalProviderNotCalled(t, providerLog)
}

func TestDeploymentRemovalRejectsAnotherClusterGeneration(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, changed := range []string{"cluster", "kubeconfig"} {
			t.Run(environment+"/"+changed, func(t *testing.T) {
				cfg, store := deploymentRemovalFixture(t, environment, true)
				writeTestLKEState(t, environment, "LKE_CLUSTER_ID=123\n")
				fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "")
				if _, err := deploymentPersistentVolumeIDs(cfg); err != nil {
					t.Fatal(err)
				}
				proofPath := filepath.Join(store.Root, "deployment", "remove-volume-ownership.json")
				original := readTestFile(t, proofPath)
				if changed == "cluster" {
					writeTestLKEState(t, environment, "LKE_CLUSTER_ID=456\n")
				} else {
					raw := readTestFile(t, store.KubeconfigPath())
					if err := store.write("kube/kubeconfig.yaml", []byte(strings.ReplaceAll(raw, "isolated-kubernetes-token", "another-identity-token")), true); err != nil {
						t.Fatal(err)
					}
				}
				providerLog := emptyRemovalProvider(t)
				if err := cleanupDeploymentEnvironment(cfg); err == nil || !strings.Contains(err.Error(), "generation") {
					t.Fatalf("another %s generation consumed old ownership: %v", changed, err)
				}
				if err := purgeDeploymentK8s(cfg); err == nil || !strings.Contains(err.Error(), "generation") {
					t.Fatalf("another %s generation was purged after capture: %v", changed, err)
				}
				assertRemovalProviderNotCalled(t, providerLog)
				if readTestFile(t, proofPath) != original {
					t.Fatal("another cluster generation overwrote original removal ownership")
				}
			})
		}
	}
}

func TestDeploymentRemovalCLIBlocksCaptureFailure(t *testing.T) {
	cfg, _ := deploymentRemovalFixture(t, "dev", true)
	fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "capture")
	providerLog := emptyRemovalProvider(t)
	err := runDeployment([]string{"remove", "--workspace", cfg.Workspace, "--environment", cfg.Environment, "--confirm", cfg.Values["CLOUD_STACK_NAME"]})
	if err == nil || !strings.Contains(err.Error(), "persistent volume ownership capture") {
		t.Fatalf("deployment remove did not propagate capture failure: %v", err)
	}
	assertRemovalProviderNotCalled(t, providerLog)
}

func TestDeploymentRemovalRejectsUnsafeCanonicalKubeconfig(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store := deploymentRemovalFixture(t, environment, true)
			if err := os.Chmod(store.KubeconfigPath(), 0644); err != nil {
				t.Fatal(err)
			}
			providerLog := emptyRemovalProvider(t)
			if err := cleanupDeploymentEnvironment(cfg); err == nil || !strings.Contains(err.Error(), "0600") {
				t.Fatalf("unsafe canonical kubeconfig was accepted: %v", err)
			}
			assertRemovalProviderNotCalled(t, providerLog)
		})
	}
}

func TestDeploymentRemovalFreezesIdentityWhenCanonicalChangesDuringQuery(t *testing.T) {
	for _, phase := range []string{"get persistentvolume", "delete"} {
		t.Run(phase, func(t *testing.T) {
			cfg, store := deploymentRemovalFixture(t, "dev", true)
			original, _, err := deploymentRemovalKubeconfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(t.TempDir(), "replacement-kubeconfig")
			writeTestFile(t, replacement, strings.ReplaceAll(readTestFile(t, store.KubeconfigPath()), "isolated-kubernetes-token", "replacement-identity-token"))
			t.Setenv("RTK_REMOVE_REPLACEMENT_SOURCE", replacement)
			t.Setenv("RTK_REMOVE_REPLACEMENT_TARGET", store.KubeconfigPath())
			t.Setenv("RTK_REMOVE_REPLACEMENT_PHASE", phase)
			kubeLog := fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "")
			providerLog := emptyRemovalProvider(t)
			if err := cleanupDeploymentEnvironment(cfg); err == nil || !strings.Contains(err.Error(), "generation") {
				t.Fatalf("concurrent identity replacement reached provider removal: %v", err)
			}
			if readTestFile(t, kubeLog+".kube") != string(original.KubeconfigContent) {
				t.Fatal("kubectl followed mutable canonical path instead of frozen original identity")
			}
			assertRemovalProviderNotCalled(t, providerLog)
		})
	}
}
