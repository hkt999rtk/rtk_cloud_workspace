package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	loggerOnly := fs.Bool("logger-only", false, "install and verify only logger backend and log forwarders")
	videoOnly := fs.Bool("video-only", false, "deploy only Video Cloud")
	binaryOnly := fs.Bool("binary-only", false, "fast path: update only Video Cloud API binaries; requires --video-only and --video-release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *binaryOnly {
		if !*videoOnly {
			return errors.New("--binary-only requires --video-only")
		}
		if strings.TrimSpace(*videoRelease) == "" {
			return errors.New("--binary-only requires --video-release")
		}
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
	paths.VideoState = provisionCloudVideoStatePath(envRoot, env.Values["CLOUD_STACK_NAME"], paths.VideoState)
	operator, _ := readEnvFile(paths.OperatorEnv)
	return deployAllServices(paths, env.Values, operator, provisionOptions{
		videoRelease:         *videoRelease,
		accountRelease:       *accountRelease,
		adminRelease:         *adminRelease,
		accountReleaseBundle: *accountBundle,
		adminReleaseBundle:   *adminBundle,
		loggerOnly:           *loggerOnly,
		videoOnly:            *videoOnly,
		binaryOnly:           *binaryOnly,
		sshKey:               *sshKey,
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
	if opts.sshKey == "" {
		opts.sshKey = defaultStagingSSHKey()
	}
	logLevels, err := serviceLogLevelsFrom(env, processServiceLogLevelEnv())
	if err != nil {
		return err
	}
	reportDir := filepath.Join(paths.ArtifactsDir, "readiness-"+time.Now().UTC().Format("20060102T150405Z"))
	report := newReadinessReport(reportDir)
	fmt.Fprintf(os.Stderr, "[cloud-deploy] readiness report: %s\n", report.path())

	runLoggerProvisionHooks(paths, env, opts.sshKey, report)
	runLoggerForwarderInstallHooks(paths, env, opts.sshKey, report)
	runLoggerReadinessHooks(paths, env, opts.sshKey, report)
	if opts.loggerOnly {
		return report.write(true)
	}

	videoEnv := mergeEnv(operator, map[string]string{
		"LINODE_DEPLOY_CERT_CACHE_DIR": filepath.Join(paths.EnvRoot, "certificates", env["VIDEO_CLOUD_DOMAIN"]),
		"VIDEO_CLOUD_LOG_LEVEL":        logLevels["VIDEO_CLOUD_LOG_LEVEL"],
	})
	videoArgs := videoDeployArgs(paths, env, opts)
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_video_cloud"), videoEnv, "linode_deploy/scripts/deploy-staging.sh", videoArgs...); err != nil {
		report.add("video-cloud-deploy-verify", "FAIL", "")
		report.add("cloud-admin-deploy", "SKIP", "video-cloud-deploy-verify")
		report.add("cloud-admin-verify", "SKIP", "video-cloud-deploy-verify")
		_ = report.write(false)
		return err
	}
	report.add("video-cloud-deploy-verify", "PASS", "")
	if opts.videoOnly {
		runLoggerForwarderInstallHooks(paths, env, opts.sshKey, report)
		runLoggerReadinessHooks(paths, env, opts.sshKey, report)
		return report.write(true)
	}

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
		"ACCOUNT_MANAGER_LOG_LEVEL":             logLevels["ACCOUNT_MANAGER_LOG_LEVEL"],
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
		"CLOUD_ADMIN_LOG_LEVEL":           logLevels["CLOUD_ADMIN_LOG_LEVEL"],
	})
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_cloud_admin"), adminValues, "deploy/linode/deploy-admin.sh"); err != nil {
		report.add("cloud-admin-deploy", "FAIL", "")
		_ = report.write(false)
		return err
	}
	report.add("cloud-admin-deploy", "PASS", "")
	_ = runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_cloud_admin"), adminValues, "deploy/linode/verify-admin.sh")
	report.add("cloud-admin-verify", "PASS", "")
	runLoggerForwarderInstallHooks(paths, env, opts.sshKey, report)
	runLoggerReadinessHooks(paths, env, opts.sshKey, report)
	if err := report.write(true); err != nil {
		return err
	}
	writePlatformAdminSummary(os.Stdout, paths)
	return nil
}

