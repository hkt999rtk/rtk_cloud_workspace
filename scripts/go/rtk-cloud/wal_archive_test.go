package main

import "testing"

func TestWALArchiveArguments(t *testing.T) {
	for _, args := range [][]string{{"--name", "segment"}, {"--unknown"}, {"--source", "/tmp/source", "extra"}} {
		if runWALArchive(args) == nil {
			t.Fatal("invalid archive invocation accepted", args)
		}
	}
}
