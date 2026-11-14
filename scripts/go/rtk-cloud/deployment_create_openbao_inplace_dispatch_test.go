package main

import (
	"strings"
	"testing"
)

// The real dispatcher must reach the existing scoped CLI before any access to
// a local environment or cluster; malformed requests never start a handoff.
func TestOpenBaoInPlaceFormalDispatchRejectsIncompleteRequest(t *testing.T) {
	for _, args := range [][]string{
		{"recover-create-openbao-inplace"},
		{"recover-create-openbao-inplace", "--environment", "dev", "--execute"},
		{"recover-create-openbao-inplace", "--environment", "dev", "--plan-sha256", strings.Repeat("a", 64)},
		{"recover-create-openbao-inplace", "--environment", "dev", "--timeout", "0s"},
	} {
		err := runDeployment(args)
		if err == nil || !strings.Contains(err.Error(), "select environment and the qualified plan for execution") {
			t.Fatalf("dispatcher did not validate %v: %v", args, err)
		}
	}
}
