package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	godaddyEnv           string
	dnsWaitTTL           string
	dnsFinalTTL          string
	dnsWaitMaxSeconds    string
	artifactDir          string
	videoRelease         string
	accountRelease       string
	accountReleaseBundle string
	adminRelease         string
	adminReleaseBundle   string
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
	if opts.mode.artifacts && !opts.mode.preflight && !opts.mode.plan && !opts.mode.reset && !opts.mode.apply && !opts.mode.dns && !opts.mode.deploy && !opts.mode.e2e {
		dir, err := writeProvisionArtifacts(paths, "")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, dir)
		return nil
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
	if _, statErr := os.Stat(filepath.Join(envRoot, "env", "stack.env")); statErr == nil {
		if err := envroot.Validate(envRoot, env); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if opts.operatorEnv == "" {
		opts.operatorEnv = paths.OperatorEnv
	}
	loadProvisionOperatorToken(paths)
	if opts.sshKey == "" {
		opts.sshKey = filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")
	}
	envValues := env.Values
	paths.VideoState = provisionCloudVideoStatePath(envRoot, envValues["CLOUD_STACK_NAME"], paths.VideoState)
	if opts.mode.preflight {
		if err := provisionPreflight(paths, &opts); err != nil {
			return err
		}
	}
	if opts.mode.apply || opts.mode.dns || opts.mode.deploy || opts.mode.e2e {
		if err := ensureProvisionRuntimeContracts(paths, envValues); err != nil {
			return err
		}
	}
	if opts.mode.plan {
		if err := provisionPlan(paths, envValues); err != nil {
			return err
		}
	}
	if opts.mode.reset {
		return errors.New("native provision reset is not implemented; use remove-all-vm for explicit teardown")
	}
	if opts.mode.apply {
		if err := provisionApply(paths, envValues, opts); err != nil {
			return err
		}
		if !opts.mode.deploy {
			fmt.Fprintln(os.Stderr, "[cloud-provision] apply complete without deploy; service runtimes were not installed. Run ./stg.sh provision --deploy with explicit releases, or use ./stg.sh provision/--all for the full automated path before brand/user/device steps.")
		}
	}
	if opts.mode.dns {
		if err := provisionDNS(paths, envValues, opts); err != nil {
			return err
		}
	}
	if opts.mode.deploy {
		if err := provisionDeploy(paths, envValues, opts); err != nil {
			return err
		}
	}
	if opts.mode.artifacts {
		dir, err := writeProvisionArtifacts(paths, envValues["CLOUD_STACK_NAME"])
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, dir)
	}
	if opts.mode.e2e {
		if err := provisionE2E(paths, envValues); err != nil {
			return err
		}
	}
	return nil
}

func defaultProvisionEnvValues() map[string]string {
	return envroot.Derive(map[string]string{
		"CLOUD_ENV_NAME":        "staging",
		"CLOUD_PROVIDER":        "linode",
		"CLOUD_REGION":          "us-sea",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
	})
}

