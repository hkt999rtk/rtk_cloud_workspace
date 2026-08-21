package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeploymentRuntimeEndpointsPreferExplicitServiceDomains(t *testing.T) {
	endpoints := deploymentRuntimeEndpoints(map[string]string{
		"CLOUD_STACK_NAME":          "coverage-123-1",
		"CLOUD_DNS_ROOT_DOMAIN":     "coverage-123-1.invalid",
		"ACCOUNT_MANAGER_DOMAIN":    "account.coverage-123-1.invalid",
		"VIDEO_CLOUD_DOMAIN":        "video.coverage-123-1.invalid",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video.coverage-123-1.invalid",
	})
	want := map[string]string{
		"ACCOUNT_MANAGER_BASE_URL":    "https://account.coverage-123-1.invalid",
		"VIDEO_CLOUD_BASE_URL":        "https://video.coverage-123-1.invalid",
		"VIDEO_CLOUD_PUBLIC_BASE_URL": "https://video.coverage-123-1.invalid",
		"VIDEO_CLOUD_MTLS_BASE_URL":   "https://device.video.coverage-123-1.invalid",
		"VIDEO_CLOUD_TOKEN_BASE_URL":  "https://device.video.coverage-123-1.invalid",
		"VIDEO_CLOUD_MQTT_ADDR":       "video.coverage-123-1.invalid:8883",
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("endpoints = %#v, want %#v", endpoints, want)
	}
}

func TestDeploymentRuntimeEndpointsDeriveLegacyDomains(t *testing.T) {
	endpoints := deploymentRuntimeEndpoints(map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
	})
	if got := endpoints["VIDEO_CLOUD_MTLS_BASE_URL"]; got != "https://device.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("VIDEO_CLOUD_MTLS_BASE_URL = %q", got)
	}
	if got := endpoints["ACCOUNT_MANAGER_BASE_URL"]; got != "https://account-manager.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("ACCOUNT_MANAGER_BASE_URL = %q", got)
	}
}

func TestDeploymentRejectsUnknownActionAndMissingConfirmation(t *testing.T) {
	if err := runDeploymentWithOperations([]string{"unknown"}, deploymentOperations{}); err == nil || !strings.Contains(err.Error(), "unknown deployment action") {
		t.Fatalf("unknown action error = %v", err)
	}
	workspace := writeDeploymentFixture(t, "staging", "lke")
	if err := runDeploymentWithOperations([]string{"acceptance", "--workspace", workspace, "--environment", "staging"}, deploymentOperations{}); err == nil || !strings.Contains(err.Error(), "--confirm video-cloud-staging is required") {
		t.Fatalf("missing confirmation error = %v", err)
	}
}

