package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/legacy"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/runner"
)

type commandSpec struct {
	script string
	run    func([]string) error
}

var commands = map[string]commandSpec{
	"bind-devices":           {run: runBindDevices},
	"check-certificates":     {run: runCheckCertificates},
	"collect-evidence":       {script: "collect-private-cloud-evidence.sh"},
	"create-brandname-cloud": {run: runCreateBrandnameCloud},
	"create-users":           {run: runCreateUsers},
	"deploy":                 {script: "cloud-deploy.sh"},
	"docs-check":             {run: runDocsCheck},
	"generate-load-devices":  {run: runGenerateLoadDevices},
	"list-brandname-clouds":  {run: runListBrandnameClouds},
	"logs-check":             {run: runLogsCheck},
	"migrate-env":            {run: runMigrateEnv},
	"mqtt-test":              {run: runMQTTTest},
	"provision":              {script: "cloud-provision.sh"},
	"remove-all-vm":          {run: runRemoveAllVM},
	"secrets-check":          {run: runSecretsCheck},
	"staging-e2e-test":       {run: runStagingE2ETest},
	"status-all":             {run: runStatusAll},
	"sync-all":               {run: runSyncAll},
	"test-matrix":            {run: runTestMatrix},
	"unprovision-devices":    {run: runUnprovisionDevices},
	"update-ssh-whitelist":   {run: runUpdateSSHWhitelist},
	"validate-device-bind":   {run: runValidateDeviceBind},
}

var ciRunnerCommands = map[string]commandSpec{
	"archive-artifacts": {script: "linode-ci-runners/archive-ci-artifacts.sh"},
	"list":              {run: runCIRunnersList},
	"power":             {run: runCIRunnersPower},
	"provision":         {script: "linode-ci-runners/provision-ci-runners.sh"},
	"run-session":       {script: "linode-ci-runners/run-ci-session.sh"},
	"wait-online":       {run: runCIRunnersWaitOnline},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var code exitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string {
	return fmt.Sprintf("exit status %d", int(e))
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printUsage()
		return nil
	}
	args = normalizeLegacyPathArgs(args)
	cmdName := args[0]
	if cmdName == "ci-runners" {
		if len(args) < 2 || args[1] == "-h" || args[1] == "--help" {
			printCIRunnerUsage()
			return nil
		}
		spec, ok := ciRunnerCommands[args[1]]
		if !ok {
			return fmt.Errorf("unknown ci-runners command: %s", args[1])
		}
		if spec.run != nil {
			return spec.run(args[2:])
		}
		return runLegacy(spec.script, normalizeLegacyPathArgs(args[2:]))
	}
	spec, ok := commands[cmdName]
	if !ok {
		return fmt.Errorf("unknown command: %s", cmdName)
	}
	if cmdName == "deploy" && !hasFlag(args[1:], "--env-root") && !hasFlag(args[1:], "--operator-env") {
		return errors.New("--env-root is required")
	}
	if spec.run != nil {
		return spec.run(args[1:])
	}
	return runLegacy(spec.script, normalizeLegacyPathArgs(args[1:]))
}

func normalizeLegacyPathArgs(args []string) []string {
	pathFlags := map[string]bool{
		"--env-root":      true,
		"--secrets-root":  true,
		"--operator-env":  true,
		"--workspace":     true,
		"--out-dir":       true,
		"--users-file":    true,
		"--devices-dir":   true,
		"--bind-artifact": true,
		"--ssh-key":       true,
		"--public-key":    true,
		"--state-dir":     true,
		"--repo-root":     true,
		"--artifacts-dir": true,
		"--output-dir":    true,
		"--config":        true,
	}
	out := append([]string(nil), args...)
	cwd, err := os.Getwd()
	if err != nil {
		return out
	}
	for i := 0; i < len(out); i++ {
		arg := out[i]
		if pathFlags[arg] && i+1 < len(out) {
			out[i+1] = absIfRelative(cwd, out[i+1])
			i++
			continue
		}
		if name, value, ok := strings.Cut(arg, "="); ok && pathFlags[name] {
			out[i] = name + "=" + absIfRelative(cwd, value)
		}
	}
	return out
}

func absIfRelative(cwd, value string) string {
	if value == "" || strings.HasPrefix(value, "-") || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func runMQTTTest(args []string) error {
	fs := flag.NewFlagSet("mqtt-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRoot := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	outDir := fs.String("out-dir", "", "output directory")
	profile := fs.String("profile", "smoke", "profile")
	duration := fs.Int("duration-seconds", 120, "duration seconds")
	maxUsers := fs.String("max-users", "", "max users")
	seed := fs.Int("seed", 20260531, "seed")
	mqttProbe := true
	fs.BoolFunc("mqtt-probe", "run mqtt probe", func(string) error { mqttProbe = true; return nil })
	fs.BoolFunc("no-mqtt-probe", "skip mqtt probe", func(string) error { mqttProbe = false; return nil })
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRoot == "" {
		return errors.New("--env-root is required")
	}
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *profile != "smoke" && *profile != "real-case" {
		return errors.New("--profile must be smoke or real-case")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	resolvedEnv, err := resolveEnvRoot(workspace, *envRoot)
	if err != nil {
		return err
	}
	if *maxUsers == "" && *profile == "smoke" {
		*maxUsers = "1"
	}
	if *outDir == "" {
		*outDir = filepath.Join(resolvedEnv, "artifacts", "home-mqtt-loadtest", time.Now().UTC().Format("20060102T150405Z"))
	}
	goCmd, err := exec.LookPath("go")
	if err != nil {
		return errors.New("go is required")
	}
	cmd := exec.Command(goCmd, "run", "./cloud-mqtt-test",
		"--root", workspace,
		"--env-root", resolvedEnv,
		"--brandname", *brandname,
		"--out-dir", *outDir,
		"--profile", *profile,
		"--duration-seconds", strconv.Itoa(*duration),
		"--max-users", *maxUsers,
		"--seed", strconv.Itoa(*seed),
		"--mqtt-probe", strconv.FormatBool(mqttProbe),
	)
	cmd.Dir = filepath.Join(workspace, "scripts", "go")
	cmd.Env = withEnv(os.Environ(), map[string]string{"GOWORK": "off"})
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

type certCheckResult struct {
	Target    string `json:"target"`
	Domain    string `json:"domain"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	DaysLeft  any    `json:"days_left"`
	ExpiresAt string `json:"expires_at"`
	Issuer    string `json:"issuer"`
	Detail    string `json:"detail"`
}

func runCheckCertificates(args []string) error {
	fs := flag.NewFlagSet("check-certificates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	dnsRoot := fs.String("dns-root-domain", "realtekconnect.com", "dns root domain")
	minValidDays := fs.Int("min-valid-days", 7, "minimum valid days")
	jsonOut := fs.Bool("json", false, "json output")
	skipLive := fs.Bool("skip-live", false, "skip live")
	_ = skipLive
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	adminEnv := firstExistingPath(filepath.Join(envRoot, "services", "cloud-admin", "admin.env"), filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"))
	videoDomain := "video-cloud-staging." + *dnsRoot
	certIssuerDomain := "certissuer.video-cloud-staging." + *dnsRoot
	accountDomain := envFileValue(accountEnv, "ACCOUNT_MANAGER_LINODE_DOMAIN")
	adminDomain := envFileValue(adminEnv, "ADMIN_LINODE_DOMAIN")
	targets := []struct {
		name   string
		domain string
		dir    string
	}{
		{"video-cloud", videoDomain, filepath.Join(envRoot, "certificates", videoDomain)},
		{"video-cloud-certissuer", certIssuerDomain, filepath.Join(envRoot, "certificates", videoDomain)},
		{"account-manager", accountDomain, filepath.Join(envRoot, "certificates", accountDomain)},
		{"cloud-admin", adminDomain, filepath.Join(envRoot, "certificates", adminDomain)},
	}
	results := []certCheckResult{}
	overall := "pass"
	for _, target := range targets {
		result := checkCertTarget(target.name, target.domain, filepath.Join(target.dir, "fullchain.pem"), *minValidDays)
		if result.Status != "pass" {
			overall = "fail"
		}
		results = append(results, result)
	}
	payload := map[string]any{"status": overall, "results": results}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	fmt.Fprintf(os.Stdout, "cloud_certificates status=%s min_valid_days=%d env_root=%s\n", overall, *minValidDays, envRoot)
	for _, result := range results {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%v\t%s\n", result.Target, result.Domain, result.Status, result.DaysLeft, result.Detail)
	}
	if overall != "pass" {
		return exitCode(1)
	}
	return nil
}

func runMigrateEnv(args []string) error {
	fs := flag.NewFlagSet("migrate-env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	force := fs.Bool("force", false, "force")
	_ = force
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	backup := filepath.Join(envRoot, "backups", "migration-"+timestamp)
	manifest := filepath.Join(backup, "migration-manifest.tsv")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		return err
	}
	rows := [][]string{{"item", "source", "destination", "status", "detail"}}
	copyItem := func(item, src, dst string) {
		status := "missing"
		detail := "source not found"
		if err := copyPath(src, dst); err == nil {
			status = "copied"
			detail = "ok"
		} else if !os.IsNotExist(err) {
			status = "error"
			detail = err.Error()
		}
		rows = append(rows, []string{item, src, dst, status, detail})
	}
	copyItem("operator-env", filepath.Join(workspace, ".secrets", "staging", "linode", "video-cloud", "env", "operator.env"), filepath.Join(envRoot, "env", "operator.env"))
	copyItem("video-topology", filepath.Join(workspace, ".secrets", "staging", "linode", "video-cloud", "config", "video-cloud-staging.yaml"), filepath.Join(envRoot, "topology", "video-cloud-staging.yaml"))
	copyItem("video-env", filepath.Join(workspace, ".secrets", "staging", "linode", "video-cloud", "env", "video-cloud-staging.env"), filepath.Join(envRoot, "services", "video-cloud", "video-cloud-staging.env"))
	copyItem("account-manager-env", filepath.Join(workspace, "repos", "rtk_account_manager", "linode_deploy", "secrets", "account-manager-public-staging.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	copyItem("account-manager-platform-admin", filepath.Join(workspace, "repos", "rtk_account_manager", "linode_deploy", "secrets", "account-manager-platform-admin.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env"))
	copyItem("account-manager-state", filepath.Join(workspace, "repos", "rtk_account_manager", "linode_deploy", "state", "rtk-account-manager-staging.env"), filepath.Join(envRoot, "state", "account-manager-staging.env"))
	copyItem("cloud-admin-env", filepath.Join(workspace, "repos", "rtk_cloud_admin", "deploy", "linode", "admin-staging.env"), filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"))
	copyItem("cloud-admin-state", filepath.Join(workspace, "repos", "rtk_cloud_admin", "deploy", "linode", "rtk-cloud-admin-staging.state"), filepath.Join(envRoot, "state", "cloud-admin-staging.env"))
	copyItem("video-state", filepath.Join(workspace, "repos", "rtk_video_cloud", "linode_deploy", "state", "video-cloud-staging.state.json"), filepath.Join(envRoot, "state", "video-cloud-staging.state.json"))
	copyItem("video-key-root-ca", filepath.Join(workspace, "keys", "staging", "linode", "video-cloud", "root-ca.key.pem"), filepath.Join(envRoot, "keys", "video-cloud", "root-ca.key.pem"))
	copyItem("test-device-manifest", filepath.Join(workspace, "keys", "test_device", "manifests", "device_ids.txt"), filepath.Join(envRoot, "devices", "test_device", "manifests", "device_ids.txt"))
	copyItem("artifacts", filepath.Join(workspace, ".secrets", "staging", "linode", "video-cloud", "artifacts"), filepath.Join(envRoot, "artifacts"))
	stackPath := filepath.Join(envRoot, "env", "stack.env")
	if err := os.MkdirAll(filepath.Dir(stackPath), 0o755); err != nil {
		return err
	}
	accountDomain := envFileValue(filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"), "ACCOUNT_MANAGER_LINODE_DOMAIN")
	adminDomain := envFileValue(filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"), "ADMIN_LINODE_DOMAIN")
	stack := fmt.Sprintf(`CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-staging.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=%s
CLOUD_ADMIN_DOMAIN=%s
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-staging
VIDEO_CLOUD_VPC_LABEL=video-cloud-staging-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-staging-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
`, firstNonEmpty(accountDomain, "account-manager.video-cloud-staging.realtekconnect.com"), firstNonEmpty(adminDomain, "admin.video-cloud-staging.realtekconnect.com"))
	if err := os.WriteFile(stackPath, []byte(stack), 0o644); err != nil {
		return err
	}
	rows = append(rows, []string{"stack-metadata", "generated", stackPath, "copied", "ok"})
	if err := writeTSV(manifest, rows); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-env-migrate] workspace=%s\n", workspace)
	fmt.Fprintf(os.Stderr, "[cloud-env-migrate] env_root=%s\n", envRoot)
	fmt.Fprintf(os.Stderr, "[cloud-env-migrate] backup=%s\n", backup)
	fmt.Fprintf(os.Stderr, "[cloud-env-migrate] migration manifest: %s\n", manifest)
	fmt.Fprintf(os.Stdout, "manifest=%s\n", manifest)
	return nil
}

type linodeList[T any] struct {
	Data []T `json:"data"`
}

type linodeEntity struct {
	ID     int    `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

func runRemoveAllVM(args []string) error {
	fs := flag.NewFlagSet("remove-all-vm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	confirm := fs.Bool("yes", false, "confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	if !*confirm {
		fmt.Fprint(os.Stderr, `Delete all Linode VMs whose label contains "staging"? Type yes to continue: `)
		var answer string
		_, _ = fmt.Fscan(os.Stdin, &answer)
		if answer != "yes" {
			fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] cancelled")
			return nil
		}
	}
	token := os.Getenv("LINODE_TOKEN")
	if token == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	instances, err := linodeGetList[linodeEntity](token, "/linode/instances?page_size=500")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] deleting VMs:")
	for _, vm := range instances {
		if !strings.Contains(vm.Label, "staging") {
			continue
		}
		fmt.Fprintf(os.Stderr, "  - %s (%d)\n", vm.Label, vm.ID)
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] delete %s (%d)\n", vm.Label, vm.ID)
		if err := linodeDelete(token, fmt.Sprintf("/linode/instances/%d", vm.ID)); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] VM delete requests submitted")
	firewalls, err := linodeGetList[linodeEntity](token, "/networking/firewalls?page_size=500")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] deleting staging firewalls:")
	for _, fw := range firewalls {
		if !isStagingFirewall(fw.Label) {
			continue
		}
		fmt.Fprintf(os.Stderr, "  - %s (%d)\n", fw.Label, fw.ID)
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] delete firewall %s (%d)\n", fw.Label, fw.ID)
		if err := linodeDelete(token, fmt.Sprintf("/networking/firewalls/%d", fw.ID)); err != nil {
			return err
		}
	}
	vpcs, err := linodeGetList[linodeEntity](token, "/vpcs?page_size=500")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] deleting staging VPCs:")
	for _, vpc := range vpcs {
		if !strings.Contains(vpc.Label, "staging") {
			continue
		}
		fmt.Fprintf(os.Stderr, "  - %s (%d)\n", vpc.Label, vpc.ID)
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] delete VPC %s (%d)\n", vpc.Label, vpc.ID)
		if err := linodeDelete(token, fmt.Sprintf("/vpcs/%d", vpc.ID)); err != nil {
			return err
		}
	}
	if err := backupAndRemoveState(envRoot); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] remove complete")
	return nil
}

