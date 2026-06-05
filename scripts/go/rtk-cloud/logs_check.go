package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type logsCheckTarget struct {
	Host      string `json:"host"`
	User      string `json:"user"`
	ProxyJump string `json:"proxy_jump,omitempty"`
	PrivateIP string `json:"private_ip,omitempty"`
}

type logsCheckDomains struct {
	VideoCloud     string `json:"video_cloud"`
	AccountManager string `json:"account_manager"`
	CloudAdmin     string `json:"cloud_admin"`
}

type logsCheckConfig struct {
	EnvRoot      string                     `json:"env_root"`
	ArtifactDir  string                     `json:"artifact_dir"`
	RawDir       string                     `json:"raw_dir"`
	Since        string                     `json:"since"`
	Tail         int                        `json:"tail"`
	SSHKey       string                     `json:"ssh_key"`
	KnownHosts   string                     `json:"known_hosts"`
	SkipTraffic  bool                       `json:"skip_traffic"`
	JSONOnly     bool                       `json:"json_only"`
	FailOnSecret bool                       `json:"fail_on_secret"`
	Domains      logsCheckDomains           `json:"domains"`
	Targets      map[string]logsCheckTarget `json:"targets"`
}

type logsCheckItem struct {
	Name     string   `json:"name"`
	Target   string   `json:"target"`
	Status   string   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Artifact string   `json:"artifact,omitempty"`
	Secrets  []string `json:"secrets,omitempty"`
}

type logsCheckResult struct {
	GeneratedAt string          `json:"generated_at"`
	Status      string          `json:"status"`
	ArtifactDir string          `json:"artifact_dir"`
	Checks      []logsCheckItem `json:"checks"`
}

type remoteRunner interface {
	Run(target logsCheckTarget, command string) (string, error)
}

type sshRemoteRunner struct {
	KeyPath    string
	KnownHosts string
}

