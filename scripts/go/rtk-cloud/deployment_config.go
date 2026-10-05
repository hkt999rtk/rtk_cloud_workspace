package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type deploymentConfig struct {
	Workspace       string
	Environment     string
	EnvironmentRoot string
	RuntimeRoot     string
	Architecture    string
	Adapter         string
	DNSAdapter      string
	Values          map[string]string
	AdapterValues   map[string]string
	AdapterResolved map[string]string
	DNSValues       map[string]string
	Capacity        sharedCapacityPlan
	Storage         deploymentStoragePlan
}

type deploymentStorageTarget struct {
	Purpose         string `json:"purpose"`
	Policy          string `json:"policy"`
	LogicalLocation string `json:"logical_location"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	Region          string `json:"region"`
	Endpoint        string `json:"endpoint,omitempty"`
}

type deploymentStoragePlan struct {
	RuntimeMedia                deploymentStorageTarget `json:"runtime_media"`
	RuntimeMediaCutoverRequired bool                    `json:"runtime_media_cutover_required,omitempty"`
	OTAFirmware                 deploymentStorageTarget `json:"ota_firmware,omitempty"`
	OTAMode                     string                  `json:"ota_mode"`
	ReleaseArtifacts            deploymentStorageTarget `json:"release_artifacts"`
}

type deploymentOperations struct {
	preflight                     func(deploymentConfig, string) error
	validateTarget                func(deploymentConfig, string) error
	credentials                   func(deploymentConfig, string) error
	bootstrapCredentials          func(deploymentConfig, string) error
	grantObjectStorageCredentials func(deploymentConfig, string) error
	plan                          func(deploymentConfig) error
	prepareTest                   func(deploymentConfig) error
	provision                     func(deploymentConfig) error
	acceptance                    func(deploymentConfig) error
	cleanup                       func(deploymentConfig) error
	normalize                     func(deploymentConfig) error
}

var deploymentIntegerKeys = map[string]bool{
	"CAPACITY_TARGET_CONNECTIONS": true, "CAPACITY_CONNECTIONS_PER_MQTT_POD": true,
	"CAPACITY_ACTIVE_DEVICES": true, "CAPACITY_ACTIVE_DEVICES_PER_API_POD": true,
	"CAPACITY_SYSTEM_RESERVED_CPU_MILLI": true, "CAPACITY_SYSTEM_RESERVED_MEMORY_MIB": true,
	"NODE_CLASS_GENERAL_MIN_COUNT": true, "NODE_CLASS_BROKER_MIN_COUNT": true,
	"NODE_CLASS_DATABASE_MIN_COUNT": true,
	"NODE_CLASS_GENERAL_MIN_VCPU":   true, "NODE_CLASS_GENERAL_MIN_MEMORY_GIB": true,
	"NODE_CLASS_BROKER_MIN_VCPU": true, "NODE_CLASS_BROKER_MIN_MEMORY_GIB": true,
	"NODE_CLASS_DATABASE_MIN_VCPU": true, "NODE_CLASS_DATABASE_MIN_MEMORY_GIB": true,
	"EDGE_REPLICAS":        true,
	"EDGE_MAX_CONNECTIONS": true, "TURN_REPLICAS": true,
	"TURN_MIN_PORT": true, "TURN_MAX_PORT": true,
}

var deploymentArchitectureKeys = architectureKeySet()

var deploymentEnvironmentKeys = keySet(
	"CLOUD_STACK_NAME", "CLOUD_DNS_ROOT_DOMAIN", "DEPLOYMENT_LOCATION",
	"DEPLOYMENT_REQUIRE_PKITURN",
	"PRIVACY_POLICY_URL", "GOOGLE_ANALYTICS_MEASUREMENT_ID",
	"FRONTEND_DOMAIN", "CONSOLE_DOMAIN", "PUBLIC_BASE_URL", "DISABLE_SEARCH_INDEXING",
	"TEST_LAB_ENABLED",
	"SUPPORT_TICKETS_ENABLED", "ZAMMAD_SUPPORT_GROUP_ID", "ZAMMAD_UNASSIGNED_OWNER_ID",
	"FACTORY_ENROLL_PUBLIC_ENABLED", "FACTORY_ENROLL_DOMAIN",
	"CHIPSET_PROVIDER_ALLOWED_HOSTS",
	"AUTH_TOKEN_BASE_URL", "SOCIAL_LOGIN_CALLBACK_URL", "GOOGLE_LOGIN_ENABLED", "GOOGLE_OAUTH_CLIENT_ID", "GITHUB_LOGIN_ENABLED", "GITHUB_OAUTH_CLIENT_ID", "SENDMAIL_HTTP_BASE_URL", "SENDMAIL_HTTP_TIMEOUT",
	"EMAIL_OUTBOX_POLL_INTERVAL", "EMAIL_OUTBOX_BATCH_SIZE", "EMAIL_OUTBOX_MAX_ATTEMPTS",
	"EMAIL_OUTBOX_RETRY_BASE", "EMAIL_OUTBOX_RETRY_MAX",
	"ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES", "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED",
	"VIDEO_CLOUD_OTA_CDN_BASE_URL", "VIDEO_CLOUD_OTA_CDN_TOKEN_NAME",
)

var deploymentEnvironmentServiceKeys = keySet(
	"TEST_LAB_ENABLED",
	"SUPPORT_TICKETS_ENABLED", "ZAMMAD_SUPPORT_GROUP_ID", "ZAMMAD_UNASSIGNED_OWNER_ID",
	"FACTORY_ENROLL_PUBLIC_ENABLED", "FACTORY_ENROLL_DOMAIN",
	"CHIPSET_PROVIDER_ALLOWED_HOSTS",
	"AUTH_TOKEN_BASE_URL", "SOCIAL_LOGIN_CALLBACK_URL", "GOOGLE_LOGIN_ENABLED", "GOOGLE_OAUTH_CLIENT_ID", "GITHUB_LOGIN_ENABLED", "GITHUB_OAUTH_CLIENT_ID", "SENDMAIL_HTTP_BASE_URL", "SENDMAIL_HTTP_TIMEOUT",
	"EMAIL_OUTBOX_POLL_INTERVAL", "EMAIL_OUTBOX_BATCH_SIZE", "EMAIL_OUTBOX_MAX_ATTEMPTS",
	"EMAIL_OUTBOX_RETRY_BASE", "EMAIL_OUTBOX_RETRY_MAX",
	"ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES", "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED",
)

func architectureKeySet() map[string]bool {
	out := keySet(
		"DEPLOYMENT_RUNTIME", "NODE_CLASS_LABEL_KEY", "DEFAULT_WORKLOAD_NODE_CLASS", "POD_SPREAD_TOPOLOGY_KEY",
		"CAPACITY_TARGET_CONNECTIONS", "CAPACITY_CONNECTIONS_PER_MQTT_POD",
		"CAPACITY_ACTIVE_DEVICES", "CAPACITY_ACTIVE_DEVICES_PER_API_POD", "CAPACITY_SYSTEM_RESERVED_CPU_MILLI", "CAPACITY_SYSTEM_RESERVED_MEMORY_MIB",
		"NODE_CLASS_GENERAL_MIN_COUNT", "NODE_CLASS_BROKER_MIN_COUNT", "NODE_CLASS_DATABASE_MIN_COUNT",
		"NODE_CLASS_GENERAL_MIN_VCPU", "NODE_CLASS_GENERAL_MIN_MEMORY_GIB",
		"NODE_CLASS_BROKER_MIN_VCPU", "NODE_CLASS_BROKER_MIN_MEMORY_GIB",
		"NODE_CLASS_DATABASE_MIN_VCPU", "NODE_CLASS_DATABASE_MIN_MEMORY_GIB",
		"MQTT_HARD_ANTI_AFFINITY", "POSTGRES_LIMIT_MEMORY", "CLOUD_LOGGER_LIMIT_MEMORY",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "VIDEO_CLOUD_CLIP_VERIFIER_NODE_CLASS", "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON",
		"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM", "CERTIFICATE_APP_CSR_KEY_ALGORITHMS", "CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS",
		"EDGE_REPLICAS", "EDGE_MAX_CONNECTIONS", "TURN_REPLICAS", "TURN_MIN_PORT", "TURN_MAX_PORT",
	)
	for _, key := range capacitySourceKeys() {
		out[key] = true
	}
	return out
}

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

func runDeployment(args []string) error {
	return runDeploymentWithOperations(args, defaultDeploymentOperations())
}

func defaultDeploymentOperations() deploymentOperations {
	return deploymentOperations{
		preflight:                     runDeploymentPreflight,
		validateTarget:                validateDeploymentActionTarget,
		credentials:                   validateDeploymentCredentials,
		bootstrapCredentials:          validateAndBootstrapDeploymentCredentials,
		grantObjectStorageCredentials: validateAndGrantDeploymentObjectStorageAccess,
		plan: func(cfg deploymentConfig) error {
			return runProvision([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--plan"})
		},
		prepareTest: validateEphemeralDeploymentEnvironmentAbsent,
		provision:   provisionDeploymentEnvironment,
		acceptance: func(cfg deploymentConfig) error {
			_, restore, err := configureProvisionSecretStore(cfg.Environment)
			if err != nil {
				return err
			}
			defer restore()
			return runEnvironmentAcceptance([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--confirm", cfg.Values["CLOUD_STACK_NAME"], "--no-resume"})
		},
		cleanup:   cleanupDeploymentEnvironment,
		normalize: normalizeDeploymentRuntime,
	}
}

func runDeploymentWithOperations(args []string, ops deploymentOperations) error {
	if len(args) > 0 && args[0] == "check" {
		return runDeploymentCheck(args[1:])
	}
	if len(args) > 0 && args[0] == "console-check" {
		// Read-only feature checks must not materialize or normalize a live runtime.
		return runDeploymentConsoleCheck(args[1:])
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printDeploymentUsage()
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("deployment "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "environment name under cloud_env")
	environmentRoot := fs.String("environment-root", "", "explicit environment root for tests/custom environments")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "stack confirmation for mutation")
	envFile := fs.String("env-file", "", "retired; use the environment SecretStore operator/env directory")
	sharedEnvFile := fs.String("shared-env-file", "", "retired; shared secret fallback is not supported")
	createMissingObjectStorageBucket := fs.Bool("create-missing-object-storage-bucket", false, "create a missing configured Object Storage bucket before revalidation")
	grantObjectStorageBucketAccess := fs.Bool("grant-object-storage-bucket-access", false, "create and activate a replacement limited key for the configured Object Storage bucket")
	destinationEnvFile := fs.String("destination-env-file", "", "private candidate credential profile for storage preparation, migration and cutover")
	sourceEnvFile := fs.String("source-env-file", "", "source Object Storage credential profile for migration and OTA cutover")
	keyID := fs.Int("key-id", 0, "recorded old Object Storage key ID to retire")
	storagePurpose := fs.String("purpose", "media", "storage purpose: media, ota or artifacts")
	reinitializePlan := fs.Bool("plan", false, "read-only storage-reinitialize plan")
	discardedSource := fs.String("acknowledge-discarded-source", "", "exact deleted source bucket whose data was explicitly discarded")
	metricsStart := fs.String("window-start", "", "UTC start of the OTA Cloud Pulse qualification window")
	metricsEnd := fs.String("window-end", "", "UTC end of the OTA Cloud Pulse qualification window")
	metricsRecorder := fs.String("recorded-by", "", "operator identity for the OTA metrics qualification")
	metricsProbeFile := fs.String("probe-evidence-file", "", "private JSON evidence of the verified OTA GetObject bodies")
	acceptSmallProbeShortfall := fs.Bool("accept-small-probe-shortfall", false, "accept an OTA probe byte shortfall of at most 1024 bytes and 0.01% of verified bytes")
	operation := fs.String("operation", "", "preflight operation: plan, provision, acceptance, or ephemeral-test")
	var qualification deploymentCredentialCheckOptions
	var selectedChecks string
	fs.BoolVar(&qualification.readOnly, "read-only", false, "credential qualification without DNS/storage writes or receipts")
	fs.StringVar(&selectedChecks, "checks", "", "credential checks: linode,ghcr,dns,storage,tls,mounts (default: configured providers plus supplied local checks)")
	fs.Func("image", "repeatable exact GHCR @sha256 image to pull for linux/amd64", func(v string) error { qualification.images = append(qualification.images, v); return nil })
	fs.Func("manifest", "repeatable rendered workload JSON for Secret mount checks", func(v string) error { qualification.manifests = append(qualification.manifests, v); return nil })
	fs.StringVar(&qualification.tls.cert, "tls-cert", "", "PEM leaf certificate and intermediate chain")
	fs.StringVar(&qualification.tls.key, "tls-key", "", "private key file (0600)")
	fs.StringVar(&qualification.tls.ca, "tls-ca", "", "trusted CA PEM bundle")
	fs.StringVar(&qualification.tls.hostname, "tls-name", "", "actual server DNS name")
	fs.StringVar(&qualification.tls.purpose, "tls-purpose", "server", "server or client")
	fs.IntVar(&qualification.tls.minDays, "min-valid-days", 7, "required certificate-chain lifetime in days")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if (action == "credentials-check" || action == "storage-metrics-export") && fs.NArg() != 0 {
		return errors.New("unexpected positional arguments; use --flag=value for boolean values")
	}
	if action != "storage-reinitialize" && (hasFlag(args[1:], "--acknowledge-discarded-source") || hasFlag(args[1:], "--plan")) {
		return errors.New("--acknowledge-discarded-source and --plan require storage-reinitialize")
	}
	if action != "storage-metrics-export" && (hasFlag(args[1:], "--window-start") || hasFlag(args[1:], "--window-end") || hasFlag(args[1:], "--recorded-by") || hasFlag(args[1:], "--probe-evidence-file") || hasFlag(args[1:], "--accept-small-probe-shortfall")) {
		return errors.New("OTA metrics window, operator and probe flags require deployment storage-metrics-export")
	}
	if action == "storage-metrics-export" && strings.TrimSpace(*metricsProbeFile) == "" {
		return errors.New("--probe-evidence-file is required for OTA metrics qualification")
	}
	qualificationFlags := keySet("read-only", "checks", "image", "manifest", "tls-cert", "tls-key", "tls-ca", "tls-name", "tls-purpose", "min-valid-days")
	customQualification := false
	fs.Visit(func(f *flag.Flag) {
		if qualificationFlags[f.Name] {
			customQualification = true
		}
	})
	if customQualification {
		preflightReadOnlyOnly := action == "preflight" && qualification.readOnly && selectedChecks == "" && len(qualification.images) == 0 && len(qualification.manifests) == 0 && qualification.tls.cert == "" && qualification.tls.key == "" && qualification.tls.ca == "" && qualification.tls.hostname == "" && !hasFlag(args[1:], "--tls-purpose") && !hasFlag(args[1:], "--min-valid-days")
		if action != "credentials-check" && !preflightReadOnlyOnly {
			return errors.New("qualification flags are only valid with deployment credentials-check")
		}
		if *createMissingObjectStorageBucket || *grantObjectStorageBucketAccess {
			return errors.New("run credential repairs separately from scoped/read-only qualification")
		}
		if qualification.tls.cert == "" && (hasFlag(args[1:], "--tls-purpose") || hasFlag(args[1:], "--min-valid-days")) {
			return errors.New("TLS purpose/lifetime flags require --tls-cert, --tls-key and --tls-ca")
		}
		if err := qualification.configureChecks(selectedChecks); err != nil {
			return err
		}
	}
	if !rtkCloudTestMode() {
		if hasFlag(args[1:], "--env-file") || hasFlag(args[1:], "--shared-env-file") {
			return errors.New("--env-file and --shared-env-file are retired; use ~/.config/rtk_cloud/<environment>/operator/env")
		}
	}
	storageAction := strings.HasPrefix(action, "storage-")
	if action != "preflight" && action != "credentials-check" && action != "plan" && action != "create" && action != "upgrade" && action != "provision" && action != "acceptance" && action != "remove" && action != "test" && !keySet("storage-plan", "storage-bootstrap", "storage-migrate", "storage-cutover", "storage-reinitialize", "storage-rollback", "storage-retire", "storage-metrics-export")[action] {
		return fmt.Errorf("unknown deployment action %q", action)
	}
	if *createMissingObjectStorageBucket && action != "credentials-check" {
		return errors.New("--create-missing-object-storage-bucket is only valid with deployment credentials-check")
	}
	if *grantObjectStorageBucketAccess && action != "credentials-check" {
		return errors.New("--grant-object-storage-bucket-access is only valid with deployment credentials-check")
	}
	if *createMissingObjectStorageBucket && *grantObjectStorageBucketAccess {
		return errors.New("Object Storage bucket creation and access grant must be run as separate credential-check operations")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil {
		return err
	}
	if (*createMissingObjectStorageBucket || *grantObjectStorageBucketAccess) && cfg.Storage.RuntimeMediaCutoverRequired {
		if err := validateDeploymentStorageActivation(cfg); err != nil {
			return fmt.Errorf("use storage-bootstrap with an isolated candidate profile before cutover: %w", err)
		}
	}
	if *envFile == "" {
		*envFile = defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
	}
	_ = os.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", *envFile)
	defer os.Unsetenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE")
	if *sharedEnvFile != "" {
		_ = os.Setenv("RTK_CLOUD_SHARED_CREDENTIAL_ENV_FILE", *sharedEnvFile)
		defer os.Unsetenv("RTK_CLOUD_SHARED_CREDENTIAL_ENV_FILE")
	}
	if action == "preflight" {
		return runDeploymentPreflight(cfg, *operation)
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if action != "plan" && action != "credentials-check" && action != "storage-plan" && !(action == "storage-reinitialize" && *reinitializePlan) && *confirm != stack {
		return fmt.Errorf("--confirm %s is required", stack)
	}
	if *destinationEnvFile != "" && !storageAction {
		return errors.New("--destination-env-file requires a storage action")
	}
	if storageAction {
		if keySet("storage-bootstrap", "storage-migrate", "storage-cutover", "storage-reinitialize")[action] && *destinationEnvFile == "" {
			return errors.New("--destination-env-file is required to keep replacement storage credentials isolated until cutover")
		}
		if *destinationEnvFile != "" {
			if err := stageDeploymentStorageProfile(cfg.Environment, *envFile, *destinationEnvFile, action == "storage-bootstrap"); err != nil {
				return err
			}
			*envFile = *destinationEnvFile
		}
		if action == "storage-reinitialize" {
			if fs.NArg() != 0 {
				return errors.New("storage-reinitialize does not accept positional arguments")
			}
			return runStorageReinitialize(cfg, *storagePurpose, *sourceEnvFile, *envFile, *discardedSource, *reinitializePlan)
		}
		if action == "storage-metrics-export" {
			if *storagePurpose != "ota" {
				return errors.New("storage-metrics-export requires --purpose ota")
			}
			return runDeploymentOTAMetricsExport(cfg, *envFile, *metricsStart, *metricsEnd, *metricsRecorder, *metricsProbeFile, *acceptSmallProbeShortfall)
		}
		return runDeploymentStorageLifecyclePurpose(action, cfg, *envFile, *sourceEnvFile, *keyID, *storagePurpose)
	}
	if keySet("create", "upgrade", "provision", "test")[action] {
		if err := validateDeploymentStorageActivation(cfg); err != nil {
			return err
		}
	}
	if cfg.Adapter != "lke" && action != "plan" && action != "credentials-check" {
		return fmt.Errorf("deployment adapter %s is not implemented", cfg.Adapter)
	}
	if action == "create" || action == "upgrade" || action == "provision" || action == "test" {
		operation := "provision"
		if action == "test" {
			operation = "ephemeral-test"
		}
		if ops.preflight != nil {
			if err := ops.preflight(cfg, operation); err != nil {
				return err
			}
		}
		if (action == "create" || action == "upgrade") && ops.validateTarget != nil {
			if err := ops.validateTarget(cfg, action); err != nil {
				return err
			}
		}
	}
	if action == "credentials-check" || action == "create" || action == "upgrade" || action == "provision" || action == "test" {
		credentialCheck := ops.credentials
		if customQualification {
			credentialCheck = func(cfg deploymentConfig, envFile string) error {
				return defaultDeploymentCredentialChecker().checkWithOptions(cfg, envFile, qualification)
			}
		}
		credentialsValidated := false
		if *createMissingObjectStorageBucket {
			credentialCheck = ops.bootstrapCredentials
		}
		if *grantObjectStorageBucketAccess {
			credentialCheck = ops.grantObjectStorageCredentials
		}
		if credentialCheck == nil {
			if action == "credentials-check" {
				return errors.New("deployment credential checker is not configured")
			}
		} else if err := credentialCheck(cfg, *envFile); err != nil {
			return err
		} else {
			credentialsValidated = true
		}
		if action == "credentials-check" {
			return nil
		}
		if credentialsValidated {
			values, profileCheck := deploymentCredentialProfileValues(cfg.Environment, *envFile, defaultDeploymentSharedCredentialFile())
			if !profileCheck.Passed {
				return errors.New(profileCheck.Detail)
			}
			receipt, receiptErr := readDeploymentStorageReceipt(cfg.RuntimeRoot)
			if receiptErr != nil {
				return errors.New("validated storage receipt is required before provisioning")
			}
			values["LINODE_OBJ_BUCKET"] = cfg.Storage.RuntimeMedia.Bucket
			values["LINODE_OBJ_REGION"] = cfg.Storage.RuntimeMedia.Region
			values["LINODE_OBJ_ENDPOINT"] = receipt.Endpoint
			restore := installDeploymentChildCredentialEnvironment(values)
			defer restore()
		}
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	switch action {
	case "plan":
		fmt.Printf("environment: %s\narchitecture: %s\nadapter: %s\ndns_adapter: %s\nruntime_root: %s\n", cfg.Environment, cfg.Architecture, cfg.Adapter, cfg.DNSAdapter, cfg.RuntimeRoot)
		if cfg.Adapter != "lke" {
			fmt.Printf("infrastructure: adapter not implemented; mutation will fail fast\n")
			return normalizeDeploymentRuntime(cfg)
		}
		if err := ops.plan(cfg); err != nil {
			return err
		}
		return ops.normalize(cfg)
	case "create", "upgrade", "provision":
		err = ops.provision(cfg)
	case "acceptance":
		err = ops.acceptance(cfg)
	case "remove":
		err = ops.cleanup(cfg)
	case "test":
		if err = ops.prepareTest(cfg); err != nil {
			return fmt.Errorf("ephemeral test preflight failed: %w", err)
		}
		provisionErr := ops.provision(cfg)
		var acceptanceErr error
		if provisionErr == nil {
			acceptanceErr = ops.acceptance(cfg)
		}
		cleanupErr := ops.cleanup(cfg)
		err = errors.Join(
			wrapDeploymentPhaseError("provision", provisionErr),
			wrapDeploymentPhaseError("acceptance", acceptanceErr),
			wrapDeploymentPhaseError("cleanup", cleanupErr),
		)
	}
	if err != nil {
		return err
	}
	return ops.normalize(cfg)
}

func wrapDeploymentPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s phase failed: %w", phase, err)
}

func provisionDeploymentEnvironment(cfg deploymentConfig) error {
	if err := validateDNSBeforeMutation(cfg); err != nil {
		return err
	}
	if cfg.Adapter == "lke" {
		if err := validateLKEEnvironmentStateBeforeMutation(cfg); err != nil {
			return err
		}
		if err := resolveLKEImagesIfNeeded(cfg.Workspace, cfg.RuntimeRoot); err != nil {
			return err
		}
	}
	return runProvision([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--preflight", "--plan", "--apply", "--deploy", "--dns", "--artifacts", "--confirm", cfg.Values["CLOUD_STACK_NAME"]})
}

func validateDeploymentActionTarget(cfg deploymentConfig, action string) error {
	switch action {
	case "create":
		if err := validateEphemeralDeploymentEnvironmentAbsent(cfg); err != nil {
			return fmt.Errorf("create requires an absent environment: %w", err)
		}
		return nil
	case "upgrade":
		return validateExistingDeploymentEnvironment(cfg)
	default:
		return nil
	}
}

func validateExistingDeploymentEnvironment(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" {
		return fmt.Errorf("upgrade target validation is not implemented for adapter %s", cfg.Adapter)
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("upgrade requires LKE credentials")
	}
	compatInput := appendMap(cfg.Values, cfg.AdapterResolved)
	compat := appendMap(compatInput, deploymentLegacyLKEValues(compatInput, cfg.Environment))
	if _, err := discoverLKECluster(token, provisionPaths{EnvRoot: cfg.RuntimeRoot}, compat, false); err != nil {
		if errors.Is(err, errLKEMissingCluster) {
			return fmt.Errorf("upgrade requires existing stack %s; use create for a new environment", cfg.Values["CLOUD_STACK_NAME"])
		}
		return err
	}
	kubeconfig, err := ensureK8SKubeconfig(cfg.Workspace, cfg.RuntimeRoot, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return fmt.Errorf("resolve upgrade kubeconfig: %w", err)
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "get", "pvc", "data-postgresql-0", "-o", "jsonpath={.status.phase}{'\\t'}{.spec.volumeName}")
	body, err := cmd.Output()
	if err != nil {
		return errors.New("upgrade requires the existing PostgreSQL PVC data-postgresql-0")
	}
	parts := strings.Split(strings.TrimSpace(string(body)), "\t")
	if len(parts) != 2 || parts[0] != "Bound" || strings.TrimSpace(parts[1]) == "" {
		phase := ""
		if len(parts) > 0 {
			phase = parts[0]
		}
		return fmt.Errorf("upgrade requires a bound PostgreSQL PVC; current phase is %q", phase)
	}
	return nil
}

func cleanupDeploymentEnvironment(cfg deploymentConfig) error {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	volumeIDs, volumeCaptureErr := deploymentPersistentVolumeIDs(cfg)
	k8sErr := purgeDeploymentK8s(cfg)
	dnsErr := removeOwnedDNSRecords(
		newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{}),
		appendMap(cfg.Values, cfg.DNSValues),
	)
	resourceErr := runDestroyEnvironmentResources([]string{
		"--workspace", cfg.Workspace,
		"--env-root", cfg.RuntimeRoot,
		"--yes",
		"--confirm-text", "destroy " + stack,
		"--include-object-storage",
	})
	if resourceErr == nil {
		resourceErr = deleteDeploymentVolumesWhenDetached(resolveLinodeToken(cfg.RuntimeRoot), volumeIDs, envDurationDefault("RTK_CLOUD_ENVIRONMENT_CLEANUP_TIMEOUT", 10*time.Minute))
	}
	if resourceErr == nil {
		resourceErr = waitForDeploymentEnvironmentRemoval(cfg)
	}
	return errors.Join(
		wrapDeploymentPhaseError("persistent volume ownership capture", volumeCaptureErr),
		wrapDeploymentPhaseError("Kubernetes storage cleanup", k8sErr),
		wrapDeploymentPhaseError("DNS cleanup", dnsErr),
		wrapDeploymentPhaseError("provider cleanup", resourceErr),
	)
}

func deploymentPersistentVolumeIDs(cfg deploymentConfig) ([]string, error) {
	kubeconfig := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "persistentvolume", "-o", "json")
	body, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("list persistent volumes: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("list persistent volumes: %w", err)
	}
	return persistentVolumeIDsForStack(body, cfg.Values["CLOUD_STACK_NAME"])
}

func persistentVolumeIDsForStack(body []byte, stack string) ([]string, error) {
	var listed struct {
		Items []struct {
			Spec struct {
				ClaimRef struct {
					Namespace string `json:"namespace"`
				} `json:"claimRef"`
				CSI struct {
					VolumeHandle string `json:"volumeHandle"`
				} `json:"csi"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("decode persistent volumes: %w", err)
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, item := range listed.Items {
		if !strings.HasPrefix(item.Spec.ClaimRef.Namespace, stack+"-") {
			continue
		}
		id := strings.TrimSpace(item.Spec.CSI.VolumeHandle)
		if id == "" {
			continue
		}
		volumeID := id
		if numericID, suffix, ok := strings.Cut(id, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
			volumeID = numericID
		}
		if _, err := strconv.Atoi(volumeID); err != nil {
			return nil, fmt.Errorf("stack %s persistent volume has unsupported Linode volume handle %q", stack, id)
		}
		if !seen[volumeID] {
			seen[volumeID] = true
			ids = append(ids, volumeID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func purgeDeploymentK8s(cfg deploymentConfig) error {
	kubeconfig := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	old, hadOld := os.LookupEnv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET")
	if err := os.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", "1"); err != nil {
		return err
	}
	defer func() {
		if hadOld {
			_ = os.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", old)
		} else {
			_ = os.Unsetenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET")
		}
	}()
	return runRemoveK8s([]string{
		"--workspace", cfg.Workspace,
		"--env-root", cfg.RuntimeRoot,
		"--yes",
		"--purge-storage",
	})
}

func validateEphemeralDeploymentEnvironmentAbsent(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" {
		return fmt.Errorf("ephemeral test preflight is not implemented for adapter %s", cfg.Adapter)
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required before ephemeral test preflight")
	}
	plan, err := buildLinodeDestroyPlan(token, cfg.Values, cfg.Values["CLOUD_STACK_NAME"], "")
	if err != nil {
		return err
	}
	if labels := deploymentEnvironmentResourceLabels(plan); len(labels) > 0 {
		return fmt.Errorf("stack %s already owns provider resources: %s; use acceptance for an existing environment or remove it explicitly", cfg.Values["CLOUD_STACK_NAME"], strings.Join(labels, ", "))
	}
	ownership := filepath.Join(cfg.RuntimeRoot, "dns", cfg.DNSAdapter, "ownership.json")
	if body, readErr := os.ReadFile(ownership); readErr == nil && len(strings.TrimSpace(string(body))) > 0 {
		return fmt.Errorf("stack %s has existing DNS ownership state at %s", cfg.Values["CLOUD_STACK_NAME"], ownership)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	return nil
}

func waitForDeploymentEnvironmentRemoval(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" {
		return nil
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required to verify provider cleanup")
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	timeout := envDurationDefault("RTK_CLOUD_ENVIRONMENT_CLEANUP_TIMEOUT", 10*time.Minute)
	deadline := time.Now().Add(timeout)
	for {
		plan, err := buildLinodeDestroyPlan(token, cfg.Values, stack, "")
		if err != nil {
			return err
		}
		labels := deploymentEnvironmentResourceLabels(plan)
		if len(labels) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for stack %s cleanup; resources still present: %s", timeout, stack, strings.Join(labels, ", "))
		}
		time.Sleep(5 * time.Second)
	}
}

func deploymentEnvironmentResourceLabels(plan linodeDestroyPlan) []string {
	labels := []string{}
	for _, group := range [][]linodeDestroyResource{plan.LKEClusters, plan.Instances, plan.Firewalls, plan.VPCs, plan.ObjectBuckets} {
		for _, resource := range group {
			labels = append(labels, resource.Label)
		}
	}
	sort.Strings(labels)
	return labels
}

func validateDNSBeforeMutation(cfg deploymentConfig) error {
	env := appendMap(cfg.Values, cfg.DNSValues)
	paths := newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{})
	_, _, _, err := selectedDNSAdapter(paths, env)
	return err
}

func validateLKEEnvironmentStateBeforeMutation(cfg deploymentConfig) error {
	return validateLKEEnvironmentStateBeforeMutationWithDiscovery(cfg, discoverLKECluster)
}

func validateLKEEnvironmentStateBeforeMutationWithDiscovery(cfg deploymentConfig, discover func(string, provisionPaths, map[string]string, bool) (lkeCluster, error)) error {
	account, err := readLKEAccountState(cfg.RuntimeRoot, true)
	if err != nil {
		return err
	}
	if err := validateActiveServiceLimit(account["LKE_ACTIVE_SERVICE_LIMIT"]); err != nil {
		return err
	}
	compatInput := appendMap(cfg.Values, cfg.AdapterResolved)
	compat := appendMap(compatInput, deploymentLegacyLKEValues(compatInput, cfg.Environment))
	compat["LKE_LINODE_ACTIVE_SERVICE_LIMIT"] = account["LKE_ACTIVE_SERVICE_LIMIT"]
	for _, key := range []string{"LKE_REGION", "LKE_GENERAL_NODE_TYPE", "LKE_BROKER_NODE_TYPE", "LKE_DATABASE_NODE_TYPE"} {
		if strings.TrimSpace(cfg.AdapterResolved[key]) == "" {
			return fmt.Errorf("LKE adapter setting %s is required before mutation", key)
		}
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required before mutation: configure LINODE_TOKEN in the environment runtime secret")
	}
	_, err = discover(token, provisionPaths{EnvRoot: cfg.RuntimeRoot}, compat, false)
	if errors.Is(err, errLKEMissingCluster) {
		return nil
	}
	if err != nil {
		return err
	}
	store, storeErr := newSecretStore("", cfg.Environment)
	if storeErr != nil {
		return storeErr
	}
	required := []string{
		filepath.Join(store.Root, "openbao", "unseal-key"),
		filepath.Join(store.Root, "openbao", "root-token"),
		filepath.Join(store.Root, "runtime", "postgres"),
	}
	missing := make([]string, 0, len(required))
	for _, path := range required {
		if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("existing LKE cluster requires matching environment runtime state before mutation; missing %s; either restore the environment state or intentionally rebuild the cluster and persistent storage", strings.Join(missing, ", "))
	}
	return nil
}

func printDeploymentUsage() {
	fmt.Fprint(os.Stdout, `Usage:
  rtk-cloud deployment check --environment NAME [--phase pre-deploy|post-deploy] [--fast] [--timeout 2m] [--report PATH]
  rtk-cloud deployment credentials-check --environment NAME
  rtk-cloud deployment credentials-check --environment NAME --read-only [--checks ghcr,tls,mounts] [--image GHCR_DIGEST] [--manifest WORKLOAD_JSON]
  rtk-cloud deployment credentials-check --environment NAME --create-missing-object-storage-bucket
  rtk-cloud deployment credentials-check --environment NAME --grant-object-storage-bucket-access
  rtk-cloud deployment preflight --environment NAME --operation plan|provision|acceptance|ephemeral-test
  rtk-cloud deployment plan --environment NAME
  rtk-cloud deployment console-check --environment NAME --cloud-id UUID [--product-id UUID]
  rtk-cloud deployment create --environment NAME --confirm STACK
  rtk-cloud deployment upgrade --environment NAME --confirm STACK
  rtk-cloud deployment provision --environment NAME --confirm STACK
  rtk-cloud deployment acceptance --environment NAME --confirm STACK
  rtk-cloud deployment remove --environment NAME --confirm STACK
  rtk-cloud deployment test --environment NAME --confirm STACK
  rtk-cloud deployment storage-plan --environment NAME
  rtk-cloud deployment storage-bootstrap --environment NAME --purpose media|ota|artifacts --destination-env-file PATH --confirm STACK
  rtk-cloud deployment storage-reinitialize --environment NAME --purpose media|ota --source-env-file PATH --destination-env-file PATH --acknowledge-discarded-source BUCKET [--plan | --confirm STACK]
  rtk-cloud deployment storage-migrate --environment NAME --purpose media|ota|artifacts --destination-env-file PATH --source-env-file PATH --confirm STACK
  rtk-cloud deployment storage-metrics-export --environment NAME --purpose ota --window-start YYYY-MM-DDTHH:MM:SSZ --window-end YYYY-MM-DDTHH:MM:SSZ --recorded-by OPERATOR --probe-evidence-file FILE [--accept-small-probe-shortfall] --confirm STACK
  rtk-cloud deployment storage-cutover --environment NAME --purpose media|ota --destination-env-file PATH --source-env-file PATH --confirm STACK
  rtk-cloud deployment storage-rollback --environment NAME --purpose media|ota --destination-env-file PATH --confirm STACK
  rtk-cloud deployment storage-retire --environment NAME --key-id ID --confirm STACK
`)
}

func resolveDeploymentConfig(workspace, environment, environmentRoot string) (deploymentConfig, error) {
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return deploymentConfig{}, err
		}
	}
	if environmentRoot != "" {
		cleanRoot := filepath.Clean(environmentRoot)
		if base := filepath.Base(cleanRoot); base == "lke" || base == "linode" {
			return deploymentConfig{}, errors.New("legacy provider env-root is not supported; use --environment NAME or cloud_env/<environment>/runtime")
		}
		environmentRoot, err = filepath.Abs(environmentRoot)
		if err != nil {
			return deploymentConfig{}, err
		}
		if environment == "" {
			environment = filepath.Base(environmentRoot)
		} else if environment != filepath.Base(environmentRoot) {
			return deploymentConfig{}, fmt.Errorf("--environment %s does not match --environment-root %s", environment, environmentRoot)
		}
	} else {
		if environment == "" {
			return deploymentConfig{}, errors.New("--environment is required")
		}
		environmentRoot = filepath.Join(workspace, "cloud_env", environment)
	}
	if !secretEnvironmentPattern.MatchString(environment) || strings.HasSuffix(environment, "-") || len("video-cloud-"+environment) > 63 {
		return deploymentConfig{}, fmt.Errorf("invalid DNS environment name %q: use lowercase alphanumeric characters and internal hyphens; video-cloud-<environment> must fit one DNS label", environment)
	}
	envIdentity, err := readStrictEnv(filepath.Join(environmentRoot, "environment.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for key := range envIdentity {
		if !deploymentEnvironmentKeys[key] {
			return deploymentConfig{}, fmt.Errorf("unknown environment key %s", key)
		}
	}
	selection, err := readStrictEnv(filepath.Join(environmentRoot, "deployment.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for key := range selection {
		if !keySet("DEPLOYMENT_ARCHITECTURE", "DEPLOYMENT_ADAPTER", "DNS_ADAPTER")[key] {
			return deploymentConfig{}, fmt.Errorf("unknown deployment selection key %s", key)
		}
	}
	for _, key := range []string{"CLOUD_STACK_NAME", "CLOUD_DNS_ROOT_DOMAIN", "DEPLOYMENT_LOCATION"} {
		if strings.TrimSpace(envIdentity[key]) == "" {
			return deploymentConfig{}, fmt.Errorf("%s is required in environment.env", key)
		}
	}
	expectedStack := envroot.Derive(map[string]string{"CLOUD_ENV_NAME": environment})["CLOUD_STACK_NAME"]
	if envIdentity["CLOUD_STACK_NAME"] != expectedStack {
		return deploymentConfig{}, fmt.Errorf("CLOUD_STACK_NAME must be %s for environment %s; coverage-* stacks are scoped runtime-coverage exceptions", expectedStack, environment)
	}
	if envIdentity["CONSOLE_DOMAIN"] != "" && environment != "prod" {
		return deploymentConfig{}, errors.New("CONSOLE_DOMAIN is supported only in the prod environment")
	}
	if err := validateManagedDNSHostname(envIdentity["CLOUD_DNS_ROOT_DOMAIN"]); err != nil {
		return deploymentConfig{}, fmt.Errorf("CLOUD_DNS_ROOT_DOMAIN: %w", err)
	}
	architecture := selection["DEPLOYMENT_ARCHITECTURE"]
	adapter := selection["DEPLOYMENT_ADAPTER"]
	dnsAdapter := selection["DNS_ADAPTER"]
	if architecture == "" || adapter == "" || dnsAdapter == "" {
		return deploymentConfig{}, errors.New("DEPLOYMENT_ARCHITECTURE, DEPLOYMENT_ADAPTER, and DNS_ADAPTER are required")
	}
	values := map[string]string{}
	for _, name := range []string{"architecture.env", "capacity.env", "topology.env", "workloads.env"} {
		part, readErr := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "architectures", architecture, name))
		if readErr != nil {
			return deploymentConfig{}, readErr
		}
		if err := mergeDeploymentLayer(values, part, "architecture"); err != nil {
			return deploymentConfig{}, err
		}
	}
	for k := range values {
		if strings.HasPrefix(k, "LKE_") || strings.HasPrefix(k, "EKS_") || strings.HasPrefix(k, "GKE_") {
			return deploymentConfig{}, fmt.Errorf("provider key %s is not allowed in architecture", k)
		}
		if !deploymentArchitectureKeys[k] {
			return deploymentConfig{}, fmt.Errorf("unknown architecture key %s", k)
		}
	}
	architectureOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "architecture.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range architectureOverride {
		if _, ok := values[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown architecture override %s", k)
		}
		values[k] = architectureOverride[k]
	}
	for k, v := range envIdentity {
		values[k] = v
	}
	if strings.TrimSpace(values["FACTORY_ENROLL_DOMAIN"]) == "" {
		values["FACTORY_ENROLL_DOMAIN"] = "factory-enroll." + values["CLOUD_STACK_NAME"] + "." + values["CLOUD_DNS_ROOT_DOMAIN"]
	}
	if enabled := values["FACTORY_ENROLL_PUBLIC_ENABLED"]; enabled != "" && enabled != "true" && enabled != "false" {
		return deploymentConfig{}, errors.New("FACTORY_ENROLL_PUBLIC_ENABLED must be true or false")
	}
	for _, key := range []string{"ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES", "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED", "SUPPORT_TICKETS_ENABLED"} {
		if value := values[key]; value != "" && value != "true" && value != "false" {
			return deploymentConfig{}, fmt.Errorf("%s must be true or false", key)
		}
	}
	if values["SUPPORT_TICKETS_ENABLED"] == "true" {
		groupID, err := strconv.ParseInt(values["ZAMMAD_SUPPORT_GROUP_ID"], 10, 64)
		if err != nil || groupID <= 0 {
			return deploymentConfig{}, errors.New("ZAMMAD_SUPPORT_GROUP_ID must be a positive integer when support tickets are enabled")
		}
		if owner := values["ZAMMAD_UNASSIGNED_OWNER_ID"]; owner != "" {
			ownerID, err := strconv.ParseInt(owner, 10, 64)
			if err != nil || ownerID <= 0 {
				return deploymentConfig{}, errors.New("ZAMMAD_UNASSIGNED_OWNER_ID must be a positive integer")
			}
		}
	}
	if values["FACTORY_ENROLL_PUBLIC_ENABLED"] == "true" {
		if err := validateFactoryEnrollmentDomain(values["FACTORY_ENROLL_DOMAIN"], values["CLOUD_STACK_NAME"], values["CLOUD_DNS_ROOT_DOMAIN"]); err != nil {
			return deploymentConfig{}, err
		}
	}
	for k, v := range selection {
		values[k] = v
	}
	adapterValues, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "adapters", adapter, "defaults.env"))
	if err != nil && adapter == "lke" {
		return deploymentConfig{}, err
	}
	if adapterValues == nil {
		adapterValues = map[string]string{}
	}
	adapterSchema, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "adapters", adapter, "schema.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	if adapterSchema["ADAPTER_NAME"] != adapter || adapterSchema["ADAPTER_RUNTIME"] != values["DEPLOYMENT_RUNTIME"] {
		return deploymentConfig{}, fmt.Errorf("adapter %s schema is incompatible with runtime %s", adapter, values["DEPLOYMENT_RUNTIME"])
	}
	for key := range adapterValues {
		if deploymentArchitectureKeys[key] {
			return deploymentConfig{}, fmt.Errorf("adapter %s may not override architecture key %s", adapter, key)
		}
	}
	adapterOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "adapter.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range adapterOverride {
		if _, ok := adapterValues[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown %s adapter override %s", adapter, k)
		}
		adapterValues[k] = adapterOverride[k]
	}
	dnsValues, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "dns_adapters", dnsAdapter, "defaults.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	dnsSchema, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "dns_adapters", dnsAdapter, "schema.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	if dnsSchema["DNS_ADAPTER_NAME"] != dnsAdapter {
		return deploymentConfig{}, fmt.Errorf("DNS adapter %s schema is invalid", dnsAdapter)
	}
	dnsAllowed := keySet("DNS_RECORD_TTL", "DNS_PROPAGATION_TIMEOUT_SECONDS", "DNS_PROPAGATION_INTERVAL_SECONDS")
	if dnsAdapter == "godaddy" {
		dnsAllowed["GODADDY_ENV"] = true
	}
	if dnsAdapter == "route53" {
		dnsAllowed["ROUTE53_CONTROL_PLANE_REGION"] = true
	}
	for key := range dnsValues {
		if !dnsAllowed[key] {
			return deploymentConfig{}, fmt.Errorf("unknown %s DNS adapter key %s", dnsAdapter, key)
		}
	}
	dnsOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "dns.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range dnsOverride {
		if _, ok := dnsValues[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown %s DNS adapter override %s", dnsAdapter, k)
		}
		dnsValues[k] = dnsOverride[k]
	}
	for _, key := range []string{"DNS_RECORD_TTL", "DNS_PROPAGATION_TIMEOUT_SECONDS", "DNS_PROPAGATION_INTERVAL_SECONDS"} {
		if _, err := positiveIntValue(key, dnsValues[key]); err != nil {
			return deploymentConfig{}, err
		}
	}
	if dnsAdapter == "godaddy" {
		if dnsValues["GODADDY_ENV"] != "prod" && dnsValues["GODADDY_ENV"] != "ote" {
			return deploymentConfig{}, errors.New("GODADDY_ENV must be prod or ote")
		}
		if ttl, _ := strconv.Atoi(dnsValues["DNS_RECORD_TTL"]); ttl < 600 {
			return deploymentConfig{}, errors.New("GoDaddy DNS_RECORD_TTL must be at least 600")
		}
	}
	for k, v := range appendMap(values, adapterValues) {
		if deploymentIntegerKeys[k] {
			n, parseErr := strconv.Atoi(v)
			if parseErr != nil || n < 0 {
				return deploymentConfig{}, fmt.Errorf("%s must be a non-negative integer", k)
			}
		}
	}
	for _, key := range []string{
		"NODE_CLASS_GENERAL_MIN_VCPU", "NODE_CLASS_GENERAL_MIN_MEMORY_GIB",
		"NODE_CLASS_BROKER_MIN_VCPU", "NODE_CLASS_BROKER_MIN_MEMORY_GIB",
		"NODE_CLASS_DATABASE_MIN_VCPU", "NODE_CLASS_DATABASE_MIN_MEMORY_GIB",
	} {
		n, _ := strconv.Atoi(values[key])
		if n <= 0 {
			return deploymentConfig{}, fmt.Errorf("%s must be a positive integer", key)
		}
	}
	if raw := values["MQTT_HARD_ANTI_AFFINITY"]; raw != "true" && raw != "false" {
		return deploymentConfig{}, errors.New("MQTT_HARD_ANTI_AFFINITY must be true or false")
	}
	if _, err := deploymentCertificateAlgorithm("CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM", values["CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM"]); err != nil {
		return deploymentConfig{}, err
	}
	for _, key := range []string{"CERTIFICATE_APP_CSR_KEY_ALGORITHMS", "CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS"} {
		if _, err := deploymentCertificateAlgorithms(key, values[key]); err != nil {
			return deploymentConfig{}, err
		}
	}
	if err := validateDeploymentOTATrustedManifestKeys(values["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"]); err != nil {
		return deploymentConfig{}, err
	}
	turnMin, _ := strconv.Atoi(values["TURN_MIN_PORT"])
	turnMax, _ := strconv.Atoi(values["TURN_MAX_PORT"])
	if turnMin > turnMax {
		return deploymentConfig{}, errors.New("TURN_MIN_PORT must not exceed TURN_MAX_PORT")
	}
	if values["DEPLOYMENT_RUNTIME"] != "kubernetes" {
		return deploymentConfig{}, fmt.Errorf("unsupported deployment runtime %q", values["DEPLOYMENT_RUNTIME"])
	}
	capacity, capacityValues, err := buildSharedCapacityPlan(values)
	if err != nil {
		return deploymentConfig{}, err
	}
	for key, value := range capacityValues {
		values[key] = value
	}
	adapterResolved := map[string]string{}
	if adapter == "lke" {
		adapterResolved, err = resolveLKEAdapterResources(workspace, values, adapterValues)
		if err != nil {
			return deploymentConfig{}, err
		}
	}
	storage, err := resolveDeploymentStoragePlan(workspace, environmentRoot, envIdentity, adapterResolved)
	if err != nil {
		return deploymentConfig{}, err
	}
	cfg := deploymentConfig{Workspace: workspace, Environment: environment, EnvironmentRoot: environmentRoot, RuntimeRoot: filepath.Join(environmentRoot, "runtime"), Architecture: architecture, Adapter: adapter, DNSAdapter: dnsAdapter, Values: values, AdapterValues: adapterValues, AdapterResolved: adapterResolved, DNSValues: dnsValues, Capacity: capacity, Storage: storage}
	if err := validateDeploymentDNSPlan(cfg); err != nil {
		return deploymentConfig{}, err
	}
	return cfg, nil
}

