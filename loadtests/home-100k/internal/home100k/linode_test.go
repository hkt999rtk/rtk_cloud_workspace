package home100k

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinodeClientProvisionVMUsesLifecycleActionPayload(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/linode/instances" {
			t.Fatalf("request = %s %s, want POST /v4/linode/instances", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Fatalf("authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":123,"label":"lg01","ipv4":["203.0.113.10"],"tags":["home-100k","run-1","load-generator","device-mqtt"]}`))
	}))
	defer server.Close()

	client := NewLinodeClient(server.URL+"/v4", "test-token")
	vm, err := client.ProvisionVM(context.Background(), LifecycleAction{
		Action:     "provision-vm",
		RunID:      "run-1",
		Role:       "device-mqtt",
		ShardIndex: 0,
		Region:     "us-sea",
		Label:      "lg01",
		Tags:       []string{"home-100k", "run-1", "load-generator", "device-mqtt"},
	}, LinodeVMConfig{
		Type:           "g6-standard-2",
		Image:          "linode/ubuntu24.04",
		RootPass:       "not-for-real-runs",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
	})
	if err != nil {
		t.Fatalf("ProvisionVM() error = %v", err)
	}
	if vm.ID != 123 || vm.PublicIPv4 != "203.0.113.10" {
		t.Fatalf("vm = %#v", vm)
	}
	if got["region"] != "us-sea" || got["type"] != "g6-standard-2" || got["image"] != "linode/ubuntu24.04" {
		t.Fatalf("unexpected payload: %#v", got)
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 4 || tags[0] != "home-100k" || tags[1] != "run-1" || tags[2] != "load-generator" || tags[3] != "device-mqtt" {
		t.Fatalf("tags = %#v", got["tags"])
	}
}

func TestLinodeClientProvisionVMIncludesFailureBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"field":"authorized_keys","reason":"must provide root_pass or authorized_keys"}]}`))
	}))
	defer server.Close()

	client := NewLinodeClient(server.URL+"/v4", "test-token")
	_, err := client.ProvisionVM(context.Background(), LifecycleAction{
		Region: "us-sea",
		Label:  "lg01",
	}, LinodeVMConfig{})
	if err == nil {
		t.Fatal("ProvisionVM() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "authorized_keys") || !strings.Contains(err.Error(), "must provide root_pass or authorized_keys") {
		t.Fatalf("error missing Linode failure body: %v", err)
	}
}

func TestLinodeClientDestroyVMDeletesInstance(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/linode/instances/123") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewLinodeClient(server.URL+"/v4", "test-token")
	if err := client.DestroyVM(context.Background(), 123); err != nil {
		t.Fatalf("DestroyVM() error = %v", err)
	}
	if gotPath == "" {
		t.Fatal("server did not receive request")
	}
}

func TestLinodeClientDestroyVMIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewLinodeClient(server.URL+"/v4", "test-token")
	if err := client.DestroyVM(context.Background(), 123); err != nil {
		t.Fatalf("DestroyVM() after prior cleanup error = %v", err)
	}
}

func TestLinodeClientListVMsUsesRunIDTagFilter(t *testing.T) {
	var gotFilter string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v4/linode/instances" {
			t.Fatalf("request = %s %s, want GET /v4/linode/instances", r.Method, r.URL.Path)
		}
		gotFilter = r.Header.Get("X-Filter")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":123,"label":"lg01","ipv4":["203.0.113.10"],"tags":["home-100k","run-1","load-generator"]}],"page":1,"pages":1,"results":1}`))
	}))
	defer server.Close()

	client := NewLinodeClient(server.URL+"/v4", "test-token")
	vms, err := client.ListVMs(context.Background(), []string{"home-100k", "run-1"})
	if err != nil {
		t.Fatalf("ListVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].ID != 123 || vms[0].PublicIPv4 != "203.0.113.10" {
		t.Fatalf("vms = %#v", vms)
	}
	if !strings.Contains(gotFilter, "home-100k") || !strings.Contains(gotFilter, "run-1") {
		t.Fatalf("X-Filter = %q", gotFilter)
	}
}