func videoDeployArgs(paths provisionPaths, env map[string]string, opts provisionOptions) []string {
	args := []string{
		"--stack", env["CLOUD_STACK_NAME"],
		"--config", paths.VideoConfig,
		"--secrets-file", paths.VideoEnv,
		"--env-file", paths.OperatorEnv,
		"--gateway-domain", env["VIDEO_CLOUD_DOMAIN"],
		"--certbot-extra-domain", env["VIDEO_CLOUD_CERTISSUER_DOMAIN"],
	}
	if opts.videoRelease != "" {
		args = append(args, "--release", opts.videoRelease)
	}
	if opts.binaryOnly {
		args = append(args, "--binary-only")
	}
	return args
}

func serviceLogLevels(env map[string]string) (map[string]string, error) {
	return serviceLogLevelsFrom(env, nil)
}

func serviceLogLevelsFrom(env, process map[string]string) (map[string]string, error) {
	global, err := serviceLogLevelValue("CLOUD_SERVICE_LOG_LEVEL", env, process, "info")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, key := range []string{"VIDEO_CLOUD_LOG_LEVEL", "ACCOUNT_MANAGER_LOG_LEVEL", "CLOUD_ADMIN_LOG_LEVEL"} {
		value, err := serviceLogLevelValue(key, env, process, global)
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

func serviceLogLevelValue(key string, env, process map[string]string, fallback string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(firstNonEmpty(process[key], env[key], fallback)))
	switch value {
	case "debug", "info", "warn", "error":
		return value, nil
	default:
		return "", fmt.Errorf("%s must be one of debug, info, warn, error: %s", key, value)
	}
}

func processServiceLogLevelEnv() map[string]string {
	out := map[string]string{}
	for _, key := range []string{"CLOUD_SERVICE_LOG_LEVEL", "VIDEO_CLOUD_LOG_LEVEL", "ACCOUNT_MANAGER_LOG_LEVEL", "CLOUD_ADMIN_LOG_LEVEL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			out[key] = value
		}
	}
	return out
}

func writePlatformAdminSummary(w io.Writer, paths provisionPaths) {
	platformEnv := filepath.Join(paths.EnvRoot, "services", "account-manager", "account-manager-platform-admin.env")
	username := envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Platform admin:")
	if username == "" {
		fmt.Fprintln(w, "- username: unavailable")
		fmt.Fprintf(w, "- password: see %s\n", platformEnv)
		return
	}
	fmt.Fprintf(w, "- username: %s\n", username)
	fmt.Fprintf(w, "- password: see %s\n", platformEnv)
	fmt.Fprintln(w, "- token: run ./stg.sh token")
}

func runLoggerProvisionHooks(paths provisionPaths, env map[string]string, sshKey string, report *readinessReport) {
	script := os.Getenv("CLOUD_LOGGER_SCRIPT")
	if script != "" {
		loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
		loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
		endpoint := firstNonEmpty(env["CLOUD_LOGGER_ENDPOINT"], "https://logger."+env["CLOUD_DNS_ROOT_DOMAIN"])
		if err := runCmdWithEnv(paths.Workspace, nil, script, "provision-backend", "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--endpoint", endpoint); err != nil {
			report.add("logger-backend-provision", "DEGRADED", "")
		} else {
			report.add("logger-backend-provision", "PASS", "")
		}
		return
	}
	if err := installNativeLoggerBackend(paths, env, sshKey); err != nil {
		report.add("logger-backend-provision", "DEGRADED", "")
		fmt.Fprintf(os.Stderr, "[cloud-deploy] logger backend provision degraded: %v\n", err)
	} else {
		report.add("logger-backend-provision", "PASS", "")
	}
}