func validateFactoryEnrollmentDomain(domain, stack, root string) error {
	if len(domain) > 253 || !strings.HasSuffix(domain, "."+root) || domain == root || domain != strings.ToLower(domain) {
		return errors.New("FACTORY_ENROLL_DOMAIN must be a lowercase hostname beneath CLOUD_DNS_ROOT_DOMAIN")
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("FACTORY_ENROLL_DOMAIN contains an invalid DNS label")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("FACTORY_ENROLL_DOMAIN contains an invalid DNS character")
			}
		}
	}
	base := stack + "." + root
	for _, prefix := range []string{"", "device.", "certissuer.", "turnregistry.", "account-manager.", "admin.", "frontend.", "logger.", "turn.", "billing.", "payment-simulator."} {
		if domain == prefix+base {
			return errors.New("FACTORY_ENROLL_DOMAIN must be independent of existing service hostnames")
		}
	}
	return nil
}

func validateDeploymentOTATrustedManifestKeys(raw string) error {
	var keys map[string]string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil || keys == nil {
		return errors.New("VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON must be a JSON object")
	}
	for id, encoded := range keys {
		if strings.TrimSpace(id) == "" {
			return errors.New("VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON key IDs must not be empty")
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != 32 {
			decoded, err = base64.StdEncoding.DecodeString(encoded)
		}
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON key %q must be a 32-byte Ed25519 public key encoded as hex or standard base64", id)
		}
	}
	return nil
}

