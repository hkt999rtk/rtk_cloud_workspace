package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func videoStorageStartupFixture(t *testing.T, environment string) (deploymentConfig, environmentOTAStorageObservation, deploymentplan.Object) {
	t.Helper()
	cfg := deploymentConfig{Environment: environment, Values: map[string]string{"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "true"}, Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Bucket: "isolated-media-" + environment, Region: "us-sea", Prefix: "environments/video-cloud-" + environment}}}
	o := environmentOTAStorageObservation{Version: 1, Environment: environment, Provider: "linode", Bucket: cfg.Storage.RuntimeMedia.Bucket, Region: cfg.Storage.RuntimeMedia.Region, Prefix: cfg.Storage.RuntimeMedia.Prefix, Endpoint: "https://isolated-media.example.invalid", EndpointType: "E1"}
	values := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment, "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false", "LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)}
	if err := projectVideoStorageRenderInputs(cfg, o, values); err != nil {
		t.Fatal(err)
	}
	var object deploymentplan.Object
	if err := yaml.Unmarshal([]byte(lkeVideoStorageServiceDeploymentManifest(values)), &object); err != nil {
		t.Fatal(err)
	}
	object = deploymentplan.Clone(object)
	bindEnvironmentVideoStorageObjectStorage(videoStorageTestDefinitions(object))
	if err := deriveEnvironmentVideoStorageStartup(values, videoStorageTestDefinitions(object)); err != nil {
		t.Fatal(err)
	}
	return cfg, o, object
}

func videoStorageTestDefinitions(object deploymentplan.Object) []deploymentplan.Definition {
	ns := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	stack := strings.TrimSuffix(ns, "-video-cloud")
	return []deploymentplan.Definition{{Object: object}, {Object: deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": stack + "-account-manager"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "http", "port": float64(80)}, deploymentplan.Object{"name": "service-reg", "port": float64(8443)}}}}}, {Object: deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"name": "postgresql", "namespace": stack + "-platform"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "postgres", "port": float64(5432)}}}}}}
}

func mediaTestProvider(t *testing.T, cfg deploymentConfig, endpoint *string, missing *bool) (*httptest.Server, deploymentCredentialChecker) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/object-storage/buckets" {
			t.Errorf("media observation must not mutate provider: %s %s", r.Method, r.URL.Path)
		}
		items := []any{linodeStorageBucket{Label: cfg.Storage.RuntimeMedia.Bucket, Region: cfg.Storage.RuntimeMedia.Region, S3Endpoint: *endpoint, EndpointType: "E1"}}
		if *missing {
			items = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "pages": 1, "data": items})
	}))
	t.Cleanup(server.Close)
	return server, deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}
}

func TestVideoStorageProjectionRestoresOnlyEnabledRoleAndRejectsMissingStartupInputs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "direct-disabled", "empty-bucket", "empty-region", "empty-prefix", "bad-endpoint", "bad-identity-path", "missing-credential", "unsafe-schema-init", "wrong-db", "wrong-registration-url", "enable-webrtc", "wrong-password-order"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, o, object := videoStorageStartupFixture(t, environment)
				c := videoStorageStartupContainer(object)
				key, value := map[string]string{"direct-disabled": "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "empty-bucket": "VIDEO_CLOUD_BLOB_BUCKET", "empty-region": "VIDEO_CLOUD_BLOB_REGION", "empty-prefix": "VIDEO_CLOUD_BLOB_PREFIX", "bad-endpoint": "VIDEO_CLOUD_BLOB_ENDPOINT", "bad-identity-path": "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_CERT", "unsafe-schema-init": "VIDEO_CLOUD_DB_ENSURE_SCHEMA"}[mode], ""
				if mode == "bad-endpoint" {
					value = "http://private-canary.invalid"
				}
				if mode == "bad-identity-path" {
					value = "/foreign/client.crt"
				}
				if mode == "unsafe-schema-init" {
					value = "true"
				}
				if key != "" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": key, "value": value}})
				}
				if mode == "missing-credential" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "AWS_ACCESS_KEY_ID", "value": "private-canary"}})
				}
				if mode == "wrong-db" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_DB_DSN", "value": "postgres://postgres:$(POSTGRES_PASSWORD)@foreign.example:5432/video_cloud"}})
				}
				if mode == "wrong-registration-url" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL", "value": "https://foreign.example:8443/path"}})
				}
				if mode == "enable-webrtc" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_WEBRTC_SERVICE_ENABLED", "value": "true"}})
				}
				if mode == "wrong-password-order" {
					var remaining []any
					var password any
					for _, raw := range certIssuerObjectList(c["env"]) {
						if deploymentplan.Map(raw)["name"] == "POSTGRES_PASSWORD" {
							password = raw
						} else {
							remaining = append(remaining, raw)
						}
					}
					c["env"] = append(remaining, password)
				}
				err := validateEnvironmentVideoStorageStartup(videoStorageTestDefinitions(object))
				if (err == nil) != (mode == "valid") {
					t.Fatal(mode, err)
				}
				if err != nil && strings.Contains(err.Error(), "private-canary") {
					t.Fatal("private diagnostic leaked")
				}
				values := map[string]string{"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
				if err := projectVideoStorageRenderInputs(cfg, o, values); err != nil {
					t.Fatal(err)
				}
				if values["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"] != "false" || values["VIDEO_CLOUD_BLOB_BUCKET"] != "" {
					t.Fatal("working Core/global settings changed")
				}
				snapshot := environmentDeploymentSnapshot{MediaStorage: o}
				before := snapshot.Digest(cfg)
				snapshot.MediaStorage.Endpoint = "https://changed.example.invalid"
				if before == snapshot.Digest(cfg) {
					t.Fatal("media allocation omitted from input fence")
				}
			})
		}
	}
}

