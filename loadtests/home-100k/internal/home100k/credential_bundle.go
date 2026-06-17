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
	if err := insertBundleDevices(db, envRoot, deviceRows); err != nil {
		return shardCredentialBundle{}, err
	}
	userArtifact, bindArtifact, err := insertBundleArtifacts(db, envRoot, strings.ToLower(plan.Conditions.Brandname))
	if err != nil {
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
		UserArtifact:   filepath.Base(userArtifact),
		BindArtifact:   filepath.Base(bindArtifact),
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
		UserArtifact:   userArtifact,
		BindArtifact:   bindArtifact,
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
	if rows, err := loadShardDeviceRowsFromArtifacts(envRoot, plan, assignment); err == nil && len(rows) > 0 {
		return rows, nil
	}
	deviceRows, err := loadDeviceManifestRows(envRoot)
	if err != nil {
		return nil, err
	}
	out := []deviceManifestRow{}
	for _, shard := range assignment.TaskShards {
		if shard.Role != "device-mqtt" {
			continue
		}
		for idx := shard.Start; idx < shard.End && idx < len(deviceRows); idx++ {
			out = append(out, deviceRows[idx])
		}
	}
	return out, nil
}

func insertBundleDevices(db *sql.DB, envRoot string, rows []deviceManifestRow) error {
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
		deviceDir := filepath.Join(envRoot, "devices", "test_device", "devices", row.DeviceType, row.DeviceID)
		bundlePath := filepath.Join(envRoot, "devices", "test_device", "bundles", row.DeviceType, row.DeviceID+".pem")
		if _, err := stmt.Exec(
			row.DeviceID,
			row.DeviceType,
			readOptionalText(filepath.Join(deviceDir, "device.cert.pem")),
			readOptionalText(filepath.Join(deviceDir, "device.key.pem")),
			readOptionalText(filepath.Join(deviceDir, "device.chain.pem")),
			readOptionalText(bundlePath),
			readOptionalText(filepath.Join(deviceDir, "metadata.json")),
			readOptionalText(filepath.Join(deviceDir, "factory-enroll-request.json")),
			readOptionalText(filepath.Join(deviceDir, "factory-enroll-response.redacted.json")),
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func insertBundleArtifacts(db *sql.DB, envRoot string, brandLower string) (string, string, error) {
	if strings.TrimSpace(brandLower) == "" {
		brandLower = "rtk"
	}
	usersPath := latestFile(filepath.Join(envRoot, "artifacts", "users", brandLower+"-users-*.json"))
	bindPath := latestHomeBindArtifact(filepath.Join(envRoot, "artifacts", "device-bind", brandLower+"-device-bind-*.json"), brandLower)
	if usersPath == "" || bindPath == "" {
		return "", "", fmt.Errorf("missing users or device-bind artifact")
	}
	for _, item := range []struct {
		name string
		kind string
		path string
	}{
		{name: filepath.Base(usersPath), kind: "users", path: usersPath},
		{name: filepath.Base(bindPath), kind: "device_bind", path: bindPath},
	} {
		body, err := os.ReadFile(item.path)
		if err != nil {
			return "", "", err
		}
		if !json.Valid(body) {
			return "", "", fmt.Errorf("artifact is not valid JSON: %s", item.path)
		}
		if _, err := db.Exec(`insert into artifacts(name, kind, body_json) values(?, ?, ?)`, item.name, item.kind, string(body)); err != nil {
			return "", "", err
		}
	}
	return usersPath, bindPath, nil
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
