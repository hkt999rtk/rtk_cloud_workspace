package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeAccountBootstrapSealRetirementAndLocalSeedProtection(t *testing.T) {
	for _, mode := range []string{"valid", "unsealed", "wrong-system", "unreadable", "replaced-owner", "concurrent-local", "deployment-drift", "restart-notready"} {
		t.Run(mode, func(t *testing.T) {
			kube := frozenKubernetesFixture(t)
			store, _ := newSecretStore("", "dev")
			workspace := writeDeploymentFixture(t, "dev", "lke")
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			cfg, err := resolveDeploymentConfig(workspace, "dev", "")
			if err != nil {
				t.Fatal(err)
			}
			profile := postgresBackupTestConfig(t)
			profile.Environment, profile.Stack, profile.Namespace = "dev", "video-cloud-dev", "video-cloud-dev-platform"
			profile.Worker.Environment, profile.Worker.Stack, profile.Worker.Remote.Prefix = "dev", profile.Stack, "dev/postgres"
			profile.Worker.Source.Host = "postgresql." + profile.Namespace + ".svc.cluster.local"
			raw, _ := json.Marshal(profile)
			writeTestFile(t, filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), string(raw))
			kubeRaw, _ := yaml.Marshal(kube)
			if err := store.write("kube/kubeconfig.yaml", kubeRaw, true); err != nil {
				t.Fatal(err)
			}
			backend, err := newEnvironmentKubernetesBackend(string(kubeRaw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			secret := accountBootstrapRetirementFixture("dev")
			delete(deploymentplan.Map(secret["data"]), "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL")
			delete(deploymentplan.Map(secret["data"]), "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD")
			if mode == "replaced-owner" {
				deploymentplan.Map(secret["metadata"])["uid"] = "replacement-owner"
			}
			secretRaw, _ := json.Marshal(secret)
			deployment := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "account-manager", "namespace": "video-cloud-dev-account-manager", "uid": "owned-deployment", "generation": float64(2)}, "spec": map[string]any{"replicas": float64(1), "template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{"rtk.cloud/bootstrap-credentials": "retired-v1"}}}}, "status": map[string]any{"observedGeneration": float64(2), "updatedReplicas": float64(1), "availableReplicas": float64(1)}}
			if mode == "deployment-drift" {
				deploymentplan.Map(deployment["metadata"])["uid"] = "replacement-deployment"
			}
			if mode == "restart-notready" {
				deploymentplan.Map(deployment["status"])["updatedReplicas"] = float64(0)
			}
			deploymentRaw, _ := json.Marshal(deployment)
			seal := accountBootstrapSeal{SystemIdentifier: "123456789", Sealed: true}
			if mode == "unsealed" {
				seal.Sealed = false
			}
			if mode == "wrong-system" {
				seal.SystemIdentifier = "other-cluster"
			}
			sealRaw, _ := json.Marshal(seal)
			directory := t.TempDir()
			fixtureSecret, fixtureSeal := filepath.Join(directory, "secret.json"), filepath.Join(directory, "seal.json")
			fixtureDeployment := filepath.Join(directory, "deployment.json")
			writeTestFile(t, fixtureDeployment, string(deploymentRaw))
			writeTestFile(t, fixtureSecret, string(secretRaw))
			writeTestFile(t, fixtureSeal, string(sealRaw))
			code := "#!/bin/sh\ncase \"$*\" in\n *' exec '*) cat \"$RTK_TEST_SEAL_FILE\";;\n *' get Secret '*) cat \"$RTK_TEST_SECRET_FILE\";;\n *' get Deployment '*) cat \"$RTK_TEST_DEPLOYMENT_FILE\";;\n *) exit 1;;\nesac\n"
			if mode == "unreadable" {
				code = "#!/bin/sh\nexit 1\n"
			}
			writeTestFile(t, filepath.Join(directory, "kubectl"), code)
			os.Chmod(filepath.Join(directory, "kubectl"), 0755)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_SEAL_FILE", fixtureSeal)
			t.Setenv("RTK_TEST_SECRET_FILE", fixtureSecret)
			t.Setenv("RTK_TEST_DEPLOYMENT_FILE", fixtureDeployment)
			if err := store.write("runtime/platform-admin", []byte("approved-seed\n"), true); err != nil {
				t.Fatal(err)
			}
			if mode == "concurrent-local" {
				if err := store.write("runtime/platform-admin", []byte("concurrent-seed\n"), true); err != nil {
					t.Fatal(err)
				}
			}
			plan := &deploymentplan.Plan{Version: 2, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: profile.Stack, Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: "Secret/video-cloud-dev-account-manager/account-manager-runtime", UID: "owned-secret"}, {ID: "Deployment/video-cloud-dev-account-manager/account-manager", UID: "owned-deployment"}}}
			c := &compiledEnvironmentDeployment{Plan: plan, Store: store, Backend: backend, Configuration: cfg, BootstrapSeedSHA256: deploymentplan.Digest("approved-seed")}
			if mode == "valid" {
				beforeSecret := accountBootstrapRetirementFixture("dev")
				target := deploymentplan.Clone(deployment)
				delete(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(target["spec"])["template"])["metadata"]), "annotations")
				definitions, err := bindAccountManagerBootstrapRetirement(context.Background(), backend, []deploymentplan.Definition{{Object: target, Phase: "workload"}}, []deploymentplan.Object{beforeSecret}, environmentIdentityBindings{Stack: "video-cloud-dev"}, profile, "upgrade")
				if err != nil || len(definitions) != 2 {
					t.Fatal("compiler omitted bootstrap lifecycle", err)
				}
				metadata := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(target["spec"])["template"])["metadata"])
				if deploymentplan.Map(metadata["annotations"])["rtk.cloud/bootstrap-credentials"] != "retired-v1" {
					t.Fatal("credential-free restart absent from target")
				}
			}
			err = retireLocalAccountBootstrapSeed(context.Background(), c)
			if (err == nil) != (mode == "valid") {
				t.Fatal("local lifecycle outcome incorrect", err)
			}
			if mode == "valid" {
				if _, err := store.readRuntime("platform-admin"); !os.IsNotExist(err) {
					t.Fatal("retired local seed remains")
				}
				if supportSecretRequired(store, "platform-admin") {
					t.Fatal("actual retired bootstrap seed remains required")
				}
				var out bytes.Buffer
				if err := ensureMissingRuntimeSecrets(&out, store); err != nil {
					t.Fatal("ensure rejected verified retired state", err)
				}
				if _, err := store.readRuntime("platform-admin"); !os.IsNotExist(err) {
					t.Fatal("ensure recreated bootstrap seed")
				}
			} else {
				if _, err := store.readRuntime("platform-admin"); err != nil {
					t.Fatal("failed cleanup removed original or concurrent seed")
				}
			}
		})
	}
}

