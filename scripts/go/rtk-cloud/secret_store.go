package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const secretInventorySchemaVersion = 1

var (
	secretEnvironmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	operatorKeyPattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type secretCatalogEntry struct {
	ID         string             `json:"id"`
	Category   string             `json:"category"`
	Consumers  []string           `json:"consumers"`
	Rotation   string             `json:"rotation"`
	K8SBinding []secretK8SBinding `json:"k8s_bindings,omitempty"`
}

type secretK8SBinding struct {
	NamespaceSuffix string `json:"namespace_suffix"`
	Secret          string `json:"secret"`
	Key             string `json:"key"`
}

type secretInventory struct {
	SchemaVersion int                    `json:"schema_version"`
	Environment   string                 `json:"environment"`
	Entries       []secretInventoryEntry `json:"entries"`
}

type secretInventoryEntry struct {
	ID        string   `json:"id"`
	Category  string   `json:"category"`
	Path      string   `json:"path"`
	Consumers []string `json:"consumers,omitempty"`
	Rotation  string   `json:"rotation,omitempty"`
}

type secretStore struct {
	ConfigRoot  string
	Environment string
	Root        string
}

var activeSecretEnvironmentRoot string

func rtkCloudTestMode() bool { return os.Getenv("RTK_CLOUD_TEST_MODE") == "1" }

func sensitiveEnvironmentPath(paths provisionPaths, category string, elements ...string) string {
	if activeSecretEnvironmentRoot == "" {
		if category == "kube" {
			return filepath.Join(append([]string{paths.EnvRoot, "state"}, elements...)...)
		}
		parts := append([]string{paths.EnvRoot, "state", category}, elements...)
		return filepath.Join(parts...)
	}
	base := map[string]string{
		"kube":         filepath.Join(activeSecretEnvironmentRoot, "kube"),
		"certissuer":   filepath.Join(activeSecretEnvironmentRoot, "pki", "certissuer"),
		"mqtt-tls":     filepath.Join(activeSecretEnvironmentRoot, "pki", "mqtt"),
		"public-https": filepath.Join(activeSecretEnvironmentRoot, "pki", "public-https"),
		"openbao":      filepath.Join(activeSecretEnvironmentRoot, "openbao"),
	}[category]
	if base == "" {
		panic("unsupported sensitive path category: " + category)
	}
	return filepath.Join(append([]string{base}, elements...)...)
}

func defaultRTKCloudConfigRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("RTK_CLOUD_CONFIG_ROOT")); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("cannot resolve home directory for RTK Cloud config root")
	}
	return filepath.Join(home, ".config", "rtk_cloud"), nil
}

func newSecretStore(configRoot, environment string) (secretStore, error) {
	environment = strings.TrimSpace(environment)
	if !secretEnvironmentPattern.MatchString(environment) || environment == "." || environment == ".." {
		return secretStore{}, fmt.Errorf("invalid environment name %q", environment)
	}
	if strings.TrimSpace(configRoot) == "" {
		var err error
		configRoot, err = defaultRTKCloudConfigRoot()
		if err != nil {
			return secretStore{}, err
		}
	}
	abs, err := filepath.Abs(configRoot)
	if err != nil {
		return secretStore{}, fmt.Errorf("resolve RTK Cloud config root: %w", err)
	}
	return secretStore{ConfigRoot: filepath.Clean(abs), Environment: environment, Root: filepath.Join(filepath.Clean(abs), environment)}, nil
}

func (s secretStore) RuntimeDir() string { return filepath.Join(s.Root, "runtime") }

func (s secretStore) KubeconfigPath() string {
	return filepath.Join(s.Root, "kube", "kubeconfig.yaml")
}

func (s secretStore) runtimePath(id string) (string, error) {
	if !secretEnvironmentPattern.MatchString(id) {
		return "", fmt.Errorf("invalid runtime secret id %q", id)
	}
	return s.safePath(filepath.Join("runtime", id))
}

