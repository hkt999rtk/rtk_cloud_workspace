package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type lkeEdgeHAProxyVM struct {
	ID        int    `json:"id,omitempty"`
	Label     string `json:"label"`
	PublicIP  string `json:"public_ip"`
	PrivateIP string `json:"private_ip,omitempty"`
	Region    string `json:"region,omitempty"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
}

type lkeEdgeHAProxyUpstream struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Kind    string `json:"kind"`
}

type lkeKubernetesNodeInternalIP struct {
	Name string
	IP   string
}

type lkeEdgeHAProxyPlan struct {
	Mode          string                   `json:"mode"`
	EdgeVMs       []lkeEdgeHAProxyVM       `json:"edge_vms"`
	Upstreams     []lkeEdgeHAProxyUpstream `json:"upstreams"`
	HTTPSNodePort int                      `json:"https_node_port"`
	MQTTSNodePort int                      `json:"mqtts_node_port"`
	MaxConn       int                      `json:"maxconn"`
	ProxyProtocol bool                     `json:"proxy_protocol"`
}

type lkeEdgeHAProxySSHAccess struct {
	User          string `json:"user"`
	KeyPath       string `json:"key_path"`
	PublicKeyPath string `json:"public_key_path"`
}

func lkeValidatePublicEdge(env map[string]string) error {
	mode := firstNonEmpty(os.Getenv("LKE_PUBLIC_EDGE_MODE"), env["LKE_PUBLIC_EDGE_MODE"], "external-haproxy")
	if mode != "external-haproxy" {
		return fmt.Errorf("unsupported LKE_PUBLIC_EDGE_MODE=%s; only external-haproxy is supported", mode)
	}
	countRaw := firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_COUNT"), env["LKE_EDGE_HAPROXY_COUNT"], "1")
	count, err := strconv.Atoi(strings.TrimSpace(countRaw))
	if err != nil || count < 1 {
		return errors.New("LKE_EDGE_HAPROXY_COUNT must be a positive integer")
	}
	if count > 1 {
		return errors.New("LKE_EDGE_HAPROXY_COUNT>1 is not supported by external HAProxy edge v1")
	}
	return nil
}

func lkeIngressHTTPSNodePort(env map[string]string) int {
	return envNodePortDefault("LKE_INGRESS_HTTPS_NODEPORT", env, 30443)
}

func lkeMQTTPublicNodePort(env map[string]string) int {
	return envNodePortDefault("LKE_MQTT_PUBLIC_MQTTS_NODEPORT", env, 31883)
}

func envNodePortDefault(key string, env map[string]string, fallback int) int {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv(key), env[key], strconv.Itoa(fallback)))
	value, err := strconv.Atoi(raw)
	if err != nil || value < 30000 || value > 32767 {
		return fallback
	}
	return value
}

func lkeEnsureExternalHAProxyEdge(paths provisionPaths, env map[string]string, opts provisionOptions) (lkeEdgeHAProxyVM, error) {
	if err := lkeValidatePublicEdge(env); err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	upstreams, err := lkeEdgeHAProxyUpstreams(env)
	if err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	edge, installMode, err := lkeResolveEdgeHAProxyVM(paths, env, opts)
	if err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	plan := lkeEdgeHAProxyPlan{
		Mode:          "external-haproxy",
		EdgeVMs:       []lkeEdgeHAProxyVM{edge},
		Upstreams:     upstreams,
		HTTPSNodePort: lkeIngressHTTPSNodePort(env),
		MQTTSNodePort: lkeMQTTPublicNodePort(env),
		MaxConn:       envIntDefault("LKE_EDGE_HAPROXY_MAXCONN", 200000),
		ProxyProtocol: lkeEnvBool("LKE_EDGE_HAPROXY_ENABLE_PROXY_PROTOCOL"),
	}
	cfg := renderLKEEdgeHAProxyConfig(plan)
	install := renderLKEEdgeHAProxyInstallScript(plan, cfg)
	sshAccess := lkeEdgeHAProxySSHAccessFor(opts)
	validation := map[string]any{
		"schema":         "rtk-cloud-workspace.edge-haproxy-validation/v1",
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"install_mode":   installMode,
		"edge_public_ip": edge.PublicIP,
		"edge_vms":       plan.EdgeVMs,
		"ssh_access":     sshAccess,
	}
	if err := writeLKEEdgeHAProxyArtifacts(paths, plan, cfg, install, validation, sshAccess); err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	if installMode == "skip-existing-ip" {
		return edge, nil
	}
	if err := lkeInstallExternalHAProxyEdge(paths, opts, edge, install); err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	return edge, nil
}

func lkeResolveEdgeHAProxyVM(paths provisionPaths, env map[string]string, opts provisionOptions) (lkeEdgeHAProxyVM, string, error) {
	publicIP := firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_PUBLIC_IP"), env["LKE_EDGE_HAPROXY_PUBLIC_IP"])
	if publicIP != "" {
		if net.ParseIP(publicIP) == nil {
			return lkeEdgeHAProxyVM{}, "", fmt.Errorf("LKE_EDGE_HAPROXY_PUBLIC_IP is not a valid IP: %s", publicIP)
		}
		return lkeEdgeHAProxyVM{
			Label:     lkeEdgeHAProxyLabel(env),
			PublicIP:  publicIP,
			PrivateIP: firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_PRIVATE_IP"), env["LKE_EDGE_HAPROXY_PRIVATE_IP"]),
			Region:    firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
			Type:      firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_TYPE"), env["LKE_EDGE_HAPROXY_TYPE"], "g6-standard-2"),
			Status:    "provided",
		}, "skip-existing-ip", nil
	}
	token := resolveLinodeToken(paths.EnvRoot)
	if token == "" {
		return lkeEdgeHAProxyVM{}, "", errors.New("LINODE_TOKEN or LKE_EDGE_HAPROXY_PUBLIC_IP is required for external HAProxy edge")
	}
	edge, found, err := lkeFindEdgeHAProxyVM(token, env)
	if err != nil {
		return lkeEdgeHAProxyVM{}, "", err
	}
	if !found {
		edge, err = lkeCreateEdgeHAProxyVM(token, env, opts)
		if err != nil {
			return lkeEdgeHAProxyVM{}, "", err
		}
	} else if edge.PrivateIP == "" {
		if err := lkeAllocatePrivateIPv4(token, edge.ID); err != nil {
			return lkeEdgeHAProxyVM{}, "", err
		}
	}
	edge, err = lkeWaitForEdgeHAProxyVM(token, edge.ID)
	if err != nil {
		return lkeEdgeHAProxyVM{}, "", err
	}
	if edge.PublicIP == "" {
		return lkeEdgeHAProxyVM{}, "", errors.New("HAProxy edge VM has no public IPv4")
	}
	return edge, "linode-vm", nil
}

func lkeAllocatePrivateIPv4(token string, id int) error {
	if id == 0 {
		return errors.New("HAProxy edge VM id is required to allocate private IPv4")
	}
	_, err := linodeRequestRaw(token, "POST", fmt.Sprintf("/linode/instances/%d/ips", id), `{"type":"ipv4","public":false}`)
	return err
}

func lkeEdgeHAProxyLabel(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_LABEL"), env["LKE_EDGE_HAPROXY_LABEL"], firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")+"-edge-haproxy-01")
}

func lkeEdgeHAProxyUpstreams(env map[string]string) ([]lkeEdgeHAProxyUpstream, error) {
	nodes, err := lkeKubernetesNodeInternalIPsByName()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("no LKE node InternalIP addresses found for HAProxy upstreams")
	}
	mqttNodes, err := lkeMQTTNodeInternalIPs(env, nodes)
	if err != nil {
		return nil, err
	}
	if len(mqttNodes) == 0 {
		mqttNodes = nodes
	}
	out := make([]lkeEdgeHAProxyUpstream, 0, len(nodes)+len(mqttNodes))
	for i, node := range nodes {
		name := fmt.Sprintf("lke-node-%d", i+1)
		out = append(out, lkeEdgeHAProxyUpstream{Name: name, Address: node.IP, Port: lkeIngressHTTPSNodePort(env), Kind: "https"})
	}
	for i, node := range mqttNodes {
		name := fmt.Sprintf("mqtt-node-%d", i+1)
		out = append(out, lkeEdgeHAProxyUpstream{Name: name, Address: node.IP, Port: lkeMQTTPublicNodePort(env), Kind: "mqtts"})
	}
	return out, nil
}

func lkeKubernetesNodeInternalIPs() ([]string, error) {
	nodes, err := lkeKubernetesNodeInternalIPsByName()
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ips = append(ips, node.IP)
	}
	return ips, nil
}

func lkeKubernetesNodeInternalIPsByName() ([]lkeKubernetesNodeInternalIP, error) {
	out, err := kubectlCombinedOutput(nil, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("get LKE nodes for HAProxy upstreams: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var nodes []lkeKubernetesNodeInternalIP
	for _, item := range parsed.Items {
		for _, addr := range item.Status.Addresses {
			if addr.Type != "InternalIP" || net.ParseIP(addr.Address) == nil || seen[addr.Address] {
				continue
			}
			seen[addr.Address] = true
			nodes = append(nodes, lkeKubernetesNodeInternalIP{Name: item.Metadata.Name, IP: addr.Address})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Name == nodes[j].Name {
			return nodes[i].IP < nodes[j].IP
		}
		if nodes[i].Name == "" {
			return false
		}
		if nodes[j].Name == "" {
			return true
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes, nil
}

func lkeMQTTNodeInternalIPs(env map[string]string, nodes []lkeKubernetesNodeInternalIP) ([]lkeKubernetesNodeInternalIP, error) {
	pods, err := lkeMQTTPods(env)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, nil
	}
	byName := map[string]lkeKubernetesNodeInternalIP{}
	for _, node := range nodes {
		byName[node.Name] = node
	}
	seen := map[string]bool{}
	var out []lkeKubernetesNodeInternalIP
	for _, pod := range pods {
		if pod.KubernetesNodeName == "" || seen[pod.KubernetesNodeName] {
			continue
		}
		node, ok := byName[pod.KubernetesNodeName]
		if !ok || node.IP == "" {
			continue
		}
		seen[pod.KubernetesNodeName] = true
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func renderLKEEdgeHAProxyConfig(plan lkeEdgeHAProxyPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "global\n")
	fmt.Fprintf(&b, "    log /dev/log local0\n")
	fmt.Fprintf(&b, "    maxconn %d\n\n", plan.MaxConn)
	fmt.Fprintf(&b, "defaults\n")
	fmt.Fprintf(&b, "    log global\n")
	fmt.Fprintf(&b, "    mode tcp\n")
	fmt.Fprintf(&b, "    option tcplog\n")
	fmt.Fprintf(&b, "    timeout connect 10s\n")
	fmt.Fprintf(&b, "    timeout client 1h\n")
	fmt.Fprintf(&b, "    timeout server 1h\n\n")
	fmt.Fprintf(&b, "frontend public_https_443\n")
	fmt.Fprintf(&b, "    bind *:443\n")
	fmt.Fprintf(&b, "    default_backend k8s_ingress_https\n\n")
	fmt.Fprintf(&b, "backend k8s_ingress_https\n")
	fmt.Fprintf(&b, "    balance roundrobin\n")
	for _, upstream := range plan.Upstreams {
		if upstream.Kind == "https" {
			fmt.Fprintf(&b, "    server %s %s:%d check%s\n", upstream.Name, upstream.Address, upstream.Port, proxyProtocolSuffix(plan))
		}
	}
	fmt.Fprintf(&b, "\nfrontend public_mqtts_8883\n")
	fmt.Fprintf(&b, "    bind *:8883\n")
	fmt.Fprintf(&b, "    default_backend k8s_mqtts\n\n")
	fmt.Fprintf(&b, "backend k8s_mqtts\n")
	fmt.Fprintf(&b, "    balance roundrobin\n")
	for _, upstream := range plan.Upstreams {
		if upstream.Kind == "mqtts" {
			fmt.Fprintf(&b, "    server %s %s:%d check%s\n", upstream.Name, upstream.Address, upstream.Port, proxyProtocolSuffix(plan))
		}
	}
	return b.String()
}

func proxyProtocolSuffix(plan lkeEdgeHAProxyPlan) string {
	if plan.ProxyProtocol {
		return " send-proxy"
	}
	return ""
}

func renderLKEEdgeHAProxyInstallScript(plan lkeEdgeHAProxyPlan, cfg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#!/usr/bin/env bash\nset -euo pipefail\n")
	fmt.Fprintf(&b, "export DEBIAN_FRONTEND=noninteractive\n")
	fmt.Fprintf(&b, "apt-get update\n")
	fmt.Fprintf(&b, "apt-get install -y haproxy prometheus-node-exporter\n")
	fmt.Fprintf(&b, "cat >/etc/sysctl.d/99-rtk-cloud-edge.conf <<'SYSCTL'\n")
	fmt.Fprintf(&b, "net.core.somaxconn=65535\n")
	fmt.Fprintf(&b, "net.core.netdev_max_backlog=250000\n")
	fmt.Fprintf(&b, "net.ipv4.ip_local_port_range=1024 65535\n")
	fmt.Fprintf(&b, "net.ipv4.tcp_tw_reuse=1\n")
	fmt.Fprintf(&b, "net.ipv4.tcp_fin_timeout=15\n")
	fmt.Fprintf(&b, "net.ipv4.tcp_keepalive_time=300\n")
	fmt.Fprintf(&b, "net.ipv4.tcp_keepalive_intvl=30\n")
	fmt.Fprintf(&b, "net.ipv4.tcp_keepalive_probes=5\n")
	fmt.Fprintf(&b, "net.netfilter.nf_conntrack_max=1048576\n")
	fmt.Fprintf(&b, "SYSCTL\n")
	fmt.Fprintf(&b, "sysctl --system || true\n")
	fmt.Fprintf(&b, "mkdir -p /etc/systemd/system/haproxy.service.d\n")
	fmt.Fprintf(&b, "cat >/etc/systemd/system/haproxy.service.d/10-rtk-cloud-edge.conf <<'SYSTEMD'\n")
	fmt.Fprintf(&b, "[Service]\nLimitNOFILE=1048576\nSYSTEMD\n")
	fmt.Fprintf(&b, "cat >/etc/haproxy/haproxy.cfg <<'HAPROXY'\n%sHAPROXY\n", cfg)
	fmt.Fprintf(&b, "haproxy -c -f /etc/haproxy/haproxy.cfg\n")
	fmt.Fprintf(&b, "systemctl daemon-reload\n")
	fmt.Fprintf(&b, "systemctl enable --now haproxy\n")
	fmt.Fprintf(&b, "systemctl reload haproxy || systemctl restart haproxy\n")
	fmt.Fprintf(&b, "systemctl enable --now prometheus-node-exporter || true\n")
	fmt.Fprintf(&b, "ulimit -n\n")
	fmt.Fprintf(&b, "ss -s || true\n")
	_ = plan
	return b.String()
}

func lkeEdgeHAProxySSHAccessFor(opts provisionOptions) lkeEdgeHAProxySSHAccess {
	keyPath := opts.sshKey
	if keyPath == "" {
		keyPath = defaultStagingSSHKey()
	}
	return lkeEdgeHAProxySSHAccess{
		User:          "root",
		KeyPath:       keyPath,
		PublicKeyPath: keyPath + ".pub",
	}
}

func writeLKEEdgeHAProxyArtifacts(paths provisionPaths, plan lkeEdgeHAProxyPlan, cfg, install string, validation map[string]any, sshAccess lkeEdgeHAProxySSHAccess) error {
	dir := filepath.Join(paths.EnvRoot, "artifacts", "edge-haproxy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "edge-vms.json"), map[string]any{
		"schema":         "rtk-cloud-workspace.edge-haproxy-vms/v1",
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"mode":           plan.Mode,
		"edge_vms":       plan.EdgeVMs,
		"ssh_access":     sshAccess,
		"proxy_protocol": plan.ProxyProtocol,
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "upstreams.json"), map[string]any{
		"schema":          "rtk-cloud-workspace.edge-haproxy-upstreams/v1",
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"https_node_port": plan.HTTPSNodePort,
		"mqtts_node_port": plan.MQTTSNodePort,
		"upstreams":       plan.Upstreams,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "haproxy.cfg"), []byte(cfg), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(install), 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "validation.json"), validation)
}

func lkeInstallExternalHAProxyEdge(paths provisionPaths, opts provisionOptions, edge lkeEdgeHAProxyVM, install string) error {
	sshKey := opts.sshKey
	if sshKey == "" {
		sshKey = defaultStagingSSHKey()
	}
	deadline := time.Now().Add(envDurationDefault("LKE_EDGE_HAPROXY_SSH_TIMEOUT", 5*time.Minute))
	var lastErr error
	for time.Now().Before(deadline) {
		if err := runCmdQuiet("ssh", loggerSSHArgs(paths, sshKey, edge.PublicIP, "true")...); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("HAProxy edge SSH did not become ready: %w", lastErr)
	}
	return runCmdWithInput("", install, "ssh", loggerSSHArgs(paths, sshKey, edge.PublicIP, "bash", "-s")...)
}

func lkeFindEdgeHAProxyVM(token string, env map[string]string) (lkeEdgeHAProxyVM, bool, error) {
	out, err := linodeRequestRaw(token, "GET", "/linode/instances?page_size=500", "")
	if err != nil {
		return lkeEdgeHAProxyVM{}, false, err
	}
	var listed struct {
		Data []linodeInstance `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return lkeEdgeHAProxyVM{}, false, err
	}
	label := lkeEdgeHAProxyLabel(env)
	for _, item := range listed.Data {
		if item.Label == label {
			return lkeEdgeVMFromLinode(item), true, nil
		}
	}
	return lkeEdgeHAProxyVM{}, false, nil
}

