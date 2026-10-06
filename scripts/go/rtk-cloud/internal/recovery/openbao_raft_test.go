package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func raftSourceFixture(t *testing.T, handler http.Handler) (OpenBaoRaftSource, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	directory := t.TempDir()
	ca := filepath.Join(directory, "ca.crt")
	token := filepath.Join(directory, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	fp := sha256.Sum256(server.Certificate().Raw)
	return OpenBaoRaftSource{server.URL, ca, hex.EncodeToString(fp[:]), token, "selected-cluster"}, server
}

func TestOpenBaoRaftSnapshotUsesOriginalSealAndExactTLSIdentity(t *testing.T) {
	snapshot := []byte("native Raft snapshot")
	get, post := 0, 0
	source, _ := raftSourceFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "fixture-token" {
			t.Error("credential reference not installed")
		}
		switch r.URL.Path {
		case "/v1/sys/seal-status":
			json.NewEncoder(w).Encode(map[string]any{"initialized": true, "sealed": false, "type": "shamir", "n": 5, "t": 3, "storage_type": "raft", "cluster_id": "selected-cluster"})
		case "/v1/sys/storage/raft/snapshot":
			if r.Method == "GET" {
				get++
				w.Write(snapshot)
			} else if r.Method == "POST" {
				post++
				raw, _ := io.ReadAll(r.Body)
				if !bytes.Equal(raw, snapshot) {
					t.Error("restore snapshot differs")
				}
				w.WriteHeader(http.StatusNoContent)
			} else {
				t.Error("unexpected operation")
			}
		default:
			t.Error("unapproved OpenBao operation", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	component := Component{ID: "openbao", Kind: "openbao-raft", OpenBao: &source}
	engine := Engine{Config: Config{MaxArchiveBytes: 1 << 20}}
	var out bytes.Buffer
	if err := engine.captureOpenBaoRaft(context.Background(), component, &out); err != nil {
		t.Fatal(err)
	}
	if get != 1 || !bytes.Equal(out.Bytes(), snapshot) {
		t.Fatal("snapshot capture failed")
	}
	file := filepath.Join(t.TempDir(), "snapshot")
	os.WriteFile(file, snapshot, 0600)
	if err := engine.restoreOpenBaoRaft(context.Background(), component, file); err != nil {
		t.Fatal(err)
	}
	if post != 1 {
		t.Fatal("snapshot not restored")
	}
	component.OpenBao.ClusterID = "foreign"
	if engine.restoreOpenBaoRaft(context.Background(), component, file) == nil || post != 1 {
		t.Fatal("foreign cluster accepted")
	}
}

func TestOpenBaoRaftSnapshotRejectsCustodySealAndStorageGaps(t *testing.T) {
	for _, test := range []struct {
		name              string
		sealed            bool
		shares, threshold int
		storage           string
	}{{"sealed", true, 5, 3, "raft"}, {"single-custodian", false, 1, 1, "raft"}, {"wrong-quorum", false, 5, 2, "raft"}, {"file-storage", false, 5, 3, "file"}} {
		t.Run(test.name, func(t *testing.T) {
			snapshotCalls := 0
			source, _ := raftSourceFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/sys/seal-status" {
					snapshotCalls++
				}
				json.NewEncoder(w).Encode(map[string]any{"initialized": true, "sealed": test.sealed, "type": "shamir", "n": test.shares, "t": test.threshold, "storage_type": test.storage, "cluster_id": "selected-cluster"})
			}))
			engine := Engine{Config: Config{MaxArchiveBytes: 1 << 20}}
			if engine.captureOpenBaoRaft(context.Background(), Component{OpenBao: &source}, io.Discard) == nil || snapshotCalls != 0 {
				t.Fatal("unsafe snapshot source accepted")
			}
		})
	}
}

func TestOpenBaoRaftErrorsDoNotExposeAuthorizationOrResponse(t *testing.T) {
	source, _ := raftSourceFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("secret response fixture-token"))
	}))
	engine := Engine{Config: Config{MaxArchiveBytes: 1 << 20}}
	err := engine.captureOpenBaoRaft(context.Background(), Component{OpenBao: &source}, io.Discard)
	if err == nil || strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "secret response") {
		t.Fatal("unsafe diagnostic", err)
	}
	source.CAFingerprint = strings.Repeat("a", 64)
	if _, err := newRaftBackupClient(source); err == nil {
		t.Fatal("wrong trust anchor accepted")
	}
	if err := os.Chmod(source.TokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newRaftBackupClient(source); err == nil {
		t.Fatal("public token reference accepted")
	}
}