func deploymentCertificateAlgorithm(key, raw string) (string, error) {
	algorithm := strings.TrimSpace(strings.ToLower(raw))
	if algorithm != "ed25519" && algorithm != "p256" {
		return "", fmt.Errorf("%s must be ed25519 or p256", key)
	}
	return algorithm, nil
}

func deploymentCertificateAlgorithms(key, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%s must contain at least one certificate key algorithm", key)
	}
	parts := strings.Split(raw, ",")
	algorithms := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		algorithm, err := deploymentCertificateAlgorithm(key, part)
		if err != nil {
			return nil, err
		}
		if seen[algorithm] {
			return nil, fmt.Errorf("%s must not contain duplicate algorithm %s", key, algorithm)
		}
		seen[algorithm] = true
		algorithms = append(algorithms, algorithm)
	}
	return algorithms, nil
}

func resolveDeploymentStoragePlan(workspace, environmentRoot string, identity, adapterResolved map[string]string) (deploymentStoragePlan, error) {
	runtime, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "storage.env"))
	if err != nil {
		return deploymentStoragePlan{}, err
	}
	if len(runtime) == 0 {
		region := strings.TrimSpace(adapterResolved["LKE_REGION"])
		runtime = map[string]string{"RUNTIME_MEDIA_STORAGE_POLICY": "colocated", "RUNTIME_MEDIA_STORAGE_BUCKET": "rtk-cloud-" + filepath.Base(environmentRoot) + "-runtime-" + region, "RUNTIME_MEDIA_STORAGE_PREFIX": "environments/" + identity["CLOUD_STACK_NAME"]}
	}
	for key := range runtime {
		if !keySet("RUNTIME_MEDIA_STORAGE_POLICY", "RUNTIME_MEDIA_STORAGE_BUCKET", "RUNTIME_MEDIA_STORAGE_PREFIX", "RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED", "RUNTIME_MEDIA_STORAGE_NAMING_EXCEPTION", "RUNTIME_OTA_STORAGE_MODE", "RUNTIME_OTA_STORAGE_POLICY", "RUNTIME_OTA_STORAGE_BUCKET", "RUNTIME_OTA_STORAGE_REGION", "RUNTIME_OTA_STORAGE_PREFIX")[key] {
			return deploymentStoragePlan{}, fmt.Errorf("unknown runtime storage key %s", key)
		}
	}
	if runtime["RUNTIME_MEDIA_STORAGE_POLICY"] != "colocated" {
		return deploymentStoragePlan{}, errors.New("RUNTIME_MEDIA_STORAGE_POLICY must be colocated")
	}
	if value := runtime["RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED"]; value != "" && value != "true" && value != "false" {
		return deploymentStoragePlan{}, errors.New("RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED must be true or false")
	}
	for _, key := range []string{"RUNTIME_MEDIA_STORAGE_BUCKET", "RUNTIME_MEDIA_STORAGE_PREFIX"} {
		if strings.TrimSpace(runtime[key]) == "" {
			return deploymentStoragePlan{}, fmt.Errorf("%s is required in storage.env", key)
		}
	}
	if err := storagepolicy.ValidatePrefix(runtime["RUNTIME_MEDIA_STORAGE_PREFIX"]); err != nil {
		return deploymentStoragePlan{}, fmt.Errorf("RUNTIME_MEDIA_STORAGE_PREFIX: %w", err)
	}
	shared, err := readOptionalStrictEnv(filepath.Join(workspace, "cloud_deploy", "storage", "release-artifacts.env"))
	if err != nil {
		return deploymentStoragePlan{}, err
	}
	if len(shared) == 0 {
		shared = map[string]string{"RELEASE_ARTIFACT_STORAGE_POLICY": "shared-cross-region", "RELEASE_ARTIFACT_STORAGE_BUCKET": "rtk-cloud-shared-artifacts-us-sea", "RELEASE_ARTIFACT_STORAGE_LOCATION": "us-west", "RELEASE_ARTIFACT_STORAGE_REGION": "us-sea", "RELEASE_ARTIFACT_STORAGE_PREFIX": "releases"}
	}
	allowed := keySet("RELEASE_ARTIFACT_STORAGE_POLICY", "RELEASE_ARTIFACT_STORAGE_BUCKET", "RELEASE_ARTIFACT_STORAGE_LOCATION", "RELEASE_ARTIFACT_STORAGE_REGION", "RELEASE_ARTIFACT_STORAGE_PREFIX")
	for key := range shared {
		if !allowed[key] {
			return deploymentStoragePlan{}, fmt.Errorf("unknown release artifact storage key %s", key)
		}
	}
	if shared["RELEASE_ARTIFACT_STORAGE_POLICY"] != "shared-cross-region" {
		return deploymentStoragePlan{}, errors.New("RELEASE_ARTIFACT_STORAGE_POLICY must be shared-cross-region")
	}
	for _, key := range []string{"RELEASE_ARTIFACT_STORAGE_BUCKET", "RELEASE_ARTIFACT_STORAGE_LOCATION", "RELEASE_ARTIFACT_STORAGE_REGION"} {
		if strings.TrimSpace(shared[key]) == "" {
			return deploymentStoragePlan{}, fmt.Errorf("%s is required in release-artifacts.env", key)
		}
	}
	if err := storagepolicy.ValidatePrefix(shared["RELEASE_ARTIFACT_STORAGE_PREFIX"]); err != nil {
		return deploymentStoragePlan{}, fmt.Errorf("RELEASE_ARTIFACT_STORAGE_PREFIX: %w", err)
	}
	computeRegion := strings.TrimSpace(adapterResolved["LKE_REGION"])
	if computeRegion == "" && len(adapterResolved) > 0 {
		return deploymentStoragePlan{}, errors.New("resolved compute region is required for colocated runtime storage")
	}
	if computeRegion != "" && strings.TrimSpace(runtime["RUNTIME_MEDIA_STORAGE_NAMING_EXCEPTION"]) == "" {
		if err := storagepolicy.Validate(runtime["RUNTIME_MEDIA_STORAGE_BUCKET"], filepath.Base(environmentRoot), "runtime", computeRegion); err != nil {
			return deploymentStoragePlan{}, err
		}
	}
	if err := storagepolicy.Validate(shared["RELEASE_ARTIFACT_STORAGE_BUCKET"], "shared", "artifacts", shared["RELEASE_ARTIFACT_STORAGE_REGION"]); err != nil {
		return deploymentStoragePlan{}, err
	}
	otaMode := firstNonEmpty(runtime["RUNTIME_OTA_STORAGE_MODE"], "legacy-shared")
	if otaMode != "legacy-shared" && otaMode != "dedicated" {
		return deploymentStoragePlan{}, errors.New("RUNTIME_OTA_STORAGE_MODE must be legacy-shared or dedicated")
	}
	ota := deploymentStorageTarget{Purpose: "ota-firmware", Policy: "colocated", LogicalLocation: identity["DEPLOYMENT_LOCATION"], Bucket: runtime["RUNTIME_MEDIA_STORAGE_BUCKET"], Prefix: strings.Trim(runtime["RUNTIME_MEDIA_STORAGE_PREFIX"], "/"), Region: computeRegion}
	if otaMode == "dedicated" {
		if strings.TrimSpace(runtime["RUNTIME_OTA_STORAGE_BUCKET"]) == "" || strings.TrimSpace(runtime["RUNTIME_OTA_STORAGE_PREFIX"]) == "" {
			return deploymentStoragePlan{}, errors.New("dedicated OTA storage requires a bucket and prefix")
		}
		if err := storagepolicy.ValidatePrefix(runtime["RUNTIME_OTA_STORAGE_PREFIX"]); err != nil {
			return deploymentStoragePlan{}, fmt.Errorf("RUNTIME_OTA_STORAGE_PREFIX: %w", err)
		}
		if runtime["RUNTIME_OTA_STORAGE_BUCKET"] == runtime["RUNTIME_MEDIA_STORAGE_BUCKET"] {
			return deploymentStoragePlan{}, errors.New("dedicated OTA bucket must differ from runtime media bucket")
		}
		if computeRegion == "" {
			return deploymentStoragePlan{}, errors.New("dedicated OTA storage requires a resolved compute region")
		}
		ota.Policy = runtime["RUNTIME_OTA_STORAGE_POLICY"]
		switch ota.Policy {
		case "colocated":
			if strings.TrimSpace(runtime["RUNTIME_OTA_STORAGE_REGION"]) != "" {
				return deploymentStoragePlan{}, errors.New("colocated OTA storage must not set RUNTIME_OTA_STORAGE_REGION")
			}
		case "cross-region":
			ota.Region = strings.TrimSpace(runtime["RUNTIME_OTA_STORAGE_REGION"])
			if ota.Region == "" || ota.Region == computeRegion {
				return deploymentStoragePlan{}, errors.New("cross-region OTA storage requires an explicit region different from the compute region")
			}
		default:
			return deploymentStoragePlan{}, errors.New("dedicated OTA storage policy must be colocated or cross-region")
		}
		expectedBucket, nameErr := storagepolicy.Bucket(filepath.Base(environmentRoot), "ota-firmware", ota.Region)
		if nameErr != nil {
			return deploymentStoragePlan{}, nameErr
		}
		if runtime["RUNTIME_OTA_STORAGE_BUCKET"] != expectedBucket {
			return deploymentStoragePlan{}, fmt.Errorf("dedicated OTA bucket must be %s for the selected environment and storage region", expectedBucket)
		}
		ota.Bucket = runtime["RUNTIME_OTA_STORAGE_BUCKET"]
		ota.Prefix = strings.Trim(runtime["RUNTIME_OTA_STORAGE_PREFIX"], "/")
	}
	return deploymentStoragePlan{
		RuntimeMedia:                deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", LogicalLocation: identity["DEPLOYMENT_LOCATION"], Bucket: runtime["RUNTIME_MEDIA_STORAGE_BUCKET"], Prefix: strings.Trim(runtime["RUNTIME_MEDIA_STORAGE_PREFIX"], "/"), Region: computeRegion},
		RuntimeMediaCutoverRequired: runtime["RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED"] == "true",
		OTAFirmware:                 ota,
		OTAMode:                     otaMode,
		ReleaseArtifacts:            deploymentStorageTarget{Purpose: "release-artifacts", Policy: "shared-cross-region", LogicalLocation: shared["RELEASE_ARTIFACT_STORAGE_LOCATION"], Bucket: shared["RELEASE_ARTIFACT_STORAGE_BUCKET"], Prefix: strings.Trim(shared["RELEASE_ARTIFACT_STORAGE_PREFIX"], "/"), Region: shared["RELEASE_ARTIFACT_STORAGE_REGION"]},
	}, nil
}