func parseProvisionArgs(args []string) (provisionOptions, error) {
	opts := provisionOptions{
		dnsRoot:           "realtekconnect.com",
		godaddyEnv:        "prod",
		dnsWaitTTL:        firstNonEmpty(os.Getenv("GODADDY_WAIT_TTL"), os.Getenv("GODADDY_RECORD_WAIT_TTL"), "600"),
		dnsFinalTTL:       firstNonEmpty(os.Getenv("GODADDY_RECORD_TTL"), "600"),
		dnsWaitMaxSeconds: firstNonEmpty(os.Getenv("DNS_WAIT_MAX_SECONDS"), "700"),
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
		case "--godaddy-env":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.godaddyEnv = v
		case "--dns-wait-ttl":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.dnsWaitTTL = v
		case "--dns-final-ttl", "--dns-ttl":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.dnsFinalTTL = v
		case "--dns-wait-max-seconds":
			v, err := next()
			if err != nil {
				return opts, err
			}
			opts.dnsWaitMaxSeconds = v
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
	return opts, validateProvisionNumericOptions(opts)
}

func validateProvisionNumericOptions(opts provisionOptions) error {
	for name, value := range map[string]string{
		"--dns-wait-ttl":            opts.dnsWaitTTL,
		"--dns-final-ttl":           opts.dnsFinalTTL,
		"--dns-wait-max-seconds":    opts.dnsWaitMaxSeconds,
		"DNS_WAIT_INTERVAL_SECONDS": firstNonEmpty(os.Getenv("DNS_WAIT_INTERVAL_SECONDS"), "10"),
	} {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s must be a positive integer", name)
		}
		if (name == "--dns-wait-ttl" || name == "--dns-final-ttl") && n < 600 {
			return fmt.Errorf("%s must be >= 600 for GoDaddy DNS records", name)
		}
	}
	return nil
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

func provisionPreflight(paths provisionPaths, opts *provisionOptions) error {
	for _, cmd := range []string{"curl", "jq", "ssh", "openssl", "go", "tar"} {
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf("%s is required", cmd)
		}
	}
	for _, path := range []string{paths.VideoConfig, paths.VideoEnv, paths.AccountManagerEnv, paths.AdminEnv, opts.sshKey, opts.sshKey + ".pub"} {
		if path == "" {
			return errors.New("provision path resolved empty")
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	operator, _ := readEnvFile(paths.OperatorEnv)
	if firstNonEmpty(os.Getenv("LINODE_TOKEN"), operator["LINODE_TOKEN"]) == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	if err := resolveProvisionReleases(paths, operator, opts); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "[rtk-cloud provision] preflight ok")
	return nil
}

func provisionPlan(paths provisionPaths, env map[string]string) error {
	logger := loggerProvisionTarget(paths, env)
	fmt.Fprintln(os.Stdout, "Target instances:")
	if raw, err := curlLinodeQuiet("GET", "/linode/instances?page_size=500", ""); err == nil {
		var listed struct {
			Data []struct {
				Label string `json:"label"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &listed) == nil {
			for _, item := range listed.Data {
				if item.Label != "" {
					fmt.Fprintf(os.Stdout, "- %s\n", item.Label)
				}
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "[rtk-cloud provision] warning: cannot list Linode target instances: %v\n", err)
	}
	fmt.Fprintln(os.Stdout, "Intended resources:")
	fmt.Fprintf(os.Stdout, "- instances: %s-edge/api/infra/mqtt/coturn, %s, %s\n", env["VIDEO_CLOUD_LABEL_PREFIX"], env["ACCOUNT_MANAGER_LINODE_LABEL"], env["ADMIN_LINODE_LABEL"])
	fmt.Fprintf(os.Stdout, "- firewalls: %s-edge/api/infra/mqtt/coturn, %s, %s\n", env["VIDEO_CLOUD_LABEL_PREFIX"], env["ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL"], env["ADMIN_LINODE_FIREWALL_LABEL"])
	fmt.Fprintf(os.Stdout, "- vpc/subnet: %s / %s\n", env["VIDEO_CLOUD_VPC_LABEL"], env["VIDEO_CLOUD_SUBNET_LABEL"])
	fmt.Fprintf(os.Stdout, "- dns: %s, %s, %s, %s\n", env["VIDEO_CLOUD_DOMAIN"], env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], env["ACCOUNT_MANAGER_DOMAIN"], env["CLOUD_ADMIN_DOMAIN"])
	fmt.Fprintf(os.Stdout, "- cloud-admin private IP: %s\n", adminPrivateIPv4(paths))
	for _, item := range loggerProvisionStatuses(logger) {
		fmt.Fprintf(os.Stdout, "- logger %s: %s [%s]\n", item.kind, item.value, item.status)
	}
	fmt.Fprintln(os.Stdout, "- forwarder targets: edge, api, infra, mqtt, coturn, account-manager, cloud-admin, frontend, non-go-host-sources")
	fmt.Fprintln(os.Stdout, "- journald retention: SystemMaxUse=1G SystemKeepFree=2G MaxRetentionSec=7day")
	return nil
}

func loadProvisionOperatorToken(paths provisionPaths) {
	if os.Getenv("LINODE_TOKEN") != "" {
		return
	}
	operator, _ := readEnvFile(paths.OperatorEnv)
	if operator["LINODE_TOKEN"] != "" {
		_ = os.Setenv("LINODE_TOKEN", operator["LINODE_TOKEN"])
	}
}

func provisionApply(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	operator, _ := readEnvFile(paths.OperatorEnv)
	if token := firstNonEmpty(os.Getenv("LINODE_TOKEN"), operator["LINODE_TOKEN"]); token != "" {
		_ = os.Setenv("LINODE_TOKEN", token)
	}
	currentCIDR, err := currentPublicIPv4CIDR()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] staging automation will replace SSH whitelist with current operator CIDR: %s\n", currentCIDR)
	updateLocalSSHWhitelistInputs(paths.EnvRoot, currentCIDR, false)
	repoState := provisionRepoVideoStatePath(paths, env["CLOUD_STACK_NAME"])
	stateUsable := provisionVideoStateHasInstances(paths.VideoState) || provisionVideoStateHasInstances(repoState)
	if stateUsable {
		if err := syncProvisionVideoState(paths.VideoState, repoState); err != nil {
			return err
		}
		if err := hydrateProvisionVideoState(paths, env, repoState); err != nil {
			return err
		}
	} else if err := cleanupOrphanedProvisionState(paths, env); err != nil {
		return err
	}
	if err := runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_video_cloud", "linode_deploy"), mergeEnv(operator, map[string]string{}), "go", "run", "./cmd/linode-deploy", "apply", "--config", paths.VideoConfig); err != nil {
		return err
	}
	if err := syncProvisionVideoStateFromRepo(paths.VideoState, repoState); err != nil {
		return err
	}
	if err := provisionPublicService(paths, env, "account-manager"); err != nil {
		return err
	}
	if err := ensureProvisionVPCInterface(paths, "Account Manager", paths.AccountManagerState, "ACCOUNT_MANAGER", envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID"), accountManagerPrivateIPv4(paths)); err != nil {
		return err
	}
	if err := provisionPublicService(paths, env, "cloud-admin"); err != nil {
		return err
	}
	if err := ensureProvisionVPCInterface(paths, "Cloud Admin", paths.AdminState, "ADMIN", envFileValue(paths.AdminState, "ADMIN_LINODE_ID"), adminPrivateIPv4(paths)); err != nil {
		return err
	}
	if err := provisionLoggerResource(paths, env, opts); err != nil {
		return err
	}
	return replaceLiveSSHWhitelist(paths.EnvRoot, currentCIDR)
}

type loggerTarget struct {
	Label         string
	FirewallLabel string
	Domain        string
	Endpoint      string
	EnvPath       string
	StatePath     string
	State         map[string]string
}

type loggerStatus struct {
	kind   string
	value  string
	status string
}

func loggerProvisionTarget(paths provisionPaths, env map[string]string) loggerTarget {
	envName := firstNonEmpty(env["CLOUD_ENV_NAME"], "staging")
	domain := firstNonEmpty(env["CLOUD_LOGGER_DOMAIN"], "logger."+env["VIDEO_CLOUD_DOMAIN"], "logger."+env["CLOUD_DNS_ROOT_DOMAIN"])
	if domain == "logger." {
		domain = "logger.video-cloud-staging.realtekconnect.com"
	}
	statePath := filepath.Join(paths.EnvRoot, "state", "cloud-logger.env")
	state, _ := readEnvFile(statePath)
	return loggerTarget{
		Label:         firstNonEmpty(env["CLOUD_LOGGER_LINODE_LABEL"], "rtk-cloud-logger-"+envName),
		FirewallLabel: firstNonEmpty(env["CLOUD_LOGGER_LINODE_FIREWALL_LABEL"], "rtk-cloud-logger-"+envName+"-firewall"),
		Domain:        domain,
		Endpoint:      firstNonEmpty(env["CLOUD_LOGGER_ENDPOINT"], "https://"+domain),
		EnvPath:       filepath.Join(paths.EnvRoot, "services", "cloud-logger", "logger.env"),
		StatePath:     statePath,
		State:         state,
	}
}

func loggerProvisionStatuses(target loggerTarget) []loggerStatus {
	vmStatus := "missing"
	if target.State["CLOUD_LOGGER_LINODE_ID"] != "" {
		vmStatus = "provisioned"
	}
	firewallStatus := "missing"
	if target.State["CLOUD_LOGGER_LINODE_FIREWALL_ID"] != "" {
		firewallStatus = "provisioned"
	}
	dnsStatus := "missing"
	if target.State["CLOUD_LOGGER_DNS_RECORD"] != "" {
		dnsStatus = "provisioned"
	}
	envStatus := "missing"
	if exists(target.EnvPath) {
		envStatus = "provisioned"
	}
	stateStatus := "missing"
	if exists(target.StatePath) {
		stateStatus = "provisioned"
	}
	return []loggerStatus{
		{"VM", target.Label, vmStatus},
		{"firewall", target.FirewallLabel, firewallStatus},
		{"DNS", target.Domain, dnsStatus},
		{"env", target.EnvPath, envStatus},
		{"state", target.StatePath, stateStatus},
	}
}

func provisionLoggerResource(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	target := loggerProvisionTarget(paths, env)
	state := target.State
	if state == nil {
		state = map[string]string{}
	}
	if state["CLOUD_LOGGER_INGEST_TOKEN"] == "" {
		token, err := randomURLToken(32)
		if err != nil {
			return err
		}
		state["CLOUD_LOGGER_INGEST_TOKEN"] = token
	}
	if state["CLOUD_LOGGER_LINODE_ID"] == "" {
		vm, err := ensureLoggerVM(target, opts)
		if err != nil {
			return err
		}
		state["CLOUD_LOGGER_LINODE_ID"] = strconv.Itoa(vm.ID)
		state["CLOUD_LOGGER_LINODE_LABEL"] = target.Label
		state["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"] = vm.PublicIPv4
	}
	if state["CLOUD_LOGGER_LINODE_FIREWALL_ID"] == "" {
		firewallID, err := ensureLoggerFirewall(target, state, opts)
		if err != nil {
			return err
		}
		state["CLOUD_LOGGER_LINODE_FIREWALL_ID"] = strconv.Itoa(firewallID)
		state["CLOUD_LOGGER_LINODE_FIREWALL_LABEL"] = target.FirewallLabel
	}
	state["CLOUD_LOGGER_DOMAIN"] = target.Domain
	state["CLOUD_LOGGER_ENDPOINT"] = target.Endpoint
	if state["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"] != "" {
		state["CLOUD_LOGGER_DNS_RECORD"] = target.Domain
	}
	if err := writeEnvMap(target.StatePath, state, 0o600); err != nil {
		return err
	}
	envValues := map[string]string{
		"CLOUD_LOGGER_DOMAIN":       target.Domain,
		"CLOUD_LOGGER_ENDPOINT":     target.Endpoint,
		"CLOUD_LOGGER_INGEST_TOKEN": state["CLOUD_LOGGER_INGEST_TOKEN"],
	}
	return writeEnvMap(target.EnvPath, envValues, 0o600)
}

type loggerVM struct {
	ID         int
	PublicIPv4 string
}

func ensureLoggerVM(target loggerTarget, opts provisionOptions) (loggerVM, error) {
	if existing, err := findLinodeInstance(target.Label); err != nil {
		return loggerVM{}, err
	} else if existing.ID != 0 {
		return existing, nil
	}
	publicKeyPath := opts.sshKey + ".pub"
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return loggerVM{}, err
	}
	rootPass, err := randomPassword()
	if err != nil {
		return loggerVM{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"label":           target.Label,
		"region":          firstNonEmpty(os.Getenv("CLOUD_LOGGER_LINODE_REGION"), "us-sea"),
		"type":            firstNonEmpty(os.Getenv("CLOUD_LOGGER_LINODE_TYPE"), "g6-standard-2"),
		"image":           firstNonEmpty(os.Getenv("CLOUD_LOGGER_LINODE_IMAGE"), "linode/ubuntu24.04"),
		"root_pass":       rootPass,
		"authorized_keys": []string{strings.TrimSpace(string(publicKey))},
		"tags":            []string{"rtk-cloud", "cloud-logger", "staging"},
	})
	raw, err := curlLinode("POST", "/linode/instances", string(payload))
	if err != nil {
		return loggerVM{}, err
	}
	return parseLoggerVM(raw)
}

func ensureLoggerFirewall(target loggerTarget, state map[string]string, opts provisionOptions) (int, error) {
	if existing, err := findLinodeFirewallID(target.FirewallLabel); err != nil {
		return 0, err
	} else if existing != 0 {
		return existing, attachLoggerFirewall(existing, state)
	}
	currentCIDR, err := currentPublicIPv4CIDR()
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]any{
		"label": target.FirewallLabel,
		"rules": map[string]any{
			"inbound_policy":  "DROP",
			"outbound_policy": "ACCEPT",
			"inbound": []map[string]any{
				{"label": "ssh", "action": "ACCEPT", "protocol": "TCP", "ports": "22", "addresses": map[string]any{"ipv4": []string{currentCIDR}}},
				{"label": "http", "action": "ACCEPT", "protocol": "TCP", "ports": "80", "addresses": map[string]any{"ipv4": []string{"0.0.0.0/0"}}},
				{"label": "https", "action": "ACCEPT", "protocol": "TCP", "ports": "443", "addresses": map[string]any{"ipv4": []string{"0.0.0.0/0"}}},
			},
			"outbound": []any{},
		},
	})
	raw, err := curlLinode("POST", "/networking/firewalls", string(payload))
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return 0, err
	}
	if created.ID == 0 {
		return 0, errors.New("logger firewall creation did not return an id")
	}
	return created.ID, attachLoggerFirewall(created.ID, state)
}

func attachLoggerFirewall(firewallID int, state map[string]string) error {
	vmID := atoiOrZero(state["CLOUD_LOGGER_LINODE_ID"])
	if firewallID == 0 || vmID == 0 {
		return nil
	}
	device, _ := json.Marshal(map[string]any{"id": vmID, "type": "linode"})
	_, err := curlLinode("POST", fmt.Sprintf("/networking/firewalls/%d/devices", firewallID), string(device))
	return err
}

func findLinodeInstance(label string) (loggerVM, error) {
	if label == "" {
		return loggerVM{}, nil
	}
	raw, err := curlLinode("GET", "/linode/instances?page_size=500", "")
	if err != nil {
		return loggerVM{}, err
	}
	var listed struct {
		Data []struct {
			ID    int      `json:"id"`
			Label string   `json:"label"`
			IPv4  []string `json:"ipv4"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return loggerVM{}, err
	}
	for _, item := range listed.Data {
		if item.Label == label {
			publicIP := ""
			if len(item.IPv4) > 0 {
				publicIP = item.IPv4[0]
			}
			return loggerVM{ID: item.ID, PublicIPv4: publicIP}, nil
		}
	}
	return loggerVM{}, nil
}

func findLinodeFirewallID(label string) (int, error) {
	if label == "" {
		return 0, nil
	}
	raw, err := curlLinode("GET", "/networking/firewalls?page_size=500", "")
	if err != nil {
		return 0, err
	}
	var listed struct {
		Data []struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return 0, err
	}
	for _, item := range listed.Data {
		if item.Label == label {
			return item.ID, nil
		}
	}
	return 0, nil
}

func parseLoggerVM(raw []byte) (loggerVM, error) {
	var vm struct {
		ID   int      `json:"id"`
		IPv4 []string `json:"ipv4"`
	}
	if err := json.Unmarshal(raw, &vm); err != nil {
		return loggerVM{}, err
	}
	if vm.ID == 0 {
		return loggerVM{}, errors.New("logger VM creation did not return an id")
	}
	publicIP := ""
	if len(vm.IPv4) > 0 {
		publicIP = vm.IPv4[0]
	}
	return loggerVM{ID: vm.ID, PublicIPv4: publicIP}, nil
}

func randomURLToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func cleanupOrphanedProvisionState(paths provisionPaths, env map[string]string) error {
	legacyVideoState := filepath.Join(paths.Workspace, "repos", "rtk_video_cloud", "linode_deploy", "state", "video-cloud-staging.state.json")
	if _, err := os.Stat(legacyVideoState); err == nil {
		backupDir := filepath.Join(paths.ArtifactsDir, "legacy-state-backup-"+time.Now().UTC().Format("20060102T150405Z"))
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		if err := os.Rename(legacyVideoState, filepath.Join(backupDir, filepath.Base(legacyVideoState))); err != nil {
			return err
		}
	}
	if raw, err := curlLinodeQuiet("GET", "/networking/firewalls?page_size=500", ""); err == nil {
		var listed struct {
			Data []struct {
				ID    int      `json:"id"`
				Label string   `json:"label"`
				Tags  []string `json:"tags"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &listed) == nil {
			for _, item := range listed.Data {
				if item.ID == 0 {
					continue
				}
				if item.Label == env["ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL"] || item.Label == env["ADMIN_LINODE_FIREWALL_LABEL"] || strings.HasPrefix(item.Label, env["VIDEO_CLOUD_LABEL_PREFIX"]+"-") {
					_, _ = curlLinodeQuiet("DELETE", fmt.Sprintf("/networking/firewalls/%d", item.ID), "")
				}
			}
		}
	}
	if raw, err := curlLinodeQuiet("GET", "/vpcs?page_size=500", ""); err == nil {
		var listed struct {
			Data []struct {
				ID    int    `json:"id"`
				Label string `json:"label"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &listed) == nil {
			for _, item := range listed.Data {
				if item.ID != 0 && item.Label == env["VIDEO_CLOUD_VPC_LABEL"] {
					_, _ = curlLinodeQuiet("DELETE", fmt.Sprintf("/vpcs/%d", item.ID), "")
				}
			}
		}
	}
	_ = os.Remove(paths.VideoState)
	return nil
}

func provisionCloudVideoStatePath(envRoot, stack, fallback string) string {
	if stack == "" {
		return fallback
	}
	stackPath := filepath.Join(envRoot, "state", stack+".state.json")
	if exists(stackPath) {
		return stackPath
	}
	if exists(fallback) {
		return fallback
	}
	return stackPath
}

func provisionRepoVideoStatePath(paths provisionPaths, stack string) string {
	if stack == "" {
		stack = "video-cloud-staging"
	}
	return filepath.Join(paths.Workspace, "repos", "rtk_video_cloud", "linode_deploy", "state", stack+".state.json")
}

func syncProvisionVideoState(cloudState, repoState string) error {
	switch {
	case exists(cloudState) && !exists(repoState):
		return copyFile(cloudState, repoState)
	case exists(repoState) && !exists(cloudState):
		return copyFile(repoState, cloudState)
	default:
		return nil
	}
}

func syncProvisionVideoStateFromRepo(cloudState, repoState string) error {
	if !exists(repoState) {
		return nil
	}
	return copyFile(repoState, cloudState)
}

func provisionVideoStateHasInstances(path string) bool {
	state, err := readJSONMap(path)
	if err != nil {
		return false
	}
	instances, _ := state["instances"].(map[string]any)
	return len(instances) > 0
}

func uniqueNonEmpty(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func hydrateProvisionVideoState(paths provisionPaths, env map[string]string, repoState string) error {
	for _, statePath := range uniqueNonEmpty(paths.VideoState, repoState) {
		if !exists(statePath) {
			continue
		}
		state, err := readJSONMap(statePath)
		if err != nil {
			return err
		}
		changed := false
		if atoiOrZero(stringValue(state["vpc_id"])) == 0 {
			vpcID, err := findLinodeVPCID(env["VIDEO_CLOUD_VPC_LABEL"])
			if err != nil {
				return err
			}
			if vpcID != 0 {
				state["vpc_id"] = vpcID
				changed = true
			}
		}
		vpcID := atoiOrZero(stringValue(state["vpc_id"]))
		if atoiOrZero(stringValue(state["subnet_id"])) == 0 && vpcID != 0 {
			subnetID, err := findLinodeSubnetID(vpcID, env["VIDEO_CLOUD_SUBNET_LABEL"])
			if err != nil {
				return err
			}
			if subnetID != 0 {
				state["subnet_id"] = subnetID
				changed = true
			}
		}
		firewalls, _ := state["firewalls"].(map[string]any)
		if firewalls == nil {
			firewalls = map[string]any{}
		}
		missingFirewall := false
		for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
			if atoiOrZero(stringValue(firewalls[role])) == 0 {
				missingFirewall = true
				break
			}
		}
		if missingFirewall {
			found, err := findVideoFirewallIDs(env["VIDEO_CLOUD_LABEL_PREFIX"])
			if err != nil {
				return err
			}
			for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
				if atoiOrZero(stringValue(firewalls[role])) == 0 && found[role] != 0 {
					firewalls[role] = found[role]
					changed = true
				}
			}
			state["firewalls"] = firewalls
		}
		if changed {
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				return err
			}
			if err := writeJSON(statePath, state); err != nil {
				return err
			}
		}
	}
	return syncProvisionVideoState(paths.VideoState, repoState)
}

func findLinodeVPCID(label string) (int, error) {
	if label == "" {
		return 0, nil
	}
	raw, err := curlLinode("GET", "/vpcs?page_size=500", "")
	if err != nil {
		return 0, err
	}
	var listed struct {
		Data []struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return 0, err
	}
	for _, item := range listed.Data {
		if item.Label == label {
			return item.ID, nil
		}
	}
	return 0, fmt.Errorf("state is missing vpc_id and Linode VPC label was not found: %s", label)
}

func findLinodeSubnetID(vpcID int, label string) (int, error) {
	if vpcID == 0 || label == "" {
		return 0, nil
	}
	raw, err := curlLinode("GET", fmt.Sprintf("/vpcs/%d/subnets?page_size=500", vpcID), "")
	if err != nil {
		return 0, err
	}
	var listed struct {
		Data []struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return 0, err
	}
	for _, item := range listed.Data {
		if item.Label == label {
			return item.ID, nil
		}
	}
	return 0, fmt.Errorf("state is missing subnet_id and Linode subnet label was not found: %s", label)
}

func findVideoFirewallIDs(labelPrefix string) (map[string]int, error) {
	out := map[string]int{}
	if labelPrefix == "" {
		return out, nil
	}
	raw, err := curlLinode("GET", "/networking/firewalls?page_size=500", "")
	if err != nil {
		return nil, err
	}
	var listed struct {
		Data []struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	for _, item := range listed.Data {
		for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
			if item.Label == labelPrefix+"-"+role {
				out[role] = item.ID
			}
		}
	}
	return out, nil
}

func provisionPublicService(paths provisionPaths, env map[string]string, service string) error {
	switch service {
	case "account-manager":
		if envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID") != "" {
			return nil
		}
		values, _ := readEnvFile(paths.AccountManagerEnv)
		values["LINODE_TOKEN"] = os.Getenv("LINODE_TOKEN")
		values["ACCOUNT_MANAGER_LINODE_VPC_SUBNET_ID"] = videoSubnetID(paths)
		values["ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"] = accountManagerPrivateIPv4(paths)
		values["ACCOUNT_MANAGER_LINODE_VPC_CIDR"] = provisionVPCCIDR(paths)
		values["ACCOUNT_MANAGER_LINODE_STATE_PATH"] = paths.AccountManagerState
		return runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_account_manager"), mergeEnv(values, nil), "linode_deploy/scripts/provision-public-vm.sh")
	case "cloud-admin":
		if envFileValue(paths.AdminState, "ADMIN_LINODE_ID") != "" {
			return nil
		}
		values, _ := readEnvFile(paths.AdminEnv)
		values["LINODE_TOKEN"] = os.Getenv("LINODE_TOKEN")
		values["ADMIN_LINODE_VPC_SUBNET_ID"] = videoSubnetID(paths)
		values["ADMIN_LINODE_PRIVATE_IPV4"] = adminPrivateIPv4(paths)
		values["ADMIN_LINODE_VPC_CIDR"] = provisionVPCCIDR(paths)
		values["ADMIN_LINODE_STATE_PATH"] = paths.AdminState
		return runCmdWithEnv(filepath.Join(paths.Workspace, "repos", "rtk_cloud_admin"), mergeEnv(values, nil), "deploy/linode/provision-admin-vm.sh")
	default:
		return fmt.Errorf("unknown service: %s", service)
	}
}

func provisionDeploy(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	operator, _ := readEnvFile(paths.OperatorEnv)
	return deployAllServices(paths, env, operator, opts)
}

func ensureProvisionRuntimeContracts(paths provisionPaths, env map[string]string) error {
	if err := ensureProvisionDeviceMTLSIngress(paths, env); err != nil {
		return err
	}
	return ensureProvisionAccountManagerInternalAuth(paths)
}

func ensureProvisionDeviceMTLSIngress(paths provisionPaths, env map[string]string) error {
	videoDomain := firstNonEmpty(env["VIDEO_CLOUD_DOMAIN"], "video-cloud-staging.realtekconnect.com")
	deviceDomain := "device." + videoDomain
	if strings.HasPrefix(videoDomain, "video-cloud-") {
		deviceDomain = "device." + videoDomain
	}
	keysDir := filepath.Join(paths.Workspace, "keys", "staging", "linode", "video-cloud")
	bundlePath := filepath.Join(keysDir, "device-app-client-ca-bundle.pem")
	rootCA := filepath.Join(keysDir, "root-ca.ed25519.cert.pem")
	deviceIssuer := filepath.Join(keysDir, "production-issuer.ed25519.cert.pem")
	appIssuer := filepath.Join(keysDir, "app-user-issuer.ed25519.cert.pem")
	if err := ensurePEMBundle(bundlePath, []string{rootCA, deviceIssuer, appIssuer}); err != nil {
		return err
	}
	if err := setYAMLScalar(paths.VideoConfig, "device_client_domain", deviceDomain); err != nil {
		return err
	}
	return setYAMLScalar(paths.VideoConfig, "device_client_ca_cert_path", bundlePath)
}

func ensureProvisionAccountManagerInternalAuth(paths provisionPaths) error {
	accountEnv, _ := readEnvFile(paths.AccountManagerEnv)
	videoEnv, _ := readEnvFile(paths.VideoEnv)
	token := firstNonEmpty(accountEnv["ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"], videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"])
	if token == "" {
		generated, err := randomHex(32)
		if err != nil {
			return err
		}
		token = generated
	}
	accountEnv["ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"] = token
	accountHost := firstNonEmpty(envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"), accountManagerPrivateIPv4(paths), "10.42.1.50")
	videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL"] = "http://" + accountHost + ":18081"
	videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"] = token
	if strings.TrimSpace(videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT"]) == "" {
		videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT"] = "10s"
	}
	if err := writeEnvMap(paths.AccountManagerEnv, accountEnv, 0o600); err != nil {
		return err
	}
	return writeEnvMap(paths.VideoEnv, videoEnv, 0o600)
}

func ensurePEMBundle(bundlePath string, certPaths []string) error {
	var b strings.Builder
	for _, path := range certPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read CA certificate for device mTLS bundle: %w", err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return fmt.Errorf("CA certificate for device mTLS bundle is empty: %s", path)
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(bundlePath, []byte(b.String()), 0o644)
}

func setYAMLScalar(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	prefix := key + ":"
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + key + ": " + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+": "+value)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func yamlScalarValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + ":"
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), `"'`)
		}
	}
	return ""
}

