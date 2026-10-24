package main

import (
	"errors"
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
	want := []string{"go", "test", "-json", "-failfast", "-timeout=180m", "-skip=^TestHistoricalLateBootstrapCompletesFromOriginalSourceAndPairedCRLs$", "./scripts/go/..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace baseline command = %q, want %q", got, want)
	}
}

func TestRunTestMatrixPolicyOnlyDoesNotRepeatGovernedGoSuite(t *testing.T) {
	called := false
	err := runTestMatrixBaseline("/workspace", true, func(string) error {
		called = true
		return errors.New("duplicate full Go suite")
	})
	if err != nil || called {
		t.Fatalf("policy-only baseline repeated governed Go suite: called=%t err=%v", called, err)
	}

	err = runTestMatrixBaseline("/workspace", false, func(workspace string) error {
		called = true
		if workspace != "/workspace" {
			t.Fatalf("baseline workspace = %q", workspace)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("ordinary matrix omitted workspace baseline: called=%t err=%v", called, err)
	}
}
