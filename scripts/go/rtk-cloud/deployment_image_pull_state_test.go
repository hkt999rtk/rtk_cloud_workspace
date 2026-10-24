package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImagePullCredentialsUseSelectedPrivateEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "used-config")
			copy := filepath.Join(t.TempDir(), "copied-config.json")
			t.Setenv("PATH", bin)
			t.Setenv("RTK_PULL_TEST_RECORD", record)
			t.Setenv("RTK_PULL_TEST_COPY", copy)
			rolloutWrite(t, filepath.Join(bin, "docker"), `#!/bin/sh
[ "$1" = --config ] && [ "$3" = pull ] && [ "$4" = --platform ] && [ "$5" = linux/amd64 ] || exit 2
printf '%s' "$2" > "$RTK_PULL_TEST_RECORD"
/bin/cp -p "$2/config.json" "$RTK_PULL_TEST_COPY" || exit 3
printf 'private-pull-diagnostic' >&2
exit "$RTK_PULL_TEST_EXIT"
`, 0700)
			for _, exit := range []string{"0", "1"} {
				t.Setenv("RTK_PULL_TEST_EXIT", exit)
				err := pullRolloutImageContext(context.Background(), environment, "fixture-user", "fixture-token", "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64))
				if (err == nil) != (exit == "0") || err != nil && strings.Contains(err.Error(), "private-pull-diagnostic") {
					t.Fatal("pull outcome or error redaction changed", err)
				}
				path := readTestFile(t, record)
				if !strings.HasPrefix(path, filepath.Join(store.Root, "deployment", "tmp", "image-pull-")) {
					t.Fatal("credential temporary directory escaped selected environment", path)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("credentials remain after pull", err)
				}
				if info, err := os.Lstat(copy); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("Docker credential file was not private", err)
				}
				for dir := filepath.Dir(path); dir != filepath.Dir(store.ConfigRoot); dir = filepath.Dir(dir) {
					if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
						t.Fatal("private credential parent was unsafe", dir, err)
					}
				}
			}
		})
	}
}

func TestImagePullRejectsUnsafeEnvironmentStorageBeforeDocker(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-environment", "symlink-parent", "public-parent"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				bin := t.TempDir()
				ran := filepath.Join(t.TempDir(), "must-not-run")
				t.Setenv("PATH", bin)
				t.Setenv("RTK_PULL_TEST_RAN", ran)
				rolloutWrite(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf ran > \"$RTK_PULL_TEST_RAN\"\n", 0700)
				selected := environment
				switch mode {
				case "missing-environment":
					selected = ""
				case "symlink-parent":
					if err := os.Symlink(t.TempDir(), filepath.Join(store.Root, "deployment")); err != nil {
						t.Fatal(err)
					}
				case "public-parent":
					if err := os.Mkdir(filepath.Join(store.Root, "deployment"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				if err := pullRolloutImageContext(context.Background(), selected, "fixture-user", "fixture-token", "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64)); err == nil || strings.Contains(err.Error(), "fixture-token") {
					t.Fatal("unsafe image credential location accepted", err)
				}
				if _, err := os.Lstat(ran); !os.IsNotExist(err) {
					t.Fatal("Docker ran before private environment validation")
				}
			})
		}
	}
}
