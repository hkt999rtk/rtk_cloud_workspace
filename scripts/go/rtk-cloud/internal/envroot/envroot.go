package envroot

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	Root                      string
	OperatorEnv               string
	StackEnv                  string
	VideoConfig               string
	VideoEnv                  string
	AccountManagerEnv         string
	AccountManagerPlatformEnv string
	AdminEnv                  string
	CloudLoggerEnv            string
	VideoState                string
	AccountManagerState       string
	AdminState                string
	ArtifactsDir              string
	KeysDir                   string
	CertificatesDir           string
	TestDevicesDir            string
}

type Environment struct {
	Values map[string]string
}

var generatedKeys = []string{
	"CLOUD_STACK_NAME",
	"VIDEO_CLOUD_DOMAIN",
	"VIDEO_CLOUD_CERTISSUER_DOMAIN",
	"ACCOUNT_MANAGER_DOMAIN",
	"CLOUD_ADMIN_DOMAIN",
	"CLOUD_LOGGER_DOMAIN",
}

func GeneratedKeys() []string {
	return append([]string(nil), generatedKeys...)
}

func Resolve(workspace, envRoot string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	if envRoot == "" {
		envRoot = filepath.Join(workspace, "cloud_env", "staging", "linode")
	}
	abs, err := filepath.Abs(envRoot)
	if err != nil {
		return "", err
	}
	if filepath.Base(abs) == "staging" {
		return filepath.Join(abs, stagingProviderForRoot(abs)), nil
	}
	if isDir(filepath.Join(abs, "linode")) && !isDir(filepath.Join(abs, "services")) && !isDir(filepath.Join(abs, "env")) && !isDir(filepath.Join(abs, "topology")) {
		return filepath.Join(abs, "linode"), nil
	}
	return abs, nil
}

func stagingProviderForRoot(stagingRoot string) string {
	provider := firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), os.Getenv("RTK_CLOUD_STAGING_PROVIDER"))
	if provider == "" {
		for _, candidate := range []string{"lke", "linode"} {
			if value := FileVar(filepath.Join(stagingRoot, candidate, "env", "stack.env"), "CLOUD_PROVIDER"); value == candidate {
				provider = candidate
				break
			}
		}
	}
	if provider == "" {
		provider = "linode"
	}
	return provider
}

