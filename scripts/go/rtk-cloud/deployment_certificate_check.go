package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type certificateCheckTarget struct {
	ID           string   `json:"id"`
	Source       string   `json:"source"`
	EnabledBy    string   `json:"enabled_by,omitempty"`
	EnabledAny   []string `json:"enabled_by_any,omitempty"`
	Disabled     bool     `json:"disabled,omitempty"`
	Subject      string   `json:"subject,omitempty"`
	Purpose      string   `json:"purpose"`
	DNS          string   `json:"dns,omitempty"`
	RootFile     string   `json:"root_file,omitempty"`
	RootSHA256   string   `json:"root_sha256,omitempty"`
	ServiceRoot  bool     `json:"service_root,omitempty"`
	CRLFile      string   `json:"crl_file,omitempty"`
	CertFile     string   `json:"cert_file,omitempty"`
	KeyFile      string   `json:"key_file,omitempty"`
	RecordFile   string   `json:"record_file,omitempty"`
	Namespace    string   `json:"namespace_suffix,omitempty"`
	Secret       string   `json:"secret,omitempty"`
	CertKey      string   `json:"cert_key,omitempty"`
	KeyKey       string   `json:"key_key,omitempty"`
	CompareTo    string   `json:"compare_to,omitempty"`
	Selector     string   `json:"selector,omitempty"`
	Container    string   `json:"container,omitempty"`
	StateFile    string   `json:"state_file,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
}

type certificateCheckInventory struct {
	Version int                      `json:"version"`
	Targets []certificateCheckTarget `json:"targets"`
}

type certificateCheckRow struct {
	ID             string   `json:"id"`
	Source         string   `json:"source"`
	Status         string   `json:"status"`
	Subject        string   `json:"subject,omitempty"`
	Fingerprint    string   `json:"sha256,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	ChainExpiresAt string   `json:"chain_expires_at,omitempty"`
	DaysLeft       int      `json:"days_left"`
	Findings       []string `json:"findings"`
	Revocation     string   `json:"revocation"`
	temporalOnly   bool
}

type certificateCheckReport struct {
	Environment string                `json:"environment"`
	Stack       string                `json:"stack"`
	CheckedAt   string                `json:"checked_at"`
	Scope       string                `json:"scope"`
	Rows        []certificateCheckRow `json:"certificates"`
}

type certificateCheckMaterial struct {
	chain, key string
	keyMatches bool
	pending    bool
	subject    string
}

type certificateCheckRunner struct {
	store          secretStore
	stack          string
	now            time.Time
	warn, critical time.Duration
	localOnly      bool
	query          func(...string) ([]byte, error)
	checkedOwners  map[string]bool
}