func (s secretStore) operatorPath(key string) (string, error) {
	if !operatorKeyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid operator key %q", key)
	}
	return s.safePath(filepath.Join("operator", "env", key))
}

func (s secretStore) safePath(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("secret path must be relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("secret path escapes environment root: %q", relative)
	}
	path := filepath.Join(s.Root, clean)
	rel, err := filepath.Rel(s.Root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("secret path escapes environment root: %q", relative)
	}
	return path, nil
}

func (s secretStore) ensureLayout() error {
	if err := ensurePrivateDirectory(s.ConfigRoot); err != nil {
		return err
	}
	for _, relative := range []string{
		"operator/env", "runtime", "kube", "pki/certissuer", "pki/mqtt", "pki/public-https",
		"openbao", "test/devices", "test/databases", "test/credential-bundles", "test/archive", "migration-backup",
	} {
		path, err := s.safePath(relative)
		if err != nil {
			return err
		}
		if err := ensurePrivateDirectory(path); err != nil {
			return err
		}
	}
	return s.writeInventory()
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("secure directory %s must be a real directory", path)
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("secure directory %s must use mode 0700: %w", path, err)
			}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func (s secretStore) write(relative string, value []byte, replace bool) error {
	path, err := s.safePath(relative)
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("secret path %s must be a regular file", path)
		}
		if !replace {
			return fmt.Errorf("secret path %s already exists", path)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s secretStore) read(relative string) (string, error) {
	path, err := s.safePath(relative)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret path %s must be a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("secret path %s must use mode 0600", path)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func (s secretStore) readRuntime(id string) (string, error) {
	path, err := s.runtimePath(id)
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(s.Root, path)
	return s.read(rel)
}

func (s secretStore) readOperator() (map[string]string, error) {
	dir, err := s.safePath(filepath.Join("operator", "env"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !operatorKeyPattern.MatchString(entry.Name()) {
			return nil, fmt.Errorf("invalid operator credential entry %q", entry.Name())
		}
		value, err := s.read(filepath.Join("operator", "env", entry.Name()))
		if err != nil {
			return nil, err
		}
		values[entry.Name()] = value
	}
	return values, nil
}

func (s secretStore) writeInventory() error {
	entries := make([]secretInventoryEntry, 0, len(rtkSecretCatalog()))
	for _, item := range rtkSecretCatalog() {
		entries = append(entries, secretInventoryEntry{
			ID: item.ID, Category: item.Category, Path: filepath.ToSlash(filepath.Join("runtime", item.ID)),
			Consumers: append([]string(nil), item.Consumers...), Rotation: item.Rotation,
		})
	}
	payload, err := json.MarshalIndent(secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: s.Environment, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return s.write("inventory.json", payload, true)
}

func rtkSecretCatalog() []secretCatalogEntry {
	ids := []struct {
		id, consumer, rotation string
	}{
		{"postgres", "postgresql,account-manager,video-cloud,billing", "manual"},
		{"jwt-access", "account-manager", "manual"}, {"jwt-refresh", "account-manager", "manual"},
		{"internal-auth", "account-manager,video-cloud", "manual"}, {"video-auth", "video-cloud", "manual"},
		{"platform-admin", "account-manager", "manual"}, {"factory-enroll-auth", "factoryenroll", "manual"},
		{"factory-production-jwt", "account-manager,factoryenroll", "manual"},
		{"factory-admission", "account-manager,factoryenroll", "manual"},
		{"mqtt-broker-auth", "video-cloud,mqtt", "manual"}, {"mqtt-server-password", "video-cloud,mqtt", "manual"},
		{"mqtt-usage-ingest", "video-cloud-workers", "manual"}, {"emqx-dashboard-password", "mqtt", "manual"},
		{"turn-shared", "video-cloud,coturn", "manual"}, {"turn-registry-node-auth", "video-cloud-workers,coturn", "manual"},
		{"cloud-logger-ingest-token", "video-cloud,cloud-logger", "manual"},
		{"cloud-logger-billing-usage-token", "video-cloud,cloud-logger", "manual"},
		{"grafana-admin-password", "grafana", "manual"}, {"clip-private-key-seed", "video-cloud", "manual"},
		{"billing-service-token", "billing,cloud-admin", "manual"}, {"billing-internal-token", "billing", "manual"},
		{"job-authorization-token", "account-manager,cloud-admin", "manual"},
		{"billing-debit-token", "billing", "manual"}, {"payment-simulator-shared", "billing", "manual"},
		{"billing-cloud-creation", "account-manager,billing", "manual"},
		{"billing-handoff", "account-manager,billing", "manual"},
		{"factory-handoff", "account-manager,factoryenroll", "manual"},
		{"video-control-handoff", "account-manager,video-cloud", "manual"},
		{"mqtt-usage-handoff", "account-manager,video-cloud-workers", "manual"},
		{"mqtt-usage-settlement", "billing,video-cloud-workers", "manual"},
		{"emqx-handoff-api-key", "mqtt,video-cloud-workers", "manual"},
		{"emqx-handoff-api-secret", "mqtt,video-cloud-workers", "manual"},
		{"payment-simulator-callback", "billing", "manual"}, {"payment-simulator-admin-token", "billing", "manual"},
		{"payment-reference-encryption", "billing", "manual"}, {"newebpay-hash-key-seed", "billing", "manual"},
		{"newebpay-hash-iv-seed", "billing", "manual"}, {"email-outbox-encryption", "account-manager", "manual"},
		{"github-oauth-client-secret", "account-manager", "manual"}, {"google-oauth-client-secret", "account-manager", "manual"}, {"social-oauth-state-secret", "account-manager", "manual"},
	}
	out := make([]secretCatalogEntry, 0, len(ids))
	for _, item := range ids {
		out = append(out, secretCatalogEntry{ID: item.id, Category: "runtime", Consumers: strings.Split(item.consumer, ","), Rotation: item.rotation, K8SBinding: catalogK8SBindings(item.id)})
	}
	return out
}

func catalogK8SBindings(id string) []secretK8SBinding {
	table := map[string][]secretK8SBinding{
		"postgres":               {{"-platform", "postgresql-runtime", "POSTGRES_PASSWORD"}},
		"jwt-access":             {{"-account-manager", "account-manager-runtime", "JWT_ACCESS_SECRET"}},
		"jwt-refresh":            {{"-account-manager", "account-manager-runtime", "JWT_REFRESH_SECRET"}},
		"internal-auth":          {{"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"}},
		"video-auth":             {{"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_AUTH_SECRET"}},
		"platform-admin":         {{"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"}},
		"factory-enroll-auth":    {{"-video-cloud", "factoryenroll-runtime", "FACTORY_ENROLL_AUTH_KEY"}},
		"factory-production-jwt": {{"-video-cloud", "factoryenroll-runtime", "FACTORY_ENROLL_PRODUCTION_JWT_SECRET"}},
		"factory-admission": {
			{"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_FACTORY_ENROLLMENT_TOKEN"},
			{"-video-cloud", "factoryenroll-runtime", "FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN"},
		},
		"mqtt-broker-auth":                 {{"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY"}},
		"mqtt-server-password":             {{"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_MQTT_SERVER_PASSWORD"}},
		"mqtt-usage-ingest":                {{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN"}},
		"emqx-dashboard-password":          {{"-video-cloud", "mqtt-runtime", "EMQX_DASHBOARD_PASSWORD"}},
		"turn-shared":                      {{"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_TURN_SHARED_SECRET"}},
		"turn-registry-node-auth":          {{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY"}},
		"cloud-logger-ingest-token":        {{"-logger", "cloud-logger-runtime", "RTK_CLOUD_LOGGER_TOKEN"}},
		"cloud-logger-billing-usage-token": {{"-logger", "cloud-logger-runtime", "RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN"}},
		"grafana-admin-password":           {{"-observability", "video-cloud-grafana-admin", "admin-password"}},
		"billing-service-token":            {{"-billing", "billing-runtime", "BILLING_SERVICE_TOKEN"}},
		"job-authorization-token": {
			{"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"},
			{"-cloud-admin", "cloud-admin-billing-client", "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"},
		},
		"billing-internal-token": {
			{"-billing", "billing-runtime", "BILLING_INTERNAL_TOKEN"},
			{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_BILLING_USAGE_TOKEN"},
		},
		"billing-debit-token": {{"-billing", "billing-runtime", "BILLING_DEBIT_TOKEN"}},
		"billing-cloud-creation": {
			{"-account-manager", "account-manager-runtime", "BILLING_CLOUD_CREATION_TOKEN"},
			{"-billing", "billing-runtime", "BILLING_CLOUD_CREATION_TOKEN"},
		},
		"billing-handoff": {
			{"-account-manager", "account-manager-runtime", "BILLING_HANDOFF_TOKEN"},
			{"-billing", "billing-runtime", "BILLING_HANDOFF_TOKEN"},
		},
		"factory-handoff": {
			{"-account-manager", "account-manager-runtime", "FACTORY_HANDOFF_TOKEN"},
			{"-video-cloud", "factoryenroll-runtime", "FACTORY_ENROLL_RECOVERY_TOKEN"},
		},
		"video-control-handoff": {
			{"-account-manager", "account-manager-runtime", "VIDEO_CONTROL_PLANE_HANDOFF_TOKEN"},
			{"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN"},
		},
		"mqtt-usage-handoff": {
			{"-account-manager", "account-manager-runtime", "MQTT_USAGE_HANDOFF_TOKEN"},
			{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN"},
		},
		"mqtt-usage-settlement": {
			{"-billing", "billing-runtime", "MQTT_USAGE_SETTLEMENT_TOKEN"},
			{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN"},
		},
		"emqx-handoff-api-key": {
			{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_EMQX_API_KEY"},
		},
		"emqx-handoff-api-secret": {
			{"-video-cloud", "video-cloud-workers-runtime", "VIDEO_CLOUD_EMQX_API_SECRET"},
		},
		"payment-simulator-shared":      {{"-billing", "billing-runtime", "PAYMENT_SIMULATOR_SHARED_SECRET"}},
		"payment-simulator-callback":    {{"-billing", "billing-runtime", "PAYMENT_SIMULATOR_CALLBACK_SECRET"}},
		"payment-simulator-admin-token": {{"-billing", "billing-runtime", "PAYMENT_SIMULATOR_ADMIN_TOKEN"}},
		"payment-reference-encryption":  {{"-billing", "billing-runtime", "PAYMENT_REFERENCE_ENCRYPTION_KEY"}},
		"email-outbox-encryption":       {{"-account-manager", "account-manager-runtime", "EMAIL_OUTBOX_ENCRYPTION_KEY"}},
		"github-oauth-client-secret":    {{"-account-manager", "account-manager-runtime", "GITHUB_OAUTH_CLIENT_SECRET"}},
		"google-oauth-client-secret":    {{"-account-manager", "account-manager-runtime", "GOOGLE_OAUTH_CLIENT_SECRET"}},
		"social-oauth-state-secret":     {{"-account-manager", "account-manager-runtime", "SOCIAL_OAUTH_STATE_SECRET"}},
	}
	return table[id]
}

func runSecrets(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stdout, "Usage: rtk-cloud secrets <init|plan|migrate|verify|inventory> --environment NAME [--config-root PATH]")
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("secrets "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "environment name")
	configRoot := fs.String("config-root", "", "RTK Cloud config root")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "stack confirmation for migration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *environment == "" {
		return errors.New("--environment is required")
	}
	store, err := newSecretStore(*configRoot, *environment)
	if err != nil {
		return err
	}
	if *workspace == "" {
		*workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	switch action {
	case "init":
		if err := store.ensureLayout(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "initialized %s secret store\n", store.Environment)
		return nil
	case "plan":
		return planSecretMigration(os.Stdout, store, *workspace)
	case "migrate":
		expected := "video-cloud-" + store.Environment
		if *confirm != expected {
			return fmt.Errorf("--confirm %s is required", expected)
		}
		return migrateSecrets(store, *workspace)
	case "verify":
		return verifySecretStore(os.Stdout, store, *workspace)
	case "inventory":
		return printSecretInventory(os.Stdout, store)
	default:
		return fmt.Errorf("unknown secrets command %q", action)
	}
}

func printSecretInventory(out io.Writer, store secretStore) error {
	if _, err := os.Stat(filepath.Join(store.Root, "inventory.json")); err != nil {
		return err
	}
	for _, entry := range rtkSecretCatalog() {
		bindings := make([]string, 0, len(entry.K8SBinding))
		for _, binding := range entry.K8SBinding {
			bindings = append(bindings, binding.NamespaceSuffix+"/"+binding.Secret+":"+binding.Key)
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", entry.ID, entry.Category, strings.Join(entry.Consumers, ","), strings.Join(bindings, ","))
	}
	return nil
}

func planSecretMigration(out io.Writer, store secretStore, workspace string) error {
	legacyRoot := filepath.Join(workspace, "cloud_env", store.Environment, "runtime")
	home, _ := os.UserHomeDir()
	sources := []string{filepath.Join(home, ".env"), filepath.Join(home, ".config", "rtk-cloud"), filepath.Join(legacyRoot, "state"), filepath.Join(legacyRoot, "services"), filepath.Join(legacyRoot, "devices"), filepath.Join(legacyRoot, "artifacts", "test-data")}
	fmt.Fprintf(out, "destination: %s\n", store.Root)
	for _, source := range sources {
		if _, err := os.Lstat(source); err == nil {
			fmt.Fprintf(out, "source: %s\n", source)
		}
	}
	missing := []string{}
	for _, entry := range rtkSecretCatalog() {
		if _, err := store.readRuntime(entry.ID); err != nil {
			missing = append(missing, entry.ID)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		fmt.Fprintf(out, "required: %s\n", id)
	}
	return nil
}

func migrateSecrets(destination secretStore, workspace string) error {
	if _, err := os.Lstat(destination.Root); err == nil {
		return fmt.Errorf("destination %s already exists; migration refuses to overwrite", destination.Root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensurePrivateDirectory(destination.ConfigRoot); err != nil {
		return err
	}
	tmpRoot, err := os.MkdirTemp(destination.ConfigRoot, destination.Environment+".migrating-")
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpRoot, 0o700); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpRoot)
		}
	}()
	staged := destination
	staged.Root = tmpRoot
	if err := staged.ensureLayout(); err != nil {
		return err
	}
	legacyRoot := filepath.Join(workspace, "cloud_env", destination.Environment, "runtime")
	home, _ := os.UserHomeDir()
	if err := importEnvFileToStore(staged, filepath.Join(home, ".env")); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(home, ".config", "rtk-cloud", "shared.env"), filepath.Join(home, ".config", "rtk-cloud", "environments", destination.Environment+".env")} {
		if err := importEnvFileToStore(staged, path); err != nil {
			return err
		}
	}
	legacySecretDir := filepath.Join(legacyRoot, "state", "secrets")
	if entries, readErr := os.ReadDir(legacySecretDir); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			value, readErr := os.ReadFile(filepath.Join(legacySecretDir, entry.Name()))
			if readErr != nil {
				return readErr
			}
			if err := staged.write(filepath.Join("runtime", entry.Name()), value, true); err != nil {
				return err
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := importLiveK8SRuntimeSecrets(staged, legacyRoot); err != nil {
		return err
	}
	for _, move := range []struct{ source, target string }{
		{filepath.Join(legacyRoot, "state", "kubeconfig.yaml"), "kube/kubeconfig.yaml"},
		{filepath.Join(legacyRoot, "state", "certissuer"), "pki/certissuer"},
		{filepath.Join(legacyRoot, "state", "mqtt-tls"), "pki/mqtt"},
		{filepath.Join(legacyRoot, "state", "public-https"), "pki/public-https"},
		{filepath.Join(legacyRoot, "state", "openbao"), "openbao"},
		{filepath.Join(legacyRoot, "artifacts", "test-data"), "test/databases"},
	} {
		if err := copySensitivePath(staged, move.source, move.target); err != nil {
			return err
		}
	}
	if err := copyTestDeviceMaterial(staged, filepath.Join(legacyRoot, "devices", "test_device")); err != nil {
		return err
	}
	if err := copySensitiveArtifacts(staged, workspace, legacyRoot, "test/archive"); err != nil {
		return err
	}
	if err := verifySecretStoreContents(staged); err != nil {
		return fmt.Errorf("staged secret store verification failed: %w", err)
	}
	if err := os.Rename(tmpRoot, destination.Root); err != nil {
		return err
	}
	committed = true
	if err := archiveAndRemoveLegacySecrets(destination, workspace); err != nil {
		return fmt.Errorf("secret store committed but legacy cleanup failed: %w", err)
	}
	fmt.Fprintf(os.Stdout, "migrated %s secrets to %s\n", destination.Environment, destination.Root)
	return nil
}

func copyTestDeviceMaterial(store secretStore, source string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("legacy test device path must not contain symlinks: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join("test", "devices", "test_device", rel)
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			target = filepath.Join("test", "archive", "quarantine", "devices", "test_device", rel)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return store.write(target, value, true)
	})
}

func importEnvFileToStore(store secretStore, path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		if !ok || !operatorKeyPattern.MatchString(key) {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if err := store.write(filepath.Join("operator", "env", key), []byte(value+"\n"), true); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func importLiveK8SRuntimeSecrets(store secretStore, legacyRoot string) error {
	kubeconfig := filepath.Join(legacyRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	stack := "video-cloud-" + store.Environment
	for _, entry := range rtkSecretCatalog() {
		for _, binding := range entry.K8SBinding {
			cmd := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", stack+binding.NamespaceSuffix, "get", "secret", binding.Secret, "-o", "json")
			out, err := cmd.Output()
			if err != nil {
				continue
			}
			var payload struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				return fmt.Errorf("decode K8s secret metadata for %s: %w", entry.ID, err)
			}
			encoded := strings.TrimSpace(payload.Data[binding.Key])
			if encoded == "" {
				continue
			}
			value, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(value) == 0 {
				return fmt.Errorf("decode live K8s value for %s", entry.ID)
			}
			if err := store.write(filepath.Join("runtime", entry.ID), append(value, '\n'), true); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func copySensitivePath(store secretStore, source, target string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("legacy sensitive path %s must not be a symlink", source)
	}
	if info.Mode().IsRegular() {
		value, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return store.write(target, value, true)
	}
	if !info.IsDir() {
		return fmt.Errorf("unsupported sensitive source %s", source)
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("legacy sensitive path %s must not contain symlinks", source)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." || entry.IsDir() {
			return err
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return store.write(filepath.Join(target, rel), value, true)
	})
}

func copySensitiveArtifacts(store secretStore, workspace, legacyRoot, targetRoot string) error {
	roots := []struct {
		path, label string
	}{{filepath.Join(legacyRoot, "artifacts"), "environment-artifacts"}, {filepath.Join(workspace, ".artifacts"), "workspace-artifacts"}}
	for _, root := range roots {
		if _, err := os.Stat(root.path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("sensitive artifact path must not be a symlink: %s", path)
			}
			rel, err := filepath.Rel(root.path, path)
			if err != nil || !isSensitiveArtifactPath(rel) {
				return err
			}
			value, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return store.write(filepath.Join(targetRoot, root.label, rel), value, true)
		}); err != nil {
			return err
		}
	}
	return nil
}

func isSensitiveArtifactPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(lower, "/credential-bundles/") || strings.HasPrefix(lower, "credential-bundles/") ||
		strings.HasSuffix(base, ".sqlite") || strings.HasSuffix(base, ".sqlite.gz") || strings.HasSuffix(base, ".db")
}

func verifySecretStore(out io.Writer, store secretStore, workspace string) error {
	if err := verifySecretStoreContents(store); err != nil {
		return err
	}
	if err := verifySecretStoreK8SBindings(store); err != nil {
		return err
	}
	legacyRoot := filepath.Join(workspace, "cloud_env", store.Environment, "runtime")
	for _, path := range []string{filepath.Join(legacyRoot, "state", "secrets"), filepath.Join(legacyRoot, "state", "kubeconfig.yaml"), filepath.Join(legacyRoot, "state", "openbao"), filepath.Join(legacyRoot, "services", "video-cloud", "video-cloud.env")} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("legacy sensitive path still exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(out, "verified %s secret store\n", store.Environment)
	return nil
}

// verifySecretStoreK8SBindings compares values in memory and only reports
// logical IDs. Kubernetes remains a deployment mirror and is never used to
// update the canonical store outside the explicit migration command.
func verifySecretStoreK8SBindings(store secretStore) error {
	kubeconfig := store.KubeconfigPath()
	if _, err := os.Stat(kubeconfig); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	stack := "video-cloud-" + store.Environment
	for _, entry := range rtkSecretCatalog() {
		if len(entry.K8SBinding) == 0 {
			continue
		}
		canonical, err := store.readRuntime(entry.ID)
		if err != nil {
			return fmt.Errorf("read canonical secret %s: %w", entry.ID, err)
		}
		matched := false
		for _, binding := range entry.K8SBinding {
			cmd := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "-n", stack+binding.NamespaceSuffix, "get", "secret", binding.Secret, "-o", "json")
			out, commandErr := cmd.Output()
			if commandErr != nil {
				continue
			}
			var payload struct {
				Data map[string]string `json:"data"`
			}
			if json.Unmarshal(out, &payload) != nil {
				return fmt.Errorf("K8s binding for %s returned invalid metadata", entry.ID)
			}
			value, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.Data[binding.Key]))
			if decodeErr != nil || len(value) == 0 {
				continue
			}
			if strings.TrimSpace(string(value)) != canonical {
				return fmt.Errorf("K8s mirror differs from canonical secret: %s", entry.ID)
			}
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("K8s binding is missing for canonical secret: %s", entry.ID)
		}
	}
	return nil
}

func verifySecretStoreContents(store secretStore) error {
	rootInfo, err := os.Lstat(store.Root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 {
		return fmt.Errorf("secret environment root must be a real 0700 directory")
	}
	if err := filepath.WalkDir(store.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is forbidden in secret store: %s", path)
		}
		if entry.IsDir() && info.Mode().Perm() != 0o700 {
			return fmt.Errorf("secret directory must use mode 0700: %s", path)
		}
		if !entry.IsDir() && info.Mode().IsRegular() && info.Mode().Perm() != 0o600 {
			return fmt.Errorf("secret file must use mode 0600: %s", path)
		}
		return nil
	}); err != nil {
		return err
	}
	missing := []string{}
	for _, entry := range rtkSecretCatalog() {
		value, err := store.readRuntime(entry.ID)
		if err != nil || value == "" {
			missing = append(missing, entry.ID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("required runtime secrets are missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

func archiveAndRemoveLegacySecrets(store secretStore, workspace string) error {
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	backupRoot, err := store.safePath(filepath.Join("migration-backup", timestamp))
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(backupRoot); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	legacyRoot := filepath.Join(workspace, "cloud_env", store.Environment, "runtime")
	targets := []struct{ source, backup string }{
		{filepath.Join(home, ".env"), "home-env"},
		{filepath.Join(home, ".config", "rtk-cloud"), "legacy-rtk-cloud"},
		{filepath.Join(legacyRoot, "state", "secrets"), "workspace/state/secrets"},
		{filepath.Join(legacyRoot, "state", "kubeconfig.yaml"), "workspace/state/kubeconfig.yaml"},
		{filepath.Join(legacyRoot, "state", "kubeconfig.stale-20260828.yaml"), "workspace/state/kubeconfig.stale.yaml"},
		{filepath.Join(legacyRoot, "state", "certissuer"), "workspace/state/certissuer"},
		{filepath.Join(legacyRoot, "state", "mqtt-tls"), "workspace/state/mqtt-tls"},
		{filepath.Join(legacyRoot, "state", "public-https"), "workspace/state/public-https"},
		{filepath.Join(legacyRoot, "state", "openbao"), "workspace/state/openbao"},
		{filepath.Join(legacyRoot, "services", "account-manager"), "workspace/services/account-manager"},
		{filepath.Join(legacyRoot, "services", "video-cloud"), "workspace/services/video-cloud"},
		{filepath.Join(legacyRoot, "devices", "test_device"), "workspace/devices/test_device"},
		{filepath.Join(legacyRoot, "artifacts", "test-data"), "workspace/artifacts/test-data"},
	}
	backupStore := store
	backupStore.Root = backupRoot
	for _, target := range targets {
		if _, err := os.Lstat(target.source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := copySensitivePath(backupStore, target.source, target.backup); err != nil {
			return err
		}
		if err := os.RemoveAll(target.source); err != nil {
			return err
		}
	}
	artifactBackup := secretStore{ConfigRoot: store.ConfigRoot, Environment: store.Environment, Root: backupRoot}
	if err := copySensitiveArtifacts(artifactBackup, workspace, legacyRoot, "sensitive-artifacts"); err != nil {
		return err
	}
	for _, root := range []string{filepath.Join(legacyRoot, "artifacts"), filepath.Join(workspace, ".artifacts")} {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !isSensitiveArtifactPath(path) {
				return nil
			}
			return os.Remove(path)
		})
	}
	return nil
}

func configureProvisionSecretStore(environment string) (secretStore, func(), error) {
	store, err := newSecretStore("", environment)
	if err != nil {
		return secretStore{}, nil, err
	}
	if err := verifySecretStorePermissionsOnly(store); err != nil {
		return secretStore{}, nil, err
	}
	if err := verifySecretStoreContents(store); err != nil {
		return secretStore{}, nil, err
	}
	values, err := store.readOperator()
	if err != nil {
		return secretStore{}, nil, err
	}
	values["RTK_CLOUD_LKE_KUBECONFIG"] = store.KubeconfigPath()
	values["RTK_CLOUD_KUBECONFIG"] = store.KubeconfigPath()
	restore := installAllCredentialEnvironment(values)
	lkeRuntimeSecretCache = map[string]string{}
	lkeRuntimeSecretStateDir = store.RuntimeDir()
	activeSecretEnvironmentRoot = store.Root
	return store, restore, nil
}

func verifySecretStorePermissionsOnly(store secretStore) error {
	info, err := os.Lstat(store.Root)
	if err != nil {
		return fmt.Errorf("secret store %s is not initialized: %w", store.Root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("secret store %s must be a real 0700 directory", store.Root)
	}
	return nil
}

func installAllCredentialEnvironment(values map[string]string) func() {
	keys := map[string]bool{"GODADDY_KEY": true, "GODADDY_SECRET": true}
	for _, key := range deploymentCredentialKeys() {
		keys[key] = true
	}
	for key := range values {
		keys[key] = true
	}
	previous := map[string]*string{}
	for key := range keys {
		if old, ok := os.LookupEnv(key); ok {
			copy := old
			previous[key] = &copy
		} else {
			previous[key] = nil
		}
		if value, ok := values[key]; ok {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	}
	return func() {
		for key, value := range previous {
			if value == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *value)
			}
		}
	}
}
