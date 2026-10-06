package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Governed PR profiles must execute their PostgreSQL cases instead of accepting
// a unit-profile pass with integration tests skipped. Never reuse an ambient DB.
func startPostgresPRFixture(module string) (func(), error) {
	if module != "account-manager" && module != "billing-service" {
		return nil, errors.New("unsupported PostgreSQL PR fixture")
	}
	if _, err := prePRRunFixtureCommand("", nil, "docker", "info"); err != nil {
		return nil, errors.New("Docker is required for selected PostgreSQL PR coverage")
	}
	name := "rtk-pre-pr-pg-" + module + "-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + strconv.Itoa(os.Getpid())
	cleanup := func() { _, _ = prePRRunFixtureCommand("", nil, "docker", "rm", "--force", name) }
	fail := func(err error) (func(), error) { cleanup(); return nil, err }
	if _, err := prePRRunFixtureCommand("", nil, "docker", "run", "--detach", "--name", name, "--label", "rtk.local-ci=postgres-pre-pr", "--tmpfs", "/var/lib/postgresql/data:rw,size=2g", "--env", "POSTGRES_DB=pr_test", "--env", "POSTGRES_USER=pr_test", "--env", "POSTGRES_PASSWORD=local_integration_only", "--publish", "127.0.0.1::5432", "postgres:16"); err != nil {
		return fail(errors.New("start isolated PostgreSQL PR fixture failed"))
	}
	ready := false
	for attempt := 0; attempt < 30; attempt++ {
		if _, err := prePRRunFixtureCommand("", nil, "docker", "exec", name, "pg_isready", "-U", "pr_test", "-d", "pr_test"); err == nil {
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		return fail(errors.New("isolated PostgreSQL PR fixture is not ready"))
	}
	raw, err := prePRRunFixtureCommand("", nil, "docker", "port", name, "5432/tcp")
	if err != nil {
		return fail(errors.New("isolated PostgreSQL port unavailable"))
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(string(raw)))
	if err != nil || host != "127.0.0.1" {
		return fail(errors.New("PostgreSQL PR fixture must bind only loopback"))
	}
	previous, existed := os.LookupEnv("TEST_DATABASE_URL")
	os.Setenv("TEST_DATABASE_URL", fmt.Sprintf("postgres://pr_test:local_integration_only@127.0.0.1:%s/pr_test?sslmode=disable", port))
	return func() {
		if existed {
			os.Setenv("TEST_DATABASE_URL", previous)
		} else {
			os.Unsetenv("TEST_DATABASE_URL")
		}
		cleanup()
	}, nil
}
