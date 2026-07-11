package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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
		WaitSeconds:        firstNonEmpty(operatorEnv["GODADDY_DNS_WAIT_SECONDS"], os.Getenv("GODADDY_DNS_WAIT_SECONDS"), "900"),
		PropagationSeconds: firstNonEmpty(operatorEnv["GODADDY_DNS_PROPAGATION_SECONDS"], os.Getenv("GODADDY_DNS_PROPAGATION_SECONDS"), "200"),
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
state_dir="${RTK_CLOUD_CERTBOT_DNS_STATE:-${TMPDIR:-/tmp}/rtk-cloud-certbot-dns-state}"
expected="${RTK_CLOUD_CERTBOT_DNS_EXPECTED:-1}"
mkdir -p "$state_dir/records"
CERTBOT_RECORD="$record" CERTBOT_VALIDATION="$CERTBOT_VALIDATION" CERTBOT_STATE_DIR="$state_dir" python3 - <<'PY'
import hashlib, os, pathlib, urllib.parse
state = pathlib.Path(os.environ["CERTBOT_STATE_DIR"])
record = os.environ["CERTBOT_RECORD"]
validation = os.environ["CERTBOT_VALIDATION"]
record_dir = state / "records" / urllib.parse.quote(record, safe="")
record_dir.mkdir(parents=True, exist_ok=True)
(record_dir / hashlib.sha256(validation.encode()).hexdigest()).write_text(validation)
PY
current_count="$(find "$state_dir/records" -type f | wc -l | tr -d '[:space:]')"
payload="$(CERTBOT_RECORD="$record" CERTBOT_VALIDATION="$CERTBOT_VALIDATION" GODADDY_DNS_TTL="$ttl" python3 - <<'PY'
import json, os
print(json.dumps([{"data": os.environ["CERTBOT_VALIDATION"], "ttl": int(os.environ["GODADDY_DNS_TTL"])}]))
PY
)"
curl --connect-timeout 10 --max-time 30 -fsS -X PUT "$api_root/v1/domains/$zone/records/TXT/$record" \
  -H "Authorization: sso-key $GODADDY_KEY:$GODADDY_SECRET" \
  -H "Content-Type: application/json" \
  --data "$payload" >/dev/null
if [ "$current_count" -lt "$expected" ]; then
  exit 0
fi
batch_file="$state_dir/batch-records.tsv"
CERTBOT_STATE_DIR="$state_dir" GODADDY_DNS_TTL="$ttl" python3 - <<'PY' > "$batch_file"
import json, os, pathlib, urllib.parse
state = pathlib.Path(os.environ["CERTBOT_STATE_DIR"]) / "records"
ttl = int(os.environ["GODADDY_DNS_TTL"])
for record_dir in sorted(p for p in state.iterdir() if p.is_dir()):
    record = urllib.parse.unquote(record_dir.name)
    values = []
    for value_file in sorted(p for p in record_dir.iterdir() if p.is_file()):
        value = value_file.read_text().strip()
        if value and value not in values:
            values.append(value)
    if values:
        print(record + "\t" + json.dumps([{"data": value, "ttl": ttl} for value in values]))
PY
while IFS=$'\t' read -r batch_record batch_payload; do
  curl --connect-timeout 10 --max-time 30 -fsS -X PUT "$api_root/v1/domains/$zone/records/TXT/$batch_record" \
    -H "Authorization: sso-key $GODADDY_KEY:$GODADDY_SECRET" \
    -H "Content-Type: application/json" \
    --data "$batch_payload" >/dev/null
done < "$batch_file"
deadline=$((SECONDS + ${GODADDY_DNS_WAIT_SECONDS:-300}))
resolvers="${GODADDY_DNS_RESOLVERS:-8.8.8.8 1.1.1.1 9.9.9.9}"
propagation_seconds="${GODADDY_DNS_PROPAGATION_SECONDS:-200}"
# GoDaddy TXT TTL is normally 600s. Probe every propagation_seconds instead of
# sleeping a full TTL unconditionally; once every configured resolver sees every
# TXT validation value, ACME validation can proceed.
while [ "$SECONDS" -lt "$deadline" ]; do
  found=1
  wait_file="$state_dir/wait-records.tsv"
  CERTBOT_STATE_DIR="$state_dir" python3 - <<'PY' > "$wait_file"
import os, pathlib, urllib.parse
state = pathlib.Path(os.environ["CERTBOT_STATE_DIR"]) / "records"
for record_dir in sorted(p for p in state.iterdir() if p.is_dir()):
    record = urllib.parse.unquote(record_dir.name)
    for value_file in sorted(p for p in record_dir.iterdir() if p.is_file()):
        value = value_file.read_text().strip()
        if value:
            print(record + "\t" + value)
PY
  while IFS=$'\t' read -r wait_record wait_value; do
    fqdn="$wait_record.$zone"
    for resolver in $resolvers; do
      if ! dig +time=5 +tries=1 +short TXT "$fqdn" "@$resolver" | tr -d '"' | grep -Fx -- "$wait_value" >/dev/null; then
        found=0
        break
      fi
    done
    if [ "$found" = "0" ]; then
      break
    fi
  done < "$wait_file"
  if [ "$found" = "1" ]; then
    sleep "$propagation_seconds"
    exit 0
  fi
  sleep 10
done
echo "DNS TXT validation did not propagate for all requested public HTTPS hosts" >&2
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
curl --connect-timeout 10 --max-time 30 -fsS -X DELETE "$api_root/v1/domains/$zone/records/TXT/$record" \
  -H "Authorization: sso-key $GODADDY_KEY:$GODADDY_SECRET" >/dev/null || true`
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

func godaddyUpsert(paths provisionPaths, rootDomain, godaddyEnv, operatorEnv, domain, ip, ttl string) error {
	name := recordNameForDomain(rootDomain, domain)
	goCmd := firstNonEmpty(os.Getenv("RTK_CLOUD_GO"), "go")
	cmd := exec.Command(goCmd, "run", "./cmd/godaddy-dns", "--env-file", operatorEnv, "records", "upsert", rootDomain, "--type", "A", "--name", name, "--data", ip, "--ttl", ttl)
	cmd.Dir = firstNonEmpty(os.Getenv("RTK_CLOUD_GODADDY_DNS_DIR"), filepath.Join(paths.Workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"))
	cmd.Env = append(os.Environ(), "GODADDY_ENV="+godaddyEnv, "GOWORK=off")
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

func defaultStagingSSHKey() string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")
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

func runCmdWithInput(dir, input, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(redactCommandArgs(args), " "), err)
	}
	return nil
}

func redactCommandArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for i, arg := range redacted {
		key, _, found := strings.Cut(arg, "=")
		upper := strings.ToUpper(key)
		if found && (strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") || strings.HasSuffix(upper, "_KEY") || strings.Contains(upper, "PASSWORD")) {
			redacted[i] = key + "=<redacted>"
		}
	}
	return redacted
}

func runCmdQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
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