func randomHex(bytesLen int) (string, error) {
	if bytesLen <= 0 {
		return "", errors.New("random hex byte length must be positive")
	}
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const hex = "0123456789abcdef"
	out := make([]byte, bytesLen*2)
	for i, b := range buf {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out), nil
}

func resolveProvisionReleases(paths provisionPaths, operator map[string]string, opts *provisionOptions) error {
	releases := []struct {
		display string
		prefix  string
		value   *string
	}{
		{"Video Cloud", "rtk_video_cloud", &opts.videoRelease},
		{"Account Manager", "rtk_account_manager", &opts.accountRelease},
		{"Cloud Admin", "rtk_cloud_admin", &opts.adminRelease},
	}
	for _, item := range releases {
		version, objectKey, err := selectObjectRelease(operator, item.display, item.prefix, *item.value)
		if err != nil {
			return err
		}
		*item.value = version
		fmt.Fprintf(os.Stderr, "selected %s Object Storage release: %s\n", item.display, version)
		fmt.Fprintf(os.Stderr, "%s Object Storage release readable: %s\n", item.display, objectKey)
	}
	return nil
}

func selectObjectRelease(operator map[string]string, display, prefix, requested string) (string, string, error) {
	store, err := provisionObjectStoreFromEnv(operator)
	if err != nil {
		return "", "", err
	}
	entries, err := provisionListObjects(store, "releases/")
	if err != nil {
		return "", "", err
	}
	manifestEntries := []provisionObjectEntry{}
	wantPrefix := "releases/" + prefix + "-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, wantPrefix) && strings.HasSuffix(entry.Key, "/manifest.json") {
			manifestEntries = append(manifestEntries, entry)
		}
	}
	if len(manifestEntries) == 0 {
		return "", "", fmt.Errorf("no %s release manifest found in Object Storage under releases/", prefix)
	}
	sort.Slice(manifestEntries, func(i, j int) bool {
		if manifestEntries[i].LastModified == manifestEntries[j].LastModified {
			return manifestEntries[i].Key < manifestEntries[j].Key
		}
		return manifestEntries[i].LastModified > manifestEntries[j].LastModified
	})
	fmt.Fprintf(os.Stderr, "Available %s releases in Object Storage:\n", display)
	type candidate struct {
		version string
		key     string
	}
	candidates := []candidate{}
	for _, entry := range manifestEntries {
		data, err := provisionReadObject(store, entry.Key)
		if err != nil {
			return "", "", err
		}
		manifest := map[string]any{}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return "", "", err
		}
		version := stringValue(manifest["version"])
		artifactKey := stringValue(manifest["artifact_path"])
		if version == "" || artifactKey == "" {
			return "", "", fmt.Errorf("release manifest missing version or artifact_path: %s", entry.Key)
		}
		candidates = append(candidates, candidate{version: version, key: artifactKey})
		fmt.Fprintf(os.Stderr, "%d) %s\n", len(candidates), version)
	}
	chosen := candidates[0]
	if requested != "" {
		found := false
		for _, c := range candidates {
			if c.version == requested {
				chosen = c
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("%s release not found: %s", display, requested)
		}
	}
	if err := provisionObjectExists(store, chosen.key); err != nil {
		return "", "", err
	}
	return chosen.version, chosen.key, nil
}