type linodeInstance struct {
	ID     int      `json:"id"`
	Label  string   `json:"label"`
	Region string   `json:"region"`
	Type   string   `json:"type"`
	Status string   `json:"status"`
	IPv4   []string `json:"ipv4"`
}

func lkeCreateEdgeHAProxyVM(token string, env map[string]string, opts provisionOptions) (lkeEdgeHAProxyVM, error) {
	publicKeyPath := opts.sshKey + ".pub"
	if opts.sshKey == "" {
		publicKeyPath = defaultStagingSSHKey() + ".pub"
	}
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return lkeEdgeHAProxyVM{}, fmt.Errorf("read HAProxy edge SSH public key %s: %w", publicKeyPath, err)
	}
	payload, err := json.Marshal(map[string]any{
		"label":           lkeEdgeHAProxyLabel(env),
		"region":          firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_REGION"), env["LKE_EDGE_HAPROXY_REGION"], env["CLOUD_REGION"], "us-sea"),
		"type":            firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_TYPE"), env["LKE_EDGE_HAPROXY_TYPE"], "g6-standard-2"),
		"image":           firstNonEmpty(os.Getenv("LKE_EDGE_HAPROXY_IMAGE"), env["LKE_EDGE_HAPROXY_IMAGE"], "linode/ubuntu24.04"),
		"private_ip":      true,
		"authorized_keys": []string{strings.TrimSpace(string(publicKey))},
		"tags":            []string{"rtk-cloud", firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"), "edge-haproxy"},
	})
	if err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	out, err := linodeRequestRaw(token, "POST", "/linode/instances", string(payload))
	if err != nil {
		if isLinodeActiveServicesLimitError(err) {
			return lkeEdgeHAProxyVM{}, fmt.Errorf("Linode active services limit reached while creating HAProxy edge VM %q; delete unused Linode services or request a quota increase before rerunning staging provision: %w", lkeEdgeHAProxyLabel(env), err)
		}
		return lkeEdgeHAProxyVM{}, err
	}
	var created linodeInstance
	if err := json.Unmarshal(out, &created); err != nil {
		return lkeEdgeHAProxyVM{}, err
	}
	if created.ID == 0 {
		return lkeEdgeHAProxyVM{}, errors.New("HAProxy edge VM create response did not include id")
	}
	return lkeEdgeVMFromLinode(created), nil
}

