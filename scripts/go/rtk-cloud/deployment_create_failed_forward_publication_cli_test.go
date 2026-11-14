package main

import "testing"

func TestParseFailedForwardPublicationArgsRequiresExactEnvironmentAndPR(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"--environment", "dev", "--pull-request", "776"}, true},
		{[]string{"--environment", "staging", "--pull-request", "1"}, true},
		{[]string{"--environment", "prod", "--pull-request", "42"}, true},
		{[]string{"--environment", "dev", "--pull-request", "778", "--purpose", "openbao-public-crl"}, true},
		{[]string{"--environment", "dev", "--pull-request", "778", "--purpose", "unknown"}, false},
		{[]string{"--environment", "dev"}, false},
		{[]string{"--environment", "dev", "--pull-request", "0"}, false},
		{[]string{"--environment", "../dev", "--pull-request", "1"}, false},
		{[]string{"--environment", "dev", "--pull-request", "1", "--image", "fake"}, false},
		{[]string{"--environment", "dev", "--pull-request", "1", "extra"}, false},
	} {
		_, _, err := parseFailedForwardPublicationArgs(tc.args)
		if (err == nil) != tc.ok {
			t.Fatalf("args %v: %v", tc.args, err)
		}
	}
}

func TestOpenCreateExecutionForPublicationPreservesOwnerAndExcludesConcurrentProcess(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	checkpoint := createRecoveryCheckpoint{Owner: e.owner, Journal: e.Journal}
	if got, err := openCreateExecutionForPublication(t.Context(), store, checkpoint); err == nil {
		if got != nil {
			_ = got.Close()
		}
		t.Fatal("concurrent owner accepted")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := openCreateExecutionForPublication(t.Context(), store, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = got.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := openCreateExecutionForPublication(t.Context(), store, checkpoint)
	if err != nil {
		t.Fatal("original fence was removed", err)
	}
	if err = again.Close(); err != nil {
		t.Fatal(err)
	}
}
