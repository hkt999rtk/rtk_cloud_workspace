package recovery

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs only inside the disposable network-isolated test container. This fixture
// exercises immutable S3 PUT/GET transport, not provider authentication or bucket policy.
func TestPITRObjectServerHelper(t *testing.T) {
	root := os.Getenv("RTK_PITR_OBJECT_HELPER")
	if root == "" {
		t.Skip("container-only TLS object fixture")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/private/")
		if !strings.HasPrefix(r.URL.Path, "/private/") || !SafeRelative(key) {
			http.Error(w, "missing", 404)
			return
		}
		if r.Method == "PUT" {
			if r.Header.Get("If-None-Match") != "*" {
				http.Error(w, "conditional create required", 400)
				return
			}
			path := filepath.Join(root, "objects", filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				http.Error(w, "storage failure", 500)
				return
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				w.WriteHeader(412)
				io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			n, err := io.Copy(f, io.LimitReader(r.Body, (256<<20)+1))
			if n > 256<<20 {
				err = errors.New("fixture object too large")
			}
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				os.Remove(path)
				http.Error(w, "storage failure", 500)
				return
			}
			w.WriteHeader(200)
			return
		}
		if r.Method != "GET" {
			http.Error(w, "unsupported", 405)
			return
		}
		data, err := os.Open(filepath.Join(root, "objects", filepath.FromSlash(key)))
		if err != nil {
			w.WriteHeader(404)
			io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		defer data.Close()
		log, err := os.OpenFile(filepath.Join(root, "downloads.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			log.WriteString(key + "\n")
			log.Close()
		}
		io.Copy(w, data)
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(root, "object-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "object-url"), []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Second)
}

func startPITRFixture(t *testing.T, ctx context.Context, docker, container, directory string) string {
	t.Helper()
	binary := os.Getenv("RTK_PITR_HELPER_BINARY")
	if binary == "" {
		t.Fatal("RTK_PITR_HELPER_BINARY must name the Linux/arm64 recovery test binary")
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(directory, "object-helper")
	if err = os.WriteFile(helper, raw, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, docker, "exec", "--env", "RTK_PITR_OBJECT_HELPER="+directory, container, helper, "-test.run=^TestPITRObjectServerHelper$")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for {
		raw, err := os.ReadFile(filepath.Join(directory, "object-url"))
		if err == nil {
			return string(raw)
		}
		if ctx.Err() != nil {
			t.Fatal("TLS fixture unavailable", ctx.Err())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func exercisePITR(t *testing.T, ctx context.Context, docker, container, directory, encrypted, key, target string, e BaseBackupEngine, objects *memoryObjects, walID string) {
	t.Helper()
	binary := os.Getenv("RTK_PITR_CLI_BINARY")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("Linux recovery CLI required", err)
	}
	tool := filepath.Join(directory, "rtk-cloud")
	if err = os.WriteFile(tool, raw, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "wal.json")
	if err = WriteJSON(config, e.Config.WAL); err != nil {
		t.Fatal(err)
	}
	for key, raw := range objects.objects {
		path := filepath.Join(directory, "objects", filepath.FromSlash(key))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(directory, "pitr")
	if err = e.restoreFile(ctx, "fixture", encrypted, destination, key); err != nil {
		t.Fatal(err)
	}
	plan := PITRPlan{Version: 1, TargetLSN: target, TargetTimeline: 1, Executable: tool, WALConfigFile: config}
	if err = e.preparePITR(ctx, "fixture", destination, key, plan); err != nil {
		t.Fatal("prepare PITR", err)
	}
	if err = exec.CommandContext(ctx, docker, "exec", container, "chown", "-R", "postgres:postgres", directory).Run(); err != nil {
		t.Fatal(err)
	}
	env := []string{docker, "exec", "--user", "postgres", "--env", "RTK_BACKUP_ACCESS_KEY_ID=fixture", "--env", "RTK_BACKUP_SECRET_ACCESS_KEY=fixture", "--env", "SSL_CERT_FILE=" + filepath.Join(directory, "object-ca.pem"), container}
	// Docker Desktop bind mounts cannot host PostgreSQL Unix sockets. Use a
	// private container-local socket directory; the generated TCP disable remains.
	socket := "/tmp/rtk-pitr-socket"
	setup := append(append([]string{}, env...), "mkdir", "-m", "700", socket)
	if err = exec.CommandContext(ctx, setup[0], setup[1:]...).Run(); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{}, env...), "pg_ctl", "-D", filepath.Join(destination, "pgdata"), "-l", filepath.Join(destination, "server.log"), "-o", "-c unix_socket_directories="+socket, "-w", "start")
	if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
		log, _ := os.ReadFile(filepath.Join(destination, "server.log"))
		t.Fatalf("PITR start: %s %v\n%s", out, err, log)
	}
	query := func(sql string) (string, error) {
		args := append(append([]string{}, env...), "psql", "-X", "-h", socket, "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		return strings.TrimSpace(string(out)), err
	}
	for {
		state, err := query("SELECT pg_get_wal_replay_pause_state()")
		if err == nil && state == "paused" {
			break
		}
		if ctx.Err() != nil {
			log, _ := os.ReadFile(filepath.Join(destination, "server.log"))
			t.Fatalf("target not reached: %v\n%s", ctx.Err(), log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got, err := query("SELECT string_agg(id::text, ',' ORDER BY id) FROM physical_fixture"); err != nil || got != "1,2" {
		t.Fatal("target data boundary failed", got, err)
	}
	if got, err := query("SELECT pg_is_in_recovery()"); err != nil || got != "t" {
		t.Fatal("recovery promoted automatically", got, err)
	}
	if got, err := query("SHOW listen_addresses"); err != nil || got != "" {
		t.Fatal("recovery has TCP listener", got, err)
	}
	if _, err := query("INSERT INTO physical_fixture VALUES(99,'must fail')"); err == nil {
		t.Fatal("paused recovery accepted writes")
	}
	log, err := os.ReadFile(filepath.Join(directory, "downloads.log"))
	if err != nil || !strings.Contains(string(log), walID+".age") {
		t.Fatal("native recovery did not fetch encrypted remote WAL", err)
	}
	exercisePITRTimeline(t, ctx, docker, container, directory, encrypted, key, destination, e, plan, env, query)
	// A fresh recovery with the required ciphertext removed must fail before
	// reaching the target, rather than silently starting at an earlier point.
	for objectKey := range objects.objects {
		if strings.HasSuffix(objectKey, walID+".age") {
			if err = os.Remove(filepath.Join(directory, "objects", filepath.FromSlash(objectKey))); err != nil {
				t.Fatal(err)
			}
		}
	}
	missing := filepath.Join(directory, "pitr-missing")
	if err = e.restoreFile(ctx, "fixture", encrypted, missing, key); err != nil {
		t.Fatal(err)
	}
	if err = e.preparePITR(ctx, "fixture", missing, key, plan); err != nil {
		t.Fatal(err)
	}
	if err = exec.CommandContext(ctx, docker, "exec", container, "chown", "-R", "postgres:postgres", missing).Run(); err != nil {
		t.Fatal(err)
	}
	missingSocket := "/tmp/rtk-pitr-missing"
	setup = append(append([]string{}, env...), "mkdir", "-m", "700", missingSocket)
	if err = exec.CommandContext(ctx, setup[0], setup[1:]...).Run(); err != nil {
		t.Fatal(err)
	}
	args = append(append([]string{}, env...), "pg_ctl", "-D", filepath.Join(missing, "pgdata"), "-l", filepath.Join(missing, "server.log"), "-o", "-c unix_socket_directories="+missingSocket, "-w", "start")
	// It may briefly reach read-only consistency before discovering missing WAL.
	exec.CommandContext(ctx, args[0], args[1:]...).Run()
	for {
		log, _ := os.ReadFile(filepath.Join(missing, "server.log"))
		if strings.Contains(string(log), "recovery ended before configured recovery target was reached") && strings.Contains(string(log), "database system is shut down") {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("missing WAL did not fail closed: %v\n%s", ctx.Err(), log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("actual wal-restore CLI fetched TLS ciphertext; PostgreSQL paused at target with later write excluded")
}
