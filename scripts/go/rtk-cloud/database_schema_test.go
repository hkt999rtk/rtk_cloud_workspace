package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaCommandForwardsArgumentsAndExitStatus(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	argsFile := filepath.Join(root, "args.txt")
	python := filepath.Join(bin, "python3")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RTK_SCHEMA_ARGS\"\nif [ \"$2\" = fail ]; then exit 7; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_WORKSPACE", root)
	t.Setenv("RTK_SCHEMA_ARGS", argsFile)
	t.Setenv("PATH", bin)

	if err := runSchema([]string{"snapshot", "--environment", "staging"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "scripts/database_schema_catalog.py") + "\nsnapshot\n--environment\nstaging\n"
	if string(got) != want {
		t.Fatalf("forwarded arguments = %q, want %q", got, want)
	}
	if err := runSchema([]string{"fail"}); err == nil || err.Error() != "exit status 7" {
		t.Fatalf("child exit status = %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if err := runSchema(nil); err == nil || !strings.Contains(err.Error(), "database schema command failed") {
		t.Fatalf("missing python error = %v", err)
	}
}
