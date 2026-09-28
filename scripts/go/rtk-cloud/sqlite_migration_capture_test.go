package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const captureTestUID = "11111111-2222-3333-4444-555555555555"

func captureFixture(t *testing.T) (string, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	output := filepath.Join(base, "output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(source, "rtk-cloud-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE capture_test (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"analytics.db", "connectplus.db"} {
		data, err := os.ReadFile(filepath.Join(source, "rtk-cloud-admin.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" get deployment "*)
    if [[ "${FAKE_CAPTURE_MOUNT:-}" == true ]]; then
      printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{"volumes":[{"name":"sqlite-data"}]}}}}'
    else
      printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{}}}}'
    fi
    exit 0 ;;
  *" get pods "*)
    count=0
    [[ ! -f "$FAKE_POD_COUNT" ]] || count="$(cat "$FAKE_POD_COUNT")"
    count="$((count+1))"
    printf '%s' "$count" > "$FAKE_POD_COUNT"
    uid='11111111-2222-3333-4444-555555555555'
    if [[ "${FAKE_CAPTURE_UID_CHANGE:-}" == true && "$count" -gt 1 ]]; then uid='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'; fi
    printf '{"items":[{"metadata":{"name":"source-pod","uid":"%s"},"status":{"phase":"Running"}}]}\n' "$uid"
    exit 0 ;;
esac
while (($#)); do
  if [[ "$1" == -- ]]; then shift; break; fi
  shift
done
[[ "$1" == sh && "$2" == -c ]] || exit 2
script="$3"
shift 5
case "$script" in
  *'find '* )
    if [[ -n "${FAKE_CAPTURE_LIST:-}" ]]; then printf '%s\n' "$FAKE_CAPTURE_LIST"
    elif [[ "${FAKE_CAPTURE_NO_FILES:-}" == true ]]; then :
    elif [[ "${FAKE_CAPTURE_FRONTEND:-}" == true ]]; then printf '/data/analytics.db\n/data/connectplus.db\n'
    else printf '/app/data/rtk-cloud-admin.db\n'; fi ;;
  *'sha256sum '* )
    count=0
    [[ ! -f "$FAKE_HASH_COUNT" ]] || count="$(cat "$FAKE_HASH_COUNT")"
    count="$((count+1))"
    printf '%s' "$count" > "$FAKE_HASH_COUNT"
    if [[ "${FAKE_CAPTURE_BAD_HASH:-}" == true ]]; then
      printf 'not-a-sha256  rtk-cloud-admin.db\n'
    elif [[ "${FAKE_CAPTURE_HASH_CHANGE:-}" == true && "$count" -gt 1 ]]; then
      printf '%064d  rtk-cloud-admin.db\n' 0
    else
      (cd "$FAKE_SOURCE" && sha256sum "$@")
    fi ;;
  *'tar -cf '* )
    if [[ "${FAKE_CAPTURE_TAR_CORRUPT:-}" == true ]]; then printf 'changed' >> "$FAKE_SOURCE/rtk-cloud-admin.db"; fi
    if [[ "${FAKE_CAPTURE_TAR_EXTRA:-}" == true ]]; then
      printf 'extra' > "$FAKE_SOURCE/extra.db"
      COPYFILE_DISABLE=1 tar -C "$FAKE_SOURCE" -cf - "$@" extra.db
    else
      COPYFILE_DISABLE=1 tar -C "$FAKE_SOURCE" -cf - "$@"
    fi ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SOURCE", source)
	t.Setenv("FAKE_POD_COUNT", filepath.Join(base, "pod-count"))
	t.Setenv("FAKE_HASH_COUNT", filepath.Join(base, "hash-count"))
	kubeconfig := filepath.Join(base, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	return source, output, kubeconfig
}

func captureArgs(output, kubeconfig string) []string {
	return []string{"--stack", "video-cloud-staging", "--kubeconfig", kubeconfig, "--workload", "cloud-admin", "--source-pod-uid", captureTestUID, "--output-dir", output}
}