func readCertificateCheckFile(store secretStore, relative string, private bool) ([]byte, error) {
	path, err := store.safePath(relative)
	if err != nil {
		return nil, errors.New("unsafe environment file path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (private && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("expected a regular file with private permissions")
	}
	if info.Size() > 256<<10 {
		return nil, errors.New("certificate input exceeds size limit")
	}
	return os.ReadFile(path)
}

func loadCertificateCheckInventory(workspace, environment string) ([]certificateCheckTarget, error) {
	entries := map[string]certificateCheckTarget{}
	for index, path := range []string{filepath.Join(workspace, "cloud_deploy/certificate-check.json"), filepath.Join(workspace, "cloud_env", environment, "certificate-check.json")} {
		raw, err := os.ReadFile(path)
		if index == 1 && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("certificate inspection inventory unavailable")
		}
		var inventory certificateCheckInventory
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&inventory) != nil || inventory.Version != 1 || len(inventory.Targets) == 0 {
			return nil, errors.New("invalid certificate inspection inventory")
		}
		seen := map[string]bool{}
		for _, entry := range inventory.Targets {
			if entry.ID == "" || seen[entry.ID] {
				return nil, errors.New("empty or duplicate certificate inventory ID")
			}
			seen[entry.ID] = true
			if !entry.Disabled {
				if entry.Purpose != "client" && entry.Purpose != "server" && entry.Purpose != "ca" {
					return nil, fmt.Errorf("%s: expected client/server/ca purpose", entry.ID)
				}
				if entry.Subject == "" && entry.DNS == "" {
					return nil, fmt.Errorf("%s: expected subject or DNS is required", entry.ID)
				}
				if !entry.ServiceRoot && entry.RootFile == "" {
					return nil, fmt.Errorf("%s: independent root is required", entry.ID)
				}
				switch entry.Source {
				case "file":
					if entry.CertFile == "" || (entry.Purpose != "ca" && entry.KeyFile == "") {
						return nil, fmt.Errorf("%s: incomplete file binding", entry.ID)
					}
				case "deployment":
					if entry.RecordFile == "" {
						return nil, fmt.Errorf("%s: missing record", entry.ID)
					}
				case "secret":
					if entry.Secret == "" || entry.CertKey == "" || entry.KeyKey == "" {
						return nil, fmt.Errorf("%s: incomplete Secret binding", entry.ID)
					}
				case "managed":
					if entry.Selector == "" || entry.Container == "" || !filepath.IsAbs(entry.StateFile) {
						return nil, fmt.Errorf("%s: incomplete managed owner binding", entry.ID)
					}
				default:
					return nil, fmt.Errorf("%s: unknown source", entry.ID)
				}
				if (entry.Source == "secret" || entry.Source == "managed") && !secretEnvironmentPattern.MatchString(entry.Namespace) {
					return nil, fmt.Errorf("%s: namespace suffix is required", entry.ID)
				}
			}
			entries[entry.ID] = entry
		}
	}
	targets := make([]certificateCheckTarget, 0, len(entries))
	for _, entry := range entries {
		targets = append(targets, entry)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	for _, entry := range targets {
		if entry.SupersededBy != "" {
			owner, ok := entries[entry.SupersededBy]
			if !ok || owner.Source != "managed" || owner.Subject != entry.Subject || entry.Source != "deployment" {
				return nil, fmt.Errorf("%s: provenance requires a matching managed current target", entry.ID)
			}
		}
	}
	return targets, nil
}

func (r *certificateCheckRunner) root(target certificateCheckTarget) (string, string, error) {
	path, pin := target.RootFile, target.RootSHA256
	if target.ServiceRoot {
		raw, err := readCertificateCheckFile(r.store, "pki/services/issuer.json", false)
		if err != nil {
			return "", "", err
		}
		var issuer deploymentServiceIssuer
		if json.Unmarshal(raw, &issuer) != nil || issuer.Environment != r.store.Environment || issuer.Stack != r.stack {
			return "", "", errors.New("issuer is not bound to the selected environment")
		}
		path, pin = issuer.RootCAFile, issuer.RootSHA256
		if len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
			return "", "", errors.New("Service root fingerprint is missing or invalid")
		}
	}
	raw, err := readCertificateCheckFile(r.store, path, false)
	return string(raw), pin, err
}

func (r *certificateCheckRunner) local(target certificateCheckTarget) (certificateCheckMaterial, error) {
	var material certificateCheckMaterial
	if target.Source == "deployment" {
		raw, err := readCertificateCheckFile(r.store, target.RecordFile, true)
		if err != nil {
			return material, err
		}
		var record deploymentServiceIdentity
		if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.Environment != r.store.Environment || record.Stack != r.stack || record.Subject != target.Subject {
			return material, errors.New("invalid or mismatched environment identity record")
		}
		material.chain, material.key, material.subject = record.CertificateChain, record.PrivateKey, record.Subject
		material.pending = record.CertificateChain == "" && record.CSR != ""
	} else {
		raw, err := readCertificateCheckFile(r.store, target.CertFile, false)
		if err != nil {
			return material, err
		}
		material.chain = string(raw)
		if target.KeyFile != "" {
			raw, err = readCertificateCheckFile(r.store, target.KeyFile, true)
			if err != nil {
				return material, err
			}
			material.key = string(raw)
		}
	}
	return material, nil
}

