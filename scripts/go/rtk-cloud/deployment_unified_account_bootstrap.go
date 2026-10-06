package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type accountBootstrapSeal struct {
	SystemIdentifier string `json:"system_identifier"`
	Sealed           bool   `json:"sealed"`
}

// Verify the original cluster's actual durable seal. Operator-created JSON or
// the absence of a bootstrap password does not prove the database was sealed.
func (b environmentKubernetesBackend) accountBootstrapSeal(ctx context.Context, protection postgresBackupDeployment) (accountBootstrapSeal, error) {
	var result accountBootstrapSeal
	if protection.Environment != b.Store.Environment || protection.Namespace != "video-cloud-"+b.Store.Environment+"-platform" || protection.SourcePod != "postgresql-0" || protection.SourceContainer != "postgres" || protection.Worker.Source.SystemIdentifier == "" {
		return result, errors.New("original Account Manager database owner binding required")
	}
	query := `SELECT json_build_object('system_identifier',(SELECT system_identifier::text FROM pg_control_system()),'sealed',(SELECT sealed_at IS NOT NULL FROM platform_bootstrap WHERE singleton=true))::text`
	raw, err := b.command(ctx, nil, "exec", "--namespace", protection.Namespace, protection.SourcePod, "--container", protection.SourceContainer, "--", "psql", "-X", "-U", "postgres", "-d", "rtk_account_manager", "-tA", "-v", "ON_ERROR_STOP=1", "-c", query)
	if err != nil || json.Unmarshal(raw, &result) != nil || !result.Sealed || result.SystemIdentifier != protection.Worker.Source.SystemIdentifier {
		return result, errors.New("original Account Manager bootstrap seal is unavailable, unsealed or belongs to another database cluster")
	}
	return result, nil
}

func accountManagerBootstrapRetirement(secret deploymentplan.Object, stack string, seal accountBootstrapSeal, expectedSystemID string) (deploymentplan.Definition, bool, error) {
	id := "Secret/" + stack + "-account-manager/account-manager-runtime"
	if deploymentplan.ID(secret) != id || !seal.Sealed || expectedSystemID == "" || seal.SystemIdentifier != expectedSystemID {
		return deploymentplan.Definition{}, false, errors.New("actual original sealed Account Manager and runtime Secret required")
	}
	data := deploymentplan.Map(secret["data"])
	dsn, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["DATABASE_URL"]))
	parsed, parseErr := url.Parse(string(dsn))
	if err != nil || parseErr != nil || parsed == nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Hostname() != "postgresql."+stack+"-platform.svc.cluster.local" || parsed.Path != "/rtk_account_manager" || parsed.Port() != "" && parsed.Port() != "5432" {
		return deploymentplan.Definition{}, false, errors.New("Account Manager runtime database does not match the verified original source")
	}
	changed := false
	for _, key := range []string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"} {
		if _, exists := data[key]; exists {
			changed = true
		}
	}
	if !changed {
		return deploymentplan.Definition{}, false, nil
	}
	if secret["immutable"] == true {
		return deploymentplan.Definition{}, false, &deploymentplan.Blocker{ID: "deployment.bootstrap.account-manager", Resource: id, Cause: "immutable runtime Secret still contains bootstrap credentials", Next: "complete the separately reviewed runtime Secret reference migration; do not delete or reconstruct existing credentials"}
	}
	// This explicit Secret lifecycle preserves every non-bootstrap byte. It is
	// not a live workload spec used as renderer intent. The planner fences UID,
	// resource version and full baseline for the exact admitted retirement body.
	desired := deploymentplan.Clone(secret)
	delete(desired, "status")
	for _, key := range []string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"} {
		delete(deploymentplan.Map(desired["data"]), key)
	}
	if !accountManagerBootstrapKeysAbsent(desired) {
		return deploymentplan.Definition{}, false, errors.New("unknown Account Manager bootstrap field requires a reviewed lifecycle mapping")
	}
	return deploymentplan.Definition{Object: desired, Phase: "configuration"}, true, nil
}

