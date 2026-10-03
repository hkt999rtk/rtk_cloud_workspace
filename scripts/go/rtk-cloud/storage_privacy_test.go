package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const privateStorageACLForTest = `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>owner</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`

func TestStoragePrivacyACL(t *testing.T) {
	for _, body := range []string{
		privateStorageACLForTest,
		strings.Replace(privateStorageACLForTest, "<ID>owner</ID></Grantee>", "<ID>explicit-private-account</ID></Grantee>", 1),
	} {
		if err := validatePrivateStorageACL([]byte(body)); err != nil {
			t.Fatalf("private ACL rejected: %v", err)
		}
	}
	for _, body := range []string{
		`<AccessControlPolicy/>`, `<ListBucketResult/>`, `not xml`,
		strings.Replace(privateStorageACLForTest, "<ID>owner</ID></Grantee>", "<URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee>", 1),
		strings.Replace(privateStorageACLForTest, "<ID>owner</ID></Grantee>", "<URI>http://acs.amazonaws.com/groups/global/AuthenticatedUsers</URI></Grantee>", 1),
		strings.Replace(privateStorageACLForTest, "<ID>owner</ID></Grantee>", "<ID>*</ID></Grantee>", 1),
	} {
		if err := validatePrivateStorageACL([]byte(body)); err == nil {
			t.Fatal("public or unresolved ACL accepted")
		}
	}
}

func TestStoragePrivacyPolicyPreservesLimitedKeyGrants(t *testing.T) {
	for _, body := range []string{
		`{"Statement":[]}`,
		`{"Statement":{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam:::user/limited-key"},"Action":"s3:*","Resource":"arn:aws:s3:::private/*"}}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/writer","arn:aws:iam:::user/reader"]}},{"Effect":"Deny","Principal":"*"}]}`,
	} {
		if err := validatePrivateStoragePolicy([]byte(body)); err != nil {
			t.Fatalf("private policy rejected: %v", err)
		}
	}
	for _, body := range []string{
		`{}`, `not json`, `{"Statement":42}`, `{"Statement":null}`,
		`{"Statement":{"Effect":"Allow","Principal":"*"}}`,
		`{"Statement":{"Effect":"Allow","Principal":{"AWS":["private","*"]}}}`,
		`{"Statement":{"Effect":"Allow","NotPrincipal":{"AWS":"private"}}}`,
		`{"Statement":{"Effect":"Allow","Principal":{"Unknown":"private"}}}`,
		`{"Statement":{"Effect":"Allow"}}`,
		`{"Statement":{"Effect":"Allow","Principal":"*","Condition":{"IpAddress":{"aws:SourceIp":"192.0.2.0/24"}}}}`,
	} {
		if err := validatePrivateStoragePolicy([]byte(body)); err == nil {
			t.Fatal("public or unresolved policy accepted")
		}
	}
}