func TestResolveDeploymentStoragePlanRejectsInvalidProfiles(t *testing.T) {
	identity := map[string]string{"DEPLOYMENT_LOCATION": "asia-southeast", "CLOUD_STACK_NAME": "video-cloud-staging"}
	validRuntime := "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=media\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/staging\n"
	validShared := "RELEASE_ARTIFACT_STORAGE_POLICY=shared-cross-region\nRELEASE_ARTIFACT_STORAGE_BUCKET=artifacts\nRELEASE_ARTIFACT_STORAGE_LOCATION=us-west\nRELEASE_ARTIFACT_STORAGE_REGION=us-sea\nRELEASE_ARTIFACT_STORAGE_PREFIX=releases\n"
	tests := []struct {
		name, runtime, shared string
		adapterResolved       map[string]string
		makeRuntimeDir        bool
		makeSharedDir         bool
	}{
		{name: "runtime read error", makeRuntimeDir: true},
		{name: "unknown runtime key", runtime: validRuntime + "UNKNOWN=value\n"},
		{name: "runtime policy", runtime: strings.Replace(validRuntime, "colocated", "shared", 1)},
		{name: "runtime required value", runtime: strings.Replace(validRuntime, "media", "", 1)},
		{name: "shared read error", runtime: validRuntime, makeSharedDir: true},
		{name: "unknown shared key", runtime: validRuntime, shared: validShared + "UNKNOWN=value\n"},
		{name: "shared policy", runtime: validRuntime, shared: strings.Replace(validShared, "shared-cross-region", "colocated", 1)},
		{name: "shared required value", runtime: validRuntime, shared: strings.Replace(validShared, "artifacts", "", 1)},
		{name: "missing compute region", runtime: validRuntime, shared: validShared, adapterResolved: map[string]string{"LKE_REGION": ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			environmentRoot := filepath.Join(workspace, "cloud_env", "staging")
			if err := os.MkdirAll(environmentRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			runtimePath := filepath.Join(environmentRoot, "storage.env")
			sharedPath := filepath.Join(workspace, "cloud_deploy", "storage", "release-artifacts.env")
			if tc.makeRuntimeDir {
				if err := os.Mkdir(runtimePath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if tc.runtime != "" {
				writeTestFile(t, runtimePath, tc.runtime)
			}
			if tc.makeSharedDir {
				if err := os.MkdirAll(sharedPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if tc.shared != "" {
				writeTestFile(t, sharedPath, tc.shared)
			}
			if _, err := resolveDeploymentStoragePlan(workspace, environmentRoot, identity, tc.adapterResolved); err == nil {
				t.Fatal("invalid storage profile unexpectedly resolved")
			}
		})
	}
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "storage.env"), "UNKNOWN=value\n")
	if _, err := resolveDeploymentConfig(workspace, "staging", ""); err == nil {
		t.Fatal("deployment config accepted invalid storage profile")
	}
}

func TestDeploymentCredentialFailureStopsBeforeRuntimeMutation(t *testing.T) {
	for _, action := range []string{"provision", "test"} {
		t.Run(action, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			calls := []string{}
			ops := deploymentOperations{
				credentials: func(deploymentConfig, string) error {
					calls = append(calls, "credentials")
					return errors.New("invalid token")
				},
				prepareTest: func(deploymentConfig) error {
					calls = append(calls, "prepare-test")
					return nil
				},
				provision: func(deploymentConfig) error {
					calls = append(calls, "provision")
					return nil
				},
				cleanup: func(deploymentConfig) error {
					calls = append(calls, "cleanup")
					return nil
				},
				normalize: func(deploymentConfig) error {
					calls = append(calls, "normalize")
					return nil
				},
			}
			err := runDeploymentWithOperations([]string{
				action, "--workspace", workspace, "--environment", "staging",
				"--confirm", "video-cloud-staging", "--env-file", filepath.Join(workspace, "operator.env"),
			}, ops)
			if err == nil || !strings.Contains(err.Error(), "invalid token") {
				t.Fatalf("error = %v", err)
			}
			if want := []string{"credentials"}; !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
			if _, statErr := os.Stat(filepath.Join(workspace, "cloud_env", "staging", "runtime")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("runtime was materialized before credential validation: %v", statErr)
			}
		})
	}
}

func TestDeploymentProvisionInstallsValidatedStorageCredentials(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	sharedFile := filepath.Join(t.TempDir(), "shared.env")
	environmentFile := filepath.Join(t.TempDir(), "staging.env")
	if err := os.WriteFile(sharedFile, []byte("LINODE_TOKEN=token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentFile, []byte("LINODE_MEDIA_OBJ_ACCESS_KEY_ID=media-access\nLINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=media-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDeploymentStorageReceipt(cfg.RuntimeRoot, deploymentStorageReceipt{
		Purpose: "runtime-media", Bucket: cfg.Storage.RuntimeMedia.Bucket, Region: cfg.Storage.RuntimeMedia.Region, Endpoint: "https://example.invalid",
	}); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	ops := deploymentOperations{
		credentials: func(deploymentConfig, string) error { calls = append(calls, "credentials"); return nil },
		provision: func(deploymentConfig) error {
			calls = append(calls, "provision")
			if os.Getenv("LINODE_OBJ_ACCESS_KEY_ID") != "media-access" || os.Getenv("LINODE_OBJ_ENDPOINT") != "https://example.invalid" {
				return errors.New("validated storage credentials were not installed")
			}
			return nil
		},
		normalize: func(deploymentConfig) error { calls = append(calls, "normalize"); return nil },
	}
	if err := runDeploymentWithOperations([]string{
		"provision", "--workspace", workspace, "--environment", "staging", "--confirm", "video-cloud-staging",
		"--env-file", environmentFile, "--shared-env-file", sharedFile,
	}, ops); err != nil {
		t.Fatal(err)
	}
	if want := []string{"credentials", "provision", "normalize"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if os.Getenv("LINODE_OBJ_ACCESS_KEY_ID") != "" {
		t.Fatal("child storage credential leaked after provisioning")
	}

	missingReceiptWorkspace := writeDeploymentFixture(t, "staging", "lke")
	err = runDeploymentWithOperations([]string{
		"provision", "--workspace", missingReceiptWorkspace, "--environment", "staging", "--confirm", "video-cloud-staging", "--env-file", environmentFile,
	}, deploymentOperations{credentials: func(deploymentConfig, string) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "validated storage receipt is required") {
		t.Fatalf("missing receipt error = %v", err)
	}
}

func TestDeploymentCredentialCheckExplicitlyBootstrapsMissingObjectStorageBucket(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	calls := []string{}
	ops := deploymentOperations{
		credentials: func(deploymentConfig, string) error {
			calls = append(calls, "read-only")
			return nil
		},
		bootstrapCredentials: func(deploymentConfig, string) error {
			calls = append(calls, "bootstrap")
			return nil
		},
	}
	err := runDeploymentWithOperations([]string{
		"credentials-check", "--workspace", workspace, "--environment", "staging",
		"--env-file", filepath.Join(workspace, "operator.env"),
		"--create-missing-object-storage-bucket",
	}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bootstrap"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "cloud_env", "staging", "runtime")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential bootstrap materialized deployment runtime: %v", statErr)
	}
}

func TestDeploymentCredentialCheckExplicitlyGrantsObjectStorageBucketAccess(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	calls := []string{}
	ops := deploymentOperations{
		credentials: func(deploymentConfig, string) error {
			calls = append(calls, "read-only")
			return nil
		},
		grantObjectStorageCredentials: func(deploymentConfig, string) error {
			calls = append(calls, "grant")
			return nil
		},
	}
	err := runDeploymentWithOperations([]string{
		"credentials-check", "--workspace", workspace, "--environment", "staging",
		"--env-file", filepath.Join(workspace, "operator.env"),
		"--grant-object-storage-bucket-access",
	}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"grant"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDeploymentCredentialBucketBootstrapFlagIsRejectedForDeploymentMutation(t *testing.T) {
	for _, flag := range []string{"--create-missing-object-storage-bucket", "--grant-object-storage-bucket-access"} {
		err := runDeploymentWithOperations([]string{"provision", flag}, deploymentOperations{})
		if err == nil || !strings.Contains(err.Error(), "only valid with deployment credentials-check") {
			t.Fatalf("flag %s error = %v", flag, err)
		}
	}
	err := runDeploymentWithOperations([]string{
		"credentials-check",
		"--create-missing-object-storage-bucket",
		"--grant-object-storage-bucket-access",
	}, deploymentOperations{})
	if err == nil || !strings.Contains(err.Error(), "must be run as separate") {
		t.Fatalf("combined credential repair flags error = %v", err)
	}
}

func TestDeploymentPreflightPlanIsReadOnly(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	runtimeRoot := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	if err := runDeploymentWithOperations([]string{
		"preflight", "--workspace", workspace, "--environment", "staging", "--operation", "plan",
	}, deploymentOperations{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("preflight materialized runtime: %v", err)
	}
}

func TestDeploymentPreflightProvisionChecksInputsWithoutSecrets(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("GHCR_PULL_USERNAME", "")
	t.Setenv("GHCR_PULL_TOKEN", "")
	t.Setenv("GODADDY_KEY", "")
	t.Setenv("GODADDY_SECRET", "")

	var out bytes.Buffer
	checks := defaultDeploymentPreflightChecks()
	checks.lookPath = func(string) (string, error) { return "/fake/tool", nil }
	checks.validateDNS = func(deploymentConfig) error { return errors.New("GODADDY_SECRET is required") }
	checks.validateLKEState = func(deploymentConfig) error { return errors.New("LINODE_TOKEN is required") }
	err = runDeploymentPreflightWithChecks(cfg, "provision", checks, &out)
	if err == nil {
		t.Fatal("expected missing deployment inputs to fail")
	}
	for _, want := range []string{"FAIL credential:linode", "FAIL credential:ghcr", "FAIL credential:dns", "FAIL active-service-limit", "Preflight result: FAIL"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("preflight output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "test-secret-value") {
		t.Fatalf("preflight leaked a secret:\n%s", out.String())
	}
}

func TestDeploymentPreflightAcceptanceRequiresMatchingRuntime(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	checks := defaultDeploymentPreflightChecks()
	checks.lookPath = func(string) (string, error) { return "/fake/tool", nil }
	checks.validateKube = func(deploymentConfig) error { return errors.New("kubeconfig unavailable") }
	if err := runDeploymentPreflightWithChecks(cfg, "acceptance", checks, &out); err == nil {
		t.Fatal("expected missing matching runtime to fail")
	}
	if !strings.Contains(out.String(), "required matching runtime file is missing") || !strings.Contains(out.String(), "FAIL kubernetes-access") {
		t.Fatalf("unexpected acceptance preflight output:\n%s", out.String())
	}
}

func TestDeploymentPreflightAcceptancePassesWithMatchingRuntime(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"state/provider-preflight.env": "LKE_CLUSTER_ID=123\n",
		"state/kubeconfig.yaml":        "apiVersion: v1\n",
		"env/stack.env":                "CLOUD_STACK_NAME=" + cfg.Values["CLOUD_STACK_NAME"] + "\n",
		"state/openbao/unseal-key":     "test-unseal-key\n",
		"state/openbao/root-token":     "test-root-token\n",
		"state/secrets/postgres":       "test-postgres-password\n",
	} {
		writeTestFile(t, filepath.Join(cfg.RuntimeRoot, path), contents)
	}

	var out bytes.Buffer
	checks := defaultDeploymentPreflightChecks()
	checks.lookPath = func(string) (string, error) { return "/fake/tool", nil }
	checks.validateKube = func(deploymentConfig) error { return nil }
	if err := runDeploymentPreflightWithChecks(cfg, "acceptance", checks, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PASS runtime-state", "PASS runtime-identity", "PASS kubernetes-access", "Preflight result: PASS"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("preflight output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDeploymentPreflightEphemeralRejectsExistingStack(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LINODE_TOKEN", "redacted-linode")
	t.Setenv("GHCR_PULL_USERNAME", "operator")
	t.Setenv("GHCR_PULL_TOKEN", "redacted-ghcr")
	writeTestFile(t, filepath.Join(home, ".ssh", "id_ed25519_rtkcloud"), "private-placeholder")
	writeTestFile(t, filepath.Join(home, ".ssh", "id_ed25519_rtkcloud.pub"), "public-placeholder")
	writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "account.env"), "LKE_ACTIVE_SERVICE_LIMIT=20\n")

	var out bytes.Buffer
	checks := defaultDeploymentPreflightChecks()
	checks.lookPath = func(string) (string, error) { return "/fake/tool", nil }
	checks.validateDNS = func(deploymentConfig) error { return nil }
	checks.validateLKEState = func(deploymentConfig) error { return nil }
	checks.validateEphemeral = func(deploymentConfig) error { return errors.New("stack already owns provider resources") }
	if err := runDeploymentPreflightWithChecks(cfg, "ephemeral-test", checks, &out); err == nil {
		t.Fatal("expected pre-existing stack to fail ephemeral preflight")
	}
	if !strings.Contains(out.String(), "FAIL ephemeral-ownership") {
		t.Fatalf("unexpected ephemeral preflight output:\n%s", out.String())
	}
	for _, secret := range []string{"redacted-linode", "redacted-ghcr"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("preflight output leaked %s", secret)
		}
	}
}

func TestDeploymentTestUsesIdenticalLifecycleForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			calls := []string{}
			ops := deploymentOperations{
				prepareTest: func(deploymentConfig) error { return nil },
				provision: func(deploymentConfig) error {
					calls = append(calls, "provision")
					return nil
				},
				acceptance: func(deploymentConfig) error {
					calls = append(calls, "acceptance")
					return nil
				},
				cleanup: func(deploymentConfig) error {
					calls = append(calls, "cleanup")
					return nil
				},
				normalize: func(deploymentConfig) error {
					calls = append(calls, "normalize")
					return nil
				},
			}
			err := runDeploymentWithOperations([]string{
				"test", "--workspace", workspace, "--environment", environment,
				"--confirm", "video-cloud-" + environment,
			}, ops)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"provision", "acceptance", "cleanup", "normalize"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		})
	}
}

func TestDeploymentTestAlwaysCleansUpAfterFailure(t *testing.T) {
	for _, tc := range []struct {
		name              string
		provisionErr      error
		acceptanceErr     error
		wantAcceptanceRun bool
		wantError         string
	}{
		{name: "provision", provisionErr: errors.New("partial provision"), wantError: "provision phase failed"},
		{name: "acceptance", acceptanceErr: errors.New("probe failed"), wantAcceptanceRun: true, wantError: "acceptance phase failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			acceptanceRan := false
			cleanupRan := false
			ops := deploymentOperations{
				prepareTest: func(deploymentConfig) error { return nil },
				provision:   func(deploymentConfig) error { return tc.provisionErr },
				acceptance: func(deploymentConfig) error {
					acceptanceRan = true
					return tc.acceptanceErr
				},
				cleanup: func(deploymentConfig) error {
					cleanupRan = true
					return nil
				},
				normalize: func(deploymentConfig) error { return nil },
			}
			err := runDeploymentWithOperations([]string{
				"test", "--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev",
			}, ops)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if acceptanceRan != tc.wantAcceptanceRun {
				t.Fatalf("acceptanceRan = %t, want %t", acceptanceRan, tc.wantAcceptanceRun)
			}
			if !cleanupRan {
				t.Fatal("cleanup did not run")
			}
		})
	}
}

func TestDeploymentTestReportsCleanupFailure(t *testing.T) {
	workspace := writeDeploymentFixture(t, "prod", "lke")
	ops := deploymentOperations{
		prepareTest: func(deploymentConfig) error { return nil },
		provision:   func(deploymentConfig) error { return nil },
		acceptance:  func(deploymentConfig) error { return nil },
		cleanup:     func(deploymentConfig) error { return errors.New("resource remained") },
		normalize:   func(deploymentConfig) error { return nil },
	}
	err := runDeploymentWithOperations([]string{
		"test", "--workspace", workspace, "--environment", "prod", "--confirm", "video-cloud-prod",
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "cleanup phase failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeploymentTestDoesNotCleanupPreExistingEnvironment(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cleanupRan := false
	ops := deploymentOperations{
		prepareTest: func(deploymentConfig) error { return errors.New("stack already exists") },
		provision:   func(deploymentConfig) error { return errors.New("must not run") },
		acceptance:  func(deploymentConfig) error { return errors.New("must not run") },
		cleanup: func(deploymentConfig) error {
			cleanupRan = true
			return nil
		},
		normalize: func(deploymentConfig) error { return nil },
	}
	err := runDeploymentWithOperations([]string{
		"test", "--workspace", workspace, "--environment", "staging", "--confirm", "video-cloud-staging",
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "ephemeral test preflight failed") {
		t.Fatalf("error = %v", err)
	}
	if cleanupRan {
		t.Fatal("cleanup must not run for a pre-existing environment")
	}
}

func TestDeploymentEnvironmentResourceLabelsExcludeUnownedOrphans(t *testing.T) {
	plan := linodeDestroyPlan{
		LKEClusters:         []linodeDestroyResource{{Label: "stack-lke"}},
		Instances:           []linodeDestroyResource{{Label: "stack-edge"}},
		ObjectBuckets:       []linodeDestroyResource{{Label: "stack-artifacts"}},
		OrphanVolumes:       []linodeDestroyResource{{Label: "pvc-unrelated"}},
		OrphanNodeBalancers: []linodeDestroyResource{{Label: "lke-unrelated"}},
	}
	want := []string{"stack-artifacts", "stack-edge", "stack-lke"}
	if got := deploymentEnvironmentResourceLabels(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestPersistentVolumeIDsForStackSelectsOnlyOwnedNumericHandles(t *testing.T) {
	body := []byte(`{"items":[
		{"spec":{"claimRef":{"namespace":"video-cloud-dev-platform"},"csi":{"volumeHandle":"102-pvc-example"}}},
		{"spec":{"claimRef":{"namespace":"video-cloud-dev-video-cloud"},"csi":{"volumeHandle":"101"}}},
		{"spec":{"claimRef":{"namespace":"video-cloud-staging-platform"},"csi":{"volumeHandle":"999"}}}
	]}`)
	got, err := persistentVolumeIDsForStack(body, "video-cloud-dev")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"101", "102"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	bad := []byte(`{"items":[{"spec":{"claimRef":{"namespace":"video-cloud-dev-platform"},"csi":{"volumeHandle":"not-a-linode-id"}}}]}`)
	if _, err := persistentVolumeIDsForStack(bad, "video-cloud-dev"); err == nil {
		t.Fatal("expected non-numeric owned volume handle to fail")
	}
}

func TestResolveDeploymentConfigSupportsMultipleEnvironments(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" || cfg.Architecture != "kubernetes" || cfg.Adapter != "lke" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Values["CAPACITY_TARGET_CONNECTIONS"] != "1000" {
		t.Fatalf("target = %q", cfg.Values["CAPACITY_TARGET_CONNECTIONS"])
	}
	if cfg.RuntimeRoot != filepath.Join(workspace, "cloud_env", "staging", "runtime") {
		t.Fatalf("runtime = %s", cfg.RuntimeRoot)
	}
	for key, want := range map[string]string{"LKE_REGION": "us-sea", "LKE_GENERAL_NODE_TYPE": "g6-standard-4", "LKE_BROKER_NODE_TYPE": "g6-standard-4", "LKE_DATABASE_NODE_TYPE": "g6-standard-8"} {
		if cfg.AdapterResolved[key] != want {
			t.Fatalf("%s = %q, want %q", key, cfg.AdapterResolved[key], want)
		}
	}
}

func TestResolveDeploymentConfigRejectsLegacyRoot(t *testing.T) {
	for _, providerRoot := range []string{"lke", "linode"} {
		_, err := resolveDeploymentConfig(t.TempDir(), "", filepath.Join(t.TempDir(), "cloud_env", "staging", providerRoot))
		if err == nil || !strings.Contains(err.Error(), "legacy provider env-root") {
			t.Fatalf("%s got %v", providerRoot, err)
		}
	}
}

func TestResolveDeploymentConfigRequiresEnvironmentIdentity(t *testing.T) {
	workspace := writeDeploymentFixture(t, "qa", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "qa", "environment.env"), "CLOUD_STACK_NAME=video-cloud-qa\n")
	_, err := resolveDeploymentConfig(workspace, "qa", "")
	if err == nil || !strings.Contains(err.Error(), "CLOUD_DNS_ROOT_DOMAIN is required") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsProviderKeyInArchitecture(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env"), "LKE_NODE_COUNT=2\n")
	_, err := resolveDeploymentConfig(workspace, "dev", "")
	if err == nil || !strings.Contains(err.Error(), "not allowed in architecture") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnknownArchitectureKey(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env"), "UNDECLARED_WORKLOAD_SETTING=true\n")
	_, err := resolveDeploymentConfig(workspace, "dev", "")
	if err == nil || !strings.Contains(err.Error(), "unknown architecture key") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsProviderKeyInEnvironment(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "LKE_REGION=us-sea\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "unknown environment key LKE_REGION") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnknownLocationAndUnsatisfiedShape(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=moon\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "no region mapping") {
		t.Fatalf("unknown location got %v", err)
	}
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\n")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "NODE_CLASS_GENERAL_MIN_VCPU=100\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "no Linode type satisfies") {
		t.Fatalf("unsatisfied shape got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsNonPositiveNodeIntent(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "NODE_CLASS_BROKER_MIN_VCPU=0\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "must be a positive integer") {
		t.Fatalf("got %v", err)
	}
}

func TestSelectLKENodeTypeDeterministicAndValidatesPins(t *testing.T) {
	for _, tc := range []struct {
		cpu, memory int
		want        string
	}{
		{cpu: 4, memory: 8, want: "g6-standard-4"},
		{cpu: 4, memory: 16, want: "g6-standard-6"},
		{cpu: 8, memory: 16, want: "g6-standard-8"},
	} {
		got, err := selectLKENodeType(tc.cpu, tc.memory, "")
		if err != nil || got != tc.want {
			t.Fatalf("select %d/%d = %q, %v; want %q", tc.cpu, tc.memory, got, err, tc.want)
		}
	}
	if _, err := selectLKENodeType(8, 16, "g6-standard-4"); err == nil || !strings.Contains(err.Error(), "below required") {
		t.Fatalf("undersized pin got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnimplementedMutation(t *testing.T) {
	for _, adapter := range []string{"eks", "gke"} {
		workspace := writeDeploymentFixture(t, "prod", adapter)
		if err := runDeployment([]string{"plan", "--workspace", workspace, "--environment", "prod"}); err != nil {
			t.Fatalf("%s plan failed: %v", adapter, err)
		}
		err := runDeployment([]string{"provision", "--workspace", workspace, "--environment", "prod", "--confirm", "video-cloud-prod"})
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("%s mutation got %v", adapter, err)
		}
	}
}

func TestMaterializeDeploymentRuntimeSeparatesSharedAndAdapterConfig(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	resolved := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "resolved", "deployment.env"))
	if strings.Contains(resolved, "LKE_") || strings.Contains(resolved, "LINODE_") {
		t.Fatalf("resolved generic config leaked compatibility keys:\n%s", resolved)
	}
	compat := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	for _, want := range []string{
		"CLOUD_PROVIDER=lke",
		"CAPACITY_TARGET_CONNECTIONS=1000",
		"MQTT_EFFECTIVE_REPLICAS=2",
		"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=ed25519",
		"CERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,p256",
		"CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=ed25519,p256",
	} {
		if !strings.Contains(compat, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(compat, "LKE_") {
		t.Fatalf("shared stack contains adapter compatibility keys:\n%s", compat)
	}
	resources := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "resolved-resources.env"))
	for _, want := range []string{"LKE_REGION=us-sea", "LKE_GENERAL_NODE_TYPE=g6-standard-4", "LKE_BROKER_NODE_TYPE=g6-standard-4", "LKE_DATABASE_NODE_TYPE=g6-standard-8"} {
		if !strings.Contains(resources, want) {
			t.Fatalf("adapter resources missing %s:\n%s", want, resources)
		}
	}
	adapterConfig := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "config.env"))
	for _, want := range []string{"LKE_TARGET_CONNECTS=1000", "LKE_MQTT_REPLICAS=2", "LKE_REGION=us-sea", "LKE_GENERAL_NODE_TYPE=g6-standard-4"} {
		if !strings.Contains(adapterConfig, want) {
			t.Fatalf("adapter config missing %s", want)
		}
	}
}

func TestResolveDeploymentConfigValidatesCertificateAlgorithms(t *testing.T) {
	tests := []struct {
		name      string
		overrides string
		wantError string
	}{
		{name: "p256 policy", overrides: "CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=p256\nCERTIFICATE_APP_CSR_KEY_ALGORITHMS=p256,ed25519\nCERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=p256\n"},
		{name: "alias rejected", overrides: "CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=p-256\n", wantError: "must be ed25519 or p256"},
		{name: "duplicate rejected", overrides: "CERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,ed25519\n", wantError: "must not contain duplicate"},
		{name: "empty rejected", overrides: "CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=\n", wantError: "must contain at least one"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "overrides", "architecture.env"), tc.overrides)
			cfg, err := resolveDeploymentConfig(workspace, "staging", "")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("resolveDeploymentConfig() error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Values["CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM"] != "p256" || cfg.Values["CERTIFICATE_APP_CSR_KEY_ALGORITHMS"] != "p256,ed25519" || cfg.Values["CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS"] != "p256" {
				t.Fatalf("unexpected certificate policy: %#v", cfg.Values)
			}
		})
	}
}

func TestLKEAccountStateRequiredOnlyForMutation(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateLKEEnvironmentStateBeforeMutation(cfg); err == nil || !strings.Contains(err.Error(), "provider account state is required") {
		t.Fatalf("missing account state got %v", err)
	}
	writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "account.env"), "LKE_ACTIVE_SERVICE_LIMIT=20\n")
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	preflight := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env"))
	for _, want := range []string{"PROVIDER_ACTIVE_SERVICE_LIMIT=20", "PROVIDER_REGION=us-sea"} {
		if !strings.Contains(preflight, want+"\n") {
			t.Fatalf("provider preflight missing %s: %q", want, preflight)
		}
	}
	adapterConfig := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "config.env"))
	if !strings.Contains(adapterConfig, "LKE_LINODE_ACTIVE_SERVICE_LIMIT=20") {
		t.Fatalf("adapter config missing quota compatibility key:\n%s", adapterConfig)
	}
}

