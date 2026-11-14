package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenBaoStartupFailureRetainsSafeNativeBoundary(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	frozen, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newEnvironmentKubernetesBackend(string(frozen), "dev")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct{ message, code string }{
		{"OpenBao original registry admission failed: PRIVATE_DATABASE", "OPENBAO_STARTUP_REGISTRY"},
		{"original startup issuer unavailable PRIVATE_DATABASE", "OPENBAO_STARTUP_ISSUER"},
		{"original startup CRL unavailable PRIVATE_DATABASE", "OPENBAO_STARTUP_CRL_MISSING"},
		{"OpenBao startup CRL invalid: PRIVATE_DATABASE", "OPENBAO_STARTUP_CRL_INVALID"},
		{"open PRIVATE_DATABASE: permission denied", "OPENBAO_STARTUP_PERMISSION"},
		{"Service identity is unavailable PRIVATE_DATABASE", "OPENBAO_STARTUP_IDENTITY"},
		{"context deadline exceeded PRIVATE_DATABASE", "OPENBAO_STARTUP_TIMEOUT"},
		{"OCI runtime exec failed: no such file PRIVATE_DATABASE", "OPENBAO_STARTUP_TOOL_MISSING"},
		{"unexpected PRIVATE_DATABASE", "OPENBAO_STARTUP_NATIVE"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			body := "#!/bin/sh\nprintf '%s\\n' '" + tc.message + "' 'command terminated with exit code 1' >&2\nexit 1\n"
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			var classified interface{ CheckCode() string }
			for _, binary := range []string{"/app/openbaopkihost", "/usr/local/bin/openbaopkihost"} {
				_, err := b.command(context.Background(), nil, "--namespace", "video-cloud-dev-secrets", "exec", "pod/openbao-0", "--container", "openbao-pki", "--", binary, "prepare-startup")
				if !errors.As(err, &classified) || classified.CheckCode() != tc.code || strings.Contains(err.Error(), "PRIVATE_DATABASE") {
					t.Fatalf("native boundary was lost or leaked: %v", err)
				}
			}
			_, other := b.command(context.Background(), nil, "--namespace", "video-cloud-dev-secrets", "get", "pod", "openbao-0", "-o", "json")
			if errors.As(other, &classified) && strings.HasPrefix(classified.CheckCode(), "OPENBAO_STARTUP_") {
				t.Fatal("native classification leaked into an unrelated API command")
			}
		})
	}
}