func linodeGetList[T any](token, path string) ([]T, error) {
	out, err := exec.Command("curl", "-fsS", "-X", "GET", "https://api.linode.com/v4"+path, "-H", "Authorization: Bearer "+token, "-H", "Content-Type: application/json").Output()
	if err != nil {
		return nil, err
	}
	var parsed linodeList[T]
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

func linodeDelete(token, path string) error {
	cmd := exec.Command("curl", "-fsS", "-X", "DELETE", "https://api.linode.com/v4"+path, "-H", "Authorization: Bearer "+token, "-H", "Content-Type: application/json")
	cmd.Stdout = ioDiscard{}
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func isStagingFirewall(label string) bool {
	return strings.Contains(label, "video-cloud-staging") ||
		label == "rtk-account-manager-staging-fw" ||
		label == "rtk-cloud-admin-staging-firewall"
}

func backupAndRemoveState(envRoot string) error {
	stateDir := filepath.Join(envRoot, "state")
	backupDir := filepath.Join(envRoot, "backups", "remove-vm-"+time.Now().UTC().Format("20060102T150405Z"), "state")
	files := []string{"video-cloud-staging.state.json", "account-manager-staging.env", "cloud-admin-staging.env"}
	for _, name := range files {
		src := filepath.Join(stateDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		dst := filepath.Join(backupDir, name)
		if err := copyFile(src, dst); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] removed local state: %s\n", src)
	}
	if _, err := os.Stat(backupDir); err == nil {
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] local state backup: %s\n", filepath.Dir(backupDir))
	}
	return nil
}

func runStatusAll(args []string) error {
	fs := flag.NewFlagSet("status-all", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "workspace:")
	if err := runCmd(workspace, "git", "status", "--short", "--branch"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, path := range submodules {
		abs := filepath.Join(workspace, path)
		if !exists(abs) {
			continue
		}
		fmt.Fprintln(os.Stdout)
		fmt.Fprintf(os.Stdout, "[%s] %s\n", filepath.Base(path), path)
		if err := runCmd(abs, "git", "status", "--short", "--branch"); err != nil {
			return err
		}
		if err := runCmd(abs, "git", "log", "-1", "--oneline", "--decorate"); err != nil {
			return err
		}
	}
	return nil
}

func runSyncAll(args []string) error {
	fs := flag.NewFlagSet("sync-all", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	steps := [][]string{
		{"git", "fetch", "origin"},
		{"git", "submodule", "sync", "--recursive"},
		{"git", "submodule", "update", "--init", "--recursive"},
	}
	for _, step := range steps {
		if err := runCmd(workspace, step[0], step[1:]...); err != nil {
			return err
		}
	}
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, path := range submodules {
		abs := filepath.Join(workspace, path)
		if exists(abs) {
			if err := runCmd(abs, "git", "fetch", "--all", "--prune"); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(os.Stdout, "Fetched workspace and submodule remotes. Pinned commits were not changed.")
	return nil
}

func runDocsCheck(args []string) error {
	fs := flag.NewFlagSet("docs-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	check := newCheck()
	fmt.Fprintln(os.Stdout, "== workspace documentation entries ==")
	for _, path := range []string{
		"README.md",
		"docs/README.md",
		"docs/architecture.md",
		"docs/documentation-governance.md",
		"docs/deployment-secrets-governance.md",
		"docs/linode-ci-runners.md",
		"docs/examples/secrets-manifest.example.json",
		"docs/testing.md",
		"docs/LOAD_TEST_REPORT.md",
		"e2e_test/README.md",
		"e2e_test/go.mod",
		"e2e_test/fixtures/README.md",
		"e2e_test/factory_enroll/README.md",
		"e2e_test/factory_enroll/cmd/rtk-factory-enroll-test/main.go",
		"e2e_test/provisioning/account_video_smoke/README.md",
		"e2e_test/provisioning/account_video_smoke/cmd/rtk-account-video-smoke/main.go",
		"e2e_test/provisioning/bulk_bind_validation/README.md",
		"e2e_test/provisioning/bulk_bind_validation/cmd/rtk-bulk-bind-validate/main.go",
		"e2e_test/admin_bff/README.md",
		"e2e_test/video_cloud/load/cmd/rtk-video-loadtest/main.go",
		"docs/adr/README.md",
		"docs/product-level-evidence.md",
		"docs/cross-service-broker-packaging.md",
		"repos/rtk_cloud_contracts_doc/README.md",
		"scripts/README.zh-TW.md",
		"scripts/go/go.mod",
		"scripts/go/rtk-cloud/main.go",
		"scripts/go/rtk-cloud/internal/envroot/envroot.go",
		"scripts/go/rtk-cloud/internal/runner/runner.go",
		"scripts/go/linode-object-storage/main.go",
		"scripts/go/cloud-mqtt-test/main.go",
		"tests/helpers/factory_enroll_mock.go",
		"tests/staging-bind-devices.test.sh",
		"tests/staging-bind-validation.test.sh",
	} {
		check.requireFile(workspace, path)
	}
	if anyFileContains(workspace, []string{"README.md", "docs/architecture.md"}, "repos/rtk_mqtt") {
		check.fail("workspace README or architecture still references repos/rtk_mqtt")
	} else {
		check.pass("removed repos/rtk_mqtt workspace references")
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== submodule registry ==")
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	readme := readText(filepath.Join(workspace, "README.md"))
	for _, path := range submodules {
		check.requireDir(workspace, path)
		if strings.Contains(readme, "`"+path+"`") || strings.Contains(readme, path) {
			check.pass("README documents " + path)
		} else {
			check.fail("README does not document " + path)
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== service documentation entry points ==")
	for _, path := range []string{
		"repos/rtk_cloud_client/docs/README.md",
		"repos/rtk_video_cloud/docs/architecture.md",
		"repos/rtk_account_manager/docs/SPEC.md",
		"repos/rtk_cloud_frontend/README.md",
		"repos/rtk_cloud_admin/README.md",
	} {
		check.requireFile(workspace, path)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== contracts submodule alignment ==")
	contractPaths := []string{
		"repos/rtk_cloud_contracts_doc",
		"repos/rtk_account_manager/contracts",
		"repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc",
		"repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc",
		"repos/rtk_cloud_admin/rtk_cloud_contracts_doc",
	}
	expected := ""
	for _, path := range contractPaths {
		check.requireDir(workspace, path)
		commit, err := gitOutput(filepath.Join(workspace, path), "rev-parse", "HEAD")
		if err != nil {
			check.fail(path + " commit could not be read")
			continue
		}
		commit = strings.TrimSpace(commit)
		fmt.Fprintf(os.Stdout, "%s %s\n", path, commit)
		if expected == "" {
			expected = commit
		} else if commit != expected {
			fmt.Fprintf(os.Stderr, "WARN: %s is pinned to %s, top-level contracts is %s\n", path, commit, expected)
		}
	}
	if check.failures == 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Documentation checks passed.")
		return nil
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stderr, "Documentation checks failed: %d\n", check.failures)
	return exitCode(1)
}

func runSecretsCheck(args []string) error {
	fs := flag.NewFlagSet("secrets-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	check := newCheck()
	fmt.Fprintln(os.Stdout, "== ignore rules ==")
	for _, path := range []string{
		".secrets",
		".secrets.backup",
		".secrets/staging/linode/admin/env/admin.env",
		"cloud_env/staging/linode/env/operator.env",
		"cloud_env/staging/linode/keys/root-ca.key.pem",
	} {
		if err := exec.Command("git", "-C", workspace, "check-ignore", "-q", path).Run(); err == nil {
			check.pass(path + " is ignored")
		} else {
			check.fail(path + " is not ignored")
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== tracked workspace secret scan ==")
	workspacePaths := []string{".gitignore", "README.md", "docs", "scripts", "e2e_test"}
	checkGitGrepNoMatchFiltered(check, workspace, "private key block", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`, workspacePaths, func(line string) bool {
		return strings.Contains(line, "_test.go:") && strings.Contains(line, `"-----BEGIN PRIVATE KEY-----"`)
	})
	for _, scan := range []struct {
		label   string
		pattern string
	}{
		{"bearer token literal", `Bearer[[:space:]]+[A-Za-z0-9._~+/-]{24,}`},
		{"JWT-like token", `eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`},
		{"hard-coded password assignment", `(^|[^A-Za-z0-9_])(PASSWORD|PASS|TOKEN|SECRET|PRIVATE_KEY)[A-Za-z0-9_]*=[^[:space:]<>$][^[:space:]]{7,}`},
	} {
		checkGitGrepNoMatch(check, workspace, scan.label, scan.pattern, workspacePaths)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== manifest example ==")
	manifest := filepath.Join(workspace, "docs/examples/secrets-manifest.example.json")
	checkFileNoMatch(check, manifest, "private key block", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`)
	checkFileNoMatch(check, manifest, "JWT-like token", `eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`)
	checkFileNoMatch(check, manifest, "production staging reference", `"environment"[[:space:]]*:[[:space:]]*"production"|video-cloud-staging|staging-token|factory-linode-certset|example.invalid`)
	if check.failures > 0 {
		fmt.Fprintf(os.Stderr, "Secrets checks failed: %d\n", check.failures)
		return exitCode(1)
	}
	fmt.Fprintln(os.Stdout, "Secrets checks passed.")
	return nil
}

func runTestMatrix(args []string) error {
	fs := flag.NewFlagSet("test-matrix", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "== workspace status ==")
	if err := runCmd(workspace, "git", "status", "--short", "--branch"); err != nil {
		return err
	}
	if err := runCmd(workspace, "git", "submodule", "status", "--recursive"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== workspace Go validation ==")
	if err := runCmd(workspace, "git", "diff", "--check"); err != nil {
		return err
	}
	if err := runCmd(workspace, "go", "test", "./scripts/go/..."); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== repository status checks ==")
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, repo := range submodules {
		abs := filepath.Join(workspace, repo)
		if !exists(abs) {
			fmt.Fprintf(os.Stdout, "SKIP: %s is missing\n", repo)
			continue
		}
		fmt.Fprintf(os.Stdout, "-- %s\n", repo)
		if err := runCmd(abs, "git", "status", "--short", "--branch"); err != nil {
			return err
		}
	}
	return nil
}

type loadDeviceType struct {
	Name           string
	Model          string
	Capability     string
	ServiceOptions []string
	Capabilities   []string
}

var loadDeviceTypes = []loadDeviceType{
	{"camera", "RTC-CAM-PRO2-SIM", "camera", []string{"mqtt", "video_streaming", "video_storage"}, []string{"camera_event", "status_report", "snapshot", "websocket_owner", "webrtc", "recording_clip", "mqtt_legacy_snapshot"}},
	{"light", "RTC-LIGHT-SIM", "light", []string{"mqtt"}, []string{"mqtt", "power", "brightness", "color_temperature", "state_report", "command_result"}},
	{"air_conditioner", "RTC-AC-SIM", "air_conditioner", []string{"mqtt"}, []string{"mqtt", "power", "target_temperature", "mode", "fan", "state_report", "command_result"}},
	{"smart_meter", "RTC-METER-SIM", "smart_meter", []string{"mqtt"}, []string{"mqtt", "status_report", "telemetry_report", "power_watts", "energy_kwh", "voltage", "current"}},
}

type generatedDevice struct {
	DeviceID             string   `json:"device_id"`
	DeviceType           string   `json:"device_type"`
	MQTTCapability       string   `json:"mqtt_capability"`
	ServiceOptions       []string `json:"service_options"`
	Model                string   `json:"model"`
	DisplayName          string   `json:"display_name"`
	FirmwareVersion      string   `json:"firmware_version"`
	Capabilities         []string `json:"capabilities"`
	CertificateProfile   string   `json:"certificate_profile"`
	CertificatePath      string   `json:"certificate_path"`
	CertificateChainPath string   `json:"certificate_chain_path"`
	KeyPath              string   `json:"key_path"`
	CSRPath              string   `json:"csr_path"`
	BundlePath           string   `json:"bundle_path"`
	Production           bool     `json:"production"`
	Warning              string   `json:"warning"`
}

func runGenerateLoadDevices(args []string) error {
	fs := flag.NewFlagSet("generate-load-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	count := fs.Int("count", 100, "device count")
	mix := fs.String("mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	prefix := fs.String("prefix", "load-device", "device prefix")
	outDir := fs.String("out-dir", "", "output directory")
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	factoryURL := fs.String("factory-url", os.Getenv("FACTORY_ENROLL_URL"), "factory enroll URL")
	factoryAuthKey := fs.String("factory-auth-key", os.Getenv("FACTORY_ENROLL_AUTH_KEY"), "factory enroll auth key")
	factoryID := fs.String("factory-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_FACTORY_ID"), "staging-loadtest"), "factory id")
	lineID := fs.String("line-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_LINE_ID"), "loadtest-line"), "line id")
	stationID := fs.String("station-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_STATION_ID"), "loadtest-station"), "station id")
	fixtureID := fs.String("fixture-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_FIXTURE_ID"), "loadtest-fixture"), "fixture id")
	operatorID := fs.String("operator-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_OPERATOR_ID"), "loadtest-operator"), "operator id")
	batchID := fs.String("batch-id", os.Getenv("FACTORY_ENROLL_BATCH_ID"), "batch id")
	serialPrefix := fs.String("serial-prefix", firstNonEmpty(os.Getenv("FACTORY_ENROLL_SERIAL_PREFIX"), "LOAD"), "serial prefix")
	runID := fs.String("run-id", os.Getenv("FACTORY_ENROLL_RUN_ID"), "run id")
	timeoutSeconds := fs.Int("enroll-timeout", envInt("FACTORY_ENROLL_TIMEOUT", 30), "enroll timeout seconds")
	generateOnly := fs.Bool("generate-only", false, "generate only")
	caValidDays := fs.Int("ca-valid-days", 365, "CA validity days")
	deviceValidDays := fs.Int("device-valid-days", 180, "device validity days")
	force := fs.Bool("force", false, "force")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *count <= 0 {
		return errors.New("--count must be a positive integer")
	}
	if *caValidDays <= 0 || *deviceValidDays <= 0 || *timeoutSeconds <= 0 {
		return errors.New("validity days and enroll timeout must be positive integers")
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`, *prefix); !ok {
		return errors.New("--prefix contains unsupported characters")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	if *batchID == "" {
		*batchID = *runID
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "devices", "test_device")
	}
	if !*generateOnly {
		videoEnv := firstExistingPath(filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"), filepath.Join(envRoot, "services", "video-cloud", "video-cloud-staging.env"))
		if *factoryURL == "" {
			*factoryURL = envFileValue(videoEnv, "FACTORY_ENROLL_URL")
		}
		if *factoryAuthKey == "" {
			*factoryAuthKey = envFileValue(videoEnv, "FACTORY_ENROLL_AUTH_KEY")
		}
		if *factoryURL == "" {
			return errors.New("factory enrollment URL missing; set FACTORY_ENROLL_URL in video-cloud env or pass --factory-url")
		}
		if *factoryAuthKey == "" {
			return errors.New("factory enrollment auth key missing; set FACTORY_ENROLL_AUTH_KEY in video-cloud env or pass --factory-auth-key")
		}
		*factoryURL = strings.TrimRight(*factoryURL, "/")
	}
	if exists(*outDir) {
		if !*force {
			return fmt.Errorf("%s already exists; use --force to replace it", *outDir)
		}
		if err := os.RemoveAll(*outDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	*outDir, _ = filepath.Abs(*outDir)
	opensslLog := filepath.Join(*outDir, "openssl.log")
	if err := os.WriteFile(opensslLog, nil, 0o644); err != nil {
		return err
	}
	mode := "factory_enroll"
	if *generateOnly {
		mode = "generate_only"
	}
	logLoad("start load-test device generation: count=%d mix=%s mode=%s%s", *count, *mix, mode, factoryLogSuffix(mode, *factoryURL))
	logLoad("workspace=%s", workspace)
	logLoad("output=%s", *outDir)
	logLoad("run_id=%s batch_id=%s", *runID, *batchID)
	alloc, err := allocateDeviceMix(*count, *mix)
	if err != nil {
		return err
	}
	var caKey *ecdsa.PrivateKey
	var caCert []byte
	if *generateOnly {
		logLoad("generating simulation device CA")
		caKey, caCert, err = writeGeneratedCA(*outDir, *caValidDays)
		if err != nil {
			return err
		}
	}
	manifestsDir := filepath.Join(*outDir, "manifests")
	if err := os.MkdirAll(manifestsDir, 0o755); err != nil {
		return err
	}
	csvPath := filepath.Join(manifestsDir, "devices.csv")
	deviceIDsPath := filepath.Join(manifestsDir, "device_ids.txt")
	enrollResultsPath := filepath.Join(manifestsDir, "factory-enroll-results.jsonl")
	if err := os.WriteFile(csvPath, []byte("device_id,device_type,mqtt_capability,service_options,model,certificate_path,key_path,bundle_path\n"), 0o644); err != nil {
		return err
	}
	_ = os.WriteFile(deviceIDsPath, nil, 0o644)
	_ = os.WriteFile(enrollResultsPath, nil, 0o644)
	devices := []generatedDevice{}
	deviceIDs := []string{}
	enrollSucceeded := 0
	enrollFailed := 0
	index := 1
	for _, dt := range loadDeviceTypes {
		n := alloc[dt.Name]
		if n == 0 {
			continue
		}
		logLoad("generating devices: type=%s count=%d", dt.Name, n)
		for ordinal := 1; ordinal <= n; ordinal++ {
			device, ok, err := writeLoadDevice(loadDeviceInput{
				Index:          index,
				Ordinal:        ordinal,
				Type:           dt,
				Prefix:         *prefix,
				OutDir:         *outDir,
				GenerateOnly:   *generateOnly,
				CAKey:          caKey,
				CACert:         caCert,
				DeviceDays:     *deviceValidDays,
				FactoryURL:     *factoryURL,
				FactoryAuthKey: *factoryAuthKey,
				FactoryID:      *factoryID,
				LineID:         *lineID,
				StationID:      *stationID,
				FixtureID:      *fixtureID,
				OperatorID:     *operatorID,
				BatchID:        *batchID,
				SerialPrefix:   *serialPrefix,
				RunID:          *runID,
				Timeout:        time.Duration(*timeoutSeconds) * time.Second,
				ResultsPath:    enrollResultsPath,
			})
			if err != nil {
				return err
			}
			if ok {
				enrollSucceeded++
				devices = append(devices, device)
				deviceIDs = append(deviceIDs, device.DeviceID)
				appendCSV(csvPath, device)
				appendLine(deviceIDsPath, device.DeviceID)
			} else {
				enrollFailed++
			}
			index++
		}
	}
	if err := writeJSON(filepath.Join(manifestsDir, "devices.json"), devices); err != nil {
		return err
	}
	profile := "mixed"
	if alloc["camera"] == *count {
		profile = "camera"
	} else if alloc["camera"] == 0 {
		profile = "iot"
	}
	iotMix := fmt.Sprintf("light=%d,air_conditioner=%d,smart_meter=%d", alloc["light"], alloc["air_conditioner"], alloc["smart_meter"])
	loadtestEnv := fmt.Sprintf(`# Source this file before e2e_test/video_cloud/load/scripts/run_video_loadtest.sh.
# It contains no bearer tokens; provide VIDEO_CLOUD_LOAD_*_TOKEN separately.
export VIDEO_CLOUD_LOAD_DEVICE_PREFIX='%s'
export VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES=%d
export VIDEO_CLOUD_LOAD_DEVICE_IDS='%s'
export VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE='%s'
export VIDEO_CLOUD_LOAD_MQTT_IOT_MIX='%s'
export VIDEO_CLOUD_LOAD_DEVICE_MANIFEST='%s'
export VIDEO_CLOUD_LOAD_DEVICE_CERT_ROOT='%s'
`, shellQuote(*prefix), *count, strings.Join(deviceIDs, ","), shellQuote(profile), shellQuote(iotMix), shellQuote(filepath.Join(*outDir, "manifests", "devices.json")), shellQuote(*outDir))
	if err := os.WriteFile(filepath.Join(*outDir, "loadtest.env"), []byte(loadtestEnv), 0o644); err != nil {
		return err
	}
	summary := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"count":         *count,
		"prefix":        *prefix,
		"requested_mix": *mix,
		"allocated":     alloc,
		"enrollment":    map[string]any{"mode": mode, "factory_url": *factoryURL, "succeeded": enrollSucceeded, "failed": enrollFailed, "results": "manifests/factory-enroll-results.jsonl"},
		"paths":         map[string]any{"output_dir": *outDir, "loadtest_env": "loadtest.env", "device_ids": "manifests/device_ids.txt", "devices_csv": "manifests/devices.csv", "devices_json": "manifests/devices.json", "ca_cert": "ca/sim-device-ca.cert.pem"},
	}
	if err := writeJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		return err
	}
	if err := writeLoadDeviceReadme(*outDir, *count, *mix, mode, *factoryURL, *caValidDays, *deviceValidDays); err != nil {
		return err
	}
	if enrollFailed > 0 {
		logLoad("complete with failures: requested=%d succeeded=%d failed=%d results=%s", *count, enrollSucceeded, enrollFailed, enrollResultsPath)
		fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\nenroll_results=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), enrollResultsPath, opensslLog)
		return exitCode(1)
	}
	logLoad("complete: requested=%d succeeded=%d failed=%d", *count, enrollSucceeded, enrollFailed)
	fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\nenroll_results=%s\nloadtest_env=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), enrollResultsPath, filepath.Join(*outDir, "loadtest.env"), opensslLog)
	return nil
}

type firewallRules struct {
	InboundPolicy  string         `json:"inbound_policy,omitempty"`
	OutboundPolicy string         `json:"outbound_policy,omitempty"`
	Inbound        []firewallRule `json:"inbound"`
	Outbound       []firewallRule `json:"outbound"`
	Version        any            `json:"version,omitempty"`
	Fingerprint    any            `json:"fingerprint,omitempty"`
}

type firewallRule struct {
	Label     string              `json:"label,omitempty"`
	Action    string              `json:"action,omitempty"`
	Protocol  string              `json:"protocol,omitempty"`
	Ports     string              `json:"ports,omitempty"`
	Addresses map[string][]string `json:"addresses,omitempty"`
}

func runUpdateSSHWhitelist(args []string) error {
	fs := flag.NewFlagSet("update-ssh-whitelist", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cidr := fs.String("cidr", "", "CIDR")
	mode := fs.String("mode", "", "append or replace")
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	operatorEnv := fs.String("operator-env", "", "operator env")
	dryRun := fs.Bool("dry-run", false, "dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *mode == "" {
		*mode = "append"
	}
	if *mode != "append" && *mode != "replace" {
		return fmt.Errorf("invalid --mode: %s; expected append or replace", *mode)
	}
	if *cidr == "" {
		return errors.New("--cidr is required in Go CLI mode")
	}
	if ok, _ := regexp.MatchString(`^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$`, *cidr); !ok {
		return fmt.Errorf("invalid IPv4 CIDR: %s", *cidr)
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	if *operatorEnv == "" {
		*operatorEnv = filepath.Join(envRoot, "env", "operator.env")
	}
	if token := envFileValue(*operatorEnv, "LINODE_TOKEN"); token != "" && os.Getenv("LINODE_TOKEN") == "" {
		_ = os.Setenv("LINODE_TOKEN", token)
	}
	if os.Getenv("LINODE_TOKEN") == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] allowing SSH CIDR: mode=%s cidr=%s\n", *mode, *cidr)
	targets, err := firewallTargets(envRoot)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ID == "" || target.ID == "null" {
			fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] skip: %s firewall id missing label=%s\n", target.Role, target.Label)
			continue
		}
		if err := updateFirewallRules(target, *mode, *cidr, *dryRun); err != nil {
			return err
		}
	}
	if !*dryRun {
		videoConfig := filepath.Join(envRoot, "topology", "video-cloud-staging.yaml")
		accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
		adminEnv := firstExistingPath(filepath.Join(envRoot, "services", "cloud-admin", "admin.env"), filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"))
		if *mode == "replace" {
			updateCSVEnv(accountEnv, "ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS", *cidr, false)
			updateCSVEnv(adminEnv, "ADMIN_LINODE_ALLOWED_SSH_CIDRS", *cidr, false)
			updateVideoCIDR(videoConfig, *cidr, false)
		} else {
			updateCSVEnv(accountEnv, "ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS", *cidr, true)
			updateCSVEnv(adminEnv, "ADMIN_LINODE_ALLOWED_SSH_CIDRS", *cidr, true)
			updateVideoCIDR(videoConfig, *cidr, true)
		}
		fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] local ignored staging config/env updated: mode=%s cidr=%s\n", *mode, *cidr)
	}
	return nil
}

type accountManagerContext struct {
	EnvRoot          string
	BaseURL          string
	AdminEmail       string
	AdminPassword    string
	Host             string
	SSHUser          string
	SSHKey           string
	PlatformAdminEnv string
}

func runListBrandnameClouds(args []string) error {
	fs := flag.NewFlagSet("list-brandname-clouds", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	brandname := fs.String("brandname", "", "brand name")
	limit := fs.Int("limit", 200, "limit")
	jsonOutput := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *limit <= 0 {
		return errors.New("--limit must be a positive integer")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logBrandList("start staging brand cloud list")
	logBrandList("workspace=%s", mustWorkspace(*workspaceFlag))
	logBrandList("env_root=%s", ctx.EnvRoot)
	logBrandList("loading Account Manager staging env/state")
	token, err := accountLogin(ctx, logBrandList)
	if err != nil {
		return err
	}
	payload, err := accountListBrandClouds(ctx, token, *limit)
	if err != nil {
		return err
	}
	if *brandname != "" {
		filtered := []any{}
		for _, item := range anySlice(payload["brand_clouds"]) {
			obj, _ := item.(map[string]any)
			metadata, _ := obj["metadata"].(map[string]any)
			if obj["name"] == *brandname || metadata["brandname"] == *brandname {
				filtered = append(filtered, item)
			}
		}
		payload["brand_clouds"] = filtered
		pagination, _ := payload["pagination"].(map[string]any)
		if pagination == nil {
			pagination = map[string]any{}
			payload["pagination"] = pagination
		}
		pagination["filtered_total"] = len(filtered)
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}
	clouds := anySlice(payload["brand_clouds"])
	total := len(clouds)
	if pagination, ok := payload["pagination"].(map[string]any); ok {
		if v, ok := pagination["total"]; ok {
			total = int(asFloat(v))
		}
	}
	if *brandname != "" {
		fmt.Fprintf(os.Stdout, "brand_clouds=%d api_total=%d filter=%s\n", len(clouds), total, *brandname)
	} else {
		fmt.Fprintf(os.Stdout, "brand_clouds=%d api_total=%d\n", len(clouds), total)
	}
	fmt.Fprintf(os.Stdout, "%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n", "id", "name", "status", "tier", "quota", "metadata.brandname", "created_at", "metadata")
	for _, item := range clouds {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		metaJSON, _ := json.Marshal(metadata)
		fmt.Fprintf(os.Stdout, "%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n",
			stringValue(obj["id"]), stringValue(obj["name"]), stringValue(obj["status"]), stringValue(obj["tier"]),
			fmt.Sprintf("%.0f", asFloat(obj["evaluation_device_quota"])), stringValue(metadata["brandname"]), stringValue(obj["created_at"]), string(metaJSON))
	}
	logBrandList("complete: listed brand clouds")
	return nil
}

func runCreateBrandnameCloud(args []string) error {
	fs := flag.NewFlagSet("create-brandname-cloud", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	brandname := fs.String("brandname", "", "brand name")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if strings.ContainsFunc(*brandname, func(r rune) bool { return r < 32 || r == 127 }) {
		return errors.New("--brandname must not contain control characters")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logBrandCreate("start staging brand cloud create: brandname=%s", *brandname)
	logBrandCreate("workspace=%s", mustWorkspace(*workspaceFlag))
	logBrandCreate("loading Account Manager staging env/state")
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	token, err := accountLogin(ctx, logBrandCreate)
	if err != nil {
		return err
	}
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		if obj["name"] == *brandname {
			logBrandCreate("brand cloud already exists: id=%s", stringValue(obj["id"]))
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "exists", "brand_cloud": obj})
		}
	}
	created, status, err := accountCreateBrandCloud(ctx, token, *brandname)
	if err != nil {
		return err
	}
	if status == 201 {
		logBrandCreate("brand cloud created via API")
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "created", "brand_cloud": created["brand_cloud"]}); err != nil {
			return err
		}
		logBrandCreate("complete: brand cloud created")
		return nil
	}
	if status != 500 {
		return fmt.Errorf("brand cloud create failed: HTTP %d", status)
	}
	logBrandCreate("API create returned HTTP 500; falling back to direct PostgreSQL upsert")
	fallback, err := accountPostgresFallback(ctx, *brandname)
	if err != nil {
		return err
	}
	var fallbackObj map[string]any
	if err := json.Unmarshal([]byte(fallback), &fallbackObj); err != nil {
		return err
	}
	verify, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return err
	}
	id := ""
	if bc, ok := fallbackObj["brand_cloud"].(map[string]any); ok {
		id = stringValue(bc["id"])
	}
	found := false
	for _, item := range anySlice(verify["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		if stringValue(obj["id"]) == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("post-create brand cloud verification did not find %s", id)
	}
	fmt.Fprintln(os.Stdout, strings.TrimSpace(fallback))
	logBrandCreate("complete: brand cloud created through fallback")
	return nil
}

func runCreateUsers(args []string) error {
	fs := flag.NewFlagSet("create-users", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	count := fs.Int("count", 10, "count")
	role := fs.String("role", "member", "role")
	rotatePassword := fs.Bool("rotate-password", false, "rotate password")
	dryRun := fs.Bool("dry-run", false, "dry run")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *count <= 0 {
		return errors.New("--count must be greater than zero")
	}
	if *role != "owner" && *role != "admin" && *role != "member" {
		return errors.New("--role must be owner, admin, or member")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logCreateUsers("workspace=%s", mustWorkspace(*workspaceFlag))
	logCreateUsers("env_root=%s", ctx.EnvRoot)
	logCreateUsers("loading Account Manager env/state")
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	token, err := accountLogin(ctx, logCreateUsers)
	if err != nil {
		return err
	}
	brandCloud, err := accountFindBrandCloud(ctx, token, *brandname)
	if err != nil {
		return err
	}
	brandCloudID := stringValue(brandCloud["id"])
	logCreateUsers("brand cloud found: id=%s", brandCloudID)
	slug := brandSlug(*brandname)
	planned := plannedUsers(*brandname, slug, *role, *count)
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brand_cloud": brandCloud, "role": *role, "users": planned})
	}
	users := []map[string]any{}
	created := 0
	assigned := 0
	for _, plan := range planned {
		email := plan["email"].(string)
		displayName := plan["display_name"].(string)
		password, err := randomPassword()
		if err != nil {
			return err
		}
		logCreateUsers("ensuring brand user: email=%s role=%s", email, *role)
		action, err := accountCreateUser(ctx, token, brandCloudID, email, displayName, password, *role, *rotatePassword)
		if err != nil {
			return err
		}
		if action == "created" {
			created++
		} else {
			if !*rotatePassword {
				return fmt.Errorf("brand user already exists and password was not rotated: email=%s; use the previous credentials artifact or rerun with --rotate-password", email)
			}
			assigned++
		}
		users = append(users, map[string]any{"email": email, "display_name": displayName, "role": *role, "password": password, "action": action})
	}
	artifactDir := filepath.Join(ctx.EnvRoot, "artifacts", "users")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	credentialsFile := filepath.Join(artifactDir, fmt.Sprintf("%s-users-%s.json", slug, time.Now().UTC().Format("20060102T150405Z")))
	if err := writeJSON(credentialsFile, map[string]any{"brandname": *brandname, "brand_cloud_id": brandCloudID, "role": *role, "users": users}); err != nil {
		return err
	}
	_ = os.Chmod(credentialsFile, 0o600)
	logCreateUsers("credentials written: %s", credentialsFile)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action":           "created",
		"brandname":        *brandname,
		"brand_cloud_id":   brandCloudID,
		"role":             *role,
		"count":            *count,
		"created":          created,
		"assigned":         assigned,
		"credentials_file": credentialsFile,
	})
}

type e2eStep struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	DurationSeconds int64  `json:"duration_seconds"`
	LogFile         string `json:"log_file"`
}

func runStagingE2ETest(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	runMode := fs.Bool("run", false, "run")
	planMode := fs.Bool("plan", false, "plan")
	confirm := fs.String("confirm", "", "confirm")
	skipRemove := fs.Bool("skip-remove", false, "skip remove")
	brandname := fs.String("brandname", "RTK", "brand name")
	userCount := fs.Int("user-count", 10, "user count")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	videoRelease := fs.String("video-release", os.Getenv("VIDEO_RELEASE"), "video release")
	accountRelease := fs.String("account-release", os.Getenv("ACCOUNT_RELEASE"), "account release")
	adminRelease := fs.String("admin-release", os.Getenv("ADMIN_RELEASE"), "admin release")
	outDir := fs.String("out-dir", "", "out dir")
	skipMQTTProbe := fs.Bool("skip-mqtt-probe", false, "skip mqtt probe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *userCount <= 0 {
		return errors.New("--user-count must be a positive integer")
	}
	if *deviceCount <= 0 {
		return errors.New("--device-count must be a positive integer")
	}
	_ = planMode
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stackName := envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME")
	if stackName == "" {
		stackName = "video-cloud-staging"
	}
	scripts := map[string]string{
		"remove":           firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_REMOVE_SCRIPT"), selfCommandPath("remove-all-vm")),
		"provision":        firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_PROVISION_SCRIPT"), selfCommandPath("provision")),
		"create-brand":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT"), selfCommandPath("create-brandname-cloud")),
		"create-users":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT"), selfCommandPath("create-users")),
		"generate-devices": firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT"), selfCommandPath("generate-load-devices")),
		"bind-devices":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT"), selfCommandPath("bind-devices")),
		"validate-bind":    firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT"), selfCommandPath("validate-device-bind")),
		"mqtt-test":        firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT"), selfCommandPath("mqtt-test")),
	}
	if !*runMode {
		printE2EPlan(workspace, envRoot, stackName, *brandname, *userCount, *deviceCount, *deviceMix, *skipRemove, scripts)
		return nil
	}
	if *confirm != stackName {
		return fmt.Errorf("--confirm %s does not match CLOUD_STACK_NAME=%s", *confirm, stackName)
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "staging-e2e", time.Now().UTC().Format("20060102T150405Z"))
	}
	logsDir := filepath.Join(*outDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	steps := []e2eStep{}
	runStep := func(name string, argv ...string) error {
		step, err := runE2EStep(name, filepath.Join(logsDir, name+".log"), argv...)
		steps = append(steps, step)
		return err
	}
	if !*skipRemove {
		if err := runStep("remove_vm", append(commandWithArgs(scripts["remove"], "--workspace", workspace, "--env-root", envRoot), "--yes")...); err != nil {
			return err
		}
	}
	provisionArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--reset-and-all", "--confirm", stackName}
	if *videoRelease != "" {
		provisionArgs = append(provisionArgs, "--video-release", *videoRelease)
	}
	if *accountRelease != "" {
		provisionArgs = append(provisionArgs, "--account-release", *accountRelease)
	}
	if *adminRelease != "" {
		provisionArgs = append(provisionArgs, "--admin-release", *adminRelease)
	}
	if err := runStep("provision_all", commandWithArgs(scripts["provision"], provisionArgs...)...); err != nil {
		return err
	}
	if err := runStep("create_brand", commandWithArgs(scripts["create-brand"], "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname)...); err != nil {
		return err
	}
	if err := runStep("create_users", commandWithArgs(scripts["create-users"], "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*userCount))...); err != nil {
		return err
	}
	if err := runStep("create_devices", commandWithArgs(scripts["generate-devices"], "--workspace", workspace, "--env-root", envRoot, "--count", strconv.Itoa(*deviceCount), "--mix", *deviceMix, "--prefix", *devicePrefix, "--force")...); err != nil {
		return err
	}
	slug := brandSlug(*brandname)
	usersFile := latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
	if usersFile == "" {
		return fmt.Errorf("no users artifact found for brand slug %s", slug)
	}
	devicesDir := filepath.Join(envRoot, "devices", "test_device")
	if err := runStep("bind_devices", commandWithArgs(scripts["bind-devices"], "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--users-file", usersFile, "--devices-dir", devicesDir, "--count", strconv.Itoa(*deviceCount))...); err != nil {
		return err
	}
	bindFile := latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
	if bindFile == "" {
		return fmt.Errorf("no device-bind artifact found for brand slug %s", slug)
	}
	expectedPerUser := (*deviceCount + *userCount - 1) / *userCount
	if err := runStep("validate_bind", commandWithArgs(scripts["validate-bind"], "--bind-artifact", bindFile, "--out-dir", filepath.Join(*outDir, "bind-validation"), "--expected-count", strconv.Itoa(*deviceCount), "--expected-devices-per-user", strconv.Itoa(expectedPerUser))...); err != nil {
		return err
	}
	mqttArgs := []string{"--env-root", envRoot, "--brandname", *brandname, "--profile", "smoke", "--out-dir", filepath.Join(*outDir, "home-mqtt")}
	if *skipMQTTProbe {
		mqttArgs = append(mqttArgs, "--no-mqtt-probe")
	} else {
		mqttArgs = append(mqttArgs, "--mqtt-probe")
	}
	if err := runStep("cloud_mqtt_test", commandWithArgs(scripts["mqtt-test"], mqttArgs...)...); err != nil {
		return err
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	reportFile := filepath.Join(*outDir, "TEST_REPORT.md")
	summary := map[string]any{
		"overall":      overall,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"env_root":     envRoot,
		"stack":        stackName,
		"brandname":    *brandname,
		"artifacts":    map[string]any{"users_file": usersFile, "device_bind_file": bindFile, "report_file": reportFile},
		"steps":        steps,
	}
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderE2EReport(overall, envRoot, stackName, *brandname, usersFile, bindFile, filepath.Join(*outDir, "home-mqtt"), steps)), 0o644); err != nil {
		return err
	}
	if containsSensitiveReportTerms(readText(summaryFile)) || containsSensitiveReportTerms(readText(reportFile)) {
		return errors.New("sanitized report contains sensitive terms")
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": overall, "summary_file": summaryFile, "report_file": reportFile}); err != nil {
		return err
	}
	if overall != "pass" {
		return exitCode(1)
	}
	return nil
}

func printE2EPlan(workspace, envRoot, stack, brandname string, userCount, deviceCount int, deviceMix string, skipRemove bool, scripts map[string]string) {
	fmt.Fprintln(os.Stdout, "cloud-staging-e2e-test plan")
	fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
	fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
	fmt.Fprintf(os.Stdout, "stack: %s\n", stack)
	fmt.Fprintf(os.Stdout, "brandname: %s\n", brandname)
	fmt.Fprintf(os.Stdout, "user_count: %d\n", userCount)
	fmt.Fprintf(os.Stdout, "device_count: %d\n", deviceCount)
	fmt.Fprintf(os.Stdout, "device_mix: %s\n", deviceMix)
	fmt.Fprintf(os.Stdout, "skip_remove: %v\n", skipRemove)
	fmt.Fprintln(os.Stdout, "steps:")
	if !skipRemove {
		fmt.Fprintf(os.Stdout, "  - remove VMs with %s\n", scripts["remove"])
	}
	fmt.Fprintf(os.Stdout, "  - provision all with %s\n", scripts["provision"])
	fmt.Fprintf(os.Stdout, "  - create brand cloud with %s\n", scripts["create-brand"])
	fmt.Fprintf(os.Stdout, "  - create users with %s\n", scripts["create-users"])
	fmt.Fprintf(os.Stdout, "  - generate/factory-enroll devices with %s\n", scripts["generate-devices"])
	fmt.Fprintf(os.Stdout, "  - bind/provision devices with %s\n", scripts["bind-devices"])
	fmt.Fprintf(os.Stdout, "  - validate bind artifact with %s\n", scripts["validate-bind"])
	fmt.Fprintf(os.Stdout, "  - run live home MQTT E2E with %s\n", scripts["mqtt-test"])
}

func runE2EStep(name, logPath string, argv ...string) (e2eStep, error) {
	start := time.Now()
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] start: %s\n", name)
	if len(argv) == 0 {
		return e2eStep{Name: name, Status: "FAIL", ExitCode: 1, LogFile: logPath}, errors.New("empty e2e command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	logFile, err := os.Create(logPath)
	if err != nil {
		return e2eStep{}, err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	rc := 0
	err = cmd.Run()
	if err != nil {
		rc = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			rc = exitErr.ExitCode()
		}
	}
	status := "PASS"
	if rc != 0 {
		status = "FAIL"
	}
	step := e2eStep{Name: name, Status: status, ExitCode: rc, DurationSeconds: int64(time.Since(start).Seconds()), LogFile: logPath}
	if rc != 0 {
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] fail: %s (see %s)\n", name, logPath)
		return step, err
	}
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] pass: %s\n", name)
	return step, nil
}

func commandWithArgs(command string, args ...string) []string {
	out := strings.Split(command, "\x00")
	return append(out, args...)
}

func selfCommandPath(command string) string {
	exe, err := os.Executable()
	if err != nil {
		return "rtk-cloud"
	}
	return exe + "\x00" + command
}

func latestMatchingFile(dir, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func renderE2EReport(overall, envRoot, stack, brandname, usersFile, bindFile, mqttDir string, steps []e2eStep) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Staging E2E Test Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Overall: %s\n", overall)
	fmt.Fprintf(&b, "- Generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Env root: `%s`\n", envRoot)
	fmt.Fprintf(&b, "- Stack: `%s`\n", stack)
	fmt.Fprintf(&b, "- Brand: `%s`\n\n", brandname)
	fmt.Fprintln(&b, "## Steps")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Step | Status | Duration seconds | Log |")
	fmt.Fprintln(&b, "| --- | --- | ---: | --- |")
	for _, step := range steps {
		fmt.Fprintf(&b, "| %s | %s | %d | `%s` |\n", step.Name, step.Status, step.DurationSeconds, step.LogFile)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Artifacts")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Users artifact: `%s`\n", usersFile)
	fmt.Fprintf(&b, "- Device bind artifact: `%s`\n", bindFile)
	fmt.Fprintf(&b, "- Home MQTT report: `%s`\n", filepath.Join(mqttDir, "TEST_REPORT.md"))
	fmt.Fprintf(&b, "- Home MQTT results: `%s`\n", filepath.Join(mqttDir, "results.json"))
	return b.String()
}

func containsSensitiveReportTerms(text string) bool {
	re := regexp.MustCompile(`(?i)password|bearer|raw-token|-----BEGIN|PRIVATE KEY|JWT_ACCESS_SECRET|VIDEO_CLOUD_AUTH_SECRET`)
	return re.MatchString(text)
}

func runCIRunnersList(args []string) error {
	fs := flag.NewFlagSet("ci-runners list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seen[spec.Repo] {
			continue
		}
		seen[spec.Repo] = true
		fmt.Fprintf(os.Stdout, "== %s ==\n", spec.Repo)
		out, err := ghAPI("repos/" + spec.Repo + "/actions/runners")
		if err != nil {
			return err
		}
		var parsed struct {
			Runners []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Busy   bool   `json:"busy"`
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"runners"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return err
		}
		for _, r := range parsed.Runners {
			labels := []string{}
			for _, label := range r.Labels {
				labels = append(labels, label.Name)
			}
			row := map[string]any{"name": r.Name, "status": r.Status, "busy": r.Busy, "labels": labels}
			data, _ := json.Marshal(row)
			fmt.Fprintln(os.Stdout, string(data))
		}
		fmt.Fprintln(os.Stdout)
	}
	return nil
}

func runCIRunnersPower(args []string) error {
	fs := flag.NewFlagSet("ci-runners power", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rtk-cloud ci-runners power start|stop|status")
	}
	action := fs.Arg(0)
	if action != "start" && action != "stop" && action != "status" {
		return errors.New("usage: rtk-cloud ci-runners power start|stop|status")
	}
	if os.Getenv("LINODE_TOKEN") == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	type linodeVM struct {
		ID     int      `json:"id"`
		Label  string   `json:"label"`
		Status string   `json:"status"`
		IPv4   []string `json:"ipv4"`
	}
	vms, err := linodeGetList[linodeVM](os.Getenv("LINODE_TOKEN"), "/linode/instances?page_size=500")
	if err != nil {
		return err
	}
	byLabel := map[string]linodeVM{}
	for _, vm := range vms {
		byLabel[vm.Label] = vm
	}
	seenHosts := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seenHosts[spec.HostLabel] {
			continue
		}
		seenHosts[spec.HostLabel] = true
		vm, ok := byLabel[spec.HostLabel]
		if !ok {
			fmt.Fprintf(os.Stdout, "%s\t%s\tmissing\n", spec.HostLabel, spec.Repo)
			continue
		}
		ipv4 := ""
		if len(vm.IPv4) > 0 {
			ipv4 = vm.IPv4[0]
		}
		switch action {
		case "start":
			if vm.Status == "running" {
				fmt.Fprintf(os.Stdout, "%s\talready-running\t%s\n", spec.HostLabel, ipv4)
			} else {
				if _, err := curlLinode("POST", fmt.Sprintf("/linode/instances/%d/boot", vm.ID), "{}"); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "%s\tboot-requested\t%s\n", spec.HostLabel, ipv4)
			}
		case "stop":
			if vm.Status == "offline" {
				fmt.Fprintf(os.Stdout, "%s\talready-offline\t%s\n", spec.HostLabel, ipv4)
			} else {
				if _, err := curlLinode("POST", fmt.Sprintf("/linode/instances/%d/shutdown", vm.ID), "{}"); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "%s\tshutdown-requested\t%s\n", spec.HostLabel, ipv4)
			}
		case "status":
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", spec.HostLabel, vm.Status, ipv4)
		}
	}
	return nil
}

func runCIRunnersWaitOnline(args []string) error {
	fs := flag.NewFlagSet("ci-runners wait-online", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	timeout := time.Duration(envInt("CI_RUNNER_ONLINE_TIMEOUT_SECONDS", 900)) * time.Second
	sleep := time.Duration(envInt("CI_RUNNER_ONLINE_POLL_SECONDS", 15)) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		missing := 0
		for _, spec := range runner.Specs() {
			status, _ := githubRunnerStatus(spec.Repo, spec.RunnerName)
			if status == "online" {
				fmt.Fprintf(os.Stdout, "online: %s (%s)\n", spec.RunnerName, spec.CustomLabel)
			} else {
				if status == "" {
					status = "missing"
				}
				fmt.Fprintf(os.Stdout, "waiting: %s (%s), current=%s\n", spec.RunnerName, spec.CustomLabel, status)
				missing++
			}
		}
		if missing == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for CI runners to become online")
		}
		time.Sleep(sleep)
	}
}

func githubRunnerStatus(repo, name string) (string, error) {
	out, err := ghAPI("repos/" + repo + "/actions/runners")
	if err != nil {
		return "", err
	}
	var parsed struct {
		Runners []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"runners"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", err
	}
	for _, r := range parsed.Runners {
		if r.Name == name {
			return r.Status, nil
		}
	}
	return "", nil
}

func ghAPI(path string) ([]byte, error) {
	cmd := exec.Command("gh", "api", path)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func accountFindBrandCloud(ctx accountManagerContext, token, brandname string) (map[string]any, error) {
	logCreateUsers("checking brand cloud: name=%s", brandname)
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		if obj["name"] == brandname || metadata["brandname"] == brandname {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("brand cloud not found: %s", brandname)
}

func accountCreateUser(ctx accountManagerContext, token, brandCloudID, email, displayName, password, role string, rotate bool) (string, error) {
	payload, _ := json.Marshal(map[string]any{"email": email, "password": password, "display_name": displayName, "role": role, "rotate_password": rotate})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds/%s/users", ctx.BaseURL, brandCloudID), token, payload)
	if err != nil {
		return "", err
	}
	if status != 200 && status != 201 {
		return "", fmt.Errorf("brand user create failed: email=%s HTTP %d", email, status)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	action := stringValue(parsed["action"])
	if action == "" {
		action = "assigned"
	}
	return action, nil
}

func plannedUsers(brandname, slug, role string, count int) []map[string]any {
	users := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		suffix := fmt.Sprintf("%03d", i)
		users = append(users, map[string]any{
			"email":        fmt.Sprintf("%s+%s@users.local", slug, suffix),
			"display_name": fmt.Sprintf("%s User %s", brandname, suffix),
			"role":         role,
		})
	}
	return users
}

func brandSlug(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "brand"
	}
	return slug
}

func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func logCreateUsers(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-create-users %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func accountManagerContextFromFlags(workspaceFlag, envRootFlag string) (accountManagerContext, error) {
	workspace := workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return accountManagerContext{}, err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, envRootFlag)
	if err != nil {
		return accountManagerContext{}, err
	}
	accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	accountState := filepath.Join(envRoot, "state", "account-manager-staging.env")
	platformEnv := filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env")
	domain := firstNonEmpty(envFileValue(accountEnv, "ACCOUNT_MANAGER_LINODE_DOMAIN"), "account-manager.video-cloud-staging.realtekconnect.com")
	return accountManagerContext{
		EnvRoot:          envRoot,
		BaseURL:          "https://" + domain,
		AdminEmail:       envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"),
		AdminPassword:    envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"),
		Host:             firstNonEmpty(envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_HOST"), envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4")),
		SSHUser:          firstNonEmpty(envFileValue(accountEnv, "ACCOUNT_MANAGER_LINODE_SSH_USER"), "root"),
		SSHKey:           firstNonEmpty(envFileValue(accountEnv, "ACCOUNT_MANAGER_LINODE_SSH_KEY"), filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")),
		PlatformAdminEnv: platformEnv,
	}, nil
}

func accountLogin(ctx accountManagerContext, logf func(string, ...any)) (string, error) {
	if ctx.AdminEmail == "" || ctx.AdminPassword == "" {
		return "", errors.New("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL and PASSWORD are required")
	}
	logf("logging in platform admin: %s/v1/auth/login", ctx.BaseURL)
	payload, _ := json.Marshal(map[string]string{"email": ctx.AdminEmail, "password": ctx.AdminPassword})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/login", "", payload)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("platform admin login failed: HTTP %d", status)
	}
	var parsed struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.Tokens.AccessToken == "" {
		return "", errors.New("platform admin login response did not include an access token")
	}
	logf("platform admin login ok")
	return parsed.Tokens.AccessToken, nil
}

func accountListBrandClouds(ctx accountManagerContext, token string, limit int) (map[string]any, error) {
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds?limit=%d", ctx.BaseURL, limit), token, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("brand cloud list failed: HTTP %d", status)
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func accountCreateBrandCloud(ctx accountManagerContext, token, brandname string) (map[string]any, int, error) {
	payload, _ := json.Marshal(map[string]any{"name": brandname, "metadata": map[string]string{"brandname": brandname}})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/admin/brand-clouds", token, payload)
	if err != nil {
		return nil, status, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	return parsed, status, nil
}

func curlJSONStatus(url, bearer string, payload []byte) ([]byte, int, error) {
	tmp, err := os.CreateTemp("", "rtk-curl-json-*")
	if err != nil {
		return nil, 0, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	args := []string{"-sS", "-o", tmp.Name(), "-w", "%{http_code}"}
	var payloadPath string
	if payload != nil {
		payloadFile, err := os.CreateTemp("", "rtk-curl-payload-*")
		if err != nil {
			return nil, 0, err
		}
		payloadPath = payloadFile.Name()
		if _, err := payloadFile.Write(payload); err != nil {
			payloadFile.Close()
			os.Remove(payloadPath)
			return nil, 0, err
		}
		payloadFile.Close()
		defer os.Remove(payloadPath)
		args = append(args, "-H", "content-type: application/json")
		if bearer != "" {
			args = append(args, "-H", "authorization: Bearer "+bearer)
		}
		args = append(args, "--data-binary", "@"+payloadPath)
	} else if bearer != "" {
		args = append(args, "-H", "authorization: Bearer "+bearer)
	}
	args = append(args, url)
	cmd := exec.Command("curl", args...)
	statusBytes, err := cmd.Output()
	if err != nil {
		return nil, 0, err
	}
	body, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, 0, err
	}
	status, _ := strconv.Atoi(strings.TrimSpace(string(statusBytes)))
	return body, status, nil
}

func accountBootstrap(ctx accountManagerContext) error {
	if ctx.Host == "" {
		return errors.New("ACCOUNT_MANAGER_LINODE_HOST or ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4 is required")
	}
	if ctx.SSHKey == "" {
		return errors.New("ACCOUNT_MANAGER_LINODE_SSH_KEY is required")
	}
	logBrandCreate("updating platform-admin bootstrap env on account-manager host=%s", ctx.Host)
	remote := `set -euo pipefail
env_file=/etc/rtk-account-manager/account-manager.env
test -f "$env_file"
cp -p "$env_file" "$env_file.bootstrap-admin.bak.$(date -u +%Y%m%dT%H%M%SZ)"
tmp="$(mktemp)"
grep -vE "^ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_(EMAIL|PASSWORD)=" "$env_file" > "$tmp"
cat >> "$tmp"
install -m 0600 -o root -g root "$tmp" "$env_file"
rm -f "$tmp"
systemctl restart rtk-account-manager.service
echo "bootstrap admin env applied and account-manager is healthy" >&2
`
	cmd := exec.Command("ssh", "-i", ctx.SSHKey, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", ctx.SSHUser+"@"+ctx.Host, remote)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=%s\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=%s\n", ctx.AdminEmail, ctx.AdminPassword))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	logBrandCreate("platform-admin bootstrap env ready")
	return nil
}

func accountPostgresFallback(ctx accountManagerContext, brandname string) (string, error) {
	if ctx.Host == "" {
		return "", errors.New("ACCOUNT_MANAGER_LINODE_HOST or ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4 is required")
	}
	script := `set -euo pipefail
sudo -u postgres psql -d rtk_account_manager -v ON_ERROR_STOP=1 <<'SQL'
SELECT 'placeholder';
SQL
`
	cmd := exec.Command("ssh", "-i", ctx.SSHKey, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", ctx.SSHUser+"@"+ctx.Host, "bash", "-s", "--", ctx.AdminEmail, brandname)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

func logBrandList(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-brand-cloud-list %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func logBrandCreate(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-brand-cloud %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func mustWorkspace(flagValue string) string {
	if flagValue != "" {
		abs, _ := filepath.Abs(flagValue)
		return abs
	}
	workspace, _ := workspaceRoot()
	return workspace
}

func anySlice(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

type firewallTarget struct {
	Role  string
	Label string
	ID    string
}

func firewallTargets(envRoot string) ([]firewallTarget, error) {
	targets := []firewallTarget{}
	statePath := filepath.Join(envRoot, "state", "video-cloud-staging.state.json")
	if data, err := os.ReadFile(statePath); err == nil {
		var parsed struct {
			Firewalls map[string]any `json:"firewalls"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, err
		}
		for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
			if id, ok := parsed.Firewalls[role]; ok {
				targets = append(targets, firewallTarget{Role: role, Label: "video-cloud-staging-" + role, ID: fmt.Sprintf("%.0f", asFloat(id))})
			}
		}
	}
	accountState := filepath.Join(envRoot, "state", "account-manager-staging.env")
	adminState := filepath.Join(envRoot, "state", "cloud-admin-staging.env")
	targets = append(targets, firewallTarget{
		Role:  "account-manager",
		Label: firstNonEmpty(envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL"), "rtk-account-manager-staging-fw"),
		ID:    envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_FIREWALL_ID"),
	})
	targets = append(targets, firewallTarget{
		Role:  "cloud-admin",
		Label: firstNonEmpty(envFileValue(adminState, "ADMIN_LINODE_FIREWALL_LABEL"), "rtk-cloud-admin-staging-firewall"),
		ID:    envFileValue(adminState, "ADMIN_LINODE_FIREWALL_ID"),
	})
	return targets, nil
}

func updateFirewallRules(target firewallTarget, mode, cidr string, dryRun bool) error {
	out, err := curlLinode("GET", fmt.Sprintf("/networking/firewalls/%s/rules", target.ID), "")
	if err != nil {
		return err
	}
	var rules firewallRules
	if err := json.Unmarshal(out, &rules); err != nil {
		return err
	}
	changed := false
	for i := range rules.Inbound {
		rule := &rules.Inbound[i]
		if !isSSHRule(*rule) {
			continue
		}
		if rule.Addresses == nil {
			rule.Addresses = map[string][]string{}
		}
		if mode == "replace" {
			if len(rule.Addresses["ipv4"]) != 1 || rule.Addresses["ipv4"][0] != cidr {
				rule.Addresses["ipv4"] = []string{cidr}
				changed = true
			}
		} else if !contains(rule.Addresses["ipv4"], cidr) {
			rule.Addresses["ipv4"] = append(rule.Addresses["ipv4"], cidr)
			sort.Strings(rule.Addresses["ipv4"])
			changed = true
		}
	}
	if !changed {
		if mode == "replace" {
			fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] already restricted: mode=replace role=%s firewall=%s id=%s cidr=%s\n", target.Role, target.Label, target.ID, cidr)
		} else {
			fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] already allowed: mode=append role=%s firewall=%s id=%s cidr=%s\n", target.Role, target.Label, target.ID, cidr)
		}
		return nil
	}
	rules.Version = nil
	rules.Fingerprint = nil
	payload, _ := json.Marshal(rules)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] dry-run %s: mode=%s role=%s firewall=%s id=%s cidr=%s\n", mode, mode, target.Role, target.Label, target.ID, cidr)
		return nil
	}
	if _, err := curlLinode("PUT", fmt.Sprintf("/networking/firewalls/%s/rules", target.ID), string(payload)); err != nil {
		return err
	}
	if mode == "replace" {
		fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] replaced: mode=replace role=%s firewall=%s id=%s cidr=%s\n", target.Role, target.Label, target.ID, cidr)
	} else {
		fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] appended: mode=append role=%s firewall=%s id=%s cidr=%s\n", target.Role, target.Label, target.ID, cidr)
	}
	return nil
}

