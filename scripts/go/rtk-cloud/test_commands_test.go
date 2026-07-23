package main

import "testing"

func TestTestLayerCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{"test-catalog", "test-coverage", "test-matrix", "test-services", "test-e2e", "test-ui", "test-live"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("command %q is not registered", name)
		}
	}
}

func TestTestLiveDefaultsToPlan(t *testing.T) {
	args := []string{"--env-root", "/tmp/test-runtime"}
	planned := ensureTestLiveMode(args)
	if !hasFlag(planned, "--plan") {
		t.Fatal("test-live should add --plan when no execution mode is provided")
	}
	if hasFlag(args, "--plan") {
		t.Fatal("test-live mutated its input arguments")
	}
}
