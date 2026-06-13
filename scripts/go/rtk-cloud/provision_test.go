package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteProvisionArtifactsIncludesCloudAdminPrivateIP(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
		ArtifactsDir:        filepath.Join(root, "artifacts"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{
  "stack":"video-cloud-test",
  "vpc_id":1,
  "subnet_id":2,
  "firewalls":{"edge":10,"api":11,"infra":12,"mqtt":13,"coturn":14},
  "instances":{
    "edge":{"id":101,"role":"edge","label":"vc-edge","public_ipv4":"203.0.113.5","private_ip":"10.42.1.5"},
    "api":{"id":102,"role":"api","label":"vc-api","public_ipv4":"203.0.113.6","private_ip":"10.42.1.10"},
    "infra":{"id":103,"role":"infra","label":"vc-infra","public_ipv4":"203.0.113.7","private_ip":"10.42.1.30"},
    "mqtt":{"id":104,"role":"mqtt","label":"vc-mqtt","public_ipv4":"203.0.113.8","private_ip":"10.42.1.40"},
    "coturn":{"id":105,"role":"coturn","label":"vc-coturn","public_ipv4":"203.0.113.9","private_ip":""}
  }
}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID=201\nACCOUNT_MANAGER_LINODE_LABEL=am\nACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\nACCOUNT_MANAGER_LINODE_PRIVATE_IPV4=10.42.1.50\nACCOUNT_MANAGER_LINODE_FIREWALL_ID=301\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_ID=202\nADMIN_LINODE_LABEL=admin\nADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\nADMIN_LINODE_PRIVATE_IPV4=10.42.1.60\nADMIN_LINODE_FIREWALL_ID=302\n")

	dir, err := writeProvisionArtifacts(paths, "video-cloud-test")
	if err != nil {
		t.Fatalf("writeProvisionArtifacts returned error: %v", err)
	}

	var inventory struct {
		CloudAdmin struct {
			PrivateIP string `json:"private_ip"`
		} `json:"cloud_admin"`
	}
	readJSON(t, filepath.Join(dir, "inventory.json"), &inventory)
	if inventory.CloudAdmin.PrivateIP != "10.42.1.60" {
		t.Fatalf("cloud_admin.private_ip = %q", inventory.CloudAdmin.PrivateIP)
	}

	var targets struct {
		Targets map[string]struct {
			PrivateIP string `json:"private_ip"`
		} `json:"targets"`
	}
	readJSON(t, filepath.Join(dir, "deployment-targets.json"), &targets)
	if targets.Targets["cloud_admin"].PrivateIP != "10.42.1.60" {
		t.Fatalf("targets.cloud_admin.private_ip = %q", targets.Targets["cloud_admin"].PrivateIP)
	}
}

func TestLoggerProvisionStatusesReflectEnvAndStateFiles(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{EnvRoot: root}
	env := map[string]string{
		"CLOUD_ENV_NAME":                     "ci",
		"CLOUD_DNS_ROOT_DOMAIN":              "example.test",
		"CLOUD_LOGGER_LINODE_LABEL":          "rtk-cloud-logger-ci",
		"CLOUD_LOGGER_LINODE_FIREWALL_LABEL": "rtk-cloud-logger-ci-fw",
		"CLOUD_LOGGER_DOMAIN":                "logger.video-cloud-ci.example.test",
	}

	missing := loggerProvisionStatuses(loggerProvisionTarget(paths, env))
	for _, item := range missing {
		if item.status != "missing" {
			t.Fatalf("%s status = %q, want missing", item.kind, item.status)
		}
	}

	target := loggerProvisionTarget(paths, env)
	mkdirAll(t, filepath.Dir(target.EnvPath))
	writeFile(t, target.EnvPath, "CLOUD_LOGGER_ENDPOINT=https://logger.video-cloud-ci.example.test\nCLOUD_LOGGER_INGEST_TOKEN=secret-token\n")
	mkdirAll(t, filepath.Dir(target.StatePath))
	writeFile(t, target.StatePath, "CLOUD_LOGGER_LINODE_ID=301\nCLOUD_LOGGER_LINODE_FIREWALL_ID=401\nCLOUD_LOGGER_DNS_RECORD=logger.video-cloud-ci.example.test\n")

	statuses := loggerProvisionStatuses(loggerProvisionTarget(paths, env))
	got := map[string]string{}
	for _, item := range statuses {
		got[item.kind] = item.status
	}
	for _, kind := range []string{"VM", "firewall", "DNS", "env", "state"} {
		if got[kind] != "provisioned" {
			t.Fatalf("%s status = %q, want provisioned; all=%#v", kind, got[kind], statuses)
		}
	}
}

func TestEnsureProvisionRuntimeContractsWritesMTLSAndInternalAuth(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	keysDir := filepath.Join(workspace, "keys", "staging", "linode", "video-cloud")
	mkdirAll(t, keysDir)
	writeFile(t, filepath.Join(keysDir, "root-ca.ed25519.cert.pem"), "-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n")
	writeFile(t, filepath.Join(keysDir, "production-issuer.ed25519.cert.pem"), "-----BEGIN CERTIFICATE-----\ndevice\n-----END CERTIFICATE-----\n")
	writeFile(t, filepath.Join(keysDir, "app-user-issuer.ed25519.cert.pem"), "-----BEGIN CERTIFICATE-----\napp\n-----END CERTIFICATE-----\n")

	paths := provisionPaths{
		Workspace:           workspace,
		EnvRoot:             root,
		VideoConfig:         filepath.Join(root, "topology", "video-cloud-staging.yaml"),
		VideoEnv:            filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env"),
		AccountManagerEnv:   filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoConfig))
	writeFile(t, paths.VideoConfig, `deploy:
  device_client_domain: ""
  device_client_ca_cert_path: ""
`)
	mkdirAll(t, filepath.Dir(paths.VideoEnv))
	writeFile(t, paths.VideoEnv, "VIDEO_CLOUD_AUTH_SECRET=auth-secret\n")
	mkdirAll(t, filepath.Dir(paths.AccountManagerEnv))
	writeFile(t, paths.AccountManagerEnv, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=shared-internal-token\n")
	mkdirAll(t, filepath.Dir(paths.AccountManagerState))
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4=10.42.1.55\n")

	err := ensureProvisionRuntimeContracts(paths, map[string]string{
		"VIDEO_CLOUD_DOMAIN":           "video-cloud-stg-0529.realtekconnect.com",
		"CLOUD_REQUIRE_PKCS11_SIGNING": "0",
	})
	if err != nil {
		t.Fatalf("ensureProvisionRuntimeContracts returned error: %v", err)
	}

	config := readFile(t, paths.VideoConfig)
	if !strings.Contains(config, "device_client_domain: device.video-cloud-stg-0529.realtekconnect.com") {
		t.Fatalf("video config missing device client domain:\n%s", config)
	}
	bundlePath := filepath.Join(keysDir, "device-app-client-ca-bundle.pem")
	if !strings.Contains(config, "device_client_ca_cert_path: "+bundlePath) {
		t.Fatalf("video config missing device client CA bundle:\n%s", config)
	}
	bundle := readFile(t, bundlePath)
	if strings.Count(bundle, "BEGIN CERTIFICATE") != 3 {
		t.Fatalf("bundle certificate count = %d, want 3:\n%s", strings.Count(bundle, "BEGIN CERTIFICATE"), bundle)
	}

	videoEnv, _ := readEnvFile(paths.VideoEnv)
	if videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL"] != "http://10.42.1.55:18081" {
		t.Fatalf("VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL = %q", videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL"])
	}
	if videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"] != "shared-internal-token" {
		t.Fatalf("video internal token was not synchronized")
	}
	if videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT"] != "10s" {
		t.Fatalf("VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT = %q", videoEnv["VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT"])
	}
}

