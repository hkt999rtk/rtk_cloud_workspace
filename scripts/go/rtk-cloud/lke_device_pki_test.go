package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVideoCloudBuildUsesCanonicalPKIDockerfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy", "lke", "Dockerfile")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	context, dockerfile, cleanup, err := generatedVideoCloudDockerfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if context != dir || dockerfile != path {
		t.Fatal("ignored canonical PKI image definition")
	}
}
