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
	binaryOnly := fs.Bool("binary-only", false, "fast path: update only Video Cloud API binaries")
	localBuild := fs.Bool("local-build", false, "build a local Linux x86_64 Video Cloud bundle before deploy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *binaryOnly && !*videoOnly {
		*videoOnly = true
	}
	if *binaryOnly && *loggerOnly {
		return errors.New("--binary-only cannot be combined with --logger-only")
	}
	if *localBuild && *loggerOnly {
		return errors.New("--local-build cannot be combined with --logger-only")
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
	if env.Values["CLOUD_PROVIDER"] == "lke" {
		return runLKEProvision(paths, env.Values, provisionOptions{
			mode:       provisionMode{preflight: true, apply: true, deploy: true},
			videoOnly:  *videoOnly,
			loggerOnly: *loggerOnly,
			sshKey:     *sshKey,
		})
	}
	applyDeployProcessEnv(env.Values)
	paths.VideoState = provisionCloudVideoStatePath(envRoot, env.Values["CLOUD_STACK_NAME"], paths.VideoState)
	operator, _ := readEnvFile(paths.OperatorEnv)
	if *binaryOnly && !*localBuild && strings.TrimSpace(*videoRelease) == "" {
		selected, objectKey, err := selectObjectRelease(operator, "Video Cloud", "rtk_video_cloud", "")
		if err != nil {
			return err
		}
		*videoRelease = selected
		fmt.Fprintf(os.Stderr, "[cloud-deploy] selected Video Cloud binary release: %s\n", selected)
		fmt.Fprintf(os.Stderr, "[cloud-deploy] Video Cloud release readable: %s\n", objectKey)
	}
	return deployAllServices(paths, env.Values, operator, provisionOptions{
		videoRelease:         *videoRelease,
		accountRelease:       *accountRelease,
		adminRelease:         *adminRelease,
		accountReleaseBundle: *accountBundle,
		adminReleaseBundle:   *adminBundle,
		localBuild:           *localBuild,
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

	if !opts.binaryOnly {
		runLoggerProvisionHooks(paths, env, opts.sshKey, report)
		runLoggerForwarderInstallHooks(paths, env, opts.sshKey, report)
		runLoggerReadinessHooks(paths, env, opts.sshKey, report)
	}
	if opts.loggerOnly {
		return report.write(true)
	}

	videoEnv := mergeEnv(operator, map[string]string{
		"VIDEO_CLOUD_LOG_LEVEL": logLevels["VIDEO_CLOUD_LOG_LEVEL"],
	})
	videoEnv = mergeEnv(videoEnv, certCacheEnv("LINODE_DEPLOY_CERT_CACHE_DIR", filepath.Join(paths.EnvRoot, "certificates", env["VIDEO_CLOUD_DOMAIN"])))
	videoArgs := videoDeployArgs(paths, env, opts)
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_video_cloud"), videoEnv, "linode_deploy/scripts/deploy-staging.sh", videoArgs...); err != nil {
		report.add("video-cloud-deploy-verify", "FAIL", "")
		report.add("cloud-admin-deploy", "SKIP", "video-cloud-deploy-verify")
		report.add("cloud-admin-verify", "SKIP", "video-cloud-deploy-verify")
		_ = report.write(false)
		return err
	}
	cacheVideoCertificateFromRemote(paths, env, opts.sshKey)
	report.add("video-cloud-deploy-verify", "PASS", "")
	if opts.videoOnly {
		if !opts.binaryOnly {
			runLoggerForwarderInstallHooks(paths, env, opts.sshKey, report)
			runLoggerReadinessHooks(paths, env, opts.sshKey, report)
		}
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
		"ACCOUNT_MANAGER_LOG_LEVEL":             logLevels["ACCOUNT_MANAGER_LOG_LEVEL"],
	})
	accountValues = mergeEnv(accountValues, certCacheEnv("ACCOUNT_MANAGER_LINODE_CERT_CACHE_DIR", filepath.Join(paths.EnvRoot, "certificates", env["ACCOUNT_MANAGER_DOMAIN"])))
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_account_manager"), accountValues, "linode_deploy/scripts/deploy-public-vm.sh"); err != nil {
		report.add("account-manager-deploy", "FAIL", "")
		_ = report.write(false)
		return err
	}
	report.add("account-manager-deploy", "PASS", "")
	_ = runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_account_manager"), accountValues, "linode_deploy/scripts/verify-public-vm.sh")

	adminBundle := opts.adminReleaseBundle
	if adminBundle == "" && opts.adminRelease != "" {
		var err error
		adminBundle, err = materializeReleaseBundle(reportDir, operator, "rtk_cloud_admin", opts.adminRelease)
		if err != nil {
			report.add("cloud-admin-deploy", "FAIL", "")
			_ = report.write(false)
			return err
		}
	}
	adminEnv, _ := readEnvFile(paths.AdminEnv)
	adminState, _ := readEnvFile(paths.AdminState)
	adminValues := mergeEnv(adminEnv, adminState)
	adminValues = mergeEnv(adminValues, map[string]string{
		"ADMIN_LINODE_RELEASE":            opts.adminRelease,
		"ADMIN_LINODE_RELEASE_BUNDLE":     adminBundle,
		"ACCOUNT_MANAGER_BASE_URL":        "https://" + env["ACCOUNT_MANAGER_DOMAIN"],
		"VIDEO_CLOUD_BASE_URL":            "https://" + env["VIDEO_CLOUD_DOMAIN"],
		"VIDEO_CLOUD_PROMETHEUS_BASE_URL": videoCloudPrometheusBaseURL(paths),
		"CLOUD_ADMIN_LOG_LEVEL":           logLevels["CLOUD_ADMIN_LOG_LEVEL"],
	})
	adminValues = mergeEnv(adminValues, certCacheEnv("ADMIN_LINODE_CERT_CACHE_DIR", filepath.Join(paths.EnvRoot, "certificates", env["CLOUD_ADMIN_DOMAIN"])))
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
		"--env-file", paths.OperatorEnv,
		"--gateway-domain", env["VIDEO_CLOUD_DOMAIN"],
	}
	if !opts.binaryOnly {
		args = append(args,
			"--secrets-file", paths.VideoEnv,
			"--certbot-extra-domain", env["VIDEO_CLOUD_CERTISSUER_DOMAIN"],
		)
	}
	if opts.videoRelease != "" {
		args = append(args, "--release", opts.videoRelease)
	}
	if opts.binaryOnly {
		args = append(args, "--binary-only")
	}
	if opts.localBuild {
		args = append(args, "--local-build")
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

func applyDeployProcessEnv(env map[string]string) {
	for _, key := range []string{"CLOUD_LOGGER_EMQX_VERBOSE_TRACE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			env[key] = value
		}
	}
}

