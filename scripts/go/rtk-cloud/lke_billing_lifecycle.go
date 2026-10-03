package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

type billingLifecycleFlags struct{ Backup, Retirement, Compaction, Authority bool }

func lkeBillingLifecycleFlags(env map[string]string) (billingLifecycleFlags, error) {
	var flags billingLifecycleFlags
	for _, entry := range []struct {
		key    string
		target *bool
	}{
		{"LKE_BILLING_BACKUP_ENABLED", &flags.Backup},
		{"LKE_BILLING_RAW_RETIREMENT_ENABLED", &flags.Retirement},
		{"LKE_BILLING_INBOX_COMPACTION_ENABLED", &flags.Compaction},
		{"LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED", &flags.Authority},
	} {
		switch lkeEnvValue(env, entry.key) {
		case "", "false":
		case "true":
			*entry.target = true
		default:
			return flags, fmt.Errorf("%s must be true or false", entry.key)
		}
	}
	if flags.Retirement && (!flags.Backup || !flags.Authority) {
		return flags, errors.New("billing raw retirement requires backup and retention authority")
	}
	if flags.Compaction && !flags.Backup {
		return flags, errors.New("billing inbox compaction requires backup qualification")
	}
	return flags, nil
}

func lkeBillingBackupEnabled(env map[string]string) bool {
	flags, err := lkeBillingLifecycleFlags(env)
	return err == nil && flags.Backup
}
func lkeBillingAuthorityEnabled(env map[string]string) bool {
	flags, err := lkeBillingLifecycleFlags(env)
	return err == nil && flags.Authority
}

// Never use the general object's media/OTA keys, legacy environment overrides,
// seeded fixtures, or random generation as fallback for lifecycle credentials.
func lkeBillingLifecycleSecret(id string) string {
	if activeCanonicalSecretStore {
		value, err := readSensitiveFile(filepath.Join(lkeRuntimeSecretStateDir, lkeSecretFileName(id)), "billing lifecycle runtime credential "+id)
		if err == nil {
			return value
		}
		return ""
	}
	if rtkCloudTestMode() {
		return lkeRuntimeSecretCache[id]
	}
	return ""
}

type lkeBillingPublicLifecycle struct {
	Environment             string            `json:"environment"`
	Stack                   string            `json:"stack"`
	SourceEventEnvironments []string          `json:"source_event_environments"`
	ScratchDir              string            `json:"scratch_dir"`
	ScratchCapacityBytes    int64             `json:"scratch_capacity_bytes"`
	EncryptionKeyID         string            `json:"encryption_key_id"`
	Recipients              []string          `json:"recipients"`
	VerifierKeys            map[string]string `json:"verifier_keys"`
	RecoveryKeys            map[string]string `json:"recovery_keys,omitempty"`
	AuthorityURL            string            `json:"authority_url,omitempty"`
	RetentionDays           int               `json:"retention_days"`
	PartBytes               int64             `json:"part_bytes"`
	BackupEnabled           bool              `json:"backup_enabled"`
	RetirementEnabled       bool              `json:"retirement_enabled"`
	CompactionEnabled       bool              `json:"compaction_enabled"`
}
type lkeBillingPublicObjectStore struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	SigningRegion string `json:"signing_region"`
	Bucket        string `json:"bucket"`
	Environment   string `json:"environment"`
}
type lkeBillingPublicConfig struct {
	Lifecycle   lkeBillingPublicLifecycle   `json:"lifecycle"`
	ObjectStore lkeBillingPublicObjectStore `json:"object_store"`
}

func lkeBillingDecodePublicJSON(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid public JSON configuration")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("public JSON configuration contains trailing data")
	}
	return nil
}

func lkeBillingVerifierConfig(env map[string]string) (string, string, error) {
	id, value := lkeEnvValue(env, "LKE_BILLING_BACKUP_VERIFIER_KEY_ID"), lkeEnvValue(env, "LKE_BILLING_BACKUP_VERIFIER_PUBLIC_KEY")
	key, err := base64.StdEncoding.DecodeString(value)
	if !secretEnvironmentPattern.MatchString(id) || err != nil || len(key) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(key) != value {
		return "", "", errors.New("billing backup requires verifier key ID and base64 Ed25519 PUBLIC key")
	}
	return id, value, nil
}

