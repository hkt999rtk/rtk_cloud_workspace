package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type billingUnitWorkflowStep struct {
	Name  string            `yaml:"name"`
	If    string            `yaml:"if"`
	Shell string            `yaml:"shell"`
	Run   string            `yaml:"run"`
	Env   map[string]string `yaml:"env"`
}

func billingUnitFixtureSteps(t *testing.T) (billingUnitWorkflowStep, billingUnitWorkflowStep, billingUnitWorkflowStep) {
	t.Helper()
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "go-coverage-governance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string         `yaml:"env"`
			Steps []billingUnitWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	unit, ok := workflow.Jobs["unit"]
	if !ok || unit.Env["TEST_DATABASE_URL"] != "" {
		t.Fatal("Billing fixture must be scoped to its unit matrix step, not shared job environment")
	}
	var start, coverage, cleanup billingUnitWorkflowStep
	startIndex, coverageIndex, cleanupIndex := -1, -1, -1
	for i, step := range unit.Steps {
		switch step.Name {
		case "Start isolated Billing unit PostgreSQL fixture":
			start, startIndex = step, i
		case "Run governed unit coverage":
			coverage, coverageIndex = step, i
		case "Stop isolated Billing unit PostgreSQL fixture":
			cleanup, cleanupIndex = step, i
		}
		if step.Name != "Start isolated Billing unit PostgreSQL fixture" && strings.Contains(step.Run, "TEST_DATABASE_URL=") {
			t.Fatalf("unexpected TEST_DATABASE_URL export in %q", step.Name)
		}
	}
	if startIndex < 0 || coverageIndex <= startIndex || cleanupIndex <= coverageIndex {
		t.Fatal("Billing fixture must start before coverage and clean up afterwards")
	}
	if start.If != "matrix.module == 'billing-service'" || cleanup.If != "always() && matrix.module == 'billing-service'" {
		t.Fatal("Billing fixture must be conditional, and cleanup must execute after failures")
	}
	for _, step := range []billingUnitWorkflowStep{start, coverage, cleanup} {
		if step.Shell != "bash" {
			t.Fatalf("%q must explicitly use bash", step.Name)
		}
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(step.Run)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%q shell syntax: %v\n%s", step.Name, err, output)
		}
	}
	return start, coverage, cleanup
}

func TestBillingUnitCoverageWorkflowExecutesScopedPostgresFixture(t *testing.T) {
	start, coverage, cleanup := billingUnitFixtureSteps(t)
	const fixtureID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const fixtureDSN = "postgres://integration:integration_password@127.0.0.1:63422/integration?sslmode=disable"
	for _, test := range []struct {
		name       string
		startFails bool
		readyCalls int
	}{
		{name: "ready-after-reset", readyCalls: 6},
		{name: "never-ready", startFails: true, readyCalls: 60},
		{name: "run-failure", startFails: true},
		{name: "run-collision", startFails: true},
		{name: "already-removed", readyCalls: 6},
		{name: "missing-port", startFails: true},
		{name: "public-port", startFails: true},
		{name: "multiple-ports", startFails: true},
		{name: "zero-port", startFails: true},
		{name: "oversized-port", startFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			// Execute the workflow's actual shell against a strict Docker fake. It
			// rejects any different container target, durable mount or public port.
			docker := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_DOCKER_LOG"
case "$1" in
  run)
    expected='run -d --rm --name rtk-bill-unit-pg-424242-7 --tmpfs /var/lib/postgresql/data:rw,size=2g -e POSTGRES_USER=integration -e POSTGRES_PASSWORD=integration_password -e POSTGRES_DB=integration -p 127.0.0.1::5432 postgres:16'
    [ "$*" = "$expected" ] || exit 91
    [ "$MOCK_SCENARIO" != run-failure ] || exit 23
    [ "$MOCK_SCENARIO" != run-collision ] || exit 125
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    ;;
  port)
    [ "$*" = 'port rtk-bill-unit-pg-424242-7 5432/tcp' ] || exit 92
    case "$MOCK_SCENARIO" in
      missing-port) ;;
      public-port) printf '0.0.0.0:63422\n' ;;
      multiple-ports) printf '127.0.0.1:63422\n127.0.0.1:63423\n' ;;
      zero-port) printf '127.0.0.1:0\n' ;;
      oversized-port) printf '127.0.0.1:65536\n' ;;
      *) printf '127.0.0.1:63422\n' ;;
    esac
    ;;
  exec)
    [ "$*" = 'exec rtk-bill-unit-pg-424242-7 pg_isready -h 127.0.0.1 -U integration -d integration' ] || exit 93
    count=0
    if [ -f "$MOCK_READY_COUNT" ]; then read -r count < "$MOCK_READY_COUNT"; fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$MOCK_READY_COUNT"
    [ "$MOCK_SCENARIO" != never-ready ] || exit 1
    [ "$count" != 1 ] && [ "$count" != 3 ]
    ;;
  rm)
    [ "$*" = 'rm --force --volumes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ] || exit 94
    [ "$MOCK_SCENARIO" != already-removed ] || exit 1
    printf 'fixture-container\n'
    ;;
  *) exit 95 ;;