func writePlatformAdminSummary(w io.Writer, paths provisionPaths) {
	adminEnv := filepath.Join(paths.EnvRoot, "services", "cloud-admin", "admin-staging.env")
	username := envFileValue(adminEnv, "ADMIN_BOOTSTRAP_EMAIL")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Cloud Admin platform login:")
	if username == "" {
		fmt.Fprintln(w, "- username: unavailable")
		fmt.Fprintf(w, "- password: see %s\n", adminEnv)
		return
	}
	fmt.Fprintf(w, "- username: %s\n", username)
	fmt.Fprintf(w, "- password: see %s\n", adminEnv)
	fmt.Fprintln(w, "- account-manager token: run ./stg.sh token")
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
	operatorEnv, _ := readEnvFile(paths.OperatorEnv)
	endpoint := firstNonEmpty(loggerEnv["CLOUD_LOGGER_ENDPOINT"], loggerState["CLOUD_LOGGER_ENDPOINT"], env["CLOUD_LOGGER_ENDPOINT"])
	token := firstNonEmpty(loggerEnv["CLOUD_LOGGER_INGEST_TOKEN"], loggerState["CLOUD_LOGGER_INGEST_TOKEN"])
	host := loggerState["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"]
	domain := firstNonEmpty(loggerState["CLOUD_LOGGER_DOMAIN"], loggerEnv["CLOUD_LOGGER_DOMAIN"], env["CLOUD_LOGGER_DOMAIN"])
	nodeExporterListenIP := firstNonEmpty(loggerState["CLOUD_LOGGER_LINODE_PRIVATE_IPV4"], prometheusTargetHost(paths.VideoConfig, "cloud_logger_node"))
	if endpoint == "" || token == "" || host == "" {
		return errors.New("logger endpoint, ingest token, and logger host are required")
	}
	if domain == "" {
		if parsed, err := url.Parse(endpoint); err == nil {
			domain = parsed.Hostname()
		}
	}
	dnsEnv, err := certbotDNS01Env(env, operatorEnv)
	if err != nil {
		return err
	}
	binary, cleanup, err := buildLoggerBackend(paths.Workspace)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := uploadLoggerBinary(paths, sshKey, host, binary, "/usr/local/bin/rtk-cloud-logger"); err != nil {
		return err
	}
	certCacheDir := ""
	if domain != "" {
		certCacheDir = filepath.Join(paths.EnvRoot, "certificates", domain)
	}
	cachedCert, err := uploadLoggerCachedCertificate(paths, sshKey, host, certCacheDir)
	if err != nil {
		return err
	}
	script := loggerBackendInstallScript(domain, token, firstNonEmpty(os.Getenv("CLOUD_LOGGER_LOKI_VERSION"), "v3.5.1"), cachedCert, nodeExporterListenIP)
	script = injectCertbotDNS01EnvScript(dnsEnv) + script
	if err := runCmdWithInput("", script, "ssh", loggerSSHArgs(paths, sshKey, host, "bash", "-s")...); err != nil {
		return err
	}
	if domain != "" {
		if err := cacheCertificateFromRemote(paths, sshKey, host, domain, certCacheDir, "cloud-logger"); err != nil {
			fmt.Fprintf(os.Stderr, "[cloud-deploy] logger certificate cache refresh skipped: %v\n", err)
		}
	}
	return nil
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

func uploadLoggerCachedCertificate(paths provisionPaths, sshKey, host, dir string) (bool, error) {
	if certCacheEnv("CLOUD_LOGGER_CERT_CACHE_DIR", dir) == nil {
		return false, nil
	}
	if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, host, "mkdir", "-p", "/tmp/rtk-cloud-logger-deploy/cert-cache")...); err != nil {
		return false, err
	}
	remote := "root@" + host + ":/tmp/rtk-cloud-logger-deploy/cert-cache/"
	if err := runExternal("scp", loggerSCPArgs(paths, sshKey, host, filepath.Join(dir, "fullchain.pem"), remote+"fullchain.pem")...); err != nil {
		return false, err
	}
	if err := runExternal("scp", loggerSCPArgs(paths, sshKey, host, filepath.Join(dir, "privkey.pem"), remote+"privkey.pem")...); err != nil {
		return false, err
	}
	fmt.Fprintf(os.Stderr, "[cloud-deploy] using cached certificate for logger backend: %s\n", filepath.Base(dir))
	return true, nil
}

type certificateCacheTarget struct {
	Name   string
	Host   string
	Domain string
	Dir    string
}

func refreshStagingCertificateCaches(paths provisionPaths, env map[string]string, sshKey string) []certificateCacheTarget {
	required := []certificateCacheTarget{}
	for _, target := range stagingCertificateCacheTargets(paths, env) {
		if target.Host == "" || target.Domain == "" {
			continue
		}
		present, err := remoteCertificatePresent(paths, sshKey, target.Host, target.Domain)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[cloud-e2e] certificate cache presence check skipped: target=%s domain=%s error=%v\n", target.Name, target.Domain, err)
			continue
		}
		if !present {
			fmt.Fprintf(os.Stderr, "[cloud-e2e] certificate cache refresh skipped: target=%s domain=%s remote_certificate=missing\n", target.Name, target.Domain)
			continue
		}
		if err := cacheCertificateFromRemote(paths, sshKey, target.Host, target.Domain, target.Dir, target.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[cloud-e2e] certificate cache refresh skipped: target=%s domain=%s error=%v\n", target.Name, target.Domain, err)
			required = append(required, target)
		}
	}
	return required
}