func TestNormalizeEnvironmentArgs(t *testing.T) {
	args, err := normalizeEnvironmentArgs([]string{"mqtt-test", "--workspace", "/tmp/ws", "--environment", "dev", "--brandname", "RTK"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mqtt-test", "--workspace", "/tmp/ws", "--env-root", "/tmp/ws/cloud_env/dev/runtime", "--brandname", "RTK"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, want)
	}
	if _, err := normalizeEnvironmentArgs([]string{"mqtt-test", "--env-root", "/tmp/runtime", "--environment", "dev"}); err == nil {
		t.Fatal("expected conflicting environment selectors to fail")
	}
}

func TestNormalizeEnvironmentArgsUsesCanonicalRuntimeForFeatureQualification(t *testing.T) {
	args, err := normalizeEnvironmentArgs([]string{"test-feature", "--workspace", "/tmp/ws", "--environment", "staging", "--feature", "device-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test-feature", "--workspace", "/tmp/ws", "--env-root", "/tmp/ws/cloud_env/staging/runtime", "--feature", "device-shadow"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, want)
	}
}

func TestNormalizeEnvironmentArgsAllowsExplicitFeatureEnvironmentRoot(t *testing.T) {
	input := []string{"test-feature", "--environment", "staging", "--env-root", "/tmp/runtime", "--feature", "device-shadow"}
	args, err := normalizeEnvironmentArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, input) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, input)
	}
}