func curlLinode(method, path, data string) ([]byte, error) {
	args := []string{"-fsS", "-X", method, "https://api.linode.com/v4" + path, "-H", "Authorization: Bearer " + os.Getenv("LINODE_TOKEN"), "-H", "Content-Type: application/json"}
	if data != "" {
		args = append(args, "--data-binary", data)
	}
	cmd := exec.Command("curl", args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func isSSHRule(rule firewallRule) bool {
	return rule.Label == "ssh" || (strings.EqualFold(rule.Protocol, "TCP") && rule.Ports == "22")
}

func updateCSVEnv(path, key, cidr string, appendMode bool) {
	if path == "" || !exists(path) {
		return
	}
	lines := strings.Split(strings.TrimRight(readText(path), "\n"), "\n")
	updated := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			updated = true
			if appendMode {
				_, value, _ := strings.Cut(line, "=")
				parts := splitCSV(value)
				if !contains(parts, cidr) {
					parts = append(parts, cidr)
				}
				lines[i] = key + "=" + strings.Join(parts, ",")
			} else {
				lines[i] = key + "=" + cidr
			}
		}
	}
	if !updated {
		lines = append(lines, key+"="+cidr)
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func updateVideoCIDR(path, cidr string, appendMode bool) {
	if path == "" || !exists(path) {
		return
	}
	lines := strings.Split(strings.TrimRight(readText(path), "\n"), "\n")
	out := []string{}
	inAllowed := false
	inserted := false
	replaced := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "allowed_source_cidrs:" {
			inAllowed = true
			out = append(out, line)
			if !appendMode {
				out = append(out, "    - "+cidr)
				inserted = true
				replaced = true
			}
			continue
		}
		if inAllowed {
			if strings.HasPrefix(line, "    - ") {
				if appendMode {
					if strings.TrimSpace(strings.TrimPrefix(line, "-")) == cidr {
						inserted = true
					}
					out = append(out, line)
				}
				continue
			}
			if appendMode && !inserted {
				out = append(out, "    - "+cidr)
				inserted = true
			}
			inAllowed = false
		}
		out = append(out, line)
	}
	if inAllowed && appendMode && !inserted {
		out = append(out, "    - "+cidr)
	}
	if !appendMode && !replaced {
		out = append(out, "ssh:", "  allowed_source_cidrs:", "    - "+cidr)
	}
	_ = os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

func splitCSV(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func asFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
		return f
	}
}

type loadDeviceInput struct {
	Index          int
	Ordinal        int
	Type           loadDeviceType
	Prefix         string
	OutDir         string
	GenerateOnly   bool
	CAKey          *ecdsa.PrivateKey
	CACert         []byte
	DeviceDays     int
	FactoryURL     string
	FactoryAuthKey string
	FactoryID      string
	LineID         string
	StationID      string
	FixtureID      string
	OperatorID     string
	BatchID        string
	SerialPrefix   string
	RunID          string
	Timeout        time.Duration
	ResultsPath    string
}

func writeLoadDevice(in loadDeviceInput) (generatedDevice, bool, error) {
	deviceID := fmt.Sprintf("%s-%04d", in.Prefix, in.Index)
	display := loadDisplayName(in.Type.Name, in.Ordinal)
	deviceDir := filepath.Join(in.OutDir, "devices", in.Type.Name, deviceID)
	bundleDir := filepath.Join(in.OutDir, "bundles", in.Type.Name)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		return generatedDevice{}, false, err
	}
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return generatedDevice{}, false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return generatedDevice{}, false, err
	}
	keyPath := filepath.Join(deviceDir, "device.key.pem")
	if err := writeECPrivateKey(keyPath, key); err != nil {
		return generatedDevice{}, false, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{in.Type.Name}, CommonName: deviceID},
		DNSNames: []string{deviceID + ".simulated.realtek-connect.local"},
		URIs:     mustParseURIs("urn:realtek-connect:simulated-device:" + deviceID),
	}, key)
	if err != nil {
		return generatedDevice{}, false, err
	}
	csrPath := filepath.Join(deviceDir, "device.csr.pem")
	if err := writePEM(csrPath, "CERTIFICATE REQUEST", csrDER, 0o644); err != nil {
		return generatedDevice{}, false, err
	}
	certPath := filepath.Join(deviceDir, "device.cert.pem")
	chainPath := filepath.Join(deviceDir, "device.chain.pem")
	profile := "factory-enrolled-device-mtls-client"
	warning := "Factory-enrolled staging load-test credential. Keep private key material out of source control."
	if in.GenerateOnly {
		logLoad("generate-only: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
		certDER, err := signDeviceCert(deviceID, key, in.CAKey, in.CACert, in.DeviceDays)
		if err != nil {
			return generatedDevice{}, false, err
		}
		if err := writePEM(certPath, "CERTIFICATE", certDER, 0o644); err != nil {
			return generatedDevice{}, false, err
		}
		if err := os.WriteFile(chainPath, in.CACert, 0o644); err != nil {
			return generatedDevice{}, false, err
		}
		profile = "simulation-device-mtls-client"
		warning = "Simulation-only generated credential. Do not use as a production or customer device identity."
	} else {
		ok, err := factoryEnrollDevice(in, deviceID, display, csrPath, certPath, chainPath)
		if err != nil || !ok {
			return generatedDevice{}, ok, err
		}
	}
	bundlePath := filepath.Join(bundleDir, deviceID+".pem")
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return generatedDevice{}, false, err
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return generatedDevice{}, false, err
	}
	if err := os.WriteFile(bundlePath, append(certBytes, keyBytes...), 0o600); err != nil {
		return generatedDevice{}, false, err
	}
	device := generatedDevice{
		DeviceID:             deviceID,
		DeviceType:           in.Type.Name,
		MQTTCapability:       in.Type.Capability,
		ServiceOptions:       in.Type.ServiceOptions,
		Model:                in.Type.Model,
		DisplayName:          display,
		FirmwareVersion:      "0.0.0-loadtest",
		Capabilities:         in.Type.Capabilities,
		CertificateProfile:   profile,
		CertificatePath:      relSlash(in.OutDir, certPath),
		CertificateChainPath: relSlash(in.OutDir, chainPath),
		KeyPath:              relSlash(in.OutDir, keyPath),
		CSRPath:              relSlash(in.OutDir, csrPath),
		BundlePath:           relSlash(in.OutDir, bundlePath),
		Production:           false,
		Warning:              warning,
	}
	if err := writeJSON(filepath.Join(deviceDir, "metadata.json"), device); err != nil {
		return generatedDevice{}, false, err
	}
	return device, true, nil
}