func requireStagingCertificateCaches(paths provisionPaths, env map[string]string) error {
	return requireStagingCertificateCachesForTargets(stagingCertificateCacheTargets(paths, env))
}

func requireStagingCertificateCachesForTargets(targets []certificateCacheTarget) error {
	missing := []string{}
	for _, target := range targets {
		if target.Domain == "" {
			continue
		}
		if target.Host == "" {
			continue
		}
		if certCacheEnv("CERT_CACHE", target.Dir) == nil {
			missing = append(missing, fmt.Sprintf("%s domain=%s cache=%s host=%s", target.Name, target.Domain, target.Dir, target.Host))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("staging HTTPS certificate cache is required before destructive e2e remove; missing or incomplete cache for: %s. Refresh from the existing VM first or restore fullchain.pem and privkey.pem under cloud_env/staging/linode/certificates before rerunning", strings.Join(missing, "; "))
}

func stagingCertificateCacheTargets(paths provisionPaths, env map[string]string) []certificateCacheTarget {
	logger := loggerProvisionTarget(paths, env)
	return []certificateCacheTarget{
		videoCertificateCacheTarget(paths, env),
		{
			Name:   "account-manager",
			Host:   envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"),
			Domain: env["ACCOUNT_MANAGER_DOMAIN"],
			Dir:    filepath.Join(paths.EnvRoot, "certificates", env["ACCOUNT_MANAGER_DOMAIN"]),
		},
		{
			Name:   "cloud-admin",
			Host:   envFileValue(paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4"),
			Domain: env["CLOUD_ADMIN_DOMAIN"],
			Dir:    filepath.Join(paths.EnvRoot, "certificates", env["CLOUD_ADMIN_DOMAIN"]),
		},
		{
			Name:   "cloud-logger",
			Host:   logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"],
			Domain: logger.Domain,
			Dir:    filepath.Join(paths.EnvRoot, "certificates", logger.Domain),
		},
	}
}

func cacheVideoCertificateFromRemote(paths provisionPaths, env map[string]string, sshKey string) {
	target := videoCertificateCacheTarget(paths, env)
	if target.Host == "" || target.Domain == "" {
		return
	}
	if err := cacheCertificateFromRemote(paths, sshKey, target.Host, target.Domain, target.Dir, target.Name); err != nil {
		fmt.Fprintf(os.Stderr, "[cloud-deploy] video certificate cache refresh skipped: %v\n", err)
	}
}

func videoCertificateCacheTarget(paths provisionPaths, env map[string]string) certificateCacheTarget {
	return certificateCacheTarget{
		Name:   "video-cloud",
		Host:   videoStateInstanceHost(paths.VideoState, "edge"),
		Domain: env["VIDEO_CLOUD_DOMAIN"],
		Dir:    filepath.Join(paths.EnvRoot, "certificates", env["VIDEO_CLOUD_DOMAIN"]),
	}
}

func cacheCertificateFromRemote(paths provisionPaths, sshKey, host, domain, dir, label string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	liveDir := "/etc/letsencrypt/live/" + domain
	fullchainTmp := filepath.Join(dir, ".fullchain.pem.tmp")
	privkeyTmp := filepath.Join(dir, ".privkey.pem.tmp")
	defer os.Remove(fullchainTmp)
	defer os.Remove(privkeyTmp)
	remote := "root@" + host + ":"
	if err := runExternal("scp", loggerSCPArgs(paths, sshKey, host, remote+liveDir+"/fullchain.pem", fullchainTmp)...); err != nil {
		return err
	}
	if err := runExternal("scp", loggerSCPArgs(paths, sshKey, host, remote+liveDir+"/privkey.pem", privkeyTmp)...); err != nil {
		return err
	}
	if err := os.Rename(fullchainTmp, filepath.Join(dir, "fullchain.pem")); err != nil {
		return err
	}
	if err := os.Rename(privkeyTmp, filepath.Join(dir, "privkey.pem")); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(dir, "privkey.pem"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-deploy] caching certificate: %s domain=%s\n", label, domain)
	return nil
}

func remoteCertificatePresent(paths provisionPaths, sshKey, host, domain string) (bool, error) {
	liveDir := "/etc/letsencrypt/live/" + domain
	check := fmt.Sprintf("test -s %q -a -s %q", liveDir+"/fullchain.pem", liveDir+"/privkey.pem")
	args := loggerSSHArgs(paths, sshKey, host, "sh", "-c", check)
	err := runCmdQuiet("ssh", args...)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func prometheusTargetHost(configPath, job string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	inJob := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- job:") {
			inJob = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- job:")), `"'`) == job
			continue
		}
		if !inJob || !strings.HasPrefix(trimmed, "address:") {
			continue
		}
		address := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "address:")), `"'`)
		host, _, ok := strings.Cut(address, ":")
		if !ok {
			return strings.TrimSpace(address)
		}
		return strings.TrimSpace(host)
	}
	return ""
}

func validMetricsListenIP(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && ch != '.' && ch != ':' {
			return false
		}
	}
	return true
}

