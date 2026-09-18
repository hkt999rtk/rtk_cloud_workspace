package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGrantStagingProductAccessExecutesValidatedSQL(t *testing.T) {
	const (
		cloudID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		ownerID   = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		userID    = "11111111-1111-4111-8111-111111111111"
		productID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/login" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user":   map[string]string{"id": ownerID},
			"tokens": map[string]string{"access_token": "test-access-token", "refresh_token": "test-refresh-token"},
		})
	}))
	defer server.Close()

	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=video-cloud-staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceUsers("RTK", cloudID, "rtk", "member", []map[string]any{{"email": "member@example.test", "user_id": userID, "password": "test-password"}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.ReplaceBindings("RTK", cloudID, "rtk", "run-1", []bindAssignment{{AssignedEmail: "member@example.test", DeviceID: "device-1", ProductID: productID}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	kubeconfig := filepath.Join(workspace, "kubeconfig.yaml")
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	sqlLog := filepath.Join(workspace, "grant.sql")
	argsLog := filepath.Join(workspace, "kubectl.args")
	databaseURL := base64.StdEncoding.EncodeToString([]byte("postgres://operator:test@postgres.example/rtk_account_manager?sslmode=disable"))
	kubectl := filepath.Join(workspace, "kubectl")
	script := `#!/bin/sh
case " $* " in
  *" get secret account-manager-runtime "*)
    printf '%s' '{"data":{"DATABASE_URL":"` + databaseURL + `"}}'
    ;;
  *" exec -i postgresql-0 "*)
    cat > "$TEST_SQL_LOG"
    printf '%s\n' "$*" > "$TEST_ARGS_LOG"
    ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "owner@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "test-password")
	t.Setenv("CLOUD_ENV_NAME", "")
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", kubeconfig)
	t.Setenv("TEST_SQL_LOG", sqlLog)
	t.Setenv("TEST_ARGS_LOG", argsLog)

	if err := runGrantStagingProductAccess([]string{"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK"}); err != nil {
		t.Fatal(err)
	}
	sqlBody, err := os.ReadFile(sqlLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BEGIN;", cloudID, ownerID, userID, productID, "COMMIT;"} {
		if !strings.Contains(string(sqlBody), want) {
			t.Fatalf("executed SQL missing %q:\n%s", want, sqlBody)
		}
	}
	argsBody, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argsBody), "sh rtk_account_manager") || strings.Contains(string(argsBody), "sh video_cloud") {
		t.Fatalf("kubectl did not target Account Manager database: %s", argsBody)
	}
}

func TestRunGrantStagingProductAccessRejectsUnsafeScope(t *testing.T) {
	if err := runGrantStagingProductAccess(nil); err == nil || !strings.Contains(err.Error(), "--env-root is required") {
		t.Fatalf("missing env root error = %v", err)
	}
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "dev", "runtime")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_ENV_NAME=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGrantStagingProductAccess([]string{"--workspace", workspace, "--env-root", envRoot}); err == nil || !strings.Contains(err.Error(), "restricted to the staging environment") {
		t.Fatalf("dev scope error = %v", err)
	}
}

func TestStagingProductAccessGrantsAreLimitedToBoundUserProducts(t *testing.T) {
	users := map[string]userCredential{
		"one@example.test": {UserID: "11111111-1111-4111-8111-111111111111"},
		"two@example.test": {UserID: "22222222-2222-4222-8222-222222222222"},
	}
	assignments := []bindAssignment{
		{AssignedEmail: "one@example.test", DeviceID: "one-a", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{AssignedEmail: "one@example.test", DeviceID: "one-b", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{AssignedEmail: "two@example.test", DeviceID: "two-a", ProductID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
	}
	grants, err := stagingProductAccessGrants(assignments, users)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("grant count = %d, want 2: %#v", len(grants), grants)
	}
	if grants[0].UserID != users["one@example.test"].UserID || grants[0].ProductID != assignments[0].ProductID {
		t.Fatalf("first grant = %#v", grants[0])
	}
	if grants[1].UserID != users["two@example.test"].UserID || grants[1].ProductID != assignments[2].ProductID {
		t.Fatalf("second grant = %#v", grants[1])
	}
}

func TestStagingProductAccessGrantsRejectIncompleteBindingData(t *testing.T) {
	users := map[string]userCredential{"one@example.test": {UserID: "11111111-1111-4111-8111-111111111111"}}
	for name, assignments := range map[string][]bindAssignment{
		"unknown user":    {{AssignedEmail: "missing@example.test", DeviceID: "one-a", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}},
		"missing Product": {{AssignedEmail: "one@example.test", DeviceID: "one-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := stagingProductAccessGrants(assignments, users); err == nil {
				t.Fatal("expected incomplete staging binding data to fail")
			}
		})
	}
	if _, err := databaseNameFromPostgresURL("postgres://host/not-valid-name!"); err == nil {
		t.Fatal("expected unsafe database name to fail")
	}
}

func TestBuildStagingProductAccessSQLIsTransactionalAndOwnerScoped(t *testing.T) {
	cloud := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	owner := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	grants := []stagingProductAccessGrant{{
		UserID: "11111111-1111-4111-8111-111111111111", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}}
	sql, err := buildStagingProductAccessSQL(cloud, owner, grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"BEGIN;", "ON COMMIT DROP", "role='owner'", "role IN ('admin','member')",
		"brand_cloud_product_admissions", "'owner_invitation'", "ON CONFLICT", "COMMIT;",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("SQL missing %q:\n%s", expected, sql)
		}
	}
	if strings.Contains(strings.ToLower(sql), "password") || strings.Contains(strings.ToLower(sql), "token") {
		t.Fatalf("SQL unexpectedly contains credential material: %s", sql)
	}
}

func TestBuildStagingProductAccessSQLRejectsInvalidIDs(t *testing.T) {
	valid := stagingProductAccessGrant{UserID: "11111111-1111-4111-8111-111111111111", ProductID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	for name, tc := range map[string]struct {
		cloud, owner string
		grants       []stagingProductAccessGrant
	}{
		"Brand Cloud": {"not-a-uuid", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", []stagingProductAccessGrant{valid}},
		"owner":       {"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "not-a-uuid", []stagingProductAccessGrant{valid}},
		"empty":       {"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", nil},
		"grant user":  {"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", []stagingProductAccessGrant{{UserID: "bad", ProductID: valid.ProductID}}},
		"Product":     {"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", []stagingProductAccessGrant{{UserID: valid.UserID, ProductID: "bad"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildStagingProductAccessSQL(tc.cloud, tc.owner, tc.grants); err == nil {
				t.Fatal("expected invalid staging Product access input to fail")
			}
		})
	}
}

func TestStagingAccountManagerDatabaseNameParsing(t *testing.T) {
	for name, rawURL := range map[string]string{
		"plain":   "postgres://user:secret@postgres.example/rtk_account_manager?sslmode=disable",
		"escaped": "postgres://user:secret@postgres.example/rtk%5Faccount%5Fmanager",
	} {
		t.Run(name, func(t *testing.T) {
			databaseName, err := databaseNameFromPostgresURL(rawURL)
			if err != nil || databaseName != "rtk_account_manager" {
				t.Fatalf("database name = %q, err=%v", databaseName, err)
			}
		})
	}
	if _, err := databaseNameFromPostgresURL("postgres://host/not-valid-name!"); err == nil {
		t.Fatal("expected unsafe database name to fail")
	}
}
