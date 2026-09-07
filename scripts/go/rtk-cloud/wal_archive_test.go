package main

import "testing"

func TestWALArchiveArguments(t *testing.T) {
	for _, args := range [][]string{{"--name", "segment"}, {"--unknown"}, {"--source", "/tmp/source", "extra"}} {
		if runWALArchive(args) == nil {
			t.Fatal("invalid archive invocation accepted", args)
		}
	}
}

func TestWALRestoreArguments(t *testing.T) {
	for _, args := range [][]string{{"--name", "segment"}, {"--unknown"}, {"--source", "/tmp/source"}, {"--destination", "/tmp/dest", "extra"}, {"--config", "missing", "--name", "segment", "--destination", "/tmp/dest"}} {
		if runWALRestore(args) == nil {
			t.Fatal("invalid restore invocation accepted", args)
		}
	}
}

func TestWALArchiveConfigArguments(t *testing.T) {
	for _, args := range [][]string{{"--config", "missing"}, {"--unknown"}, {"--output", "/tmp/test", "extra"}} {
		if runWALArchiveConfig(args) == nil {
			t.Fatal("invalid archive config arguments accepted", args)
		}
	}
}
