package main

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageLifecyclePlanReadOnlyAndPreservesExistingReadback(t *testing.T) {
	existing := `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>owner-rule</ID><Status>Disabled</Status><Filter><Prefix>custom/</Prefix></Filter><Expiration><Days>180</Days></Expiration></Rule></LifecycleConfiguration>`
	requests := 0
	c := defaultDeploymentCredentialChecker()
	c.linodeAPIRoot = "https://api.example.invalid/v4"
	c.client = &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != http.MethodGet {
			t.Fatalf("planner attempted cloud mutation: %s", r.Method)
		}
		body := existing
		if r.URL.Host == "api.example.invalid" {
			if r.URL.Path != "/v4/object-storage/buckets" {
				t.Fatalf("unexpected API request: %s", r.URL)
			}
			body = `{"data":[{"label":"rtk-cloud-shared-artifacts-us-sea","region":"us-sea","s3_endpoint":"us-sea-1.linodeobjects.com"}]}`
		} else if !r.URL.Query().Has("lifecycle") || r.URL.Path != "/rtk-cloud-shared-artifacts-us-sea" {
			t.Fatalf("unexpected object request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	cfg := deploymentConfig{Environment: "dev", Storage: deploymentStoragePlan{ReleaseArtifacts: deploymentStorageTarget{Bucket: "rtk-cloud-shared-artifacts-us-sea", Region: "us-sea", Prefix: "releases"}}}
	values := map[string]string{"LINODE_TOKEN": "token", "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID": "private-access", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": "private-secret"}
	plan, err := c.planStorageLifecycle(cfg, values, "artifacts", 9)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || plan.CloudMutation || plan.ReplacementConfiguration || plan.ExistingXML != existing || plan.ExistingSHA256 != provisionHexSHA256([]byte(existing)) {
		t.Fatalf("readback changed or unsafe plan: %+v", plan)
	}
	var proposed storageLifecycleConfiguration
	if err := xml.Unmarshal([]byte(plan.ProposedRulesXML), &proposed); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"ci/": 30, "tmp/": 7, "reports/": 30, "releases/__rtk_cloud_validation__/": 1}
	if len(proposed.Rules) != len(want) {
		t.Fatal(proposed)
	}
	for _, rule := range proposed.Rules {
		if rule.Filter.Prefix == "" || want[rule.Filter.Prefix] != rule.Expiration.Days || rule.Abort == nil || rule.Abort.Days != 9 {
			t.Fatalf("unsafe prefix rule: %+v", rule)
		}
	}
	if !strings.Contains(strings.Join(plan.Prerequisites, " "), "held evidence") {
		t.Fatal("missing held-evidence review gate")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-access", "private-secret", `"token"`} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plan exposed credential %q", secret)
		}
	}
}

func TestStorageLifecycleReadFailuresStayUnresolved(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		c := defaultDeploymentCredentialChecker()
		c.linodeAPIRoot = "https://api.example.invalid/v4"
		c.client = &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected cloud mutation: %s", r.Method)
			}
			code, body := status, `<Error><Code>AccessDenied</Code></Error>`
			if r.URL.Host == "api.example.invalid" {
				code, body = 200, `{"data":[{"label":"rtk-cloud-dev-runtime-us-sea","region":"us-sea","s3_endpoint":"us-sea-1.linodeobjects.com"}]}`
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		cfg := deploymentConfig{Environment: "dev", Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea", Prefix: "environments/video-cloud-dev"}}}
		plan, err := c.planStorageLifecycle(cfg, map[string]string{"LINODE_TOKEN": "token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "secret"}, "media", 0)
		if err != nil || !strings.HasPrefix(plan.ExistingStatus, "unresolved lifecycle readback:") || plan.ExistingXML != "" {
			t.Fatalf("failed readback treated as absent: %+v %v", plan, err)
		}
		if strings.Contains(plan.ProposedRulesXML, "AbortIncompleteMultipartUpload") {
			t.Fatal("unreviewed multipart abort default")
		}
		var proposed storageLifecycleConfiguration
		if err := xml.Unmarshal([]byte(plan.ProposedRulesXML), &proposed); err != nil || len(proposed.Rules) != 1 || proposed.Rules[0].Filter.Prefix != "environments/video-cloud-dev/__rtk_cloud_validation__/" {
			t.Fatalf("media plan is not limited to its canary: %+v %v", proposed, err)
		}
	}
}

func TestStorageLifecycleCLIRejectsUnsafeArgumentsBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--environment", "dev", "--purpose", "media", "--out", "unused", "extra"},
		{"--environment", "dev", "--purpose", "artifacts", "--out", "unused", "--abort-incomplete-days", "0"},
		{"--environment", "dev", "--purpose", "artifacts", "--out", "unused", "--abort-incomplete-days", "366"},
		{"--environment", "dev", "--purpose", "backup", "--out", "unused"},
		{"--environment", "dev", "--purpose", "artifacts", "--out", "unused", "--apply"},
	} {
		if err := runObjectStorageLifecyclePlan(args); err == nil {
			t.Fatalf("unsafe arguments accepted: %v", args)
		}
	}
}

