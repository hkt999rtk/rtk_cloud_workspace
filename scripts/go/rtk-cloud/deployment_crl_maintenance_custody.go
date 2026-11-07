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
	// The manifest records the authenticated creator of this original issuer.
	// PKI_OPERATOR_USER_ID is pre-bootstrap intent; native creation binds that
	// intent to the Account Manager principal without rewriting the input file.
	if !environmentBootstrapOwnerUUID.MatchString(m.Operator) || m.Operator != deploymentplan.String(root.Document["created_by"]) {
		return m, errors.New("original Root custody creator differs from the registered issuer")
	}
	for name, want := range map[string]string{"PKI_OPERATOR_SIGNER_REF": m.Reference} {
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
	// Preserve legacy and interrupted native output. A complete signed response
	// is reusable even if the process died before saving its journal. The native
	// ceremony owns exclusive creation, so every new attempt gets a fresh path.
	attemptsPath, err := maintenancePath(store, candidate+"/attempts/.probe")
	if err != nil {
		return record, err
	}
	attemptsPath = filepath.Dir(attemptsPath)
	paths := []string{candidate + "/signed/revocations.pem"}
	attempts, err := os.ReadDir(attemptsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	for _, attempt := range attempts {
		if !attempt.IsDir() || !strings.HasPrefix(attempt.Name(), "attempt-") {
			return record, errors.New("unexpected signing attempt evidence")
		}
		paths = append(paths, candidate+"/attempts/"+attempt.Name()+"/signed/revocations.pem")
	}
	var raw []byte
	for _, relative := range paths {
		path, err := maintenancePath(store, relative)
		if err != nil {
			return record, err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return record, err
		}
		data, err := readBoundedEnvironmentInput(path, true, true)
		if err != nil {
			return record, err
		}
		if block, _ := pem.Decode(data); block == nil {
			continue
		} // interrupted exclusive write
		previous, err := deviceRootCRLProviderRecord(item.Issuer, data)
		if err != nil {
			return record, errors.New("preserved signing response is invalid; inspect original evidence")
		}
		if err := validateMaintenanceSuccessor(store.Environment, item, request, previous, time.Now()); err != nil {
			return record, err
		}
		if len(raw) != 0 && record.Digest != previous.Digest {
			return record, errors.New("multiple different signed candidates; preserve original evidence")
		}
		raw, record = data, previous
	}
	if len(raw) == 0 {
		if err := os.MkdirAll(attemptsPath, 0700); err != nil {
			return record, err
		}
		attempt, err := os.MkdirTemp(attemptsPath, "attempt-")
		if err != nil {
			return record, err
		}
		relative := candidate + "/attempts/" + filepath.Base(attempt) + "/signed"
		if err := runEnvironmentCreatePKICeremony(ctx, store, image, "crl", "--issuer", "/store/"+candidate+"/issuer.json", "--crl-request", "/store/"+candidate+"/request.json", "--key", "/store/"+custody.Paths["key"], "--passphrase-file", "/store/"+custody.Paths["passphrase"], "--expected-request-sha256", deploymentplan.Digest(request), "--out", "/store/"+relative); err != nil {
			return record, err
		}
		signedPath, err := maintenancePath(store, relative+"/revocations.pem")
		if err != nil {
			return record, err
		}
		raw, err = readBoundedEnvironmentFile(signedPath, true)
		if err != nil {
			return record, errors.New("original signed CRL cannot be read")
		}
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