func factoryEnrollDevice(in loadDeviceInput, deviceID, display, csrPath, certPath, chainPath string) (bool, error) {
	requestID := fmt.Sprintf("%s-%s", in.RunID, deviceID)
	serial := fmt.Sprintf("%s-%s-%04d", in.SerialPrefix, in.RunID, in.Index)
	deviceDir := filepath.Dir(csrPath)
	logLoad("enroll start: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
	csrPEM, err := os.ReadFile(csrPath)
	if err != nil {
		return false, err
	}
	body := map[string]any{
		"request_id":      requestID,
		"devid":           deviceID,
		"csr_pem":         string(csrPEM),
		"serial_number":   serial,
		"factory_id":      in.FactoryID,
		"line_id":         in.LineID,
		"station_id":      in.StationID,
		"fixture_id":      in.FixtureID,
		"operator_id":     in.OperatorID,
		"batch_id":        in.BatchID,
		"service_options": in.Type.ServiceOptions,
		"metadata": map[string]any{
			"source":          "cloud-generate-load-devices",
			"run_id":          in.RunID,
			"device_type":     in.Type.Name,
			"model":           in.Type.Model,
			"display_name":    display,
			"mqtt_capability": in.Type.Capability,
			"capabilities":    in.Type.Capabilities,
			"service_options": in.Type.ServiceOptions,
		},
	}
	bodyBytes, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return false, err
	}
	requestPath := filepath.Join(deviceDir, "factory-enroll-request.json")
	if err := os.WriteFile(requestPath, bodyBytes, 0o644); err != nil {
		return false, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature := signFactoryRequest(in.FactoryAuthKey, "POST", "/v1/factory/enroll", timestamp, requestID, bodyBytes)
	client := &http.Client{Timeout: in.Timeout}
	req, err := http.NewRequest(http.MethodPost, in.FactoryURL+"/v1/factory/enroll", bytes.NewReader(bodyBytes))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Video-Cloud-Request-ID", requestID)
	req.Header.Set("X-Video-Cloud-Timestamp", timestamp)
	req.Header.Set("X-Video-Cloud-Signature", signature)
	resp, err := client.Do(req)
	if err != nil {
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, "000", requestID, serial, err.Error())
		return false, nil
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errText := fmt.Sprintf("factory enrollment HTTP %d", resp.StatusCode)
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, serial, errText)
		logLoad("enroll failed: index=%03d device=%s type=%s status=%d error=%s", in.Index, deviceID, in.Type.Name, resp.StatusCode, errText)
		return false, nil
	}
	var parsed struct {
		CertificatePEM      string `json:"certificate_pem"`
		CertificateChainPEM string `json:"certificate_chain_pem"`
		SerialNumber        string `json:"serial_number"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return false, err
	}
	if parsed.CertificatePEM == "" || parsed.CertificateChainPEM == "" {
		errText := "factory enrollment response missing certificate_pem or certificate_chain_pem"
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, serial, errText)
		return false, nil
	}
	if err := os.WriteFile(certPath, []byte(parsed.CertificatePEM), 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(chainPath, []byte(parsed.CertificateChainPEM), 0o644); err != nil {
		return false, err
	}
	var redacted map[string]any
	_ = json.Unmarshal(respBytes, &redacted)
	delete(redacted, "certificate_pem")
	delete(redacted, "certificate_chain_pem")
	if err := writeJSON(filepath.Join(deviceDir, "factory-enroll-response.redacted.json"), redacted); err != nil {
		return false, err
	}
	if parsed.SerialNumber == "" {
		parsed.SerialNumber = serial
	}
	logLoad("enroll ok: index=%03d device=%s type=%s status=%d serial=%s", in.Index, deviceID, in.Type.Name, resp.StatusCode, parsed.SerialNumber)
	recordEnrollResult(in.ResultsPath, "ok", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, parsed.SerialNumber, "")
	return true, nil
}

func allocateDeviceMix(count int, raw string) (map[string]int, error) {
	weights := map[string]int{"camera": 0, "light": 0, "air_conditioner": 0, "smart_meter": 0}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --mix item: %s", item)
		}
		if _, exists := weights[name]; !exists {
			return nil, fmt.Errorf("unsupported device type in --mix: %s", name)
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid weight for %s: %s", name, value)
		}
		weights[name] = n
	}
	total := 0
	for _, value := range weights {
		total += value
	}
	if total == 0 {
		return nil, errors.New("--mix must include at least one positive weight")
	}
	alloc := map[string]int{}
	remainders := map[string]int{}
	allocated := 0
	for _, dt := range loadDeviceTypes {
		base := count * weights[dt.Name] / total
		rem := count * weights[dt.Name] % total
		alloc[dt.Name] = base
		remainders[dt.Name] = rem
		allocated += base
	}
	for leftover := count - allocated; leftover > 0; leftover-- {
		selected := ""
		best := -1
		for _, dt := range loadDeviceTypes {
			if weights[dt.Name] > 0 && remainders[dt.Name] > best {
				selected = dt.Name
				best = remainders[dt.Name]
			}
		}
		alloc[selected]++
		remainders[selected] = -1
	}
	return alloc, nil
}

func writeGeneratedCA(outDir string, days int) (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caDir := filepath.Join(outDir, "ca")
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := writeECPrivateKey(filepath.Join(caDir, "sim-device-ca.key.pem"), key); err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{"Load Test Device Factory"}, CommonName: "Realtek Connect Plus Simulation Device CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(days) * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          []byte{1, 2, 3, 4},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(caDir, "sim-device-ca.cert.pem"), pemBytes, 0o644); err != nil {
		return nil, nil, err
	}
	return key, pemBytes, nil
}

func signDeviceCert(deviceID string, key, caKey *ecdsa.PrivateKey, caPEM []byte, days int) ([]byte, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, errors.New("invalid CA certificate")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{"Load Test Device"}, CommonName: deviceID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{deviceID + ".simulated.realtek-connect.local"},
		URIs:         mustParseURIs("urn:realtek-connect:simulated-device:" + deviceID),
	}
	return x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
}

func signFactoryRequest(key, method, path, timestamp, requestID string, body []byte) string {
	hash := sha256.Sum256(body)
	canonical := strings.Join([]string{strings.ToUpper(strings.TrimSpace(method)), strings.TrimSpace(path), strings.TrimSpace(timestamp), strings.TrimSpace(requestID), hex.EncodeToString(hash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(canonical))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func recordEnrollResult(path, status string, index int, deviceID, deviceType string, serviceOptions []string, httpStatus, requestID, serial, errText string) {
	entry := map[string]any{"status": status, "index": index, "device_id": deviceID, "device_type": deviceType, "service_options": serviceOptions, "http_status": httpStatus, "request_id": requestID, "serial_number": serial, "error": errText}
	data, _ := json.Marshal(entry)
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fh.Write(append(data, '\n'))
}

func appendCSV(path string, device generatedDevice) {
	line := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s\n", device.DeviceID, device.DeviceType, device.MQTTCapability, strings.Join(device.ServiceOptions, ";"), device.Model, device.CertificatePath, device.KeyPath, device.BundlePath)
	appendLine(path, strings.TrimSuffix(line, "\n"))
}

func appendLine(path, line string) {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fmt.Fprintln(fh, line)
}

func writeLoadDeviceReadme(outDir string, count int, mix, mode, factoryURL string, caDays, deviceDays int) error {
	description := "factory enrollment"
	source := "issued by " + factoryURL + "/v1/factory/enroll"
	if mode == "generate_only" {
		description = "offline generate-only"
		source = "locally signed by the simulation CA"
	}
	content := fmt.Sprintf(`# Staging Load-Test Device Factory Output

This directory contains staging load-test device identities generated for factory/provisioning flow rehearsal.

- Device count: %d
- Requested mix: %s
- Mode: %s
- Device key type: EC P-256
- Device certificate profile: clientAuth
- Credential source: %s
- CA validity days: %d
- Device validity days: %d
`, count, mix, description, source, caDays, deviceDays)
	return os.WriteFile(filepath.Join(outDir, "README.md"), []byte(content), 0o644)
}

func loadDisplayName(deviceType string, ordinal int) string {
	switch deviceType {
	case "camera":
		return fmt.Sprintf("PRO2 Camera Simulator %03d", ordinal)
	case "light":
		return fmt.Sprintf("Light Simulator %03d", ordinal)
	case "air_conditioner":
		return fmt.Sprintf("Air Conditioner Simulator %03d", ordinal)
	case "smart_meter":
		return fmt.Sprintf("Smart Meter Simulator %03d", ordinal)
	default:
		return fmt.Sprintf("Device Simulator %03d", ordinal)
	}
}

func writeECPrivateKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "EC PRIVATE KEY", der, 0o600)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
}

func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func shellQuote(value string) string {
	return strings.ReplaceAll(value, `'`, `'\''`)
}

func factoryLogSuffix(mode, url string) string {
	if mode == "factory_enroll" {
		return " factory_url=" + url
	}
	return ""
}

func logLoad(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-load-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func mustParseURIs(values ...string) []*url.URL {
	out := make([]*url.URL, 0, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err == nil {
			out = append(out, parsed)
		}
	}
	return out
}

type checkState struct {
	failures int
}

func newCheck() *checkState {
	return &checkState{}
}

func (c *checkState) fail(message string) {
	fmt.Fprintln(os.Stderr, "FAIL: "+message)
	c.failures++
}

func (c *checkState) pass(message string) {
	fmt.Fprintln(os.Stdout, "OK: "+message)
}

func (c *checkState) requireFile(workspace, path string) {
	if info, err := os.Stat(filepath.Join(workspace, path)); err == nil && !info.IsDir() {
		c.pass("found " + path)
	} else {
		c.fail("missing " + path)
	}
}

func (c *checkState) requireDir(workspace, path string) {
	if info, err := os.Stat(filepath.Join(workspace, path)); err == nil && info.IsDir() {
		c.pass("found " + path)
	} else {
		c.fail("missing " + path)
	}
}

func checkGitGrepNoMatch(check *checkState, workspace, label, pattern string, paths []string) {
	checkGitGrepNoMatchFiltered(check, workspace, label, pattern, paths, nil)
}

func checkGitGrepNoMatchFiltered(check *checkState, workspace, label, pattern string, paths []string, allow func(string) bool) {
	args := append([]string{"-C", workspace, "grep", "-n", "-E", "-e", pattern, "--"}, paths...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err == nil {
		blocking := []string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			if allow != nil && allow(line) {
				continue
			}
			blocking = append(blocking, line)
		}
		if len(blocking) == 0 {
			check.pass("no " + label + " in tracked workspace files")
			return
		}
		fmt.Fprintf(os.Stderr, "Potential %s found:\n%s\n", label, strings.Join(blocking, "\n"))
		check.fail(label + " present in tracked workspace files")
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		check.pass("no " + label + " in tracked workspace files")
		return
	}
	check.fail(label + " scan failed")
}

func checkFileNoMatch(check *checkState, path, label, pattern string) {
	data, err := os.ReadFile(path)
	if err != nil {
		check.fail("missing " + path)
		return
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		check.fail("invalid pattern for " + label)
		return
	}
	if match := re.Find(data); len(match) > 0 {
		fmt.Fprintf(os.Stderr, "Potential %s found in %s\n", label, path)
		check.fail(label + " present in " + path)
		return
	}
	check.pass("no " + label + " in " + path)
}

func anyFileContains(workspace string, paths []string, needle string) bool {
	for _, path := range paths {
		if strings.Contains(readText(filepath.Join(workspace, path)), needle) {
			return true
		}
	}
	return false
}

func readText(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func submodulePaths(workspace string) ([]string, error) {
	out, err := gitOutput(workspace, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		path := fields[1]
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return stdout.String(), err
}

func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if entry.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			return copyFile(path, target)
		})
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func writeTSV(path string, rows [][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, row := range rows {
		for i, col := range row {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(strings.ReplaceAll(col, "\t", " "))
		}
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func checkCertTarget(target, domain, certPath string, minValidDays int) certCheckResult {
	result := certCheckResult{Target: target, Domain: domain, Source: certPath, Status: "pass", DaysLeft: "n/a"}
	data, err := os.ReadFile(certPath)
	if err != nil {
		result.Status = "fail"
		result.Detail = "missing certificate"
		return result
	}
	block, _ := pem.Decode(data)
	if block == nil {
		result.Status = "fail"
		result.Detail = "invalid PEM certificate"
		return result
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		result.Status = "fail"
		result.Detail = "invalid certificate"
		return result
	}
	result.ExpiresAt = cert.NotAfter.UTC().Format(time.RFC3339)
	result.Issuer = cert.Issuer.String()
	daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
	result.DaysLeft = daysLeft
	if err := cert.VerifyHostname(domain); err != nil {
		result.Status = "fail"
		result.Detail = "hostname mismatch"
		return result
	}
	if daysLeft < minValidDays {
		result.Status = "fail"
		result.Detail = fmt.Sprintf("expires within %d days", minValidDays)
		return result
	}
	result.Detail = "ok"
	return result
}

type bindArtifact struct {
	Brandname    string           `json:"brandname"`
	BrandCloudID string           `json:"brand_cloud_id"`
	Count        int              `json:"count"`
	Inputs       bindInputs       `json:"inputs"`
	Assignments  []bindAssignment `json:"assignments"`
}

type bindInputs struct {
	UsersFile  string `json:"users_file"`
	DevicesDir string `json:"devices_dir"`
}

type bindAssignment struct {
	AssignmentIndex int      `json:"assignment_index"`
	AssignedEmail   string   `json:"assigned_email"`
	DeviceID        string   `json:"device_id"`
	DeviceType      string   `json:"device_type"`
	Category        string   `json:"category"`
	ServiceOptions  []string `json:"service_options"`
	ClaimID         string   `json:"claim_id"`
	AccountDeviceID string   `json:"account_device_id"`
	OperationID     string   `json:"operation_id"`
	Status          string   `json:"status"`
}

func runValidateDeviceBind(args []string) error {
	fs := flag.NewFlagSet("validate-device-bind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bindPath := fs.String("bind-artifact", "", "bind artifact")
	outDir := fs.String("out-dir", "", "output directory")
	expectedCount := fs.Int("expected-count", 0, "expected count")
	expectedDevicesPerUser := fs.Int("expected-devices-per-user", 0, "expected devices per user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bindPath == "" {
		return errors.New("--bind-artifact is required")
	}
	if *outDir == "" {
		return errors.New("--out-dir is required")
	}
	data, err := os.ReadFile(*bindPath)
	if err != nil {
		return err
	}
	var artifact bindArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return err
	}
	result := map[string]any{
		"overall": "pass",
		"summary": map[string]any{
			"total_devices": len(artifact.Assignments),
		},
		"user_counts": map[string]int{},
		"failures":    []string{},
	}
	userCounts := result["user_counts"].(map[string]int)
	failures := []string{}
	if *expectedCount > 0 && len(artifact.Assignments) != *expectedCount {
		failures = append(failures, fmt.Sprintf("expected %d devices, got %d", *expectedCount, len(artifact.Assignments)))
	}
	mqttOnly := 0
	videoDevices := 0
	for _, assignment := range artifact.Assignments {
		userCounts[assignment.AssignedEmail]++
		hasMQTT := contains(assignment.ServiceOptions, "mqtt")
		hasVideo := contains(assignment.ServiceOptions, "video_streaming") || contains(assignment.ServiceOptions, "video_storage")
		if assignment.Category == "mqtt_device" {
			mqttOnly++
			if hasVideo {
				failures = append(failures, fmt.Sprintf("mqtt-only device %s has video service option", assignment.DeviceID))
			}
			if !hasMQTT {
				failures = append(failures, fmt.Sprintf("mqtt-only device %s is missing mqtt service option", assignment.DeviceID))
			}
		}
		if assignment.Category == "ip_camera" {
			videoDevices++
			if !hasVideo {
				failures = append(failures, fmt.Sprintf("camera device %s is missing video service option", assignment.DeviceID))
			}
		}
		if assignment.AccountDeviceID == "" || assignment.OperationID == "" || assignment.ClaimID == "" {
			failures = append(failures, fmt.Sprintf("device %s missing bind identifiers", assignment.DeviceID))
		}
	}
	if *expectedDevicesPerUser > 0 {
		for email, count := range userCounts {
			if count != *expectedDevicesPerUser {
				failures = append(failures, fmt.Sprintf("user %s expected %d devices, got %d", email, *expectedDevicesPerUser, count))
			}
		}
	}
	if len(failures) > 0 {
		result["overall"] = "fail"
	}
	result["failures"] = failures
	result["summary"].(map[string]any)["mqtt_only_devices"] = mqttOnly
	result["summary"].(map[string]any)["video_devices"] = videoDevices
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	resultsFile := filepath.Join(*outDir, "bulk-device-bind-validation-results.json")
	reportFile := filepath.Join(*outDir, "bulk-device-bind-validation-report.md")
	if err := writeJSON(resultsFile, result); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderBindReport(artifact, result)), 0o644); err != nil {
		return err
	}
	stdout := map[string]any{
		"action":        "validated",
		"overall":       result["overall"],
		"total_devices": len(artifact.Assignments),
		"results_file":  resultsFile,
		"report_file":   reportFile,
	}
	if err := json.NewEncoder(os.Stdout).Encode(stdout); err != nil {
		return err
	}
	if result["overall"] != "pass" {
		return exitCode(1)
	}
	return nil
}

func renderBindReport(artifact bindArtifact, result map[string]any) string {
	summary := result["summary"].(map[string]any)
	return fmt.Sprintf(`# Bulk Device Bind Validation Report

- brandname: %s
- brand_cloud_id: %s
- overall: %s
- total_devices: %v
- MQTT-only devices: %v
- Video-capable devices: %v
`, artifact.Brandname, artifact.BrandCloudID, result["overall"], summary["total_devices"], summary["mqtt_only_devices"], summary["video_devices"])
}

func runUnprovisionDevices(args []string) error {
	fs := flag.NewFlagSet("unprovision-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	bindPath := fs.String("bind-artifact", "", "bind artifact")
	count := fs.Int("count", 0, "count")
	dryRun := fs.Bool("dry-run", false, "dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	slug := brandSlug(*brandname)
	if *bindPath == "" {
		*bindPath = latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
		if *bindPath == "" {
			return fmt.Errorf("--bind-artifact was not provided and no bind artifact matched")
		}
	}
	bindAbs, _ := filepath.Abs(*bindPath)
	var artifact bindArtifact
	if data, err := os.ReadFile(bindAbs); err != nil {
		return err
	} else if err := json.Unmarshal(data, &artifact); err != nil {
		return err
	}
	if artifact.Brandname != *brandname {
		return fmt.Errorf("--bind-artifact brandname %s does not match --brandname %s", artifact.Brandname, *brandname)
	}
	if artifact.BrandCloudID == "" {
		return errors.New("--bind-artifact missing brand_cloud_id")
	}
	usersFile := artifact.Inputs.UsersFile
	if usersFile == "" {
		return errors.New("--bind-artifact missing inputs.users_file")
	}
	usersAbs, _ := filepath.Abs(usersFile)
	users, err := readUsersFile(usersAbs)
	if err != nil {
		return err
	}
	if *count == 0 {
		*count = len(artifact.Assignments)
	}
	if *count <= 0 || *count > len(artifact.Assignments) {
		return fmt.Errorf("--count %d exceeds bind assignment count %d", *count, len(artifact.Assignments))
	}
	plan := artifact.Assignments[:*count]
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "bind_artifact": bindAbs, "users_file": usersAbs, "assignments": plan})
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	ctx.BaseURL = "https://" + firstNonEmpty(envFileValue(firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env")), "ACCOUNT_MANAGER_LINODE_DOMAIN"), envFileValue(filepath.Join(envRoot, "env", "stack.env"), "ACCOUNT_MANAGER_DOMAIN"), "account-manager.video-cloud-staging.realtekconnect.com")
	logUnprovision("workspace=%s", workspace)
	logUnprovision("env_root=%s", envRoot)
	logUnprovision("bind_artifact=%s", bindAbs)
	if err := preflightUnprovision(ctx, artifact.BrandCloudID, plan[0], users); err != nil {
		return err
	}
	tokens := map[string]string{}
	for _, assignment := range plan {
		if tokens[assignment.AssignedEmail] != "" {
			continue
		}
		user := users[assignment.AssignedEmail]
		if user.Password == "" {
			return fmt.Errorf("users_file missing password for assigned user: %s", assignment.AssignedEmail)
		}
		logUnprovision("logging in assigned user: email=%s", assignment.AssignedEmail)
		token, err := loginAccountUser(ctx, assignment.AssignedEmail, user.Password)
		if err != nil {
			return err
		}
		tokens[assignment.AssignedEmail] = token
	}
	results := []map[string]any{}
	for _, assignment := range plan {
		logUnprovision("unprovisioning device: device=%s account_device=%s user=%s", assignment.DeviceID, assignment.AccountDeviceID, assignment.AssignedEmail)
		result, err := unprovisionOne(ctx, artifact.BrandCloudID, assignment, tokens[assignment.AssignedEmail])
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	artifactDir := filepath.Join(envRoot, "artifacts", "device-unprovision")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	outFile := filepath.Join(artifactDir, fmt.Sprintf("%s-device-unprovision-%s.json", slug, time.Now().UTC().Format("20060102T150405Z")))
	if err := writeJSON(outFile, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-unprovision/v1", "generated_at": time.Now().UTC().Format(time.RFC3339), "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "inputs": map[string]string{"bind_artifact": bindAbs, "users_file": usersAbs}, "assignments": results}); err != nil {
		return err
	}
	_ = os.Chmod(outFile, 0o600)
	logUnprovision("unprovision artifact written: %s", outFile)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "unprovisioned", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "unprovisioned": len(results), "artifact_file": outFile})
}

type bindDeviceManifest struct {
	DeviceID       string   `json:"device_id"`
	DeviceType     string   `json:"device_type"`
	DisplayName    string   `json:"display_name"`
	ServiceOptions []string `json:"service_options"`
}

func runBindDevices(args []string) error {
	fs := flag.NewFlagSet("bind-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	usersPath := fs.String("users-file", "", "users file")
	devicesDir := fs.String("devices-dir", "", "devices dir")
	count := fs.Int("count", 0, "count")
	claimTTL := fs.Int("claim-ttl-hours", 24, "claim TTL hours")
	dryRun := fs.Bool("dry-run", false, "dry run")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *claimTTL <= 0 {
		return errors.New("--claim-ttl-hours must be greater than zero")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	slug := brandSlug(*brandname)
	if *devicesDir == "" {
		*devicesDir = filepath.Join(envRoot, "devices", "test_device")
	}
	if *usersPath == "" {
		*usersPath = latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
		if *usersPath == "" {
			return fmt.Errorf("--users-file was not provided and no users artifact matched")
		}
	}
	usersAbs, _ := filepath.Abs(*usersPath)
	devicesAbs, _ := filepath.Abs(*devicesDir)
	users, usersList, err := readUsersList(usersAbs)
	if err != nil {
		return err
	}
	devices, err := readDeviceManifest(filepath.Join(devicesAbs, "manifests", "devices.json"))
	if err != nil {
		return err
	}
	if *count == 0 {
		*count = len(devices)
	}
	if *count <= 0 || *count > len(devices) {
		return fmt.Errorf("--count %d exceeds device manifest count %d", *count, len(devices))
	}
	assignments := buildBindAssignments(devices[:*count], usersList)
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brandname": *brandname, "count": *count, "users_file": usersAbs, "devices_dir": devicesAbs, "assignments": assignments})
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	token, err := accountLogin(ctx, logBind)
	if err != nil {
		return err
	}
	brandCloud, err := accountFindBrandCloudForLog(ctx, token, *brandname, logBind)
	if err != nil {
		return err
	}
	brandCloudID := stringValue(brandCloud["id"])
	userTokens := map[string]string{}
	for _, assignment := range assignments {
		if userTokens[assignment.AssignedEmail] != "" {
			continue
		}
		user := users[assignment.AssignedEmail]
		logBind("logging in assigned user: email=%s", assignment.AssignedEmail)
		userToken, err := loginAccountUser(ctx, assignment.AssignedEmail, user.Password)
		if err != nil {
			return err
		}
		userTokens[assignment.AssignedEmail] = userToken
	}
	runID := time.Now().UTC().Format("20060102T150405Z")
	results := []bindAssignment{}
	for _, assignment := range assignments {
		claim, err := createClaimToken(ctx, token, brandCloudID, assignment, runID, *claimTTL)
		if err != nil {
			return err
		}
		claimToken := stringValue(claim["claim_token"])
		assignment.ClaimID = stringValue(firstPresent(claim, "claim_id", "id"))
		resolve, err := resolveClaim(ctx, userTokens[assignment.AssignedEmail], brandCloudID, assignment, claimToken)
		if err != nil {
			return err
		}
		if dev, ok := resolve["device"].(map[string]any); ok {
			assignment.AccountDeviceID = stringValue(dev["id"])
		}
		prov, _ := resolve["provision_input"].(map[string]any)
		opID := fmt.Sprintf("bulk-bind-%s-%s", runID, assignment.DeviceID)
		if err := startProvision(ctx, userTokens[assignment.AssignedEmail], brandCloudID, assignment, opID, prov); err != nil {
			return err
		}
		assignment.OperationID = opID
		assignment.Status = "provision_requested"
		results = append(results, assignment)
	}
	artifactDir := filepath.Join(envRoot, "artifacts", "device-bind")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	artifactFile := filepath.Join(artifactDir, fmt.Sprintf("%s-device-bind-%s.json", slug, runID))
	if err := writeJSON(artifactFile, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-bind/v1", "generated_at": time.Now().UTC().Format(time.RFC3339), "brandname": *brandname, "brand_cloud_id": brandCloudID, "count": *count, "inputs": map[string]string{"users_file": usersAbs, "devices_dir": devicesAbs}, "assignments": results}); err != nil {
		return err
	}
	_ = os.Chmod(artifactFile, 0o600)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "bound", "brandname": *brandname, "brand_cloud_id": brandCloudID, "count": *count, "created_claims": len(results), "resolved_claims": len(results), "provision_started": len(results), "artifact_file": artifactFile})
}

func readUsersList(path string) (map[string]userCredential, []userCredential, error) {
	var parsed struct {
		Users []userCredential `json:"users"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil, err
	}
	byEmail := map[string]userCredential{}
	for _, user := range parsed.Users {
		byEmail[user.Email] = user
	}
	return byEmail, parsed.Users, nil
}

