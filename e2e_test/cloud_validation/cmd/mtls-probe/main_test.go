package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpectedStatus(t *testing.T) {
	if !expectedStatus("200, 204", 204) || expectedStatus("200", 403) {
		t.Fatal("unexpected accepted status result")
	}
}

func TestRun(t *testing.T) {
	original := probeBearerGET
	defer func() { probeBearerGET = original }()
	args := []string{"--url", "https://device.test/info", "--cert", "cert", "--key", "key", "--token-file", "token"}
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != 2 {
		t.Fatalf("missing arguments code=%d, want 2", code)
	}
	probeBearerGET = func(context.Context, string, string, string, string, string, time.Duration) (int, error) {
		return 200, nil
	}
	if code := run(args, &stderr); code != 0 {
		t.Fatalf("success code=%d, stderr=%s", code, stderr.String())
	}
	probeBearerGET = func(context.Context, string, string, string, string, string, time.Duration) (int, error) {
		return 403, nil
	}
	if code := run(args, &stderr); code != 1 {
		t.Fatalf("unexpected status code=%d, want 1", code)
	}
	probeBearerGET = func(context.Context, string, string, string, string, string, time.Duration) (int, error) {
		return 0, errors.New("probe failed")
	}
	if code := run(args, &stderr); code != 1 {
		t.Fatalf("probe error code=%d, want 1", code)
	}
}