func TestEnsureProvisionRuntimeContractsRequiresPKCS11SigningByDefault(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	keysDir := filepath.Join(workspace, "keys", "staging", "linode", "video-cloud")
	mkdirAll(t, keysDir)
	for _, name := range []string{"root-ca.ed25519.cert.pem", "production-issuer.ed25519.cert.pem", "app-user-issuer.ed25519.cert.pem"} {
		writeFile(t, filepath.Join(keysDir, name), "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n")
	}
	paths := provisionPaths{
		Workspace:         workspace,
		VideoConfig:       filepath.Join(root, "topology", "video-cloud-staging.yaml"),
		VideoEnv:          filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env"),
		AccountManagerEnv: filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoConfig))
	writeFile(t, paths.VideoConfig, "deploy: {}\n")
	mkdirAll(t, filepath.Dir(paths.VideoEnv))
	writeFile(t, paths.VideoEnv, "VIDEO_CLOUD_AUTH_SECRET=auth-secret\n")
	mkdirAll(t, filepath.Dir(paths.AccountManagerEnv))
	writeFile(t, paths.AccountManagerEnv, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=shared-internal-token\n")

	err := ensureProvisionRuntimeContracts(paths, map[string]string{"VIDEO_CLOUD_DOMAIN": "video.example.test"})
	if err == nil {
		t.Fatalf("ensureProvisionRuntimeContracts succeeded without issuer key material")
	}
	for _, want := range []string{"SoftHSMv2/PKCS#11", "CERT_ISSUER_CA_KEY_SOURCE", "CERT_ISSUER_APP_CA_KEY_SOURCE"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestValidateProvisionRuntimeContractsRequiresPKCS11SigningByDefault(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	keysDir := filepath.Join(workspace, "keys", "staging", "linode", "video-cloud")
	mkdirAll(t, keysDir)
	for _, name := range []string{"root-ca.ed25519.cert.pem", "production-issuer.ed25519.cert.pem", "app-user-issuer.ed25519.cert.pem"} {
		writeFile(t, filepath.Join(keysDir, name), "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n")
	}
	paths := provisionPaths{
		Workspace:         workspace,
		VideoEnv:          filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env"),
		AccountManagerEnv: filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoEnv))
	writeFile(t, paths.VideoEnv, "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL=http://10.42.1.50:18081\nVIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=shared\nVIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT=10s\n")
	mkdirAll(t, filepath.Dir(paths.AccountManagerEnv))
	writeFile(t, paths.AccountManagerEnv, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=shared\n")

	err := validateProvisionRuntimeContracts(paths, map[string]string{"VIDEO_CLOUD_DOMAIN": "video.example.test"})
	if err == nil {
		t.Fatalf("validateProvisionRuntimeContracts succeeded without pkcs11 signer settings")
	}
	for _, want := range []string{"SoftHSMv2/PKCS#11", "CERT_ISSUER_CA_KEY_SOURCE", "CERT_ISSUER_APP_CA_KEY_SOURCE", "VIDEO_CLOUD_AUTH_TOKEN_SIGNER_PROVIDER=pkcs11"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestEnsureProvisionRuntimeContractsAcceptsPKCS11Signing(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	keysDir := filepath.Join(workspace, "keys", "staging", "linode", "video-cloud")
	mkdirAll(t, keysDir)
	for _, name := range []string{"root-ca.ed25519.cert.pem", "production-issuer.ed25519.cert.pem", "app-user-issuer.ed25519.cert.pem"} {
		writeFile(t, filepath.Join(keysDir, name), "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n")
	}
	deviceKey := filepath.Join(keysDir, "production-issuer.ed25519.key.pem")
	appKey := filepath.Join(keysDir, "app-user-issuer.ed25519.key.pem")
	writeFile(t, deviceKey, "device-key")
	writeFile(t, appKey, "app-key")
	paths := provisionPaths{
		Workspace:         workspace,
		VideoConfig:       filepath.Join(root, "topology", "video-cloud-staging.yaml"),
		VideoEnv:          filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env"),
		AccountManagerEnv: filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoConfig))
	writeFile(t, paths.VideoConfig, "deploy: {}\n")
	mkdirAll(t, filepath.Dir(paths.VideoEnv))
	writeFile(t, paths.VideoEnv, "VIDEO_CLOUD_AUTH_SECRET=auth-secret\nCERT_ISSUER_CA_KEY_SOURCE="+deviceKey+"\nCERT_ISSUER_APP_CA_KEY_SOURCE="+appKey+"\n")
	mkdirAll(t, filepath.Dir(paths.AccountManagerEnv))
	writeFile(t, paths.AccountManagerEnv, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=shared-internal-token\n")

	if err := ensureProvisionRuntimeContracts(paths, map[string]string{"VIDEO_CLOUD_DOMAIN": "video.example.test"}); err != nil {
		t.Fatalf("ensureProvisionRuntimeContracts returned error: %v", err)
	}
	videoEnv, _ := readEnvFile(paths.VideoEnv)
	for key, want := range map[string]string{
		"VIDEO_CLOUD_AUTH_TOKEN_SIGNER_PROVIDER": "pkcs11",
		"CERT_ISSUER_SIGNER_PROVIDER":            "pkcs11",
		"CERT_ISSUER_APP_SIGNER_PROVIDER":        "pkcs11",
		"CERT_ISSUER_PKCS11_KEY_LABEL":           "device-ca",
		"CERT_ISSUER_APP_PKCS11_KEY_LABEL":       "app-ca",
	} {
		if videoEnv[key] != want {
			t.Fatalf("%s = %q, want %q", key, videoEnv[key], want)
		}
	}
	if videoEnv["VIDEO_CLOUD_AUTH_TOKEN_PKCS11_PIN"] == "" {
		t.Fatalf("generated pkcs11 pin is empty")
	}
}

func TestServiceLogLevelsDefaultAndOverrides(t *testing.T) {
	env := map[string]string{
		"CLOUD_SERVICE_LOG_LEVEL":   "warn",
		"VIDEO_CLOUD_LOG_LEVEL":     "debug",
		"ACCOUNT_MANAGER_LOG_LEVEL": "error",
	}
	levels, err := serviceLogLevels(env)
	if err != nil {
		t.Fatalf("serviceLogLevels returned error: %v", err)
	}
	if levels["VIDEO_CLOUD_LOG_LEVEL"] != "debug" {
		t.Fatalf("VIDEO_CLOUD_LOG_LEVEL = %q, want debug", levels["VIDEO_CLOUD_LOG_LEVEL"])
	}
	if levels["ACCOUNT_MANAGER_LOG_LEVEL"] != "error" {
		t.Fatalf("ACCOUNT_MANAGER_LOG_LEVEL = %q, want error", levels["ACCOUNT_MANAGER_LOG_LEVEL"])
	}
	if levels["CLOUD_ADMIN_LOG_LEVEL"] != "warn" {
		t.Fatalf("CLOUD_ADMIN_LOG_LEVEL = %q, want warn", levels["CLOUD_ADMIN_LOG_LEVEL"])
	}
}

func TestServiceLogLevelsDefaultToInfo(t *testing.T) {
	levels, err := serviceLogLevels(map[string]string{})
	if err != nil {
		t.Fatalf("serviceLogLevels returned error: %v", err)
	}
	for _, key := range []string{"VIDEO_CLOUD_LOG_LEVEL", "ACCOUNT_MANAGER_LOG_LEVEL", "CLOUD_ADMIN_LOG_LEVEL"} {
		if levels[key] != "info" {
			t.Fatalf("%s = %q, want info", key, levels[key])
		}
	}
}

func TestServiceLogLevelsRejectInvalidValues(t *testing.T) {
	_, err := serviceLogLevels(map[string]string{"CLOUD_SERVICE_LOG_LEVEL": "verbose"})
	if err == nil {
		t.Fatalf("serviceLogLevels succeeded, want invalid level error")
	}
	if !strings.Contains(err.Error(), "CLOUD_SERVICE_LOG_LEVEL") || !strings.Contains(err.Error(), "debug, info, warn, error") {
		t.Fatalf("error = %v, want level guidance", err)
	}
}

func TestEMQXVerboseTraceFlagDefaultsOff(t *testing.T) {
	if emqxVerboseTraceEnabled(map[string]string{}) {
		t.Fatalf("EMQX verbose trace enabled by default")
	}
	if emqxVerboseTraceEnabled(map[string]string{"CLOUD_LOGGER_EMQX_VERBOSE_TRACE": "false"}) {
		t.Fatalf("EMQX verbose trace enabled for false")
	}
	if !emqxVerboseTraceEnabled(map[string]string{"CLOUD_LOGGER_EMQX_VERBOSE_TRACE": "true"}) {
		t.Fatalf("EMQX verbose trace disabled for true")
	}
}

func TestLoggerEMQXForwarderInstallScriptConfiguresBrokerTrace(t *testing.T) {
	script := loggerEMQXForwarderInstallScript("https://logger.example.com", "secret-token", "203.0.113.99", "http://10.42.1.10:3128", "ci")
	for _, want := range []string{
		"RTK_CLOUD_LOGGER_INGEST_URL=https://logger.example.com/v1/logs/ingest",
		"RTK_CLOUD_LOGGER_EMQX_DOCKER_CONTAINER=video-cloud-emqx",
		"RTK_CLOUD_LOGGER_CURSOR=/var/lib/rtk-cloud-logger/emqx-docker.cursor",
		"SERVICE=emqx-broker",
		"ENV=ci",
		"rtk-cloud-emqx-log-forwarder.service",
		"HTTP_PROXY=http://10.42.1.10:3128",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("EMQX forwarder install script missing %q:\n%s", want, script)
		}
	}
}

func TestLoggerForwarderTargetsUseStagingSystemdUnits(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{"instances":{"api":{"private_ip":"10.42.1.10"},"infra":{"private_ip":"10.42.1.30"},"mqtt":{"private_ip":"10.42.1.40"},"edge":{"public_ipv4":"203.0.113.5","private_ip":"10.42.1.5"},"coturn":{"public_ipv4":"203.0.113.9","private_ip":"10.42.1.80"}}}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\n")

	byName := map[string]loggerForwarderTarget{}
	for _, target := range loggerForwarderTargets(paths) {
		byName[target.name] = target
	}
	assertUnitsContain(t, byName["video-cloud-api"].units,
		"video_cloud-api.service",
		"video_cloud-logingester.service",
		"video_cloud-turnregistry.service",
		"video_cloud-metricsexporter.service",
		"video_cloud-cleaner.service",
		"video_cloud-statistics.service",
	)
	assertUnitsNotContain(t, byName["video-cloud-api"].units, "rtk-video-cloud-api.service")
	assertUnitsContain(t, byName["infra"].units, "prometheus.service", "postgresql.service", "redis-server.service")
	assertUnitsNotContain(t, byName["infra"].units, "nats-server.service", "nats.service")
	assertUnitsContain(t, byName["coturn"].units, "coturn.service", "video_cloud-turnregistrar.service")
	if byName["edge"].host != "203.0.113.5" {
		t.Fatalf("edge logger forwarder host = %q, want public IP", byName["edge"].host)
	}
	if byName["coturn"].host != "203.0.113.9" {
		t.Fatalf("coturn logger forwarder host = %q, want public IP", byName["coturn"].host)
	}
}

func TestValidateRemovedStagingServicesRejectsActiveNATSAndCrossservice(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		VideoConfig: filepath.Join(root, "topology", "video-cloud-staging.yaml"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoConfig))
	writeFile(t, paths.VideoConfig, "roles:\n  - cmd_crossservice\n  - nats\n")

	err := validateRemovedStagingServices(paths)
	if err == nil {
		t.Fatalf("validateRemovedStagingServices succeeded with retired roles")
	}
	if !strings.Contains(err.Error(), "cmd_crossservice") {
		t.Fatalf("error missing retired role: %v", err)
	}
}

func TestWriteProvisionArtifactsRedactsLoggerToken(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
		ArtifactsDir:        filepath.Join(root, "artifacts"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{"instances":{"edge":{"label":"edge","public_ipv4":"203.0.113.5"}},"firewalls":{"edge":101}}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID=201\nACCOUNT_MANAGER_LINODE_LABEL=am\nACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\nACCOUNT_MANAGER_LINODE_FIREWALL_ID=301\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_ID=202\nADMIN_LINODE_LABEL=admin\nADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\nADMIN_LINODE_FIREWALL_ID=302\n")
	writeFile(t, filepath.Join(root, "state", "cloud-logger.env"), "CLOUD_LOGGER_LINODE_ID=203\nCLOUD_LOGGER_LINODE_LABEL=logger\nCLOUD_LOGGER_LINODE_PUBLIC_IPV4=203.0.113.40\nCLOUD_LOGGER_LINODE_FIREWALL_ID=303\nCLOUD_LOGGER_DOMAIN=logger.example.test\nCLOUD_LOGGER_ENDPOINT=https://logger.example.test\nCLOUD_LOGGER_INGEST_TOKEN=super-secret-logger-token\n")
	mkdirAll(t, filepath.Join(root, "services", "cloud-logger"))
	writeFile(t, filepath.Join(root, "services", "cloud-logger", "logger.env"), "CLOUD_LOGGER_ENDPOINT=https://logger.example.test\nCLOUD_LOGGER_INGEST_TOKEN=super-secret-logger-token\n")

	dir, err := writeProvisionArtifacts(paths, "video-cloud-test")
	if err != nil {
		t.Fatalf("writeProvisionArtifacts returned error: %v", err)
	}
	for _, name := range []string{"inventory.json", "deployment-targets.json", "provision-report.md"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "super-secret-logger-token") {
			t.Fatalf("%s leaked logger token:\n%s", name, string(body))
		}
	}
	for _, name := range []string{"cloud-logger-state.redacted.env", "cloud-logger-env.redacted.env"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "super-secret-logger-token") {
			t.Fatalf("%s leaked logger token:\n%s", name, string(body))
		}
		if !strings.Contains(string(body), "REDACTED") {
			t.Fatalf("%s missing redaction marker:\n%s", name, string(body))
		}
	}
}

func TestLoggerHTTPArgsUseBoundedTimeouts(t *testing.T) {
	args := loggerHTTPArgs("https://logger.example.test/", "secret-token", http.MethodGet, "/v1/logs", "")
	got := strings.Join(args, "\x00")
	for _, want := range []string{
		"--connect-timeout\x005",
		"--max-time\x0015",
		"-X\x00GET",
		"https://logger.example.test/v1/logs",
		"Authorization: Bearer secret-token",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("loggerHTTPArgs missing %q in %#v", want, args)
		}
	}
}