func readDeviceManifest(path string) ([]bindDeviceManifest, error) {
	var devices []bindDeviceManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return devices, json.Unmarshal(data, &devices)
}

func buildBindAssignments(devices []bindDeviceManifest, users []userCredential) []bindAssignment {
	out := make([]bindAssignment, len(devices))
	offset := 0
	for _, typ := range []string{"camera", "light", "air_conditioner", "smart_meter"} {
		indexes := []int{}
		for i, device := range devices {
			if device.DeviceType == typ {
				indexes = append(indexes, i)
			}
		}
		for j, deviceIndex := range indexes {
			device := devices[deviceIndex]
			userIndex := (offset + j) % len(users)
			category := "mqtt_device"
			if contains(device.ServiceOptions, "video_streaming") || contains(device.ServiceOptions, "video_storage") {
				category = "ip_camera"
			}
			out[deviceIndex] = bindAssignment{AssignmentIndex: deviceIndex, AssignedEmail: users[userIndex].Email, DeviceID: device.DeviceID, DeviceType: device.DeviceType, Category: category, ServiceOptions: device.ServiceOptions}
		}
		if len(indexes) > 0 {
			offset = (offset + len(indexes)) % len(users)
		}
	}
	return out
}

func accountFindBrandCloudForLog(ctx accountManagerContext, token, brandname string, logf func(string, ...any)) (map[string]any, error) {
	logf("checking brand cloud: name=%s", brandname)
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		if obj["name"] == brandname || metadata["brandname"] == brandname {
			logf("brand cloud found: id=%s", stringValue(obj["id"]))
			return obj, nil
		}
	}
	return nil, fmt.Errorf("brand cloud not found: %s", brandname)
}