func loggerBackendInstallScript(domain, token, lokiVersion string, cachedCert bool, nodeExporterListenIP string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "set -euo pipefail")
	fmt.Fprintln(&b, "export DEBIAN_FRONTEND=noninteractive")
	fmt.Fprintln(&b, "apt-get update")
	fmt.Fprintln(&b, "apt-get install -y nginx certbot curl unzip prometheus-node-exporter dnsutils")
	nodeExporterListenIP = strings.TrimSpace(nodeExporterListenIP)
	loggerListenAddr := "127.0.0.1:18090"
	if validMetricsListenIP(nodeExporterListenIP) {
		loggerListenAddr = "0.0.0.0:18090"
		fmt.Fprintf(&b, "printf 'ARGS=\"--web.listen-address=%s:9100\"\\n' > /etc/default/prometheus-node-exporter\n", nodeExporterListenIP)
	}
	fmt.Fprintln(&b, "install -d -m 0755 /etc/rtk-cloud /var/lib/loki /usr/local/libexec")
	writeCertbotDNS01Hooks(&b)
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
	fmt.Fprintf(&b, "ExecStart=/usr/local/bin/rtk-cloud-logger -addr %s\n", shellEnvValue(loggerListenAddr))
	fmt.Fprintln(&b, "Restart=always")
	fmt.Fprintln(&b, "RestartSec=5")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=multi-user.target")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "cat > /etc/nginx/sites-available/rtk-cloud-logger <<'EOF'")
	if domain != "" && cachedCert {
		writeLoggerHTTPSNginx(&b, domain)
	} else {
		fmt.Fprintln(&b, "# DNS-01 bootstrap config intentionally opens no HTTP listener.")
	}
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "ln -sf /etc/nginx/sites-available/rtk-cloud-logger /etc/nginx/sites-enabled/rtk-cloud-logger")
	fmt.Fprintln(&b, "rm -f /etc/nginx/sites-enabled/default")
	fmt.Fprintln(&b, "systemctl daemon-reload")
	fmt.Fprintln(&b, "systemctl enable loki.service rtk-cloud-logger.service nginx.service prometheus-node-exporter.service")
	fmt.Fprintln(&b, "systemctl restart loki.service rtk-cloud-logger.service")
	fmt.Fprintln(&b, "systemctl restart prometheus-node-exporter.service")
	fmt.Fprintln(&b, "systemctl reload-or-restart nginx.service")
	fmt.Fprintln(&b, "systemctl is-active prometheus-node-exporter.service")
	if validMetricsListenIP(nodeExporterListenIP) {
		fmt.Fprintf(&b, "ss -lnt | grep %s\n", shellEnvValue(nodeExporterListenIP+":9100"))
	}
	if domain != "" {
		if cachedCert {
			fmt.Fprintf(&b, "domain=%s\n", shellEnvValue(domain))
			fmt.Fprintln(&b, "archive_dir=/etc/letsencrypt/archive/$domain")
			fmt.Fprintln(&b, "live_dir=/etc/letsencrypt/live/$domain")
			fmt.Fprintln(&b, "install -d -m 0755 \"$archive_dir\" \"$live_dir\" /etc/letsencrypt/renewal")
			fmt.Fprintln(&b, "install -m 0644 /tmp/rtk-cloud-logger-deploy/cert-cache/fullchain.pem \"$archive_dir/fullchain1.pem\"")
			fmt.Fprintln(&b, "openssl x509 -in /tmp/rtk-cloud-logger-deploy/cert-cache/fullchain.pem -out \"$archive_dir/cert1.pem\"")
			fmt.Fprintln(&b, "awk 'BEGIN{n=0} /-----BEGIN CERTIFICATE-----/{n++} n>1{print}' /tmp/rtk-cloud-logger-deploy/cert-cache/fullchain.pem > \"$archive_dir/chain1.pem\"")
			fmt.Fprintln(&b, "[ -s \"$archive_dir/chain1.pem\" ] || cp \"$archive_dir/fullchain1.pem\" \"$archive_dir/chain1.pem\"")
			fmt.Fprintln(&b, "install -m 0600 /tmp/rtk-cloud-logger-deploy/cert-cache/privkey.pem \"$archive_dir/privkey1.pem\"")
			fmt.Fprintln(&b, "ln -sfn \"../../archive/$domain/cert1.pem\" \"$live_dir/cert.pem\"")
			fmt.Fprintln(&b, "ln -sfn \"../../archive/$domain/chain1.pem\" \"$live_dir/chain.pem\"")
			fmt.Fprintln(&b, "ln -sfn \"../../archive/$domain/fullchain1.pem\" \"$live_dir/fullchain.pem\"")
			fmt.Fprintln(&b, "ln -sfn \"../../archive/$domain/privkey1.pem\" \"$live_dir/privkey.pem\"")
			writeCertbotRenewalConf(&b, "$domain", false)
			fmt.Fprintln(&b, "certbot register --non-interactive --agree-tos --register-unsafely-without-email >/dev/null 2>&1 || true")
			fmt.Fprintln(&b, "nginx -t")
			fmt.Fprintln(&b, "systemctl reload nginx.service")
			fmt.Fprintln(&b, "systemctl enable --now certbot.timer")
			fmt.Fprintf(&b, "printf 'installed cached certificate lineage for %%s\\n' %s\n", shellEnvValue(domain))
		} else {
			fmt.Fprintf(&b, "certbot certonly --manual --preferred-challenges dns --manual-auth-hook /usr/local/libexec/rtk-cloud-certbot-dns-auth --manual-cleanup-hook /usr/local/libexec/rtk-cloud-certbot-dns-cleanup --manual-public-ip-logging-ok --non-interactive --agree-tos --register-unsafely-without-email -d %s\n", shellEnvValue(domain))
			fmt.Fprintln(&b, "cat > /etc/nginx/sites-available/rtk-cloud-logger <<'EOF'")
			writeLoggerHTTPSNginx(&b, domain)
			fmt.Fprintln(&b, "EOF")
			fmt.Fprintln(&b, "nginx -t")
			fmt.Fprintln(&b, "systemctl reload nginx.service")
			fmt.Fprintln(&b, "systemctl enable --now certbot.timer")
		}
	}
	return b.String()
}