func installNativeLoggerBackend(paths provisionPaths, env map[string]string, sshKey string) error {
	loggerEnv, _ := readEnvFile(filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env"))
	loggerState, _ := readEnvFile(filepath.Join(paths.EnvRoot, "state", "cloud-logger.env"))
	endpoint := firstNonEmpty(loggerEnv["CLOUD_LOGGER_ENDPOINT"], loggerState["CLOUD_LOGGER_ENDPOINT"], env["CLOUD_LOGGER_ENDPOINT"])
	token := firstNonEmpty(loggerEnv["CLOUD_LOGGER_INGEST_TOKEN"], loggerState["CLOUD_LOGGER_INGEST_TOKEN"])
	host := loggerState["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"]
	domain := firstNonEmpty(loggerState["CLOUD_LOGGER_DOMAIN"], loggerEnv["CLOUD_LOGGER_DOMAIN"], env["CLOUD_LOGGER_DOMAIN"])
	if endpoint == "" || token == "" || host == "" {
		return errors.New("logger endpoint, ingest token, and logger host are required")
	}
	if domain == "" {
		if parsed, err := url.Parse(endpoint); err == nil {
			domain = parsed.Hostname()
		}
	}
	binary, cleanup, err := buildLoggerBackend(paths.Workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := uploadLoggerBinary(paths, sshKey, host, binary, "/usr/local/bin/rtk-cloud-logger"); err != nil {
		return err
	}
	script := loggerBackendInstallScript(domain, token, firstNonEmpty(os.Getenv("CLOUD_LOGGER_LOKI_VERSION"), "v3.5.1"))
	return runCmdWithInput("", script, "ssh", loggerSSHArgs(paths, sshKey, host, "bash", "-s")...)
}

func buildLoggerBackend(workspace string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-cloud-logger-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "rtk-cloud-logger")
	if err := runCmdWithEnv(filepath.Join(workspace, "repos", "rtk_cloud_logger"), linuxBuildEnv(), "go", "build", "-o", binary, "./cmd/rtk-cloud-logger"); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return binary, cleanup, nil
}

func loggerBackendInstallScript(domain, token, lokiVersion string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "set -euo pipefail")
	fmt.Fprintln(&b, "export DEBIAN_FRONTEND=noninteractive")
	fmt.Fprintln(&b, "apt-get update")
	fmt.Fprintln(&b, "apt-get install -y nginx certbot python3-certbot-nginx curl unzip")
	fmt.Fprintln(&b, "install -d -m 0755 /etc/rtk-cloud /var/lib/loki")
	fmt.Fprintln(&b, "if ! command -v loki >/dev/null 2>&1; then")
	fmt.Fprintf(&b, "  curl -fsSL -o /tmp/loki-linux-amd64.zip https://github.com/grafana/loki/releases/download/%s/loki-linux-amd64.zip\n", shellEnvValue(lokiVersion))
	fmt.Fprintln(&b, "  unzip -o /tmp/loki-linux-amd64.zip -d /tmp")
	fmt.Fprintln(&b, "  install -m 0755 /tmp/loki-linux-amd64 /usr/local/bin/loki")
	fmt.Fprintln(&b, "fi")
	fmt.Fprintln(&b, "cat > /etc/loki-local-config.yaml <<'EOF'")
	fmt.Fprintln(&b, "auth_enabled: false")
	fmt.Fprintln(&b, "server:")
	fmt.Fprintln(&b, "  http_listen_address: 127.0.0.1")
	fmt.Fprintln(&b, "  http_listen_port: 3100")
	fmt.Fprintln(&b, "common:")
	fmt.Fprintln(&b, "  path_prefix: /var/lib/loki")
	fmt.Fprintln(&b, "  replication_factor: 1")
	fmt.Fprintln(&b, "  ring:")
	fmt.Fprintln(&b, "    kvstore:")
	fmt.Fprintln(&b, "      store: inmemory")
	fmt.Fprintln(&b, "schema_config:")
	fmt.Fprintln(&b, "  configs:")
	fmt.Fprintln(&b, "    - from: 2024-01-01")
	fmt.Fprintln(&b, "      store: tsdb")
	fmt.Fprintln(&b, "      object_store: filesystem")
	fmt.Fprintln(&b, "      schema: v13")
	fmt.Fprintln(&b, "      index:")
	fmt.Fprintln(&b, "        prefix: index_")
	fmt.Fprintln(&b, "        period: 24h")
	fmt.Fprintln(&b, "storage_config:")
	fmt.Fprintln(&b, "  filesystem:")
	fmt.Fprintln(&b, "    directory: /var/lib/loki/chunks")
	fmt.Fprintln(&b, "  tsdb_shipper:")
	fmt.Fprintln(&b, "    active_index_directory: /var/lib/loki/tsdb-index")
	fmt.Fprintln(&b, "    cache_location: /var/lib/loki/tsdb-cache")
	fmt.Fprintln(&b, "limits_config:")
	fmt.Fprintln(&b, "  allow_structured_metadata: false")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "cat > /etc/systemd/system/loki.service <<'EOF'")
	fmt.Fprintln(&b, "[Unit]")
	fmt.Fprintln(&b, "Description=Loki log storage")
	fmt.Fprintln(&b, "After=network-online.target")
	fmt.Fprintln(&b, "Wants=network-online.target")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Service]")
	fmt.Fprintln(&b, "ExecStart=/usr/local/bin/loki -config.file=/etc/loki-local-config.yaml")
	fmt.Fprintln(&b, "Restart=always")
	fmt.Fprintln(&b, "RestartSec=5")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=multi-user.target")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "cat > /etc/rtk-cloud/logger.env <<'EOF'")
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_TOKEN=%s\n", shellEnvValue(token))
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_STORE=loki")
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_LOKI_URL=http://127.0.0.1:3100")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "chmod 0600 /etc/rtk-cloud/logger.env")
	fmt.Fprintln(&b, "cat > /etc/systemd/system/rtk-cloud-logger.service <<'EOF'")
	fmt.Fprintln(&b, "[Unit]")
	fmt.Fprintln(&b, "Description=RTK Cloud central logger")
	fmt.Fprintln(&b, "After=network-online.target loki.service")
	fmt.Fprintln(&b, "Wants=network-online.target loki.service")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Service]")
	fmt.Fprintln(&b, "EnvironmentFile=/etc/rtk-cloud/logger.env")
	fmt.Fprintln(&b, "ExecStart=/usr/local/bin/rtk-cloud-logger -addr 127.0.0.1:18090")
	fmt.Fprintln(&b, "Restart=always")
	fmt.Fprintln(&b, "RestartSec=5")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=multi-user.target")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "cat > /etc/nginx/sites-available/rtk-cloud-logger <<'EOF'")
	fmt.Fprintln(&b, "server {")
	fmt.Fprintln(&b, "  listen 80;")
	if domain != "" {
		fmt.Fprintf(&b, "  server_name %s;\n", shellEnvValue(domain))
	} else {
		fmt.Fprintln(&b, "  server_name _;")
	}
	fmt.Fprintln(&b, "  location / {")
	fmt.Fprintln(&b, "    proxy_pass http://127.0.0.1:18090;")
	fmt.Fprintln(&b, "    proxy_set_header Host $host;")
	fmt.Fprintln(&b, "    proxy_set_header X-Forwarded-Proto $scheme;")
	fmt.Fprintln(&b, "    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;")
	fmt.Fprintln(&b, "  }")
	fmt.Fprintln(&b, "}")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "ln -sf /etc/nginx/sites-available/rtk-cloud-logger /etc/nginx/sites-enabled/rtk-cloud-logger")
	fmt.Fprintln(&b, "rm -f /etc/nginx/sites-enabled/default")
	fmt.Fprintln(&b, "systemctl daemon-reload")
	fmt.Fprintln(&b, "systemctl enable loki.service rtk-cloud-logger.service nginx.service")
	fmt.Fprintln(&b, "systemctl restart loki.service rtk-cloud-logger.service")
	fmt.Fprintln(&b, "systemctl reload-or-restart nginx.service")
	if domain != "" {
		fmt.Fprintf(&b, "certbot --nginx -d %s --non-interactive --agree-tos --register-unsafely-without-email --redirect\n", shellEnvValue(domain))
	}
	return b.String()
}

