package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCurlJSONStatusUsesGoHTTPClient(t *testing.T) {
	curlPath := filepath.Join(t.TempDir(), "curl")
	if err := os.WriteFile(curlPath, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(curlPath))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("authorization"); got != "Bearer token-1" {
			t.Fatalf("authorization header = %q", got)
		}
		if got := r.Header.Get("content-type"); got != "application/json" {
			t.Fatalf("content-type header = %q", got)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["hello"] != "world" {
			t.Fatalf("payload = %#v", payload)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}))
	defer server.Close()

	body, status, err := curlJSONStatus(server.URL, "token-1", []byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	var parsed map[string]string
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["ok"] != "yes" {
		t.Fatalf("body = %s", body)
	}
}