func TestLoggerBackendInstallScriptUsesCachedCertificate(t *testing.T) {
	script := loggerBackendInstallScript("logger.example.test", "secret-token", "v3.5.1", true, "10.42.1.90")
	for _, want := range []string{
		"apt-get install -y nginx certbot curl unzip prometheus-node-exporter dnsutils",
		"printf 'ARGS=\"--web.listen-address=10.42.1.90:9100\"\\n' > /etc/default/prometheus-node-exporter",
		"systemctl restart prometheus-node-exporter.service",
		"ss -lnt | grep 10.42.1.90:9100",
		"/tmp/rtk-cloud-logger-deploy/cert-cache/fullchain.pem",
		"/etc/letsencrypt/live/$domain/fullchain.pem",
		"openssl x509 -in /tmp/rtk-cloud-logger-deploy/cert-cache/fullchain.pem",
		"awk 'BEGIN{n=0} /-----BEGIN CERTIFICATE-----/{n++} n>1{print}'",
		"installed cached certificate lineage",
		"systemctl enable --now certbot.timer",
		"ExecStart=/usr/local/bin/rtk-cloud-logger -addr 0.0.0.0:18090",
		"ARGS=\"--web.listen-address=10.42.1.90:9100\"",
		"ss -lnt | grep 10.42.1.90:9100",
		"listen 443 ssl;",
		"server_name logger.example.test;",
		"ssl_certificate /etc/letsencrypt/live/logger.example.test/fullchain.pem;",
		"ssl_certificate_key /etc/letsencrypt/live/logger.example.test/privkey.pem;",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("logger backend cached-cert script missing %q:\n%s", want, script)
		}
	}
	for _, want := range []string{"authenticator = manual", "pref_challs = dns-01", "manual_auth_hook = /usr/local/libexec/rtk-cloud-certbot-dns-auth", "manual_cleanup_hook = /usr/local/libexec/rtk-cloud-certbot-dns-cleanup"} {
		if !strings.Contains(script, want) {
			t.Fatalf("logger backend cached-cert script missing DNS-01 renewal %q:\n%s", want, script)
		}
	}
	for _, notWant := range []string{"certbot --nginx -d", "listen 80", "/.well-known/acme-challenge", "authenticator = nginx"} {
		if strings.Contains(script, notWant) {
			t.Fatalf("logger backend cached-cert script should not contain %q:\n%s", notWant, script)
		}
	}
}