func unknownCertificateRow(target certificateCheckTarget, finding string) certificateCheckRow {
	return certificateCheckRow{ID: target.ID, Source: target.Source, Status: "UNKNOWN", DaysLeft: 0, Findings: []string{finding}, Revocation: "unchecked"}
}

func (r *certificateCheckRunner) inspect(target certificateCheckTarget, material certificateCheckMaterial) certificateCheckRow {
	row := certificateCheckRow{ID: target.ID, Source: target.Source, Status: "OK", Findings: []string{}, Revocation: "unchecked", temporalOnly: true}
	add := func(status, message string, temporal bool) {
		if (status == "CRITICAL" && row.Status != "UNKNOWN") || row.Status == "OK" {
			row.Status = status
		}
		row.Findings = append(row.Findings, message)
		row.temporalOnly = row.temporalOnly && temporal
	}
	if material.pending {
		add("WARNING", "pending enrollment or renewal requires operator inspection", false)
	}
	chain, err := pemCertificates([]byte(material.chain))
	if err != nil {
		add("CRITICAL", "missing or malformed current certificate", false)
		return row
	}
	leaf := chain[0]
	row.Subject = leaf.Subject.CommonName
	row.Fingerprint = certificateSHA256(leaf)
	row.ExpiresAt = leaf.NotAfter.UTC().Format(time.RFC3339)
	earliest := leaf.NotAfter
	for _, cert := range chain {
		if cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
		}
		if r.now.Before(cert.NotBefore) {
			add("CRITICAL", "certificate chain contains a certificate not yet valid", true)
		}
	}
	row.ChainExpiresAt = earliest.UTC().Format(time.RFC3339)
	row.DaysLeft = int(math.Floor(earliest.Sub(r.now).Hours() / 24))
	switch remaining := earliest.Sub(r.now); {
	case remaining <= 0:
		add("CRITICAL", "certificate chain expired", true)
	case remaining <= r.critical:
		add("CRITICAL", "certificate chain reaches critical expiry threshold", true)
	case remaining <= r.warn:
		add("WARNING", "certificate chain expires soon", true)
	}
	if target.Subject != "" && leaf.Subject.CommonName != target.Subject {
		add("CRITICAL", "unexpected certificate subject", false)
	}
	if material.subject != "" && material.subject != target.Subject {
		add("CRITICAL", "owner state subject differs from inventory", false)
	}
	if target.Purpose == "client" && (len(leaf.Subject.Names) != 1 || len(leaf.DNSNames)+len(leaf.IPAddresses)+len(leaf.URIs)+len(leaf.EmailAddresses) != 0) {
		add("CRITICAL", "unexpected client subject attributes or SAN", false)
	}
	usage := x509.ExtKeyUsageClientAuth
	if target.Purpose == "server" {
		usage = x509.ExtKeyUsageServerAuth
	}
	if target.Purpose == "ca" {
		usage = x509.ExtKeyUsageAny
		if !leaf.IsCA {
			add("CRITICAL", "expected a CA certificate", false)
		}
	} else {
		if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != usage || len(leaf.UnknownExtKeyUsage) != 0 {
			add("CRITICAL", "unexpected certificate purpose", false)
		}
		keyOK := material.keyMatches
		if material.key != "" {
			_, err := tls.X509KeyPair([]byte(material.chain), []byte(material.key))
			keyOK = err == nil
		}
		if !keyOK {
			add("CRITICAL", "private key is missing or does not match certificate", false)
		}
	}
	root, pin, err := r.root(target)
	if err != nil {
		row.Status = "UNKNOWN"
		row.Findings = append(row.Findings, "independent environment trust unavailable")
		row.temporalOnly = false
		return row
	}
	rootCerts, err := pemCertificates([]byte(root))
	if err != nil {
		add("CRITICAL", "invalid independent trust bundle", false)
		return row
	}
	if pin != "" && (len(rootCerts) != 1 || certificateSHA256(rootCerts[0]) != pin) {
		add("CRITICAL", "configured root fingerprint mismatch", false)
		return row
	}
	pool := x509.NewCertPool()
	for _, cert := range rootCerts {
		if !cert.IsCA {
			add("CRITICAL", "trust anchor is not a CA", false)
			return row
		}
		pool.AddCert(cert)
	}
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	verified, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, CurrentTime: r.now, KeyUsages: []x509.ExtKeyUsage{usage}, DNSName: target.DNS})
	if err != nil {
		var invalid x509.CertificateInvalidError
		temporal := errors.As(err, &invalid) && invalid.Reason == x509.Expired
		add("CRITICAL", "certificate chain validation failed", temporal)
	} else {
		// Include an issuer omitted from the presented chain in the expiry window.
		for _, cert := range verified[0] {
			if cert.NotAfter.Before(earliest) {
				earliest = cert.NotAfter
			}
		}
		row.ChainExpiresAt = earliest.UTC().Format(time.RFC3339)
		row.DaysLeft = int(math.Floor(earliest.Sub(r.now).Hours() / 24))
		if earliest.Sub(r.now) <= r.critical {
			add("CRITICAL", "verified issuer or leaf reaches critical expiry threshold", true)
		} else if earliest.Sub(r.now) <= r.warn && row.Status == "OK" {
			add("WARNING", "verified issuer or leaf expires soon", true)
		}
		if target.CRLFile != "" {
			raw, e := readCertificateCheckFile(r.store, target.CRLFile, false)
			lists, parseErr := pemCRLs(raw)
			if e != nil || parseErr != nil {
				row.Status = "UNKNOWN"
				row.Findings = append(row.Findings, "configured CRL unavailable or malformed")
				row.temporalOnly = false
			} else {
				covered := true
				for i, cert := range verified[0][:len(verified[0])-1] {
					issuer := verified[0][i+1]
					found := false
					for _, list := range lists {
						if list.CheckSignatureFrom(issuer) != nil {
							continue
						}
						found = true
						if r.now.Before(list.ThisUpdate) || !r.now.Before(list.NextUpdate) {
							add("CRITICAL", "CRL is not current", false)
						}
						for _, entry := range list.RevokedCertificateEntries {
							if entry.SerialNumber.Cmp(cert.SerialNumber) == 0 {
								add("CRITICAL", "certificate has been revoked", false)
							}
						}
					}
					covered = covered && found
				}
				if covered {
					row.Revocation = "checked"
				} else {
					row.Status = "UNKNOWN"
					row.Findings = append(row.Findings, "CRL does not cover every non-root certificate")
					row.temporalOnly = false
				}
			}
		}
	}
	if target.CompareTo != "" {
		raw, e := readCertificateCheckFile(r.store, target.CompareTo, false)
		if strings.HasSuffix(target.CompareTo, ".json") {
			var record deploymentServiceIdentity
			if json.Unmarshal(raw, &record) != nil || record.Environment != r.store.Environment || record.Stack != r.stack || record.Subject != target.Subject {
				e = errors.New("invalid record")
			}
			raw = []byte(record.CertificateChain)
		}
		local, parseErr := pemCertificates(raw)
		if e != nil || parseErr != nil {
			row.Status = "UNKNOWN"
			row.Findings = append(row.Findings, "comparison identity unavailable")
			row.temporalOnly = false
		} else if certificateSHA256(local[0]) != row.Fingerprint {
			add("CRITICAL", "installed certificate differs from saved deployment identity", false)
		}
	}
	return row
}

