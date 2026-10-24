package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestBootstrapCapabilityExecutionContractChecksImmutableImagesBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"original", "wrong-environment", "mutable-image", "missing-operator", "changed-operator", "changed-template", "existing-authority", "missing-binary", "wrong-service", "missing-feature", "malformed", "oversized", "command-failed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, _, operator, plan := createDependencyContractFixture(t)
			cfg.Values["PKI_REQUIRE_USER_MFA"] = "false"
			for key, value := range map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@isolated.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-password", "PKI_OPERATOR_SIGNER_REF": "isolated-original-offline-signer"} {
				operator[key] = value
				if err := store.write(filepath.Join("operator/env", key), []byte(value), false); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			features := []string{"deployment-bootstrap-finalize-v1", "first-trust-v1", "create-service-enrollment-v1", "registry-only-issuance-v1", "fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1", "owned-server-export-v1", "managed-callback-export-v1", "managed-test-lab-callback-v1", "service-registration-bootstrap-ack-v1"}
			for _, service := range []string{"rtk-account-manager", "pkimanagement", "certissuer", "api", "logingester", "pkibroker", "serviceidentity-bootstrap", "emqxpkihost", "mqttfoundation", "shadowworker", "otaservice", "videostorage", "webrtcservice", "cloudlogger"} {
				name := service
				if service == "rtk-account-manager" {
					name = "account-manager"
				}
				body := mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": name, "features": features})
				if service == "rtk-account-manager" {
					switch mode {
					case "wrong-service":
						body = mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": "foreign-service", "features": features})
					case "missing-feature":
						body = mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": name, "features": []string{}})
					case "malformed":
						body = []byte("invalid capability metadata")
					case "oversized":
						body = []byte(strings.Repeat("x", 17<<10))
					}
				}
				if err := os.WriteFile(filepath.Join(bin, service+".json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$RTK_BOOTSTRAP_CAPABILITY_CALLS"
entrypoint=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --entrypoint ]; then shift; entrypoint="$1"; break; fi
  shift
done
if [ "$entrypoint" = /bin/sh ]; then
  test "$RTK_BOOTSTRAP_CAPABILITY_MODE" != missing-binary
  exit
fi
test "$RTK_BOOTSTRAP_CAPABILITY_MODE" != command-failed
cat "$RTK_BOOTSTRAP_CAPABILITY_DIRECTORY/${entrypoint##*/}.json"
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_BOOTSTRAP_CAPABILITY_DIRECTORY", bin)
			t.Setenv("RTK_BOOTSTRAP_CAPABILITY_CALLS", log)
			t.Setenv("RTK_BOOTSTRAP_CAPABILITY_MODE", mode)
			switch mode {
			case "wrong-environment":
				plan.Environment = "staging"
			case "mutable-image":
				plan.AccountManagerImage = "example.invalid/account-manager:latest"
			case "missing-operator":
				if err := os.Remove(filepath.Join(store.Root, "operator/env/PKI_OPERATOR_SIGNER_REF")); err != nil {
					t.Fatal(err)
				}
			case "changed-operator":
				operator["PKI_OPERATOR_SIGNER_REF"] = "foreign-signer"
			case "changed-template":
				for name := range plan.Templates {
					plan.Templates[name] = deploymentplan.Digest("unreviewed template")
					break
				}
			case "existing-authority":
				if err := store.write("pki/service/issuer.json", []byte(`{"original":"retained authority"}`), false); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := qualifyEnvironmentCreateBootstrapCapabilities(context.Background(), cfg, plan, store, operator)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected immutable bootstrap capability qualification", err)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("capability qualification generated identity, allocation or admission evidence")
			}
			calls, readErr := os.ReadFile(log)
			if mode == "original" {
				if readErr != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 14 {
					t.Fatal("capability qualification omitted an enabled service", readErr, string(calls))
				}
			}
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if call == "" {
					continue
				}
				for _, required := range []string{"run --rm", "--platform linux/amd64", "--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "@sha256:"} {
					if !strings.Contains(call, required) {
						t.Fatal("capability metadata did not use the isolated immutable image contract", required)
					}
				}
				for _, private := range []string{store.Root, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"], " -v ", " --env ", " -e "} {
					if strings.Contains(call, private) {
						t.Fatal("metadata qualification supplied private deployment material to a container")
					}
				}
			}
		})
	}
}