type loggerForwarderTarget struct {
	name  string
	host  string
	units string
}

func runLoggerForwarderInstallHooks(paths provisionPaths, env map[string]string, sshKey string, report *readinessReport) {
	targets := loggerForwarderTargets(paths)
	if script := os.Getenv("CLOUD_LOGGER_SCRIPT"); script != "" {
		runLoggerForwarderScriptHooks(paths, script, targets, report)
		return
	}
	installNativeLoggerForwarders(paths, env, sshKey, targets, report)
}

func runLoggerForwarderScriptHooks(paths provisionPaths, script string, targets []loggerForwarderTarget, report *readinessReport) {
	loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
	loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	for _, target := range targets {
		args := []string{"install-forwarder", target.name, "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--host", target.host, "--units", target.units, "--journald-system-max-use", "512M", "--journald-system-keep-free", "1G", "--journald-max-retention-sec", "604800"}
		if err := runCmdWithEnv(paths.Workspace, nil, script, args...); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
		}
	}
}

func loggerForwarderTargets(paths provisionPaths) []loggerForwarderTarget {
	return []loggerForwarderTarget{
		{"account-manager", envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"), "rtk-account-manager.service,rtk-account-manager-inbox-worker.service,rtk-account-manager-outbox-worker.service"},
		{"video-cloud-api", videoStateInstanceHost(paths.VideoState, "api"), "video_cloud-api.service,video_cloud-logingester.service,video_cloud-turnregistry.service,video_cloud-metricsexporter.service,video_cloud-crossservice.service,video_cloud-cleaner.service,video_cloud-statistics.service,video_cloud-certissuer.service,video_cloud-factoryenroll.service"},
		{"cloud-admin", envFileValue(paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4"), "rtk-cloud-admin.service"},
		{"edge", videoStateInstanceHost(paths.VideoState, "edge"), "nginx.service,certbot.timer"},
		{"infra", videoStateInstanceHost(paths.VideoState, "infra"), "postgresql.service,postgresql@16-main.service,redis-server.service,nats-server.service,prometheus.service,prometheus-node-exporter.service,prometheus-postgres-exporter.service,prometheus-redis-exporter.service"},
		{"mqtt", videoStateInstanceHost(paths.VideoState, "mqtt"), "emqx.service"},
		{"coturn", videoStateInstanceHost(paths.VideoState, "coturn"), "coturn.service,video_cloud-turnregistrar.service"},
	}
}

func installNativeLoggerForwarders(paths provisionPaths, env map[string]string, sshKey string, targets []loggerForwarderTarget, report *readinessReport) {
	loggerEnv, _ := readEnvFile(filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env"))
	loggerState, _ := readEnvFile(filepath.Join(paths.EnvRoot, "state", "cloud-logger.env"))
	endpoint := firstNonEmpty(loggerEnv["CLOUD_LOGGER_ENDPOINT"], loggerState["CLOUD_LOGGER_ENDPOINT"], env["CLOUD_LOGGER_ENDPOINT"])
	token := firstNonEmpty(loggerEnv["CLOUD_LOGGER_INGEST_TOKEN"], loggerState["CLOUD_LOGGER_INGEST_TOKEN"])
	loggerHostIP := loggerState["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"]
	if endpoint == "" || token == "" {
		markLoggerForwardersDegraded(report, targets)
		fmt.Fprintln(os.Stderr, "[cloud-deploy] logger forwarder install degraded: logger endpoint and ingest token are required")
		return
	}
	binary, cleanup, err := buildLoggerForwarder(paths.Workspace)
	if err != nil {
		markLoggerForwardersDegraded(report, targets)
		fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder install degraded: %v\n", err)
		return
	}
	defer cleanup()
	for _, target := range targets {
		proxyURL := ""
		if isPrivateIPv4(target.host) {
			if edge := videoStateInstanceHost(paths.VideoState, "edge"); edge != "" {
				proxyURL = "http://" + edge + ":3128"
			}
		}
		if err := installNativeLoggerForwarderTarget(paths, sshKey, binary, endpoint, token, loggerHostIP, proxyURL, target); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
			fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder install degraded: target=%s error=%v\n", target.name, err)
		}
	}
}

func markLoggerForwardersDegraded(report *readinessReport, targets []loggerForwarderTarget) {
	for _, target := range targets {
		report.add("logger-forwarder:"+target.name, "DEGRADED", "")
	}
}

func installNativeLoggerForwarderTarget(paths provisionPaths, sshKey, binary, endpoint, token, loggerHostIP, proxyURL string, target loggerForwarderTarget) error {
	if target.host == "" {
		return fmt.Errorf("logger forwarder target host missing: %s", target.name)
	}
	if err := uploadLoggerBinary(paths, sshKey, target.host, binary, "/usr/local/bin/rtk-cloud-log-forwarder"); err != nil {
		return err
	}
	script := loggerForwarderInstallScript(endpoint, token, target.units, loggerHostIP, proxyURL)
	return runCmdWithInput("", script, "ssh", loggerSSHArgs(paths, sshKey, target.host, "bash", "-s")...)
}

func uploadLoggerBinary(paths provisionPaths, sshKey, host, source, dest string) error {
	tmp := "/tmp/." + filepath.Base(dest) + "." + strconv.FormatInt(time.Now().UnixNano(), 10)
	remoteTmp := "root@" + host + ":" + tmp
	if err := runExternal("scp", loggerSCPArgs(paths, sshKey, host, source, remoteTmp)...); err != nil {
		return err
	}
	script := "set -euo pipefail\nchmod 0755 " + tmp + "\nmv -f " + tmp + " " + dest + "\n"
	return runCmdWithInput("", script, "ssh", loggerSSHArgs(paths, sshKey, host, "bash", "-s")...)
}

func buildLoggerForwarder(workspace string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-cloud-log-forwarder-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "rtk-cloud-log-forwarder")
	if err := runCmdWithEnv(filepath.Join(workspace, "repos", "rtk_cloud_logger"), linuxBuildEnv(), "go", "build", "-o", binary, "./cmd/rtk-cloud-log-forwarder"); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return binary, cleanup, nil
}

func loggerForwarderInstallScript(endpoint, token, units, loggerHostIP, proxyURL string) string {
	var b strings.Builder
	loggerHost := ""
	if parsed, err := url.Parse(endpoint); err == nil {
		loggerHost = parsed.Hostname()
	}
	fmt.Fprintln(&b, "set -euo pipefail")
	fmt.Fprintln(&b, "install -d -m 0755 /etc/rtk-cloud /var/lib/rtk-cloud-logger/spool")
	if loggerHost != "" && loggerHostIP != "" {
		fmt.Fprintf(&b, "sed -i.bak '/[[:space:]]%s$/d' /etc/hosts\n", shellEnvValue(loggerHost))
		fmt.Fprintf(&b, "printf '%%s %%s\\n' %s %s >> /etc/hosts\n", shellEnvValue(loggerHostIP), shellEnvValue(loggerHost))
	}
	fmt.Fprintln(&b, "cat > /etc/rtk-cloud/log-forwarder.env <<'EOF'")
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_INGEST_URL=%s\n", shellEnvValue(loggerIngestURL(endpoint)))
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_TOKEN=%s\n", shellEnvValue(token))
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_UNITS=%s\n", shellEnvValue(units))
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_CURSOR=/var/lib/rtk-cloud-logger/journal.cursor")
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_SPOOL_DIR=/var/lib/rtk-cloud-logger/spool")
	if proxyURL != "" {
		fmt.Fprintf(&b, "HTTPS_PROXY=%s\n", shellEnvValue(proxyURL))
		fmt.Fprintf(&b, "HTTP_PROXY=%s\n", shellEnvValue(proxyURL))
		fmt.Fprintln(&b, "NO_PROXY=localhost,127.0.0.1,10.42.0.0/16")
	}
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "chmod 0600 /etc/rtk-cloud/log-forwarder.env")
	fmt.Fprintln(&b, "cat > /etc/systemd/system/rtk-cloud-log-forwarder.service <<'EOF'")
	fmt.Fprintln(&b, "[Unit]")
	fmt.Fprintln(&b, "Description=RTK Cloud log forwarder")
	fmt.Fprintln(&b, "After=network-online.target")
	fmt.Fprintln(&b, "Wants=network-online.target")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Service]")
	fmt.Fprintln(&b, "EnvironmentFile=/etc/rtk-cloud/log-forwarder.env")
	fmt.Fprintln(&b, "ExecStart=/usr/local/bin/rtk-cloud-log-forwarder")
	fmt.Fprintln(&b, "Restart=always")
	fmt.Fprintln(&b, "RestartSec=5")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=multi-user.target")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "systemctl daemon-reload")
	fmt.Fprintln(&b, "systemctl enable rtk-cloud-log-forwarder.service")
	fmt.Fprintln(&b, "systemctl restart rtk-cloud-log-forwarder.service")
	return b.String()
}