func (r *certificateCheckRunner) check(target certificateCheckTarget) []certificateCheckRow {
	if r.localOnly && (target.Source == "secret" || target.Source == "managed") {
		return []certificateCheckRow{unknownCertificateRow(target, "runtime source not checked in local-only mode")}
	}
	switch target.Source {
	case "file", "deployment":
		material, err := r.local(target)
		if err != nil {
			return []certificateCheckRow{unknownCertificateRow(target, "local credential missing, unsafe or invalid")}
		}
		return []certificateCheckRow{r.inspect(target, material)}
	case "secret":
		raw, err := r.query("-n", r.stack+"-"+target.Namespace, "get", "secret", target.Secret, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return []certificateCheckRow{unknownCertificateRow(target, "Kubernetes Secret access failed")}
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			row := unknownCertificateRow(target, "required Secret is missing")
			row.Status = "CRITICAL"
			return []certificateCheckRow{row}
		}
		var secret map[string]any
		if json.Unmarshal(raw, &secret) != nil {
			return []certificateCheckRow{unknownCertificateRow(target, "invalid Kubernetes response")}
		}
		chain, _ := kubernetesSecretBytes(secret, target.CertKey)
		key, _ := kubernetesSecretBytes(secret, target.KeyKey)
		return []certificateCheckRow{r.inspect(target, certificateCheckMaterial{chain: string(chain), key: string(key)})}
	case "managed":
		namespace := r.stack + "-" + target.Namespace
		raw, err := r.query("-n", namespace, "get", "pods", "-l", target.Selector, "-o", "json")
		var pods certificateCheckPods
		if err != nil || json.Unmarshal(raw, &pods) != nil {
			return []certificateCheckRow{unknownCertificateRow(target, "managed owner discovery failed")}
		}
		if len(pods.Items) == 0 {
			return []certificateCheckRow{unknownCertificateRow(target, "no managed owner Pod found")}
		}
		rows := []certificateCheckRow{}
		for _, pod := range pods.Items {
			instance := target
			instance.ID += "/" + pod.Metadata.Name
			if pod.Status.Phase != "Running" {
				rows = append(rows, unknownCertificateRow(instance, "managed owner Pod is not running"))
				continue
			}
			raw, err := r.query("-n", namespace, "exec", pod.Metadata.Name, "-c", target.Container, "--", "/usr/local/bin/pkitrust", "inspect-identity", target.StateFile)
			var observed struct {
				Version    int    `json:"version"`
				Subject    string `json:"subject"`
				Chain      string `json:"certificate_chain_pem"`
				Pending    bool   `json:"pending"`
				KeyMatches bool   `json:"key_matches"`
			}
			if err != nil || json.Unmarshal(raw, &observed) != nil || observed.Version != 1 {
				rows = append(rows, unknownCertificateRow(instance, "managed inspection unavailable; verify helper version and access"))
				continue
			}
			r.checkedOwners[namespace+"/"+pod.Metadata.Name+"/"+target.Container+"/"+target.StateFile] = true
			rows = append(rows, r.inspect(instance, certificateCheckMaterial{chain: observed.Chain, subject: observed.Subject, keyMatches: observed.KeyMatches, pending: observed.Pending}))
		}
		return rows
	}
	return []certificateCheckRow{unknownCertificateRow(target, "unsupported source")}
}