func lkeBillingPublicConfiguration(env map[string]string) (lkeBillingPublicConfig, error) {
	flags, err := lkeBillingLifecycleFlags(env)
	if err != nil {
		return lkeBillingPublicConfig{}, err
	}
	environment := lkeEnvValue(env, "CLOUD_ENV_NAME")
	if environment != "dev" && environment != "staging" && environment != "prod" {
		return lkeBillingPublicConfig{}, errors.New("billing lifecycle requires explicit CLOUD_ENV_NAME dev, staging or prod")
	}
	stack := lkeEnvValue(env, "CLOUD_STACK_NAME")
	if !secretEnvironmentPattern.MatchString(stack) {
		return lkeBillingPublicConfig{}, errors.New("billing lifecycle requires explicit valid stack")
	}
	keyID, verifier, err := lkeBillingVerifierConfig(env)
	if err != nil {
		return lkeBillingPublicConfig{}, err
	}
	cfg := lkeBillingPublicConfig{Lifecycle: lkeBillingPublicLifecycle{
		Environment: environment, Stack: stack, SourceEventEnvironments: []string{environment},
		ScratchDir: "/var/lib/rtk-billing-backup/work", EncryptionKeyID: lkeEnvValue(env, "LKE_BILLING_BACKUP_ENCRYPTION_KEY_ID"),
		VerifierKeys: map[string]string{keyID: verifier}, RetentionDays: 90, PartBytes: 256 << 20,
		BackupEnabled: flags.Backup, RetirementEnabled: flags.Retirement, CompactionEnabled: flags.Compaction,
	}, ObjectStore: lkeBillingPublicObjectStore{
		Endpoint: lkeEnvValue(env, "LKE_BILLING_BACKUP_ENDPOINT"), Region: lkeEnvValue(env, "LKE_BILLING_BACKUP_REGION"), SigningRegion: lkeEnvValue(env, "LKE_BILLING_BACKUP_SIGNING_REGION"),
		Bucket: lkeEnvValue(env, "LKE_BILLING_BACKUP_BUCKET"), Environment: environment,
	}}
	if flags.Authority {
		cfg.Lifecycle.AuthorityURL = fmt.Sprintf("http://billing-raw-retention.%s.svc.cluster.local:80", lkeNamespaceName(env, "billing"))
	}
	if raw := lkeEnvValue(env, "LKE_BILLING_BACKUP_SOURCE_EVENT_ENVIRONMENTS_JSON"); raw != "" {
		if err := lkeBillingDecodePublicJSON(raw, &cfg.Lifecycle.SourceEventEnvironments); err != nil {
			return cfg, err
		}
	}
	if len(cfg.Lifecycle.SourceEventEnvironments) == 0 || len(cfg.Lifecycle.SourceEventEnvironments) > 8 {
		return cfg, errors.New("explicit source event environment registry is empty or too large")
	}
	seen := map[string]bool{}
	for _, source := range cfg.Lifecycle.SourceEventEnvironments {
		if !secretEnvironmentPattern.MatchString(source) || seen[source] {
			return cfg, errors.New("invalid or duplicate source event environment")
		}
		seen[source] = true
	}
	if !secretEnvironmentPattern.MatchString(cfg.Lifecycle.EncryptionKeyID) {
		return cfg, errors.New("billing backup requires encryption key ID")
	}
	if err := lkeBillingDecodePublicJSON(lkeEnvValue(env, "LKE_BILLING_BACKUP_RECIPIENTS_JSON"), &cfg.Lifecycle.Recipients); err != nil {
		return cfg, err
	}
	if len(cfg.Lifecycle.Recipients) == 0 || len(cfg.Lifecycle.Recipients) > 8 {
		return cfg, errors.New("billing backup requires one to eight PUBLIC age X25519 recipients")
	}
	seen = map[string]bool{}
	for _, recipient := range cfg.Lifecycle.Recipients {
		if _, err := age.ParseX25519Recipient(recipient); err != nil || seen[recipient] {
			return cfg, errors.New("invalid or duplicate PUBLIC age X25519 recipient")
		}
		seen[recipient] = true
	}
	if raw := lkeEnvValue(env, "LKE_BILLING_BACKUP_RECOVERY_KEYS_JSON"); raw != "" {
		if err := lkeBillingDecodePublicJSON(raw, &cfg.Lifecycle.RecoveryKeys); err != nil {
			return cfg, err
		}
		for id, encoded := range cfg.Lifecycle.RecoveryKeys {
			key, err := base64.StdEncoding.DecodeString(encoded)
			verifyKey, _ := base64.StdEncoding.DecodeString(verifier)
			if !secretEnvironmentPattern.MatchString(id) || id == keyID || err != nil || len(key) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(key) != encoded || bytes.Equal(key, verifyKey) {
				return cfg, errors.New("recovery approval requires independent Ed25519 PUBLIC key and key ID")
			}
		}
	}
	endpoint, err := url.Parse(cfg.ObjectStore.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || endpoint.Port() != "" || !strings.HasSuffix(endpoint.Hostname(), ".linodeobjects.com") {
		return cfg, errors.New("billing backup requires explicit HTTPS Linode Object Storage endpoint without path or credentials")
	}
	if !secretEnvironmentPattern.MatchString(cfg.ObjectStore.Region) || !secretEnvironmentPattern.MatchString(cfg.ObjectStore.SigningRegion) || cfg.ObjectStore.Bucket != "rtk-cloud-"+environment+"-billing-backup-"+cfg.ObjectStore.Region || lkeEnvValue(env, "LKE_BILLING_BACKUP_UPLOADER_BUCKET_SCOPE") != cfg.ObjectStore.Bucket {
		return cfg, errors.New("billing backup requires exact environment bucket naming, signing region and explicitly acknowledged dedicated uploader bucket scope")
	}
	capacity, err := strconv.ParseInt(lkeEnvValue(env, "LKE_BILLING_BACKUP_SCRATCH_CAPACITY_BYTES"), 10, 64)
	storageBytes, storageErr := lkeBillingStorageBytes(lkeEnvValue(env, "LKE_BILLING_BACKUP_SCRATCH_STORAGE"))
	if err != nil || capacity < 4<<30 || storageErr != nil || storageBytes < capacity {
		return cfg, errors.New("billing backup requires private scratch PVC size covering explicit budget of at least 4 GiB")
	}
	cfg.Lifecycle.ScratchCapacityBytes = capacity
	interval, err := time.ParseDuration(firstNonEmpty(lkeEnvValue(env, "LKE_BILLING_BACKUP_INTERVAL"), "12h"))
	if err != nil || interval <= 0 || interval > 12*time.Hour {
		return cfg, errors.New("billing backup interval must be positive and at most 12h")
	}
	return cfg, nil
}

var billingStoragePattern = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi|Ti)$`)

func lkeBillingStorageBytes(storage string) (int64, error) {
	parts := billingStoragePattern.FindStringSubmatch(storage)
	if len(parts) != 3 {
		return 0, errors.New("scratch storage must be an integer Mi, Gi or Ti quantity")
	}
	amount, err := strconv.ParseInt(parts[1], 10, 64)
	shift := map[string]uint{"Mi": 20, "Gi": 30, "Ti": 40}[parts[2]]
	if err != nil || amount > (1<<63-1)>>shift {
		return 0, errors.New("scratch storage capacity overflow")
	}
	return amount << shift, nil
}

func validateLKEBillingLifecycleInputs(env map[string]string) error {
	flags, err := lkeBillingLifecycleFlags(env)
	if err != nil || (!flags.Backup && !flags.Authority) {
		return err
	}
	if flags.Backup {
		if _, err := lkeBillingPublicConfiguration(env); err != nil {
			return err
		}
	} else {
		if _, _, err := lkeBillingVerifierConfig(env); err != nil {
			return err
		}
		if scope := lkeEnvValue(env, "CLOUD_ENV_NAME"); scope != "dev" && scope != "staging" && scope != "prod" {
			return errors.New("billing retention requires explicit actual environment")
		}
	}
	if flags.Authority && lkeEnvValue(env, "LKE_BILLING_RAW_RETENTION_CONSUMER_ID") != "video-cloud-mqttusage/staging" {
		return errors.New("billing retention requires explicitly pinned existing video-cloud-mqttusage/staging collector; never reset a collector cursor")
	}
	if flags.Authority && envIntDefault("LKE_BILLING_PORT", 8080) == 8081 {
		return errors.New("Billing public listener must not share private lifecycle port 8081")
	}
	credentials := map[string]string{}
	for _, entry := range billingLifecycleSecretCatalog() {
		needed := flags.Authority
		switch entry.ID {
		case "billing-backup-access-key-id", "billing-backup-secret-access-key", "cloud-logger-lifecycle-token":
			needed = flags.Backup
		case "cloud-logger-lifecycle-read-token":
			needed = flags.Backup || flags.Authority
		}
		if !needed {
			continue
		}
		value := lkeBillingLifecycleSecret(entry.ID)
		minimum := 32
		if entry.ID == "billing-backup-access-key-id" {
			minimum = 1
		}
		if len(value) < minimum || strings.ContainsAny(value, " \t\r\n") {
			return fmt.Errorf("explicit dedicated lifecycle credential %s is missing or invalid", entry.ID)
		}
		if strings.HasPrefix(entry.ID, "billing-backup-") {
			for _, generalID := range []string{"LINODE_OBJ_ACCESS_KEY_ID", "LINODE_OBJ_SECRET_ACCESS_KEY", "LINODE_TOKEN"} {
				if general := lkeObjectStorageCredential(env, generalID); general != "" && value == general {
					return fmt.Errorf("billing backup credential %s must not reuse general media/core credential %s", entry.ID, generalID)
				}
			}
		}
		if other, exists := credentials[value]; exists {
			return fmt.Errorf("billing lifecycle credentials %s and %s must be distinct", entry.ID, other)
		}
		credentials[value] = entry.ID
		for _, other := range rtkSecretCatalog() {
			if billingLifecycleSecretID(other.ID) {
				continue
			}
			if value == lkeRuntimeSecretValue(other.ID) {
				return fmt.Errorf("billing lifecycle credential %s reuses ordinary runtime credential %s", entry.ID, other.ID)
			}
		}
	}
	return nil
}

func lkeBillingLifecycleLoggerSecretData(env map[string]string) string {
	flags, _ := lkeBillingLifecycleFlags(env)
	result := ""
	if flags.Backup {
		for _, item := range []struct{ key, id string }{
			{"RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_TOKEN", "cloud-logger-lifecycle-token"},
			{"RTK_CLOUD_LOGGER_BILLING_BACKUP_ACCESS_KEY_ID", "billing-backup-access-key-id"},
			{"RTK_CLOUD_LOGGER_BILLING_BACKUP_SECRET_ACCESS_KEY", "billing-backup-secret-access-key"},
		} {
			result += fmt.Sprintf("  %s: %q\n", item.key, lkeBillingLifecycleSecret(item.id))
		}
	}
	if flags.Backup || flags.Authority {
		result += fmt.Sprintf("  RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_READ_TOKEN: %q\n", lkeBillingLifecycleSecret("cloud-logger-lifecycle-read-token"))
	}
	if flags.Backup && flags.Authority {
		result += fmt.Sprintf("  RTK_CLOUD_LOGGER_BILLING_RETENTION_AUTHORITY_TOKEN: %q\n", lkeBillingLifecycleSecret("billing-raw-retention-authority-read"))
	}
	return result
}

func lkeBillingRawRetentionSecretManifest(env map[string]string) string {
	if !lkeBillingAuthorityEnabled(env) {
		return ""
	}
	keyID, publicKey, _ := lkeBillingVerifierConfig(env)
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: billing-raw-retention-runtime
  namespace: %s
type: Opaque
stringData:
  BILLING_RAW_RETENTION_ENABLED: "true"
  BILLING_RAW_RETENTION_ENVIRONMENT: %q
  BILLING_RAW_RETENTION_LOGGER_BASE_URL: %q
  BILLING_RAW_RETENTION_CONSUMER_ID: %q
  BILLING_RAW_RETENTION_CONSUMER_BASE_URL: %q
  BILLING_RAW_RETENTION_VERIFIER_KEY_ID: %q
  BILLING_RAW_RETENTION_VERIFIER_PUBLIC_KEY: %q
  BILLING_RAW_RETENTION_LISTEN_ADDR: ":8081"
`, lkeNamespaceName(env, "billing"), lkeEnvValue(env, "CLOUD_ENV_NAME"), fmt.Sprintf("http://cloud-logger-billing-lifecycle.%s.svc.cluster.local:80", lkeNamespaceName(env, "logger")), lkeEnvValue(env, "LKE_BILLING_RAW_RETENTION_CONSUMER_ID"), fmt.Sprintf("http://video-cloud-billing-raw-lifecycle.%s.svc.cluster.local:80", lkeNamespaceName(env, "video-cloud")), keyID, publicKey)
	for _, entry := range billingLifecycleSecretCatalog() {
		for _, binding := range entry.K8SBinding {
			if binding.Secret == "billing-raw-retention-runtime" {
				manifest += fmt.Sprintf("  %s: %q\n", binding.Key, lkeBillingLifecycleSecret(entry.ID))
			}
		}
	}
	return manifest
}