func TestNormalizeEnvironmentArgsPreservesFeatureEvidenceEnvironment(t *testing.T) {
	input := []string{
		"test-feature-coverage", "record",
		"--test-id", "E2E-CA-SIGNUP-EMAIL-001",
		"--environment", "staging",
		"--output-dir", "/tmp/evidence",
	}
	args, err := normalizeEnvironmentArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, input) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, input)
	}
}

func writeDeploymentFixture(t *testing.T, environment, adapter string) string {
	t.Helper()
	root := t.TempDir()
	var workloads strings.Builder
	for _, spec := range capacityWorkloadRegistry {
		replicas, nodeClass := 1, "general"
		if spec.Name == "mqtt" {
			replicas, nodeClass = 2, "broker"
		} else if spec.Name == "video-cloud-api" {
			replicas = 2
		} else if spec.Name == "postgresql" {
			nodeClass = "database"
		}
		fmt.Fprintf(&workloads, "%s_MIN_REPLICAS=%d\n%s_NODE_CLASS=%s\n%s_REQUEST_CPU=100m\n%s_REQUEST_MEMORY=128Mi\n", spec.Prefix, replicas, spec.Prefix, nodeClass, spec.Prefix, spec.Prefix)
	}
	workloads.WriteString("MQTT_HARD_ANTI_AFFINITY=true\nPOSTGRES_LIMIT_MEMORY=4Gi\nCLOUD_LOGGER_LIMIT_MEMORY=2Gi\nCERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=ed25519\nCERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,p256\nCERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=ed25519,p256\nEDGE_REPLICAS=1\nEDGE_MAX_CONNECTIONS=400000\nTURN_REPLICAS=1\nTURN_MIN_PORT=49152\nTURN_MAX_PORT=49200\n")
	files := map[string]string{
		"cloud_deploy/architectures/kubernetes/architecture.env":   "DEPLOYMENT_RUNTIME=kubernetes\nNODE_CLASS_LABEL_KEY=rtk.io/node-class\nDEFAULT_WORKLOAD_NODE_CLASS=general\n",
		"cloud_deploy/architectures/kubernetes/capacity.env":       "CAPACITY_TARGET_CONNECTIONS=1000\nCAPACITY_CONNECTIONS_PER_MQTT_POD=20000\nCAPACITY_ACTIVE_DEVICES=1000\nCAPACITY_ACTIVE_DEVICES_PER_API_POD=40000\nCAPACITY_SYSTEM_RESERVED_CPU_MILLI=1000\nCAPACITY_SYSTEM_RESERVED_MEMORY_MIB=1536\n",
		"cloud_deploy/architectures/kubernetes/topology.env":       "NODE_CLASS_GENERAL_MIN_COUNT=2\nNODE_CLASS_GENERAL_MIN_VCPU=4\nNODE_CLASS_GENERAL_MIN_MEMORY_GIB=8\nNODE_CLASS_BROKER_MIN_COUNT=2\nNODE_CLASS_BROKER_MIN_VCPU=4\nNODE_CLASS_BROKER_MIN_MEMORY_GIB=8\nNODE_CLASS_DATABASE_MIN_COUNT=1\nNODE_CLASS_DATABASE_MIN_VCPU=8\nNODE_CLASS_DATABASE_MIN_MEMORY_GIB=16\n",
		"cloud_deploy/architectures/kubernetes/workloads.env":      workloads.String(),
		"cloud_deploy/adapters/lke/defaults.env":                   "LKE_REGION_PIN=\nLKE_GENERAL_NODE_TYPE_PIN=\nLKE_BROKER_NODE_TYPE_PIN=\nLKE_DATABASE_NODE_TYPE_PIN=\n",
		"cloud_deploy/adapters/lke/locations.env":                  "LOCATION_US_WEST=us-sea\n",
		"cloud_deploy/adapters/lke/schema.env":                     "ADAPTER_NAME=lke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=true\n",
		"cloud_deploy/adapters/eks/schema.env":                     "ADAPTER_NAME=eks\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		"cloud_deploy/adapters/gke/schema.env":                     "ADAPTER_NAME=gke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		"cloud_deploy/dns_adapters/godaddy/defaults.env":           "DNS_RECORD_TTL=600\nDNS_PROPAGATION_TIMEOUT_SECONDS=900\nDNS_PROPAGATION_INTERVAL_SECONDS=10\nGODADDY_ENV=prod\n",
		"cloud_deploy/dns_adapters/godaddy/schema.env":             "DNS_ADAPTER_NAME=godaddy\n",
		filepath.Join("cloud_env", environment, "environment.env"): "CLOUD_STACK_NAME=video-cloud-" + environment + "\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\n",
		filepath.Join("cloud_env", environment, "deployment.env"):  "DEPLOYMENT_ARCHITECTURE=kubernetes\nDEPLOYMENT_ADAPTER=" + adapter + "\nDNS_ADAPTER=godaddy\n",
	}
	for path, body := range files {
		writeTestFile(t, filepath.Join(root, path), body)
	}
	return root
}

func appendFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}