type certificateCheckPods struct {
	Items []struct {
		Metadata struct{ Name string }
		Status   struct{ Phase string }
		Spec     struct {
			Containers []struct {
				Name string
				Env  []struct {
					Name, Value string
					ValueFrom   any
				}
			}
		}
	}
}

// Unmapped managed owners are visible failures of inspection coverage, never OK.
func (r *certificateCheckRunner) checkOwnerCoverage(namespaces map[string]bool) []certificateCheckRow {
	rows := []certificateCheckRow{}
	for namespace := range namespaces {
		target := certificateCheckTarget{ID: "owner-inventory/" + namespace, Source: "managed"}
		raw, err := r.query("-n", namespace, "get", "pods", "-o", "json")
		var pods certificateCheckPods
		if err != nil || json.Unmarshal(raw, &pods) != nil {
			rows = append(rows, unknownCertificateRow(target, "could not verify managed-owner inventory coverage"))
			continue
		}
		for _, pod := range pods.Items {
			for _, container := range pod.Spec.Containers {
				for _, setting := range container.Env {
					if !strings.HasSuffix(setting.Name, "_IDENTITY_STATE") {
						continue
					}
					key := namespace + "/" + pod.Metadata.Name + "/" + container.Name + "/" + setting.Value
					if !r.checkedOwners[key] {
						instance := target
						instance.ID = key
						rows = append(rows, unknownCertificateRow(instance, "managed identity state is not covered by a successful inventory inspection"))
					}
				}
			}
		}
	}
	return rows
}

