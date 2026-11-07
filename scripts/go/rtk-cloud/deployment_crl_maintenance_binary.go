package main

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maintenanceTrustBinaryLimit = 64 << 20

// Every old consumer can use the verified helper's native installer without a
// workload upgrade. Temporary public material stays beside its original CRL
// state because hardened containers need not have a writable /tmp.
const maintenanceInstallScript = `umask 077
directory=$(mktemp -d "$(dirname "$1")/.rtk-crl-maintenance.XXXXXXXX")
trap 'rm -rf "$directory"' EXIT
IFS= read -r tool
IFS= read -r issuer
IFS= read -r record
printf '%s' "$tool" | base64 -d > "$directory/pkitrust"
printf '%s  %s\n' "$2" "$directory/pkitrust" | sha256sum -c - >/dev/null
chmod 0700 "$directory/pkitrust"
printf '%s' "$issuer" | base64 -d > "$directory/issuer.json"
printf '%s' "$record" | base64 -d > "$directory/crl.json"
"$directory/pkitrust" apply-crl "$directory/issuer.json" "$directory/crl.json" "$1"`

type maintenanceTrustBinaryEvidence struct {
	Image  string `json:"image"`
	Digest string `json:"sha256"`
}

func validateMaintenanceTrustBinary(raw []byte) error {
	file, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return errors.New("CI pkitrust is not a Linux executable")
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return errors.New("CI pkitrust is not Linux amd64")
	}
	for _, p := range file.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			return errors.New("CI pkitrust must be statically linked")
		}
	}
	return nil
}

func (r *maintenanceRuntime) nativeTrustBinary(ctx context.Context) (binary []byte, resultErr error) {
	if len(r.trustBinary) > 0 {
		return r.trustBinary, nil
	}
	if r.image == "" {
		if r.ensureImage == nil {
			return nil, errors.New("native installer CI image resolver unavailable")
		}
		var err error
		r.image, err = r.ensureImage(ctx)
		if err != nil {
			return nil, err
		}
	}
	if !immutableEnvironmentImage.MatchString(r.image) {
		return nil, errors.New("native installer requires exact immutable CI image")
	}
	relative := crlMaintenanceBase + "/helpers/" + environmentPreflightBytesSHA256([]byte(r.image)) + "/pkitrust"
	path, err := maintenancePath(r.store, relative)
	if err != nil {
		return nil, err
	}
	var proof maintenanceTrustBinaryEvidence
	if err := maintenanceRead(r.store, relative+".json", &proof); err == nil {
		raw, err := readBoundedEnvironmentInputLimit(path, true, false, maintenanceTrustBinaryLimit)
		if err != nil || proof.Image != r.image || proof.Digest != environmentPreflightBytesSHA256(raw) {
			return nil, errors.New("retained native installer differs from original verified image")
		}
		if err := validateMaintenanceTrustBinary(raw); err != nil {
			return nil, err
		}
		r.trustBinary = raw
		return raw, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Create but never start a container; no environment material is mounted.
	command := exec.CommandContext(ctx, "docker", "create", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--entrypoint", "/app/pkitrust", r.image)
	raw, err := command.Output()
	container := strings.TrimSpace(string(raw))
	if err != nil || len(container) != 64 || strings.Trim(container, "0123456789abcdef") != "" {
		return nil, errors.New("verified CI installer image cannot be opened")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := exec.CommandContext(cleanup, "docker", "rm", container).Run(); err != nil && resultErr == nil {
			resultErr = errors.New("native installer image container cleanup failed")
		}
	}()
	file, err := os.CreateTemp(filepath.Dir(path), ".pkitrust-*")
	if err != nil {
		return nil, err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := exec.CommandContext(ctx, "docker", "cp", container+":/app/pkitrust", temporary).Run(); err != nil {
		return nil, errors.New("verified CI image lacks /app/pkitrust; publish the native installer package")
	}
	info, err := os.Lstat(temporary)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maintenanceTrustBinaryLimit {
		return nil, errors.New("CI installer is not a bounded regular binary")
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		return nil, err
	}
	binary, err = readBoundedEnvironmentInputLimit(temporary, true, false, maintenanceTrustBinaryLimit)
	if err != nil {
		return nil, err
	}
	if err := validateMaintenanceTrustBinary(binary); err != nil {
		return nil, err
	}
	// A prior interrupted extraction may have retained bytes before its receipt.
	// Qualify those bytes against this exact image instead of silently replacing.
	if _, err := os.Lstat(path); err == nil {
		old, err := readBoundedEnvironmentInputLimit(path, true, false, maintenanceTrustBinaryLimit)
		if err != nil || !bytes.Equal(old, binary) {
			return nil, errors.New("unqualified retained installer differs from exact CI image")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if err := os.Rename(temporary, path); err != nil {
		return nil, err
	}
	if err := maintenanceWrite(r.store, relative+".json", maintenanceTrustBinaryEvidence{Image: r.image, Digest: environmentPreflightBytesSHA256(binary)}); err != nil {
		return nil, err
	}
	r.trustBinary = binary
	return binary, nil
}