func createClaimToken(ctx accountManagerContext, token, brandCloudID string, assignment bindAssignment, runID string, ttlHours int) (map[string]any, error) {
	expires := time.Now().UTC().Add(time.Duration(ttlHours) * time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"organization_id":   brandCloudID,
		"category":          assignment.Category,
		"video_cloud_devid": assignment.DeviceID,
		"activity_id":       "bulk-bind-" + runID + "-" + assignment.DeviceID,
		"clip_public_key":   "bulk-bind-placeholder-public-key",
		"expires_at":        expires,
		"service_options":   assignment.ServiceOptions,
		"metadata":          map[string]any{"source": "cloud-bind-devices", "device_type": assignment.DeviceType, "service_options": assignment.ServiceOptions},
	})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/admin/device-claim-tokens", token, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("claim token create failed: device=%s HTTP %d", assignment.DeviceID, status)
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func resolveClaim(ctx accountManagerContext, token, brandCloudID string, assignment bindAssignment, claimToken string) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]string{"claim_token": claimToken, "device_name": firstNonEmpty(assignment.DeviceID, assignment.DeviceID)})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/claim/resolve", ctx.BaseURL, brandCloudID), token, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("claim resolve failed: device=%s HTTP %d", assignment.DeviceID, status)
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func startProvision(ctx accountManagerContext, token, brandCloudID string, assignment bindAssignment, operationID string, provisionInput map[string]any) error {
	serviceOptions := assignment.ServiceOptions
	if items, ok := provisionInput["service_options"].([]any); ok {
		serviceOptions = []string{}
		for _, item := range items {
			serviceOptions = append(serviceOptions, stringValue(item))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"video_cloud_devid": stringValue(firstPresent(provisionInput, "video_cloud_devid")),
		"activity_id":       stringValue(firstPresent(provisionInput, "activity_id")),
		"clip_public_key":   stringValue(firstPresent(provisionInput, "clip_public_key")),
		"operation_id":      operationID,
		"service_options":   serviceOptions,
	})
	_, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/%s/provision", ctx.BaseURL, brandCloudID, assignment.AccountDeviceID), token, payload)
	if err != nil {
		return err
	}
	if status != 200 && status != 201 && status != 202 {
		return fmt.Errorf("provision start failed: device=%s account_device=%s HTTP %d", assignment.DeviceID, assignment.AccountDeviceID, status)
	}
	return nil
}

