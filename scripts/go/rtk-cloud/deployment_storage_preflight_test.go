package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPreflightMediaBucketEnsuresExistenceWithoutCutover(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		exists, wrongRegion, cutover  bool
		inventoryStatus, createStatus int
		readbackMissing               bool
		wantCreated                   bool
		wantPosts                     int
		wantError                     bool
	}{
		{name: "existing", exists: true},
		{name: "missing", wantCreated: true, wantPosts: 1},
		{name: "wrong-region", exists: true, wrongRegion: true, wantError: true},
		{name: "unauthorized", inventoryStatus: 403, wantError: true},
		{name: "provider-unavailable", inventoryStatus: 503, wantError: true},
		{name: "create-denied", createStatus: 403, wantPosts: 1, wantError: true},
		{name: "readback-incomplete", readbackMissing: true, wantPosts: 1, wantError: true},
		{name: "cutover-required", cutover: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
			exists, posts := tc.exists, 0
			cfg := deploymentConfig{Environment: "dev", Adapter: "lke", Storage: deploymentStoragePlan{RuntimeMediaCutoverRequired: tc.cutover, RuntimeMedia: deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea"}}}
			checker := providerTestChecker(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v4/object-storage/buckets":
					if tc.inventoryStatus != 0 {
						return providerTestResponse(tc.inventoryStatus, `{}`), nil
					}
					if !exists {
						return providerTestResponse(200, `{"data":[],"page":1,"pages":1}`), nil
					}
					region := "us-sea"
					if tc.wrongRegion {
						region = "us-lax"
					}
					return providerTestResponse(200, fmt.Sprintf(`{"data":[{"label":"rtk-cloud-dev-runtime-us-sea","region":%q,"s3_endpoint":"us-sea-1.linodeobjects.com"}],"page":1,"pages":1}`, region)), nil
				case r.Method == http.MethodGet && r.URL.Path == "/v4/regions/us-sea":
					return providerTestResponse(200, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`), nil
				case r.Method == http.MethodPost && r.URL.Path == "/v4/object-storage/buckets":
					posts++
					var payload map[string]string
					if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["label"] != cfg.Storage.RuntimeMedia.Bucket || payload["region"] != "us-sea" || len(payload) != 2 {
						t.Fatal("creation target differs from configuration")
					}
					if tc.createStatus != 0 {
						return providerTestResponse(tc.createStatus, `{}`), nil
					}
					exists = !tc.readbackMissing
					return providerTestResponse(201, `{}`), nil
				default:
					t.Fatalf("unexpected mutation or request: %s %s", r.Method, r.URL.Path)
					return nil, errors.New("unexpected request")
				}
			})
			created, err := checker.ensurePreflightMediaBucket(cfg, "fixture-token")
			if (err != nil) != tc.wantError || created != tc.wantCreated || posts != tc.wantPosts {
				t.Fatalf("created=%t posts=%d err=%v", created, posts, err)
			}
		})
	}
}

func TestPreflightMediaBucketRunsIndependentlyOfOtherPrerequisiteFailures(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	resolve := deps.resolve
	deps.resolve = func(workspace, environment, envRoot string) (deploymentConfig, error) {
		cfg, err := resolve(workspace, environment, envRoot)
		cfg.Adapter = "lke"
		cfg.Storage.RuntimeMedia = deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea"}
		cfg.Storage.RuntimeMediaCutoverRequired = false
		return cfg, err
	}
	calls := 0
	deps.bucket = func(context.Context, deploymentConfig, io.Writer) error { calls++; return nil }
	deps.preflight = func(context.Context, deploymentConfig, io.Writer) error {
		return errors.New("another prerequisite failed")
	}
	var out bytes.Buffer
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "dev", "--phase", "pre-deploy"}, &out, &out, deps)
	if err == nil || calls != 1 || !strings.Contains(out.String(), "[OK] provider.storage.media.bucket") {
		t.Fatalf("bucket calls=%d err=%v output=%s", calls, err, out.String())
	}
	if defaultDeploymentCheckDependencies().bucket == nil {
		t.Fatal("public preflight bucket preparation is not wired")
	}
}