func NewPaths(root string) Paths {
	return Paths{
		Root:                      root,
		OperatorEnv:               filepath.Join(root, "env", "operator.env"),
		StackEnv:                  filepath.Join(root, "env", "stack.env"),
		VideoConfig:               firstExisting(filepath.Join(root, "topology", "video-cloud.yaml"), filepath.Join(root, "topology", "video-cloud-staging.yaml")),
		VideoEnv:                  firstExisting(filepath.Join(root, "services", "video-cloud", "video-cloud.env"), filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env")),
		AccountManagerEnv:         firstExisting(filepath.Join(root, "services", "account-manager", "account-manager.env"), filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env")),
		AccountManagerPlatformEnv: firstExisting(filepath.Join(root, "services", "account-manager", "platform-admin.env"), filepath.Join(root, "services", "account-manager", "account-manager-platform-admin.env")),
		AdminEnv:                  firstExisting(filepath.Join(root, "services", "cloud-admin", "admin.env"), filepath.Join(root, "services", "cloud-admin", "admin-staging.env")),
		CloudLoggerEnv:            filepath.Join(root, "services", "cloud-logger", "logger.env"),
		VideoState:                firstExisting(filepath.Join(root, "state", "video-cloud.state.json"), filepath.Join(root, "state", "video-cloud-staging.state.json")),
		AccountManagerState:       firstExisting(filepath.Join(root, "state", "account-manager.env"), filepath.Join(root, "state", "account-manager-staging.env")),
		AdminState:                firstExisting(filepath.Join(root, "state", "cloud-admin.env"), filepath.Join(root, "state", "cloud-admin-staging.env")),
		ArtifactsDir:              filepath.Join(root, "artifacts"),
		KeysDir:                   filepath.Join(root, "keys"),
		CertificatesDir:           filepath.Join(root, "certificates"),
		TestDevicesDir:            filepath.Join(root, "devices", "test_device"),
	}
}

func Load(root, dnsOverride string) (Environment, error) {
	paths := NewPaths(root)
	values, err := parseEnvFile(paths.StackEnv)
	if err != nil {
		return Environment{}, err
	}
	if values["CLOUD_ENV_NAME"] == "" {
		values["CLOUD_ENV_NAME"] = nameFromRoot(root)
	}
	if values["CLOUD_PROVIDER"] == "" {
		values["CLOUD_PROVIDER"] = "linode"
	}
	derived := Derive(values)
	for _, key := range generatedKeys {
		expected := derived[key]
		if expected == "" {
			continue
		}
		if values[key] != "" && values[key] != expected {
			return Environment{}, fmt.Errorf("%s mismatch: expected %s from CLOUD_ENV_NAME=%s; run sync-env --env-root %s", key, expected, values["CLOUD_ENV_NAME"], root)
		}
		values[key] = expected
	}
	if dnsOverride != "" && values["CLOUD_DNS_ROOT_DOMAIN"] != "" && values["CLOUD_DNS_ROOT_DOMAIN"] != dnsOverride {
		return Environment{}, fmt.Errorf("--dns-root-domain %s does not match %s CLOUD_DNS_ROOT_DOMAIN=%s", dnsOverride, paths.StackEnv, values["CLOUD_DNS_ROOT_DOMAIN"])
	}
	required := []string{
		"CLOUD_ENV_NAME", "CLOUD_PROVIDER", "CLOUD_REGION", "CLOUD_STACK_NAME",
		"VIDEO_CLOUD_DOMAIN", "VIDEO_CLOUD_CERTISSUER_DOMAIN", "ACCOUNT_MANAGER_DOMAIN", "CLOUD_ADMIN_DOMAIN",
		"CLOUD_LOGGER_DOMAIN",
	}
	for _, key := range required {
		if values[key] == "" {
			return Environment{}, fmt.Errorf("required environment metadata missing: %s", key)
		}
	}
	if values["CLOUD_PROVIDER"] != "linode" && values["CLOUD_PROVIDER"] != "lke" {
		return Environment{}, fmt.Errorf("unsupported CLOUD_PROVIDER=%s", values["CLOUD_PROVIDER"])
	}
	return Environment{Values: values}, nil
}

func Derive(values map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range values {
		out[k] = v
	}
	envName := out["CLOUD_ENV_NAME"]
	if envName == "" {
		envName = "staging"
		out["CLOUD_ENV_NAME"] = envName
	}
	dnsRoot := out["CLOUD_DNS_ROOT_DOMAIN"]
	if dnsRoot == "" {
		dnsRoot = "realtekconnect.com"
		out["CLOUD_DNS_ROOT_DOMAIN"] = dnsRoot
	}
	stack := "video-cloud-" + envName
	out["CLOUD_STACK_NAME"] = stack
	out["VIDEO_CLOUD_DOMAIN"] = stack + "." + dnsRoot
	out["VIDEO_CLOUD_CERTISSUER_DOMAIN"] = "certissuer." + stack + "." + dnsRoot
	out["ACCOUNT_MANAGER_DOMAIN"] = "account-manager." + stack + "." + dnsRoot
	out["CLOUD_ADMIN_DOMAIN"] = "admin." + stack + "." + dnsRoot
	out["CLOUD_LOGGER_DOMAIN"] = "logger." + stack + "." + dnsRoot
	return out
}

func Validate(root string, env Environment) error {
	paths := NewPaths(root)
	checks := []struct {
		label    string
		expected string
		actual   string
	}{
		{"topology stack", env.Values["CLOUD_STACK_NAME"], YAMLTopValue(paths.VideoConfig, "stack")},
		{"topology region", env.Values["CLOUD_REGION"], YAMLTopValue(paths.VideoConfig, "region")},
		{"video certissuer domain", env.Values["VIDEO_CLOUD_CERTISSUER_DOMAIN"], YAMLPathValue(paths.VideoConfig, "deploy.certissuer_domain")},
		{"Account Manager domain", env.Values["ACCOUNT_MANAGER_DOMAIN"], FileVar(paths.AccountManagerEnv, "ACCOUNT_MANAGER_DOMAIN")},
		{"Cloud Admin domain", env.Values["CLOUD_ADMIN_DOMAIN"], FileVar(paths.AdminEnv, "CLOUD_ADMIN_DOMAIN")},
		{"Cloud Logger domain", env.Values["CLOUD_LOGGER_DOMAIN"], FileVar(paths.CloudLoggerEnv, "CLOUD_LOGGER_DOMAIN")},
	}
	for _, check := range checks {
		if check.actual != "" && check.expected != check.actual {
			return fmt.Errorf("%s mismatch: expected %s, got %s", check.label, check.expected, check.actual)
		}
	}
	return nil
}

func FileVar(path, key string) string {
	values, err := parseEnvFile(path)
	if err != nil {
		return ""
	}
	return values[key]
}

func YAMLTopValue(path, key string) string {
	return YAMLPathValue(path, key)
}

func YAMLPathValue(path, dotted string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := strings.Split(dotted, ".")
	stack := []string{}
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		indent := (len(raw) - len(strings.TrimLeft(raw, " "))) / 2
		line := strings.TrimSpace(raw)
		if !strings.Contains(line, ":") {
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if indent < len(stack) {
			stack = stack[:indent]
		}
		for len(stack) < indent {
			stack = append(stack, "")
		}
		if len(stack) == indent {
			stack = append(stack, key)
		} else {
			stack[indent] = key
		}
		if equalPath(stack, parts) {
			return value
		}
	}
	return ""
}

func equalPath(stack, parts []string) bool {
	if len(stack) != len(parts) {
		return false
	}
	for i := range parts {
		if stack[i] != parts[i] {
			return false
		}
	}
	return true
}

func parseEnvFile(path string) (map[string]string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(fh)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		values[parts[0]] = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}
	return values, scanner.Err()
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return paths[len(paths)-1]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func nameFromRoot(root string) string {
	if filepath.Base(root) == "linode" {
		return filepath.Base(filepath.Dir(root))
	}
	return filepath.Base(root)
}
