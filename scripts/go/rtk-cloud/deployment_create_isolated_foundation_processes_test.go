package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This proves actual isolated processes and their SQL/PING interfaces only.
// It does not claim Kubernetes controller, PVC, CSI, or deployment readiness.
func TestIsolatedFoundationProcessesRunSQLAndPing(t *testing.T) {
	if os.Getenv("RTK_TEST_REAL_FOUNDATION") != "1" {
		t.Skip("explicit isolated foundation process test required")
	}
	postgresImage, valkeyImage := os.Getenv("RTK_TEST_POSTGRES_IMAGE"), os.Getenv("RTK_TEST_VALKEY_IMAGE")
	if !immutableEnvironmentImage.MatchString(postgresImage) || !immutableEnvironmentImage.MatchString(valkeyImage) {
		t.Fatal("exact selected immutable foundation images required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	owner := "rtk-foundation-fixture-" + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, docker, "network", "create", "--internal", "--label", "rtk.test.owner="+owner, owner).Output()
	if err != nil {
		t.Fatal("isolated network unavailable")
	}
	network := strings.TrimSpace(string(out))
	if network == "" {
		t.Fatal("isolated network identity unavailable")
	}
	t.Cleanup(func() { _ = exec.Command(docker, "network", "rm", network).Run() })
	envPath := filepath.Join(root, "postgres.env")
	syntheticPassword := hex.EncodeToString(nonce[:])
	if err := os.WriteFile(envPath, []byte("POSTGRES_USER=postgres\nPOSTGRES_DB=video_cloud\nPOSTGRES_PASSWORD="+syntheticPassword+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.CommandContext(ctx, docker, "run", "--detach", "--rm", "--pull=never", "--label", "rtk.test.owner="+owner, "--network", network, "--network-alias", "postgresql", "--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "--env-file", envPath, postgresImage).Output()
	_ = os.Remove(envPath)
	if err != nil {
		t.Fatal("isolated PostgreSQL process unavailable")
	}
	postgres := strings.TrimSpace(string(out))
	if len(postgres) != 64 {
		t.Fatal("isolated PostgreSQL container identity unavailable")
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "--force", "--volumes", postgres).Run() })
	out, err = exec.CommandContext(ctx, docker, "run", "--detach", "--rm", "--pull=never", "--label", "rtk.test.owner="+owner, "--network", network, "--network-alias", "redis", "--tmpfs", "/data:rw,size=64m", valkeyImage, "redis-server", "--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec").Output()
	if err != nil {
		t.Fatal("isolated Valkey process unavailable")
	}
	valkey := strings.TrimSpace(string(out))
	if len(valkey) != 64 {
		t.Fatal("isolated Valkey container identity unavailable")
	}
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "--force", "--volumes", valkey).Run() })
	wait := func(label string, command []string, expected string) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			output, err := exec.CommandContext(ctx, docker, command...).Output()
			if err == nil && strings.TrimSpace(string(output)) == expected {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(label, "interrupted")
			case <-time.After(200 * time.Millisecond):
			}
		}
		t.Fatal(label, "did not return its real readiness response")
	}
	wait("PostgreSQL", []string{"exec", postgres, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "video_cloud", "-c", "SELECT 1"}, "1")
	wait("Valkey", []string{"exec", valkey, "redis-cli", "PING"}, "PONG")
	if err := exec.CommandContext(ctx, docker, "exec", postgres, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "video_cloud", "-c", "CREATE TABLE fixture_check (id integer PRIMARY KEY); INSERT INTO fixture_check VALUES (17)").Run(); err != nil {
		t.Fatal("isolated SQL write failed")
	}
	wait("PostgreSQL readback", []string{"exec", postgres, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "video_cloud", "-c", "SELECT id FROM fixture_check"}, "17")
	wait("Valkey write", []string{"exec", valkey, "redis-cli", "SET", "fixture-check", "17"}, "OK")
	wait("Valkey readback", []string{"exec", valkey, "redis-cli", "GET", "fixture-check"}, "17")
}
