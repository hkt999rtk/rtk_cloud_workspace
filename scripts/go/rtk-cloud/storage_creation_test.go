package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageBootstrapCannotCreateLegacyNamingException(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("attempted to create noncanonical bucket: %s", r.Method)
		}
		switch r.URL.Path {
		case "/v4/regions/us-sea":
			fmt.Fprint(w, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
		case "/v4/object-storage/buckets":
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	for _, purpose := range []string{"media", "ota", "artifacts"} {
		t.Run(purpose, func(t *testing.T) {
			target := deploymentStorageTarget{Bucket: "rtk-video-prod-us-west", Region: "us-sea"}
			cfg := deploymentConfig{Environment: "prod", Storage: deploymentStoragePlan{RuntimeMedia: target, OTAFirmware: target, OTAMode: "dedicated", ReleaseArtifacts: target}}
			bootstrap := checker.bootstrapRuntimeStorage
			if purpose == "artifacts" {
				bootstrap = checker.bootstrapArtifactStorage
			} else if purpose == "ota" {
				bootstrap = checker.bootstrapOTAStorage
			}
			if err := bootstrap(cfg, map[string]string{"LINODE_TOKEN": "token"}, ""); err == nil || !strings.Contains(err.Error(), "exceptions only permit existing buckets") {
				t.Fatalf("bootstrap error = %v", err)
			}
		})
	}
}
