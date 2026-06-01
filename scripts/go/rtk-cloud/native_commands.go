package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/runner"
)

func runDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated environment root")
	sshKey := fs.String("ssh-key", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud"), "SSH key")
	dnsRoot := fs.String("dns-root-domain", "", "DNS root domain override")
	videoRelease := fs.String("video-release", "", "Video Cloud release")
	accountRelease := fs.String("account-release", "", "Account Manager release")
	accountBundle := fs.String("account-release-bundle", os.Getenv("ACCOUNT_RELEASE_BUNDLE"), "Account Manager release bundle")
	adminRelease := fs.String("admin-release", "", "Cloud Admin release")
	adminBundle := fs.String("admin-release-bundle", os.Getenv("ADMIN_RELEASE_BUNDLE"), "Cloud Admin release bundle")
	_ = sshKey
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
	paths := newProvisionPaths(workspace, envRoot, provisionOptions{})
	env, err := envroot.Load(envRoot, *dnsRoot)
	if err != nil {
		return err
	}
	operator, _ := readEnvFile(paths.OperatorEnv)
	return deployAllServices(paths, env.Values, operator, provisionOptions{
		videoRelease:         *videoRelease,
		accountRelease:       *accountRelease,
		adminRelease:         *adminRelease,
		accountReleaseBundle: *accountBundle,
		adminReleaseBundle:   *adminBundle,
	})
}

func runCollectEvidence(args []string) error {
	return runLogsCheck(args)
}

func runCIRunnersProvision(args []string) error {
	fs := flag.NewFlagSet("ci-runners provision", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return provisionCIRunnerHosts()
}

func runCIRunnersArchiveArtifacts(args []string) error {
	return archiveCIRunnerArtifacts(args)
}

func runCIRunnersRunSession(args []string) error {
	return runCIRunnerSession(args)
}

func provisionCIRunnerHosts() error {
	if os.Getenv("LINODE_TOKEN") == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	publicKeyPath := firstNonEmpty(os.Getenv("CI_RUNNER_PUBLIC_KEY_PATH"), filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud.pub"))
	allowed := os.Getenv("CI_RUNNER_ALLOWED_SSH_CIDRS")
	if allowed == "" {
		return errors.New("CI_RUNNER_ALLOWED_SSH_CIDRS is required")
	}
	key, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}
	region := firstNonEmpty(os.Getenv("CI_RUNNER_REGION"), "us-sea")
	image := firstNonEmpty(os.Getenv("CI_RUNNER_IMAGE"), "linode/ubuntu24.04")
	seen := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seen[spec.HostLabel] {
			continue
		}
		seen[spec.HostLabel] = true
		rootPass, err := randomPassword()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"label":           spec.HostLabel,
			"region":          region,
			"type":            spec.Type,
			"image":           image,
			"root_pass":       rootPass,
			"authorized_keys": []string{strings.TrimSpace(string(key))},
			"tags":            []string{"rtk-cloud-ci", "github-runner"},
		})
		created, err := curlLinode("POST", "/linode/instances", string(payload))
		if err != nil {
			return err
		}
		var vm struct {
			ID   int      `json:"id"`
			IPv4 []string `json:"ipv4"`
		}
		if err := json.Unmarshal(created, &vm); err != nil {
			return err
		}
		fwPayload, _ := json.Marshal(map[string]any{
			"label": spec.HostLabel + "-firewall",
			"rules": map[string]any{
				"inbound_policy":  "DROP",
				"outbound_policy": "ACCEPT",
				"inbound": []map[string]any{{
					"label": "ssh", "action": "ACCEPT", "protocol": "TCP", "ports": "22",
					"addresses": map[string]any{"ipv4": strings.Split(allowed, ",")},
				}},
				"outbound": []any{},
			},
		})
		fw, err := curlLinode("POST", "/networking/firewalls", string(fwPayload))
		if err != nil {
			return err
		}
		var firewall struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(fw, &firewall)
		if firewall.ID != 0 && vm.ID != 0 {
			device, _ := json.Marshal(map[string]any{"id": vm.ID, "type": "linode"})
			if _, err := curlLinode("POST", fmt.Sprintf("/networking/firewalls/%d/devices", firewall.ID), string(device)); err != nil {
				return err
			}
		}
		publicIP := ""
		if len(vm.IPv4) > 0 {
			publicIP = vm.IPv4[0]
		}
		fmt.Fprintf(os.Stdout, "%s\tcreated\t%s\n", spec.HostLabel, publicIP)
	}
	return nil
}