func appendMap(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
func mergeDeploymentLayer(dst, src map[string]string, layer string) error {
	for k, v := range src {
		if _, ok := dst[k]; ok {
			return fmt.Errorf("duplicate %s key %s", layer, k)
		}
		dst[k] = v
	}
	return nil
}

func readStrictEnv(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for n, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s:%d invalid env assignment", path, n+1)
		}
		key = strings.TrimSpace(key)
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("%s:%d duplicate key %s", path, n+1, key)
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, nil
}
func readOptionalStrictEnv(path string) (map[string]string, error) {
	out, err := readStrictEnv(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	return out, err
}

func readLKEAccountState(runtimeRoot string, required bool) (map[string]string, error) {
	path := filepath.Join(runtimeRoot, "adapters", "lke", "account.env")
	account, err := readStrictEnv(path)
	if os.IsNotExist(err) && !required {
		return map[string]string{}, nil
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("LKE provider account state is required before mutation: create %s with LKE_ACTIVE_SERVICE_LIMIT", path)
		}
		return nil, err
	}
	for key := range account {
		if key != "LKE_ACTIVE_SERVICE_LIMIT" {
			return nil, fmt.Errorf("unknown LKE provider account key %s", key)
		}
	}
	return account, nil
}

func positiveIntValue(key, raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}

