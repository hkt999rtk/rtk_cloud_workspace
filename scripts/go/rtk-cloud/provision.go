package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type provisionMode struct {
	preflight bool
	plan      bool
	reset     bool
	apply     bool
	dns       bool
	deploy    bool
	artifacts bool
	e2e       bool
}

type provisionPaths struct {
	Workspace           string
	EnvRoot             string
	OperatorEnv         string
	VideoConfig         string
	VideoEnv            string
	AccountManagerEnv   string
	AdminEnv            string
	VideoState          string
	AccountManagerState string
	AdminState          string
	ArtifactsDir        string
}

type provisionOptions struct {
	mode                 provisionMode
	workspace            string
	envRoot              string
	operatorEnv          string
	sshKey               string
	dnsRoot              string
	dnsRootExplicit      bool
	artifactDir          string
	videoRelease         string
	accountRelease       string
	accountReleaseBundle string
	adminRelease         string
	adminReleaseBundle   string
	localBuild           bool
	loggerOnly           bool
	videoOnly            bool
	binaryOnly           bool
	confirm              string
	verbose              bool
}

func runProvision(args []string) error {
	opts, err := parseProvisionArgs(args)
	if err != nil {
		return err
	}
	workspace := opts.workspace
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, opts.envRoot)
	if err != nil {
		return err
	}
	paths := newProvisionPaths(workspace, envRoot, opts)
	if opts.artifactDir != "" {
		paths.ArtifactsDir = opts.artifactDir
	}
	dnsOverride := ""
	if opts.dnsRootExplicit {
		dnsOverride = opts.dnsRoot
	}
	env, err := envroot.Load(envRoot, dnsOverride)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			env = envroot.Environment{Values: defaultProvisionEnvValues()}
		} else {
			return err
		}
	}
	if err := mergeSharedRuntimeEnvDefaults(envRoot, env.Values); err != nil {
		return err
	}
	if err := mergeObjectStorageCredentialDefaults(envRoot, env.Values); err != nil {
		return err
	}
	provider, err := newCloudProvider(env.Values["CLOUD_PROVIDER"])
	if err != nil {
		return err
	}
	ctx := provisionContext{Paths: paths, Env: env.Values, Opts: opts}
	if provider.Runtime() == provisionRuntimeKubernetes {
		return provider.RunProvision(ctx)
	}
	return provider.RunProvision(ctx)
}

func mergeSharedRuntimeEnvDefaults(envRoot string, values map[string]string) error {
	if filepath.Base(envRoot) != "lke" {
		return nil
	}
	runtimeRoot := filepath.Join(filepath.Dir(envRoot), "runtime")
	if _, err := os.Stat(filepath.Join(runtimeRoot, "env", "stack.env")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	runtimeEnv, err := envroot.Load(runtimeRoot, "")
	if err != nil {
		return fmt.Errorf("load shared runtime environment: %w", err)
	}
	for _, key := range []string{
		"VIDEO_CLOUD_BLOB_ENDPOINT",
		"VIDEO_CLOUD_BLOB_REGION",
		"VIDEO_CLOUD_BLOB_BUCKET",
		"VIDEO_CLOUD_BLOB_PREFIX",
		"VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED",
		"VIDEO_CLOUD_CLIP_VERIFIER_ADDR",
		"VIDEO_CLOUD_CLIP_UPLOAD_URL_TTL",
		"VIDEO_CLOUD_CLIP_UPLOAD_SESSION_TTL",
		"VIDEO_CLOUD_CLIP_UPLOAD_MAX_BYTES",
		"VIDEO_CLOUD_CLIP_THUMBNAIL_MAX_BYTES",
		"VIDEO_CLOUD_CLIP_VERIFY_POLL_INTERVAL",
		"VIDEO_CLOUD_CLIP_VERIFY_SWEEP_INTERVAL",
	} {
		if strings.TrimSpace(values[key]) == "" {
			values[key] = runtimeEnv.Values[key]
		}
	}
	return nil
}

func mergeObjectStorageCredentialDefaults(envRoot string, values map[string]string) error {
	candidates := []string{
		filepath.Join(envRoot, "env", "operator.env"),
	}
	if filepath.Base(envRoot) == "lke" {
		stagingRoot := filepath.Dir(envRoot)
		candidates = append(candidates,
			filepath.Join(stagingRoot, "runtime", "env", "operator.env"),
			filepath.Join(stagingRoot, "linode", "env", "operator.env"),
		)
	}
	for _, path := range candidates {
		operator, err := readEnvFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("load object-storage operator credentials: %w", err)
		}
		for _, key := range []string{
			"LINODE_OBJ_ACCESS_KEY_ID",
			"LINODE_OBJ_SECRET_ACCESS_KEY",
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
		} {
			if strings.TrimSpace(values[key]) == "" {
				values[key] = operator[key]
			}
		}
	}
	if values["LINODE_OBJ_ACCESS_KEY_ID"] == "" {
		values["LINODE_OBJ_ACCESS_KEY_ID"] = values["AWS_ACCESS_KEY_ID"]
	}
	if values["LINODE_OBJ_SECRET_ACCESS_KEY"] == "" {
		values["LINODE_OBJ_SECRET_ACCESS_KEY"] = values["AWS_SECRET_ACCESS_KEY"]
	}
	return nil
}

