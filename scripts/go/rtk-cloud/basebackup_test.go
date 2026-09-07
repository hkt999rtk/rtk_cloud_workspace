package main

import "testing"

func TestBaseBackupArguments(t *testing.T) {
	for _, args := range [][]string{{"invalid"}, {"observe"}, {"observe", "--config", "missing", "--id", "fixture"}, {"observe", "--identity", "forbidden"}, {"scheduled"}, {"scheduled", "--id", "wrong"}, {"scheduled", "--config", "missing"}, {"create", "--schedule", "wrong"}, {"create"}, {"restore"}, {"create", "--destination", "/tmp/target"}, {"restore", "--config", "missing", "--id", "backup"}, {"create", "--unknown"}} {
		if runBaseBackup(args) == nil {
			t.Fatal("invalid base backup invocation accepted", args)
		}
	}
}