func validateActiveServiceLimit(raw string) error {
	if raw == "unlimited" {
		return nil
	}
	if _, err := positiveIntValue("LKE_ACTIVE_SERVICE_LIMIT", raw); err != nil {
		return errors.New("LKE_ACTIVE_SERVICE_LIMIT must be a positive integer or unlimited")
	}
	return nil
}

func nonNegativeIntValue(key, raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return n, nil
}

func materializeDeploymentRuntime(cfg deploymentConfig) error {
	if err := validateDeploymentDNSPlan(cfg); err != nil {
		return err
	}
	for _, dir := range []string{"resolved", "env", "state", filepath.Join("adapters", cfg.Adapter), filepath.Join("dns", cfg.DNSAdapter), "services", "secrets", "devices", "artifacts", "backups"} {
		if err := os.MkdirAll(filepath.Join(cfg.RuntimeRoot, dir), 0o700); err != nil {
			return err
		}
	}
	resolved := appendMap(cfg.Values, nil)
	stack := deploymentEndpointValues(cfg)
	stack = appendMap(stack, deploymentRuntimeEndpoints(stack))
	stack = appendMap(stack, cfg.DNSValues)
	stack["CLOUD_ENV_NAME"] = cfg.Environment
	stack["CLOUD_PROVIDER"] = cfg.Adapter
	stack["CLOUD_REGION"] = cfg.AdapterResolved["LKE_REGION"]
	stack["VIDEO_CLOUD_BLOB_BUCKET"] = cfg.Storage.RuntimeMedia.Bucket
	stack["VIDEO_CLOUD_BLOB_REGION"] = cfg.Storage.RuntimeMedia.Region
	stack["VIDEO_CLOUD_BLOB_PREFIX"] = cfg.Storage.RuntimeMedia.Prefix
	stack["VIDEO_CLOUD_OTA_STORAGE_MODE"] = cfg.Storage.OTAMode
	stack["VIDEO_CLOUD_OTA_BLOB_BUCKET"] = cfg.Storage.OTAFirmware.Bucket
	stack["VIDEO_CLOUD_OTA_BLOB_REGION"] = cfg.Storage.OTAFirmware.Region
	stack["VIDEO_CLOUD_OTA_BLOB_PREFIX"] = cfg.Storage.OTAFirmware.Prefix
	if receipt, err := readDeploymentStorageReceipt(cfg.RuntimeRoot); err == nil && receipt.Bucket == cfg.Storage.RuntimeMedia.Bucket && receipt.Region == cfg.Storage.RuntimeMedia.Region {
		stack["VIDEO_CLOUD_BLOB_ENDPOINT"] = receipt.Endpoint
	}
	if cfg.Storage.OTAMode == "dedicated" {
		var receipt deploymentStorageReceipt
		if body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight-ota.json")); err == nil && json.Unmarshal(body, &receipt) == nil && receipt.Bucket == cfg.Storage.OTAFirmware.Bucket && receipt.Region == cfg.Storage.OTAFirmware.Region {
			stack["VIDEO_CLOUD_OTA_BLOB_ENDPOINT"] = receipt.Endpoint
			stack["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] = receipt.EndpointType
		}
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "resolved", "deployment.env"), resolved, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"), stack, 0o600); err != nil {
		return err
	}
	adapterRuntime := appendMap(cfg.AdapterValues, cfg.AdapterResolved)
	compatInput := appendMap(resolved, cfg.AdapterResolved)
	adapterRuntime = appendMap(adapterRuntime, deploymentLegacyLKEValues(compatInput, cfg.Environment))
	account, err := readLKEAccountState(cfg.RuntimeRoot, false)
	if err != nil {
		return err
	}
	providerPreflight := map[string]string{}
	if region := cfg.AdapterResolved["LKE_REGION"]; region != "" {
		providerPreflight["PROVIDER_REGION"] = region
	}
	if limit := account["LKE_ACTIVE_SERVICE_LIMIT"]; limit != "" {
		if err := validateActiveServiceLimit(limit); err != nil {
			return err
		}
		adapterRuntime["LKE_LINODE_ACTIVE_SERVICE_LIMIT"] = limit
		providerPreflight["PROVIDER_ACTIVE_SERVICE_LIMIT"] = limit
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "adapters", cfg.Adapter, "config.env"), adapterRuntime, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "adapters", cfg.Adapter, "resolved-resources.env"), cfg.AdapterResolved, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env"), providerPreflight, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "dns", cfg.DNSAdapter, "config.env"), cfg.DNSValues, 0o600); err != nil {
		return err
	}
	dnsPlan := buildGenericDNSPlan(cfg)
	if body, err := json.MarshalIndent(dnsPlan, "", "  "); err != nil {
		return err
	} else if err := os.WriteFile(filepath.Join(cfg.RuntimeRoot, "resolved", "dns-plan.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "state", "dns.env"), map[string]string{"DNS_ADAPTER": cfg.DNSAdapter, "DNS_ROOT_DOMAIN": cfg.Values["CLOUD_DNS_ROOT_DOMAIN"]}, 0o600); err != nil {
		return err
	}
	plan := map[string]any{"environment": cfg.Environment, "architecture": cfg.Architecture, "adapter": cfg.Adapter, "dns_adapter": cfg.DNSAdapter, "values": resolved, "capacity": cfg.Capacity, "storage": cfg.Storage}
	body, _ := json.MarshalIndent(plan, "", "  ")
	body = append(body, '\n')
	return os.WriteFile(filepath.Join(cfg.RuntimeRoot, "resolved", "deployment-plan.json"), body, 0o600)
}

