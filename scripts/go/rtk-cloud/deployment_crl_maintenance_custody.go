package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

type maintenanceCustody struct {
	Schema      string            `json:"schema"`
	Environment string            `json:"environment"`
	Domain      string            `json:"trust_domain"`
	Issuer      string            `json:"root_issuer_id"`
	Fingerprint string            `json:"certificate_fingerprint_sha256"`
	Operator    string            `json:"operator_user_id"`
	Reference   string            `json:"signer_reference"`
	KeyFormat   string            `json:"key_format"`
	Paths       map[string]string `json:"paths"`
	Hashes      map[string]string `json:"file_sha256"`
}

func verifyMaintenanceCustody(store secretStore, root environmentCreateRegisteredIssuer) (maintenanceCustody, error) {
	var m maintenanceCustody
	fail := func() (maintenanceCustody, error) {
		return m, errors.New("original encrypted Root custody is missing or mismatched; preserve identity and recover original material")
	}
	if root.Environment != store.Environment || !environmentBootstrapOwnerUUID.MatchString(root.ID) {
		return fail()
	}
	if maintenanceRead(store, "pki/custody/roots/"+root.ID+"/manifest.json", &m) != nil {
		return fail()
	}
	if m.Schema != "rtk-root-custody/v1" || m.Environment != store.Environment || m.Issuer != root.ID || m.Domain != root.Domain || m.Fingerprint != root.RootFingerprint || m.KeyFormat != "encrypted-pkcs8-pem" || len(m.Paths) != 4 || len(m.Hashes) != 4 {
		return fail()
	}
	for name, want := range map[string]string{"PKI_OPERATOR_USER_ID": m.Operator, "PKI_OPERATOR_SIGNER_REF": m.Reference} {
		path, e := maintenancePath(store, "operator/env/"+name)
		if e != nil {
			return fail()
		}
		raw, e := readBoundedEnvironmentFile(path, true)
		if e != nil || want == "" || strings.TrimSpace(string(raw)) != want {
			return fail()
		}
	}
	// The issuer's logical signer and registered root identity must agree with
	// custody; paths are obtained from the manifest rather than a domain version.
	if root.Reference != "" && m.Reference != root.Reference {
		return fail()
	}
	for name, relative := range m.Paths {
		if relative == "" || filepath.ToSlash(filepath.Clean(relative)) != relative || !strings.HasPrefix(relative, "pki/custody/") {
			return fail()
		}
		path, e := maintenancePath(store, relative)
		if e != nil {
			return fail()
		}
		raw, e := readBoundedEnvironmentFile(path, true)
		if e != nil || m.Hashes[name] != environmentPreflightBytesSHA256(raw) {
			return fail()
		}
		switch name {
		case "certificate":
			if !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace([]byte(root.Certificate))) {
				return fail()
			}
		case "key":
			b, rest := pem.Decode(raw)
			if b == nil || b.Type != "ENCRYPTED PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
				return fail()
			}
		case "csr", "passphrase":
		default:
			return fail()
		}
	}
	return m, nil
}

func signMaintenanceCRL(ctx context.Context, store secretStore, image string, item crlMaintenanceIssuer, request createRootCRLRequest, candidate string) (createRootCRLRecord, error) {
	var record createRootCRLRecord
	custody, err := verifyMaintenanceCustody(store, item.Issuer)
	if err != nil {
		return record, err
	}
	for name, value := range map[string]any{"issuer.json": item.Issuer.Document, "request.json": request} {
		var old json.RawMessage
		if err := maintenanceRead(store, candidate+"/"+name, &old); errors.Is(err, os.ErrNotExist) {
			if err := maintenanceWrite(store, candidate+"/"+name, value); err != nil {
				return record, err
			}
		} else if err != nil || deploymentplan.Digest(old) != deploymentplan.Digest(value) {
			return record, errors.New("original signing input differs; preserve candidate")
		}
	}
	signedPath, err := maintenancePath(store, candidate+"/signed/revocations.pem")
	if err != nil {
		return record, err
	}
	_, statErr := os.Lstat(signedPath)
	var raw []byte
	if errors.Is(statErr, os.ErrNotExist) {
		if err := runEnvironmentCreatePKICeremony(ctx, store, image, "crl", "--issuer", "/store/"+candidate+"/issuer.json", "--crl-request", "/store/"+candidate+"/request.json", "--key", "/store/"+custody.Paths["key"], "--passphrase-file", "/store/"+custody.Paths["passphrase"], "--expected-request-sha256", deploymentplan.Digest(request), "--out", "/store/"+candidate+"/signed"); err != nil {
			return record, err
		}
	}
	raw, err = readBoundedEnvironmentFile(signedPath, true)
	if err != nil {
		return record, errors.New("original signed CRL cannot be read")
	}
	after, err := verifyMaintenanceCustody(store, item.Issuer)
	if err != nil || deploymentplan.Digest(custody) != deploymentplan.Digest(after) {
		return record, errors.New("original custody changed during signing")
	}
	record, err = deviceRootCRLProviderRecord(item.Issuer, raw)
	if err != nil {
		return record, err
	}
	return record, validateMaintenanceSuccessor(store.Environment, item, request, record, time.Now())
}
