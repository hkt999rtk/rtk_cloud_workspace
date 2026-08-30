package home100k

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type shardCredentialBundle struct {
	CompressedPath string
	ManifestPath   string
	SHA256         string
	DeviceCount    int
	UserArtifact   string
	BindArtifact   string
}

type shardCredentialBundleManifest struct {
	Label          string   `json:"label"`
	RunID          string   `json:"run_id,omitempty"`
	Format         string   `json:"format"`
	SQLiteGzipPath string   `json:"sqlite_gzip_path"`
	SHA256         string   `json:"sha256"`
	DeviceCount    int      `json:"device_count"`
	DeviceIDs      []string `json:"device_ids,omitempty"`
	UserArtifact   string   `json:"user_artifact,omitempty"`
	BindArtifact   string   `json:"bind_artifact,omitempty"`
}

func homeTestDataDBPath(envRoot, brandname string) string {
	brandLower := strings.ToLower(strings.TrimSpace(brandname))
	if brandLower == "" {
		brandLower = "rtk"
	}
	return filepath.Join(envRoot, "artifacts", "test-data", brandLower+"-test-data.sqlite")
}

func writeShardCredentialBundle(outDir string, envRoot string, plan Plan, assignment VMAssignment) (shardCredentialBundle, error) {
	if strings.TrimSpace(assignment.Label) == "" {
		return shardCredentialBundle{}, fmt.Errorf("assignment label is required")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return shardCredentialBundle{}, err
	}
	sqlitePath := filepath.Join(outDir, assignment.Label+".sqlite")
	compressedPath := sqlitePath + ".gz"
	manifestPath := filepath.Join(outDir, assignment.Label+".manifest.json")
	_ = os.Remove(sqlitePath)
	_ = os.Remove(compressedPath)
	_ = os.Remove(manifestPath)

	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		return shardCredentialBundle{}, err
	}
	defer db.Close()
	if err := initCredentialBundleSchema(db); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleMetadata(db, "format", "home-100k-credential-bundle/v1"); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleMetadata(db, "label", assignment.Label); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleMetadata(db, "brandname", plan.Conditions.Brandname); err != nil {
		return shardCredentialBundle{}, err
	}
	if len(plan.BrandDistribution) > 0 {
		if err := insertBundleMetadata(db, "brand_distribution", bundleJSONText(plan.BrandDistribution)); err != nil {
			return shardCredentialBundle{}, err
		}
	}

	deviceRows, err := loadShardDeviceRowsForBundle(envRoot, plan, assignment)
	if err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleDevices(db, envRoot, deviceRows); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleUsersAndBindings(db, envRoot, deviceRows); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := db.Close(); err != nil {
		return shardCredentialBundle{}, err
	}
	if err := gzipFile(sqlitePath, compressedPath); err != nil {
		return shardCredentialBundle{}, err
	}
	sum, err := fileSHA256(compressedPath)
	if err != nil {
		return shardCredentialBundle{}, err
	}
	manifest := shardCredentialBundleManifest{
		Label:          assignment.Label,
		Format:         "home-100k-credential-bundle/v1",
		SQLiteGzipPath: filepath.Base(compressedPath),
		SHA256:         sum,
		DeviceCount:    len(deviceRows),
		DeviceIDs:      deviceIDsFromManifestRows(deviceRows),
	}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return shardCredentialBundle{}, err
	}
	_ = os.Remove(sqlitePath)
	return shardCredentialBundle{
		CompressedPath: compressedPath,
		ManifestPath:   manifestPath,
		SHA256:         sum,
		DeviceCount:    len(deviceRows),
	}, nil
}

func deviceIDsFromManifestRows(rows []deviceManifestRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.DeviceID) != "" {
			ids = append(ids, row.DeviceID)
		}
	}
	return ids
}

