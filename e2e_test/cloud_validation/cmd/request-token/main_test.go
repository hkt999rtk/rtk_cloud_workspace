package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/mtlsclient"
)

func TestExpectedStatus(t *testing.T) {
	if !expectedStatus("401, 403", 403) {
		t.Fatal("403 should be accepted")
	}
	if expectedStatus("401,invalid", 500) {
		t.Fatal("500 must not be accepted")
	}
}

func TestRun(t *testing.T) {
	original := requestToken
	defer func() { requestToken = original }()
	args := []string{"--url", "https://device.test/request_token", "--cert", "cert", "--key", "key", "--request", "request", "--output", "output"}
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != 2 {
		t.Fatalf("missing arguments code=%d, want 2", code)
	}
	requestToken = func(context.Context, string, string, string, string, string, string, time.Duration) error { return nil }
	if code := run(args, &stderr); code != 0 {
		t.Fatalf("success code=%d, stderr=%s", code, stderr.String())
	}
	requestToken = func(context.Context, string, string, string, string, string, string, time.Duration) error {
		return &mtlsclient.HTTPStatusError{StatusCode: 403}
	}
	if code := run(append(args, "--expect-http-status", "401,403"), &stderr); code != 0 {
		t.Fatalf("expected rejection code=%d", code)
	}
	requestToken = func(context.Context, string, string, string, string, string, string, time.Duration) error {
		return errors.New("request failed")
	}
	if code := run(args, &stderr); code != 1 {
		t.Fatalf("request failure code=%d, want 1", code)
	}
}
