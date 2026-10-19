package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoTLSNativeCacheReadAcceptsOnlyOriginalFSGroupDirectoryTuples(t *testing.T) {
	for _, scenario := range []string{"700", "2700", "mixed", "750", "2750", "setuid", "sticky", "world-access", "foreign-directory-user", "foreign-directory-group", "private-symlink", "crl-symlink", "cache-symlink", "cache-group-access", "foreign-cache-user", "missing-cache", "foreign-fsgroup", "foreign-run-group", "foreign-run-user"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, "dev")
			backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-frozen-tls-cache-kubeconfig"), RolloutTimeout: time.Second}
			root, key, _, now := rootCRLFixture(t)
			root.ID, root.Domain = "734487b8-abcf-40be-a813-8b52c64f7954", "openbao_tls"
			prior := signedRootCRLFixture(t, root, key, createRootCRLRequest{root.ID, root.RootFingerprint, "1", now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), nil})
			cache, err := json.Marshal(deploymentplan.Object{"issuer_fingerprint": root.RootFingerprint, "crl": prior})
			if err != nil {
				t.Fatal(err)
			}
			fixture := t.TempDir()
			private := filepath.Join(fixture, "private")
			crl := filepath.Join(private, "crl")
			if err := os.MkdirAll(crl, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(crl, "openbao-root.json")
			if err := os.WriteFile(file, cache, 0600); err != nil {
				t.Fatal(err)
			}
			privateTuple, crlTuple := "700 10001 10001", "700 10001 10001"
			fileMode, fileUser := "600", "10001"
			security := environmentCreateServicePodSecurity()
			switch scenario {
			case "2700":
				privateTuple, crlTuple = "2700 10001 10001", "2700 10001 10001"
			case "mixed":
				crlTuple = "2700 10001 10001"
			case "750":
				crlTuple = "750 10001 10001"
			case "2750":
				crlTuple = "2750 10001 10001"
			case "setuid":
				privateTuple = "4700 10001 10001"
			case "sticky":
				privateTuple = "1700 10001 10001"
			case "world-access":
				crlTuple = "2701 10001 10001"
			case "foreign-directory-user":
				crlTuple = "2700 10002 10001"
			case "foreign-directory-group":
				crlTuple = "2700 10001 10002"
			case "private-symlink", "crl-symlink", "cache-symlink":
				path := private
				if scenario == "crl-symlink" {
					path = crl
				} else if scenario == "cache-symlink" {
					path = file
				}
				original := path + "-original"
				if err := os.Rename(path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			case "cache-group-access":
				fileMode = "640"
			case "foreign-cache-user":
				fileUser = "10002"
			case "missing-cache":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "foreign-fsgroup":
				security["fsGroup"] = float64(10002)
			case "foreign-run-group":
				security["runAsGroup"] = float64(10002)
			case "foreign-run-user":
				security["runAsUser"] = float64(10002)
			}
			uid := "11111111-1111-4111-8111-111111111111"
			desired := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"securityContext": security}}}}
			actual := deploymentplan.Clone(desired)
			deploymentplan.Map(actual["metadata"])["uid"] = uid
			writeTestFile(t, filepath.Join(fixture, "deployment.json"), string(mustProviderRecoveryJSON(actual)))
			writeTestFile(t, filepath.Join(fixture, "claim.json"), string(mustProviderRecoveryJSON(deploymentplan.Object{"kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "certissuer-service-state", "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "uid": "22222222-2222-4222-8222-222222222222"}})))
			writeTestFile(t, filepath.Join(fixture, "pod.json"), string(mustProviderRecoveryJSON(deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "certissuer-original", "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "uid": "33333333-3333-4333-8333-333333333333"}})))
			target := createRootCRLStateTarget{ID: deploymentplan.ID(desired), UID: uid, Desired: desired, Container: "certissuer", ClaimID: "PersistentVolumeClaim/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/certissuer-service-state", ClaimUID: "22222222-2222-4222-8222-222222222222", PodName: "certissuer-original", PodUID: "33333333-3333-4333-8333-333333333333", Paths: []string{"/var/lib/pki-service/private/crl/openbao-root.json", "/var/lib/pki-service/private/crl/openbao-root-original-ica.json"}}
			// Execute the actual production /bin/sh guard. The stat shim supplies
			// Linux numeric ownership/mode tuples that an unprivileged macOS test
			// cannot establish; directory/file/symlink checks use the real fixture.
			writeTestFile(t, filepath.Join(fixture, "stat"), `#!/bin/sh
set -eu
[ "$1" = -c ]
case "$2" in
 '%a %u %g')
  case "$3" in
   "$RTK_TLS_CACHE_PRIVATE") printf '%s\n' "$RTK_TLS_CACHE_PRIVATE_TUPLE" ;;
   "$RTK_TLS_CACHE_PRIVATE/crl") printf '%s\n' "$RTK_TLS_CACHE_CRL_TUPLE" ;;
   *) exit 71 ;;
  esac ;;
 '%a') printf '%s\n' "$RTK_TLS_CACHE_FILE_MODE" ;;
 '%u') printf '%s\n' "$RTK_TLS_CACHE_FILE_USER" ;;
 *) exit 72 ;;
esac
`)
			writeTestFile(t, filepath.Join(fixture, "kubectl"), `#!/bin/sh
set -eu
case "$*" in
 *' get Deployment certissuer '*) cat "$RTK_TLS_CACHE_FIXTURE/deployment.json" ;;
 *' get PersistentVolumeClaim certissuer-service-state '*) cat "$RTK_TLS_CACHE_FIXTURE/claim.json" ;;
 *' get Pod certissuer-original '*) cat "$RTK_TLS_CACHE_FIXTURE/pod.json" ;;
 *' rollout status Deployment/certissuer '*) exit 0 ;;
 *' exec pod/certissuer-original '*)
  while [ "$1" != -- ]; do shift; done
  shift
  [ "$1" = /bin/sh ] && [ "$2" = -ec ] && [ "$4" = openbao-tls-root-crl-native-state ]
  printf '%s\n' native-read >> "$RTK_TLS_CACHE_FIXTURE/exec.log"
  script=$(printf '%s' "$3" | sed "s|/var/lib/pki-service/private|$RTK_TLS_CACHE_PRIVATE|g")
  file="$RTK_TLS_CACHE_PRIVATE${5#/var/lib/pki-service/private}"
  exec /bin/sh -ec "$script" "$4" "$file" ;;
 *) exit 73 ;;
esac
`)
			for _, name := range []string{"stat", "kubectl"} {
				if err := os.Chmod(filepath.Join(fixture, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(fixture, "kubectl"))
			t.Setenv("RTK_TLS_CACHE_FIXTURE", fixture)
			t.Setenv("RTK_TLS_CACHE_PRIVATE", private)
			t.Setenv("RTK_TLS_CACHE_PRIVATE_TUPLE", privateTuple)
			t.Setenv("RTK_TLS_CACHE_CRL_TUPLE", crlTuple)
			t.Setenv("RTK_TLS_CACHE_FILE_MODE", fileMode)
			t.Setenv("RTK_TLS_CACHE_FILE_USER", fileUser)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			read, err := readOpenBaoTLSRootCRLState(ctx, cfg, backend, target, root, prior)
			pass := scenario == "700" || scenario == "2700" || scenario == "mixed"
			if (err == nil) != pass || pass && string(read) != string(cache) {
				t.Fatalf("native permission/readback result differs for %s: %v", scenario, err)
			}
			if strings.HasPrefix(scenario, "foreign-fsgroup") || strings.HasPrefix(scenario, "foreign-run-") {
				if _, err := os.Stat(filepath.Join(fixture, "exec.log")); !os.IsNotExist(err) {
					t.Fatal("foreign maintained Pod identity reached native cache read")
				}
			} else if _, err := os.Stat(filepath.Join(fixture, "exec.log")); err != nil {
				t.Fatal("permission case did not exercise the actual native shell guard", err)
			}
			if info, err := os.Stat(store.Root); err != nil || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSetgid != 0 {
				t.Fatal("native PVC rule altered the isolated canonical local-store rule", err)
			}
		})
	}
}