func (r *certificateCheckRunner) run(targets []certificateCheckTarget, env map[string]string) certificateCheckReport {
	report := certificateCheckReport{Environment: r.store.Environment, Stack: r.stack, CheckedAt: r.now.UTC().Format(time.RFC3339), Scope: "local-and-runtime", Rows: []certificateCheckRow{}}
	if r.localOnly {
		report.Scope = "local-only"
	}
	namespaces := map[string]bool{r.stack + "-video-cloud": true, r.stack + "-account-manager": true, r.stack + "-secrets": true}
	for _, target := range targets {
		enabled, valid := certificateTargetEnabled(target, env)
		if !valid {
			report.Rows = append(report.Rows, unknownCertificateRow(target, "selected environment feature flag is absent or invalid"))
			continue
		}
		if !enabled {
			report.Rows = append(report.Rows, certificateCheckRow{ID: target.ID, Source: target.Source, Status: "DISABLED", Findings: []string{"disabled by environment inventory/configuration"}, Revocation: "unchecked"})
			continue
		}
		if target.Namespace != "" {
			namespaces[r.stack+"-"+target.Namespace] = true
		}
		report.Rows = append(report.Rows, r.check(target)...)
	}
	for _, target := range targets {
		if target.SupersededBy == "" {
			continue
		}
		checked := false
		healthy := true
		for _, row := range report.Rows {
			if strings.HasPrefix(row.ID, target.SupersededBy+"/") {
				checked = true
				healthy = healthy && (row.Status == "OK" || row.Status == "WARNING")
			}
		}
		if checked && healthy {
			for i := range report.Rows {
				row := &report.Rows[i]
				if row.ID == target.ID && row.temporalOnly && row.Status != "OK" {
					row.Status = "INFO"
					row.Findings = append(row.Findings, "historical initial certificate; managed current credential checked separately")
				}
			}
		}
	}
	if !r.localOnly {
		report.Rows = append(report.Rows, r.checkOwnerCoverage(namespaces)...)
	}
	sort.Slice(report.Rows, func(i, j int) bool { return report.Rows[i].ID < report.Rows[j].ID })
	return report
}

func certificateCheckExit(report certificateCheckReport) int {
	code, checked := 0, false
	for _, row := range report.Rows {
		switch row.Status {
		case "UNKNOWN":
			code = 3
		case "CRITICAL":
			if code < 2 {
				code = 2
			}
		case "WARNING":
			if code < 1 {
				code = 1
			}
		}
		checked = checked || row.Status != "DISABLED"
	}
	if !checked {
		return 3
	}
	return code
}