func TestSQLiteMigrationCaptureChecksSourceAndCopy(t *testing.T) {
	source, output, kubeconfig := captureFixture(t)
	if err := runSQLiteMigrationCapture(captureArgs(output, kubeconfig)); err != nil {
		t.Fatal(err)
	}
	want, err := sqliteMigrationHash(filepath.Join(source, "rtk-cloud-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sqliteMigrationHash(filepath.Join(output, "rtk-cloud-admin.db"))
	if err != nil || got != want {
		t.Fatalf("copied database hash = %s, error = %v; want %s", got, err, want)
	}
	if info, err := os.Stat(filepath.Join(output, "rtk-cloud-admin.db")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private copy mode: %v, %v", info, err)
	}
}

func TestSQLiteMigrationCaptureFrontendCopiesBothDatabases(t *testing.T) {
	source, output, kubeconfig := captureFixture(t)
	t.Setenv("FAKE_CAPTURE_FRONTEND", "true")
	args := captureArgs(output, kubeconfig)
	args[5] = "frontend"
	if err := runSQLiteMigrationCapture(args); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"analytics.db", "connectplus.db"} {
		want, err := sqliteMigrationHash(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := sqliteMigrationHash(filepath.Join(output, name))
		if err != nil || got != want {
			t.Fatalf("%s hash = %s, error = %v; want %s", name, got, err, want)
		}
	}
}

func TestSQLiteMigrationCaptureRejectsChangedSource(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"pod UID", "FAKE_CAPTURE_UID_CHANGE"},
		{"source hash", "FAKE_CAPTURE_HASH_CHANGE"},
		{"existing mount", "FAKE_CAPTURE_MOUNT"},
		{"empty source", "FAKE_CAPTURE_NO_FILES"},
		{"invalid hash", "FAKE_CAPTURE_BAD_HASH"},
		{"changed tar bytes", "FAKE_CAPTURE_TAR_CORRUPT"},
		{"extra tar file", "FAKE_CAPTURE_TAR_EXTRA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, output, kubeconfig := captureFixture(t)
			t.Setenv(tc.env, "true")
			if err := runSQLiteMigrationCapture(captureArgs(output, kubeconfig)); err == nil {
				t.Fatal("unsafe source was accepted")
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed capture left plaintext: %v, %v", entries, err)
			}
		})
	}
}

func TestSQLiteMigrationCaptureRejectsUnexpectedSourceName(t *testing.T) {
	_, output, kubeconfig := captureFixture(t)
	t.Setenv("FAKE_CAPTURE_LIST", "/app/data/../rtk-cloud-admin.db")
	if err := runSQLiteMigrationCapture(captureArgs(output, kubeconfig)); err == nil || !strings.Contains(err.Error(), "filename") {
		t.Fatalf("expected source filename rejection, got %v", err)
	}
}

func TestSQLiteMigrationCaptureRejectsInvalidArguments(t *testing.T) {
	_, output, kubeconfig := captureFixture(t)
	for _, args := range [][]string{
		{"--stack", "bad/stack", "--kubeconfig", kubeconfig, "--workload", "cloud-admin", "--source-pod-uid", captureTestUID, "--output-dir", output},
		{"--stack", "video-cloud-staging", "--kubeconfig", kubeconfig, "--workload", "unknown", "--source-pod-uid", captureTestUID, "--output-dir", output},
		{"--stack", "video-cloud-staging", "--kubeconfig", filepath.Join(output, "missing"), "--workload", "cloud-admin", "--source-pod-uid", captureTestUID, "--output-dir", output},
	} {
		if err := runSQLiteMigrationCapture(args); err == nil {
			t.Fatalf("unsafe arguments accepted: %v", args)
		}
	}
}

func TestSQLiteMigrationCaptureRequiresEmptyPrivateOutput(t *testing.T) {
	_, output, kubeconfig := captureFixture(t)
	if err := os.WriteFile(filepath.Join(output, "existing"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSQLiteMigrationCapture(captureArgs(output, kubeconfig)); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected nonempty output rejection, got %v", err)
	}
}