func loggerIngestURL(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(endpoint, "/v1/logs/ingest") {
		return endpoint
	}
	return endpoint + "/v1/logs/ingest"
}

func defaultStagingSSHKey() string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")
}

func linuxBuildEnv() map[string]string {
	return map[string]string{
		"GOWORK": "off",
		"GOOS":   "linux",
		"GOARCH": "amd64",
	}
}

func loggerSSHArgs(paths provisionPaths, sshKey, host string, remoteArgs ...string) []string {
	if sshKey == "" {
		sshKey = defaultStagingSSHKey()
	}
	args := []string{
		"-i", sshKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "LogLevel=ERROR",
	}
	if proxy := loggerProxyCommand(paths, sshKey, host); proxy != "" {
		args = append(args, "-o", "ProxyCommand="+proxy)
	}
	args = append(args, "root@"+host)
	args = append(args, remoteArgs...)
	return args
}

func loggerSCPArgs(paths provisionPaths, sshKey, host, source, dest string) []string {
	if sshKey == "" {
		sshKey = defaultStagingSSHKey()
	}
	args := []string{
		"-i", sshKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "LogLevel=ERROR",
	}
	if proxy := loggerProxyCommand(paths, sshKey, host); proxy != "" {
		args = append(args, "-o", "ProxyCommand="+proxy)
	}
	args = append(args, source, dest)
	return args
}

