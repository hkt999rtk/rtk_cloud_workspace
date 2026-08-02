package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const testDataSchemaVersion = "rtk-cloud-workspace-test-data/v1"

type testDataStore struct {
	DB   *sql.DB
	Path string
}

type testDataDeviceCredential struct {
	DeviceID                          string
	CertPEM                           string
	KeyPEM                            string
	ChainPEM                          string
	BundlePEM                         string
	CSRPEM                            string
	MetadataJSON                      string
	FactoryEnrollRequestJSON          string
	FactoryEnrollResponseRedactedJSON string
}

type legacyImportSummary struct {
	Database          string         `json:"database"`
	Users             int            `json:"users"`
	Devices           int            `json:"devices"`
	Bindings          int            `json:"bindings"`
	DeviceMix         map[string]int `json:"device_mix"`
	MissingDeviceKeys int            `json:"missing_device_keys"`
}

type legacyCleanupPlanResult struct {
	Files []string `json:"files"`
	Dirs  []string `json:"dirs"`
	Count int      `json:"count"`
}

type testDataCoverage struct {
	Users      int
	Devices    int
	Bindings   int
	BoundUsers int
	DeviceMix  map[string]int
}

func testDataDBPath(envRoot, brandname string) string {
	return filepath.Join(envRoot, "artifacts", "test-data", brandSlug(brandname)+"-test-data.sqlite")
}