func bindAccountManagerBootstrapRetirement(ctx context.Context, backend environmentKubernetesBackend, definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings, protection postgresBackupDeployment, operation string) ([]deploymentplan.Definition, error) {
	if operation == "create" {
		return definitions, nil
	} // fresh bootstrap seals at its later stage gate
	id := "Secret/" + bindings.Stack + "-account-manager/account-manager-runtime"
	var secret deploymentplan.Object
	for _, object := range live {
		if deploymentplan.ID(object) == id {
			secret = object
			break
		}
	}
	if secret == nil {
		return nil, &deploymentplan.Blocker{ID: "deployment.bootstrap.account-manager", Resource: id, Cause: "original runtime Secret is absent", Next: "restore its original owned state; complete upgrade cannot initialize replacement credentials"}
	}
	meta := deploymentplan.Map(secret["metadata"])
	uid := deploymentplan.String(meta["uid"])
	owner := deploymentplan.String(deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"])
	if uid == "" || meta["deletionTimestamp"] != nil || owner != bindings.Stack && (owner != "" || bindings.Owners[id] != uid) {
		return nil, &deploymentplan.Blocker{ID: "deployment.bootstrap.account-manager", Resource: id, Cause: "runtime Secret ownership is unverified or changed", Next: "verify the exact environment Secret UID before bootstrap credential retirement"}
	}
	seal, err := backend.accountBootstrapSeal(ctx, protection)
	if err != nil {
		return nil, &deploymentplan.Blocker{ID: "deployment.bootstrap.account-manager", Resource: id, Cause: "original database bootstrap seal cannot be verified", Next: "complete Account Manager bootstrap against the original database and verify its permanent seal before full upgrade"}
	}
	definition, changed, err := accountManagerBootstrapRetirement(secret, bindings.Stack, seal, protection.Worker.Source.SystemIdentifier)
	if err != nil {
		return nil, err
	}
	if changed {
		for _, d := range definitions {
			if deploymentplan.ID(d.Object) == id {
				return nil, errors.New("bootstrap runtime Secret has more than one lifecycle owner")
			}
		}
		definitions = append(definitions, definition)
	}
	deploymentID := "Deployment/" + bindings.Stack + "-account-manager/account-manager"
	found := false
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) != deploymentID {
			continue
		}
		found = true
		template := deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])
		meta := deploymentplan.Map(template["metadata"])
		if meta == nil {
			meta = deploymentplan.Object{}
			template["metadata"] = meta
		}
		annotations := deploymentplan.Map(meta["annotations"])
		if annotations == nil {
			annotations = deploymentplan.Object{}
			meta["annotations"] = annotations
		}
		annotations["rtk.cloud/bootstrap-credentials"] = "retired-v1"
		pod := deploymentplan.Map(template["spec"])
		for _, value := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(value)
			var env []any
			for _, setting := range certIssuerObjectList(container["env"]) {
				if strings.HasPrefix(deploymentplan.String(deploymentplan.Map(setting)["name"]), "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_") {
					continue
				}
				env = append(env, setting)
			}
			if len(env) > 0 {
				container["env"] = env
			} else {
				delete(container, "env")
			}
		}
	}
	if !found {
		return nil, errors.New("Account Manager bootstrap retirement requires its maintained Deployment target")
	}
	return definitions, nil
}

func (c *compiledEnvironmentDeployment) verifyAccountManagerBootstrapSeal(ctx context.Context) error {
	if c.Plan.Operation == "create" {
		return nil
	}
	protection, err := readPostgresBackupDeployment(filepath.Join(c.Configuration.EnvironmentRoot, "postgres-backup.json"), c.Configuration.Environment)
	if err != nil {
		return errors.New("deployment.bootstrap.account-manager: original source profile unavailable")
	}
	_, err = c.Backend.accountBootstrapSeal(ctx, protection)
	return err
}

func accountManagerBootstrapKeysAbsent(secret deploymentplan.Object) bool {
	for key := range deploymentplan.Map(secret["data"]) {
		if strings.HasPrefix(key, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_") {
			return false
		}
	}
	return true
}

// A retired one-time seed is optional only after actual source/Secret checks.
// Missing kubeconfig, bindings or registry evidence keeps it required; a local
// success marker never authorizes either skipping verification or regeneration.
func accountBootstrapCredentialsRetired(store secretStore) bool {
	workspace, err := workspaceRoot()
	if err != nil {
		return false
	}
	cfg, err := resolveDeploymentConfig(workspace, store.Environment, "")
	if err != nil {
		return false
	}
	profile, err := readPostgresBackupDeployment(filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), store.Environment)
	if err != nil {
		return false
	}
	raw, err := readSensitiveFile(store.KubeconfigPath(), "selected environment kubeconfig")
	if err != nil {
		return false
	}
	backend, err := newEnvironmentKubernetesBackend(raw, store.Environment)
	if err != nil {
		return false
	}
	backend.Store = store // preserve the explicitly selected environment store
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	secret, err := backend.Read(ctx, "Secret/video-cloud-"+store.Environment+"-account-manager/account-manager-runtime")
	if err != nil || secret == nil || !accountManagerBootstrapKeysAbsent(secret) {
		return false
	}
	meta := deploymentplan.Map(secret["metadata"])
	if deploymentplan.String(meta["uid"]) == "" || meta["deletionTimestamp"] != nil || deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] != "video-cloud-"+store.Environment {
		return false
	}
	seal, err := backend.accountBootstrapSeal(ctx, profile)
	if err != nil {
		return false
	}
	_, changed, err := accountManagerBootstrapRetirement(secret, "video-cloud-"+store.Environment, seal, profile.Worker.Source.SystemIdentifier)
	return err == nil && !changed
}