esac
`
			sleep := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$MOCK_SLEEP_LOG\"\n"
			goCommand := "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s\\n' \"${TEST_DATABASE_URL:-}\" > \"$MOCK_COVERAGE_ENV\"\nprintf '%s\\n' \"$*\" > \"$MOCK_COVERAGE_ARGS\"\n"
			for name, script := range map[string]string{"docker": docker, "sleep": sleep, "go": goCommand} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			githubEnv := filepath.Join(dir, "github-env")
			dockerLog := filepath.Join(dir, "docker.log")
			readyCount := filepath.Join(dir, "ready-count")
			sleepLog := filepath.Join(dir, "sleep.log")
			coverageEnv := filepath.Join(dir, "coverage-env")
			coverageArgs := filepath.Join(dir, "coverage-args")
			env := append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_RUN_ID=424242", "GITHUB_RUN_ATTEMPT=7", "GITHUB_ENV="+githubEnv,
				"MOCK_SCENARIO="+test.name, "MOCK_DOCKER_LOG="+dockerLog,
				"MOCK_READY_COUNT="+readyCount, "MOCK_SLEEP_LOG="+sleepLog,
				"MOCK_COVERAGE_ENV="+coverageEnv, "MOCK_COVERAGE_ARGS="+coverageArgs,
			)
			run := func(script string, extraEnv ...string) ([]byte, error) {
				command := exec.Command("bash", "-c", script)
				command.Env = append(append([]string(nil), env...), extraEnv...)
				return command.CombinedOutput()
			}
			output, err := run(start.Run)
			if (err != nil) != test.startFails {
				t.Fatalf("fixture startup: %v\n%s", err, output)
			}
			calls := 0
			if raw, err := os.ReadFile(readyCount); err == nil {
				if _, err := fmt.Sscanf(string(raw), "%d", &calls); err != nil {
					t.Fatal(err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if calls != test.readyCalls {
				t.Fatalf("readiness calls = %d, want %d (three consecutive pg_isready successes)", calls, test.readyCalls)
			}
			rawEnv, envErr := os.ReadFile(githubEnv)
			clearedEnv := "BILLING_UNIT_POSTGRES_CONTAINER_ID=\n"
			ownedEnv := "BILLING_UNIT_POSTGRES_CONTAINER_ID=" + fixtureID
			if test.name == "run-failure" || test.name == "run-collision" {
				ownedEnv = "BILLING_UNIT_POSTGRES_CONTAINER_ID="
				if envErr != nil || string(rawEnv) != clearedEnv {
					t.Fatalf("failed startup must clear inherited cleanup ownership: %q, %v", rawEnv, envErr)
				}
			} else if envErr != nil || !strings.HasPrefix(string(rawEnv), clearedEnv+ownedEnv+"\n") {
				t.Fatalf("owned container identity must be published before readiness: %q, %v", rawEnv, envErr)
			}
			if test.startFails {
				if envErr != nil && !os.IsNotExist(envErr) {
					t.Fatal(envErr)
				}
				if strings.Contains(string(rawEnv), "TEST_DATABASE_URL=") {
					t.Fatal("failed fixture must not publish a TEST_DATABASE_URL")
				}
			} else {
				if envErr != nil || string(rawEnv) != clearedEnv+ownedEnv+"\nTEST_DATABASE_URL="+fixtureDSN+"\n" {
					t.Fatalf("fixture environment = %q, error %v", rawEnv, envErr)
				}
				output, err = run(coverage.Run, "TEST_DATABASE_URL="+fixtureDSN, "MODULE=billing-service", "BASE_REF=fixture-base")
				if err != nil {
					t.Fatalf("governed unit shell: %v\n%s", err, output)
				}
				raw, err := os.ReadFile(coverageEnv)
				if err != nil || string(raw) != fixtureDSN+"\n" {
					t.Fatalf("unit runner did not inherit the fixture DSN: %q, %v", raw, err)
				}
				raw, err = os.ReadFile(coverageArgs)
				want := "run ./rtk-cloud -- test-coverage --profile unit --module billing-service --run-id gh-424242-7-billing-service --base-ref fixture-base --head-ref HEAD\n"
				if err != nil || string(raw) != want {
					t.Fatalf("unit invocation = %q, want %q, error %v", raw, want, err)
				}
			}
			// Model GitHub's always() cleanup after either failed or successful
			// startup; an already --rm-deleted container must also be harmless.
			if output, err := run(cleanup.Run, ownedEnv); err != nil {
				t.Fatalf("fixture cleanup: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(dockerLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if test.name == "run-failure" || test.name == "run-collision" {
				if len(lines) != 1 || !strings.HasPrefix(lines[0], "run -d --rm --name ") {
					t.Fatalf("failed docker run must not remove an unknown same-name container: %q", raw)
				}
			} else if got := lines[len(lines)-1]; got != "rm --force --volumes "+fixtureID {
				t.Fatalf("cleanup target = %q, want exactly the owned container ID", got)
			}
			if strings.Contains(string(raw), "prune") {
				t.Fatal("fixture cleanup must never prune shared Docker resources")
			}
		})
	}
}
