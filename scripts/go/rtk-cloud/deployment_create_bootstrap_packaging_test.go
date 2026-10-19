package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCreateBootstrapImageProbeRequiresNativeTransportAndIdentityInstallers(t *testing.T) {
	for _, missing := range []string{"", "openbaopkihost", "emqxpkihost", "serviceidentity-bootstrap"} {
		t.Run(firstNonEmpty(missing, "complete"), func(t *testing.T) {
			cfg, store, operator := createBootstrapLocalFixture(t, "dev")
			workspace, err := workspaceRoot()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Workspace, cfg.Values["PKI_REQUIRE_USER_MFA"] = workspace, "false"
			operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["PKI_OPERATOR_SIGNER_REF"] = "operator@example.test", "offline:dev/service-root"
			for key, value := range operator {
				if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
					t.Fatal(err)
				}
			}
			image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud@sha256:" + strings.Repeat("a", 64)
			p := environmentCreateBootstrapPlan{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Image: image, AccountManagerImage: "ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:" + strings.Repeat("b", 64), Claims: []string{"pki-service-bootstrap-state"}, Templates: map[string]string{}, Images: map[string]string{"LKE_MQTT_PKI_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/emqx-pki@sha256:" + strings.Repeat("c", 64)}}
			p.IngressAdmissionProfile = environmentCreateIngressAdmissionProfile
			p.AppRoutingMode = "internal-service"
			for _, domain := range managedPKIDomains {
				p.Domains = append(p.Domains, environmentCreateIssuerPolicy{Domain: domain})
			}
			for _, name := range []string{"deploy/pki/render-staging-controller.py", "deploy/pki/render-staging-consumers.py", "deploy/pki/account-manager-managed.env.example", "deploy/pki/controller-service-clients.env.example", "deploy/pki/controller-host-identity.env.example", "deploy/pki/domain-consumers.env.example", "deploy/pki/server-crl-worker.env.example", "deploy/pki/app-crl-worker.env.example"} {
				raw, err := os.ReadFile(filepath.Join(workspace, "repos", "rtk_video_cloud", name))
				if err != nil {
					t.Fatal(err)
				}
				p.Templates[name] = environmentPreflightBytesSHA256(raw)
			}
			fixture := t.TempDir()
			for _, name := range []string{"pkicontroller", "pkiceremony", "serviceidentity-bootstrap", "openbaopkihost", "emqxpkihost"} {
				if name != missing {
					if err := os.WriteFile(filepath.Join(fixture, name), []byte("isolated-packaged-binary"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			docker := filepath.Join(fixture, "docker")
			writeTestFile(t, docker, `#!/usr/bin/env python3
import json, os, subprocess, sys
args = sys.argv[1:]
binary = args[args.index("--entrypoint") + 1]
if binary == "/bin/sh":
    assert args[args.index("--entrypoint") + 2] == os.environ["RTK_BOOTSTRAP_IMAGE_FIXTURE"]
    command = args[-1].replace("/app/", os.environ["RTK_BOOTSTRAP_BINARY_FIXTURE"] + "/")
    command = command.replace("openssl version >/dev/null", "true")
    sys.exit(subprocess.run(["/bin/sh", "-ec", command]).returncode)
services = {"rtk-account-manager": "account-manager", "api": "api"}
service = services.get(binary.rsplit("/", 1)[-1], binary.rsplit("/", 1)[-1])
features = {
    "account-manager": ["deployment-bootstrap-finalize-v1"],
    "pkimanagement": ["first-trust-v1", "deployment-bootstrap-finalize-v1"],
    "certissuer": ["create-service-enrollment-v1", "registry-only-issuance-v1"],
    "api": ["fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1"],
    "logingester": ["fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1"],
    "pkibroker": ["fresh-bootstrap-trust-v1"],
    "serviceidentity-bootstrap": ["owned-server-export-v1"],
    "emqxpkihost": ["managed-callback-export-v1"],
}
print(json.dumps({"schema": "rtk-cloud.deployment-capabilities/v1", "service": service, "features": features[service]}))
`)
			if err := os.Chmod(docker, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_BOOTSTRAP_IMAGE_FIXTURE", image)
			t.Setenv("RTK_BOOTSTRAP_BINARY_FIXTURE", fixture)
			before := createBootstrapStateSnapshot(t, store.Root)
			err = qualifyEnvironmentCreateBootstrap(context.Background(), cfg, p, store, operator)
			if missing == "" && err != nil || missing != "" && (err == nil || !strings.Contains(err.Error(), "exact formal API image lacks complete native bootstrap")) {
				t.Fatal("exact formal bootstrap image admitted a missing installer or rejected the complete package", err)
			}
			if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("binary qualification changed original state")
			}
		})
	}
}