func lkeVideoBillingLifecycleSecretManifest(env map[string]string) string {
	if !lkeBillingAuthorityEnabled(env) {
		return ""
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: video-cloud-billing-raw-lifecycle
  namespace: %s
type: Opaque
stringData:
  VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_TOKEN: %q
`, lkeNamespaceName(env, "video-cloud"), lkeBillingLifecycleSecret("video-billing-raw-lifecycle-token"))
}

func lkeBillingLifecycleConfigMapManifest(env map[string]string) string {
	if !lkeBillingBackupEnabled(env) {
		return ""
	}
	cfg, err := lkeBillingPublicConfiguration(env)
	if err != nil {
		return ""
	} // Deploy preflight reports the precise, secret-free error.
	payload, _ := json.Marshal(cfg)
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: cloud-logger-billing-lifecycle-public
  namespace: %s
data:
  lifecycle.json: %q
`, lkeNamespaceName(env, "logger"), string(payload))
}

func lkeBillingLifecycleScratchPVCManifest(env map[string]string) string {
	if !lkeBillingBackupEnabled(env) {
		return ""
	}
	return fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: cloud-logger-billing-backup-scratch
  namespace: %s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: %q
`, lkeNamespaceName(env, "logger"), lkeEnvValue(env, "LKE_BILLING_BACKUP_SCRATCH_STORAGE"))
}

func lkeBillingLifecycleLoggerPodParts(env map[string]string) (init, environment, mount, volumes string) {
	if !lkeBillingBackupEnabled(env) {
		return
	}
	// Projected ConfigMap entries are symlinks. Copy PUBLIC config into a
	// regular 0600 file in private scratch; the Logger rejects symlink configs.
	init = `        - name: prepare-billing-backup-scratch
          image: alpine:3.20
          command: ["/bin/sh", "-c"]
          args: ["umask 077; mkdir -p /var/lib/rtk-billing-backup/work && cp /etc/billing-lifecycle-public/lifecycle.json /var/lib/rtk-billing-backup/lifecycle.json && chown 10001:10001 /var/lib/rtk-billing-backup /var/lib/rtk-billing-backup/work /var/lib/rtk-billing-backup/lifecycle.json && chmod 0700 /var/lib/rtk-billing-backup /var/lib/rtk-billing-backup/work && chmod 0600 /var/lib/rtk-billing-backup/lifecycle.json"]
          volumeMounts:
            - name: billing-backup-scratch
              mountPath: /var/lib/rtk-billing-backup
            - name: billing-lifecycle-public
              mountPath: /etc/billing-lifecycle-public
              readOnly: true
`
	environment = fmt.Sprintf(`            - name: RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_CONFIG
              value: "/var/lib/rtk-billing-backup/lifecycle.json"
            - name: RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_LISTEN_ADDR
              value: ":8081"
            - name: RTK_CLOUD_LOGGER_BILLING_BACKUP_INTERVAL
              value: %q
`, firstNonEmpty(lkeEnvValue(env, "LKE_BILLING_BACKUP_INTERVAL"), "12h"))
	keys := []string{"RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_TOKEN", "RTK_CLOUD_LOGGER_BILLING_LIFECYCLE_READ_TOKEN", "RTK_CLOUD_LOGGER_BILLING_BACKUP_ACCESS_KEY_ID", "RTK_CLOUD_LOGGER_BILLING_BACKUP_SECRET_ACCESS_KEY"}
	if lkeBillingAuthorityEnabled(env) {
		keys = append(keys, "RTK_CLOUD_LOGGER_BILLING_RETENTION_AUTHORITY_TOKEN")
	}
	for _, key := range keys {
		environment += fmt.Sprintf("            - name: %s\n              valueFrom:\n                secretKeyRef:\n                  name: cloud-logger-runtime\n                  key: %s\n", key, key)
	}
	mount = `            - name: billing-backup-scratch
              mountPath: /var/lib/rtk-billing-backup
`
	volumes = `        - name: billing-backup-scratch
          persistentVolumeClaim:
            claimName: cloud-logger-billing-backup-scratch
        - name: billing-lifecycle-public
          configMap:
            name: cloud-logger-billing-lifecycle-public
            defaultMode: 0444
`
	return
}

func lkeVideoBillingLifecycleEnvManifest(env map[string]string) string {
	if !lkeBillingAuthorityEnabled(env) {
		return ""
	}
	return fmt.Sprintf(`            - name: VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_ENVIRONMENT
              value: %q
            - name: VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_LISTEN_ADDR
              value: ":19401"
            - name: VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-billing-raw-lifecycle
                  key: VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_TOKEN
`, lkeEnvValue(env, "CLOUD_ENV_NAME"))
}

func lkeApplyBillingLifecycleRuntime(env map[string]string, component string) error {
	if err := validateLKEBillingLifecycleInputs(env); err != nil {
		return err
	}
	manifests := []string{}
	switch component {
	case "billing":
		manifests = append(manifests, lkeBillingRawRetentionSecretManifest(env))
		if lkeBillingAuthorityEnabled(env) {
			manifests = append(manifests, lkeBillingLifecycleScopedPublicIngressManifest(env))
		}
	case "video-cloud":
		manifests = append(manifests, lkeVideoBillingLifecycleSecretManifest(env))
	case "cloud-logger":
		manifests = append(manifests, lkeBillingLifecycleConfigMapManifest(env), lkeBillingLifecycleScratchPVCManifest(env))
	}
	manifests = append(manifests, lkeBillingLifecycleServiceManifest(env, component))
	manifests = append(manifests, lkeBillingLifecycleNetworkPolicyManifests(env, component)...)
	for _, manifest := range manifests {
		if manifest != "" {
			if err := kubectlApply(manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func lkeBillingLifecycleNetworkPolicyManifests(env map[string]string, component string) []string {
	if !lkeBillingAuthorityEnabled(env) {
		return nil
	}
	// Each direction specifies both namespace and pod identity. Existing DNS
	// policies provide name resolution; no broad namespace-wide grant is added.
	var namespace, app, peerNamespace, peerApp string
	var port int
	switch component {
	case "video-cloud":
		namespace, app, peerNamespace, peerApp, port = lkeNamespaceName(env, "video-cloud"), "video-cloud-mqttusage", lkeNamespaceName(env, "billing"), "billing", 19401
	case "cloud-logger":
		namespace, app, peerNamespace, peerApp, port = lkeNamespaceName(env, "logger"), "cloud-logger", lkeNamespaceName(env, "billing"), "billing", 8081
	case "billing":
		namespace, app, peerNamespace, peerApp, port = lkeNamespaceName(env, "billing"), "billing", lkeNamespaceName(env, "logger"), "cloud-logger", 8081
	default:
		return nil
	}
	return []string{fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-billing-raw-lifecycle
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: %s
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: %s
      ports:
        - protocol: TCP
          port: %d
`, namespace, app, peerNamespace, peerApp, port)}
}

