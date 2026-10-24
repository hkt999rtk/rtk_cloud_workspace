package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func qualifyProductRootCustody(ctx context.Context, store secretStore, image string, root environmentCreateRegisteredIssuer, operator string) (string, error) {
	if !immutableEnvironmentImage.MatchString(image) {
		return "", environmentCreateBootstrapBlocker("same-Root signing requires its exact immutable ceremony image")
	}
	return qualifyProductRootCustodyWithRunner(store, root, operator, func(key, passphrase string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--mount", "type=bind,src="+store.Root+",dst=/store,readonly", "--entrypoint", "openssl", image, "pkey", "-in", "/store/"+filepath.ToSlash(key), "-passin", "file:/store/"+filepath.ToSlash(passphrase), "-pubout", "-outform", "DER").Output()
	})
}

func qualifyProductRootCustodyWithRunner(store secretStore, root environmentCreateRegisteredIssuer, operator string, publicKey func(string, string) ([]byte, error)) (string, error) {
	bad := func() (string, error) {
		return "", environmentCreateBootstrapBlocker("original Service Root custody, encrypted key or registered public key differs")
	}
	if root.Environment != store.Environment || root.Domain != "service" || root.Kind != "root" || !environmentBootstrapOwnerUUID.MatchString(root.ID) || !environmentBootstrapOwnerUUID.MatchString(operator) || publicKey == nil {
		return bad()
	}
	var manifest struct {
		Schema      string            `json:"schema"`
		Environment string            `json:"environment"`
		Domain      string            `json:"trust_domain"`
		Issuer      string            `json:"root_issuer_id"`
		Fingerprint string            `json:"certificate_fingerprint_sha256"`
		Operator    string            `json:"operator_user_id"`
		Paths       map[string]string `json:"paths"`
		Hashes      map[string]string `json:"file_sha256"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "pki/custody/roots", root.ID, "manifest.json"), true)
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Schema != "rtk-root-custody/v1" || manifest.Environment != store.Environment || manifest.Domain != "service" || manifest.Issuer != root.ID || manifest.Fingerprint != root.RootFingerprint || manifest.Operator != operator || len(manifest.Paths) != 4 || len(manifest.Hashes) != 4 {
		return bad()
	}
	paths := map[string]string{"key": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem")), "certificate": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "certificate.pem")), "csr": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "csr.pem")), "passphrase": "pki/custody/service-root-v1-key-passphrase"}
	for name, path := range paths {
		data, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
		if err != nil || manifest.Paths[name] != path || manifest.Hashes[name] != environmentPreflightBytesSHA256(data) || name == "certificate" && string(data) != root.Certificate {
			return bad()
		}
	}
	certificate, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil || environmentPreflightBytesSHA256(certificate.Raw) != root.RootFingerprint {
		return bad()
	}
	spki, err := publicKey(paths["key"], paths["passphrase"])
	if err != nil || !bytes.Equal(spki, certificate.RawSubjectPublicKeyInfo) {
		return bad()
	}
	return deploymentplan.Digest(deploymentplan.Object{"manifest_sha256": environmentPreflightBytesSHA256(raw), "files": manifest.Hashes, "root_spki_sha256": environmentPreflightBytesSHA256(spki)}), nil
}
