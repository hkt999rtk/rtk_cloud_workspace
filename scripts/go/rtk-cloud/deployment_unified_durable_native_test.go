package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Opt-in native drill: isolated local data and loopback only. The same renderer
// arguments must retain AOF data after an abrupt process loss and replacement.
// This is component evidence, not an environment backup/restore qualification.
func TestNativeCommonRedisTargetSurvivesProcessReplacement(t *testing.T) {
	server, cli := os.Getenv("RTK_REDIS_NATIVE_TEST_BINARY"), os.Getenv("RTK_REDIS_NATIVE_TEST_CLI")
	if server == "" || cli == "" {
		t.Skip("set explicit local RTK_REDIS_NATIVE_TEST_BINARY and RTK_REDIS_NATIVE_TEST_CLI for the isolated drill")
	}
	if !filepath.IsAbs(server) || !filepath.IsAbs(cli) {
		t.Fatal("explicit absolute local Redis executables are required")
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			definitions, env := durableEnvironmentFixture(environment)
			definitions, err := applyEnvironmentDurableStorage(definitions, env)
			if err != nil {
				t.Fatal(err)
			}
			container := deploymentplan.Map(certIssuerObjectList(durablePod(definitions[1].Object)["containers"])[0])
			data := t.TempDir()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal("isolated loopback listener unavailable")
			}
			port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
			listener.Close()
			var argv []string
			for _, value := range certIssuerObjectList(container["args"]) {
				v := deploymentplan.String(value)
				if v == "/data" {
					v = data
				}
				argv = append(argv, v)
			}
			// Disable RDB only for this drill: passing requires the renderer's AOF,
			// not an unrelated shutdown snapshot or clean process exit.
			argv = append(argv, "--bind", "127.0.0.1", "--port", port, "--protected-mode", "yes", "--save", "")
			var running *exec.Cmd
			stop := func() {
				if running != nil {
					running.Process.Kill()
					running.Wait()
					running = nil
				}
			}
			t.Cleanup(stop)
			command := func(args ...string) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, cli, append([]string{"--raw", "-h", "127.0.0.1", "-p", port}, args...)...).Output()
				return strings.TrimSpace(string(out)), err
			}
			start := func() {
				t.Helper()
				running = exec.Command(server, argv...)
				if running.Start() != nil {
					t.Fatal("isolated Redis process could not start")
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if value, err := command("PING"); err == nil && value == "PONG" {
						return
					}
					time.Sleep(25 * time.Millisecond)
				}
				t.Fatal("isolated Redis process never became ready")
			}
			start()
			for key, value := range map[string]string{"fixture:durable:record": "retained", "fixture:durable:tombstone": "revoked"} {
				if result, err := command("SET", key, value); err != nil || result != "OK" {
					t.Fatal("fixture data could not be written")
				}
			}
			time.Sleep(2 * time.Second) // two everysec fsync periods
			files, err := filepath.Glob(filepath.Join(data, "appendonlydir", "*.aof"))
			if err != nil || len(files) == 0 {
				t.Fatal("native renderer did not persist AOF data")
			}
			stop() // abrupt loss, no SHUTDOWN/SAVE
			start()
			for key, expected := range map[string]string{"fixture:durable:record": "retained", "fixture:durable:tombstone": "revoked"} {
				if value, err := command("GET", key); err != nil || value != expected {
					t.Fatal("durable data was lost after replacing the native process")
				}
			}
		})
	}
}