func TestStoragePrivacyRequestsAreReadOnlyAndFailClosed(t *testing.T) {
	for _, test := range []struct {
		name       string
		aclStatus  int
		policyCode int
		policy     string
		wantError  bool
	}{
		{"absent policy", 200, 404, "", false},
		{"limited key policy", 200, 200, `{"Statement":{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam:::user/limited-key"}}}`, false},
		{"unreadable ACL", 403, 404, "", true},
		{"unreadable policy", 200, 403, "", true},
		{"invalid policy", 200, 200, "{}", true},
		{"public policy", 200, 200, `{"Statement":{"Effect":"Allow","Principal":"*"}}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("privacy check attempted mutation %s", r.Method)
				}
				if r.URL.Query().Has("acl") {
					w.WriteHeader(test.aclStatus)
					fmt.Fprint(w, privateStorageACLForTest)
				} else if r.URL.Query().Has("policy") {
					w.WriteHeader(test.policyCode)
					fmt.Fprint(w, test.policy)
				} else {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			checker := deploymentCredentialChecker{client: server.Client()}
			err := checker.validateStoragePrivacy(provisionObjectStore{bucket: "bucket", endpoint: server.URL, region: "us-sea", accessKey: "access", secretKey: "secret"})
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestStorageBootstrapPrivacyFailureDoesNotWriteOrRotate(t *testing.T) {
	for _, purpose := range []string{"media", "artifacts", "ota", "media generic", "artifacts generic", "media readonly", "artifacts readonly", "ota readonly"} {
		t.Run(purpose, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unsafe mutation after privacy failure: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected mutation", 500)
					return
				}
				switch {
				case r.URL.Path == "/v4/regions/us-sea":
					fmt.Fprint(w, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
				case r.URL.Path == "/v4/object-storage/buckets":
					fmt.Fprintf(w, `{"data":[{"label":"bucket","region":"us-sea","endpoint_type":"E3","s3_endpoint":%q}]}`, server.URL)
				case r.URL.Path == "/v4/object-storage/keys":
					fmt.Fprint(w, `{"data":[{"id":42,"access_key":"access","bucket_access":[{"bucket_name":"bucket","region":"us-sea","permissions":"read_write"}]}]}`)
				case r.URL.Query().Has("acl"):
					fmt.Fprint(w, privateStorageACLForTest)
				case r.URL.Query().Has("policy"):
					fmt.Fprint(w, `{"Statement":{"Effect":"Allow","Principal":"*"}}`)
				default:
					t.Errorf("request after privacy failure: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			target := deploymentStorageTarget{Bucket: "bucket", Region: "us-sea", Prefix: "releases"}
			cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: target, ReleaseArtifacts: target, OTAFirmware: target, OTAMode: "dedicated"}}
			values := map[string]string{"LINODE_TOKEN": "token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "secret", "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID": "access", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": "secret", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "secret"}
			if strings.HasSuffix(purpose, " generic") {
				values = map[string]string{"LINODE_TOKEN": "token", "LINODE_OBJ_ACCESS_KEY_ID": "access", "LINODE_OBJ_SECRET_ACCESS_KEY": "secret"}
			}
			checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4", readOnly: strings.HasSuffix(purpose, " readonly")}
			bootstrap := checker.bootstrapRuntimeStorage
			if strings.HasPrefix(purpose, "artifacts") {
				bootstrap = checker.bootstrapArtifactStorage
			} else if strings.HasPrefix(purpose, "ota") {
				bootstrap = checker.bootstrapOTAStorage
			}
			if err := bootstrap(cfg, values, filepath.Join(t.TempDir(), "candidate.env")); err == nil || !strings.Contains(err.Error(), "public access") {
				t.Fatalf("privacy bootstrap error = %v", err)
			}
			entries, _ := os.ReadDir(cfg.RuntimeRoot)
			if len(entries) != 0 {
				t.Fatal("privacy failure produced a validation receipt")
			}
		})
	}
}

func TestStorageBootstrapPreservesNewKeyBeforePrivacyFailure(t *testing.T) {
	for _, purpose := range []string{"media", "artifacts", "ota"} {
		t.Run(purpose, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "private", "candidate.env")
			keyPrefix := map[string]string{"media": "LINODE_MEDIA_OBJ_", "artifacts": "LINODE_ARTIFACT_OBJ_", "ota": "LINODE_OTA_OBJ_"}[purpose]
			created := 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v4/object-storage/keys":
					created++
					fmt.Fprint(w, `{"access_key":"new-access","secret_key":"new-secret"}`)
				case r.Method != http.MethodGet:
					t.Errorf("object mutation before privacy validation: %s %s", r.Method, r.URL.Path)
				case r.URL.Path == "/v4/regions/us-sea":
					fmt.Fprint(w, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
				case r.URL.Path == "/v4/object-storage/buckets":
					fmt.Fprintf(w, `{"data":[{"label":"bucket","region":"us-sea","endpoint_type":"E3","s3_endpoint":%q}]}`, server.URL)
				case r.URL.Query().Has("acl"):
					data, err := os.ReadFile(candidate)
					if err != nil || !strings.Contains(string(data), keyPrefix+"ACCESS_KEY_ID=new-access") || !strings.Contains(string(data), keyPrefix+"SECRET_ACCESS_KEY=new-secret") {
						t.Error("new key was not persisted before privacy validation")
					}
					fmt.Fprint(w, privateStorageACLForTest)
				case r.URL.Query().Has("policy"):
					fmt.Fprint(w, `{"Statement":{"Effect":"Allow","Principal":"*"}}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			target := deploymentStorageTarget{Bucket: "bucket", Region: "us-sea", Prefix: "releases"}
			cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: target, ReleaseArtifacts: target, OTAFirmware: target, OTAMode: "dedicated"}}
			checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
			bootstrap := checker.bootstrapRuntimeStorage
			if purpose == "artifacts" {
				bootstrap = checker.bootstrapArtifactStorage
			} else if purpose == "ota" {
				bootstrap = checker.bootstrapOTAStorage
			}
			if err := bootstrap(cfg, map[string]string{"LINODE_TOKEN": "token"}, candidate); err == nil || !strings.Contains(err.Error(), "public access") {
				t.Fatalf("bootstrap privacy error = %v", err)
			}
			if created != 1 {
				t.Fatalf("issued %d keys, want exactly one recoverable key", created)
			}
			for path, mode := range map[string]os.FileMode{candidate: 0o600, filepath.Dir(candidate): 0o700} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("candidate path permissions: %v", err)
				}
			}
			entries, _ := os.ReadDir(cfg.RuntimeRoot)
			if len(entries) != 0 {
				t.Fatal("privacy failure produced a validation receipt")
			}
		})
	}
}