func TestLoggerBackendInstallScriptIssuesCertificateWithoutCache(t *testing.T) {
	script := loggerBackendInstallScript("logger.example.test", "secret-token", "v3.5.1", false, "")
	for _, want := range []string{"certbot certonly --manual", "--preferred-challenges dns", "--manual-auth-hook /usr/local/libexec/rtk-cloud-certbot-dns-auth", "--manual-cleanup-hook /usr/local/libexec/rtk-cloud-certbot-dns-cleanup", "-d logger.example.test"} {
		if !strings.Contains(script, want) {
			t.Fatalf("logger backend script missing DNS-01 certbot issuance %q:\n%s", want, script)
		}
	}
	for _, notWant := range []string{"certbot --nginx", "listen 80", "/.well-known/acme-challenge"} {
		if strings.Contains(script, notWant) {
			t.Fatalf("logger backend script should not contain %q:\n%s", notWant, script)
		}
	}
}

func TestCertbotPublicDeployEnvPassesOperatorDNSCredentials(t *testing.T) {
	got := certbotPublicDeployEnv(
		map[string]string{"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com"},
		map[string]string{
			"GODADDY_API_KEY":                 "operator-key",
			"GODADDY_API_SECRET":              "operator-secret",
			"GODADDY_ENV":                     "prod",
			"GODADDY_DNS_TTL":                 "600",
			"GODADDY_DNS_WAIT_SECONDS":        "300",
			"GODADDY_DNS_PROPAGATION_SECONDS": "60",
			"GODADDY_DNS_RESOLVERS":           "8.8.8.8 1.1.1.1",
		},
	)
	for key, want := range map[string]string{
		"GODADDY_KEY":                     "operator-key",
		"GODADDY_SECRET":                  "operator-secret",
		"GODADDY_ENV":                     "prod",
		"CLOUD_DNS_ROOT_DOMAIN":           "realtekconnect.com",
		"GODADDY_RECORD_TTL":              "600",
		"GODADDY_DNS_WAIT_SECONDS":        "300",
		"GODADDY_DNS_PROPAGATION_SECONDS": "60",
		"GODADDY_DNS_RESOLVERS":           "8.8.8.8 1.1.1.1",
	} {
		if got[key] != want {
			t.Fatalf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestPrometheusTargetHostReadsCloudLoggerNode(t *testing.T) {
	config := filepath.Join(t.TempDir(), "video-cloud-staging.yaml")
	writeFile(t, config, `deploy:
  prometheus_targets:
    - job: cloud_logger_app
      address: 10.42.1.90:18090
    - job: cloud_logger_node
      address: 10.42.1.90:9100
`)
	if got := prometheusTargetHost(config, "cloud_logger_node"); got != "10.42.1.90" {
		t.Fatalf("prometheusTargetHost = %q, want 10.42.1.90", got)
	}
}

func TestCheckCertificatesIncludesCloudLoggerTarget(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), `VIDEO_CLOUD_DOMAIN=video.example.test
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video.example.test
CLOUD_LOGGER_DOMAIN=logger.video.example.test
`)
	mkdirAll(t, filepath.Join(root, "services", "account-manager"))
	writeFile(t, filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"), "ACCOUNT_MANAGER_LINODE_DOMAIN=account.example.test\n")
	mkdirAll(t, filepath.Join(root, "services", "cloud-admin"))
	writeFile(t, filepath.Join(root, "services", "cloud-admin", "admin-staging.env"), "ADMIN_LINODE_DOMAIN=admin.example.test\n")

	var runErr error
	output := captureStdout(t, func() {
		runErr = runCheckCertificates([]string{"--workspace", t.TempDir(), "--env-root", root, "--json"})
	})
	if _, ok := runErr.(exitCode); !ok {
		t.Fatalf("runCheckCertificates error = %T %[1]v, want exitCode", runErr)
	}
	var payload struct {
		Results []certCheckResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, output)
	}
	for _, result := range payload.Results {
		if result.Target == "cloud-logger" && result.Domain == "logger.video.example.test" {
			return
		}
	}
	t.Fatalf("cloud-logger target missing from check-certificates results: %#v", payload.Results)
}

func TestStagingCertificateCacheTargetsIncludeLoggerBeforeRemove(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{"instances":{"edge":{"public_ipv4":"203.0.113.5"}}}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\n")
	mkdirAll(t, filepath.Join(root, "state"))
	writeFile(t, filepath.Join(root, "state", "cloud-logger.env"), "CLOUD_LOGGER_DOMAIN=logger.video.example.test\nCLOUD_LOGGER_LINODE_PUBLIC_IPV4=203.0.113.40\n")

	env := map[string]string{
		"VIDEO_CLOUD_DOMAIN":      "video.example.test",
		"ACCOUNT_MANAGER_DOMAIN":  "account.example.test",
		"CLOUD_ADMIN_DOMAIN":      "admin.example.test",
		"CLOUD_LOGGER_DOMAIN":     "logger.video.example.test",
		"CLOUD_DNS_ROOT_DOMAIN":   "example.test",
		"CLOUD_LOGGER_LINODE_ID":  "301",
		"CLOUD_LOGGER_ENDPOINT":   "https://logger.video.example.test",
		"CLOUD_LOGGER_LINODE_KEY": "ignored",
	}
	targets := stagingCertificateCacheTargets(paths, env)
	got := map[string]certificateCacheTarget{}
	for _, target := range targets {
		got[target.Name] = target
	}
	for name, want := range map[string]string{
		"video-cloud":     "video.example.test",
		"account-manager": "account.example.test",
		"cloud-admin":     "admin.example.test",
		"cloud-logger":    "logger.video.example.test",
	} {
		if got[name].Domain != want {
			t.Fatalf("%s domain = %q, want %q; targets=%#v", name, got[name].Domain, want, targets)
		}
		if got[name].Host == "" {
			t.Fatalf("%s host missing; targets=%#v", name, targets)
		}
		if got[name].Dir != filepath.Join(root, "certificates", want) {
			t.Fatalf("%s dir = %q, want certificates dir for %s", name, got[name].Dir, want)
		}
	}
}

func TestRequireStagingCertificateCachesFailsBeforeDestructiveRemove(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{"instances":{"edge":{"public_ipv4":"203.0.113.5"}}}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\n")
	mkdirAll(t, filepath.Join(root, "state"))
	writeFile(t, filepath.Join(root, "state", "cloud-logger.env"), "CLOUD_LOGGER_DOMAIN=logger.video.example.test\nCLOUD_LOGGER_LINODE_PUBLIC_IPV4=203.0.113.40\n")

	env := map[string]string{
		"VIDEO_CLOUD_DOMAIN":     "video.example.test",
		"ACCOUNT_MANAGER_DOMAIN": "account.example.test",
		"CLOUD_ADMIN_DOMAIN":     "admin.example.test",
		"CLOUD_LOGGER_DOMAIN":    "logger.video.example.test",
	}
	for _, domain := range []string{"video.example.test", "account.example.test", "admin.example.test"} {
		dir := filepath.Join(root, "certificates", domain)
		mkdirAll(t, dir)
		writeFile(t, filepath.Join(dir, "fullchain.pem"), "fullchain")
		writeFile(t, filepath.Join(dir, "privkey.pem"), "private-key")
	}

	err := requireStagingCertificateCaches(paths, env)
	if err == nil {
		t.Fatalf("requireStagingCertificateCaches succeeded with missing logger cache")
	}
	if !strings.Contains(err.Error(), "cloud-logger") || !strings.Contains(err.Error(), "logger.video.example.test") || !strings.Contains(err.Error(), "before destructive e2e remove") {
		t.Fatalf("error did not identify missing logger certificate cache: %v", err)
	}
}

func TestRequireStagingCertificateCachesPassesWhenAllTargetsCached(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{EnvRoot: root}
	env := map[string]string{
		"VIDEO_CLOUD_DOMAIN":     "video.example.test",
		"ACCOUNT_MANAGER_DOMAIN": "account.example.test",
		"CLOUD_ADMIN_DOMAIN":     "admin.example.test",
		"CLOUD_LOGGER_DOMAIN":    "logger.video.example.test",
	}
	for _, domain := range []string{"video.example.test", "account.example.test", "admin.example.test", "logger.video.example.test"} {
		dir := filepath.Join(root, "certificates", domain)
		mkdirAll(t, dir)
		writeFile(t, filepath.Join(dir, "fullchain.pem"), "fullchain")
		writeFile(t, filepath.Join(dir, "privkey.pem"), "private-key")
	}

	if err := requireStagingCertificateCaches(paths, env); err != nil {
		t.Fatalf("requireStagingCertificateCaches returned error: %v", err)
	}
}

func TestRequireStagingCertificateCachesForTargetsOnlyRequiresRemotePresentTargets(t *testing.T) {
	root := t.TempDir()
	cachedDomain := "video.example.test"
	cachedDir := filepath.Join(root, "certificates", cachedDomain)
	mkdirAll(t, cachedDir)
	writeFile(t, filepath.Join(cachedDir, "fullchain.pem"), "fullchain")
	writeFile(t, filepath.Join(cachedDir, "privkey.pem"), "private-key")

	targets := []certificateCacheTarget{
		{Name: "video-cloud", Host: "203.0.113.5", Domain: cachedDomain, Dir: cachedDir},
	}
	if err := requireStagingCertificateCachesForTargets(targets); err != nil {
		t.Fatalf("requireStagingCertificateCachesForTargets returned error: %v", err)
	}
}

func TestCertCacheEnvRequiresFullchainAndPrivateKey(t *testing.T) {
	dir := t.TempDir()
	if got := certCacheEnv("CERT_CACHE", dir); got != nil {
		t.Fatalf("certCacheEnv without files = %#v, want nil", got)
	}
	writeFile(t, filepath.Join(dir, "fullchain.pem"), "fullchain")
	if got := certCacheEnv("CERT_CACHE", dir); got != nil {
		t.Fatalf("certCacheEnv without private key = %#v, want nil", got)
	}
	writeFile(t, filepath.Join(dir, "privkey.pem"), "private-key")
	got := certCacheEnv("CERT_CACHE", dir)
	if got["CERT_CACHE"] != dir {
		t.Fatalf("certCacheEnv = %#v, want CERT_CACHE=%s", got, dir)
	}
}

func assertUnitsContain(t *testing.T, units string, wants ...string) {
	t.Helper()
	have := "," + units + ","
	for _, want := range wants {
		if !strings.Contains(have, ","+want+",") {
			t.Fatalf("units %q missing %q", units, want)
		}
	}
}

func assertUnitsNotContain(t *testing.T, units string, wants ...string) {
	t.Helper()
	have := "," + units + ","
	for _, want := range wants {
		if strings.Contains(have, ","+want+",") {
			t.Fatalf("units %q unexpectedly contains %q", units, want)
		}
	}
}

func TestFirewallTargetsUsesConfiguredVideoCloudStackState(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	mkdirAll(t, filepath.Join(root, "state"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-stg-0529\n")
	writeFile(t, filepath.Join(root, "state", "video-cloud-stg-0529.state.json"), `{
  "stack":"video-cloud-stg-0529",
  "firewalls":{"edge":101,"api":102,"infra":103,"mqtt":104,"coturn":105}
}`)
	writeFile(t, filepath.Join(root, "state", "account-manager-staging.env"), "ACCOUNT_MANAGER_LINODE_FIREWALL_ID=201\nACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=account-fw\n")
	writeFile(t, filepath.Join(root, "state", "cloud-admin-staging.env"), "ADMIN_LINODE_FIREWALL_ID=202\nADMIN_LINODE_FIREWALL_LABEL=admin-fw\n")

	targets, err := firewallTargets(root)
	if err != nil {
		t.Fatalf("firewallTargets returned error: %v", err)
	}
	byRole := map[string]firewallTarget{}
	for _, target := range targets {
		byRole[target.Role] = target
	}
	for role, wantID := range map[string]string{
		"edge":            "101",
		"api":             "102",
		"infra":           "103",
		"mqtt":            "104",
		"coturn":          "105",
		"account-manager": "201",
		"cloud-admin":     "202",
	} {
		if byRole[role].ID != wantID {
			t.Fatalf("%s firewall id = %q, want %q; targets=%#v", role, byRole[role].ID, wantID, targets)
		}
	}
	if byRole["edge"].Label != "video-cloud-stg-0529-edge" {
		t.Fatalf("edge label = %q", byRole["edge"].Label)
	}
}

func TestRemovePublicHTTPFirewallRulesKeepsPrivatePortsContaining80(t *testing.T) {
	rules := []firewallRule{
		{Label: "ssh", Protocol: "TCP", Ports: "22"},
		{Label: "http", Protocol: "TCP", Ports: "80"},
		{Label: "https", Protocol: "TCP", Ports: "443"},
		{Label: "logger-private", Protocol: "TCP", Ports: "18090"},
		{Label: "admin-private", Protocol: "TCP", Ports: "8080,9100,9113"},
	}
	filtered, removed := removePublicHTTPFirewallRules(rules)
	if !removed {
		t.Fatal("expected public HTTP rule to be removed")
	}
	for _, rule := range filtered {
		if rule.Ports == "80" {
			t.Fatalf("port 80 rule was not removed: %#v", filtered)
		}
	}
	if !firewallRuleExists(filtered, "logger-private") || !firewallRuleExists(filtered, "admin-private") || !firewallRuleExists(filtered, "https") {
		t.Fatalf("private/https rules should be preserved: %#v", filtered)
	}
}

func TestResolveLinodeTokenFallsBackToHomeEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LINODE_TOKEN", "")
	writeFile(t, filepath.Join(home, ".env"), "LINODE_TOKEN=home-token\n")

	token := resolveLinodeToken(t.TempDir())

	if token != "home-token" {
		t.Fatalf("token = %q, want home-token", token)
	}
}

func TestResolveLinodeTokenPrefersProcessEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LINODE_TOKEN", "process-token")
	writeFile(t, filepath.Join(home, ".env"), "LINODE_TOKEN=home-token\n")

	token := resolveLinodeToken(t.TempDir())

	if token != "process-token" {
		t.Fatalf("token = %q, want process-token", token)
	}
}

func TestRemoveAllVMMatcherUsesStackEnvLabels(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), `CLOUD_STACK_NAME=video-cloud-stg-0529
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-stg-0529
VIDEO_CLOUD_VPC_LABEL=video-cloud-stg-0529-vpc
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg-0529
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-stg-0529-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-stg-0529
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-0529-fw
CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-stg-0529
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-0529-fw
`)

	matcher := removeAllVMMatcherForEnv(root)

	for _, label := range []string{
		"video-cloud-stg-0529-edge",
		"rtk-account-manager-stg-0529",
		"rtk-cloud-admin-stg-0529",
		"rtk-cloud-logger-stg-0529",
	} {
		if !matcher.matchVM(label) {
			t.Fatalf("VM label %q did not match", label)
		}
	}
	for _, label := range []string{
		"video-cloud-stg-0529-edge",
		"rtk-account-manager-stg-0529-fw",
		"rtk-cloud-admin-stg-0529-fw",
		"rtk-cloud-logger-stg-0529-fw",
	} {
		if !matcher.matchFirewall(label) {
			t.Fatalf("firewall label %q did not match", label)
		}
	}
	if !matcher.matchVPC("video-cloud-stg-0529-vpc") {
		t.Fatal("VPC label did not match")
	}
}