func lkeBillingLifecycleServiceManifest(env map[string]string, component string) string {
	var name, app, namespace string
	port := 8081
	switch component {
	case "cloud-logger":
		if !lkeBillingBackupEnabled(env) {
			return ""
		}
		name, app, namespace = "cloud-logger-billing-lifecycle", "cloud-logger", lkeNamespaceName(env, "logger")
	case "billing":
		if !lkeBillingAuthorityEnabled(env) {
			return ""
		}
		name, app, namespace = "billing-raw-retention", "billing", lkeNamespaceName(env, "billing")
	case "video-cloud":
		if !lkeBillingAuthorityEnabled(env) {
			return ""
		}
		name, app, namespace, port = "video-cloud-billing-raw-lifecycle", "video-cloud-mqttusage", lkeNamespaceName(env, "video-cloud"), 19401
	default:
		return ""
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: lifecycle
      port: 80
      targetPort: %d
`, name, namespace, app, port)
}

func lkeBillingLifecycleScopedPublicIngressManifest(env map[string]string) string {
	// Payment simulator's public port is also 8081, on a different pod. Replace
	// the EXISTING generic policy name so no stale namespace-wide 8081 grant
	// remains; isolate the simulator in a second exact-pod policy.
	policy := func(name, app string, port int) string {
		return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: %s
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: %d
`, name, lkeNamespaceName(env, "billing"), app, lkeIngressNamespace(env), port)
	}
	return policy("allow-public-ingress", "billing", envIntDefault("LKE_BILLING_PORT", 8080)) + "---\n" + policy("allow-public-ingress-payment-simulator", "payment-simulator", 8081)
}
