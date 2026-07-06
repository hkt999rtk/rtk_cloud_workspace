package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type lkeCoturnVM struct {
	ID       int    `json:"id,omitempty"`
	Name     string `json:"name"`
	Label    string `json:"label"`
	PublicIP string `json:"public_ip"`
	Region   string `json:"region,omitempty"`
	Type     string `json:"type,omitempty"`
	Status   string `json:"status,omitempty"`
	Domain   string `json:"domain"`
	Role     string `json:"role"`
}

func lkeEnsureExternalCoturnVM(paths provisionPaths, env map[string]string, opts provisionOptions) (lkeCoturnVM, error) {
	if err := lkeValidateCoturnVMConfig(env); err != nil {
		return lkeCoturnVM{}, err
	}
	if strings.TrimSpace(env["LKE_COTURN_VM_INDEX"]) == "" {
		env = lkeCoturnVMEnvForIndex(env, 1)
	}
	vm, installMode, err := lkeResolveCoturnVM(paths, env, opts)
	if err != nil {
		return lkeCoturnVM{}, err
	}
	vm.Domain = lkeCoturnDomain(env)
	vm.Role = "coturn-vm"
	config := renderLKECoturnConfig(env, "<redacted>")
	install := renderLKECoturnInstallScript(env)
	validation := map[string]any{
		"schema":       "rtk-cloud-workspace.coturn-vm-validation/v1",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"install_mode": installMode,
		"coturn_vm":    vm,
		"ports": map[string]any{
			"turn_udp": 3478,
			"turn_tcp": 3478,
			"relay_udp_range": fmt.Sprintf("%s-%s",
				firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"),
				firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535")),
		},
	}
	if err := writeLKECoturnVMArtifacts(paths, vm, config, install, validation); err != nil {
		return lkeCoturnVM{}, err
	}
	if installMode == "skip-existing-ip" {
		return vm, nil
	}
	if err := lkeInstallExternalCoturnVM(paths, opts, vm, install); err != nil {
		return lkeCoturnVM{}, err
	}
	return vm, nil
}

func lkeEnsureExternalCoturnVMs(paths provisionPaths, env map[string]string, opts provisionOptions) ([]lkeCoturnVM, error) {
	if err := lkeValidateCoturnVMConfig(env); err != nil {
		return nil, err
	}
	if err := lkePruneExtraCoturnVMs(paths, env); err != nil {
		return nil, err
	}
	count := lkeCoturnVMCount(env)
	vms := make([]lkeCoturnVM, 0, count)
	for index := 1; index <= count; index++ {
		vm, err := lkeEnsureExternalCoturnVM(paths, lkeCoturnVMEnvForIndex(env, index), opts)
		if err != nil {
			return nil, err
		}
		vms = append(vms, vm)
	}
	if err := writeLKECoturnVMsSummary(paths, vms); err != nil {
		return nil, err
	}
	return vms, nil
}

func lkePruneExtraCoturnVMs(paths provisionPaths, env map[string]string) error {
	if firstNonEmpty(os.Getenv("LKE_COTURN_VM_PUBLIC_IP"), env["LKE_COTURN_VM_PUBLIC_IP"], os.Getenv("LKE_COTURN_VM_PUBLIC_IPS"), env["LKE_COTURN_VM_PUBLIC_IPS"]) != "" {
		return nil
	}
	token := resolveLinodeToken(paths.EnvRoot)
	if token == "" {
		return nil
	}
	out, err := linodeRequestRaw(token, "GET", "/linode/instances?page_size=500", "")
	if err != nil {
		return err
	}
	var listed struct {
		Data []linodeInstance `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return err
	}
	stack := firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")
	prefix := stack + "-" + firstNonEmpty(os.Getenv("LKE_COTURN_VM_NAME_PREFIX"), env["LKE_COTURN_VM_NAME_PREFIX"], "turn")
	desired := lkeCoturnVMCount(env)
	for _, item := range listed.Data {
		if !contains(item.Tags, stack) || !contains(item.Tags, "coturn-vm") {
			continue
		}
		if !strings.HasPrefix(item.Label, prefix) {
			continue
		}
		indexText := strings.TrimPrefix(item.Label, prefix)
		index, err := strconv.Atoi(indexText)
		if err != nil || index <= desired {
			continue
		}
		fmt.Fprintf(os.Stdout, "[lke] deleting extra coturn VM outside desired count: label=%s id=%d desired_count=%d\n", item.Label, item.ID, desired)
		if _, err := linodeRequestRaw(token, "DELETE", fmt.Sprintf("/linode/instances/%d", item.ID), ""); err != nil {
			return err
		}
	}
	return nil
}

func lkeResolveCoturnVM(paths provisionPaths, env map[string]string, opts provisionOptions) (lkeCoturnVM, string, error) {
	publicIP := firstNonEmpty(os.Getenv("LKE_COTURN_VM_PUBLIC_IP"), env["LKE_COTURN_VM_PUBLIC_IP"])
	if publicIP != "" {
		if net.ParseIP(publicIP) == nil {
			return lkeCoturnVM{}, "", fmt.Errorf("LKE_COTURN_VM_PUBLIC_IP is not a valid IP: %s", publicIP)
		}
		return lkeCoturnVM{
			Name:     lkeCoturnVMName(env),
			Label:    lkeCoturnVMLabel(env),
			PublicIP: publicIP,
			Region:   firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
			Type:     firstNonEmpty(os.Getenv("LKE_COTURN_VM_TYPE"), env["LKE_COTURN_VM_TYPE"], "g6-nanode-1"),
			Status:   "provided",
		}, "skip-existing-ip", nil
	}
	token := resolveLinodeToken(paths.EnvRoot)
	if token == "" {
		return lkeCoturnVM{}, "", errors.New("LINODE_TOKEN or LKE_COTURN_VM_PUBLIC_IP is required for external coturn VM")
	}
	vm, found, err := lkeFindCoturnVM(token, env)
	if err != nil {
		return lkeCoturnVM{}, "", err
	}
	if !found {
		vm, err = lkeCreateCoturnVM(token, env, opts)
		if err != nil {
			return lkeCoturnVM{}, "", err
		}
	}
	vm, err = lkeWaitForCoturnVM(token, vm.ID, env)
	if err != nil {
		return lkeCoturnVM{}, "", err
	}
	if vm.PublicIP == "" {
		return lkeCoturnVM{}, "", errors.New("coturn VM has no public IPv4")
	}
	return vm, "linode-vm", nil
}

func lkeCoturnVMLabel(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_COTURN_VM_LABEL"), env["LKE_COTURN_VM_LABEL"], firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")+"-"+lkeCoturnVMName(env))
}

func lkeCoturnVMName(env map[string]string) string {
	name := firstNonEmpty(os.Getenv("LKE_COTURN_VM_NAME"), env["LKE_COTURN_VM_NAME"])
	if name != "" {
		return name
	}
	prefix := firstNonEmpty(os.Getenv("LKE_COTURN_VM_NAME_PREFIX"), env["LKE_COTURN_VM_NAME_PREFIX"], "turn")
	index := envIntFrom(env, "LKE_COTURN_VM_INDEX", 1)
	if index < 1 {
		index = 1
	}
	return fmt.Sprintf("%s%02d", prefix, index)
}

func lkeCoturnVideoDomain(env map[string]string) string {
	return firstNonEmpty(env["VIDEO_CLOUD_DOMAIN"], firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")+"."+firstNonEmpty(env["CLOUD_DNS_ROOT_DOMAIN"], "realtekconnect.com"))
}

func lkeCoturnDomain(env map[string]string) string {
	if lkeCoturnVMCount(env) > 1 {
		index := envIntFrom(env, "LKE_COTURN_VM_INDEX", 1)
		if index < 1 {
			index = 1
		}
		return lkeCoturnDomainForIndex(env, index)
	}
	return firstNonEmpty(os.Getenv("LKE_COTURN_DOMAIN"), env["LKE_COTURN_DOMAIN"], "turn."+lkeCoturnVideoDomain(env))
}

func lkeCoturnDomainForIndex(env map[string]string, index int) string {
	if index <= 1 && lkeCoturnVMCount(env) <= 1 {
		return lkeCoturnDomain(env)
	}
	prefix := firstNonEmpty(os.Getenv("LKE_COTURN_DOMAIN_PREFIX"), env["LKE_COTURN_DOMAIN_PREFIX"], "turn")
	return fmt.Sprintf("%s%02d.%s", prefix, index, lkeCoturnVideoDomain(env))
}

func lkeTurnRegistryPublicDomain(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_TURN_REGISTRY_PUBLIC_DOMAIN"), env["LKE_TURN_REGISTRY_PUBLIC_DOMAIN"], "turnregistry."+lkeCoturnVideoDomain(env))
}

func lkeTurnRegistryPublicURL(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_TURN_REGISTRY_PUBLIC_URL"), env["LKE_TURN_REGISTRY_PUBLIC_URL"], "https://"+lkeTurnRegistryPublicDomain(env))
}

func lkeCoturnVMCount(env map[string]string) int {
	count := envIntFrom(env, "LKE_COTURN_VM_COUNT", 1)
	if count < 0 {
		return 0
	}
	return count
}

func lkeCoturnVMEnvForIndex(env map[string]string, index int) map[string]string {
	out := map[string]string{}
	for key, value := range env {
		out[key] = value
	}
	out["LKE_COTURN_VM_INDEX"] = strconv.Itoa(index)
	if lkeCoturnVMCount(env) > 1 {
		out["LKE_COTURN_DOMAIN"] = lkeCoturnDomainForIndex(env, index)
		if ips := parseCSV(firstNonEmpty(env["LKE_COTURN_VM_PUBLIC_IPS"], os.Getenv("LKE_COTURN_VM_PUBLIC_IPS"))); len(ips) >= index {
			out["LKE_COTURN_VM_PUBLIC_IP"] = ips[index-1]
		} else {
			delete(out, "LKE_COTURN_VM_PUBLIC_IP")
		}
	}
	return out
}

func lkeCoturnSTUNURLs(env map[string]string) string {
	urls := make([]string, 0, lkeCoturnVMCount(env))
	for _, domain := range lkeCoturnDomains(env) {
		urls = append(urls, "stun:"+domain+":3478")
	}
	return strings.Join(urls, ",")
}

func lkeCoturnTURNURLs(env map[string]string) string {
	urls := []string{}
	for _, host := range lkeCoturnDomains(env) {
		urls = append(urls, "turn:"+host+":3478?transport=udp", "turn:"+host+":3478?transport=tcp")
	}
	return strings.Join(urls, ",")
}

func lkeCoturnDomains(env map[string]string) []string {
	count := lkeCoturnVMCount(env)
	if count <= 1 {
		return []string{lkeCoturnDomain(env)}
	}
	domains := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		domains = append(domains, lkeCoturnDomainForIndex(env, index))
	}
	return domains
}

func lkeCoturnRealm(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_COTURN_REALM"), env["LKE_COTURN_REALM"], "video_cloud")
}

func lkeCoturnCredentialTTL(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_COTURN_CREDENTIAL_TTL"), env["LKE_COTURN_CREDENTIAL_TTL"], "10m")
}

func renderLKECoturnConfig(env map[string]string, secret string) string {
	return fmt.Sprintf(`use-auth-secret
static-auth-secret=%s
realm=%s
listening-port=3478
fingerprint
min-port=%s
max-port=%s
no-multicast-peers
log-file=stdout
`,
		secret,
		lkeCoturnRealm(env),
		firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"),
		firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535"),
	)
}

func renderLKECoturnInstallScript(env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#!/usr/bin/env bash\nset -euo pipefail\n")
	fmt.Fprintf(&b, ": \"${VIDEO_CLOUD_COTURN_SHARED_SECRET:?VIDEO_CLOUD_COTURN_SHARED_SECRET is required}\"\n")
	fmt.Fprintf(&b, "export DEBIAN_FRONTEND=noninteractive\n")
	fmt.Fprintf(&b, "apt-get update\n")
	fmt.Fprintf(&b, "apt-get install -y coturn prometheus-node-exporter\n")
	fmt.Fprintf(&b, "install -d -m 0755 /opt/video_cloud/bin\n")
	fmt.Fprintf(&b, "install -m 0755 /tmp/video-cloud-turnregistrar /opt/video_cloud/bin/turnregistrar\n")
	fmt.Fprintf(&b, "install -d -m 0755 /etc/systemd/system/coturn.service.d\n")
	fmt.Fprintf(&b, "{\n")
	fmt.Fprintf(&b, "  printf '%%s\\n' 'use-auth-secret'\n")
	fmt.Fprintf(&b, "  printf 'static-auth-secret=%%s\\n' \"$VIDEO_CLOUD_COTURN_SHARED_SECRET\"\n")
	fmt.Fprintf(&b, "  cat <<'COTURN_TAIL'\n")
	fmt.Fprintf(&b, "realm=%s\nlistening-port=3478\nfingerprint\nmin-port=%s\nmax-port=%s\nno-multicast-peers\nlog-file=stdout\nCOTURN_TAIL\n",
		lkeCoturnRealm(env),
		firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"),
		firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535"))
	fmt.Fprintf(&b, "} >/etc/turnserver.conf\n")
	fmt.Fprintf(&b, "chmod 0640 /etc/turnserver.conf\n")
	fmt.Fprintf(&b, "{\n")
	fmt.Fprintf(&b, "  printf '%%s\\n' 'VIDEO_CLOUD_ENV=staging'\n")
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_REGISTRY_ADDR=%%s\\n' %s\n", shellQuote(lkeTurnRegistryPublicURL(env)))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=%%s\\n' \"$VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY\"\n")
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_ID=%%s\\n' %s\n", shellQuote(lkeCoturnVMName(env)))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_REGION=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_VM_REGION"), env["LKE_COTURN_VM_REGION"], env["CLOUD_REGION"], "us-sea")))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_ZONE=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_VM_ZONE"), env["LKE_COTURN_VM_ZONE"], firstNonEmpty(os.Getenv("LKE_COTURN_VM_REGION"), env["LKE_COTURN_VM_REGION"], env["CLOUD_REGION"], "us-sea"))))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_PUBLIC_HOST=%%s\\n' %s\n", shellQuote(lkeCoturnDomain(env)))
	fmt.Fprintf(&b, "  printf '%%s\\n' 'VIDEO_CLOUD_TURN_NODE_UDP_PORT=3478'\n")
	fmt.Fprintf(&b, "  printf '%%s\\n' 'VIDEO_CLOUD_TURN_NODE_TCP_PORT=3478'\n")
	fmt.Fprintf(&b, "  printf '%%s\\n' 'VIDEO_CLOUD_TURN_NODE_TLS_PORT=0'\n")
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_MAX_SESSIONS=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_MAX_SESSIONS"), env["LKE_COTURN_MAX_SESSIONS"], "6000")))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_WEIGHT=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_NODE_WEIGHT"), env["LKE_COTURN_NODE_WEIGHT"], "100")))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_SECRET_VERSION=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_SECRET_VERSION"), env["LKE_COTURN_SECRET_VERSION"], "1")))
	fmt.Fprintf(&b, "  printf 'VIDEO_CLOUD_TURN_NODE_HEARTBEAT_INTERVAL=%%s\\n' %s\n", shellQuote(firstNonEmpty(os.Getenv("LKE_COTURN_HEARTBEAT_INTERVAL"), env["LKE_COTURN_HEARTBEAT_INTERVAL"], "10s")))
	fmt.Fprintf(&b, "} >/etc/video-cloud-turnregistrar.env\n")
	fmt.Fprintf(&b, "chmod 0600 /etc/video-cloud-turnregistrar.env\n")
	fmt.Fprintf(&b, "cat >/etc/systemd/system/video-cloud-turnregistrar.service <<'TURNREGISTRAR_UNIT'\n")
	fmt.Fprintf(&b, "[Unit]\nDescription=Video Cloud TURN registrar\nAfter=network-online.target coturn.service\nWants=network-online.target coturn.service\n\n[Service]\nType=simple\nEnvironmentFile=/etc/video-cloud-turnregistrar.env\nExecStart=/opt/video_cloud/bin/turnregistrar\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=multi-user.target\nTURNREGISTRAR_UNIT\n")
	fmt.Fprintf(&b, "if [ -d /etc/default ]; then printf 'TURNSERVER_ENABLED=1\\n' >/etc/default/coturn; fi\n")
	fmt.Fprintf(&b, "systemctl daemon-reload\n")
	fmt.Fprintf(&b, "systemctl enable --now coturn\n")
	fmt.Fprintf(&b, "systemctl restart coturn\n")
	fmt.Fprintf(&b, "systemctl is-active coturn >/dev/null\n")
	fmt.Fprintf(&b, "ss -lnup | grep -E '[:.]3478\\b' >/dev/null\n")
	fmt.Fprintf(&b, "ss -lntp | grep -E '[:.]3478\\b' >/dev/null\n")
	fmt.Fprintf(&b, "systemctl enable --now prometheus-node-exporter || true\n")
	fmt.Fprintf(&b, "systemctl enable video-cloud-turnregistrar\n")
	fmt.Fprintf(&b, "for attempt in $(seq 1 10); do\n")
	fmt.Fprintf(&b, "  systemctl restart video-cloud-turnregistrar\n")
	fmt.Fprintf(&b, "  sleep 3\n")
	fmt.Fprintf(&b, "  if journalctl -u video-cloud-turnregistrar --since '2 minutes ago' --no-pager | grep -q 'turn registry register succeeded'; then break; fi\n")
	fmt.Fprintf(&b, "done\n")
	fmt.Fprintf(&b, "systemctl is-active video-cloud-turnregistrar >/dev/null\n")
	fmt.Fprintf(&b, "journalctl -u video-cloud-turnregistrar --since '2 minutes ago' --no-pager | grep -q 'turn registry register succeeded'\n")
	return b.String()
}

func writeLKECoturnVMArtifacts(paths provisionPaths, vm lkeCoturnVM, config, install string, validation map[string]any) error {
	dir := filepath.Join(paths.EnvRoot, "artifacts", "coturn-vm", vm.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "coturn-vm.json"), map[string]any{
		"schema":    "rtk-cloud-workspace.coturn-vm/v1",
		"generated": time.Now().UTC().Format(time.RFC3339),
		"coturn_vm": vm,
		"name":      vm.Name,
		"public_ip": vm.PublicIP,
		"domain":    vm.Domain,
		"role":      vm.Role,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "turnserver.conf.redacted"), []byte(config), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(install), 0o700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "validation.json"), validation); err != nil {
		return err
	}
	if vm.Name == "turn01" {
		legacyDir := filepath.Join(paths.EnvRoot, "artifacts", "coturn-vm")
		if err := writeJSON(filepath.Join(legacyDir, "coturn-vm.json"), map[string]any{
			"schema":    "rtk-cloud-workspace.coturn-vm/v1",
			"generated": time.Now().UTC().Format(time.RFC3339),
			"coturn_vm": vm,
			"name":      vm.Name,
			"public_ip": vm.PublicIP,
			"domain":    vm.Domain,
			"role":      vm.Role,
		}); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(legacyDir, "turnserver.conf.redacted"), []byte(config), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(legacyDir, "install.sh"), []byte(install), 0o700); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(legacyDir, "validation.json"), validation); err != nil {
			return err
		}
	}
	return nil
}

func writeLKECoturnVMsSummary(paths provisionPaths, vms []lkeCoturnVM) error {
	dir := filepath.Join(paths.EnvRoot, "artifacts", "coturn-vm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "coturn-vms.json"), map[string]any{
		"schema":     "rtk-cloud-workspace.coturn-vms/v1",
		"generated":  time.Now().UTC().Format(time.RFC3339),
		"coturn_vms": vms,
		"count":      len(vms),
	})
}

func lkeInstallExternalCoturnVM(paths provisionPaths, opts provisionOptions, vm lkeCoturnVM, install string) error {
	sshKey := opts.sshKey
	if sshKey == "" {
		sshKey = defaultStagingSSHKey()
	}
	deadline := time.Now().Add(envDurationDefault("LKE_COTURN_VM_SSH_TIMEOUT", 5*time.Minute))
	var lastErr error
	for time.Now().Before(deadline) {
		if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, vm.PublicIP, "true")...); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("coturn VM SSH did not become ready: %w", lastErr)
	}
	binary, err := buildLKECoturnRegistrarBinary(paths)
	if err != nil {
		return err
	}
	if err := copyLKECoturnRegistrarBinary(paths, sshKey, vm.PublicIP, binary); err != nil {
		return err
	}
	turnSecret := shellQuote(lkeRuntimeSecretValue("turn-shared"))
	registrySecret := shellQuote(lkeRuntimeSecretValue("turn-registry-node-auth"))
	return runCmdWithInput("", install, "ssh", loggerSSHArgs(paths, sshKey, vm.PublicIP, "env", "VIDEO_CLOUD_COTURN_SHARED_SECRET="+turnSecret, "VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY="+registrySecret, "bash", "-s")...)
}

func buildLKECoturnRegistrarBinary(paths provisionPaths) (string, error) {
	repo := filepath.Join(paths.Workspace, "repos", "rtk_video_cloud")
	outDir := filepath.Join(paths.EnvRoot, "artifacts", "coturn-vm")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, "turnregistrar")
	goBin := firstNonEmpty(os.Getenv("RTK_CLOUD_GO"), "go")
	cmd := exec.Command(goBin, "build", "-trimpath", "-o", out, "./cmd/turnregistrar")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOWORK=off")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build turnregistrar: %w", err)
	}
	return out, nil
}

func copyLKECoturnRegistrarBinary(paths provisionPaths, sshKey, host, localPath string) error {
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
	args = append(args, localPath, "root@"+host+":/tmp/video-cloud-turnregistrar")
	if err := runCmd("", "scp", args...); err != nil {
		return fmt.Errorf("copy turnregistrar to coturn VM: %w", err)
	}
	return nil
}

func lkeFindCoturnVM(token string, env map[string]string) (lkeCoturnVM, bool, error) {
	out, err := linodeRequestRaw(token, "GET", "/linode/instances?page_size=500", "")
	if err != nil {
		return lkeCoturnVM{}, false, err
	}
	var listed struct {
		Data []linodeInstance `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return lkeCoturnVM{}, false, err
	}
	label := lkeCoturnVMLabel(env)
	for _, item := range listed.Data {
		if item.Label == label {
			return lkeCoturnVMFromLinode(item, env), true, nil
		}
	}
	return lkeCoturnVM{}, false, nil
}

func lkeCreateCoturnVM(token string, env map[string]string, opts provisionOptions) (lkeCoturnVM, error) {
	publicKeyPath := opts.sshKey + ".pub"
	if opts.sshKey == "" {
		publicKeyPath = defaultStagingSSHKey() + ".pub"
	}
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return lkeCoturnVM{}, fmt.Errorf("read coturn VM SSH public key %s: %w", publicKeyPath, err)
	}
	payload, err := json.Marshal(map[string]any{
		"label":           lkeCoturnVMLabel(env),
		"region":          firstNonEmpty(os.Getenv("LKE_COTURN_VM_REGION"), env["LKE_COTURN_VM_REGION"], env["CLOUD_REGION"], "us-sea"),
		"type":            firstNonEmpty(os.Getenv("LKE_COTURN_VM_TYPE"), env["LKE_COTURN_VM_TYPE"], "g6-nanode-1"),
		"image":           firstNonEmpty(os.Getenv("LKE_COTURN_VM_IMAGE"), env["LKE_COTURN_VM_IMAGE"], "linode/ubuntu24.04"),
		"authorized_keys": []string{strings.TrimSpace(string(publicKey))},
		"tags":            []string{"rtk-cloud", firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"), "coturn-vm"},
	})
	if err != nil {
		return lkeCoturnVM{}, err
	}
	out, err := linodeRequestRaw(token, "POST", "/linode/instances", string(payload))
	if err != nil {
		if isLinodeActiveServicesLimitError(err) {
			return lkeCoturnVM{}, fmt.Errorf("Linode active services limit reached while creating coturn VM %q; delete unused Linode services or request a quota increase before rerunning staging provision: %w", lkeCoturnVMLabel(env), err)
		}
		return lkeCoturnVM{}, err
	}
	var created linodeInstance
	if err := json.Unmarshal(out, &created); err != nil {
		return lkeCoturnVM{}, err
	}
	if created.ID == 0 {
		return lkeCoturnVM{}, errors.New("coturn VM create response did not include id")
	}
	return lkeCoturnVMFromLinode(created, env), nil
}

func lkeWaitForCoturnVM(token string, id int, env map[string]string) (lkeCoturnVM, error) {
	if id == 0 {
		return lkeCoturnVM{}, errors.New("coturn VM id is required")
	}
	timeout := envDurationDefault("LKE_COTURN_VM_BOOT_TIMEOUT", 10*time.Minute)
	deadline := time.Now().Add(timeout)
	var last lkeCoturnVM
	for {
		out, err := linodeRequestRaw(token, "GET", fmt.Sprintf("/linode/instances/%d", id), "")
		if err == nil {
			var item linodeInstance
			if unmarshalErr := json.Unmarshal(out, &item); unmarshalErr != nil {
				return lkeCoturnVM{}, unmarshalErr
			}
			last = lkeCoturnVMFromLinode(item, env)
			if last.Status == "running" && last.PublicIP != "" {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			return lkeCoturnVM{}, fmt.Errorf("coturn VM did not become running before timeout; last=%+v", last)
		}
		time.Sleep(5 * time.Second)
	}
}

func lkeCoturnVMFromLinode(item linodeInstance, env map[string]string) lkeCoturnVM {
	var publicIP string
	for _, raw := range item.IPv4 {
		ip := net.ParseIP(raw)
		if ip == nil || isPrivateNetIP(ip) {
			continue
		}
		publicIP = raw
		break
	}
	return lkeCoturnVM{
		ID:       item.ID,
		Name:     lkeCoturnVMName(env),
		Label:    item.Label,
		PublicIP: publicIP,
		Region:   item.Region,
		Type:     item.Type,
		Status:   item.Status,
		Domain:   lkeCoturnDomain(env),
		Role:     "coturn-vm",
	}
}

func lkeSyncCoturnDNS(paths provisionPaths, env map[string]string, opts provisionOptions, vm lkeCoturnVM) error {
	if vm.PublicIP == "" || vm.Domain == "" {
		return nil
	}
	if err := godaddyUpsert(paths, env["CLOUD_DNS_ROOT_DOMAIN"], opts.godaddyEnv, opts.operatorEnv, vm.Domain, vm.PublicIP, opts.dnsFinalTTL); err != nil {
		return err
	}
	return waitDNS(vm.Domain, vm.PublicIP, env["CLOUD_DNS_ROOT_DOMAIN"], opts)
}

func lkeSyncCoturnVMsDNS(paths provisionPaths, env map[string]string, opts provisionOptions, vms []lkeCoturnVM) error {
	for _, vm := range vms {
		if err := lkeSyncCoturnDNS(paths, env, opts, vm); err != nil {
			return err
		}
	}
	return nil
}

func lkeValidateCoturnVMConfig(env map[string]string) error {
	count := lkeCoturnVMCount(env)
	if count < 1 || count > 5 {
		return fmt.Errorf("LKE_COTURN_VM_COUNT must be between 1 and 5 for coturn VM staging, got %d", count)
	}
	if count > 1 && firstNonEmpty(os.Getenv("LKE_COTURN_VM_NAME"), env["LKE_COTURN_VM_NAME"], os.Getenv("LKE_COTURN_VM_LABEL"), env["LKE_COTURN_VM_LABEL"]) != "" {
		return errors.New("LKE_COTURN_VM_NAME and LKE_COTURN_VM_LABEL are only supported when LKE_COTURN_VM_COUNT=1")
	}
	if count > 1 && firstNonEmpty(os.Getenv("LKE_COTURN_VM_PUBLIC_IP"), env["LKE_COTURN_VM_PUBLIC_IP"]) != "" && firstNonEmpty(os.Getenv("LKE_COTURN_VM_PUBLIC_IPS"), env["LKE_COTURN_VM_PUBLIC_IPS"]) == "" {
		return errors.New("LKE_COTURN_VM_PUBLIC_IPS is required for multi-coturn provided-IP mode; omit LKE_COTURN_VM_PUBLIC_IP to let Linode provisioning create VMs")
	}
	if count > 1 {
		for _, ip := range parseCSV(firstNonEmpty(os.Getenv("LKE_COTURN_VM_PUBLIC_IPS"), env["LKE_COTURN_VM_PUBLIC_IPS"])) {
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("LKE_COTURN_VM_PUBLIC_IPS contains invalid IP: %s", ip)
			}
		}
	}
	minPort, minErr := strconv.Atoi(firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"))
	maxPort, maxErr := strconv.Atoi(firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535"))
	if minErr != nil || maxErr != nil || minPort < 1 || maxPort < minPort || maxPort > 65535 {
		return fmt.Errorf("invalid coturn relay port range: LKE_COTURN_MIN_PORT=%q LKE_COTURN_MAX_PORT=%q", firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"), firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535"))
	}
	return nil
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
