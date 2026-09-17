package main

import (
	"reflect"
	"testing"
)

func TestRunTestMatrixUsesExtendedWorkspaceTestTimeout(t *testing.T) {
	original := runWorkspaceBaselineCmd
	t.Cleanup(func() { runWorkspaceBaselineCmd = original })

	var got []string
	runWorkspaceBaselineCmd = func(_ string, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}

	if err := runTestMatrix(nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "test", "-timeout=20m", "./scripts/go/..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace baseline command = %q, want %q", got, want)
	}
}