func writeCertificateCheckReport(out io.Writer, report certificateCheckReport, format string) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(out, "Environment: %s  Stack: %s  UTC: %s  Scope: %s\n", report.Environment, report.Stack, report.CheckedAt, report.Scope)
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tIDENTITY / SOURCE\tSUBJECT / SHA256\tEXPIRES UTC\tDAYS\tFINDINGS")
	for _, row := range report.Rows {
		fmt.Fprintf(writer, "%s\t%s [%s]\t%s / %.12s\t%s\t%d\t%s\n", row.Status, row.ID, row.Source, strings.Map(func(c rune) rune {
			if c < 32 || c == 127 {
				return -1
			}
			return c
		}, row.Subject), row.Fingerprint, row.ChainExpiresAt, row.DaysLeft, strings.Join(row.Findings, "; "))
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Certificate inspection only; registry admission and in-process TLS reload are not verified. Revocation is unchecked unless a complete signed CRL bundle is configured.")
	return nil
}

func runDeploymentCertificateCheck(args []string) (result error) {
	defer func() {
		if result != nil {
			var code exitCode
			if !errors.As(result, &code) {
				fmt.Fprintln(os.Stderr, "certificate check:", result)
				result = exitCode(3)
			}
		}
	}()
	fs := flag.NewFlagSet("deployment certificate-check", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	format := fs.String("format", "table", "table or json")
	localOnly := fs.Bool("local-only", false, "inspect local files only; runtime remains UNKNOWN")
	warnDays := fs.Int("warn-days", 7, "expiry warning threshold in days")
	criticalDays := fs.Int("critical-days", 3, "critical expiry threshold in days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" || (*format != "table" && *format != "json") || *criticalDays < 0 || *warnDays <= *criticalDays || *warnDays > 3650 {
		return errors.New("environment, valid output format and 0 <= critical-days < warn-days <= 3650 are required")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	store, err := newSecretStore("", *environment)
	if err != nil {
		return err
	}
	targets, err := loadCertificateCheckInventory(cfg.Workspace, *environment)
	if err != nil {
		return err
	}
	runner := certificateCheckRunner{store: store, stack: cfg.Values["CLOUD_STACK_NAME"], now: time.Now(), warn: time.Duration(*warnDays) * 24 * time.Hour, critical: time.Duration(*criticalDays) * 24 * time.Hour, localOnly: *localOnly, checkedOwners: map[string]bool{}}
	runner.query = func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Explicit binding prevents inherited kubeconfig aliases from changing target.
		command := exec.CommandContext(ctx, lkeKubectl(), append([]string{"--kubeconfig", store.KubeconfigPath(), "--request-timeout=10s"}, args...)...)
		raw, err := command.Output()
		if err != nil || len(raw) > 4<<20 {
			return nil, errors.New("Kubernetes inspection unavailable")
		}
		return raw, nil
	}
	flags := appendMap(cfg.Values, cfg.AdapterValues)
	for _, target := range targets {
		for _, flag := range certificateTargetFlags(target) {
			raw, err := readCertificateCheckFile(store, filepath.Join("operator/env", flag), true)
			if err == nil {
				flags[flag] = strings.TrimSpace(string(raw))
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("cannot read selected environment feature flag %s", flag)
			}
		}
	}
	report := runner.run(targets, flags)
	if err := writeCertificateCheckReport(os.Stdout, report, *format); err != nil {
		return err
	}
	if code := certificateCheckExit(report); code != 0 {
		return exitCode(code)
	}
	return nil
}

func certificateTargetFlags(target certificateCheckTarget) []string {
	flags := append([]string(nil), target.EnabledAny...)
	if target.EnabledBy != "" {
		flags = append(flags, target.EnabledBy)
	}
	return flags
}

func certificateTargetEnabled(target certificateCheckTarget, env map[string]string) (bool, bool) {
	if target.Disabled {
		return false, true
	}
	flags := certificateTargetFlags(target)
	if len(flags) == 0 {
		return true, true
	}
	enabled := false
	for _, key := range flags {
		if !operatorKeyPattern.MatchString(key) || (env[key] != "true" && env[key] != "false") {
			return false, false
		}
		enabled = enabled || env[key] == "true"
	}
	return enabled, true
}
