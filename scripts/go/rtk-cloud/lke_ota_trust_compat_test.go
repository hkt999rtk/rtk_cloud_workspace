package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func TestLKECompatibilityArtifactsRetainOTATrustedManifestKeys(t *testing.T) {
	const key = "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"
	const trust = `{"staging-ota":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`
	paths := provisionPaths{EnvRoot: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(paths.EnvRoot, "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSortedEnv(filepath.Join(paths.EnvRoot, "env", "stack.env"), map[string]string{
		"CLOUD_ENV_NAME":        "staging",
		"CLOUD_PROVIDER":        "lke",
		"CLOUD_REGION":          "us-sea",
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"CLOUD_DNS_ROOT_DOMAIN": "example.test",
		key:                     trust,
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := envroot.Load(paths.EnvRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Values[key] != trust {
		t.Fatalf("initial trusted manifest keys = %q", initial.Values[key])
	}
	if err := writeLKECompatibilityArtifacts(paths, initial.Values); err != nil {
		t.Fatal(err)
	}
	reloaded, err := envroot.Load(paths.EnvRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Values[key] != trust {
		t.Fatalf("compatibility rewrite lost trusted manifest keys: %q", reloaded.Values[key])
	}
	for _, workload := range lkeWorkloads(reloaded.Values) {
		if workload.Key != "video-cloud" {
			continue
		}
		manifest := lkeDeploymentManifest(reloaded.Values, workload, nil)
		if !strings.Contains(manifest, "name: "+key+"\n              value: "+strconv.Quote(trust)) {
			t.Fatal("reloaded OTA trust map was not rendered in the video-cloud Deployment")
		}
		return
	}
	t.Fatal("video-cloud workload was not selected")
}
