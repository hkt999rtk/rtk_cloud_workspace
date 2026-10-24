package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The real activation callback, original journal and SSH host-key validation run
// here. Provider/SSH/HTTPS boundaries are controlled fixtures; the native router
// and agent auth/lease contract have their own maintained integration tests.
func TestCreateNormalTailTURNActivationRecoversCurrentLeaseWithoutRestart(t *testing.T) {
	for _, state := range []string{"first-then-completed", "pending", "allocated"} {
		t.Run(state, func(t *testing.T) { testTURNActivationResume(t, state, "") })
	}
}

func TestCreateNormalTailTURNActivationRefusesChangedOrUncertainNativeProof(t *testing.T) {
	for _, mode := range []string{"absent", "foreign-host", "foreign-region", "foreign-version", "foreign-ports", "inactive", "lowercase-state", "mixed-case-state", "unknown-state", "stale-heartbeat", "heartbeat-before-registration", "reported-error", "duplicate", "invalid-json", "denied", "binary-drift", "unit-failed", "replaced-vm", "missing-configuration", "wrong-intent", "cancelled"} {
		t.Run(mode, func(t *testing.T) { testTURNActivationResume(t, "pending", mode) })
	}
}

func testTURNActivationResume(t *testing.T, state, mode string) {
	t.Helper()
	cfg, store, operatorSHA, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	cfg.Adapter = "lke"
	cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] = "example.test"
	cfg.Values["VIDEO_CLOUD_DOMAIN"] = "dev.example.test"
	cfg.Values["TURN_REPLICAS"] = "1"
	var plan environmentCreatePlan
	if decodeEnvironmentJSON(a.Plan, &plan) != nil {
		t.Fatal("original fixture plan missing")
	}
	resource := environmentCreateProviderResource{Key: "turn-01", Kind: "instance", Path: "/linode/instances", Name: e.Journal.Stack + "-turn01", Region: "us-sea"}
	receipt := environmentCreateProviderReceipt{Key: resource.Key, Kind: resource.Kind, NativeID: "123", Name: resource.Name, Region: resource.Region, PublicIP: "198.51.100.12", Status: "running"}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	private, _ := ssh.MarshalPrivateKey(key, "synthetic-selected-operator")
	public, _ := ssh.NewPublicKey(key.Public())
	publicBytes := ssh.MarshalAuthorizedKey(public)
	if store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(private), true) != nil || store.write("operator/ssh/id_ed25519.pub", publicBytes, true) != nil {
		t.Fatal("synthetic SSH fixture unavailable")
	}
	plan.Provider = environmentCreateProviderPlan{Environment: cfg.Environment, Stack: e.Journal.Stack, Resources: []environmentCreateProviderResource{resource}, SSHKeySHA256: environmentPreflightBytesSHA256(bytes.TrimSpace(publicBytes)), HostIdentityPolicy: environmentCreateHostIdentityPolicy, HostIdentityTemplateSHA256: environmentPreflightBytesSHA256([]byte(environmentCreateHostIdentityTemplate))}
	if _, err := prepareEnvironmentCreateProviderMaterial(t.Context(), cfg, e.Journal.PlanSHA256, resource, store); err != nil {
		t.Fatal(err)
	}
	if err := store.write("runtime/turn-registry-node-auth", []byte("synthetic-selected-turn-auth"), true); err != nil {
		t.Fatal(err)
	}
	providerMutations := createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/linode/instances/123" {
			return false
		}
		id := 123
		if mode == "replaced-vm" {
			id = 124
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "label": resource.Name, "region": resource.Region, "status": "running", "ipv4": []string{receipt.PublicIP}})
		return true
	})
	registryReads := 0
	registered := time.Now().UTC().Add(-10 * time.Minute) // The one startup success is already far older than two minutes.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryReads++
		if r.Method != "GET" || r.URL.Path != "/v1/turn/nodes/active" || r.URL.Query().Get("region") != resource.Region || r.URL.Query().Get("count") != "100" || r.Header.Get("X-Video-Cloud-Registry-Node-ID") != "api" {
			t.Error("current lease read changed native API scope")
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte("synthetic-selected-turn-auth"))
		fmt.Fprintf(mac, "GET\n%s\napi\n%s\n", r.URL.RequestURI(), r.Header.Get("X-Video-Cloud-Registry-Timestamp"))
		if !hmac.Equal([]byte(r.Header.Get("X-Video-Cloud-Registry-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("native registry signature differs")
			w.WriteHeader(403)
			return
		}
		if mode == "denied" {
			w.WriteHeader(403)
			fmt.Fprint(w, "never-public-auth-error")
			return
		}
		if mode == "invalid-json" {
			fmt.Fprint(w, "never-public-native-envelope")
			return
		}
		node := map[string]any{"node_id": "turn01", "region": "us-sea", "zone": "us-sea", "public_host": "turn.dev.example.test", "udp_port": 3478, "tcp_port": 3478, "tls_port": nil, "secret_version": 1, "state": "ACTIVE", "registered_at": registered, "last_seen_at": time.Now().UTC(), "updated_at": time.Now().UTC(), "current_sessions": 3}
		// Qualification can consume the real native router's authenticated
		// register/heartbeat/active response. Only timestamps are moved into this
		// independent synthetic journal; enum and wire nullability remain native.
		if path := os.Getenv("RTK_TURN_NATIVE_ACTIVE_RESPONSE"); path != "" {
			raw, err := os.ReadFile(path)
			var wire struct {
				Nodes []map[string]any `json:"nodes"`
			}
			if err != nil || json.Unmarshal(raw, &wire) != nil || len(wire.Nodes) != 1 {
				t.Error("real native active response unavailable", err)
				w.WriteHeader(500)
				return
			}
			node = wire.Nodes[0]
			node["registered_at"], node["last_seen_at"] = registered, time.Now().UTC()
		}
		switch mode {
		case "foreign-host":
			node["public_host"] = "foreign.example.test"
		case "foreign-region":
			node["region"] = "us-lax"
		case "foreign-version":
			node["secret_version"] = 2
		case "foreign-ports":
			node["tcp_port"] = 4444
		case "inactive":
			node["state"] = "DRAINING"
		case "lowercase-state":
			node["state"] = "active"
		case "mixed-case-state":
			node["state"] = "Active"
		case "unknown-state":
			node["state"] = "UNKNOWN"
		case "stale-heartbeat":
			node["last_seen_at"] = registered
		case "heartbeat-before-registration":
			node["last_seen_at"] = registered.Add(-time.Second)
		case "reported-error":
			node["last_error"] = "never-public-runtime-error"
		}
		nodes := []any{node}
		if mode == "absent" {
			nodes = []any{}
		}
		if mode == "duplicate" {
			nodes = append(nodes, node)
		}
		json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
	}))
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	cfg.Values["LKE_TURN_REGISTRY_PUBLIC_URL"] = server.URL
	t.Setenv("LKE_TURN_REGISTRY_PUBLIC_URL", "https://unselected-ambient.invalid")
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.log")
	hashes := filepath.Join(dir, "hashes")
	readback := "original immutable configuration and registrar binary hashes\n"
	writeTestFile(t, hashes, readback)
	script := "#!/bin/sh\nscript=$(cat)\nprintf '%s\\n' \"$script\" >>\"$RTK_TURN_SSH_LOG\"\ncase \"$script\" in\n *'enable --now'*) exit 0;;\n *'--since'*) exit 7;;\n *'sha256sum'*) [ \"$RTK_TURN_TEST_MODE\" != binary-drift ] || { printf changed; exit 0; }; cat \"$RTK_TURN_HASHES\";;\n *) [ \"$RTK_TURN_TEST_MODE\" != unit-failed ];;\nesac\n"
	writeTestFile(t, filepath.Join(dir, "ssh"), script)
	if os.Chmod(filepath.Join(dir, "ssh"), 0755) != nil {
		t.Fatal("synthetic SSH executable unavailable")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TURN_SSH_LOG", log)
	t.Setenv("RTK_TURN_HASHES", hashes)
	t.Setenv("RTK_TURN_TEST_MODE", mode)
	configuration, _ := json.Marshal(map[string]any{"resource": resource, "receipt": receipt, "upstreams": []lkeEdgeHAProxyUpstream{{Kind: "https", Name: "original-node", Address: "192.168.1.2", Port: 30443}}, "release_sha256": deploymentplan.Digest(plan.Release)})
	configurationProof, _ := json.Marshal(map[string]any{"native": receipt, "configuration_sha256": deploymentplan.Digest(configuration), "runtime_sha256": environmentPreflightBytesSHA256([]byte(readback))})
	if mode != "missing-configuration" {
		if e.BeginEntry("configure-turn-01", "routing", configuration, time.Now().UTC()) != nil || e.RecordReceipt("configure-turn-01", configurationProof) != nil || e.CompleteEntry("configure-turn-01", time.Now().UTC()) != nil {
			t.Fatal("original configure receipt unavailable")
		}
	}
	intent, _ := json.Marshal(map[string]any{"native": receipt, "registry": buildGenericDNSPlan(cfg)})
	proof, _ := json.Marshal(map[string]any{"native": receipt, "registration": "verified-runtime-success"})
	ctx := withCreateNormalTailHandoff(t.Context(), &createNormalTailHandoff{Execution: e})
	operator := map[string]string{"LINODE_TOKEN": "private-provider-token"}
	if state == "first-then-completed" {
		if err := activateEnvironmentCreateTURNRegistrars(ctx, cfg, &plan, map[string]environmentCreateProviderReceipt{resource.Key: receipt}, store, operator, e); err != nil {
			t.Fatal("original first activation failed", err)
		}
	} else {
		if mode == "wrong-intent" {
			intent = []byte(`{"native":"foreign"}`)
		}
		if e.BeginEntry("activate-turn-01", "routing", intent, time.Now().UTC()) != nil {
			t.Fatal("original pending activation unavailable")
		}
		if state == "allocated" && e.RecordReceipt("activate-turn-01", proof) != nil {
			t.Fatal("original allocated activation unavailable")
		}
	}
	before, _ := os.ReadFile(e.JournalPath)
	if mode == "cancelled" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	}
	err = activateEnvironmentCreateTURNRegistrars(ctx, cfg, &plan, map[string]environmentCreateProviderReceipt{resource.Key: receipt}, store, operator, e)
	after, _ := os.ReadFile(e.JournalPath)
	sshLog, _ := os.ReadFile(log)
	starts := strings.Count(string(sshLog), "systemctl enable --now")
	if *providerMutations != 0 || starts != map[bool]int{true: 1, false: 0}[state == "first-then-completed"] || strings.Contains(string(sshLog), "systemctl restart") {
		t.Fatal("recovery mutated provider state or repeated activation", *providerMutations, starts)
	}
	if mode != "" {
		if err == nil || strings.Contains(err.Error(), "never-public") || !bytes.Equal(before, after) {
			t.Fatal("changed/unknown native evidence completed or exposed a pending activation", err)
		}
		return
	}
	if err != nil || createOpenBaoACKCompletionEntry(e.Journal, "activate-turn-01").State != "completed" || registryReads != 1 {
		t.Fatal("healthy current registration could not recover original activation", err, registryReads)
	}
	if state == "first-then-completed" && !bytes.Equal(before, after) {
		t.Fatal("completed activation proof was rewritten")
	}
}