func runTestData(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: test-data import-legacy|inspect|cleanup-legacy")
	}
	switch args[0] {
	case "import-legacy":
		fs := flag.NewFlagSet("test-data import-legacy", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		workspaceFlag := fs.String("workspace", "", "workspace")
		envRootFlag := fs.String("env-root", "", "environment root")
		brandname := fs.String("brandname", "RTK", "brand name")
		latestOnly := fs.Bool("latest-only", false, "import latest legacy artifacts only")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		envRoot, err := resolveEnvRootFromCommandFlags(*workspaceFlag, *envRootFlag)
		if err != nil {
			return err
		}
		summary, err := importLegacyTestData(envRoot, *brandname, *latestOnly)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(summary)
	case "inspect":
		fs := flag.NewFlagSet("test-data inspect", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		workspaceFlag := fs.String("workspace", "", "workspace")
		envRootFlag := fs.String("env-root", "", "environment root")
		brandname := fs.String("brandname", "RTK", "brand name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		envRoot, err := resolveEnvRootFromCommandFlags(*workspaceFlag, *envRootFlag)
		if err != nil {
			return err
		}
		summary, err := inspectTestData(envRoot, *brandname)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(summary)
	case "cleanup-legacy":
		fs := flag.NewFlagSet("test-data cleanup-legacy", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		workspaceFlag := fs.String("workspace", "", "workspace")
		envRootFlag := fs.String("env-root", "", "environment root")
		brandname := fs.String("brandname", "RTK", "brand name")
		dryRun := fs.Bool("dry-run", false, "dry run")
		confirm := fs.String("confirm", "", "confirmation brand name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *confirm != *brandname {
			return fmt.Errorf("--confirm must match --brandname (%s)", *brandname)
		}
		envRoot, err := resolveEnvRootFromCommandFlags(*workspaceFlag, *envRootFlag)
		if err != nil {
			return err
		}
		plan, err := legacyCleanupPlan(envRoot, *brandname)
		if err != nil {
			return err
		}
		action := "deleted"
		if *dryRun {
			action = "dry_run"
		} else {
			for _, file := range plan.Files {
				if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			for i := len(plan.Dirs) - 1; i >= 0; i-- {
				_ = os.RemoveAll(plan.Dirs[i])
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": action, "brandname": *brandname, "count": plan.Count, "files": plan.Files, "dirs": plan.Dirs})
	default:
		return fmt.Errorf("unknown test-data subcommand %q", args[0])
	}
}

func resolveEnvRootFromCommandFlags(workspaceFlag, envRootFlag string) (string, error) {
	if strings.TrimSpace(envRootFlag) == "" {
		return "", errors.New("--env-root is required")
	}
	workspace := workspaceFlag
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return "", err
		}
	}
	return resolveEnvRoot(workspace, envRootFlag)
}

func openTestDataStore(envRoot, brandname string) (*testDataStore, error) {
	path := testDataDBPath(envRoot, brandname)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &testDataStore{DB: db, Path: path}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *testDataStore) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *testDataStore) init() error {
	stmts := []string{
		`pragma journal_mode = wal`,
		`create table if not exists metadata (key text primary key, value text not null)`,
		`create table if not exists runs (
			brandname text not null,
			run_id text not null,
			kind text not null,
			created_at text not null,
			summary_json text not null,
			primary key (brandname, run_id, kind)
		)`,
		`create table if not exists users (
			brandname text not null,
			email text not null,
			brand_cloud_id text,
			tenant_slug text,
			role text,
			password text,
			tokens_json text,
			app_credentials_json text,
			app_certificate_json text,
			body_json text not null,
			updated_at text not null,
			primary key (brandname, email)
		)`,
		`create table if not exists devices (
			brandname text not null,
			device_id text not null,
			run_id text,
			device_type text not null,
			display_name text,
			category text,
			mqtt_capability text,
			service_options_json text not null,
			model text,
			body_json text not null,
			updated_at text not null,
			primary key (brandname, device_id)
		)`,
		`create table if not exists device_credentials (
			brandname text not null,
			device_id text not null,
			cert_pem text,
			key_pem text,
			chain_pem text,
			bundle_pem text,
			csr_pem text,
			metadata_json text,
			factory_enroll_request_json text,
			factory_enroll_response_redacted_json text,
			updated_at text not null,
			primary key (brandname, device_id)
		)`,
		`create table if not exists device_bindings (
			brandname text not null,
			device_id text not null,
			run_id text,
			brand_cloud_id text,
			tenant_slug text,
			assignment_index integer not null,
			assigned_email text not null,
			device_type text not null,
			category text,
			service_options_json text not null,
			account_device_id text,
			operation_id text,
			status text,
			body_json text not null,
			updated_at text not null,
			primary key (brandname, device_id)
		)`,
		`create table if not exists device_provisioning (
			brandname text not null,
			device_id text not null,
			account_device_id text,
			operation_id text,
			status text,
			detail_json text,
			updated_at text not null,
			primary key (brandname, device_id)
		)`,
		`create index if not exists idx_test_data_users_brand on users(brandname)`,
		`create index if not exists idx_test_data_devices_brand_type on devices(brandname, device_type)`,
		`create index if not exists idx_test_data_bindings_brand_email on device_bindings(brandname, assigned_email)`,
		`create index if not exists idx_test_data_bindings_run on device_bindings(brandname, run_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.DB.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := s.DB.Exec(`insert into metadata(key, value) values('schema_version', ?) on conflict(key) do update set value = excluded.value`, testDataSchemaVersion); err != nil {
		return err
	}
	return nil
}

func (s *testDataStore) ReplaceUsers(brandname, brandCloudID, tenantSlug, role string, users []map[string]any) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from users where brandname = ? and coalesce(role, '') = ?`, brandname, role); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into users(brandname, email, brand_cloud_id, tenant_slug, role, password, tokens_json, app_credentials_json, app_certificate_json, body_json, updated_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, user := range users {
		email := strings.TrimSpace(stringValue(user["email"]))
		if email == "" {
			return fmt.Errorf("user missing email")
		}
		body := mustMarshalJSONString(user)
		if _, err := stmt.Exec(brandname, email, brandCloudID, tenantSlug, firstNonEmpty(stringValue(user["role"]), role), stringValue(user["password"]), mustMarshalJSONString(user["tokens"]), mustMarshalJSONString(user["app_credentials"]), mustMarshalJSONString(user["app_certificate"]), body, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *testDataStore) ReadUsersList(brandname string) (map[string]userCredential, []userCredential, error) {
	rows, err := s.DB.Query(`select email, password, tokens_json from users where brandname = ? and (role = 'member' or coalesce(role, '') = '') order by email`, brandname)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byEmail := map[string]userCredential{}
	list := []userCredential{}
	for rows.Next() {
		var email, password, tokensJSON string
		if err := rows.Scan(&email, &password, &tokensJSON); err != nil {
			return nil, nil, err
		}
		user := userCredential{Email: email, Password: password}
		_ = json.Unmarshal([]byte(tokensJSON), &user.Tokens)
		byEmail[email] = user
		list = append(list, user)
	}
	return byEmail, list, rows.Err()
}

func (s *testDataStore) ReplaceDevices(brandname, runID string, devices []generatedDevice, credentials map[string]testDataDeviceCredential) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from devices where brandname = ?`, brandname); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from device_credentials where brandname = ?`, brandname); err != nil {
		return err
	}
	deviceStmt, err := tx.Prepare(`insert into devices(brandname, device_id, run_id, device_type, display_name, category, mqtt_capability, service_options_json, model, body_json, updated_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer deviceStmt.Close()
	credStmt, err := tx.Prepare(`insert into device_credentials(brandname, device_id, cert_pem, key_pem, chain_pem, bundle_pem, csr_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json, updated_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer credStmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, device := range devices {
		category := "mqtt_device"
		if contains(device.ServiceOptions, "video_streaming") || contains(device.ServiceOptions, "video_storage") {
			category = "ip_camera"
		}
		if _, err := deviceStmt.Exec(brandname, device.DeviceID, runID, device.DeviceType, device.DisplayName, category, device.MQTTCapability, mustMarshalJSONString(device.ServiceOptions), device.Model, mustMarshalJSONString(device), now); err != nil {
			return err
		}
		cred := credentials[device.DeviceID]
		if cred.DeviceID == "" {
			cred.DeviceID = device.DeviceID
		}
		if _, err := credStmt.Exec(brandname, cred.DeviceID, cred.CertPEM, cred.KeyPEM, cred.ChainPEM, cred.BundlePEM, cred.CSRPEM, firstNonEmpty(cred.MetadataJSON, mustMarshalJSONString(device)), cred.FactoryEnrollRequestJSON, cred.FactoryEnrollResponseRedactedJSON, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *testDataStore) ReadDeviceManifest(brandname string) ([]bindDeviceManifest, error) {
	rows, err := s.DB.Query(`select device_id, device_type, display_name, service_options_json from devices where brandname = ? order by device_id`, brandname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bindDeviceManifest{}
	for rows.Next() {
		var item bindDeviceManifest
		var serviceOptionsJSON string
		if err := rows.Scan(&item.DeviceID, &item.DeviceType, &item.DisplayName, &serviceOptionsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(serviceOptionsJSON), &item.ServiceOptions)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *testDataStore) ReadDeviceCredential(brandname, deviceID string) (testDataDeviceCredential, error) {
	var cred testDataDeviceCredential
	cred.DeviceID = deviceID
	err := s.DB.QueryRow(`select coalesce(cert_pem, ''), coalesce(key_pem, ''), coalesce(chain_pem, ''), coalesce(bundle_pem, ''), coalesce(csr_pem, ''), coalesce(metadata_json, ''), coalesce(factory_enroll_request_json, ''), coalesce(factory_enroll_response_redacted_json, '') from device_credentials where brandname = ? and device_id = ?`, brandname, deviceID).Scan(&cred.CertPEM, &cred.KeyPEM, &cred.ChainPEM, &cred.BundlePEM, &cred.CSRPEM, &cred.MetadataJSON, &cred.FactoryEnrollRequestJSON, &cred.FactoryEnrollResponseRedactedJSON)
	return cred, err
}

func (s *testDataStore) ReplaceBindings(brandname, brandCloudID, tenantSlug, runID string, assignments []bindAssignment) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from device_bindings where brandname = ?`, brandname); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from device_provisioning where brandname = ?`, brandname); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into device_bindings(brandname, device_id, run_id, brand_cloud_id, tenant_slug, assignment_index, assigned_email, device_type, category, service_options_json, account_device_id, operation_id, status, body_json, updated_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	provStmt, err := tx.Prepare(`insert into device_provisioning(brandname, device_id, account_device_id, operation_id, status, detail_json, updated_at) values(?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer provStmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, assignment := range assignments {
		if _, err := stmt.Exec(brandname, assignment.DeviceID, runID, brandCloudID, tenantSlug, assignment.AssignmentIndex, assignment.AssignedEmail, assignment.DeviceType, assignment.Category, mustMarshalJSONString(assignment.ServiceOptions), assignment.AccountDeviceID, assignment.OperationID, assignment.Status, mustMarshalJSONString(assignment), now); err != nil {
			return err
		}
		if _, err := provStmt.Exec(brandname, assignment.DeviceID, assignment.AccountDeviceID, assignment.OperationID, assignment.Status, mustMarshalJSONString(assignment), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *testDataStore) UpsertBinding(brandname, brandCloudID, tenantSlug, runID string, assignment bindAssignment) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`insert into device_bindings(brandname, device_id, run_id, brand_cloud_id, tenant_slug, assignment_index, assigned_email, device_type, category, service_options_json, account_device_id, operation_id, status, body_json, updated_at)
values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(brandname, device_id) do update set
	run_id = excluded.run_id,
	brand_cloud_id = excluded.brand_cloud_id,
	tenant_slug = excluded.tenant_slug,
	assignment_index = excluded.assignment_index,
	assigned_email = excluded.assigned_email,
	device_type = excluded.device_type,
	category = excluded.category,
	service_options_json = excluded.service_options_json,
	account_device_id = excluded.account_device_id,
	operation_id = excluded.operation_id,
	status = excluded.status,
	body_json = excluded.body_json,
	updated_at = excluded.updated_at`, brandname, assignment.DeviceID, runID, brandCloudID, tenantSlug, assignment.AssignmentIndex, assignment.AssignedEmail, assignment.DeviceType, assignment.Category, mustMarshalJSONString(assignment.ServiceOptions), assignment.AccountDeviceID, assignment.OperationID, assignment.Status, mustMarshalJSONString(assignment), now); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert into device_provisioning(brandname, device_id, account_device_id, operation_id, status, detail_json, updated_at)
values(?, ?, ?, ?, ?, ?, ?)
on conflict(brandname, device_id) do update set
	account_device_id = excluded.account_device_id,
	operation_id = excluded.operation_id,
	status = excluded.status,
	detail_json = excluded.detail_json,
	updated_at = excluded.updated_at`, brandname, assignment.DeviceID, assignment.AccountDeviceID, assignment.OperationID, assignment.Status, mustMarshalJSONString(assignment), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *testDataStore) ReadBindAssignments(brandname string) ([]bindAssignment, error) {
	rows, err := s.DB.Query(`select assignment_index, assigned_email, device_id, device_type, category, service_options_json, account_device_id, operation_id, status, body_json from device_bindings where brandname = ? order by assignment_index, device_id`, brandname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bindAssignment{}
	for rows.Next() {
		var item bindAssignment
		var serviceOptionsJSON, bodyJSON string
		if err := rows.Scan(&item.AssignmentIndex, &item.AssignedEmail, &item.DeviceID, &item.DeviceType, &item.Category, &serviceOptionsJSON, &item.AccountDeviceID, &item.OperationID, &item.Status, &bodyJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(serviceOptionsJSON), &item.ServiceOptions)
		var stored bindAssignment
		if err := json.Unmarshal([]byte(bodyJSON), &stored); err != nil {
			return nil, err
		}
		item.ClaimID = stored.ClaimID
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *testDataStore) BindingsMatchDevices(brandname string) (bool, error) {
	var missing int
	if err := s.DB.QueryRow(`select count(*)
from devices d
left join device_bindings b on b.brandname = d.brandname and b.device_id = d.device_id
where d.brandname = ? and b.device_id is null`, brandname).Scan(&missing); err != nil {
		return false, err
	}
	var stale int
	if err := s.DB.QueryRow(`select count(*)
from device_bindings b
left join devices d on d.brandname = b.brandname and d.device_id = b.device_id
where b.brandname = ? and d.device_id is null`, brandname).Scan(&stale); err != nil {
		return false, err
	}
	return missing == 0 && stale == 0, nil
}

func (s *testDataStore) ReadBindArtifact(brandname string) (bindArtifact, error) {
	assignments, err := s.ReadBindAssignments(brandname)
	if err != nil {
		return bindArtifact{}, err
	}
	artifact := bindArtifact{
		Brandname:   brandname,
		Count:       len(assignments),
		Assignments: assignments,
	}
	if err := s.DB.QueryRow(`select coalesce(brand_cloud_id, ''), coalesce(tenant_slug, '') from device_bindings where brandname = ? and coalesce(brand_cloud_id, '') != '' order by assignment_index, device_id limit 1`, brandname).Scan(&artifact.BrandCloudID, &artifact.TenantSlug); err != nil && err != sql.ErrNoRows {
		return bindArtifact{}, err
	}
	return artifact, nil
}

func (s *testDataStore) Coverage(brandname string) (testDataCoverage, error) {
	out := testDataCoverage{DeviceMix: map[string]int{}}
	if err := s.DB.QueryRow(`select count(*) from users where brandname = ? and (role = 'member' or coalesce(role, '') = '')`, brandname).Scan(&out.Users); err != nil {
		return out, err
	}
	if err := s.DB.QueryRow(`select count(*) from devices where brandname = ?`, brandname).Scan(&out.Devices); err != nil {
		return out, err
	}
	if err := s.DB.QueryRow(`select count(*) from device_bindings where brandname = ?`, brandname).Scan(&out.Bindings); err != nil {
		return out, err
	}
	if err := s.DB.QueryRow(`select count(distinct assigned_email) from device_bindings where brandname = ?`, brandname).Scan(&out.BoundUsers); err != nil {
		return out, err
	}
	rows, err := s.DB.Query(`select device_type, count(*) from devices where brandname = ? group by device_type`, brandname)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var count int
		if err := rows.Scan(&typ, &count); err != nil {
			return out, err
		}
		out.DeviceMix[typ] = count
	}
	return out, rows.Err()
}

func (s *testDataStore) UserBodies(brandname string) ([]map[string]any, error) {
	rows, err := s.DB.Query(`select body_json from users where brandname = ? order by email`, brandname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(body), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *testDataStore) UpdateUserTokens(brandname string, sessions map[string]*brandCloudUserSession) (int, error) {
	if len(sessions) == 0 {
		return 0, nil
	}
	updated := 0
	for email, session := range sessions {
		if session == nil || (session.Session.AccessToken == "" && session.Session.RefreshToken == "") {
			continue
		}
		var body string
		if err := s.DB.QueryRow(`select body_json from users where brandname = ? and email = ?`, brandname, email).Scan(&body); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return updated, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(body), &item); err != nil {
			return updated, err
		}
		item["tokens"] = session.Session
		if _, err := s.DB.Exec(`update users set tokens_json = ?, body_json = ?, updated_at = ? where brandname = ? and email = ?`, mustMarshalJSONString(session.Session), mustMarshalJSONString(item), time.Now().UTC().Format(time.RFC3339), brandname, email); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func readUsersListFromTestData(envRoot, brandname string) (map[string]userCredential, []userCredential, error) {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	return store.ReadUsersList(brandname)
}

func readDeviceManifestFromTestData(envRoot, brandname string) ([]bindDeviceManifest, error) {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.ReadDeviceManifest(brandname)
}

func readBindAssignmentsFromTestData(envRoot, brandname string) ([]bindAssignment, error) {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.ReadBindAssignments(brandname)
}

func readBindArtifactFromTestData(envRoot, brandname string) (bindArtifact, error) {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return bindArtifact{}, err
	}
	defer store.Close()
	return store.ReadBindArtifact(brandname)
}

func testDataCoverageFor(envRoot, brandname string) testDataCoverage {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return testDataCoverage{DeviceMix: map[string]int{}}
	}
	defer store.Close()
	coverage, err := store.Coverage(brandname)
	if err != nil {
		return testDataCoverage{DeviceMix: map[string]int{}}
	}
	return coverage
}

func testDataDeviceMatchesSetup(envRoot, brandname string, expectedCount int, expectedMix string) bool {
	coverage := testDataCoverageFor(envRoot, brandname)
	if coverage.Devices != expectedCount {
		return false
	}
	expected, err := allocateDeviceMix(expectedCount, expectedMix)
	if err != nil {
		return false
	}
	for typ, want := range expected {
		if coverage.DeviceMix[typ] != want {
			return false
		}
	}
	return true
}

func testDataBindMatchesSetup(envRoot, brandname string, expectedUsers, expectedDevices int, expectedMix string) bool {
	coverage := testDataCoverageFor(envRoot, brandname)
	if coverage.Users != expectedUsers || coverage.BoundUsers != expectedUsers || coverage.Bindings != expectedDevices || !testDataDeviceMatchesSetup(envRoot, brandname, expectedDevices, expectedMix) {
		return false
	}
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return false
	}
	defer store.Close()
	matches, err := store.BindingsMatchDevices(brandname)
	return err == nil && matches
}

func importLegacyTestData(envRoot, brandname string, latestOnly bool) (legacyImportSummary, error) {
	slug := brandSlug(brandname)
	usersPath := latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
	bindPath := latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
	if usersPath == "" {
		return legacyImportSummary{}, fmt.Errorf("missing legacy users artifact")
	}
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return legacyImportSummary{}, err
	}
	defer store.Close()
	var usersArtifact struct {
		Brandname    string           `json:"brandname"`
		BrandCloudID string           `json:"brand_cloud_id"`
		TenantSlug   string           `json:"tenant_slug"`
		Role         string           `json:"role"`
		Users        []map[string]any `json:"users"`
	}
	if err := readJSONFile(usersPath, &usersArtifact); err != nil {
		return legacyImportSummary{}, err
	}
	if err := store.ReplaceUsers(brandname, usersArtifact.BrandCloudID, usersArtifact.TenantSlug, usersArtifact.Role, usersArtifact.Users); err != nil {
		return legacyImportSummary{}, err
	}
	devices, err := readGeneratedDevicesFromLegacy(envRoot)
	if err != nil {
		return legacyImportSummary{}, err
	}
	creds := map[string]testDataDeviceCredential{}
	missingKeys := 0
	for _, device := range devices {
		cred := legacyDeviceCredential(envRoot, device)
		if strings.TrimSpace(cred.KeyPEM) == "" {
			missingKeys++
		}
		creds[device.DeviceID] = cred
	}
	if err := store.ReplaceDevices(brandname, "legacy-import", devices, creds); err != nil {
		return legacyImportSummary{}, err
	}
	bindings := []bindAssignment{}
	brandCloudID := usersArtifact.BrandCloudID
	tenantSlug := usersArtifact.TenantSlug
	if bindPath != "" {
		var bind bindArtifact
		if err := readJSONFile(bindPath, &bind); err != nil {
			return legacyImportSummary{}, err
		}
		bindings = bind.Assignments
		brandCloudID = firstNonEmpty(bind.BrandCloudID, brandCloudID)
		tenantSlug = firstNonEmpty(bind.TenantSlug, tenantSlug)
		if err := store.ReplaceBindings(brandname, brandCloudID, tenantSlug, "legacy-import", bindings); err != nil {
			return legacyImportSummary{}, err
		}
	}
	mix := map[string]int{}
	for _, device := range devices {
		mix[device.DeviceType]++
	}
	return legacyImportSummary{Database: store.Path, Users: len(usersArtifact.Users), Devices: len(devices), Bindings: len(bindings), DeviceMix: mix, MissingDeviceKeys: missingKeys}, nil
}

func inspectTestData(envRoot, brandname string) (map[string]any, error) {
	store, err := openTestDataStore(envRoot, brandname)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	count := func(query string) (int, error) {
		var value int
		if err := store.DB.QueryRow(query, brandname).Scan(&value); err != nil {
			return 0, err
		}
		return value, nil
	}
	users, err := count(`select count(*) from users where brandname = ?`)
	if err != nil {
		return nil, err
	}
	devices, err := count(`select count(*) from devices where brandname = ?`)
	if err != nil {
		return nil, err
	}
	bindings, err := count(`select count(*) from device_bindings where brandname = ?`)
	if err != nil {
		return nil, err
	}
	var schema string
	_ = store.DB.QueryRow(`select value from metadata where key = 'schema_version'`).Scan(&schema)
	return map[string]any{
		"schema":    schema,
		"database":  store.Path,
		"brandname": brandname,
		"users":     users,
		"devices":   devices,
		"bindings":  bindings,
	}, nil
}

func readGeneratedDevicesFromLegacy(envRoot string) ([]generatedDevice, error) {
	manifestPath := filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json")
	var devices []generatedDevice
	if err := readJSONFile(manifestPath, &devices); err == nil {
		return devices, nil
	}
	bindDevices, err := readDeviceManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	out := make([]generatedDevice, 0, len(bindDevices))
	for _, device := range bindDevices {
		out = append(out, generatedDevice{
			DeviceID:       device.DeviceID,
			DeviceType:     device.DeviceType,
			DisplayName:    device.DisplayName,
			ServiceOptions: device.ServiceOptions,
		})
	}
	return out, nil
}

func legacyDeviceCredential(envRoot string, device generatedDevice) testDataDeviceCredential {
	deviceDir := filepath.Join(envRoot, "devices", "test_device", "devices", device.DeviceType, device.DeviceID)
	bundlePath := filepath.Join(envRoot, "devices", "test_device", "bundles", device.DeviceType, device.DeviceID+".pem")
	return testDataDeviceCredential{
		DeviceID:                          device.DeviceID,
		CertPEM:                           readOptionalFileText(filepath.Join(deviceDir, "device.cert.pem")),
		KeyPEM:                            readOptionalFileText(filepath.Join(deviceDir, "device.key.pem")),
		ChainPEM:                          readOptionalFileText(filepath.Join(deviceDir, "device.chain.pem")),
		BundlePEM:                         readOptionalFileText(bundlePath),
		CSRPEM:                            readOptionalFileText(filepath.Join(deviceDir, "device.csr.pem")),
		MetadataJSON:                      firstNonEmpty(readOptionalFileText(filepath.Join(deviceDir, "metadata.json")), mustMarshalJSONString(device)),
		FactoryEnrollRequestJSON:          readOptionalFileText(filepath.Join(deviceDir, "factory-enroll-request.json")),
		FactoryEnrollResponseRedactedJSON: readOptionalFileText(filepath.Join(deviceDir, "factory-enroll-response.redacted.json")),
	}
}

func testDataCredentialFromOutputDir(outDir string, device generatedDevice) testDataDeviceCredential {
	deviceDir := filepath.Join(outDir, "devices", device.DeviceType, device.DeviceID)
	bundlePath := filepath.Join(outDir, "bundles", device.DeviceType, device.DeviceID+".pem")
	return testDataDeviceCredential{
		DeviceID:                          device.DeviceID,
		CertPEM:                           readOptionalFileText(filepath.Join(deviceDir, "device.cert.pem")),
		KeyPEM:                            readOptionalFileText(filepath.Join(deviceDir, "device.key.pem")),
		ChainPEM:                          readOptionalFileText(filepath.Join(deviceDir, "device.chain.pem")),
		BundlePEM:                         readOptionalFileText(bundlePath),
		CSRPEM:                            readOptionalFileText(filepath.Join(deviceDir, "device.csr.pem")),
		MetadataJSON:                      firstNonEmpty(readOptionalFileText(filepath.Join(deviceDir, "metadata.json")), mustMarshalJSONString(device)),
		FactoryEnrollRequestJSON:          readOptionalFileText(filepath.Join(deviceDir, "factory-enroll-request.json")),
		FactoryEnrollResponseRedactedJSON: readOptionalFileText(filepath.Join(deviceDir, "factory-enroll-response.redacted.json")),
	}
}

func cleanupGeneratedDeviceSmallFiles(outDir string) error {
	for _, path := range []string{
		filepath.Join(outDir, "devices"),
		filepath.Join(outDir, "bundles"),
		filepath.Join(outDir, "manifests"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func legacyCleanupPlan(envRoot, brandname string) (legacyCleanupPlanResult, error) {
	paths := []string{}
	slug := brandSlug(brandname)
	patterns := []string{
		filepath.Join(envRoot, "artifacts", "users", slug+"-users-*.json"),
		filepath.Join(envRoot, "artifacts", "device-bind", slug+"-device-bind-*.json"),
		filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.*"),
		filepath.Join(envRoot, "devices", "test_device", "manifests", "device_ids.txt"),
		filepath.Join(envRoot, "devices", "test_device", "manifests", "factory-enroll-results.jsonl"),
		filepath.Join(envRoot, "devices", "test_device", "factory-enroll-results.jsonl"),
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		paths = append(paths, matches...)
	}
	dirs := []string{}
	for _, dir := range []string{
		filepath.Join(envRoot, "devices", "test_device", "devices"),
		filepath.Join(envRoot, "devices", "test_device", "bundles"),
	} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		dirs = append(dirs, dir)
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}
	sort.Strings(paths)
	return legacyCleanupPlanResult{Files: paths, Dirs: dirs, Count: len(paths)}, nil
}

func readOptionalFileText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func mustMarshalJSONString(value any) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