func loggerProxyCommand(paths provisionPaths, sshKey, host string) string {
	if !isPrivateIPv4(host) {
		return ""
	}
	edge := videoStatePublicHost(paths.VideoState, "edge")
	if edge == "" || edge == host {
		return ""
	}
	return strings.Join([]string{
		"ssh",
		"-i", shellQuote(sshKey),
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		"-o", "LogLevel=ERROR",
		"-W", "%h:%p",
		"root@" + edge,
	}, " ")
}

func isPrivateIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	a := atoiOrZero(parts[0])
	b := atoiOrZero(parts[1])
	switch {
	case a == 10:
		return true
	case a == 172 && b >= 16 && b <= 31:
		return true
	case a == 192 && b == 168:
		return true
	default:
		return false
	}
}

func shellEnvValue(value string) string {
	return strings.ReplaceAll(value, "\n", "")
}

func runCmdWithInput(dir, input, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func runLoggerReadinessHooks(paths provisionPaths, env map[string]string, sshKey string, report *readinessReport) {
	script := os.Getenv("CLOUD_LOGGER_SCRIPT")
	if script != "" {
		runLoggerReadinessScriptHooks(paths, env, script, report)
		return
	}
	runNativeLoggerReadinessChecks(paths, env, sshKey, report)
}

func runLoggerReadinessScriptHooks(paths provisionPaths, env map[string]string, script string, report *readinessReport) {
	loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
	loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	checks := [][]string{
		{"backend-health"},
		{"sample-trace-query"},
	}
	for _, target := range loggerForwarderTargets(paths) {
		checks = append(checks, []string{"forwarder-status", target.name})
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
		} else {
			report.add(name, "PASS", "")
		}
	}
}

