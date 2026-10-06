package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Explicit isolated fixture only. kubectl's exec transport is bridged to the
// named disposable PostgreSQL, so the actual backend SQL runs against native
// Account Manager migrations/seal state without touching an environment API.
func TestNativeAccountBootstrapSealChecksOriginalPostgres(t *testing.T) {
	container, systemID := os.Getenv("RTK_ACCOUNT_BOOTSTRAP_TEST_CONTAINER"), os.Getenv("RTK_ACCOUNT_BOOTSTRAP_TEST_SYSTEM_ID")
	if container == "" || systemID == "" {
		t.Skip("set the named isolated Account Manager PostgreSQL fixture and its actual system identifier")
	}
	if !strings.HasPrefix(container, "rtk-account-bootstrap-") || strings.ContainsAny(container, " /\\\r\n") {
		t.Fatal("isolated bootstrap fixture required")
	}
	kube := frozenKubernetesFixture(t)
	raw, _ := yaml.Marshal(kube)
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	profile := postgresBackupTestConfig(t)
	profile.Environment, profile.Stack, profile.Namespace = "dev", "video-cloud-dev", "video-cloud-dev-platform"
	profile.Worker.Source.SystemIdentifier = systemID
	directory := t.TempDir()
	script := `#!/bin/sh
case "$*" in
 *' exec '*'postgresql-0'*'rtk_account_manager'*)
  for arg in "$@"; do query="$arg"; done
  exec docker exec "$RTK_ACCOUNT_BOOTSTRAP_TEST_CONTAINER" psql -X -U postgres -d rtk_account_manager -tA -v ON_ERROR_STOP=1 -c "$query";;
 *) exit 1;;
esac
`
	writeTestFile(t, filepath.Join(directory, "kubectl"), script)
	os.Chmod(filepath.Join(directory, "kubectl"), 0755)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if seal, err := backend.accountBootstrapSeal(context.Background(), profile); err != nil || !seal.Sealed || seal.SystemIdentifier != systemID {
		t.Fatal("native permanent seal or database identity unavailable", err)
	}
	profile.Worker.Source.SystemIdentifier = "123456789"
	if _, err := backend.accountBootstrapSeal(context.Background(), profile); err == nil {
		t.Fatal("different database identity accepted")
	}
}