func writeLoggerHTTPSNginx(b *strings.Builder, domain string) {
	fmt.Fprintln(b, "server {")
	fmt.Fprintln(b, "  listen 443 ssl;")
	if domain != "" {
		fmt.Fprintf(b, "  server_name %s;\n", shellEnvValue(domain))
	} else {
		fmt.Fprintln(b, "  server_name _;")
	}
	if domain != "" {
		fmt.Fprintf(b, "  ssl_certificate /etc/letsencrypt/live/%s/fullchain.pem;\n", domain)
		fmt.Fprintf(b, "  ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;\n", domain)
	}
	fmt.Fprintln(b, "  location / {")
	fmt.Fprintln(b, "    proxy_pass http://127.0.0.1:18090;")
	fmt.Fprintln(b, "    proxy_set_header Host $host;")
	fmt.Fprintln(b, "    proxy_set_header X-Forwarded-Proto https;")
	fmt.Fprintln(b, "    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;")
	fmt.Fprintln(b, "  }")
	fmt.Fprintln(b, "}")
}

func writeCertbotRenewalConf(b *strings.Builder, domainExpr string, quoted bool) {
	target := "\"/etc/letsencrypt/renewal/" + domainExpr + ".conf\""
	if quoted {
		target = shellEnvValue("/etc/letsencrypt/renewal/" + domainExpr + ".conf")
	}
	fmt.Fprintf(b, "cat > %s <<EOF\n", target)
	fmt.Fprintln(b, "version = 2.9.0")
	fmt.Fprintf(b, "archive_dir = /etc/letsencrypt/archive/%s\n", domainExpr)
	fmt.Fprintf(b, "cert = /etc/letsencrypt/live/%s/cert.pem\n", domainExpr)
	fmt.Fprintf(b, "privkey = /etc/letsencrypt/live/%s/privkey.pem\n", domainExpr)
	fmt.Fprintf(b, "chain = /etc/letsencrypt/live/%s/chain.pem\n", domainExpr)
	fmt.Fprintf(b, "fullchain = /etc/letsencrypt/live/%s/fullchain.pem\n", domainExpr)
	fmt.Fprintln(b)
	fmt.Fprintln(b, "[renewalparams]")
	fmt.Fprintln(b, "account =")
	fmt.Fprintln(b, "authenticator = manual")
	fmt.Fprintln(b, "pref_challs = dns-01")
	fmt.Fprintln(b, "manual_auth_hook = /usr/local/libexec/rtk-cloud-certbot-dns-auth")
	fmt.Fprintln(b, "manual_cleanup_hook = /usr/local/libexec/rtk-cloud-certbot-dns-cleanup")
	fmt.Fprintln(b, "manual_public_ip_logging_ok = True")
	fmt.Fprintln(b, "server = https://acme-v02.api.letsencrypt.org/directory")
	fmt.Fprintln(b, "key_type = rsa")
	fmt.Fprintln(b, "deploy_hook = systemctl reload nginx")
	fmt.Fprintln(b, "EOF")
}

func writeCertbotDNS01Hooks(b *strings.Builder) {
	fmt.Fprintln(b, "cat > /usr/local/libexec/rtk-cloud-certbot-dns-auth <<'EOF'")
	fmt.Fprintln(b, certbotDNSAuthHookScript())
	fmt.Fprintln(b, "EOF")
	fmt.Fprintln(b, "cat > /usr/local/libexec/rtk-cloud-certbot-dns-cleanup <<'EOF'")
	fmt.Fprintln(b, certbotDNSCleanupHookScript())
	fmt.Fprintln(b, "EOF")
	fmt.Fprintln(b, "chmod 0755 /usr/local/libexec/rtk-cloud-certbot-dns-auth /usr/local/libexec/rtk-cloud-certbot-dns-cleanup")
}

type certbotDNS01EnvValues struct {
	Key                string
	Secret             string
	Env                string
	RootDomain         string
	TTL                string
	WaitSeconds        string
	PropagationSeconds string
	Resolvers          string
}