func TestBackupAndRemoveStateUsesConfiguredStackState(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	mkdirAll(t, filepath.Join(root, "state"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-stg-0529\n")
	writeFile(t, filepath.Join(root, "state", "video-cloud-stg-0529.state.json"), "{}\n")

	if err := backupAndRemoveState(root); err != nil {
		t.Fatalf("backupAndRemoveState returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "video-cloud-stg-0529.state.json")); !os.IsNotExist(err) {
		t.Fatalf("stack state still exists or unexpected stat error: %v", err)
	}
}

func TestSyncProvisionVideoStateFromRepoOverwritesCloudState(t *testing.T) {
	root := t.TempDir()
	cloudState := filepath.Join(root, "cloud", "video-cloud-stg-0529.state.json")
	repoState := filepath.Join(root, "repo", "video-cloud-stg-0529.state.json")
	mkdirAll(t, filepath.Dir(cloudState))
	mkdirAll(t, filepath.Dir(repoState))
	writeFile(t, cloudState, `{"firewalls":{"edge":101}}`)
	writeFile(t, repoState, `{"firewalls":{"edge":202}}`)

	if err := syncProvisionVideoStateFromRepo(cloudState, repoState); err != nil {
		t.Fatalf("syncProvisionVideoStateFromRepo returned error: %v", err)
	}
	state, err := readJSONMap(cloudState)
	if err != nil {
		t.Fatalf("read cloud state: %v", err)
	}
	firewalls, _ := state["firewalls"].(map[string]any)
	if got := stringValue(firewalls["edge"]); got != "202" {
		t.Fatalf("cloud edge firewall = %q, want repo value 202", got)
	}
}

func TestProvisionVideoStateReusableTreatsMissingVPCAsFresh(t *testing.T) {
	root := t.TempDir()
	cloudState := filepath.Join(root, "cloud", "video-cloud-staging.state.json")
	repoState := filepath.Join(root, "repo", "video-cloud-staging.state.json")
	mkdirAll(t, filepath.Dir(cloudState))
	mkdirAll(t, filepath.Dir(repoState))
	writeFile(t, cloudState, `{"instances":{"edge":{"id":1,"label":"video-cloud-staging-edge"}}}`)
	writeFile(t, repoState, `{"instances":{"api":{"id":2,"label":"video-cloud-staging-api"}}}`)

	oldFind := findProvisionLinodeVPCID
	findProvisionLinodeVPCID = func(label string) (int, error) {
		if label != "video-cloud-staging-vpc" {
			t.Fatalf("lookup label = %q", label)
		}
		return 0, fmt.Errorf("%w: %s", errLinodeVPCNotFound, label)
	}
	t.Cleanup(func() { findProvisionLinodeVPCID = oldFind })

	reusable, err := provisionVideoStateReusable(
		provisionPaths{VideoState: cloudState},
		map[string]string{"VIDEO_CLOUD_VPC_LABEL": "video-cloud-staging-vpc"},
		repoState,
	)
	if err != nil {
		t.Fatalf("provisionVideoStateReusable returned error: %v", err)
	}
	if reusable {
		t.Fatal("stale state with missing VPC was marked reusable")
	}
}

func TestProvisionVideoStateReusableRequiresExistingVPC(t *testing.T) {
	root := t.TempDir()
	cloudState := filepath.Join(root, "cloud", "video-cloud-staging.state.json")
	mkdirAll(t, filepath.Dir(cloudState))
	writeFile(t, cloudState, `{"instances":{"edge":{"id":101,"label":"video-cloud-staging-edge"}}}`)

	oldFind := findProvisionLinodeVPCID
	findProvisionLinodeVPCID = func(label string) (int, error) {
		if label != "video-cloud-staging-vpc" {
			t.Fatalf("lookup label = %q", label)
		}
		return 42, nil
	}
	t.Cleanup(func() { findProvisionLinodeVPCID = oldFind })

	reusable, err := provisionVideoStateReusable(
		provisionPaths{VideoState: cloudState},
		map[string]string{"VIDEO_CLOUD_VPC_LABEL": "video-cloud-staging-vpc"},
		"",
	)
	if err != nil {
		t.Fatalf("provisionVideoStateReusable returned error: %v", err)
	}
	if !reusable {
		t.Fatal("state with existing VPC was not marked reusable")
	}
}

func TestWritePlatformAdminSummaryRedactsPassword(t *testing.T) {
	root := t.TempDir()
	adminEnv := filepath.Join(root, "services", "cloud-admin", "admin-staging.env")
	mkdirAll(t, filepath.Dir(adminEnv))
	writeFile(t, adminEnv, "ADMIN_BOOTSTRAP_EMAIL=admin@example.test\nADMIN_BOOTSTRAP_PASSWORD=super-secret-password\n")

	var out strings.Builder
	writePlatformAdminSummary(&out, provisionPaths{EnvRoot: root})
	body := out.String()
	if !strings.Contains(body, "Cloud Admin platform login:") {
		t.Fatalf("summary missing cloud admin heading:\n%s", body)
	}
	if !strings.Contains(body, "username: admin@example.test") {
		t.Fatalf("summary missing username:\n%s", body)
	}
	if !strings.Contains(body, "password: see "+adminEnv) {
		t.Fatalf("summary missing password file hint:\n%s", body)
	}
	if !strings.Contains(body, "account-manager token: run ./stg.sh token") {
		t.Fatalf("summary missing token command hint:\n%s", body)
	}
	if strings.Contains(body, "super-secret-password") {
		t.Fatalf("summary leaked password:\n%s", body)
	}
}

func TestWritePlatformAdminTokenLogsInWithBootstrapCredentials(t *testing.T) {
	var gotEmail, gotPassword string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/login" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		gotEmail = payload["email"]
		gotPassword = payload["password"]
		_, _ = w.Write([]byte(`{"tokens":{"access_token":"access-token-123"}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	platformEnv := filepath.Join(root, "services", "account-manager", "account-manager-platform-admin.env")
	mkdirAll(t, filepath.Dir(platformEnv))
	writeFile(t, platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.test\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=super-secret-password\n")

	var out strings.Builder
	err := writePlatformAdminToken(&out, accountManagerContext{
		BaseURL:       server.URL,
		AdminEmail:    envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"),
		AdminPassword: envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"),
	})
	if err != nil {
		t.Fatalf("writePlatformAdminToken returned error: %v", err)
	}
	if gotEmail != "root@example.test" || gotPassword != "super-secret-password" {
		t.Fatalf("login credentials email=%q password=%q", gotEmail, gotPassword)
	}
	if strings.TrimSpace(out.String()) != "access-token-123" {
		t.Fatalf("token output = %q", out.String())
	}
}

func TestAccountLoginLogsPlatformAdminUsername(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tokens":{"access_token":"access-token-123"}}`))
	}))
	defer server.Close()

	logs := []string{}
	_, err := accountLogin(accountManagerContext{
		BaseURL:       server.URL,
		AdminEmail:    "admin@example.test",
		AdminPassword: "super-secret-password",
	}, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatalf("accountLogin returned error: %v", err)
	}
	body := strings.Join(logs, "\n")
	if !strings.Contains(body, "username=admin@example.test") {
		t.Fatalf("login logs missing username:\n%s", body)
	}
	if strings.Contains(body, "super-secret-password") {
		t.Fatalf("login logs leaked password:\n%s", body)
	}
}