func initCredentialBundleSchema(db *sql.DB) error {
	stmts := []string{
		`create table metadata (key text primary key, value text not null)`,
		`create table devices (
			brandname text not null default '',
			device_id text not null,
			device_type text not null,
			cert_pem text,
			key_pem text,
			chain_pem text,
			bundle_pem text,
			metadata_json text,
			factory_enroll_request_json text,
			factory_enroll_response_redacted_json text,
			primary key (brandname, device_id)
		)`,
		`create table users (
			brandname text not null default '',
			user_id text,
			brand_cloud_id text,
			tenant_slug text,
			email text not null,
			password text,
			tokens_json text,
			app_credentials_json text,
			app_certificate_json text,
			body_json text not null,
			primary key (brandname, email)
		)`,
		`create table device_bindings (
			brandname text not null default '',
			brand_cloud_id text,
			tenant_slug text,
			device_id text not null,
			assignment_index integer not null,
			assigned_email text not null,
			device_type text not null,
			service_options_json text not null,
			body_json text not null,
			primary key (brandname, device_id)
		)`,
		`create table artifacts (
			name text primary key,
			kind text not null,
			body_json text not null
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func insertBundleMetadata(db *sql.DB, key string, value string) error {
	_, err := db.Exec(`insert into metadata(key, value) values(?, ?) on conflict(key) do update set value = excluded.value`, key, value)
	return err
}

func loadShardDeviceRowsForBundle(envRoot string, plan Plan, assignment VMAssignment) ([]deviceManifestRow, error) {
	if rows, err := loadShardDeviceRowsFromTestData(envRoot, plan, assignment); err == nil && len(rows) > 0 {
		return rows, nil
	}
	return nil, fmt.Errorf("missing SQLite test-data rows for %s", plan.Conditions.Brandname)
}

func loadShardDeviceRowsFromTestData(envRoot string, plan Plan, assignment VMAssignment) ([]deviceManifestRow, error) {
	brands := plan.BrandDistribution
	if len(brands) == 0 {
		brands = []BrandDistributionEntry{{Brandname: plan.Conditions.Brandname}}
	}
	selected := []shardBindAssignment{}
	for _, brand := range brands {
		rows, err := loadEligibleBrandRows(envRoot, brand.Brandname)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Brandname != selected[j].Brandname {
			return selected[i].Brandname < selected[j].Brandname
		}
		if selected[i].AssignedEmail != selected[j].AssignedEmail {
			return selected[i].AssignedEmail < selected[j].AssignedEmail
		}
		return selected[i].DeviceID < selected[j].DeviceID
	})
	shardCount := len(plan.ShardsByRole("device-mqtt"))
	if shardCount <= 0 {
		shardCount = 1
	}
	shardIndex := assignment.Index
	out := []deviceManifestRow{}
	maxConnected := maxAssignmentConnectedDevices(assignment)
	for idx, item := range selected {
		if idx%shardCount != shardIndex {
			continue
		}
		out = append(out, deviceManifestRow{
			Brandname:       item.Brandname,
			BrandCloudID:    item.BrandCloudID,
			TenantSlug:      item.TenantSlug,
			AssignmentIndex: item.AssignmentIndex,
			AssignedEmail:   item.AssignedEmail,
			DeviceID:        item.DeviceID,
			DeviceType:      item.DeviceType,
			ServiceOptions:  item.ServiceOptions,
		})
		if maxConnected > 0 && len(out) >= maxConnected {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no selected devices for shard %d/%d", shardIndex, shardCount)
	}
	return out, nil
}

func loadEligibleBrandRows(envRoot string, brandname string) ([]shardBindAssignment, error) {
	db, err := sql.Open("sqlite", homeTestDataDBPath(envRoot, brandname))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := `select b.brandname, coalesce(b.brand_cloud_id, ''), coalesce(b.tenant_slug, ''), b.assignment_index, b.assigned_email, b.device_id, b.device_type, b.service_options_json from device_bindings b where b.brandname = ? order by b.assignment_index, b.device_id`
	if homeSQLiteColumnExists(db, "users", "role") {
		query = `select b.brandname, coalesce(b.brand_cloud_id, ''), coalesce(b.tenant_slug, ''), b.assignment_index, b.assigned_email, b.device_id, b.device_type, b.service_options_json from device_bindings b join users u on u.brandname = b.brandname and u.email = b.assigned_email where b.brandname = ? and (` + homeRuntimeUserRolePredicate("u") + `) order by b.assignment_index, b.device_id`
	}
	rows, err := db.Query(query, brandname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selectedByUser := map[string][]shardBindAssignment{}
	for rows.Next() {
		var item shardBindAssignment
		var serviceOptionsJSON string
		if err := rows.Scan(&item.Brandname, &item.BrandCloudID, &item.TenantSlug, &item.AssignmentIndex, &item.AssignedEmail, &item.DeviceID, &item.DeviceType, &serviceOptionsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(serviceOptionsJSON), &item.ServiceOptions)
		if !homeDeviceType(item.DeviceType) || !stringSliceContains(item.ServiceOptions, "mqtt") {
			continue
		}
		selectedByUser[item.AssignedEmail] = append(selectedByUser[item.AssignedEmail], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	selectedUsers := sortedMapKeys(selectedByUser)
	selected := []shardBindAssignment{}
	for _, email := range selectedUsers {
		selected = append(selected, selectedByUser[email]...)
	}
	return selected, nil
}

func insertBundleDevices(db *sql.DB, envRoot string, rows []deviceManifestRow) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into devices(brandname, device_id, device_type, cert_pem, key_pem, chain_pem, bundle_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	sources := map[string]*sql.DB{}
	defer closeBundleSources(sources)
	for _, row := range rows {
		source, err := bundleSourceForBrand(sources, envRoot, row.Brandname)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		var certPEM, keyPEM, chainPEM, bundlePEM, metadataJSON, requestJSON, responseJSON string
		if err := source.QueryRow(`select cert_pem, key_pem, chain_pem, bundle_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json from device_credentials where brandname = ? and device_id = ?`, row.Brandname, row.DeviceID).Scan(&certPEM, &keyPEM, &chainPEM, &bundlePEM, &metadataJSON, &requestJSON, &responseJSON); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := stmt.Exec(
			row.Brandname,
			row.DeviceID,
			row.DeviceType,
			certPEM,
			keyPEM,
			chainPEM,
			bundlePEM,
			metadataJSON,
			requestJSON,
			responseJSON,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func insertBundleUsersAndBindings(db *sql.DB, envRoot string, rows []deviceManifestRow) error {
	if brandCloudID, tenantSlug := firstShardTenantMetadata(rows); brandCloudID != "" || tenantSlug != "" {
		if brandCloudID != "" {
			if err := insertBundleMetadata(db, "brand_cloud_id", brandCloudID); err != nil {
				return err
			}
		}
		if tenantSlug != "" {
			if err := insertBundleMetadata(db, "tenant_slug", tenantSlug); err != nil {
				return err
			}
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	userStmt, err := tx.Prepare(`insert into users(brandname, user_id, brand_cloud_id, tenant_slug, email, password, tokens_json, app_credentials_json, app_certificate_json, body_json) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) on conflict(brandname, email) do nothing`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer userStmt.Close()
	bindStmt, err := tx.Prepare(`insert into device_bindings(brandname, brand_cloud_id, tenant_slug, device_id, assignment_index, assigned_email, device_type, service_options_json, body_json) values(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer bindStmt.Close()
	sources := map[string]*sql.DB{}
	defer closeBundleSources(sources)
	for _, row := range rows {
		source, err := bundleSourceForBrand(sources, envRoot, row.Brandname)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		var userID, password, tokensJSON, appCredentialsJSON, appCertificateJSON, userBodyJSON string
		userIDExpression := `''`
		if sqliteTableHasColumn(source, "users", "user_id") {
			userIDExpression = `coalesce(user_id, '')`
		}
		query := `select ` + userIDExpression + `, coalesce(password, ''), coalesce(tokens_json, '{}'), coalesce(app_credentials_json, '{}'), coalesce(app_certificate_json, '{}'), body_json from users where brandname = ? and email = ?`
		if err := source.QueryRow(query, row.Brandname, row.AssignedEmail).Scan(&userID, &password, &tokensJSON, &appCredentialsJSON, &appCertificateJSON, &userBodyJSON); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := userStmt.Exec(row.Brandname, userID, row.BrandCloudID, row.TenantSlug, row.AssignedEmail, password, tokensJSON, appCredentialsJSON, appCertificateJSON, userBodyJSON); err != nil {
			_ = tx.Rollback()
			return err
		}
		bindBody := map[string]any{
			"brandname":        row.Brandname,
			"brand_cloud_id":   row.BrandCloudID,
			"tenant_slug":      row.TenantSlug,
			"assignment_index": row.AssignmentIndex,
			"assigned_email":   row.AssignedEmail,
			"device_id":        row.DeviceID,
			"device_type":      row.DeviceType,
			"service_options":  row.ServiceOptions,
		}
		if _, err := bindStmt.Exec(row.Brandname, row.BrandCloudID, row.TenantSlug, row.DeviceID, row.AssignmentIndex, row.AssignedEmail, row.DeviceType, bundleJSONText(row.ServiceOptions), bundleJSONText(bindBody)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func sqliteTableHasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey) == nil && name == column {
			return true
		}
	}
	return false
}

func bundleSourceForBrand(sources map[string]*sql.DB, envRoot string, brandname string) (*sql.DB, error) {
	if db := sources[brandname]; db != nil {
		return db, nil
	}
	db, err := sql.Open("sqlite", homeTestDataDBPath(envRoot, brandname))
	if err != nil {
		return nil, err
	}
	sources[brandname] = db
	return db, nil
}

func closeBundleSources(sources map[string]*sql.DB) {
	for _, db := range sources {
		_ = db.Close()
	}
}

func firstShardTenantMetadata(rows []deviceManifestRow) (string, string) {
	for _, row := range rows {
		if row.BrandCloudID != "" || row.TenantSlug != "" {
			return row.BrandCloudID, row.TenantSlug
		}
	}
	return "", ""
}

func shardBundleTenantMetadata(db *sql.DB, brandname string) (string, string) {
	var brandCloudID, tenantSlug string
	err := db.QueryRow(`select coalesce(brand_cloud_id, ''), coalesce(tenant_slug, '') from device_bindings where brandname = ? and (coalesce(brand_cloud_id, '') != '' or coalesce(tenant_slug, '') != '') order by assignment_index, device_id limit 1`, brandname).Scan(&brandCloudID, &tenantSlug)
	if err == nil && (brandCloudID != "" || tenantSlug != "") {
		return brandCloudID, tenantSlug
	}
	_ = db.QueryRow(`select coalesce(brand_cloud_id, ''), coalesce(tenant_slug, '') from users where brandname = ? and (coalesce(brand_cloud_id, '') != '' or coalesce(tenant_slug, '') != '') order by email limit 1`, brandname).Scan(&brandCloudID, &tenantSlug)
	return brandCloudID, tenantSlug
}

func bundleJSONText(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func readOptionalText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func gzipFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
