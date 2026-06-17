package home100k

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShadowHTTPClientUpdatesDesiredAndReported(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		if r.URL.Path != "/api/devices/device-1/shadow" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		state, _ := body["state"].(map[string]any)
		switch r.Header.Get("Authorization") {
		case "Bearer app-token":
			if _, ok := state["desired"]; !ok {
				t.Fatalf("app update missing desired: %#v", body)
			}
		case "Bearer device-token":
			if _, ok := state["reported"]; !ok {
				t.Fatalf("device update missing reported: %#v", body)
			}
		default:
			t.Fatalf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"state": {
				"desired": {"power": true},
				"reported": {"power": true},
				"delta": {}
			},
			"version": 2,
			"clientToken": "token-1"
		}`))
	}))
	defer server.Close()

	client := NewShadowHTTPClient(server.URL, http.DefaultClient)
	if _, err := client.UpdateDesired(context.Background(), "device-1", "app-token", map[string]any{"power": true}, "token-1", 0); err != nil {
		t.Fatalf("UpdateDesired() error = %v", err)
	}
	if _, err := client.UpdateReported(context.Background(), "device-1", "device-token", map[string]any{"power": true}, "token-2", 1); err != nil {
		t.Fatalf("UpdateReported() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestShadowHTTPClientGetParsesDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		if !strings.Contains(r.URL.RawQuery, "clientToken=get-1") {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"state": {
				"desired": {"brightness": 70},
				"reported": {},
				"delta": {"brightness": 70}
			},
			"version": 5,
			"clientToken": "get-1"
		}`))
	}))
	defer server.Close()

	client := NewShadowHTTPClient(server.URL, http.DefaultClient)
	doc, err := client.Get(context.Background(), "device-1", "device-token", "get-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if doc.Version != 5 || doc.ClientToken != "get-1" || doc.Delta["brightness"] != float64(70) {
		t.Fatalf("doc = %#v", doc)
	}
}