func TestAccountFindDeviceByVideoCloudDevidSkipsDisabledAndPaginates(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.Header.Get("authorization"); got != "Bearer user-token" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/orgs/org-123/devices" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		switch r.URL.Query().Get("offset") {
		case "0":
			devices := []map[string]any{{"id": "disabled-device", "metadata": map[string]any{"video_cloud_devid": "load-device-0001"}, "disabled_at": "2026-06-01T00:00:00Z"}}
			for i := 1; i < 200; i++ {
				devices = append(devices, map[string]any{"id": fmt.Sprintf("other-%03d", i), "metadata": map[string]any{"video_cloud_devid": fmt.Sprintf("other-device-%03d", i)}, "disabled_at": nil})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": devices})
		case "200":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"devices":[{"id":"active-device","metadata":{"video_cloud_devid":"load-device-0001"},"disabled_at":null}]}`))
		default:
			t.Fatalf("unexpected offset: %s", r.URL.Query().Get("offset"))
		}
	}))
	defer server.Close()

	device, found, err := accountFindDeviceByVideoCloudDevid(accountManagerContext{BaseURL: server.URL}, "user-token", "org-123", "load-device-0001")
	if err != nil {
		t.Fatalf("accountFindDeviceByVideoCloudDevid returned error: %v", err)
	}
	if !found {
		t.Fatal("expected device to be found")
	}
	if got := stringValue(device["id"]); got != "active-device" {
		t.Fatalf("device id = %q", got)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestAccountIndexDevicesByVideoCloudDevidBuildsReusableMap(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("authorization"); got != "Bearer user-token" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/orgs/org-123/devices" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []map[string]any{
			{"id": "active-1", "metadata": map[string]any{"video_cloud_devid": "load-device-0001"}, "disabled_at": nil},
			{"id": "disabled-2", "metadata": map[string]any{"video_cloud_devid": "load-device-0002"}, "disabled_at": "2026-06-01T00:00:00Z"},
			{"id": "active-3", "metadata": map[string]any{"video_cloud_devid": "load-device-0003"}, "disabled_at": nil},
		}})
	}))
	defer server.Close()

	index, count, err := accountIndexDevicesByVideoCloudDevid(accountManagerContext{BaseURL: server.URL}, "user-token", "org-123")
	if err != nil {
		t.Fatalf("accountIndexDevicesByVideoCloudDevid returned error: %v", err)
	}
	if count != 2 || len(index) != 2 {
		t.Fatalf("count=%d len=%d index=%#v", count, len(index), index)
	}
	if got := stringValue(index["load-device-0001"]["id"]); got != "active-1" {
		t.Fatalf("load-device-0001 id = %q", got)
	}
	if _, ok := index["load-device-0002"]; ok {
		t.Fatal("disabled device should not be indexed")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestErrorBodySuffixIncludesStructuredAPIError(t *testing.T) {
	got := errorBodySuffix([]byte(`{"error":"already_claimed","message":"Claim token has already been claimed"}`))
	want := ": already_claimed (Claim token has already been claimed)"
	if got != want {
		t.Fatalf("suffix = %q, want %q", got, want)
	}
}

func TestSelectObjectReleaseSupportsHTTPObjectStorage(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket" && r.URL.Query().Get("prefix") == "releases/":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult>
<Contents><Key>releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json</Key><LastModified>2026-06-01T00:00:00Z</LastModified><Size>1</Size></Contents>
<Contents><Key>releases/rtk_video_cloud-v1.2.3/manifest.json</Key><LastModified>2026-05-31T00:00:00Z</LastModified><Size>1</Size></Contents>
<IsTruncated>false</IsTruncated>
</ListBucketResult>`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json":
			_, _ = w.Write([]byte(`{"version":"ci-20260527-093000-abcdef123456","artifact_path":"releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-v1.2.3/manifest.json":
			_, _ = w.Write([]byte(`{"version":"v1.2.3","artifact_path":"releases/rtk_video_cloud-v1.2.3/v1.2.3.tar.gz"}`))
		case r.Method == http.MethodHead && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected object storage request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	version, key, err := selectObjectRelease(map[string]string{
		"LINODE_OBJ_BUCKET":            "test-bucket",
		"LINODE_OBJ_ENDPOINT":          server.URL,
		"LINODE_OBJ_ACCESS_KEY_ID":     "access",
		"LINODE_OBJ_SECRET_ACCESS_KEY": "secret",
	}, "Video Cloud", "rtk_video_cloud", "")
	if err != nil {
		t.Fatalf("selectObjectRelease returned error: %v", err)
	}
	if version != "ci-20260527-093000-abcdef123456" {
		t.Fatalf("version = %q", version)
	}
	if key != "releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz" {
		t.Fatalf("key = %q", key)
	}
	if len(requests) < 4 || !strings.HasPrefix(requests[0], "GET /test-bucket?") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestMaterializeReleaseBundleSupportsHTTPObjectStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_account_manager-account-test/manifest.json":
			_, _ = w.Write([]byte(`{"version":"account-test","artifact_path":"releases/rtk_account_manager-account-test/account-test.tar.gz"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_account_manager-account-test/account-test.tar.gz":
			_, _ = w.Write([]byte("bundle"))
		default:
			t.Fatalf("unexpected object storage request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	out, err := materializeReleaseBundle(t.TempDir(), map[string]string{
		"LINODE_OBJ_BUCKET":            "test-bucket",
		"LINODE_OBJ_ENDPOINT":          server.URL,
		"LINODE_OBJ_ACCESS_KEY_ID":     "access",
		"LINODE_OBJ_SECRET_ACCESS_KEY": "secret",
	}, "rtk_account_manager", "account-test")
	if err != nil {
		t.Fatalf("materializeReleaseBundle returned error: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "bundle" {
		t.Fatalf("bundle = %q", string(data))
	}
}

func TestVideoDeployArgsSupportBinaryOnlyFastMode(t *testing.T) {
	paths := provisionPaths{
		Workspace:   "/workspace",
		EnvRoot:     "/env",
		VideoConfig: "/env/topology/video-cloud-staging.yaml",
		VideoEnv:    "/env/services/video-cloud/video-cloud-staging.env",
		OperatorEnv: "/env/env/operator.env",
	}
	args := videoDeployArgs(paths, map[string]string{
		"CLOUD_STACK_NAME":              "video-cloud-stg-0529",
		"VIDEO_CLOUD_DOMAIN":            "video-cloud.example.test",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud.example.test",
	}, provisionOptions{
		videoRelease: "v-fast",
		binaryOnly:   true,
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--release v-fast", "--binary-only", "--config /env/topology/video-cloud-staging.yaml"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("video deploy args missing %q: %#v", want, args)
		}
	}
	for _, notWant := range []string{"--secrets-file", "--certbot-extra-domain"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("binary-only video deploy args unexpectedly include %q: %s", notWant, joined)
		}
	}

	full := strings.Join(videoDeployArgs(paths, map[string]string{"CLOUD_STACK_NAME": "stack"}, provisionOptions{videoRelease: "v-full"}), " ")
	if strings.Contains(full, "--binary-only") {
		t.Fatalf("full deploy args unexpectedly include binary-only: %s", full)
	}
	for _, want := range []string{"--secrets-file", "--certbot-extra-domain"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full video deploy args missing %q: %s", want, full)
		}
	}
}

func TestDeployLocalBuildCannotCombineWithLoggerOnly(t *testing.T) {
	err := runDeploy([]string{"--env-root", t.TempDir(), "--local-build", "--logger-only"})
	if err == nil || !strings.Contains(err.Error(), "--local-build cannot be combined with --logger-only") {
		t.Fatalf("expected local-build/logger-only conflict, got %v", err)
	}
}

func TestProvisionArgsAcceptLocalBuild(t *testing.T) {
	opts, err := parseProvisionArgs([]string{"--env-root", t.TempDir(), "--all", "--local-build"})
	if err != nil {
		t.Fatalf("parseProvisionArgs returned error: %v", err)
	}
	if !opts.localBuild {
		t.Fatal("expected provision --local-build to set localBuild")
	}
}

func TestStgDeployShortcutDefaultsToVideoBinaryOnly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeGo := filepath.Join(tmp, "fake-go")
	writeFile(t, fakeGo, "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$RTK_FAKE_GO_ARGS\"\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "bin", "stg.sh"), "deploy")
	cmd.Env = append(os.Environ(),
		"RTK_CLOUD_GO="+fakeGo,
		"RTK_FAKE_GO_ARGS="+argsFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stg.sh deploy failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(data)), " ")
	for _, want := range []string{"deploy", "--video-only", "--binary-only"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stg.sh deploy args missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "--video-release") {
		t.Fatalf("stg.sh deploy should not require an explicit release by default: %s", got)
	}
}