func defaultProvisionEnvValues() map[string]string {
	return envroot.Derive(map[string]string{
		"CLOUD_ENV_NAME":        "staging",
		"CLOUD_PROVIDER":        firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), os.Getenv("RTK_CLOUD_STAGING_PROVIDER"), "lke"),
		"CLOUD_REGION":          "us-sea",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
	})
}

func parseProvisionArgs(args []string) (provisionOptions, error) {
	opts := provisionOptions{
		dnsRoot: "realtekconnect.com",
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--preflight":
			opts.mode.preflight = true
		case "--plan":
			opts.mode.plan = true
		case "--reset":
			opts.mode.reset = true
		case "--apply":
			opts.mode.apply = true
		case "--dns":
			opts.mode.dns = true
		case "--deploy":
			opts.mode.deploy = true
		case "--artifacts":
			opts.mode.artifacts = true
		case "--e2e":
			opts.mode.e2e = true
		case "--all":
			opts.mode = provisionMode{preflight: true, plan: true, apply: true, dns: true, deploy: true, artifacts: true, e2e: true}
		case "--reset-and-all":
			opts.mode = provisionMode{preflight: true, plan: true, reset: true, apply: true, dns: true, deploy: true, artifacts: true, e2e: true}
		case "--workspace":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.workspace = v
		case "--env-root", "--secrets-root":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.envRoot = v
		case "--operator-env":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.operatorEnv = v
		case "--ssh-key":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.sshKey = v
		case "--dns-root-domain":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.dnsRoot = v
			opts.dnsRootExplicit = true
		case "--artifact-dir":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.artifactDir = v
		case "--video-release":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.videoRelease = v
		case "--account-release":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.accountRelease = v
		case "--account-release-bundle":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.accountReleaseBundle = v
		case "--admin-release":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.adminRelease = v
		case "--admin-release-bundle":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.adminReleaseBundle = v
		case "--confirm":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.confirm = v
		case "--verbose":
			opts.verbose = true
		case "-h", "--help":
			printProvisionUsage()
			return opts, flag.ErrHelp
		default:
			return opts, fmt.Errorf("unknown provision argument: %s", arg)
		}
	}
	if !opts.mode.preflight && !opts.mode.plan && !opts.mode.reset && !opts.mode.apply && !opts.mode.dns && !opts.mode.deploy && !opts.mode.artifacts && !opts.mode.e2e {
		opts.mode = provisionMode{preflight: true, plan: true, apply: true, dns: true, deploy: true, artifacts: true, e2e: true}
	}
	if opts.envRoot == "" {
		return opts, errors.New("--env-root is required")
	}
	return opts, nil
}

func printProvisionUsage() {
	fmt.Fprint(os.Stdout, `Usage:
  rtk-cloud provision --env-root cloud_env/staging [--all|--plan|--apply|--deploy|--artifacts]

Default:
  no mode flags is the same as --all.
`)
}