func isLinodeActiveServicesLimitError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "limit for the number of active services")
}

func lkeWaitForEdgeHAProxyVM(token string, id int) (lkeEdgeHAProxyVM, error) {
	if id == 0 {
		return lkeEdgeHAProxyVM{}, errors.New("HAProxy edge VM id is required")
	}
	timeout := envDurationDefault("LKE_EDGE_HAPROXY_BOOT_TIMEOUT", 10*time.Minute)
	deadline := time.Now().Add(timeout)
	var last lkeEdgeHAProxyVM
	for {
		out, err := linodeRequestRaw(token, "GET", fmt.Sprintf("/linode/instances/%d", id), "")
		if err == nil {
			var item linodeInstance
			if unmarshalErr := json.Unmarshal(out, &item); unmarshalErr != nil {
				return lkeEdgeHAProxyVM{}, unmarshalErr
			}
			last = lkeEdgeVMFromLinode(item)
			if last.Status == "running" && last.PublicIP != "" {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			return lkeEdgeHAProxyVM{}, fmt.Errorf("HAProxy edge VM did not become running before timeout; last=%+v", last)
		}
		time.Sleep(5 * time.Second)
	}
}

func lkeEdgeVMFromLinode(item linodeInstance) lkeEdgeHAProxyVM {
	var publicIP, privateIP string
	for _, raw := range item.IPv4 {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		if isPrivateNetIP(ip) {
			if privateIP == "" {
				privateIP = raw
			}
		} else if publicIP == "" {
			publicIP = raw
		}
	}
	return lkeEdgeHAProxyVM{
		ID:        item.ID,
		Label:     item.Label,
		PublicIP:  publicIP,
		PrivateIP: privateIP,
		Region:    item.Region,
		Type:      item.Type,
		Status:    item.Status,
	}
}

func isPrivateNetIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 10 ||
		(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
		(ip4[0] == 192 && ip4[1] == 168)
}