func TestStorageLifecycleCLIPrivatePlanUsesSelectedProfileWithoutMutation(t *testing.T) {
	for _, useCandidate := range []bool{false, true} {
		t.Run(map[bool]string{false: "active profile", true: "isolated candidate"}[useCandidate], func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
			t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", "")
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", "https://api.example.invalid/v4")
			t.Setenv("LINODE_TOKEN", "ambient-token-must-not-be-used")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\n")
			store, err := newSecretStore("", "dev")
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"LINODE_TOKEN": "dev-token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "active-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "active-secret"}
			for key, value := range values {
				if err := store.write(filepath.Join("operator", "env", key), []byte(value), false); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(t.TempDir(), "review", "lifecycle.json")
			args := []string{"--environment", "dev", "--purpose", "media", "--out", output}
			expectedAccess := "active-access"
			if useCandidate {
				candidate := filepath.Join(t.TempDir(), "candidate.env")
				values["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] = "dev"
				values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"] = "candidate-access", "candidate-secret"
				if err := writeSortedEnv(candidate, values, 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--candidate-profile", candidate)
				expectedAccess = "candidate-access"
			}
			existing := `<LifecycleConfiguration><Rule><ID>owner-managed</ID><Status>Disabled</Status><Filter><Prefix>private-customer-filter/</Prefix></Filter><Expiration><Days>120</Days></Expiration></Rule></LifecycleConfiguration>`
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			requests := 0
			http.DefaultTransport = consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != http.MethodGet {
					t.Fatalf("planner attempted mutation: %s", r.Method)
				}
				body := existing
				if r.URL.Host == "api.example.invalid" {
					if r.URL.Path != "/v4/object-storage/buckets" || r.Header.Get("Authorization") != "Bearer dev-token" {
						t.Fatal("planner used an unselected account/profile")
					}
					body = `{"data":[{"label":"rtk-cloud-dev-runtime-us-sea","region":"us-sea","s3_endpoint":"https://objects.example.invalid"}]}`
				} else if r.URL.Host != "objects.example.invalid" || r.URL.Path != "/rtk-cloud-dev-runtime-us-sea" || !r.URL.Query().Has("lifecycle") || !strings.Contains(r.Header.Get("Authorization"), "Credential="+expectedAccess+"/") {
					t.Fatal("lifecycle read escaped exact bucket or selected scoped key")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			stdout, stderr, err := captureOutput(func() error { return runObjectStorageLifecyclePlan(args) })
			if err != nil {
				t.Fatal(err)
			}
			if stdout != "Lifecycle review plan saved: "+output+"\n" || stderr != "" {
				t.Fatalf("private plan data printed: %q %q", stdout, stderr)
			}
			body, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(output)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("lifecycle plan is not mode 0600")
			}
			var plan storageLifecyclePlan
			if err := json.Unmarshal(body, &plan); err != nil {
				t.Fatal(err)
			}
			if requests != 2 || plan.Environment != "dev" || plan.Target.Bucket != "rtk-cloud-dev-runtime-us-sea" || plan.Target.Region != "us-sea" || plan.CloudMutation || plan.ReplacementConfiguration || plan.ExistingXML != existing || plan.ExistingSHA256 != provisionHexSHA256([]byte(existing)) {
				t.Fatalf("incorrect lifecycle plan: %+v", plan)
			}
			var proposed storageLifecycleConfiguration
			if err := xml.Unmarshal([]byte(plan.ProposedRulesXML), &proposed); err != nil {
				t.Fatal(err)
			}
			if len(proposed.Rules) != 1 || proposed.Rules[0].Filter.Prefix != "environments/video-cloud-dev/__rtk_cloud_validation__/" || proposed.Rules[0].Expiration.Days != 1 || proposed.Rules[0].Abort != nil {
				t.Fatalf("media plan expires data outside its canary: %+v", proposed)
			}
			for _, secret := range []string{"dev-token", "active-access", "active-secret", "candidate-access", "candidate-secret", "ambient-token-must-not-be-used"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("plan exposed credential %q", secret)
				}
			}
			active, err := store.readOperator()
			if err != nil || active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "active-access" || active["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"] != "active-secret" {
				t.Fatal("review planning promoted or changed active credentials")
			}
		})
	}
}

