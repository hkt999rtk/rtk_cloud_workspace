package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProvisionArgsCoversModesValuesAndFailures(t *testing.T) {
	args := []string{
		"--preflight", "--plan", "--reset", "--apply", "--dns", "--deploy", "--artifacts", "--e2e",
		"--workspace", "/workspace",
		"--env-root", "/runtime",
		"--operator-env", "/operator.env",
		"--ssh-key", "/id_ed25519",
		"--dns-root-domain", "example.test",
		"--artifact-dir", "/artifacts",
		"--video-release", "video.tgz",
		"--account-release", "account.tgz",
		"--account-release-bundle", "account-bundle.tgz",
		"--admin-release", "admin.tgz",
		"--admin-release-bundle", "admin-bundle.tgz",
		"--confirm", "video-cloud-staging",
		"--verbose",
	}
	opts, err := parseProvisionArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.mode.preflight || !opts.mode.plan || !opts.mode.reset || !opts.mode.apply ||
		!opts.mode.dns || !opts.mode.deploy || !opts.mode.artifacts || !opts.mode.e2e {
		t.Fatalf("modes = %#v", opts.mode)
	}
	if opts.workspace != "/workspace" || opts.envRoot != "/runtime" || opts.operatorEnv != "/operator.env" ||
		opts.sshKey != "/id_ed25519" || opts.dnsRoot != "example.test" || !opts.dnsRootExplicit ||
		opts.artifactDir != "/artifacts" || opts.videoRelease != "video.tgz" ||
		opts.accountRelease != "account.tgz" || opts.accountReleaseBundle != "account-bundle.tgz" ||
		opts.adminRelease != "admin.tgz" || opts.adminReleaseBundle != "admin-bundle.tgz" ||
		opts.confirm != "video-cloud-staging" || !opts.verbose {
		t.Fatalf("options = %#v", opts)
	}
	for _, flagName := range []string{
		"--workspace", "--env-root", "--secrets-root", "--operator-env", "--ssh-key",
		"--dns-root-domain", "--artifact-dir", "--video-release", "--account-release",
		"--account-release-bundle", "--admin-release", "--admin-release-bundle", "--confirm",
	} {
		if _, err := parseProvisionArgs([]string{flagName}); err == nil || !strings.Contains(err.Error(), "requires a value") {
			t.Fatalf("%s error = %v", flagName, err)
		}
	}
	if _, err := parseProvisionArgs([]string{"--unknown", "--env-root", "/runtime"}); err == nil {
		t.Fatal("unknown argument unexpectedly passed")
	}
	if _, err := parseProvisionArgs(nil); err == nil || !strings.Contains(err.Error(), "--env-root") {
		t.Fatalf("missing env root error = %v", err)
	}
	if _, err := parseProvisionArgs([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	for _, mode := range []string{"--all", "--reset-and-all"} {
		opts, err := parseProvisionArgs([]string{mode, "--env-root", "/runtime"})
		if err != nil || !opts.mode.preflight || !opts.mode.e2e {
			t.Fatalf("%s options = %#v, error = %v", mode, opts, err)
		}
	}
}

func TestProvisionStateAndCredentialHelpers(t *testing.T) {
	root := t.TempDir()
	opts := provisionOptions{operatorEnv: "/custom/operator.env"}
	paths := newProvisionPaths("/workspace", root, opts)
	if paths.Workspace != "/workspace" || paths.EnvRoot != root || paths.OperatorEnv != opts.operatorEnv ||
		paths.VideoState == "" || paths.ArtifactsDir == "" {
		t.Fatalf("paths = %#v", paths)
	}

	statePath := filepath.Join(root, "state", "service.env")
	if err := writeStateVar(statePath, "SECOND", "2"); err != nil {
		t.Fatal(err)
	}
	if err := writeStateVar(statePath, "FIRST", "1"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(statePath)
	if err != nil || string(body) != "FIRST=1\nSECOND=2\n" {
		t.Fatalf("state = %q, error = %v", body, err)
	}
	jsonPath := filepath.Join(root, "state.json")
	if err := os.WriteFile(jsonPath, []byte(`{"subnet_id":"subnet-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths.VideoState = jsonPath
	if videoSubnetID(paths) != "subnet-1" {
		t.Fatal("video subnet was not read")
	}
	if atoiOrZero("12") != 12 || atoiOrZero("bad") != 0 || len(asSlice([]any{"one"})) != 1 || asSlice("one") != nil {
		t.Fatal("conversion helper mismatch")
	}

	operatorPath := filepath.Join(root, "env", "operator.env")
	if err := writeEnvMap(operatorPath, map[string]string{
		"AWS_ACCESS_KEY_ID":     "access",
		"AWS_SECRET_ACCESS_KEY": "secret",
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	if err := mergeObjectStorageCredentialDefaults(root, values); err != nil {
		t.Fatal(err)
	}
	if values["LINODE_OBJ_ACCESS_KEY_ID"] != "access" || values["LINODE_OBJ_SECRET_ACCESS_KEY"] != "secret" {
		t.Fatalf("credentials = %#v", values)
	}

	merged := mergeEnv(map[string]string{"one": "base", "two": "base"}, map[string]string{"two": "overlay"})
	if merged["one"] != "base" || merged["two"] != "overlay" {
		t.Fatalf("merged env = %#v", merged)
	}
	redacted := redactEnvValues(map[string]string{"API_TOKEN": "secret", "NORMAL": "visible"})
	if redacted["API_TOKEN"] != "REDACTED" || redacted["NORMAL"] != "visible" ||
		!sensitiveEnvKey("private-key") || sensitiveEnvKey("NORMAL") {
		t.Fatalf("redacted env = %#v", redacted)
	}
}