func provisionDNS(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	video, _ := readJSONMap(paths.VideoState)
	instances, _ := video["instances"].(map[string]any)
	edge, _ := instances["edge"].(map[string]any)
	logger := loggerProvisionTarget(paths, env)
	records := []struct {
		domain string
		ip     string
	}{
		{env["VIDEO_CLOUD_DOMAIN"], stringValue(edge["public_ipv4"])},
		{env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], stringValue(edge["public_ipv4"])},
		{yamlScalarValue(paths.VideoConfig, "device_client_domain"), stringValue(edge["public_ipv4"])},
		{env["ACCOUNT_MANAGER_DOMAIN"], envFileValue(paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4")},
		{env["CLOUD_ADMIN_DOMAIN"], envFileValue(paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4")},
		{logger.Domain, logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"]},
	}
	for _, record := range records {
		if record.domain == "" || record.ip == "" {
			continue
		}
		if err := godaddyUpsert(paths, env["CLOUD_DNS_ROOT_DOMAIN"], opts.godaddyEnv, opts.operatorEnv, record.domain, record.ip, opts.dnsWaitTTL); err != nil {
			return err
		}
		if err := waitDNS(record.domain, record.ip, env["CLOUD_DNS_ROOT_DOMAIN"], opts); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "restoring DNS final TTL: %s\n", opts.dnsFinalTTL)
	for _, record := range records {
		if record.domain == "" || record.ip == "" {
			continue
		}
		if err := godaddyUpsert(paths, env["CLOUD_DNS_ROOT_DOMAIN"], opts.godaddyEnv, opts.operatorEnv, record.domain, record.ip, opts.dnsFinalTTL); err != nil {
			return err
		}
	}
	return nil
}

func godaddyUpsert(paths provisionPaths, rootDomain, godaddyEnv, operatorEnv, domain, ip, ttl string) error {
	name := recordNameForDomain(rootDomain, domain)
	goCmd := firstNonEmpty(os.Getenv("RTK_CLOUD_GO"), "go")
	cmd := exec.Command(goCmd, "run", "./cmd/godaddy-dns", "--env-file", operatorEnv, "records", "upsert", rootDomain, "--type", "A", "--name", name, "--data", ip, "--ttl", ttl)
	cmd.Dir = filepath.Join(paths.Workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns")
	cmd.Env = append(os.Environ(), "GODADDY_ENV="+godaddyEnv)
	cmd.Env = append(cmd.Env, "GOWORK=off")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func recordNameForDomain(rootDomain, domain string) string {
	if domain == rootDomain {
		return "@"
	}
	return strings.TrimSuffix(domain, "."+rootDomain)
}

func waitDNS(domain, ip, rootDomain string, opts provisionOptions) error {
	interval, _ := strconv.Atoi(firstNonEmpty(os.Getenv("DNS_WAIT_INTERVAL_SECONDS"), "10"))
	maxSeconds, _ := strconv.Atoi(opts.dnsWaitMaxSeconds)
	maxAttempts := (maxSeconds + interval - 1) / interval
	nsBytes, _ := exec.Command("dig", "NS", rootDomain, "+short").Output()
	ns := strings.TrimSpace(strings.Split(string(nsBytes), "\n")[0])
	if ns == "" {
		return fmt.Errorf("could not resolve authoritative NS for %s", rootDomain)
	}
	var gotGoogle, gotAuth string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		gotGoogle = digShort("8.8.8.8", domain)
		gotAuth = digShort(ns, domain)
		if gotGoogle == ip && gotAuth == ip {
			fmt.Fprintf(os.Stderr, "DNS converged: %s -> %s\n", domain, ip)
			return nil
		}
		fmt.Fprintf(os.Stderr, "waiting DNS attempt %d/%d: %s expected=%s google=%s auth=%s\n", attempt, maxAttempts, domain, ip, firstNonEmpty(gotGoogle, "<empty>"), firstNonEmpty(gotAuth, "<empty>"))
		_ = exec.Command("sleep", strconv.Itoa(interval)).Run()
	}
	return fmt.Errorf("DNS did not converge: %s expected=%s google=%s auth=%s", domain, ip, firstNonEmpty(gotGoogle, "<empty>"), firstNonEmpty(gotAuth, "<empty>"))
}

func digShort(server, domain string) string {
	out, _ := exec.Command("dig", "+short", "@"+server, domain).Output()
	return strings.TrimSpace(strings.Split(string(out), "\n")[0])
}

func provisionE2E(paths provisionPaths, env map[string]string) error {
	checks := []struct {
		name string
		url  string
	}{
		{"video-cloud-healthz", "https://" + env["VIDEO_CLOUD_DOMAIN"] + "/healthz"},
		{"video-cloud-version", "https://" + env["VIDEO_CLOUD_DOMAIN"] + "/version"},
		{"account-manager-health", "https://" + env["ACCOUNT_MANAGER_DOMAIN"] + "/v1/health"},
		{"admin-service-health", "https://" + env["CLOUD_ADMIN_DOMAIN"] + "/api/service-health"},
	}
	dir := filepath.Join(paths.ArtifactsDir, "e2e-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[rtk-cloud provision] e2e report: %s\n", dir)
	ok := true
	var b strings.Builder
	fmt.Fprintln(&b, "# E2E Report")
	fmt.Fprintln(&b)
	for _, check := range checks {
		cmd := exec.Command("curl", "-fsS", check.url)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			ok = false
			fmt.Fprintf(&b, "- FAIL `%s`\n", check.name)
		} else {
			fmt.Fprintf(&b, "- PASS `%s`\n", check.name)
		}
	}
	status := "passed"
	if !ok {
		status = "failed"
	}
	report := strings.Replace(b.String(), "# E2E Report\n", "# E2E Report\n\nstatus: "+status+"\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "e2e-report.md"), []byte(report), 0o644); err != nil {
		return err
	}
	if !ok {
		return errors.New("provision e2e failed")
	}
	return nil
}

func writeProvisionArtifacts(paths provisionPaths, stack string) (string, error) {
	video, err := readJSONMap(paths.VideoState)
	if err != nil {
		return "", err
	}
	am, _ := readEnvFile(paths.AccountManagerState)
	admin, _ := readEnvFile(paths.AdminState)
	logger := loggerProvisionTarget(paths, map[string]string{})
	ts := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(paths.ArtifactsDir, "provision-"+ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	inventory := map[string]any{
		"stack":        stack,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"video_cloud":  video,
		"account_manager": map[string]any{
			"id":          atoiOrZero(am["ACCOUNT_MANAGER_LINODE_ID"]),
			"label":       am["ACCOUNT_MANAGER_LINODE_LABEL"],
			"public_ipv4": am["ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"],
			"private_ip":  am["ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"],
			"firewall_id": atoiOrZero(am["ACCOUNT_MANAGER_LINODE_FIREWALL_ID"]),
		},
		"cloud_admin": map[string]any{
			"id":          atoiOrZero(admin["ADMIN_LINODE_ID"]),
			"label":       admin["ADMIN_LINODE_LABEL"],
			"public_ipv4": admin["ADMIN_LINODE_PUBLIC_IPV4"],
			"private_ip":  admin["ADMIN_LINODE_PRIVATE_IPV4"],
			"firewall_id": atoiOrZero(admin["ADMIN_LINODE_FIREWALL_ID"]),
		},
		"cloud_logger": map[string]any{
			"id":          atoiOrZero(logger.State["CLOUD_LOGGER_LINODE_ID"]),
			"label":       logger.State["CLOUD_LOGGER_LINODE_LABEL"],
			"domain":      logger.State["CLOUD_LOGGER_DOMAIN"],
			"endpoint":    logger.State["CLOUD_LOGGER_ENDPOINT"],
			"public_ipv4": logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"],
			"firewall_id": atoiOrZero(logger.State["CLOUD_LOGGER_LINODE_FIREWALL_ID"]),
			"token":       "REDACTED",
		},
	}
	if err := writeJSON(filepath.Join(dir, "inventory.json"), inventory); err != nil {
		return "", err
	}
	targets := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"targets": map[string]any{
			"edge":            targetFromVideo(video, "edge", false),
			"api":             targetFromVideo(video, "api", true),
			"infra":           targetFromVideo(video, "infra", true),
			"mqtt":            targetFromVideo(video, "mqtt", true),
			"coturn":          targetFromVideo(video, "coturn", false),
			"account_manager": map[string]any{"host": am["ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"], "user": "root", "private_ip": am["ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"]},
			"cloud_admin":     map[string]any{"host": admin["ADMIN_LINODE_PUBLIC_IPV4"], "user": "root", "private_ip": admin["ADMIN_LINODE_PRIVATE_IPV4"]},
			"cloud_logger":    map[string]any{"host": logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"], "user": "root", "endpoint": logger.State["CLOUD_LOGGER_ENDPOINT"]},
		},
	}
	if err := writeJSON(filepath.Join(dir, "deployment-targets.json"), targets); err != nil {
		return "", err
	}
	if err := writeEnvMap(filepath.Join(dir, "cloud-logger-state.redacted.env"), redactEnvValues(logger.State), 0o600); err != nil {
		return "", err
	}
	loggerEnv, _ := readEnvFile(logger.EnvPath)
	if err := writeEnvMap(filepath.Join(dir, "cloud-logger-env.redacted.env"), redactEnvValues(loggerEnv), 0o600); err != nil {
		return "", err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# Provision Report\n\n- generated_at: %s\n- artifact_dir: %s\n- cloud_admin_private_ip: %s\n- prometheus: %s\n\n", time.Now().UTC().Format(time.RFC3339), dir, admin["ADMIN_LINODE_PRIVATE_IPV4"], videoCloudPrometheusBaseURL(paths))
	fmt.Fprintln(&report, "## VM Configuration")
	fmt.Fprintln(&report)
	fmt.Fprintln(&report, "| Role | Label | Linode ID | Firewall ID | Network | Public IPv4 | Private IPv4 | SSH route | ProxyJump |")
	fmt.Fprintln(&report, "| --- | --- | ---: | ---: | --- | --- | --- | --- | --- |")
	writeVideoVMRows(&report, video)
	fmt.Fprintf(&report, "| `account-manager` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `direct public SSH` | `N/A` |\n", am["ACCOUNT_MANAGER_LINODE_LABEL"], am["ACCOUNT_MANAGER_LINODE_ID"], am["ACCOUNT_MANAGER_LINODE_FIREWALL_ID"], publicPrivateNetwork(am["ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"], am["ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"]), displayNA(am["ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"]), displayNA(am["ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4"]))
	fmt.Fprintf(&report, "| `cloud-admin` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `direct public SSH` | `N/A` |\n", admin["ADMIN_LINODE_LABEL"], admin["ADMIN_LINODE_ID"], admin["ADMIN_LINODE_FIREWALL_ID"], publicPrivateNetwork(admin["ADMIN_LINODE_PUBLIC_IPV4"], admin["ADMIN_LINODE_PRIVATE_IPV4"]), displayNA(admin["ADMIN_LINODE_PUBLIC_IPV4"]), displayNA(admin["ADMIN_LINODE_PRIVATE_IPV4"]))
	fmt.Fprintf(&report, "| `cloud-logger` | `%s` | `%s` | `%s` | `%s` | `%s` | `N/A` | `direct public SSH` | `N/A` |\n", logger.State["CLOUD_LOGGER_LINODE_LABEL"], logger.State["CLOUD_LOGGER_LINODE_ID"], logger.State["CLOUD_LOGGER_LINODE_FIREWALL_ID"], publicPrivateNetwork(logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"], ""), displayNA(logger.State["CLOUD_LOGGER_LINODE_PUBLIC_IPV4"]))
	fmt.Fprintln(&report)
	fmt.Fprintln(&report, "## Logger")
	fmt.Fprintln(&report)
	fmt.Fprintf(&report, "- domain: %s\n", displayNA(logger.State["CLOUD_LOGGER_DOMAIN"]))
	fmt.Fprintf(&report, "- endpoint: %s\n", displayNA(logger.State["CLOUD_LOGGER_ENDPOINT"]))
	fmt.Fprintln(&report, "- ingest_token: REDACTED")
	fmt.Fprintln(&report)
	fmt.Fprintln(&report, "VPN: not configured by this script; private service access uses edge SSH ProxyJump over the Linode VPC.")
	if err := os.WriteFile(filepath.Join(dir, "provision-report.md"), []byte(report.String()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func writeVideoVMRows(b *strings.Builder, video map[string]any) {
	instances, _ := video["instances"].(map[string]any)
	firewalls, _ := video["firewalls"].(map[string]any)
	edge, _ := instances["edge"].(map[string]any)
	edgePublic := stringValue(edge["public_ipv4"])
	for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
		item, _ := instances[role].(map[string]any)
		if len(item) == 0 {
			continue
		}
		publicIP := stringValue(item["public_ipv4"])
		privateIP := stringValue(item["private_ip"])
		route := "`direct public SSH`"
		proxy := "`N/A`"
		if privateVideoRoleUsesProxyJump(role) && privateIP != "" {
			route = "`VPC via edge ProxyJump`"
			proxy = "`root@" + edgePublic + "`"
		}
		fmt.Fprintf(b, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | %s |\n", role, stringValue(item["label"]), stringValue(item["id"]), stringValue(firewalls[role]), publicPrivateNetwork(publicIP, privateIP), displayNA(publicIP), displayNA(privateIP), route, proxy)
	}
}

func privateVideoRoleUsesProxyJump(role string) bool {
	switch role {
	case "api", "infra", "mqtt":
		return true
	default:
		return false
	}
}

func publicPrivateNetwork(publicIP, privateIP string) string {
	switch {
	case publicIP != "" && privateIP != "":
		return "public+vpc"
	case privateIP != "":
		return "private"
	default:
		return "public"
	}
}

func displayNA(value string) string {
	if value == "" {
		return "N/A"
	}
	return value
}

func targetFromVideo(video map[string]any, role string, proxy bool) map[string]any {
	instances, _ := video["instances"].(map[string]any)
	item, _ := instances[role].(map[string]any)
	host := stringValue(item["public_ipv4"])
	if proxy {
		host = stringValue(item["private_ip"])
	}
	out := map[string]any{"host": host, "user": "root"}
	if proxy {
		edge, _ := instances["edge"].(map[string]any)
		out["proxy_jump"] = "root@" + stringValue(edge["public_ipv4"])
	}
	return out
}

func ensureProvisionVPCInterface(paths provisionPaths, serviceName, statePath, statePrefix, linodeID, privateIP string) error {
	if linodeID == "" || privateIP == "" {
		return nil
	}
	if envFileValue(statePath, statePrefix+"_LINODE_PRIVATE_IPV4") == privateIP {
		return nil
	}
	subnetID := videoSubnetID(paths)
	if subnetID == "" {
		return errors.New("video cloud subnet_id is required")
	}
	configsRaw, err := curlLinode("GET", "/linode/instances/"+linodeID+"/configs", "")
	if err != nil {
		return err
	}
	var configs struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(configsRaw, &configs); err != nil {
		return err
	}
	if len(configs.Data) == 0 {
		return fmt.Errorf("cannot find %s Linode config", serviceName)
	}
	configPath := fmt.Sprintf("/linode/instances/%s/configs/%d", linodeID, configs.Data[0].ID)
	configRaw, err := curlLinode("GET", configPath, "")
	if err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return err
	}
	for _, raw := range asSlice(config["interfaces"]) {
		if iface, ok := raw.(map[string]any); ok && iface["purpose"] == "vpc" {
			if ipv4, ok := iface["ipv4"].(map[string]any); ok && ipv4["vpc"] == privateIP {
				return writeStateVar(statePath, statePrefix+"_LINODE_PRIVATE_IPV4", privateIP)
			}
		}
	}
	subnet, _ := strconv.Atoi(subnetID)
	config["interfaces"] = []map[string]any{
		{"purpose": "public", "primary": true},
		{"purpose": "vpc", "subnet_id": subnet, "ipv4": map[string]any{"vpc": privateIP}},
	}
	payload, _ := json.Marshal(config)
	if _, err := curlLinode("PUT", configPath, string(payload)); err != nil {
		return err
	}
	if err := writeStateVar(statePath, statePrefix+"_LINODE_PRIVATE_IPV4", privateIP); err != nil {
		return err
	}
	_, err = curlLinode("POST", "/linode/instances/"+linodeID+"/reboot", "{}")
	return err
}

func readEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return values, nil
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