func TestStorageLifecycleCLIRejectsMismatchedOrMissingProfilesBeforeNetwork(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", "")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid profile reached network")
		return nil, nil
	})
	for _, tc := range []struct {
		name, environment, marker string
		missing                   bool
	}{
		{name: "wrong candidate environment", environment: "dev", marker: "staging"},
		{name: "unmarked candidate", environment: "dev"},
		{name: "missing profile", environment: "dev", missing: true},
		{name: "unregistered environment", environment: "unknown", marker: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "candidate.env")
			if !tc.missing {
				if err := writeSortedEnv(candidate, map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": tc.marker, "LINODE_TOKEN": "private-token"}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(t.TempDir(), "plan.json")
			if err := runObjectStorageLifecyclePlan([]string{"--environment", tc.environment, "--purpose", "media", "--candidate-profile", candidate, "--out", output}); err == nil {
				t.Fatal("invalid environment/profile accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("invalid profile produced a plan")
			}
		})
	}
}

func TestStorageLifecyclePlannerKeepsMissingIdentityAndInvalidReadbackUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name, inventory, lifecycle, want string
		status                           int
		omitKey                          bool
	}{
		{name: "missing bucket", inventory: `{"data":[]}`, status: 200, want: "unresolved bucket identity:"},
		{name: "provider failure", inventory: `{"errors":[]}`, status: 503, want: "unresolved bucket identity:"},
		{name: "missing scoped credentials", inventory: `{"data":[{"label":"ota-bucket","region":"us-sea","s3_endpoint":"https://objects.example.invalid"}]}`, status: 200, omitKey: true, want: "unresolved: purpose-scoped credentials are unavailable"},
		{name: "wrong XML root", inventory: `{"data":[{"label":"ota-bucket","region":"us-sea","s3_endpoint":"https://objects.example.invalid"}]}`, lifecycle: `<Error><Code>AccessDenied</Code></Error>`, status: 200, want: "unresolved: invalid lifecycle XML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultDeploymentCredentialChecker()
			c.linodeAPIRoot = "https://api.example.invalid/v4"
			requests := 0
			c.client = &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != http.MethodGet {
					t.Fatal("planner attempted mutation")
				}
				body, status := tc.inventory, tc.status
				if r.URL.Host != "api.example.invalid" {
					body, status = tc.lifecycle, 200
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			cfg := deploymentConfig{Environment: "dev", Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Bucket: "ota-bucket", Region: "us-sea", Prefix: "environments/video-cloud-dev"}}}
			values := map[string]string{"LINODE_TOKEN": "token"}
			if !tc.omitKey {
				values["LINODE_OTA_OBJ_ACCESS_KEY_ID"], values["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"] = "ota-access", "ota-secret"
			}
			plan, err := c.planStorageLifecycle(cfg, values, "ota", 0)
			if err != nil || !strings.HasPrefix(plan.ExistingStatus, tc.want) || plan.ExistingXML != "" || plan.ExistingSHA256 != "" || plan.CloudMutation {
				t.Fatalf("unresolved storage mistaken for verified configuration: %+v %v", plan, err)
			}
			var proposed storageLifecycleConfiguration
			if err := xml.Unmarshal([]byte(plan.ProposedRulesXML), &proposed); err != nil || len(proposed.Rules) != 1 || proposed.Rules[0].Filter.Prefix != "environments/video-cloud-dev/__rtk_cloud_validation__/" {
				t.Fatal("OTA plan did not stay within exact disposable canary prefix")
			}
			if tc.omitKey && requests != 1 {
				t.Fatal("missing scoped credentials were replaced by a fallback")
			}
		})
	}
}

func TestStorageLifecyclePlannerRejectsUnqualifiedPurposeAndMissingToken(t *testing.T) {
	checker := defaultDeploymentCredentialChecker()
	for _, purpose := range []string{"ota", "backup"} {
		if _, err := checker.planStorageLifecycle(deploymentConfig{}, nil, purpose, 0); err == nil {
			t.Fatalf("unqualified purpose accepted: %s", purpose)
		}
	}
	if _, err := checker.planStorageLifecycle(deploymentConfig{Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Prefix: "environments/dev"}}}, nil, "media", 0); err == nil || !strings.Contains(err.Error(), "LINODE_TOKEN") {
		t.Fatalf("missing account token accepted: %v", err)
	}
	if _, err := plannedStorageLifecycleXML("media", "environments/dev", -1); err == nil {
		t.Fatal("negative abort period accepted")
	}
	if _, err := plannedStorageLifecycleXML("backup", "", 0); err == nil {
		t.Fatal("undefined backup expiry accepted")
	}
}