func TestMediaObservationIsReadOnlyStablePrivateAndCannotReplaceAllocation(t *testing.T) {
	cfg, _, _ := videoStorageStartupFixture(t, "dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	endpoint := "isolated-media.example.invalid"
	missing := false
	_, checker := mediaTestProvider(t, cfg, &endpoint, &missing)
	operator := map[string]string{"LINODE_TOKEN": "private-canary"}
	o, err := observeMediaStorageForRender(context.Background(), cfg, store, operator, checker)
	if err != nil {
		t.Fatal(err)
	}
	path, err := privateDeploymentStatePath("dev", environmentMediaObservationPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("observation is not private")
	}
	if _, err := observeMediaStorageForRender(context.Background(), cfg, store, operator, checker); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(first) != string(after) {
		t.Fatal("unchanged GET rewrote stable evidence")
	}
	endpoint = "changed.example.invalid"
	if _, err := observeMediaStorageForRender(context.Background(), cfg, store, operator, checker); err == nil {
		t.Fatal("changed allocation silently replaced")
	}
	after, _ = os.ReadFile(path)
	if string(first) != string(after) {
		t.Fatal("failed observation replaced original evidence")
	}
	missing = true
	if _, err := observeMediaStorageForRender(context.Background(), cfg, store, operator, checker); err == nil {
		t.Fatal("missing bucket was accepted or created")
	}
	if o.EndpointType != "E1" {
		t.Fatal("media introduced an unrelated OTA E3 requirement")
	}
	// Missing files do not bypass ancestor validation or use another directory.
	other := makeIsolatedTestSecretStore(t, "staging")
	cfg.Environment = "staging"
	cfg.Storage.RuntimeMedia.Bucket = "isolated-media-staging"
	cfg.Storage.RuntimeMedia.Prefix = "environments/video-cloud-staging"
	if err := os.Symlink(t.TempDir(), filepath.Join(other.Root, "deployment")); err != nil {
		t.Fatal(err)
	}
	if _, err := readMediaStorageObservation(cfg, other); err == nil {
		t.Fatal("symlink ancestor accepted when observation file absent")
	}
}

func TestVideoStorageCandidateGuardPreservesOriginalRefsAndDoesNotRequireHealthyBaselineOrFuturePod(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "broken-baseline", "create-prereqs", "create-wrong-field", "changed-source-allocation", "changed-source-credential-reference", "changed-secret-owner", "missing-secret-key", "changed-key-bytes", "changed-provider", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, o, object := videoStorageStartupFixture(t, environment)
				store := makeIsolatedTestSecretStore(t, environment)
				endpoint := "isolated-media.example.invalid"
				missing := false
				_, checker := mediaTestProvider(t, cfg, &endpoint, &missing)
				operator := map[string]string{"LINODE_TOKEN": "private-canary"}
				if _, err := observeMediaStorageForRender(context.Background(), cfg, store, operator, checker); err != nil {
					t.Fatal(err)
				}
				old := deploymentplan.Clone(object)
				if mode == "broken-baseline" {
					c := videoStorageStartupContainer(old)
					for _, key := range []string{"VIDEO_CLOUD_BLOB_BUCKET", "VIDEO_CLOUD_BLOB_REGION", "VIDEO_CLOUD_BLOB_PREFIX", "VIDEO_CLOUD_BLOB_ENDPOINT", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"} {
						c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": key, "value": ""}})
					}
				}
				if mode == "changed-source-allocation" {
					c := videoStorageStartupContainer(old)
					c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_BLOB_BUCKET", "value": "original-other-bucket"}})
				}
				if mode == "changed-source-credential-reference" {
					videoStorageStartupContainer(old)["env"] = mergeEnvironmentNamedFields(videoStorageStartupContainer(old)["env"], []any{deploymentplan.Object{"name": "AWS_ACCESS_KEY_ID", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-runtime", "key": "AWS_ACCESS_KEY_ID"}}}})
				}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(object), UID: "original-worker", Action: "update", Desired: object, Baseline: old}}}
				for _, d := range videoStorageTestDefinitions(object)[1:] {
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Action: "retain", Desired: d.Object})
				}
				objects := map[string]deploymentplan.Object{}
				for _, name := range []string{"video-cloud-runtime", environmentVideoStorageObjectStorageSecret, videoStorageServiceIdentitySecretName} {
					id := "Secret/" + plan.Stack + "-video-cloud/" + name
					secret := deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"uid": "original-" + name}, "data": deploymentplan.Object{}}
					for _, key := range []string{"POSTGRES_PASSWORD", "VIDEO_CLOUD_AUTH_SECRET", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "client.crt", "client.key", "server-ca.crt", "clip-private-key.pem"} {
						deploymentplan.Map(secret["data"])[key] = base64.StdEncoding.EncodeToString([]byte("private-canary"))
					}
					objects[id] = secret
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, UID: "original-" + name, Action: "retain", Baseline: deploymentplan.Clone(secret), Desired: deploymentplan.Clone(secret)})
				}
				id := "Secret/" + plan.Stack + "-video-cloud/" + environmentVideoStorageObjectStorageSecret
				if mode == "create-prereqs" || mode == "create-wrong-field" {
					plan.Operation = "create"
					for i := range plan.Resources {
						plan.Resources[i].UID = ""
					}
					if mode == "create-wrong-field" {
						c := videoStorageStartupContainer(object)
						c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_BLOB_BUCKET", "value": "foreign-bucket"}})
					}
				}
				switch mode {
				case "changed-secret-owner":
					deploymentplan.Map(objects[id]["metadata"])["uid"] = "replacement"
				case "missing-secret-key":
					delete(deploymentplan.Map(objects[id]["data"]), "AWS_ACCESS_KEY_ID")
				case "changed-key-bytes":
					deploymentplan.Map(objects[id]["data"])["AWS_ACCESS_KEY_ID"] = base64.StdEncoding.EncodeToString([]byte("different-private-canary"))
				case "changed-provider":
					endpoint = "changed.example.invalid"
				}
				read := func(_ context.Context, id string) (deploymentplan.Object, error) {
					if mode == "create-prereqs" {
						t.Fatal("create qualification required a future resource UID")
					}
					if !strings.HasPrefix(id, "Secret/") {
						t.Fatal("candidate check depended on future workload readiness", id)
					}
					return objects[id], nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				err := checkEnvironmentVideoStorageStartup(ctx, plan, cfg, store, read, operator, checker)
				if (err == nil) != (mode == "valid" || mode == "broken-baseline" || mode == "create-prereqs") {
					t.Fatal(mode, err)
				}
				if err != nil {
					var b *deploymentplan.Blocker
					if !errors.As(err, &b) || b.ID != "deployment.configuration.video-storage-startup" || strings.Contains(err.Error(), "private-canary") {
						t.Fatal("unsafe diagnostic", err)
					}
				}
				if o.Bucket != cfg.Storage.RuntimeMedia.Bucket {
					t.Fatal("fixture used wrong selected bucket")
				}
			})
		}
	}
}