func runLogsCheck(args []string) error {
	fs := flag.NewFlagSet("logs-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRootFlag := fs.String("env-root", "", "environment root")
	outDir := fs.String("out-dir", "", "output directory")
	since := fs.String("since", "15m", "journalctl window")
	tail := fs.Int("tail", 300, "log tail lines")
	sshKey := fs.String("ssh-key", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud"), "SSH private key")
	failOnSecret := fs.Bool("fail-on-secret", true, "fail when log secret patterns are found")
	skipTraffic := fs.Bool("skip-traffic", false, "skip external smoke traffic")
	jsonOnly := fs.Bool("json", false, "write summary JSON to stdout only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	envRootPath, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	cfg, err := newLogCheckConfig(envRootPath, *outDir, *since, *tail, *sshKey, *skipTraffic, *jsonOnly, *failOnSecret)
	if err != nil {
		return err
	}
	result, err := executeLogsCheck(cfg, sshRemoteRunner{KeyPath: cfg.SSHKey, KnownHosts: cfg.KnownHosts}, http.DefaultClient)
	if writeErr := writeLogsCheckArtifacts(cfg, result); writeErr != nil && err == nil {
		err = writeErr
	}
	if *jsonOnly {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encodeErr := enc.Encode(result); encodeErr != nil && err == nil {
			err = encodeErr
		}
	} else {
		fmt.Fprintf(os.Stderr, "[logs-check] report: %s\n", filepath.Join(cfg.ArtifactDir, "report.md"))
		fmt.Fprintf(os.Stdout, "%s\n", cfg.ArtifactDir)
	}
	if err != nil {
		return err
	}
	if result.Status != "passed" {
		return errors.New("logs check failed")
	}
	return nil
}

func newLogCheckConfig(envRootPath, outDir, since string, tail int, sshKey string, skipTraffic, jsonOnly, failOnSecret bool) (logsCheckConfig, error) {
	if envRootPath == "" {
		return logsCheckConfig{}, errors.New("--env-root is required")
	}
	if tail <= 0 {
		return logsCheckConfig{}, errors.New("--tail must be greater than zero")
	}
	if sshKey == "" {
		sshKey = filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")
	}
	paths := envroot.NewPaths(envRootPath)
	if outDir == "" {
		outDir = filepath.Join(paths.ArtifactsDir, "logs-check", time.Now().UTC().Format("20060102T150405Z"))
	}
	targets, err := loadLogsCheckTargets(envRootPath)
	if err != nil {
		return logsCheckConfig{}, err
	}
	domains := logsCheckDomains{
		VideoCloud:     envFileValue(paths.StackEnv, "VIDEO_CLOUD_DOMAIN"),
		AccountManager: envFileValue(paths.StackEnv, "ACCOUNT_MANAGER_DOMAIN"),
		CloudAdmin:     envFileValue(paths.StackEnv, "CLOUD_ADMIN_DOMAIN"),
	}
	return logsCheckConfig{
		EnvRoot:      envRootPath,
		ArtifactDir:  outDir,
		RawDir:       filepath.Join(outDir, "raw"),
		Since:        since,
		Tail:         tail,
		SSHKey:       sshKey,
		KnownHosts:   filepath.Join(outDir, "known_hosts"),
		SkipTraffic:  skipTraffic,
		JSONOnly:     jsonOnly,
		FailOnSecret: failOnSecret,
		Domains:      domains,
		Targets:      targets,
	}, nil
}

func loadLogsCheckTargets(root string) (map[string]logsCheckTarget, error) {
	targets := map[string]logsCheckTarget{}
	if path := latestProvisionTargets(root); path != "" {
		loaded, err := readDeploymentTargets(path)
		if err != nil {
			return nil, err
		}
		for role, target := range loaded {
			targets[role] = target
		}
	}
	if len(targets) == 0 {
		loaded, err := readVideoStateTargets(root)
		if err != nil {
			return nil, err
		}
		for role, target := range loaded {
			targets[role] = target
		}
	} else if loaded, err := readVideoStateTargets(root); err == nil {
		for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
			if target := loaded[role]; target.Host != "" {
				targets[role] = target
			}
		}
	}
	accountState := filepath.Join(root, "state", "account-manager-staging.env")
	if host := firstNonEmpty(envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_HOST"), envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4")); host != "" {
		targets["account-manager"] = logsCheckTarget{Host: host, User: "root", PrivateIP: envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4")}
	}
	adminState := filepath.Join(root, "state", "cloud-admin-staging.env")
	if host := firstNonEmpty(envFileValue(adminState, "ADMIN_LINODE_HOST"), envFileValue(adminState, "ADMIN_LINODE_PUBLIC_IPV4")); host != "" {
		targets["cloud-admin"] = logsCheckTarget{Host: host, User: "root"}
	}
	if targets["edge"].Host == "" {
		return nil, errors.New("edge target is required; run provision artifacts first")
	}
	return targets, nil
}

func latestProvisionTargets(root string) string {
	matches, _ := filepath.Glob(filepath.Join(root, "artifacts", "provision-*", "deployment-targets.json"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func readDeploymentTargets(path string) (map[string]logsCheckTarget, error) {
	var data struct {
		Targets map[string]logsCheckTarget `json:"targets"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]logsCheckTarget{}
	for role, target := range data.Targets {
		role = strings.ReplaceAll(role, "_", "-")
		if target.User == "" {
			target.User = "root"
		}
		out[role] = target
	}
	return out, nil
}

func readVideoStateTargets(root string) (map[string]logsCheckTarget, error) {
	stack := envFileValue(filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME")
	candidates := []string{}
	if stack != "" {
		candidates = append(candidates, filepath.Join(root, "state", stack+".state.json"))
	}
	candidates = append(candidates, filepath.Join(root, "state", "video-cloud-staging.state.json"))
	var path string
	for _, candidate := range candidates {
		if exists(candidate) {
			path = candidate
			break
		}
	}
	if path == "" {
		return nil, errors.New("video-cloud state not found")
	}
	var data struct {
		Instances map[string]struct {
			PublicIPv4 string `json:"public_ipv4"`
			PrivateIP  string `json:"private_ip"`
		} `json:"instances"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	edgePublic := data.Instances["edge"].PublicIPv4
	out := map[string]logsCheckTarget{}
	for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
		inst := data.Instances[role]
		target := logsCheckTarget{User: "root"}
		if role == "edge" || role == "coturn" {
			target.Host = inst.PublicIPv4
		} else {
			target.Host = firstNonEmpty(inst.PrivateIP, inst.PublicIPv4)
			if edgePublic != "" {
				target.ProxyJump = "root@" + edgePublic
			}
		}
		if target.Host != "" {
			out[role] = target
		}
	}
	return out, nil
}

func executeLogsCheck(cfg logsCheckConfig, runner remoteRunner, client *http.Client) (logsCheckResult, error) {
	result := logsCheckResult{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Status:      "passed",
		ArtifactDir: cfg.ArtifactDir,
	}
	if err := os.MkdirAll(cfg.RawDir, 0o755); err != nil {
		return result, err
	}
	if !cfg.SkipTraffic {
		result.Checks = append(result.Checks, runSmokeChecks(cfg.Domains, client)...)
	}
	for _, spec := range logsCheckRemoteSpecs(cfg) {
		item := runRemoteLogCheck(cfg, runner, spec)
		result.Checks = append(result.Checks, item)
	}
	for _, check := range result.Checks {
		if check.Status == "fail" {
			result.Status = "failed"
			break
		}
	}
	return result, nil
}

func runSmokeChecks(domains logsCheckDomains, client *http.Client) []logsCheckItem {
	specs := []struct {
		name string
		url  string
	}{
		{"video-cloud-healthz", "https://" + domains.VideoCloud + "/healthz"},
		{"video-cloud-version", "https://" + domains.VideoCloud + "/version"},
		{"account-manager-health", "https://" + domains.AccountManager + "/v1/health"},
		{"cloud-admin-healthz", "https://" + domains.CloudAdmin + "/healthz"},
		{"cloud-admin-service-health", "https://" + domains.CloudAdmin + "/api/service-health"},
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	out := []logsCheckItem{}
	for _, spec := range specs {
		if strings.Contains(spec.url, "https:///") {
			out = append(out, logsCheckItem{Name: spec.name, Target: "operator", Status: "fail", Detail: "domain missing"})
			continue
		}
		req, err := http.NewRequest(http.MethodGet, spec.url, nil)
		if err != nil {
			out = append(out, logsCheckItem{Name: spec.name, Target: "operator", Status: "fail", Detail: err.Error()})
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			out = append(out, logsCheckItem{Name: spec.name, Target: "operator", Status: "fail", Detail: err.Error()})
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()
		status := "pass"
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			status = "fail"
		}
		out = append(out, logsCheckItem{Name: spec.name, Target: "operator", Status: status, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)})
	}
	return out
}

type logsCheckRemoteSpec struct {
	Name     string
	Role     string
	Command  string
	Artifact string
}

func logsCheckRemoteSpecs(cfg logsCheckConfig) []logsCheckRemoteSpec {
	since := logShellQuote(journalSince(cfg.Since))
	tail := strconv.Itoa(cfg.Tail)
	return []logsCheckRemoteSpec{
		{"edge-failed-units", "edge", "systemctl list-units --state=failed --no-legend --no-pager || true", "edge-failed-units.log"},
		{"edge-nginx-journal", "edge", "journalctl -u nginx --since " + since + " -n " + tail + " --no-pager || true", "edge-nginx-journal.log"},
		{"edge-nginx-access", "edge", "tail -n " + tail + " /var/log/nginx/access.log 2>&1 || true", "edge-nginx-access.log"},
		{"edge-nginx-error", "edge", "tail -n " + tail + " /var/log/nginx/error.log 2>&1 || true", "edge-nginx-error.log"},
		{"api-services", "api", serviceStatusCommand([]string{"video_cloud-api.service", "video_cloud-logingester.service", "video_cloud-turnregistry.service", "video_cloud-metricsexporter.service", "video_cloud-crossservice.service", "video_cloud-cleaner.service", "video_cloud-statistics.service"}, []string{"video_cloud-certissuer.service", "video_cloud-factoryenroll.service"}), "api-services.log"},
		{"api-journal", "api", "journalctl -u video_cloud-api.service -u video_cloud-logingester.service -u video_cloud-turnregistry.service -u video_cloud-metricsexporter.service -u video_cloud-crossservice.service -u video_cloud-cleaner.service -u video_cloud-statistics.service -u video_cloud-certissuer.service -u video_cloud-factoryenroll.service --since " + since + " -n " + tail + " --no-pager || true", "api-journal.log"},
		{"infra-services", "infra", serviceStatusCommand([]string{"postgresql", "redis-server", "nats-server", "prometheus"}, nil), "infra-services.log"},
		{"infra-journal", "infra", "journalctl -u postgresql -u redis-server -u nats-server -u prometheus --since " + since + " -n " + tail + " --no-pager || true", "infra-journal.log"},
		{"mqtt-services", "mqtt", serviceStatusCommand([]string{"emqx"}, nil), "mqtt-services.log"},
		{"mqtt-journal", "mqtt", "journalctl -u emqx --since " + since + " -n " + tail + " --no-pager || true", "mqtt-journal.log"},
		{"mqtt-emqx", "mqtt", "docker logs --tail " + tail + " video-cloud-emqx 2>&1 || true", "mqtt-emqx.log"},
		{"coturn-services", "coturn", serviceStatusCommand([]string{"coturn"}, []string{"video_cloud-turnregistrar.service"}), "coturn-services.log"},
		{"coturn-journal", "coturn", "journalctl -u coturn -u video_cloud-turnregistrar.service --since " + since + " -n " + tail + " --no-pager || true", "coturn-journal.log"},
		{"account-manager-service", "account-manager", serviceStatusCommand([]string{"rtk-account-manager.service"}, nil), "account-manager-service.log"},
		{"account-manager-journal", "account-manager", "journalctl -u rtk-account-manager.service --since " + since + " -n " + tail + " --no-pager || true", "account-manager-journal.log"},
		{"cloud-admin-units", "cloud-admin", "systemctl list-units '*admin*' '*cloud*' --no-pager || true", "cloud-admin-units.log"},
		{"cloud-admin-journal", "cloud-admin", "journalctl --since " + since + " -n " + tail + " --no-pager || true", "cloud-admin-journal.log"},
		{"journald-usage-edge", "edge", "journalctl --disk-usage; systemctl status systemd-journald --no-pager || true", "edge-journald-usage.log"},
	}
}

func serviceStatusCommand(required, optional []string) string {
	var b strings.Builder
	b.WriteString("set +e; ")
	for _, unit := range required {
		b.WriteString("printf ")
		b.WriteString(logShellQuote("%s\\t"))
		b.WriteString(" ")
		b.WriteString(logShellQuote(unit))
		b.WriteString("; systemctl is-active ")
		b.WriteString(logShellQuote(unit))
		b.WriteString(" 2>/dev/null || true; ")
	}
	for _, unit := range optional {
		b.WriteString("if systemctl list-unit-files ")
		b.WriteString(logShellQuote(unit))
		b.WriteString(" --no-legend 2>/dev/null | grep -q . || systemctl list-units ")
		b.WriteString(logShellQuote(unit))
		b.WriteString(" --no-legend 2>/dev/null | grep -q .; then printf ")
		b.WriteString(logShellQuote("%s\\t"))
		b.WriteString(" ")
		b.WriteString(logShellQuote(unit))
		b.WriteString("; systemctl is-active ")
		b.WriteString(logShellQuote(unit))
		b.WriteString(" 2>/dev/null || true; else printf ")
		b.WriteString(logShellQuote("%s\\tskipped\\n"))
		b.WriteString(" ")
		b.WriteString(logShellQuote(unit))
		b.WriteString("; fi; ")
	}
	return b.String()
}

func runRemoteLogCheck(cfg logsCheckConfig, runner remoteRunner, spec logsCheckRemoteSpec) logsCheckItem {
	target, ok := cfg.Targets[spec.Role]
	if !ok || target.Host == "" {
		return logsCheckItem{Name: spec.Name, Target: spec.Role, Status: "skip", Detail: "target missing"}
	}
	out, err := runner.Run(target, spec.Command)
	artifact := filepath.Join("raw", spec.Artifact)
	absArtifact := filepath.Join(cfg.ArtifactDir, artifact)
	if writeErr := os.WriteFile(absArtifact, []byte(out), 0o644); writeErr != nil && err == nil {
		err = writeErr
	}
	item := logsCheckItem{Name: spec.Name, Target: spec.Role, Status: "pass", Artifact: artifact}
	if err != nil {
		item.Status = "fail"
		item.Detail = err.Error()
		return item
	}
	if isFailedServiceOutput(spec.Name, out) {
		item.Status = "fail"
		item.Detail = "required service is not active"
	}
	if strings.Contains(spec.Name, "failed-units") && strings.TrimSpace(filterSSHNoise(out)) != "" {
		item.Status = "fail"
		item.Detail = "systemctl --failed returned units"
	}
	if cfg.FailOnSecret {
		if hits := scanLogSecrets(out); len(hits) > 0 {
			item.Status = "fail"
			item.Detail = "secret pattern matched"
			item.Secrets = hits
		}
	}
	return item
}

func (r sshRemoteRunner) Run(target logsCheckTarget, command string) (string, error) {
	args := []string{
		"-i", r.KeyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
	}
	if r.KnownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+r.KnownHosts)
	}
	if target.ProxyJump != "" {
		proxyCommand := "ssh -i " + logShellQuote(r.KeyPath) + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o LogLevel=ERROR -W %h:%p " + logShellQuote(target.ProxyJump)
		if r.KnownHosts != "" {
			proxyCommand = "ssh -i " + logShellQuote(r.KeyPath) + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o LogLevel=ERROR -o UserKnownHostsFile=" + logShellQuote(r.KnownHosts) + " -W %h:%p " + logShellQuote(target.ProxyJump)
		}
		args = append(args, "-o", "ProxyCommand="+proxyCommand)
	}
	user := firstNonEmpty(target.User, "root")
	args = append(args, user+"@"+target.Host, "bash -lc "+logShellQuote(command))
	cmd := exec.Command("ssh", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func isFailedServiceOutput(name, out string) bool {
	if !strings.Contains(name, "services") && !strings.Contains(name, "service") {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if isSSHNoiseLine(line) {
			continue
		}
		if line == "" || strings.HasSuffix(line, "\tskipped") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[len(fields)-1] != "active" {
			return true
		}
	}
	return false
}

func filterSSHNoise(out string) string {
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if isSSHNoiseLine(strings.TrimSpace(line)) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func isSSHNoiseLine(line string) bool {
	return strings.HasPrefix(line, "Warning: Permanently added ")
}

func scanLogSecrets(body string) []string {
	patterns := []string{
		`(?i)authorization:\s*bearer\s+[^<\s][^\s]+`,
		`(?i)\bpassword\s*=\s*[^<\s][^\s]+`,
		`(?i)\btoken\s*=\s*[^<\s][^\s]+`,
		`(?i)\bsecret\s*=\s*[^<\s][^\s]+`,
		`(?i)BEGIN [A-Z ]*PRIVATE KEY`,
		`(?i)AWS_SECRET_ACCESS_KEY\s*=\s*[^<\s][^\s]+`,
		`(?i)LINODE_TOKEN\s*=\s*[^<\s][^\s]+`,
		`(?i)GODADDY_SECRET\s*=\s*[^<\s][^\s]+`,
	}
	allow := regexp.MustCompile(`(?i)(<redacted>|redacted|\*{3,}|=\s*$|bearer\s+<redacted>)`)
	hits := []string{}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		for _, match := range re.FindAllString(body, -1) {
			if allow.MatchString(match) {
				continue
			}
			hits = append(hits, redactLogSecretHit(match))
		}
	}
	sort.Strings(hits)
	return uniqueStrings(hits)
}

func redactLogSecretHit(hit string) string {
	hit = strings.TrimSpace(hit)
	if len(hit) <= 32 {
		return hit
	}
	return hit[:32] + "..."
}

func uniqueStrings(items []string) []string {
	out := []string{}
	for _, item := range items {
		if len(out) == 0 || out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}

func journalSince(value string) string {
	if value == "" {
		return "15 minutes ago"
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return value
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d hours ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d minutes ago", int(d/time.Minute))
}

func logShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeLogsCheckArtifacts(cfg logsCheckConfig, result logsCheckResult) error {
	if err := os.MkdirAll(cfg.ArtifactDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(cfg.ArtifactDir, "summary.json"), result); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.ArtifactDir, "report.md"), []byte(renderLogsCheckReport(result)), 0o644)
}

func renderLogsCheckReport(result logsCheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Logs Check Report\n\n")
	fmt.Fprintf(&b, "- generated_at: %s\n", result.GeneratedAt)
	fmt.Fprintf(&b, "- status: %s\n", result.Status)
	fmt.Fprintf(&b, "- artifact_dir: `%s`\n\n", result.ArtifactDir)
	fmt.Fprintf(&b, "## Checks\n\n")
	fmt.Fprintf(&b, "| Check | Target | Status | Detail | Artifact |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- | --- |\n")
	for _, check := range result.Checks {
		detail := check.Detail
		if len(check.Secrets) > 0 {
			detail = strings.TrimSpace(detail + " " + strings.Join(check.Secrets, ", "))
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | `%s` |\n", check.Name, check.Target, check.Status, markdownCell(detail), check.Artifact)
	}
	return b.String()
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "`" + value + "`"
}