func materializeStagingE2EDeploymentConfig(workspace, envRoot string) error {
	environmentRoot := filepath.Dir(filepath.Clean(envRoot))
	cfg, err := resolveDeploymentConfig(workspace, "", environmentRoot)
	if err != nil {
		return fmt.Errorf("staging E2E deployment config: %w", err)
	}
	if filepath.Clean(cfg.RuntimeRoot) != filepath.Clean(envRoot) {
		return fmt.Errorf("staging E2E runtime mismatch: resolved %s, requested %s", cfg.RuntimeRoot, envRoot)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return fmt.Errorf("materialize staging E2E deployment config: %w", err)
	}
	stack, err := readStrictEnv(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	if lkeClipDirectUploadEnabled(stack) && strings.TrimSpace(stack["VIDEO_CLOUD_BLOB_ENDPOINT"]) == "" {
		return errors.New("staging reset blocked before deleting workloads: validated Object Storage receipt is missing; run deployment credentials-check --environment " + cfg.Environment)
	}
	return nil
}

func deploymentRuntimeEndpoints(v map[string]string) map[string]string {
	stack := strings.TrimSpace(v["CLOUD_STACK_NAME"])
	rootDomain := strings.TrimSpace(v["CLOUD_DNS_ROOT_DOMAIN"])
	if stack == "" || rootDomain == "" {
		return map[string]string{}
	}
	publicHost := firstNonEmpty(strings.TrimSpace(v["VIDEO_CLOUD_DOMAIN"]), stack+"."+rootDomain)
	accountHost := firstNonEmpty(strings.TrimSpace(v["ACCOUNT_MANAGER_DOMAIN"]), "account-manager."+stack+"."+rootDomain)
	deviceHost := lkeDeviceDomain(appendMap(v, map[string]string{"VIDEO_CLOUD_DOMAIN": publicHost}))
	return map[string]string{
		"ACCOUNT_MANAGER_BASE_URL":    "https://" + accountHost,
		"VIDEO_CLOUD_BASE_URL":        "https://" + publicHost,
		"VIDEO_CLOUD_PUBLIC_BASE_URL": "https://" + publicHost,
		"VIDEO_CLOUD_MTLS_BASE_URL":   "https://" + deviceHost,
		"VIDEO_CLOUD_TOKEN_BASE_URL":  "https://" + deviceHost,
		"VIDEO_CLOUD_MQTT_ADDR":       publicHost + ":8883",
	}
}

// deploymentEndpointValues supplies the same generated hostnames and TURN intent
// to planning and runtime materialization without consulting a live cluster.
func deploymentEndpointValues(cfg deploymentConfig) map[string]string {
	values := appendMap(cfg.Values, map[string]string{"CLOUD_ENV_NAME": cfg.Environment})
	values = envroot.Derive(values)
	return values
}

func validateDeploymentDNSPlan(cfg deploymentConfig) error {
	values := deploymentEndpointValues(cfg)
	if values["FACTORY_ENROLL_PUBLIC_ENABLED"] == "true" {
		for _, route := range deploymentPublicHTTPSRoutes(values) {
			if route.Host == values["FACTORY_ENROLL_DOMAIN"] && (route.Service != "factoryenroll" || route.Path != "/v1/factory/enroll" || !route.Exact) {
				return errors.New("FACTORY_ENROLL_DOMAIN must be independent of effective public service hostnames")
			}
		}
	}
	plan := buildGenericDNSPlan(cfg)
	if err := validateManagedDNSHostname(plan.RootDomain); err != nil {
		return fmt.Errorf("CLOUD_DNS_ROOT_DOMAIN: %w", err)
	}
	owners := map[string]string{}
	for _, record := range plan.Records {
		if err := validateManagedDNSHostname(record.Name); err != nil {
			return err
		}
		if err := validateDNSRecord(plan.RootDomain, record); err != nil {
			return err
		}
		key := record.Type + ":" + record.Name
		target := strings.Join(record.Values, ",")
		if previous, ok := owners[key]; ok && previous != target {
			return fmt.Errorf("DNS hostname %s has conflicting targets %s and %s", record.Name, previous, target)
		}
		owners[key] = target
	}
	if raw := lkeEnvValue(cfg.Values, "PUBLIC_BASE_URL"); raw != "" {
		parsed, err := url.Parse(raw)
		frontend := lkeFrontendPublicDomain(deploymentEndpointValues(cfg))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != frontend || (parsed.Port() != "" && parsed.Port() != "443") || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("PUBLIC_BASE_URL must use the effective frontend origin https://%s", frontend)
		}
	}
	return nil
}

