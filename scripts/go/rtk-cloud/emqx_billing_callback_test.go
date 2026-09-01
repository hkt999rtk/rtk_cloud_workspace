package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEMQXBillingRulesSelectStableCallbackIDsAndEncodeRows(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	var mu sync.Mutex
	var actions, rules []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/v5/actions" {
			actions = append(actions, body)
		}
		if r.URL.Path == "/api/v5/rules" {
			rules = append(rules, body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts/configure-emqx-billing.sh"))
	cmd.Env = append(os.Environ(), "EMQX_API_URL="+server.URL+"/api/v5", "EMQX_API_TOKEN=test-admin", "VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN=test-ingest")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configuration: %v %s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 2 || len(rules) != 2 {
		t.Fatal(len(actions), len(rules))
	}
	for _, a := range actions {
		params := a["parameters"].(map[string]any)
		if _, ok := params["body"]; ok {
			t.Fatal("unescaped body template restored")
		}
		if params["path"] != "/v1/internal/mqtt/usage" {
			t.Fatal(params)
		}
	}
	for _, r := range rules {
		sql := r["sql"].(string)
		for _, want := range []string{"uuid_v4() as callback_id", " id,", "base64_encode(payload) as payload_b64", "publish_received_at"} {
			if !strings.Contains(sql, want) {
				t.Fatal("missing callback provenance", want, sql)
			}
		}
		if strings.Contains(sql, "message.delivered") && !strings.Contains(sql, "from_clientid") {
			t.Fatal("subscriber context missing")
		}
	}
}
