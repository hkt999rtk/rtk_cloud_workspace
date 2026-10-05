package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Probe the published artifact against disposable PostgreSQL with no schema
// CREATE privilege. No Staging data, credentials or provider writes are used.
func verifyDeploymentSimulatorStartup(ctx context.Context, image string) (result error) {
	seed := make([]byte, 8)
	if _, err := rand.Read(seed); err != nil {
		return errors.New("cannot allocate isolated simulator startup fixture")
	}
	run := "rtk-readiness-" + hex.EncodeToString(seed)
	network, postgres, simulator := run+"-net", run+"-pg", run+"-sim"
	label := "rtk.readiness-run=" + run
	networkCreated := false
	docker := func(args ...string) ([]byte, error) { return deploymentUpgradeCommand(ctx, "docker", args...) }
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, err := deploymentUpgradeCommand(cleanup, "docker", "ps", "-aq", "--filter", "label="+label)
		if err == nil {
			for _, id := range strings.Fields(string(raw)) {
				if !regexp.MustCompile(`^[a-f0-9]{12,64}$`).MatchString(id) {
					err = errors.New("fixture cleanup inventory is invalid")
					break
				}
			}
			if err == nil && len(strings.Fields(string(raw))) > 0 {
				_, err = deploymentUpgradeCommand(cleanup, "docker", append([]string{"rm", "--force", "--volumes"}, strings.Fields(string(raw))...)...)
			}
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("simulator startup fixture cleanup failed; inspect task label %s", label))
		}
		// This name is random, task-owned and created below. Docker refuses removal
		// while any unrelated attached container remains.
		if networkCreated {
			if _, err := deploymentUpgradeCommand(cleanup, "docker", "network", "rm", network); err != nil {
				result = errors.Join(result, errors.New("simulator startup network cleanup failed"))
			}
		}
	}()
	if _, err := docker("network", "create", "--internal", "--label", label, network); err != nil {
		return err
	}
	networkCreated = true
	if _, err := docker("run", "--detach", "--name", postgres, "--label", label, "--network", network,
		"--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "--env", "POSTGRES_PASSWORD=fixture_only", "--env", "POSTGRES_DB=fixture", "postgres:16"); err != nil {
		return err
	}
	ready := false
	for i := 0; i < 40; i++ {
		// Initialization uses a temporary socket-only server. TCP readiness
		// waits for the final server and the requested database initialization.
		if _, err := docker("exec", postgres, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "fixture"); err == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("simulator startup fixture was cancelled")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !ready {
		return errors.New("isolated PostgreSQL startup fixture is not Ready")
	}
	raw, err := docker("exec", postgres, "psql", "-U", "postgres", "-d", "fixture", "-qAt", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE USER simulator_runtime PASSWORD 'fixture_only'; REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT USAGE ON SCHEMA public TO simulator_runtime; SELECT has_schema_privilege('simulator_runtime','public','CREATE');")
	if err != nil || strings.TrimSpace(string(raw)) != "f" {
		return errors.New("simulator startup fixture did not establish a restricted runtime identity")
	}
	if _, err := docker("run", "--detach", "--platform", "linux/amd64", "--name", simulator, "--label", label, "--network", network,
		"--entrypoint", "/rtk-billing-payment-simulator",
		"--env", "DATABASE_URL=postgres://simulator_runtime:fixture_only@"+postgres+":5432/fixture?sslmode=disable",
		"--env", "BILLING_DB_MIGRATE_ON_STARTUP=false", "--env", "ENVIRONMENT=test", "--env", "PORT=8081",
		"--env", "PAYMENT_SIMULATOR_SHARED_SECRET=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--env", "PAYMENT_SIMULATOR_CALLBACK_SECRET=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"--env", "PAYMENT_SIMULATOR_PUBLIC_BASE_URL=http://127.0.0.1:8081", "--env", "PAYMENT_SIMULATOR_CALLBACK_URL=http://127.0.0.1:1/callback", image); err != nil {
		return err
	}
	// Internal Docker networks do not publish host ports consistently across
	// engines. Probe HTTP from the PostgreSQL fixture without exposing a port.
	healthProbe := `exec 3<>"/dev/tcp/$1/8081"; printf 'GET /internal/v1/health HTTP/1.1\r\nHost: simulator\r\nConnection: close\r\n\r\n' >&3; IFS= read -r status <&3; printf '%s\n' "$status"`
	for i := 0; i < 40; i++ {
		raw, err := docker("inspect", "--format", "{{.State.Running}}", simulator)
		if err != nil || strings.TrimSpace(string(raw)) != "true" {
			return errors.New("candidate payment simulator cannot start with migration disabled and a restricted runtime identity")
		}
		raw, err = docker("exec", postgres, "timeout", "2", "bash", "-e", "-c", healthProbe, "rtk-health", simulator)
		if err == nil && regexp.MustCompile(`^HTTP/1\.[01] 200(?: |\r|\n|$)`).Match(raw) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("simulator startup qualification was cancelled")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("candidate payment simulator did not become healthy with restricted runtime credentials")
}
