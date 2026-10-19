package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func releaseDockerAuthFixture(t *testing.T, environment string) (secretStore, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	for key, value := range map[string]string{"GHCR_PULL_USERNAME": "selected-" + environment, "GHCR_PULL_TOKEN": "private-release-token-canary"} {
		if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_RELEASE_AUTH_DIR", dir)
	t.Setenv("RTK_RELEASE_STORE_ROOT", store.Root)
	return store, dir
}

func TestEnvironmentReleasePullUsesOnlyCanonicalSelectedAuthentication(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store, dir := releaseDockerAuthFixture(t, environment)
			global := filepath.Join(dir, "ambient-docker")
			if err := os.Mkdir(global, 0700); err != nil {
				t.Fatal(err)
			}
			globalBytes := []byte(`{"credsStore":"desktop","currentContext":"desktop-linux","auths":{"ghcr.io":{"auth":"foreign-authentication"}}}`)
			if err := os.WriteFile(filepath.Join(global, "config.json"), globalBytes, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", global)
			// The maintained pull adapter preserves an explicitly selected engine.
			// This fake endpoint never contacts a real daemon or context.
			t.Setenv("DOCKER_HOST", "unix://isolated-release-engine.sock")
			t.Setenv("DOCKER_CONTEXT", "isolated-release-context")
			script := `#!/bin/sh
set -eu
test "$#" = 6 && test "$1" = --config && test "$3" = pull && test "$4" = --platform && test "$5" = linux/amd64
case "$2" in "$RTK_RELEASE_STORE_ROOT"/deployment/tmp/image-pull-*) ;; *) exit 71 ;; esac
test "${DOCKER_HOST-}" = unix://isolated-release-engine.sock
test "${DOCKER_CONTEXT-}" = isolated-release-context
printf '%s' "$2" > "$RTK_RELEASE_AUTH_DIR/config-path"
/bin/cp -p "$2/config.json" "$RTK_RELEASE_AUTH_DIR/copied-config.json"
printf '%s\n' "$6" > "$RTK_RELEASE_AUTH_DIR/image"
printf 'private-release-token-canary raw registry diagnostic' >&2
exit "${RTK_RELEASE_DOCKER_EXIT:-0}"
`
			rolloutWrite(t, filepath.Join(dir, "docker"), script, 0700)
			rolloutWrite(t, filepath.Join(dir, "docker-credential-desktop"), "#!/bin/sh\nprintf called > \"$RTK_RELEASE_AUTH_DIR/desktop-helper-called\"\nexit 72\n", 0700)
			run := environmentCIImageCommand(deploymentConfig{Environment: environment})
			for _, reference := range []string{"ghcr.io/hkt999rtk/account-manager@sha256:" + strings.Repeat("a", 64), "docker.io/library/postgres@sha256:" + strings.Repeat("b", 64), "docker.io/library/postgres:16-alpine"} {
				for _, exit := range []string{"0", "1"} {
					t.Setenv("RTK_RELEASE_DOCKER_EXIT", exit)
					raw, err := run(context.Background(), "docker", "pull", "--platform", "linux/amd64", reference)
					if (err == nil) != (exit == "0") || len(raw) != 0 || err != nil && strings.Contains(err.Error(), "private-release-token-canary") {
						t.Fatal("release pull changed its outcome or leaked raw private diagnostics", err)
					}
					configPath := readTestFile(t, filepath.Join(dir, "config-path"))
					if _, err := os.Lstat(configPath); !os.IsNotExist(err) {
						t.Fatal("release command retained temporary registry identity", err)
					}
					var config struct {
						Auths       map[string]map[string]string `json:"auths"`
						CredsStore  string                       `json:"credsStore"`
						CredHelpers map[string]string            `json:"credHelpers"`
					}
					copied := filepath.Join(dir, "copied-config.json")
					if json.Unmarshal([]byte(readTestFile(t, copied)), &config) != nil || len(config.Auths) != 1 || config.Auths["ghcr.io"]["auth"] != base64.StdEncoding.EncodeToString([]byte("selected-"+environment+":private-release-token-canary")) || config.CredsStore != "" || len(config.CredHelpers) != 0 {
						t.Fatal("release pull borrowed ambient authentication")
					}
					if info, err := os.Stat(copied); err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("selected Docker credentials were not private", err)
					}
					if got := readTestFile(t, filepath.Join(global, "config.json")); got != string(globalBytes) {
						t.Fatal("release pull changed global Docker config")
					}
					if _, err := os.Stat(filepath.Join(dir, "desktop-helper-called")); !os.IsNotExist(err) {
						t.Fatal("release pull invoked the desktop credential helper")
					}
					if got := readTestFile(t, filepath.Join(dir, "image")); strings.TrimSpace(got) != reference {
						t.Fatal("release pull changed selected image reference")
					}
					entries, err := os.ReadDir(filepath.Join(store.Root, "deployment/tmp"))
					if err != nil || len(entries) != 0 {
						t.Fatal("private release identity cleanup incomplete", err)
					}
				}
			}
		})
	}
}

