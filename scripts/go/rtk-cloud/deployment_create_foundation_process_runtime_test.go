package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These local containers belong only to one synthetic test owner/network.
// Their SQL/PING responses are real; no Kubernetes or CSI equivalence is implied.
type isolatedFoundationProcess struct{ Container, ConfigDigest, StartedAt string }

// One private, owner-labelled internal network is shared with the existing
// isolated native migration Job runner. It starts no workload or identity.
func (c *isolatedOpenBaoFirstController) ensureIsolatedFoundationNetwork(ctx context.Context) (string, string, error) {
	bad := errors.New("isolated foundation network unavailable")
	if !validIsolatedOpenBaoVolumeParent(c.volumeParent) || c.cfg.Environment != "dev" || !isolatedScriptTempPath(c.store.Root) {
		return "", "", bad
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return "", "", bad
	}
	if c.foundationNetwork == "" {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", "", bad
		}
		owner := "rtk-foundation-test-" + hex.EncodeToString(nonce[:])
		raw, err := exec.CommandContext(ctx, docker, "network", "create", "--internal", "--label", "rtk.test.owner="+owner, owner).Output()
		network := strings.TrimSpace(string(raw))
		if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(network) {
			return "", "", bad
		}
		c.foundationNetwork, c.foundationOwner = network, owner
		c.t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = exec.CommandContext(cleanupCtx, docker, "network", "rm", network).Run()
		})
	}
	network, owner := c.foundationNetwork, c.foundationOwner
	raw, err := exec.CommandContext(ctx, docker, "network", "inspect", "--format", `{{index .Labels "rtk.test.owner"}}|{{.Internal}}`, network).Output()
	if err != nil || strings.TrimSpace(string(raw)) != owner+"|true" {
		return "", "", bad
	}
	return network, owner, nil
}

