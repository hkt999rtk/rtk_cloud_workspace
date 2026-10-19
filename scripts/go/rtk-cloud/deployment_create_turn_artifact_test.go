package main

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateTURNArtifactUsesExactImagePathWithoutExecutingItsEntrypoint(t *testing.T) {
	for _, mode := range []string{"amd64", "missing", "arm64"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _ := createProviderTestFixture(t, "dev")
			store, err := newSecretStore("", cfg.Environment)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"GHCR_PULL_USERNAME": "isolated-ci-user", "GHCR_PULL_TOKEN": "isolated-ci-token"} {
				if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
					t.Fatal(err)
				}
			}
			image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud@sha256:" + strings.Repeat("d", 64)
			plan := environmentCreatePlan{Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: image}}}, Provider: environmentCreateProviderPlan{TurnRegistrarImage: image}}
			fixture := t.TempDir()
			artifact := filepath.Join(fixture, "native-fixture")
			// A bounded ELF fixture verifies the frozen VM architecture without
			// executing a Linux artifact on the operator's current architecture.
			header := make([]byte, 64)
			copy(header, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
			binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
			machine := elf.EM_X86_64
			if mode == "arm64" {
				machine = elf.EM_AARCH64
			}
			binary.LittleEndian.PutUint16(header[18:], uint16(machine))
			binary.LittleEndian.PutUint32(header[20:], 1)
			binary.LittleEndian.PutUint16(header[52:], 64)
			if err := os.WriteFile(artifact, header, 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(fixture, "docker-arguments")
			docker := filepath.Join(fixture, "docker")
			writeTestFile(t, docker, `#!/bin/sh
printf '%s\n' "$@" >>"$RTK_TURN_ARTIFACT_ARGUMENTS"
test "$1" = --config || exit 90
shift 2
case "$1" in
  create) test "$2" = --platform && test "$3" = linux/amd64 || exit 92; printf '%s\n' cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc ;;
  cp) test "$RTK_TURN_ARTIFACT_MODE" != missing || exit 1; cp "$RTK_TURN_ARTIFACT_FIXTURE" "$3" ;;
  rm) exit 0 ;;
  *) exit 91 ;;
esac
`)
			if err := os.Chmod(docker, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TURN_ARTIFACT_ARGUMENTS", log)
			t.Setenv("RTK_TURN_ARTIFACT_MODE", mode)
			t.Setenv("RTK_TURN_ARTIFACT_FIXTURE", artifact)
			path, err := extractEnvironmentCreateTURNRegistrar(context.Background(), cfg, &plan, store)
			switch mode {
			case "amd64":
				if err != nil {
					t.Fatal(err)
				}
				copied, readErr := os.ReadFile(path)
				info, statErr := os.Lstat(path)
				if readErr != nil || statErr != nil || string(copied) != string(header) || info.Mode().Perm() != 0600 {
					t.Fatal("formal artifact was replaced or lost private permissions", readErr, statErr)
				}
			case "missing":
				if err == nil || !strings.Contains(err.Error(), "does not contain the required TURN registrar binary") {
					t.Fatal("missing formal binary did not block qualification", err)
				}
			case "arm64":
				if err == nil || !strings.Contains(err.Error(), "frozen Linux amd64") {
					t.Fatal("wrong VM architecture did not block qualification", err)
				}
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(raw)
			if !strings.Contains(calls, "create\n--platform\nlinux/amd64\n--entrypoint\n/app/turnregistrar\n"+image+"\n") || !strings.Contains(calls, ":/app/turnregistrar\n") || !strings.Contains(calls, "rm\n") || strings.Contains(calls, "\nstart\n") || strings.Contains(calls, "\nrun\n") || strings.Contains(calls, "\nbuild\n") {
				t.Fatal("artifact inspection changed image role/path or executed the native registrar", calls)
			}
		})
	}
}
