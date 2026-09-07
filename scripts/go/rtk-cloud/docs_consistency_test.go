package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDesignConsistencyRejectsObsoleteGuidance(t *testing.T) {
	examples := []string{
		"CSR subject app-brand-cloud-user:old-user",
		"`target_type` is `brand_cloud_user` or `end_user`",
		"Account Manager is the\nphase-one owner of commercial accounts",
		"uses the channel rather than a direct server-to-server synchronization path",
		"Current default is NATS JetStream or equivalent",
		"Redis hot path with Postgres flush",
		"runtime/\n  state/{kubeconfig.yaml,topology.json}",
		"The shared profile normally contains credentials",
		"POST /api/device/provision_certificate",
		"claims include `scope`, `issued_at`, and `expires_at`",
		"When sources disagree, this document wins until the conflict is corrected",
	}
	if len(examples) != len(docsConsistencyRules) {
		t.Fatal("missing regression example")
	}
	for i, rule := range docsConsistencyRules {
		t.Run(rule.name, func(t *testing.T) {
			re := regexp.MustCompile(rule.pattern)
			if !re.MatchString(activeDesignText(examples[i])) {
				t.Fatal("obsolete guidance was accepted")
			}
			historical := "Current behavior follows canonical contracts.\n## Historical Evidence\n" + examples[i]
			if re.MatchString(activeDesignText(historical)) {
				t.Fatal("explicit historical evidence was rejected")
			}
		})
	}
}

func TestDesignConsistencyCannotHideActiveGuidanceBehindSubheading(t *testing.T) {
	text := "## Current Flow\n### Historical naming\nAccount Manager is the phase-one owner"
	if !regexp.MustCompile(docsConsistencyRules[2].pattern).MatchString(activeDesignText(text)) {
		t.Fatal("ordinary subheading bypassed the current design check")
	}
}

func TestCheckDocsConsistencyReadsSourcesAndReportsFailures(t *testing.T) {
	workspace := t.TempDir()
	for _, rule := range docsConsistencyRules {
		for _, path := range rule.paths {
			full := filepath.Join(workspace, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("Current behavior follows canonical contracts.\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := newCheck()
	checkDocsConsistency(check, workspace)
	if check.failures != 0 {
		t.Fatalf("valid sources: %d failures", check.failures)
	}
	path := filepath.Join(workspace, docsConsistencyRules[0].paths[0])
	if err := os.WriteFile(path, []byte("CSR subject app-brand-cloud-user:old-user\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check = newCheck()
	checkDocsConsistency(check, workspace)
	if check.failures != 1 {
		t.Fatalf("obsolete source: got %d failures, want 1", check.failures)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check = newCheck()
	checkDocsConsistency(check, workspace)
	if check.failures != 1 {
		t.Fatalf("missing source: got %d failures, want 1", check.failures)
	}
}

func TestRunDocsCheckReportsObsoleteDesignSource(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	writeFile(t, filepath.Join(workspace, ".gitmodules"), "")
	path := "repos/rtk_cloud_contracts_doc/api_usage.md"
	mkdirAll(t, filepath.Dir(filepath.Join(workspace, path)))
	writeFile(t, filepath.Join(workspace, path), "CSR subject app-brand-cloud-user:old-user\n")
	stdout, stderr, err := captureOutput(func() error { return runDocsCheck(nil) })
	if err == nil {
		t.Fatal("docs-check accepted an incomplete workspace with obsolete guidance")
	}
	if !strings.Contains(stdout+stderr, "global app CSR identity: obsolete guidance in "+path) {
		t.Fatalf("docs-check did not report the design conflict: %s%s", stdout, stderr)
	}
}