func firstPresent(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func logBind(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-bind-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

type userCredential struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func readUsersFile(path string) (map[string]userCredential, error) {
	var parsed struct {
		Users []userCredential `json:"users"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	out := map[string]userCredential{}
	for _, user := range parsed.Users {
		out[user.Email] = user
	}
	return out, nil
}

func preflightUnprovision(ctx accountManagerContext, brandCloudID string, assignment bindAssignment, users map[string]userCredential) error {
	user := users[assignment.AssignedEmail]
	logUnprovision("checking Account Manager unprovision API route: email=%s", assignment.AssignedEmail)
	token, err := loginAccountUser(ctx, assignment.AssignedEmail, user.Password)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"reason": "route_preflight"})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/00000000-0000-0000-0000-000000000000/unprovision", ctx.BaseURL, brandCloudID), token, payload)
	if err != nil {
		return err
	}
	if status == 404 {
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["error"] == "not_found" {
			logUnprovision("Account Manager unprovision API route is available")
			return nil
		}
		return fmt.Errorf("Account Manager unprovision API route is not deployed at %s; deploy an Account Manager build with /v1/orgs/:orgId/devices/:deviceId/unprovision before running this script", ctx.BaseURL)
	}
	if status == 400 || status == 409 {
		logUnprovision("Account Manager unprovision API route is available")
		return nil
	}
	if status == 403 {
		return fmt.Errorf("assigned user lacks device.unprovision permission in brand cloud: email=%s brand_cloud_id=%s", assignment.AssignedEmail, brandCloudID)
	}
	return fmt.Errorf("unexpected Account Manager unprovision API preflight status: HTTP %d", status)
}

func unprovisionOne(ctx accountManagerContext, brandCloudID string, assignment bindAssignment, token string) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]string{"reason": "user_resale_factory_ready"})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/%s/unprovision", ctx.BaseURL, brandCloudID, assignment.AccountDeviceID), token, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("unprovision failed: device=%s account_device=%s HTTP %d", assignment.DeviceID, assignment.AccountDeviceID, status)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	unprov, _ := parsed["unprovision"].(map[string]any)
	return map[string]any{
		"assignment_index":   assignment.AssignmentIndex,
		"assigned_email":     assignment.AssignedEmail,
		"device_id":          assignment.DeviceID,
		"device_type":        assignment.DeviceType,
		"category":           assignment.Category,
		"service_options":    assignment.ServiceOptions,
		"claim_id":           assignment.ClaimID,
		"account_device_id":  assignment.AccountDeviceID,
		"response_device_id": stringValue(unprov["device_id"]),
		"organization_id":    stringValue(unprov["organization_id"]),
		"video_cloud_devid":  stringValue(unprov["video_cloud_devid"]),
		"status":             "unprovisioned",
		"unprovisioned_at":   stringValue(unprov["unprovisioned_at"]),
	}, nil
}

func loginAccountUser(ctx accountManagerContext, email, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/login", "", payload)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("login failed: email=%s HTTP %d", email, status)
	}
	var parsed struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.Tokens.AccessToken == "" {
		return "", fmt.Errorf("login response did not include an access token: %s", email)
	}
	return parsed.Tokens.AccessToken, nil
}

func logUnprovision(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-unprovision-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func writeJSON(path string, value any) error {
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[len(paths)-1]
}

func envFileValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if ok && name == key {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func printUsage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "Usage: rtk-cloud <command> [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}
	fmt.Fprintln(os.Stderr, "  ci-runners <command>")
}

func printCIRunnerUsage() {
	names := make([]string, 0, len(ciRunnerCommands))
	for name := range ciRunnerCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "Usage: rtk-cloud ci-runners <command> [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}
}

func runLegacy(script string, args []string) error {
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "rtk-cloud-scripts-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := materializeLegacyScripts(tmp, workspace); err != nil {
		return err
	}
	scriptPath := filepath.Join(tmp, "scripts", script)
	cmd := exec.Command("/usr/bin/env", append([]string{"bash", scriptPath}, args...)...)
	cmd.Dir = workspace
	goCmd := os.Getenv("RTK_CLOUD_GO")
	if goCmd == "" {
		goCmd, _ = exec.LookPath("go")
		if goCmd == "" {
			goCmd = "go"
		}
	}
	cmd.Env = withEnv(os.Environ(), map[string]string{
		"RTK_CLOUD_WORKSPACE": workspace,
		"RTK_CLOUD_GO":        goCmd,
		"GOWORK":              "off",
	})
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

func materializeLegacyScripts(root, workspace string) error {
	for name, content := range legacy.Scripts {
		content = rewriteLegacyScript(content)
		path := filepath.Join(root, "scripts", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return err
		}
	}
	goLink := filepath.Join(root, "scripts", "go")
	target := filepath.Join(workspace, "scripts", "go")
	if err := os.Symlink(target, goLink); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func rewriteLegacyScript(content string) string {
	replacements := map[string]string{
		`WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"`:                           `WORKSPACE="${RTK_CLOUD_WORKSPACE:-$(cd "$SCRIPT_DIR/.." && pwd)}"`,
		`ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"`:      `ROOT_DIR="${RTK_CLOUD_WORKSPACE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"`,
		`"$ROOT_DIR/scripts/linode-ci-runners/power-ci-runners.sh"`:           `"$SCRIPT_DIR/power-ci-runners.sh"`,
		`"$ROOT_DIR/scripts/linode-ci-runners/wait-runners-online.sh"`:        `"$SCRIPT_DIR/wait-runners-online.sh"`,
		`"$ROOT_DIR/scripts/linode-ci-runners/archive-ci-artifacts.sh"`:       `"$SCRIPT_DIR/archive-ci-artifacts.sh"`,
		`source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"`:        `source "$SCRIPT_DIR/runner-specs.sh"`,
		`source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"` + "\n": `source "$SCRIPT_DIR/runner-specs.sh"` + "\n",
		`GODADDY_ENV="$GODADDY_ENVIRONMENT" go run ./cmd/godaddy-dns`:         `GODADDY_ENV="$GODADDY_ENVIRONMENT" "$RTK_CLOUD_GO" run ./cmd/godaddy-dns`,
		`go run ./cmd/linode-deploy apply --config "$VC_CONFIG"`:              `"$RTK_CLOUD_GO" run ./cmd/linode-deploy apply --config "$VC_CONFIG"`,
	}
	for old, newValue := range replacements {
		content = strings.ReplaceAll(content, old, newValue)
	}
	return content
}

func workspaceRoot() (string, error) {
	if v := os.Getenv("RTK_CLOUD_WORKSPACE"); v != "" {
		return filepath.Abs(v)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, ".git")) && exists(filepath.Join(dir, "scripts")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("could not locate workspace root")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveEnvRoot(workspace, envRoot string) (string, error) {
	if envRoot == "" {
		return "", errors.New("--env-root is required")
	}
	if !filepath.IsAbs(envRoot) {
		envRoot = filepath.Join(workspace, envRoot)
	}
	envRoot = filepath.Clean(envRoot)
	if filepath.Base(envRoot) == "staging" {
		return filepath.Join(envRoot, "linode"), nil
	}
	if info, err := os.Stat(filepath.Join(envRoot, "linode")); err == nil && info.IsDir() {
		if _, servicesErr := os.Stat(filepath.Join(envRoot, "services")); os.IsNotExist(servicesErr) {
			if _, envErr := os.Stat(filepath.Join(envRoot, "env")); os.IsNotExist(envErr) {
				return filepath.Join(envRoot, "linode"), nil
			}
		}
	}
	return envRoot, nil
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func withEnv(base []string, overrides map[string]string) []string {
	values := map[string]string{}
	order := []string{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for key, value := range overrides {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

func init() {
	for name := range legacy.Scripts {
		if strings.Contains(name, string(filepath.Separator)) {
			continue
		}
	}
}