func deploymentLegacyLKEValues(v map[string]string, environment string) map[string]string {
	if v["DEPLOYMENT_ADAPTER"] != "lke" {
		return map[string]string{}
	}
	out := map[string]string{
		"CLOUD_ENV_NAME": environment, "CLOUD_PROVIDER": "lke", "CLOUD_REGION": v["LKE_REGION"],
		"LKE_TARGET_CONNECTS": v["CAPACITY_TARGET_CONNECTIONS"], "LKE_MQTT_CONNECTIONS_PER_POD": v["CAPACITY_CONNECTIONS_PER_MQTT_POD"],
		"LKE_SYSTEM_RESERVED_CPU_PER_NODE": v["CAPACITY_SYSTEM_RESERVED_CPU_MILLI"] + "m", "LKE_SYSTEM_RESERVED_MEMORY_PER_NODE": v["CAPACITY_SYSTEM_RESERVED_MEMORY_MIB"] + "Mi",
		"LKE_NODE_COUNT": v["NODE_CLASS_BROKER_EFFECTIVE_COUNT"], "LKE_NODE_TYPE": v["LKE_BROKER_NODE_TYPE"],
		"LKE_GENERAL_NODE_COUNT": v["NODE_CLASS_GENERAL_EFFECTIVE_COUNT"], "LKE_GENERAL_NODE_TYPE": v["LKE_GENERAL_NODE_TYPE"],
		"LKE_POSTGRES_DEDICATED_NODE_POOL": strconv.FormatBool(v["NODE_CLASS_DATABASE_EFFECTIVE_COUNT"] != "" && v["NODE_CLASS_DATABASE_EFFECTIVE_COUNT"] != "0"), "LKE_POSTGRES_NODE_COUNT": v["NODE_CLASS_DATABASE_EFFECTIVE_COUNT"], "LKE_POSTGRES_NODE_TYPE": v["LKE_DATABASE_NODE_TYPE"],
		"LKE_MQTT_REPLICAS": v["MQTT_EFFECTIVE_REPLICAS"], "LKE_VIDEO_CLOUD_REPLICAS": v["VIDEO_CLOUD_API_EFFECTIVE_REPLICAS"],
		"LKE_POSTGRES_REQUEST_CPU": v["POSTGRES_REQUEST_CPU"], "LKE_POSTGRES_REQUEST_MEMORY": v["POSTGRES_REQUEST_MEMORY"], "LKE_POSTGRES_LIMIT_MEMORY": v["POSTGRES_LIMIT_MEMORY"],
		"LKE_CLOUD_LOGGER_REQUEST_CPU": v["CLOUD_LOGGER_REQUEST_CPU"], "LKE_CLOUD_LOGGER_REQUEST_MEMORY": v["CLOUD_LOGGER_REQUEST_MEMORY"], "LKE_CLOUD_LOGGER_LIMIT_MEMORY": v["CLOUD_LOGGER_LIMIT_MEMORY"],
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": v["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"],
		"LKE_EDGE_HAPROXY_COUNT":                 v["EDGE_REPLICAS"], "LKE_EDGE_HAPROXY_MAXCONN": v["EDGE_MAX_CONNECTIONS"],
		"LKE_COTURN_VM_COUNT": v["TURN_REPLICAS"], "LKE_COTURN_MIN_PORT": v["TURN_MIN_PORT"], "LKE_COTURN_MAX_PORT": v["TURN_MAX_PORT"],
	}
	for _, prefix := range []string{"INGRESS", "REDIS", "REDIS_EXPORTER", "FLEET_VALKEY", "FLEET_VALKEY_EXPORTER"} {
		out["LKE_"+prefix+"_REQUEST_CPU"] = v[prefix+"_REQUEST_CPU"]
		out["LKE_"+prefix+"_REQUEST_MEMORY"] = v[prefix+"_REQUEST_MEMORY"]
	}
	return out
}

func writeSortedEnv(path string, values map[string]string, mode os.FileMode) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if values[k] != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, values[k])
		}
	}
	return os.WriteFile(path, []byte(b.String()), mode)
}

func normalizeDeploymentRuntime(cfg deploymentConfig) error {
	normalized := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	body, err := os.ReadFile(normalized)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" {
		return nil
	}
	adapterState, err := readOptionalStrictEnv(filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "state.env"))
	if err != nil {
		return err
	}
	clusterID := strings.TrimSpace(adapterState["LKE_CLUSTER_ID"])
	if clusterID == "" {
		return nil
	}
	replacements := []string{
		"lke" + clusterID + "-admin", "rtk-cloud-" + cfg.Environment + "-admin",
		"lke" + clusterID + "-ctx", "rtk-cloud-" + cfg.Environment + "-context",
		"lke" + clusterID, "rtk-cloud-" + cfg.Environment + "-cluster",
	}
	sanitized := strings.NewReplacer(replacements...).Replace(string(body))
	return os.WriteFile(normalized, []byte(sanitized), 0o600)
}
