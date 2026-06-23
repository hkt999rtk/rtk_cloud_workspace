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
	Label          string `json:"label"`
	RunID          string `json:"run_id,omitempty"`
	Format         string `json:"format"`
	SQLiteGzipPath string `json:"sqlite_gzip_path"`
	SHA256         string `json:"sha256"`
	DeviceCount    int    `json:"device_count"`
	UserArtifact   string `json:"user_artifact,omitempty"`
	BindArtifact   string `json:"bind_artifact,omitempty"`
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

	deviceRows, err := loadShardDeviceRowsForBundle(envRoot, plan, assignment)
	if err != nil {
		return shardCredentialBundle{}, err
	}
	if err := insertBundleDevices(db, envRoot, plan.Conditions.Brandname, deviceRows); err != nil {
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

func initCredentialBundleSchema(db *sql.DB) error {
	stmts := []string{
		`create table metadata (key text primary key, value text not null)`,
		`create table devices (
			device_id text primary key,
			device_type text not null,
			cert_pem text,
			key_pem text,
			chain_pem text,
			bundle_pem text,
			metadata_json text,
			factory_enroll_request_json text,
			factory_enroll_response_redacted_json text
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
	_, err := db.Exec(`insert into metadata(key, value) values(?, ?)`, key, value)
	return err
}

func loadShardDeviceRowsForBundle(envRoot string, plan Plan, assignment VMAssignment) ([]deviceManifestRow, error) {
	if rows, err := loadShardDeviceRowsFromTestData(envRoot, plan, assignment); err == nil && len(rows) > 0 {
		return rows, nil
	}
	return nil, fmt.Errorf("missing SQLite test-data rows for %s", plan.Conditions.Brandname)
}

func loadShardDeviceRowsFromTestData(envRoot string, plan Plan, assignment VMAssignment) ([]deviceManifestRow, error) {
	db, err := sql.Open("sqlite", homeTestDataDBPath(envRoot, plan.Conditions.Brandname))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`select assigned_email, device_id, device_type, service_options_json from device_bindings where brandname = ? order by assignment_index, device_id`, plan.Conditions.Brandname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selectedByUser := map[string][]shardBindAssignment{}
	for rows.Next() {
		var item shardBindAssignment
		var serviceOptionsJSON string
		if err := rows.Scan(&item.AssignedEmail, &item.DeviceID, &item.DeviceType, &serviceOptionsJSON); err != nil {
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
		out = append(out, deviceManifestRow{DeviceID: item.DeviceID, DeviceType: item.DeviceType})
		if maxConnected > 0 && len(out) >= maxConnected {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no selected devices for shard %d/%d", shardIndex, shardCount)
	}
	return out, nil
}

func insertBundleDevices(db *sql.DB, envRoot string, brandname string, rows []deviceManifestRow) error {
	source, err := sql.Open("sqlite", homeTestDataDBPath(envRoot, brandname))
	if err != nil {
		return err
	}
	defer source.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into devices(device_id, device_type, cert_pem, key_pem, chain_pem, bundle_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json) values(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		var certPEM, keyPEM, chainPEM, bundlePEM, metadataJSON, requestJSON, responseJSON string
		if err := source.QueryRow(`select cert_pem, key_pem, chain_pem, bundle_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json from device_credentials where brandname = ? and device_id = ?`, brandname, row.DeviceID).Scan(&certPEM, &keyPEM, &chainPEM, &bundlePEM, &metadataJSON, &requestJSON, &responseJSON); err != nil {
			return err
		}
		if _, err := stmt.Exec(
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