func archiveCIRunnerArtifacts(args []string) error {
	fs := flag.NewFlagSet("ci-runners archive-artifacts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", "", "GitHub repo")
	runID := fs.String("run-id", "", "GitHub Actions run id")
	outDir := fs.String("out-dir", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *runID == "" {
		return errors.New("--repo and --run-id are required")
	}
	if *outDir == "" {
		*outDir = filepath.Join(".artifacts", "ci-runners", strings.ReplaceAll(*repo, "/", "-"), *runID)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("gh", "run", "download", *runID, "--repo", *repo, "--dir", *outDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	meta := map[string]any{"repo": *repo, "run_id": *runID, "artifact_dir": *outDir, "archived_at": time.Now().UTC().Format(time.RFC3339)}
	return writeJSON(filepath.Join(*outDir, "archive-metadata.json"), meta)
}

func runCIRunnerSession(args []string) error {
	fs := flag.NewFlagSet("ci-runners run-session", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runIDs := map[string]*string{
		"hkt999rtk/rtk_account_manager": fs.String("account-run-id", "", "Account Manager run id"),
		"hkt999rtk/rtk_cloud_admin":     fs.String("admin-run-id", "", "Cloud Admin run id"),
		"hkt999rtk/rtk_cloud_frontend":  fs.String("frontend-run-id", "", "Frontend run id"),
		"hkt999rtk/rtk_cloud_client":    fs.String("client-run-id", "", "Client run id"),
		"hkt999rtk/rtk_cloud_logger":    fs.String("logger-run-id", "", "Logger run id"),
	}
	rerun := fs.Bool("rerun", true, "rerun selected workflows")
	shutdownPolicy := fs.String("shutdown-policy", "always", "always, on-success, or never")
	smokeOnly := fs.Bool("smoke-only", false, "only start/wait/shutdown")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shutdownPolicy != "always" && *shutdownPolicy != "on-success" && *shutdownPolicy != "never" {
		return errors.New("--shutdown-policy must be always, on-success, or never")
	}
	if err := runCIRunnersPower([]string{"start"}); err != nil {
		return err
	}
	shouldShutdown := *shutdownPolicy == "always"
	defer func() {
		if shouldShutdown {
			_ = runCIRunnersPower([]string{"stop"})
		}
	}()
	if err := runCIRunnersWaitOnline(nil); err != nil {
		return err
	}
	if *smokeOnly {
		if *shutdownPolicy == "on-success" {
			shouldShutdown = true
		}
		return nil
	}
	overall := 0
	for repo, idPtr := range runIDs {
		if *idPtr == "" {
			continue
		}
		if *rerun {
			if err := runExternal("gh", "run", "rerun", *idPtr, "--repo", repo); err != nil {
				overall = 1
			}
		}
		if err := runExternal("gh", "run", "watch", *idPtr, "--repo", repo, "--exit-status"); err != nil {
			overall = 1
		}
		if err := archiveCIRunnerArtifacts([]string{"--repo", repo, "--run-id", *idPtr}); err != nil {
			overall = 1
		}
	}
	if *shutdownPolicy == "on-success" && overall == 0 {
		shouldShutdown = true
	}
	if overall != 0 {
		return exitCode(overall)
	}
	return nil
}

func runExternal(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func deployAllServices(paths provisionPaths, env, operator map[string]string, opts provisionOptions) error {
	reportDir := filepath.Join(paths.ArtifactsDir, "readiness-"+time.Now().UTC().Format("20060102T150405Z"))
	report := newReadinessReport(reportDir)
	fmt.Fprintf(os.Stderr, "[cloud-deploy] readiness report: %s\n", report.path())

	runLoggerProvisionHooks(paths, env, report)

	videoEnv := mergeEnv(operator, map[string]string{
		"LINODE_DEPLOY_CERT_CACHE_DIR": filepath.Join(paths.EnvRoot, "certificates", env["VIDEO_CLOUD_DOMAIN"]),
	})
	videoArgs := []string{
		"--stack", env["CLOUD_STACK_NAME"],
		"--config", paths.VideoConfig,
		"--secrets-file", paths.VideoEnv,
		"--env-file", paths.OperatorEnv,
		"--gateway-domain", env["VIDEO_CLOUD_DOMAIN"],
		"--certbot-extra-domain", env["VIDEO_CLOUD_CERTISSUER_DOMAIN"],
	}
	if opts.videoRelease != "" {
		videoArgs = append(videoArgs, "--release", opts.videoRelease)
	}
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_video_cloud"), videoEnv, "linode_deploy/scripts/deploy-staging.sh", videoArgs...); err != nil {
		report.add("video-cloud-deploy-verify", "FAIL", "")
		report.add("cloud-admin-deploy", "SKIP", "video-cloud-deploy-verify")
		report.add("cloud-admin-verify", "SKIP", "video-cloud-deploy-verify")
		_ = report.write(false)
		return err
	}
	report.add("video-cloud-deploy-verify", "PASS", "")

	accountBundle := opts.accountReleaseBundle
	if accountBundle == "" && opts.accountRelease != "" {
		var err error
		accountBundle, err = materializeReleaseBundle(reportDir, operator, "rtk_account_manager", opts.accountRelease)
		if err != nil {
			report.add("account-manager-deploy", "FAIL", "")
			_ = report.write(false)
			return err
		}
	}
	accountEnv, _ := readEnvFile(paths.AccountManagerEnv)
	accountState, _ := readEnvFile(paths.AccountManagerState)
	accountValues := mergeEnv(accountEnv, accountState)
	accountValues = mergeEnv(accountValues, map[string]string{
		"ACCOUNT_MANAGER_LINODE_RELEASE":        opts.accountRelease,
		"ACCOUNT_MANAGER_LINODE_RELEASE_BUNDLE": accountBundle,
		"ACCOUNT_MANAGER_LINODE_CERT_CACHE_DIR": filepath.Join(paths.EnvRoot, "certificates", env["ACCOUNT_MANAGER_DOMAIN"]),
	})
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_account_manager"), accountValues, "linode_deploy/scripts/deploy-public-vm.sh"); err != nil {
		report.add("account-manager-deploy", "FAIL", "")
		_ = report.write(false)
		return err
	}
	report.add("account-manager-deploy", "PASS", "")
	_ = runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_account_manager"), accountValues, "linode_deploy/scripts/verify-public-vm.sh")

	adminEnv, _ := readEnvFile(paths.AdminEnv)
	adminState, _ := readEnvFile(paths.AdminState)
	adminValues := mergeEnv(adminEnv, adminState)
	adminValues = mergeEnv(adminValues, map[string]string{
		"ADMIN_LINODE_RELEASE":            opts.adminRelease,
		"ADMIN_LINODE_RELEASE_BUNDLE":     opts.adminReleaseBundle,
		"ADMIN_LINODE_CERT_CACHE_DIR":     filepath.Join(paths.EnvRoot, "certificates", env["CLOUD_ADMIN_DOMAIN"]),
		"ACCOUNT_MANAGER_BASE_URL":        "https://" + env["ACCOUNT_MANAGER_DOMAIN"],
		"VIDEO_CLOUD_BASE_URL":            "https://" + env["VIDEO_CLOUD_DOMAIN"],
		"VIDEO_CLOUD_PROMETHEUS_BASE_URL": videoCloudPrometheusBaseURL(paths),
	})
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_cloud_admin"), adminValues, "deploy/linode/deploy-admin.sh"); err != nil {
		report.add("cloud-admin-deploy", "FAIL", "")
		_ = report.write(false)
		return err
	}
	report.add("cloud-admin-deploy", "PASS", "")
	_ = runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_cloud_admin"), adminValues, "deploy/linode/verify-admin.sh")
	report.add("cloud-admin-verify", "PASS", "")
	runLoggerReadinessHooks(paths, env, report)
	return report.write(true)
}

func runLoggerProvisionHooks(paths provisionPaths, env map[string]string, report *readinessReport) {
	script := os.Getenv("CLOUD_LOGGER_SCRIPT")
	if script == "" {
		return
	}
	loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
	loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	endpoint := firstNonEmpty(env["CLOUD_LOGGER_ENDPOINT"], "https://logger."+env["CLOUD_DNS_ROOT_DOMAIN"])
	if err := runCmdWithEnv(paths.Workspace, nil, script, "provision-backend", "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--endpoint", endpoint); err != nil {
		report.add("logger-backend-provision", "DEGRADED", "")
	}
	targets := []struct {
		name string
		host string
	}{
		{"account-manager", envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4")},
		{"video-cloud-api", videoStateInstanceHost(paths.VideoState, "api")},
		{"cloud-admin", envFileValue(paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4")},
		{"frontend", ""},
		{"non-go-host-sources", ""},
	}
	for _, target := range targets {
		args := []string{"install-forwarder", target.name, "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--host", target.host, "--journald-system-max-use", "512M", "--journald-system-keep-free", "1G", "--journald-max-retention-sec", "604800"}
		if err := runCmdWithEnv(paths.Workspace, nil, script, args...); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
		}
	}
}

func runLoggerReadinessHooks(paths provisionPaths, env map[string]string, report *readinessReport) {
	script := os.Getenv("CLOUD_LOGGER_SCRIPT")
	if script == "" {
		return
	}
	loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
	loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	checks := [][]string{
		{"backend-health"},
		{"forwarder-status", "account-manager"},
		{"forwarder-status", "video-cloud-api"},
		{"forwarder-status", "cloud-admin"},
		{"sample-trace-query"},
	}
	for _, check := range checks {
		name := "logger-" + check[0]
		args := append([]string{}, check...)
		args = append(args, "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--endpoint", firstNonEmpty(env["CLOUD_LOGGER_ENDPOINT"], ""))
		if len(check) > 1 {
			name = "logger-" + check[0] + ":" + check[1]
		}
		if err := runCmdWithEnv(paths.Workspace, nil, script, args...); err != nil {
			report.add(name, "DEGRADED", "")
		}
	}
}

func videoStateInstanceHost(path, role string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		Instances map[string]struct {
			PublicIPv4 string `json:"public_ipv4"`
			PrivateIP  string `json:"private_ip"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	inst := state.Instances[role]
	return firstNonEmpty(inst.PrivateIP, inst.PublicIPv4)
}

func materializeReleaseBundle(dir string, operator map[string]string, prefix, release string) (string, error) {
	store, err := provisionObjectStoreFromEnv(operator)
	if err != nil {
		return "", err
	}
	manifestKey := "releases/" + prefix + "-" + release + "/manifest.json"
	manifestData, err := provisionReadObject(store, manifestKey)
	if err != nil {
		return "", err
	}
	manifest := map[string]any{}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", err
	}
	objectKey := stringValue(manifest["artifact_path"])
	if objectKey == "" {
		return "", fmt.Errorf("release manifest missing artifact_path: %s", manifestKey)
	}
	out := filepath.Join(dir, filepath.Base(objectKey))
	if err := provisionWriteObjectToFile(store, objectKey, out); err != nil {
		return "", err
	}
	return out, nil
}

type readinessReport struct {
	dir   string
	steps []readinessStep
}

type readinessStep struct {
	Name      string
	Status    string
	BlockedBy string
}

func newReadinessReport(dir string) *readinessReport {
	return &readinessReport{dir: dir}
}

func (r *readinessReport) add(name, status, blockedBy string) {
	r.steps = append(r.steps, readinessStep{Name: name, Status: status, BlockedBy: blockedBy})
}

func (r *readinessReport) path() string {
	return filepath.Join(r.dir, "readiness-report.md")
}

func (r *readinessReport) write(ok bool) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	status := "failed"
	if ok {
		status = "passed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Readiness Report\n\nstatus: %s\n\n", status)
	if r.hasStatus("DEGRADED") {
		fmt.Fprintf(&b, "logging: degraded\n\n")
	}
	for _, step := range r.steps {
		if step.BlockedBy != "" {
			fmt.Fprintf(&b, "- %s `%s` blocked_by=`%s`\n", step.Status, step.Name, step.BlockedBy)
		} else {
			fmt.Fprintf(&b, "- %s `%s`\n", step.Status, step.Name)
		}
	}
	return os.WriteFile(r.path(), []byte(b.String()), 0o644)
}

func (r *readinessReport) hasStatus(status string) bool {
	for _, step := range r.steps {
		if step.Status == status {
			return true
		}
	}
	return false
}