func certbotDNS01Env(stackEnv, operatorEnv map[string]string) (certbotDNS01EnvValues, error) {
	values := certbotDNS01EnvValues{
		Key:                firstNonEmpty(operatorEnv["GODADDY_KEY"], operatorEnv["GODADDY_API_KEY"], os.Getenv("GODADDY_KEY"), os.Getenv("GODADDY_API_KEY")),
		Secret:             firstNonEmpty(operatorEnv["GODADDY_SECRET"], operatorEnv["GODADDY_API_SECRET"], os.Getenv("GODADDY_SECRET"), os.Getenv("GODADDY_API_SECRET")),
		Env:                firstNonEmpty(operatorEnv["GODADDY_ENV"], os.Getenv("GODADDY_ENV"), "prod"),
		RootDomain:         firstNonEmpty(stackEnv["CLOUD_DNS_ROOT_DOMAIN"], operatorEnv["CLOUD_DNS_ROOT_DOMAIN"], os.Getenv("CLOUD_DNS_ROOT_DOMAIN")),
		TTL:                firstNonEmpty(operatorEnv["GODADDY_DNS_TTL"], operatorEnv["GODADDY_RECORD_TTL"], os.Getenv("GODADDY_DNS_TTL"), os.Getenv("GODADDY_RECORD_TTL"), "600"),
		WaitSeconds:        firstNonEmpty(operatorEnv["GODADDY_DNS_WAIT_SECONDS"], os.Getenv("GODADDY_DNS_WAIT_SECONDS"), "300"),
		PropagationSeconds: firstNonEmpty(operatorEnv["GODADDY_DNS_PROPAGATION_SECONDS"], os.Getenv("GODADDY_DNS_PROPAGATION_SECONDS"), "60"),
		Resolvers:          firstNonEmpty(operatorEnv["GODADDY_DNS_RESOLVERS"], os.Getenv("GODADDY_DNS_RESOLVERS"), "8.8.8.8 1.1.1.1 9.9.9.9"),
	}
	var missing []string
	if values.Key == "" {
		missing = append(missing, "GODADDY_KEY")
	}
	if values.Secret == "" {
		missing = append(missing, "GODADDY_SECRET")
	}
	if values.RootDomain == "" {
		missing = append(missing, "CLOUD_DNS_ROOT_DOMAIN")
	}
	if len(missing) > 0 {
		return certbotDNS01EnvValues{}, fmt.Errorf("GoDaddy DNS-01 credentials missing: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

func injectCertbotDNS01EnvScript(values certbotDNS01EnvValues) string {
	var b strings.Builder
	fmt.Fprintln(&b, "install -d -m 0755 /etc/rtk-cloud")
	fmt.Fprintln(&b, "cat > /etc/rtk-cloud/godaddy-dns.env <<'EOF'")
	fmt.Fprintf(&b, "GODADDY_KEY=%s\n", shellEnvValue(values.Key))
	fmt.Fprintf(&b, "GODADDY_SECRET=%s\n", shellEnvValue(values.Secret))
	fmt.Fprintf(&b, "GODADDY_ENV=%s\n", shellEnvValue(values.Env))
	fmt.Fprintf(&b, "CLOUD_DNS_ROOT_DOMAIN=%s\n", shellEnvValue(values.RootDomain))
	fmt.Fprintf(&b, "GODADDY_DNS_TTL=%s\n", shellEnvValue(values.TTL))
	fmt.Fprintf(&b, "GODADDY_DNS_WAIT_SECONDS=%s\n", shellEnvValue(values.WaitSeconds))
	fmt.Fprintf(&b, "GODADDY_DNS_PROPAGATION_SECONDS=%s\n", shellEnvValue(values.PropagationSeconds))
	fmt.Fprintf(&b, "GODADDY_DNS_RESOLVERS=%s\n", shellEnvValue(values.Resolvers))
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "chmod 0600 /etc/rtk-cloud/godaddy-dns.env")
	return b.String()
}

func certbotDNSAuthHookScript() string {
	return `#!/usr/bin/env bash
set -euo pipefail
. /etc/rtk-cloud/godaddy-dns.env
: "${GODADDY_KEY:?GODADDY_KEY is required}"
: "${GODADDY_SECRET:?GODADDY_SECRET is required}"
: "${CLOUD_DNS_ROOT_DOMAIN:?CLOUD_DNS_ROOT_DOMAIN is required}"
: "${CERTBOT_DOMAIN:?CERTBOT_DOMAIN is required}"
: "${CERTBOT_VALIDATION:?CERTBOT_VALIDATION is required}"
zone="${CLOUD_DNS_ROOT_DOMAIN%.}"
domain="${CERTBOT_DOMAIN%.}"
case "$domain" in
  "$zone") relative="" ;;
  *."$zone") relative="${domain%.$zone}" ;;
  *) echo "CERTBOT_DOMAIN $domain is outside zone $zone" >&2; exit 1 ;;
esac
record="_acme-challenge"
if [ -n "$relative" ]; then
  record="$record.$relative"
fi
ttl="${GODADDY_DNS_TTL:-600}"
api_root="https://api.godaddy.com"
if [ "${GODADDY_ENV:-prod}" != "prod" ]; then
  api_root="https://api.ote-godaddy.com"
fi
payload="$(CERTBOT_VALIDATION="$CERTBOT_VALIDATION" GODADDY_DNS_TTL="$ttl" python3 - <<'PY'
import json, os
print(json.dumps([{"data": os.environ["CERTBOT_VALIDATION"], "ttl": int(os.environ["GODADDY_DNS_TTL"])}]))
PY
)"
curl -fsS -X PUT "$api_root/v1/domains/$zone/records/TXT/$record" \
  -H "Authorization: sso-key $GODADDY_KEY:$GODADDY_SECRET" \
  -H "Content-Type: application/json" \
  --data "$payload" >/dev/null
fqdn="$record.$zone"
deadline=$((SECONDS + ${GODADDY_DNS_WAIT_SECONDS:-300}))
resolvers="${GODADDY_DNS_RESOLVERS:-8.8.8.8 1.1.1.1 9.9.9.9}"
propagation_seconds="${GODADDY_DNS_PROPAGATION_SECONDS:-60}"
while [ "$SECONDS" -lt "$deadline" ]; do
  found=1
  for resolver in $resolvers; do
    if ! dig +short TXT "$fqdn" "@$resolver" | tr -d '"' | grep -Fx "$CERTBOT_VALIDATION" >/dev/null; then
      found=0
      break
    fi
  done
  if [ "$found" = "1" ]; then
    sleep "$propagation_seconds"
    exit 0
  fi
  sleep 10
done
echo "DNS TXT validation did not propagate for $fqdn" >&2
exit 1`
}

func certbotDNSCleanupHookScript() string {
	return `#!/usr/bin/env bash
set -euo pipefail
. /etc/rtk-cloud/godaddy-dns.env
: "${GODADDY_KEY:?GODADDY_KEY is required}"
: "${GODADDY_SECRET:?GODADDY_SECRET is required}"
: "${CLOUD_DNS_ROOT_DOMAIN:?CLOUD_DNS_ROOT_DOMAIN is required}"
: "${CERTBOT_DOMAIN:?CERTBOT_DOMAIN is required}"
zone="${CLOUD_DNS_ROOT_DOMAIN%.}"
domain="${CERTBOT_DOMAIN%.}"
case "$domain" in
  "$zone") relative="" ;;
  *."$zone") relative="${domain%.$zone}" ;;
  *) exit 0 ;;
esac
record="_acme-challenge"
if [ -n "$relative" ]; then
  record="$record.$relative"
fi
api_root="https://api.godaddy.com"
if [ "${GODADDY_ENV:-prod}" != "prod" ]; then
  api_root="https://api.ote-godaddy.com"
fi
curl -fsS -X DELETE "$api_root/v1/domains/$zone/records/TXT/$record" \
  -H "Authorization: sso-key $GODADDY_KEY:$GODADDY_SECRET" >/dev/null || true`
}

type loggerForwarderTarget struct {
	name  string
	host  string
	units string
}

func runLoggerForwarderInstallHooks(paths provisionPaths, env map[string]string, sshKey string, report *readinessReport) {
	targets := loggerForwarderTargets(paths)
	if script := os.Getenv("CLOUD_LOGGER_SCRIPT"); script != "" {
		runLoggerForwarderScriptHooks(paths, env, script, targets, report)
		return
	}
	installNativeLoggerForwarders(paths, env, sshKey, targets, report)
}

func runLoggerForwarderScriptHooks(paths provisionPaths, env map[string]string, script string, targets []loggerForwarderTarget, report *readinessReport) {
	loggerEnv := filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env")
	loggerState := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	for _, target := range targets {
		args := []string{"install-forwarder", target.name, "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--host", target.host, "--units", target.units, "--journald-system-max-use", "512M", "--journald-system-keep-free", "1G", "--journald-max-retention-sec", "604800"}
		if err := runCmdWithEnv(paths.Workspace, nil, script, args...); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
		}
	}
	if emqxVerboseTraceEnabled(env) {
		mqtt := loggerForwarderTargetByName(targets, "mqtt")
		args := []string{"install-forwarder", "emqx-broker-trace", "--workspace", paths.Workspace, "--env-root", paths.EnvRoot, "--logger-env", loggerEnv, "--logger-state", loggerState, "--host", mqtt.host, "--emqx-docker-container", "video-cloud-emqx", "--service", "emqx-broker", "--source", "emqx", "--component", "mqtt-broker", "--operation-id", "mqtt-broker-trace"}
		if err := runCmdWithEnv(paths.Workspace, nil, script, args...); err != nil {
			report.add("logger-forwarder:emqx-broker-trace", "DEGRADED", "")
		}
	}
}

func loggerForwarderTargets(paths provisionPaths) []loggerForwarderTarget {
	return []loggerForwarderTarget{
		{"account-manager", envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"), "rtk-account-manager.service,rtk-account-manager-inbox-worker.service,rtk-account-manager-outbox-worker.service"},
		{"video-cloud-api", videoStatePrivateHost(paths.VideoState, "api"), "video_cloud-api.service,video_cloud-logingester.service,video_cloud-turnregistry.service,video_cloud-metricsexporter.service,video_cloud-cleaner.service,video_cloud-statistics.service,video_cloud-certissuer.service,video_cloud-factoryenroll.service"},
		{"cloud-admin", envFileValue(paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4"), "rtk-cloud-admin.service"},
		{"edge", videoStatePublicHost(paths.VideoState, "edge"), "nginx.service,certbot.timer"},
		{"infra", videoStatePrivateHost(paths.VideoState, "infra"), "postgresql.service,postgresql@16-main.service,redis-server.service,prometheus.service,prometheus-node-exporter.service,prometheus-postgres-exporter.service,prometheus-redis-exporter.service"},
		{"mqtt", videoStatePrivateHost(paths.VideoState, "mqtt"), "emqx.service"},
		{"coturn", videoStatePublicHost(paths.VideoState, "coturn"), "coturn.service,video_cloud-turnregistrar.service"},
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
		if err := waitForLoggerForwarderSSH(paths, sshKey, target); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
			fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder install degraded: target=%s host=%s readiness_error=%v\n", target.name, target.host, err)
			continue
		}
		if err := installNativeLoggerForwarderTarget(paths, sshKey, binary, endpoint, token, loggerHostIP, proxyURL, target); err != nil {
			report.add("logger-forwarder:"+target.name, "DEGRADED", "")
			fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder install degraded: target=%s error=%v\n", target.name, err)
		}
		if target.name == "mqtt" && emqxVerboseTraceEnabled(env) {
			if err := installNativeLoggerEMQXForwarderTarget(paths, sshKey, binary, endpoint, token, loggerHostIP, proxyURL, firstNonEmpty(env["CLOUD_ENV_NAME"], "staging"), target); err != nil {
				report.add("logger-forwarder:emqx-broker-trace", "DEGRADED", "")
				fmt.Fprintf(os.Stderr, "[cloud-deploy] EMQX verbose broker trace forwarder degraded: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "[cloud-deploy] EMQX verbose broker trace forwarding enabled: service=emqx-broker source=emqx operation_id=mqtt-broker-trace")
			}
		}
	}
}

func waitForLoggerForwarderSSH(paths provisionPaths, sshKey string, target loggerForwarderTarget) error {
	if target.host == "" {
		return fmt.Errorf("logger forwarder target host missing: %s", target.name)
	}
	attempts := envInt("CLOUD_LOGGER_FORWARDER_SSH_READY_ATTEMPTS", 60)
	delay := time.Duration(envInt("CLOUD_LOGGER_FORWARDER_SSH_READY_DELAY_SEC", 10)) * time.Second
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder SSH readiness attempt %d/%d: target=%s host=%s\n", attempt, attempts, target.name, target.host)
		if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, target.host, "true")...); err == nil {
			fmt.Fprintf(os.Stderr, "[cloud-deploy] logger forwarder SSH ready: target=%s host=%s\n", target.name, target.host)
			return nil
		} else {
			lastErr = err
		}
		if attempt < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("ssh did not become ready after %d attempts: %w", attempts, lastErr)
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

func installNativeLoggerEMQXForwarderTarget(paths provisionPaths, sshKey, binary, endpoint, token, loggerHostIP, proxyURL, envName string, target loggerForwarderTarget) error {
	if target.host == "" {
		return fmt.Errorf("logger forwarder target host missing: %s", target.name)
	}
	if err := uploadLoggerBinary(paths, sshKey, target.host, binary, "/usr/local/bin/rtk-cloud-log-forwarder"); err != nil {
		return err
	}
	script := loggerEMQXForwarderInstallScript(endpoint, token, loggerHostIP, proxyURL, envName)
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

func loggerEMQXForwarderInstallScript(endpoint, token, loggerHostIP, proxyURL, envName string) string {
	var b strings.Builder
	loggerHost := ""
	if parsed, err := url.Parse(endpoint); err == nil {
		loggerHost = parsed.Hostname()
	}
	fmt.Fprintln(&b, "set -euo pipefail")
	fmt.Fprintln(&b, "install -d -m 0755 /etc/rtk-cloud /var/lib/rtk-cloud-logger/emqx-spool")
	if loggerHost != "" && loggerHostIP != "" {
		fmt.Fprintf(&b, "sed -i.bak '/[[:space:]]%s$/d' /etc/hosts\n", shellEnvValue(loggerHost))
		fmt.Fprintf(&b, "printf '%%s %%s\\n' %s %s >> /etc/hosts\n", shellEnvValue(loggerHostIP), shellEnvValue(loggerHost))
	}
	fmt.Fprintln(&b, "cat > /etc/rtk-cloud/emqx-log-forwarder.env <<'EOF'")
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_INGEST_URL=%s\n", shellEnvValue(loggerIngestURL(endpoint)))
	fmt.Fprintf(&b, "RTK_CLOUD_LOGGER_TOKEN=%s\n", shellEnvValue(token))
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_EMQX_DOCKER_CONTAINER=video-cloud-emqx")
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_CURSOR=/var/lib/rtk-cloud-logger/emqx-docker.cursor")
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_SPOOL_DIR=/var/lib/rtk-cloud-logger/emqx-spool")
	fmt.Fprintln(&b, "RTK_CLOUD_LOGGER_INITIAL_SINCE=5m")
	fmt.Fprintln(&b, "SERVICE=emqx-broker")
	fmt.Fprintf(&b, "ENV=%s\n", shellEnvValue(firstNonEmpty(envName, "staging")))
	fmt.Fprintln(&b, "VERSION=emqx")
	if proxyURL != "" {
		fmt.Fprintf(&b, "HTTPS_PROXY=%s\n", shellEnvValue(proxyURL))
		fmt.Fprintf(&b, "HTTP_PROXY=%s\n", shellEnvValue(proxyURL))
		fmt.Fprintln(&b, "NO_PROXY=localhost,127.0.0.1,10.42.0.0/16")
	}
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "chmod 0600 /etc/rtk-cloud/emqx-log-forwarder.env")
	fmt.Fprintln(&b, "if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' | grep -qx video-cloud-emqx; then")
	fmt.Fprintln(&b, "  docker exec video-cloud-emqx sh -lc 'emqx ctl log set-level debug || emqx ctl log primary-level debug || true' >/dev/null 2>&1 || true")
	fmt.Fprintln(&b, "fi")
	fmt.Fprintln(&b, "cat > /etc/systemd/system/rtk-cloud-emqx-log-forwarder.service <<'EOF'")
	fmt.Fprintln(&b, "[Unit]")
	fmt.Fprintln(&b, "Description=RTK Cloud EMQX verbose broker log forwarder")
	fmt.Fprintln(&b, "After=network-online.target docker.service")
	fmt.Fprintln(&b, "Wants=network-online.target docker.service")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Service]")
	fmt.Fprintln(&b, "EnvironmentFile=/etc/rtk-cloud/emqx-log-forwarder.env")
	fmt.Fprintln(&b, "ExecStart=/usr/local/bin/rtk-cloud-log-forwarder")
	fmt.Fprintln(&b, "Restart=always")
	fmt.Fprintln(&b, "RestartSec=5")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=multi-user.target")
	fmt.Fprintln(&b, "EOF")
	fmt.Fprintln(&b, "systemctl daemon-reload")
	fmt.Fprintln(&b, "systemctl enable rtk-cloud-emqx-log-forwarder.service")
	fmt.Fprintln(&b, "systemctl restart rtk-cloud-emqx-log-forwarder.service")
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
		"-o", "UserKnownHostsFile=/dev/null",
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
		"-o", "UserKnownHostsFile=/dev/null",
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
		"-o", "UserKnownHostsFile=/dev/null",
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
	if emqxVerboseTraceEnabled(env) {
		checks = append(checks, []string{"forwarder-status", "emqx-broker-trace"})
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
		if emqxVerboseTraceEnabled(env) {
			report.add("logger-forwarder:emqx-broker-trace", "DEGRADED", "")
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
	if emqxVerboseTraceEnabled(env) {
		target := loggerForwarderTargetByName(loggerForwarderTargets(paths), "mqtt")
		if target.host == "" {
			report.add("logger-forwarder:emqx-broker-trace", "DEGRADED", "")
		} else if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, target.host, "systemctl", "is-active", "--quiet", "rtk-cloud-emqx-log-forwarder.service")...); err != nil {
			report.add("logger-forwarder:emqx-broker-trace", "DEGRADED", "")
		} else {
			report.add("logger-forwarder:emqx-broker-trace", "PASS", "")
		}
	}
}

func loggerForwarderTargetByName(targets []loggerForwarderTarget, name string) loggerForwarderTarget {
	for _, target := range targets {
		if target.name == name {
			return target
		}
	}
	return loggerForwarderTarget{}
}

func emqxVerboseTraceEnabled(env map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(env["CLOUD_LOGGER_EMQX_VERBOSE_TRACE"])) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func loggerHTTP(endpoint, token, method, path, body string) error {
	_, err := loggerHTTPOutput(endpoint, token, method, path, body)
	return err
}

func loggerHTTPOutput(endpoint, token, method, path, body string) ([]byte, error) {
	cmd := exec.Command("curl", loggerHTTPArgs(endpoint, token, method, path, body)...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return out, err
	}
	return out, nil
}

func loggerHTTPArgs(endpoint, token, method, path, body string) []string {
	args := []string{
		"-fsS",
		"--connect-timeout", "5",
		"--max-time", "15",
		"-X", method,
		strings.TrimRight(endpoint, "/") + path,
		"-H", "Authorization: Bearer " + token,
		"-H", "Content-Type: application/json",
	}
	if body != "" {
		args = append(args, "--data-binary", body)
	}
	return args
}

func certCacheEnv(key, dir string) map[string]string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "fullchain.pem")); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "privkey.pem")); err != nil {
		return nil
	}
	return map[string]string{key: dir}
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

func videoStatePrivateHost(path, role string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		Instances map[string]struct {
			PrivateIP string `json:"private_ip"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.Instances[role].PrivateIP
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