func accountBootstrapRetirementFixture(environment string) deploymentplan.Object {
	stack := "video-cloud-" + environment
	return deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": map[string]any{"name": "account-manager-runtime", "namespace": stack + "-account-manager", "uid": "owned-secret", "resourceVersion": "3", "labels": map[string]any{"rtk.realtek.com/stack": stack}}, "data": map[string]any{
		"DATABASE_URL":      base64.StdEncoding.EncodeToString([]byte("postgres://postgres:private-db-password@postgresql." + stack + "-platform.svc.cluster.local:5432/rtk_account_manager?sslmode=disable")),
		"JWT_ACCESS_SECRET": base64.StdEncoding.EncodeToString([]byte("private-current-jwt")),
		"CUSTOM_RETAINED":   base64.StdEncoding.EncodeToString([]byte("private-extra")),
		"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL":    base64.StdEncoding.EncodeToString([]byte("initial@example.test")),
		"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": base64.StdEncoding.EncodeToString([]byte("private-bootstrap-password")),
	}}
}

func TestAccountBootstrapRetirementUsesSameFencedBodyAndPreservesRuntimeCredentials(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			secret := accountBootstrapRetirementFixture(environment)
			stack := "video-cloud-" + environment
			before := deploymentplan.Digest(secret)
			definition, changed, err := accountManagerBootstrapRetirement(secret, stack, accountBootstrapSeal{SystemIdentifier: "123456789", Sealed: true}, "123456789")
			if err != nil || !changed || !accountManagerBootstrapKeysAbsent(definition.Object) {
				t.Fatal("sealed credential retirement missing", err)
			}
			if deploymentplan.Digest(secret) != before {
				t.Fatal("preflight mutated live state")
			}
			for _, key := range []string{"DATABASE_URL", "JWT_ACCESS_SECRET", "CUSTOM_RETAINED"} {
				if deploymentplan.Map(definition.Object["data"])[key] != deploymentplan.Map(secret["data"])[key] {
					t.Fatal("unrelated runtime credential changed")
				}
			}
			input := deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}
			plan, err := deploymentplan.Compile(input, []deploymentplan.Definition{definition}, []deploymentplan.Object{secret})
			if err != nil {
				t.Fatal(err)
			}
			resource := plan.Resources[0]
			admitted, err := resource.Body(secret)
			if err != nil || !accountManagerBootstrapKeysAbsent(admitted) {
				t.Fatal("admission body incorrect", err)
			}
			executed, err := resource.Body(secret)
			if err != nil || deploymentplan.Digest(admitted) != deploymentplan.Digest(executed) {
				t.Fatal("execution differs from preflight")
			}
			concurrent := deploymentplan.Clone(secret)
			deploymentplan.Map(concurrent["data"])["CUSTOM_RETAINED"] = "concurrent-value"
			if _, err := resource.Body(concurrent); err == nil {
				t.Fatal("concurrent Secret change overwritten")
			}
			_, changed, err = accountManagerBootstrapRetirement(executed, stack, accountBootstrapSeal{SystemIdentifier: "123456789", Sealed: true}, "123456789")
			if err != nil || changed {
				t.Fatal("already retired state was reinitialized", err)
			}
		})
	}
}