func TestVideoStorageExistingUpgradeSkipsPreparationWritesWhileCreateKeepsExistingPreparation(t *testing.T) {
	for _, operation := range []string{"upgrade", "create"} {
		t.Run(operation, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, "dev")
			c.Configuration.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] = "true"
			preparations := 0
			deps.CheckExecutionAvailable = func(context.Context, deploymentConfig, string) error { return nil }
			deps.PrepareStorage = func(context.Context, deploymentConfig, io.Writer) error { preparations++; return nil }
			stop := &deploymentplan.Blocker{ID: "deployment.test.compile-stop", Cause: "isolated stop before compilation", Next: "finish the isolated preparation test"}
			deps.Compile = func(_ context.Context, _ deploymentConfig, selected string) (*compiledEnvironmentDeployment, error) {
				if selected != operation {
					t.Fatal("operation lost")
				}
				return nil, stop
			}
			var out bytes.Buffer
			err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev", "--operation", operation}, &out, &out, deps)
			if err != exitCode(1) || !strings.Contains(out.String(), stop.ID) {
				t.Fatal("preparation fixture did not reach the selected compiler boundary", err, out.String())
			}
			expected := 0
			if operation == "create" {
				expected = 1
			}
			if preparations != expected || *writes != 0 {
				t.Fatal("existing/create preparation boundary changed", preparations, *writes)
			}
		})
	}
}