// Retire the local one-time copy only after an accepted target has proved the
// actual original seal and absence of both runtime fields. The marker prevents
// accidental regeneration; later verification still probes the real database.
func retireLocalAccountBootstrapSeed(ctx context.Context, c *compiledEnvironmentDeployment) error {
	profile, err := readPostgresBackupDeployment(filepath.Join(c.Configuration.EnvironmentRoot, "postgres-backup.json"), c.Configuration.Environment)
	if err != nil {
		return errors.New("Account Manager bootstrap source profile unavailable")
	}
	seal, err := c.Backend.accountBootstrapSeal(ctx, profile)
	if err != nil {
		return err
	}
	id := "Secret/" + c.Plan.Stack + "-account-manager/account-manager-runtime"
	secret, err := c.Backend.Read(ctx, id)
	if err != nil || secret == nil || !accountManagerBootstrapKeysAbsent(secret) {
		return errors.New("Account Manager runtime credential retirement is unverified")
	}
	meta := deploymentplan.Map(secret["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if uid == "" || deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] != c.Plan.Stack {
		return errors.New("Account Manager runtime Secret owner unverified")
	}
	expectedUID := ""
	for _, resource := range c.Plan.Resources {
		if resource.ID == id {
			expectedUID = resource.UID
			break
		}
	}
	if expectedUID == "" || uid != expectedUID {
		return errors.New("accepted Account Manager Secret owner changed before local credential retirement")
	}
	if _, changed, err := accountManagerBootstrapRetirement(secret, c.Plan.Stack, seal, profile.Worker.Source.SystemIdentifier); err != nil || changed {
		return errors.New("Account Manager runtime source differs from the sealed original database")
	}
	deploymentID := "Deployment/" + c.Plan.Stack + "-account-manager/account-manager"
	deployment, err := c.Backend.Read(ctx, deploymentID)
	if err != nil || deployment == nil {
		return errors.New("Account Manager restart after credential retirement is unverified")
	}
	deploymentUID := ""
	for _, resource := range c.Plan.Resources {
		if resource.ID == deploymentID {
			deploymentUID = resource.UID
			break
		}
	}
	metaDeployment := deploymentplan.Map(deployment["metadata"])
	specDeployment, statusDeployment := deploymentplan.Map(deployment["spec"]), deploymentplan.Map(deployment["status"])
	replicas, _ := specDeployment["replicas"].(float64)
	templateMetadata := deploymentplan.Map(deploymentplan.Map(specDeployment["template"])["metadata"])
	if deploymentUID == "" || metaDeployment["uid"] != deploymentUID || replicas < 1 || statusDeployment["observedGeneration"] != metaDeployment["generation"] || statusDeployment["updatedReplicas"] != replicas || statusDeployment["availableReplicas"] != replicas || deploymentplan.Map(templateMetadata["annotations"])["rtk.cloud/bootstrap-credentials"] != "retired-v1" {
		return errors.New("accepted Account Manager owner has not completed its credential-free restart")
	}
	marker := map[string]any{"schema": "rtk-cloud.account-bootstrap-retired/v1", "environment": c.Configuration.Environment, "stack": c.Plan.Stack, "system_identifier": seal.SystemIdentifier, "secret_uid": uid, "plan_sha256": c.Plan.SHA256()}
	markerJSON, err := json.Marshal(marker)
	if err != nil {
		return errors.New("Account Manager bootstrap retirement could not be encoded")
	}
	if err := c.Store.write("deployment/account-bootstrap-retired.json", append(markerJSON, '\n'), true); err != nil {
		return errors.New("Account Manager bootstrap retirement marker could not be persisted")
	}
	path, err := c.Store.safePath("runtime/platform-admin")
	if err != nil {
		return err
	}
	original, err := readStorageEvidenceFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("original local bootstrap seed unavailable")
	}
	if c.BootstrapSeedSHA256 == "" || deploymentplan.Digest(strings.TrimSpace(string(original))) != c.BootstrapSeedSHA256 {
		return errors.New("local bootstrap seed differs from approved inputs; preserve concurrent credentials")
	}
	expected := sha256.Sum256(original)
	current, err := readStorageEvidenceFile(path)
	if err != nil || sha256.Sum256(current) != expected {
		return errors.New("local bootstrap seed changed; preserve concurrent credentials")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("local bootstrap seed removal incomplete")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("local bootstrap credential absence unverified")
	}
	return nil
}