func (c *isolatedOpenBaoFirstController) startFoundationProcesses(ctx context.Context, objects map[string]deploymentplan.Object) error {
	bad := func(stage string) error { return errors.New("process-" + stage) }
	if !validIsolatedOpenBaoVolumeParent(c.volumeParent) {
		return bad("01")
	}
	// PVC binding is intentionally deferred by the native executor until a
	// consumer is scheduled. Check the original PVC owner before observing
	// the synthetic allocation, then bind that exact path into the container.
	if err := c.observeMockRedisClaim(false); err != nil {
		return bad("02")
	}
	if c.redisVolumeRoot == "" || !isolatedScriptTempPath(c.redisVolumeRoot) {
		return bad("volume-path")
	}
	a, err := isolatedFoundationProcessAuthority(c.cfg, c.store, objects)
	if err != nil {
		c.t.Log("isolated foundation source:", err.Error()) // fixed labels only
		return bad("03")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return bad("04")
	}
	network, owner, err := c.ensureIsolatedFoundationNetwork(ctx)
	if err != nil {
		return bad("05")
	}
	candidates := []struct{ role, configured, expected, child, config, target, alias string }{
		{"redis", a.RedisImage, "docker.io/valkey/valkey:8-alpine@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507", "docker.io/valkey/valkey@sha256:32627109abf6f741121096b45c732f758876803efd7b2e1018ebc0350d117119", "sha256:09f1cebdbddf5d5c6e7521bd66e488a55bc11c47736572c101d9d0cf628121d0", "/data", "redis." + c.cfg.Values["CLOUD_STACK_NAME"] + "-platform.svc"},
		{"postgres", a.PostgresImage, "docker.io/library/postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685", "docker.io/library/postgres@sha256:075f7ba66bc9b3ce7d6b8b635208ff61cd7cf1a67d71ec530eec5d7ae0cbe571", "sha256:75f5a96988cdf694a215073c3e9c001b706b371e2f94df3967f2efdec2787f6b", "/var/lib/postgresql/data", "postgresql." + c.cfg.Values["CLOUD_STACK_NAME"] + "-platform.svc"},
	}
	if c.foundationProcesses == nil {
		c.foundationProcesses = map[string]isolatedFoundationProcess{}
	}
	for _, item := range candidates {
		if item.configured != item.expected {
			return bad("06")
		}
		if _, exists := c.foundationProcesses[item.role]; exists {
			continue
		}
		imageInfo, err := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}", item.child).Output()
		if err != nil || strings.TrimSpace(string(imageInfo)) != item.config+"|linux/amd64" {
			return bad("07")
		}
		volume := c.redisVolumeRoot
		args := []string{"run", "--detach", "--rm", "--platform", "linux/amd64", "--pull=never", "--label", "rtk.test.owner=" + owner, "--network", network, "--network-alias", item.alias, "--network-alias", item.alias + ".cluster.local"}
		envPath := ""
		if item.role == "postgres" {
			if a.PostgresPassword == "" || a.PostgresInitSQL == "" || strings.ContainsAny(a.PostgresPassword, "\r\n") {
				return bad("08")
			}
			volume, err = os.MkdirTemp(c.volumeParent, "postgres-volume-")
			if err != nil {
				return bad("09")
			}
			if err := os.Chmod(volume, 0700); err != nil {
				return bad("10")
			}
			c.postgresVolumeRoot = volume
			initPath := filepath.Join(c.volumeParent, "postgresql-init.sql")
			if err := os.WriteFile(initPath, []byte(a.PostgresInitSQL), 0644); err != nil {
				return bad("11")
			}
			envPath = filepath.Join(c.volumeParent, "postgres.env")
			if err := os.WriteFile(envPath, []byte("POSTGRES_PASSWORD="+a.PostgresPassword+"\nPGDATA=/var/lib/postgresql/data/pgdata\n"), 0600); err != nil {
				return bad("12")
			}
			args = append(args, "--env-file", envPath, "--mount", "type=bind,src="+initPath+",dst=/docker-entrypoint-initdb.d/001-create-databases.sql,readonly")
		}
		args = append(args, "--mount", "type=bind,src="+volume+",dst="+item.target, item.child)
		if item.role == "redis" {
			args = append(args, a.RedisCommand...)
			args = append(args, a.RedisArgs...)
		} else {
			args = append(args, a.PostgresArgs...)
		}
		out, err := exec.CommandContext(ctx, docker, args...).Output()
		if envPath != "" {
			_ = os.Remove(envPath)
		}
		container := strings.TrimSpace(string(out))
		if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(container) {
			return bad("13")
		}
		c.t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = exec.CommandContext(cleanupCtx, docker, "rm", "--force", container).Run()
		})
		deadline := time.Now().Add(60 * time.Second)
		ready := false
		for time.Now().Before(deadline) {
			probe := []string{"exec", container, "redis-cli", "-h", item.alias + ".cluster.local", "PING"}
			expected := "PONG"
			if item.role == "postgres" {
				probe = []string{"exec", container, "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" exec psql -X -A -t -U postgres -h "$1" -d video_cloud -c 'SELECT 1'`, "isolated-pg-probe", item.alias + ".cluster.local"}
				expected = "1"
			}
			raw, err := exec.CommandContext(ctx, docker, probe...).Output()
			if err == nil && strings.TrimSpace(string(raw)) == expected {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				return bad("14")
			case <-time.After(250 * time.Millisecond):
			}
		}
		if !ready {
			return bad("15")
		}
		inspect, err := exec.CommandContext(ctx, docker, "inspect", "--format", `{{index .Config.Labels "rtk.test.owner"}}|{{.Id}}|{{.Image}}|{{.State.Running}}|{{.State.StartedAt}}`, container).Output()
		parts := strings.Split(strings.TrimSpace(string(inspect)), "|")
		if err != nil || len(parts) != 5 || parts[0] != owner || parts[1] != container || parts[2] != item.config || parts[3] != "true" {
			return bad("16")
		}
		if _, err := time.Parse(time.RFC3339Nano, parts[4]); err != nil {
			return bad("17")
		}
		c.foundationProcesses[item.role] = isolatedFoundationProcess{Container: container, ConfigDigest: item.config, StartedAt: parts[4]}
	}
	return nil
}

func TestIsolatedFoundationProcessesRejectMissingOriginalVolume(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	c := newIsolatedOpenBaoFirstController(t, cfg, store, t.TempDir())
	if err := c.startFoundationProcesses(t.Context(), nil); err == nil {
		t.Fatal("missing original Redis volume accepted")
	}
	if c.foundationNetwork != "" || len(c.foundationProcesses) != 0 {
		t.Fatal("rejected input started network or processes")
	}
}