func TestEnvironmentReleasePullRejectsMissingSelectedCredentialsAndWrongShape(t *testing.T) {
	for _, mode := range []string{"missing-token", "missing-user", "newline-token", "invalid-environment", "wrong-platform", "missing-platform"} {
		t.Run(mode, func(t *testing.T) {
			store, dir := releaseDockerAuthFixture(t, "dev")
			cfg := deploymentConfig{Environment: "dev"}
			args := []string{"pull", "--platform", "linux/amd64", "ghcr.io/hkt999rtk/account-manager@sha256:" + strings.Repeat("a", 64)}
			switch mode {
			case "missing-token":
				if err := os.Remove(filepath.Join(store.Root, "operator/env/GHCR_PULL_TOKEN")); err != nil {
					t.Fatal(err)
				}
			case "missing-user":
				if err := os.Remove(filepath.Join(store.Root, "operator/env/GHCR_PULL_USERNAME")); err != nil {
					t.Fatal(err)
				}
			case "newline-token":
				if err := store.write("operator/env/GHCR_PULL_TOKEN", []byte("private-release\ntoken-canary"), true); err != nil {
					t.Fatal(err)
				}
			case "invalid-environment":
				cfg.Environment = ""
			case "wrong-platform":
				args[2] = "linux/arm64"
			case "missing-platform":
				args = []string{"pull", args[3]}
			}
			rolloutWrite(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf called > \"$RTK_RELEASE_AUTH_DIR/docker-called\"\n", 0700)
			if _, err := environmentCIImageCommand(cfg)(context.Background(), "docker", args...); err == nil || strings.Contains(err.Error(), "token-canary") {
				t.Fatal("release pull accepted unsafe selected authentication or leaked it", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "docker-called")); !os.IsNotExist(err) {
				t.Fatal("invalid selected credentials fell back to ambient Docker")
			}
		})
	}
}

func TestEnvironmentReleasePullCancellationKillsHelperAndCleansCredentials(t *testing.T) {
	for _, mode := range []string{"cancellation", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			store, dir := releaseDockerAuthFixture(t, "dev")
			rolloutWrite(t, filepath.Join(dir, "docker"), `#!/bin/sh
set -eu
test "$1" = --config
printf '%s' "$2" > "$RTK_RELEASE_AUTH_DIR/config-path"
/bin/sh -c 'trap "" TERM; while :; do /bin/sleep 1; done' &
printf '%s\n%s\n' "$$" "$!" > "$RTK_RELEASE_AUTH_DIR/pids"
wait
`, 0700)
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := environmentCIImageCommand(deploymentConfig{Environment: "dev"})(ctx, "docker", "pull", "--platform", "linux/amd64", "ghcr.io/hkt999rtk/account-manager@sha256:"+strings.Repeat("a", 64))
				done <- err
			}()
			started := false
			// Allow credential setup and process scheduling under parallel test
			// load; the assertion below must exercise an actually started helper.
			for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
				if raw, err := os.ReadFile(filepath.Join(dir, "pids")); err == nil && len(strings.Fields(string(raw))) == 2 {
					started = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if mode == "cancellation" || !started {
				cancel()
			}
			<-ctx.Done()
			select {
			case err := <-done:
				if err == nil || ctx.Err() == nil || !started {
					t.Fatal("release pull failed to exercise bounded descendant cleanup", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("release cancellation left Docker/helper execution running")
			}
			for _, value := range strings.Fields(readTestFile(t, filepath.Join(dir, "pids"))) {
				pid, err := strconv.Atoi(value)
				if err != nil {
					t.Fatal(err)
				}
				if syscall.Kill(pid, 0) == nil {
					status, err := exec.Command("/bin/ps", "-p", value, "-o", "stat=").Output()
					if err == nil && !strings.HasPrefix(strings.TrimSpace(string(status)), "Z") {
						t.Fatal("release pull returned with a live descendant")
					}
				}
			}
			configPath := readTestFile(t, filepath.Join(dir, "config-path"))
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatal("cancelled release pull retained credentials", err)
			}
			entries, err := os.ReadDir(filepath.Join(store.Root, "deployment/tmp"))
			if err != nil || len(entries) != 0 {
				t.Fatal("cancelled release pull did not join private cleanup", err)
			}
		})
	}
}