func TestAccountBootstrapRetirementBlocksMissingSealForeignDatabaseAndImmutableState(t *testing.T) {
	for _, mode := range []string{"unsealed", "wrong-system", "foreign-secret", "external-database", "wrong-db", "invalid-url", "immutable", "unknown-bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			secret := accountBootstrapRetirementFixture("dev")
			seal := accountBootstrapSeal{SystemIdentifier: "123456789", Sealed: true}
			switch mode {
			case "unsealed":
				seal.Sealed = false
			case "wrong-system":
				seal.SystemIdentifier = "987654321"
			case "foreign-secret":
				deploymentplan.Map(secret["metadata"])["namespace"] = "video-cloud-other-account-manager"
			case "external-database":
				deploymentplan.Map(secret["data"])["DATABASE_URL"] = base64.StdEncoding.EncodeToString([]byte("postgres://user:private@other-db:5432/rtk_account_manager"))
			case "wrong-db":
				deploymentplan.Map(secret["data"])["DATABASE_URL"] = base64.StdEncoding.EncodeToString([]byte("postgres://user:private@postgresql.video-cloud-dev-platform.svc.cluster.local:5432/other_db"))
			case "invalid-url":
				deploymentplan.Map(secret["data"])["DATABASE_URL"] = "invalid"
			case "immutable":
				secret["immutable"] = true
			case "unknown-bootstrap":
				deploymentplan.Map(secret["data"])["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_UNKNOWN"] = "unknown"
			}
			before := deploymentplan.Digest(secret)
			_, _, err := accountManagerBootstrapRetirement(secret, "video-cloud-dev", seal, "123456789")
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe lifecycle accepted or Secret exposed", err)
			}
			if deploymentplan.Digest(secret) != before {
				t.Fatal("failed planning changed Secret")
			}
		})
	}
}

func TestAccountBootstrapRetirementMarkerCannotAuthorizeGOOrRegenerateSeed(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("deployment/account-bootstrap-retired.json", []byte(`{"caller_success":true}`), true); err != nil {
		t.Fatal(err)
	}
	path, _ := store.safePath("runtime/platform-admin")
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if !supportSecretRequired(store, "platform-admin") {
		t.Fatal("local marker replaced actual source verification")
	}
	var out bytes.Buffer
	if err := ensureMissingRuntimeSecrets(&out, store); err == nil {
		t.Fatal("unverified retired state allowed generation")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("closed seed was regenerated")
	}
	// Older catalogs may not have recorded the bootstrap credential. Updating
	// such a catalog cannot bypass the same retirement fence as ensure.
	raw, err := store.read("inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory secretInventory
	if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
		t.Fatal(err)
	}
	kept := inventory.Entries[:0]
	for _, entry := range inventory.Entries {
		if entry.ID != "platform-admin" {
			kept = append(kept, entry)
		}
	}
	inventory.Entries = kept
	body, _ := json.Marshal(inventory)
	if err := store.write("inventory.json", body, true); err != nil {
		t.Fatal(err)
	}
	if err := ensureSecretStoreCatalogAdditions(&out, store); err == nil {
		t.Fatal("catalog migration regenerated an unverified retired seed")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("catalog migration wrote a new bootstrap seed")
	}
}