func newProvisionPaths(workspace, root string, opts provisionOptions) provisionPaths {
	paths := envroot.NewPaths(root)
	return provisionPaths{
		Workspace:           workspace,
		EnvRoot:             root,
		OperatorEnv:         firstNonEmpty(opts.operatorEnv, paths.OperatorEnv),
		VideoConfig:         paths.VideoConfig,
		VideoEnv:            paths.VideoEnv,
		AccountManagerEnv:   paths.AccountManagerEnv,
		AdminEnv:            paths.AdminEnv,
		VideoState:          paths.VideoState,
		AccountManagerState: paths.AccountManagerState,
		AdminState:          paths.AdminState,
		ArtifactsDir:        paths.ArtifactsDir,
	}
}

func mergeEnv(base map[string]string, overlay map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func runCmdWithEnv(dir string, env map[string]string, name string, args ...string) error {
	isGo := name == "go"
	if name == "go" && os.Getenv("RTK_CLOUD_GO") != "" {
		name = os.Getenv("RTK_CLOUD_GO")
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for k, v := range env {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if isGo {
		cmd.Env = append(cmd.Env, "GOWORK=off")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s in %s: %w", name, strings.Join(args, " "), dir, err)
	}
	return nil
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeStateVar(path, key, value string) error {
	values, _ := readEnvFile(path)
	if values == nil {
		values = map[string]string{}
	}
	values[key] = value
	return writeEnvMap(path, values, 0o600)
}

func writeEnvMap(path string, values map[string]string, perm os.FileMode) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), perm)
}

func redactEnvValues(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		if sensitiveEnvKey(key) {
			out[key] = "REDACTED"
		} else {
			out[key] = value
		}
	}
	return out
}

func sensitiveEnvKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, item := range []string{"token", "password", "secret", "credential", "private_key", "access_key"} {
		if strings.Contains(normalized, item) {
			return true
		}
	}
	return false
}

func videoSubnetID(paths provisionPaths) string {
	video, err := readJSONMap(paths.VideoState)
	if err != nil {
		return ""
	}
	return stringValue(video["subnet_id"])
}

func provisionVPCCIDR(paths provisionPaths) string {
	return firstNonEmpty(envroot.YAMLPathValue(paths.VideoConfig, "vpc.subnet.cidr"), "10.42.1.0/24")
}

func accountManagerPrivateIPv4(paths provisionPaths) string {
	return firstNonEmpty(envFileValue(paths.AccountManagerEnv, "ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"), envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"), "10.42.1.50")
}

func adminPrivateIPv4(paths provisionPaths) string {
	return firstNonEmpty(envFileValue(paths.AdminEnv, "ADMIN_LINODE_PRIVATE_IPV4"), envFileValue(paths.AdminState, "ADMIN_LINODE_PRIVATE_IPV4"), "10.42.1.60")
}

func loggerPrivateIPv4(paths provisionPaths) string {
	return firstNonEmpty(
		envFileValue(filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env"), "CLOUD_LOGGER_LINODE_PRIVATE_IPV4"),
		envFileValue(filepath.Join(paths.EnvRoot, "state", "cloud-logger.env"), "CLOUD_LOGGER_LINODE_PRIVATE_IPV4"),
		prometheusTargetHost(paths.VideoConfig, "cloud_logger_node"),
		"10.42.1.90",
	)
}

func videoCloudPrometheusBaseURL(paths provisionPaths) string {
	if value := envFileValue(paths.AdminEnv, "VIDEO_CLOUD_PROMETHEUS_BASE_URL"); value != "" {
		return value
	}
	video, err := readJSONMap(paths.VideoState)
	if err != nil {
		return "http://10.42.1.30:9090"
	}
	instances, _ := video["instances"].(map[string]any)
	infra, _ := instances["infra"].(map[string]any)
	return "http://" + firstNonEmpty(stringValue(infra["private_ip"]), "10.42.1.30") + ":9090"
}

func atoiOrZero(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func asSlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}
