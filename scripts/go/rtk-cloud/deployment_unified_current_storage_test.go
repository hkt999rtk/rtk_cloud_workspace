package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQualifiedMediaStorageRechecksCurrentGrantsPrivacyAndReadsWithoutWrites(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, change := range []string{"unchanged", "revoked-key", "public-policy", "read-denied"} {
			t.Run(environment+"/"+change, func(t *testing.T) {
				makeIsolatedTestSecretStore(t, environment)
				target := deploymentStorageTarget{Bucket: "qualified-media-" + environment, Region: "us-sea", Prefix: "environments/video-cloud-" + environment}
				cfg := deploymentConfig{Environment: environment, Storage: deploymentStoragePlan{RuntimeMedia: target}}
				state, reads, mutations := "unchanged", 0, 0
				var server *httptest.Server
				server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						mutations++
						http.Error(w, "unexpected write", http.StatusMethodNotAllowed)
						return
					}
					switch {
					case r.URL.Path == "/v4/regions/us-sea":
						fmt.Fprint(w, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
					case r.URL.Path == "/v4/object-storage/buckets":
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{linodeStorageBucket{Label: target.Bucket, Region: target.Region, S3Endpoint: server.URL, EndpointType: "E1"}}})
					case r.URL.Path == "/v4/object-storage/keys":
						if state == "revoked-key" {
							fmt.Fprint(w, `{"data":[]}`)
						} else {
							fmt.Fprintf(w, `{"data":[{"id":42,"access_key":"private-storage-access-canary","bucket_access":[{"bucket_name":%q,"region":"us-sea","permissions":"read_write"}]}]}`, target.Bucket)
						}
					case r.URL.Query().Has("acl"):
						fmt.Fprint(w, privateStorageACLForTest)
					case r.URL.Query().Has("policy"):
						if state == "public-policy" {
							fmt.Fprint(w, `{"Statement":{"Effect":"Allow","Principal":"*"}}`)
						} else {
							w.WriteHeader(http.StatusNotFound)
						}
					case r.URL.Query().Get("list-type") == "2":
						reads++
						if r.Header.Get("Authorization") == "" || r.URL.Query().Get("prefix") != target.Prefix+"/" {
							t.Error("current media read was unsigned or used another prefix")
						}
						if state == "read-denied" {
							http.Error(w, "private-provider-error-canary", http.StatusForbidden)
						} else {
							fmt.Fprint(w, `<ListBucketResult></ListBucketResult>`)
						}
					default:
						t.Error("unexpected current media read", r.URL.Path)
					}
				}))
				defer server.Close()
				checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
				operator := map[string]string{"LINODE_TOKEN": "private-provider-token-canary", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "private-storage-access-canary", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "private-storage-secret-canary"}
				if err := checkQualifiedEnvironmentMediaStorageAccess(context.Background(), cfg, operator, checker); err != nil || reads != 1 {
					t.Fatal("original media access could not be qualified", err, reads)
				}
				state = change
				err := checkQualifiedEnvironmentMediaStorageAccess(context.Background(), cfg, operator, checker)
				if (err == nil) != (change == "unchanged") || mutations != 0 || err != nil && strings.Contains(err.Error(), "private-") {
					t.Fatal("current media grants/privacy/read change was cached, mutated or exposed", err, mutations)
				}
				if _, err := readDeploymentStorageState(environment, "storage-preflight.json"); err == nil {
					t.Fatal("current media access refresh fabricated a write-validation receipt")
				}
			})
		}
	}
}
