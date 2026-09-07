package main

import "testing"

func TestBaseBackupArguments(t *testing.T) {
	for _, args := range [][]string{{"invalid"}, {"create"}, {"restore"}, {"create", "--destination", "/tmp/target"}, {"restore", "--config", "missing", "--id", "backup"}, {"create", "--unknown"}} {
		if runBaseBackup(args) == nil {
			t.Fatal("invalid base backup invocation accepted", args)
		}
	}
}
