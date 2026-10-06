package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Completed state is owned by the environment, independently of a checkout.
// Legacy runtime files are read only by an explicitly selected import operation.
func deploymentStorageStatePath(environment, name string) (string, error) {
	if !keySet("storage-cutover.json", "storage-cutover-ota.json", "storage-migration.json", "storage-migration-ota.json", "storage-migration-artifacts.json", "storage-preflight.json", "storage-preflight-ota.json", "storage-preflight-release-artifacts.json", "storage-consumers.json")[name] {
		return "", errors.New("unknown environment storage state file")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return "", err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "storage")} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", errors.New("environment storage state requires real private 0700 directories")
		}
	}
	path, err := store.safePath(filepath.Join("deployment", "storage", name))
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", errors.New("environment storage evidence destination must be a private regular 0600 file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func readDeploymentStorageState(environment, name string) ([]byte, error) {
	path, err := deploymentStorageStatePath(environment, name)
	if err != nil {
		return nil, err
	}
	return readStorageEvidenceFile(path)
}

func writeDeploymentStorageState(environment, name string, value any) error {
	path, err := deploymentStorageStatePath(environment, name)
	if err != nil {
		return err
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(body) > 32<<20 {
		return errors.New("bounded environment storage state could not be encoded")
	}
	return store.write(filepath.Join("deployment", "storage", name), append(body, '\n'), true)
}

func storageCutoverReceiptName(purpose string) string {
	if purpose == "ota" {
		return "storage-cutover-ota.json"
	}
	return "storage-cutover.json"
}

func storageMigrationReceiptName(purpose string) string {
	if purpose == "ota" {
		return "storage-migration-ota.json"
	}
	if purpose == "artifacts" {
		return "storage-migration-artifacts.json"
	}
	return "storage-migration.json"
}

func runDeploymentStorageImport(args []string) error {
	fs := flag.NewFlagSet("deployment storage-import-state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	environmentRoot := fs.String("environment-root", "", "maintained environment root")
	purpose := fs.String("purpose", "media", "media, ota or artifacts")
	source := fs.String("source-runtime", "", "explicit original runtime containing state receipts; never searched automatically")
	confirm := fs.String("confirm", "", "exact selected stack")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return exitCode(2)
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || !filepath.IsAbs(*source) || !keySet("media", "ota", "artifacts")[*purpose] {
		fmt.Fprintln(os.Stderr, "error: valid --environment and absolute original --source-runtime are required")
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil || *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		fmt.Fprintln(os.Stderr, "error: maintained configuration and exact --confirm stack are required")
		return exitCode(2)
	}
	count, err := importDeploymentStorageState(cfg, *source, *purpose)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: original storage evidence could not be imported; retain the original files, completed private journals and migration proofs; existing canonical state is never overwritten")
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "IMPORTED environment=%s files=%d location=~/.config/rtk_cloud/%s/deployment/storage\n", cfg.Environment, count, cfg.Environment)
	fmt.Fprintln(os.Stdout, "Original completion times and proofs are retained. No cloud resources or credentials were changed. Repeat full preflight; import is not GO.")
	return nil
}

func readStorageEvidenceFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "original-storage-evidence")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<20 {
		return nil, errors.New("bounded private original storage evidence required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 {
		return nil, errors.New("original storage evidence exceeds its bound")
	}
	return raw, nil
}

// A hard link publishes the fully synced original file atomically and refuses
// an existing destination. Import cannot overwrite a concurrent writer.
func publishOriginalStorageEvidence(path string, raw []byte) error {
	if !filepath.IsAbs(path) || len(raw) == 0 || len(raw) > 32<<20 {
		return errors.New("bounded canonical storage destination required")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".storage-import-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return errors.New("original storage evidence could not be persisted")
	}
	return os.Link(f.Name(), path)
}

func importDeploymentStorageState(cfg deploymentConfig, sourceRuntime, selectedPurpose string) (int, error) {
	if !filepath.IsAbs(sourceRuntime) || !keySet("media", "ota", "artifacts")[selectedPurpose] {
		return 0, errors.New("explicit absolute original runtime required")
	}
	for _, path := range []string{sourceRuntime, filepath.Join(sourceRuntime, "state")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("original runtime must be real directories")
		}
	}
	// Proofs are published before completion receipts. A partial import cannot
	// turn a missing or mismatched completed journal into activation evidence.
	names := []string{"storage-migration.json", "storage-migration-ota.json", "storage-migration-artifacts.json", "storage-preflight.json", "storage-preflight-ota.json", "storage-preflight-release-artifacts.json", "storage-cutover.json", "storage-cutover-ota.json"}
	files := map[string][]byte{}
	for _, name := range names {
		filePurpose := "media"
		if strings.Contains(name, "ota") {
			filePurpose = "ota"
		}
		if strings.Contains(name, "artifacts") {
			filePurpose = "artifacts"
		}
		if filePurpose != selectedPurpose {
			continue
		}
		raw, err := readStorageEvidenceFile(filepath.Join(sourceRuntime, "state", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		var identity struct {
			Environment       string `json:"environment"`
			Bucket            string `json:"bucket"`
			Region            string `json:"region"`
			Purpose           string `json:"purpose"`
			Destination       string `json:"destination_bucket"`
			DestinationRegion string `json:"destination_region"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.Environment != cfg.Environment {
			return 0, errors.New("original storage state belongs to another environment or is invalid")
		}
		purpose, target := "media", cfg.Storage.RuntimeMedia
		if strings.Contains(name, "ota") {
			purpose, target = "ota", cfg.Storage.OTAFirmware
		}
		if strings.Contains(name, "artifacts") {
			purpose, target = "artifacts", cfg.Storage.ReleaseArtifacts
		}
		switch {
		case strings.HasPrefix(name, "storage-cutover"):
			proof := files[storageMigrationReceiptName(purpose)]
			if err := validateStorageCutoverReceipt(cfg.Environment, purpose, target, raw, proof); err != nil {
				return 0, err
			}
		case strings.HasPrefix(name, "storage-migration"):
			if identity.Destination != target.Bucket || identity.DestinationRegion != target.Region {
				return 0, errors.New("original migration target changed")
			}
		default:
			if identity.Bucket != target.Bucket || identity.Region != target.Region {
				return 0, errors.New("original storage preparation target changed")
			}
		}
		files[name] = raw
	}
	if len(files) == 0 {
		return 0, errors.New("no original storage evidence found")
	}
	// Inspect every existing destination before publishing any file.
	for name, raw := range files {
		old, err := readDeploymentStorageState(cfg.Environment, name)
		if err == nil && !bytes.Equal(old, raw) {
			return 0, errors.New("canonical storage evidence already differs")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return 0, err
	}
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "storage")} {
		if err := ensurePrivateDirectory(path); err != nil {
			return 0, err
		}
	}
	for _, name := range names {
		raw, ok := files[name]
		if !ok {
			continue
		}
		path, err := deploymentStorageStatePath(cfg.Environment, name)
		if err != nil {
			return 0, err
		}
		if err := publishOriginalStorageEvidence(path, raw); err != nil {
			old, readErr := readDeploymentStorageState(cfg.Environment, name)
			if readErr != nil || !bytes.Equal(old, raw) {
				return 0, errors.New("canonical storage publication conflicted; retain partial evidence and retry the same original input")
			}
		}
	}
	return len(files), nil
}