func TestStgDeployShortcutAcceptsOptionalRelease(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeGo := filepath.Join(tmp, "fake-go")
	writeFile(t, fakeGo, "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$RTK_FAKE_GO_ARGS\"\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "bin", "stg.sh"), "deploy", "v-test")
	cmd.Env = append(os.Environ(),
		"RTK_CLOUD_GO="+fakeGo,
		"RTK_FAKE_GO_ARGS="+argsFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stg.sh deploy v-test failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(data)), " ")
	for _, want := range []string{"deploy", "--video-only", "--binary-only", "--video-release v-test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stg.sh deploy v-test args missing %q: %s", want, got)
		}
	}
}

func TestStgDeployLocalShortcutBuildsLocalBinaryRelease(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeGo := filepath.Join(tmp, "fake-go")
	writeFile(t, fakeGo, "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$RTK_FAKE_GO_ARGS\"\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "bin", "stg.sh"), "deploy-local", "v-local")
	cmd.Env = append(os.Environ(),
		"RTK_CLOUD_GO="+fakeGo,
		"RTK_FAKE_GO_ARGS="+argsFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stg.sh deploy-local failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(data)), " ")
	for _, want := range []string{"deploy", "--video-only", "--binary-only", "--local-build", "--video-release v-local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stg.sh deploy-local args missing %q: %s", want, got)
		}
	}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