func runNativeLoggerReadinessChecks(paths provisionPaths, env map[string]string, sshKey string, report *readinessReport) {
	loggerEnv, _ := readEnvFile(filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env"))
	loggerState, _ := readEnvFile(filepath.Join(paths.EnvRoot, "state", "cloud-logger.env"))
	endpoint := firstNonEmpty(loggerEnv["CLOUD_LOGGER_ENDPOINT"], loggerState["CLOUD_LOGGER_ENDPOINT"], env["CLOUD_LOGGER_ENDPOINT"])
	token := firstNonEmpty(loggerEnv["CLOUD_LOGGER_INGEST_TOKEN"], loggerState["CLOUD_LOGGER_INGEST_TOKEN"])
	if endpoint == "" || token == "" {
		report.add("logger-backend-health", "DEGRADED", "")
		report.add("logger-ingest-idempotency", "DEGRADED", "")
		report.add("logger-sample-trace-query", "DEGRADED", "")
		for _, target := range loggerForwarderTargets(paths) {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
		}
		return
	}
	if err := loggerHTTP(endpoint, token, http.MethodGet, "/healthz", ""); err != nil {
		report.add("logger-backend-health", "DEGRADED", "")
	} else {
		report.add("logger-backend-health", "PASS", "")
	}
	eventID := "readiness-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	traceID := eventID + "-trace"
	requestID := eventID + "-request"
	event := map[string]any{
		"event_id":   eventID,
		"ts":         time.Now().UTC().Format(time.RFC3339Nano),
		"level":      "info",
		"msg":        "logger readiness probe",
		"service":    "workspace-readiness",
		"env":        firstNonEmpty(env["CLOUD_ENV_NAME"], "staging"),
		"version":    "workspace",
		"host":       "operator",
		"unit":       "stg.sh",
		"source":     "readiness",
		"trace_id":   traceID,
		"request_id": requestID,
	}
	bodyBytes, _ := json.Marshal(map[string]any{"events": []map[string]any{event}})
	if err := loggerHTTP(endpoint, token, http.MethodPost, "/v1/logs/ingest", string(bodyBytes)); err != nil {
		report.add("logger-ingest-idempotency", "DEGRADED", "")
	} else if err := loggerHTTP(endpoint, token, http.MethodPost, "/v1/logs/ingest", string(bodyBytes)); err != nil {
		report.add("logger-ingest-idempotency", "DEGRADED", "")
	} else {
		report.add("logger-ingest-idempotency", "PASS", "")
	}
	queryPath := "/v1/logs?service=workspace-readiness&trace_id=" + url.QueryEscape(traceID) + "&request_id=" + url.QueryEscape(requestID)
	if raw, err := loggerHTTPOutput(endpoint, token, http.MethodGet, queryPath, ""); err != nil || !strings.Contains(string(raw), eventID) {
		report.add("logger-sample-trace-query", "DEGRADED", "")
	} else {
		report.add("logger-sample-trace-query", "PASS", "")
	}
	for _, target := range loggerForwarderTargets(paths) {
		if target.host == "" {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
			continue
		}
		if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, target.host, "systemctl", "is-active", "--quiet", "rtk-cloud-log-forwarder.service")...); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
		} else {
			report.add("logger-forwarder:"+target.name, "PASS", "")
		}
	}
}

func loggerHTTP(endpoint, token, method, path, body string) error {
	_, err := loggerHTTPOutput(endpoint, token, method, path, body)
	return err
}

func loggerHTTPOutput(endpoint, token, method, path, body string) ([]byte, error) {
	args := []string{"-fsS", "-X", method, strings.TrimRight(endpoint, "/") + path, "-H", "Authorization: Bearer " + token, "-H", "Content-Type: application/json"}
	if body != "" {
		args = append(args, "--data-binary", body)
	}
	cmd := exec.Command("curl", args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return out, err
	}
	return out, nil
}

func runCmdQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
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

func videoStatePublicHost(path, role string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		Instances map[string]struct {
			PublicIPv4 string `json:"public_ipv4"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.Instances[role].PublicIPv4
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
