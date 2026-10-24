package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This intentionally narrow recovery is valid only before any Kubernetes
// bootstrap/runtime mutation. It preserves the failed journal and all allocated
// identities, separately qualifies live state, then starts a new journal.
// It cannot resume an uncertain provider request or a partial PKI ceremony.
type environmentCreateProviderRecoveryPlan struct {
	Schema                string                                      `json:"schema"`
	RecoveryProfile       string                                      `json:"recovery_profile,omitempty"`
	OriginalPlanSHA256    string                                      `json:"original_plan_sha256"`
	OriginalJournal       string                                      `json:"original_journal"`
	OriginalJournalSHA256 string                                      `json:"original_journal_sha256"`
	OriginalOwnerSHA256   string                                      `json:"original_owner_sha256"`
	CheckerSHA256         string                                      `json:"checker_sha256"`
	KubeconfigSHA256      string                                      `json:"kubeconfig_sha256"`
	Plan                  environmentCreatePlan                       `json:"plan"`
	Receipts              map[string]environmentCreateProviderReceipt `json:"receipts"`
}

type environmentCreateProviderRecoveryArtifact struct {
	Schema        string                                `json:"schema"`
	Plan          environmentCreateProviderRecoveryPlan `json:"plan"`
	Qualification deploymentplan.Qualification          `json:"qualification"`
	QualifiedAt   time.Time                             `json:"qualified_at"`
	ValidUntil    time.Time                             `json:"valid_until"`
}

func createProviderRecoveryBlock(cause string) error {
	return environmentCreateStateBlocker("deployment.create.recovery", cause)
}

func validateProviderOnlyCreateJournal(j environmentCreateJournal, p environmentCreatePlan) (map[string]environmentCreateProviderReceipt, string, error) {
	if j.Schema != environmentCreateJournalSchema || j.Environment != "dev" || j.Environment != p.Environment || j.Stack != p.Stack || !j.CompletedAt.IsZero() || len(j.KubernetesLeases) != 0 || j.OriginalJournal != "" || len(j.Entries) != len(p.Provider.Resources)+2 {
		return nil, "", createProviderRecoveryBlock("only an unfinished original dev provider-only execution with no Kubernetes leases can use this recovery")
	}
	wanted := map[string]environmentCreateProviderResource{}
	for _, r := range p.Provider.Resources {
		if _, duplicate := wanted[r.Key]; duplicate {
			return nil, "", createProviderRecoveryBlock("duplicate provider intent")
		}
		wanted[r.Key] = r
	}
	seen := map[string]bool{}
	receipts := map[string]environmentCreateProviderReceipt{}
	kubeSHA := ""
	for _, entry := range j.Entries {
		if seen[entry.Key] || !environmentPreflightDigest.MatchString(entry.IntentSHA256) || entry.StartedAt.Before(j.AdmittedAt) {
			return nil, "", createProviderRecoveryBlock("duplicate or invalid original stage evidence")
		}
		seen[entry.Key] = true
		if entry.Key == "activate-storage" {
			if entry.Phase != "storage" || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return nil, "", createProviderRecoveryBlock("storage has partial or unknown writes; this recovery cannot replay it")
			}
			continue
		}
		if entry.State != "completed" || entry.Phase != "provider" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) {
			return nil, "", createProviderRecoveryBlock("provider allocation is incomplete or uncertain; automatic replay is prohibited")
		}
		if entry.Key == "provider-kube-access" {
			var proof struct {
				ClusterID           string `json:"cluster_id"`
				KubeconfigSHA256    string `json:"kubeconfig_sha256"`
				ProviderStateSHA256 string `json:"provider_state_sha256"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &proof) != nil {
				return nil, "", createProviderRecoveryBlock("original cluster-access receipt is unreadable")
			}
			if !environmentPreflightDigest.MatchString(proof.KubeconfigSHA256) || !environmentPreflightDigest.MatchString(proof.ProviderStateSHA256) || proof.ClusterID == "" {
				return nil, "", createProviderRecoveryBlock("original Kubernetes identity lacks a verified digest")
			}
			kubeSHA = proof.KubeconfigSHA256
			continue
		}
		r, ok := wanted[entry.Key]
		var receipt environmentCreateProviderReceipt
		if !ok || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Key != r.Key || receipt.Kind != r.Kind || receipt.Name != r.Name || receipt.Region != r.Region || receipt.NativeID == "" {
			return nil, "", createProviderRecoveryBlock("recorded allocation differs from the frozen provider request")
		}
		receipts[entry.Key] = receipt
	}
	if len(receipts) != len(wanted) || !seen["activate-storage"] || !seen["provider-kube-access"] || kubeSHA == "" {
		return nil, "", createProviderRecoveryBlock("complete original provider and Kubernetes-access receipts are required")
	}
	var proof map[string]string
	for _, entry := range j.Entries {
		if entry.Key == "provider-kube-access" {
			_ = json.Unmarshal(entry.Receipt, &proof)
		}
	}
	if receipts["lke"].NativeID != proof["cluster_id"] {
		return nil, "", createProviderRecoveryBlock("Kubernetes identity belongs to another cluster allocation")
	}
	return receipts, kubeSHA, nil
}

func loadStoppedProviderCreate(cfg deploymentConfig, store secretStore, originalSHA string, afterCanary bool) (*environmentCreateExecution, environmentCreatePlan, error) {
	var plan environmentCreatePlan
	if cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || !environmentPreflightDigest.MatchString(originalSHA) {
		return nil, plan, createProviderRecoveryBlock("this profile qualifies only the explicitly selected original dev create plan")
	}
	fence := filepath.Join(store.Root, "deployment", ".execution-active")
	if err := validateEnvironmentPreflightDirectories(store, fence); err != nil {
		return nil, plan, err
	}
	file, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return nil, plan, createProviderRecoveryBlock("original executor is running or its process lock cannot be verified")
	}
	e := &environmentCreateExecution{Store: store, running: file}
	ok := false
	defer func() {
		if !ok {
			_ = e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return nil, plan, err
	}
	e.runningInfo, err = file.Stat()
	if err != nil {
		return nil, plan, err
	}
	if err := readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &e.owner); err != nil {
		return nil, plan, err
	}
	if e.owner.Schema != environmentCreateOwnerSchema || e.owner.Environment != cfg.Environment || e.owner.Stack != cfg.Values["CLOUD_STACK_NAME"] || e.owner.PlanSHA256 != originalSHA || !strings.HasPrefix(e.owner.Journal, "deployment/create/executions/"+originalSHA+"/execution-") || filepath.Clean(e.owner.Journal) != e.owner.Journal {
		return nil, plan, createProviderRecoveryBlock("original owner does not name the selected canonical execution")
	}
	e.JournalPath, err = privateDeploymentStatePath(cfg.Environment, e.owner.Journal)
	if err != nil {
		return nil, plan, err
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if err := readEnvironmentPreflightJSON(e.JournalPath, &e.Journal); err != nil {
		return nil, plan, err
	}
	if e.Journal.OwnerToken != e.owner.OwnerToken || e.Journal.PlanSHA256 != originalSHA {
		return nil, plan, createProviderRecoveryBlock("journal and stopped owner differ")
	}
	if afterCanary {
		var artifact environmentCreateProviderRecoveryArtifact
		if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/recovery", originalSHA, "qualification.json"), &artifact); err != nil {
			return nil, plan, err
		}
		if artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || artifact.Plan.RecoveryProfile != "" || deploymentplan.Digest(artifact.Plan) != originalSHA || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 || e.Journal.OriginalJournal != artifact.Plan.OriginalJournal || e.Journal.OriginalJournalSHA256 != artifact.Plan.OriginalJournalSHA256 || e.Journal.ProviderMaterialPlanSHA256 != artifact.Plan.OriginalPlanSHA256 {
			return nil, plan, createProviderRecoveryBlock("storage recovery lacks the original admitted provider-only recovery provenance")
		}
		plan = artifact.Plan.Plan
		ancestorPath, err := privateDeploymentStatePath(cfg.Environment, e.Journal.OriginalJournal)
		if err != nil {
			return nil, plan, err
		}
		var ancestor environmentCreateJournal
		if err := readEnvironmentPreflightJSON(ancestorPath, &ancestor); err != nil {
			return nil, plan, err
		}
		if deploymentplan.Digest(ancestor) != e.Journal.OriginalJournalSHA256 || ancestor.PlanSHA256 != e.Journal.ProviderMaterialPlanSHA256 {
			return nil, plan, createProviderRecoveryBlock("original provider ancestry changed")
		}
		for index := 0; index < 6; index++ {
			if len(e.Journal.Entries) <= index || len(ancestor.Entries) <= index || deploymentplan.Digest(e.Journal.Entries[index]) != deploymentplan.Digest(ancestor.Entries[index]) {
				return nil, plan, createProviderRecoveryBlock("retained provider stage differs from its original receipt")
			}
		}
		if _, _, err := validateProviderStorageRecoveryJournal(e.Journal, plan, cfg, ancestor.AdmittedAt); err != nil {
			return nil, plan, err
		}
		if err := e.Check(context.Background()); err != nil {
			return nil, plan, err
		}
		ok = true
		return e, plan, nil
	}
	var active environmentPreflightActive
	if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/preflight/active.json"), &active); err != nil {
		return nil, plan, err
	}
	if active.PlanSHA256 != originalSHA || !environmentPreflightArtifactName.MatchString(active.Artifact) {
		return nil, plan, createProviderRecoveryBlock("original qualification reference is unavailable")
	}
	var artifact environmentCreateArtifact
	path := filepath.Join(store.Root, "deployment/create/preflight", filepath.FromSlash(active.Artifact), "artifact.json")
	if err := readEnvironmentPreflightJSON(path, &artifact); err != nil {
		return nil, plan, err
	}
	// Expired qualification is provenance, never new execution admission.
	if artifact.PlanSHA256 != originalSHA || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || decodeEnvironmentJSON(artifact.Plan, &plan) != nil {
		return nil, plan, createProviderRecoveryBlock("original frozen plan or artifact changed")
	}
	sha, err := environmentCreatePlanSHA256(artifact.Plan)
	if err != nil || sha != originalSHA || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 {
		return nil, plan, createProviderRecoveryBlock("original request lacks complete authentic qualification provenance")
	}
	if _, _, err := validateProviderOnlyCreateJournal(e.Journal, plan); err != nil {
		return nil, plan, err
	}
	if err := e.Check(context.Background()); err != nil {
		return nil, plan, err
	}
	ok = true
	return e, plan, nil
}

func verifyProviderRecoveryBaseline(ctx context.Context, cfg deploymentConfig, store secretStore, p environmentCreateProviderRecoveryPlan, operator map[string]string) error {
	for _, r := range p.Plan.Provider.Resources {
		previous := p.Receipts[r.Key]
		actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, r, previous, operator)
		if err != nil || !ready || previous.PublicIP != "" && previous.PublicIP != actual.PublicIP || previous.PrivateIP != "" && previous.PrivateIP != actual.PrivateIP {
			return createProviderRecoveryBlock("an original allocation is unavailable, changed, or no longer ready")
		}
	}
	path, err := privateDeploymentStatePath(cfg.Environment, "kube/kubeconfig.yaml")
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || environmentPreflightBytesSHA256(raw) != p.KubeconfigSHA256 {
		return createProviderRecoveryBlock("the original canonical Kubernetes identity changed")
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return err
	}
	body, err := backend.command(ctx, nil, "get", "namespaces", "-o", "json")
	if err != nil {
		return createProviderRecoveryBlock("original Kubernetes namespace inventory is unavailable")
	}
	var list struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Items      []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &list) != nil || list.APIVersion != "v1" || (list.Kind != "NamespaceList" && list.Kind != "List") || list.Items == nil {
		return createProviderRecoveryBlock("namespace inventory is invalid")
	}
	for _, ns := range list.Items {
		if ns.Kind != "Namespace" || ns.Metadata.Name == "" {
			return createProviderRecoveryBlock("namespace inventory contains a non-namespace object")
		}
		if ns.Metadata.Name == p.Plan.Stack || strings.HasPrefix(ns.Metadata.Name, p.Plan.Stack+"-") {
			return createProviderRecoveryBlock("Kubernetes bootstrap has started; provider-only recovery cannot replay it")
		}
	}
	// Complete inventory fences unexpected stack owners and proves remaining
	// workload allocations fit the confirmed account limit. No provider replay.
	wanted := map[string]bool{}
	for _, r := range p.Plan.Provider.Resources {
		wanted[r.Name] = true
	}
	current := 0
	for _, path := range []string{"/lke/clusters", "/linode/instances", "/networking/firewalls", "/vpcs", "/object-storage/buckets", "/volumes", "/nodebalancers"} {
		items, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], path)
		if err != nil {
			return createProviderRecoveryBlock("complete recovery provider inventory is unavailable")
		}
		if path == "/linode/instances" || path == "/volumes" || path == "/nodebalancers" {
			current += len(items)
		}
		for _, raw := range items {
			var item struct {
				Label string   `json:"label"`
				Tags  []string `json:"tags"`
			}
			if json.Unmarshal(raw, &item) != nil {
				return createProviderRecoveryBlock("recovery inventory contains an invalid owner")
			}
			if (strings.HasPrefix(item.Label, p.Plan.Stack+"-") || createProviderContains(item.Tags, p.Plan.Stack)) && !wanted[item.Label] {
				return createProviderRecoveryBlock("an unrecorded stack allocation exists; do not adopt it")
			}
		}
	}
	account, err := readLKEAccountState(cfg.Environment, true)
	if err != nil {
		return err
	}
	if account["LKE_ACTIVE_SERVICE_LIMIT"] != "unlimited" {
		limit, err := strconv.Atoi(account["LKE_ACTIVE_SERVICE_LIMIT"])
		if err != nil || limit <= 0 || current+p.Plan.Provider.WorkloadAllocations > limit {
			return createProviderRecoveryBlock("confirmed account capacity cannot cover the remaining workload allocations")
		}
	}
	return nil
}

func qualifyProviderRecoveryStorage(ctx context.Context, cfg deploymentConfig, operator map[string]string) error {
	if err := qualifyEnvironmentCreateStorage(ctx, cfg, operator); err != nil {
		return err
	}
	checker := defaultDeploymentCredentialChecker().withCheckContext(ctx)
	checker.out = io.Discard
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		bucket, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], target)
		if err != nil {
			return createProviderRecoveryBlock("recorded storage allocation is unavailable")
		}
		endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			return err
		}
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		s3 := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint, accessKey: access, secretKey: secret}
		if err := checker.validateStoragePrivacy(s3); err != nil {
			return createProviderRecoveryBlock("configured storage key lacks genuine private ACL/policy readback; repair its exact bucket grant before recovery")
		}
	}
	return nil
}

func qualifyProviderRecoveryBootstrap(ctx context.Context, cfg deploymentConfig, plan environmentCreateBootstrapPlan, store secretStore, operator map[string]string) error {
	for _, relative := range []string{"inventory.json", "runtime/platform-admin"} {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return createProviderRecoveryBlock("initialized runtime requires a different recovery profile")
		}
	}
	path, err := store.safePath("runtime")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) > 0 {
		return createProviderRecoveryBlock("original runtime state has already been initialized")
	}
	return qualifyEnvironmentCreateBootstrap(ctx, cfg, plan, store, operator)
}

func runEnvironmentCreateProviderRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-provider", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	original := fs.String("original-plan-sha256", "", "failed original create plan")
	review := fs.String("review-sha256", "", "fresh recovery plan from qualification")
	confirm := fs.String("confirm", "", "exact stack for execution")
	execute := fs.Bool("execute", false, "execute only a separately qualified provider-only recovery")
	establishKeys := fs.Bool("establish-storage-keys", false, "establish new bucket-scoped keys, preserve original values and keep original keys")
	afterCanary := fs.Bool("after-storage-canary", false, "qualify a stopped provider recovery with only completed and deleted storage canaries")
	reportPath := fs.String("report", "", "private canonical recovery report")
	timeout := fs.Duration("timeout", 90*time.Minute, "complete recovery timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*original) || *timeout <= 0 {
		return createProviderRecoveryBlock("selected dev and original plan digest are required")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	if *reportPath != "" {
		if err := validateEnvironmentDeploymentReportPath(*env, *reportPath); err != nil {
			return err
		}
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-provider"}, args...)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	old, plan, err := loadStoppedProviderCreate(cfg, store, *original, *afterCanary)
	if err != nil {
		return err
	}
	defer old.Close()
	validationJournal := old.Journal
	if *afterCanary {
		validationJournal.OriginalJournal, validationJournal.OriginalJournalSHA256, validationJournal.ProviderMaterialPlanSHA256 = "", "", ""
		validationJournal.Entries = append([]environmentCreateJournalEntry(nil), old.Journal.Entries[:7]...)
		validationJournal.AdmittedAt = old.Journal.Entries[0].StartedAt
	}
	receipts, kubeSHA, err := validateProviderOnlyCreateJournal(validationJournal, plan)
	if err != nil {
		return err
	}
	if *establishKeys {
		if *execute || *afterCanary || *confirm != cfg.Values["CLOUD_STACK_NAME"] || *review != "" {
			return createProviderRecoveryBlock("new storage keys require exact stack confirmation as a separate preparation operation")
		}
		return establishProviderRecoveryStorageKeys(ctx, cfg, store, operator, old, plan, *original)
	}
	checkerSHA, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	plan.ConfigurationSHA256, plan.OperatorSHA256 = deploymentplan.Digest(cfg), deploymentplan.Digest(operator)
	p := environmentCreateProviderRecoveryPlan{Schema: "rtk-cloud.provider-create-recovery/v1", OriginalPlanSHA256: *original, OriginalJournal: old.owner.Journal, OriginalJournalSHA256: deploymentplan.Digest(old.Journal), OriginalOwnerSHA256: deploymentplan.Digest(old.owner), CheckerSHA256: checkerSHA, KubeconfigSHA256: kubeSHA, Plan: plan, Receipts: receipts}
	if *afterCanary {
		p.RecoveryProfile = "completed-storage-canary"
	}
	sha := deploymentplan.Digest(p)
	directory, err := prepareEnvironmentCreateDirectories(store, filepath.Join("deployment", "create", "recovery", sha))
	if err != nil {
		return err
	}
	artifactPath := filepath.Join(directory, "qualification.json")
	if !*execute {
		q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "NO-GO"}
		checks := []struct {
			id  string
			run func() error
		}{
			{"deployment.recovery.target", func() error { return verifyProviderRecoveryBaseline(ctx, cfg, store, p, operator) }},
			{"deployment.recovery.release", func() error { return verifyEnvironmentRelease(ctx, cfg, plan.Release) }},
			{"deployment.recovery.storage", func() error {
				if err := qualifyProviderRecoveryStorage(ctx, cfg, operator); err != nil {
					return err
				}
				if *afterCanary {
					return verifyProviderRecoveryCanaryAbsence(ctx, cfg, old.Journal, operator)
				}
				return nil
			}},
			{"deployment.recovery.bootstrap", func() error { return qualifyProviderRecoveryBootstrap(ctx, cfg, plan.Bootstrap, store, operator) }},
			{"deployment.recovery.network", func() error { return qualifyEnvironmentCreateDNS(ctx, cfg, plan.DNS, operator) }},
			{"deployment.recovery.inputs", func() error {
				fresh, err := resolveDeploymentConfig(workspace, *env, "")
				if err != nil {
					return err
				}
				_, current, err := readEnvironmentCreateOperator(fresh)
				if err != nil {
					return err
				}
				if deploymentplan.Digest(fresh) != plan.ConfigurationSHA256 || deploymentplan.Digest(current) != plan.OperatorSHA256 {
					return createProviderRecoveryBlock("recovery inputs changed")
				}
				currentChecker, err := environmentPreflightCheckerSHA256()
				if err != nil || currentChecker != checkerSHA {
					return createProviderRecoveryBlock("recovery checker changed during qualification")
				}
				if err := verifyEnvironmentImageSources(ctx, fresh, plan.Release, environmentCIImageCommand(fresh)); err != nil {
					return err
				}
				return old.Check(ctx)
			}},
		}
		for _, check := range checks {
			err := runEnvironmentProgress(ctx, os.Stdout, check.id, func(context.Context) error { return check.run() })
			gate := deploymentplan.GateResult{ID: check.id, Required: true, Status: "PASS", EvidenceSHA256: sha}
			if err != nil {
				gate.Status = "BLOCKED"
				gate.Cause = err.Error()
			}
			q.Gates = append(q.Gates, gate)
		}
		pass := true
		for _, g := range q.Gates {
			pass = pass && g.Status == "PASS"
		}
		if pass {
			q.Decision = "GO"
		}
		now := time.Now().UTC()
		artifact := environmentCreateProviderRecoveryArtifact{Schema: "rtk-cloud.provider-create-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
		if pass {
			proofRelative := filepath.Join("deployment", "create", "recovery", sha, "qualifications", now.Format("20060102T150405.000000000Z")+".json")
			if err := writeEnvironmentCreateDerivedPrivateFile(store, proofRelative, mustProviderRecoveryJSON(artifact)); err != nil {
				return err
			}
			// Renew only by completing every live gate again. Preserve each
			// immutable proof before replacing the current admission artifact.
			if err := store.write(strings.TrimPrefix(artifactPath, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(artifact), true); err != nil {
				return err
			}
		}
		if *reportPath != "" {
			if err := store.write(strings.TrimPrefix(*reportPath, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(artifact), true); err != nil {
				return err
			}
		}
		fmt.Fprintf(os.Stdout, "%s environment=dev scope=provider-only-create-recovery review-sha256=%s\n", q.Decision, sha)
		if !pass {
			return exitCode(1)
		}
		return nil
	}
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] || *review != sha {
		return createProviderRecoveryBlock("execution requires exact stack and freshly reviewed recovery digest")
	}
	var artifact environmentCreateProviderRecoveryArtifact
	if err := readEnvironmentPreflightJSON(artifactPath, &artifact); err != nil {
		return err
	}
	if artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != sha || artifact.Qualification.PlanSHA256 != sha || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 || !artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(time.Hour)) || artifact.QualifiedAt.After(time.Now().UTC()) || !time.Now().Before(artifact.ValidUntil) {
		return createProviderRecoveryBlock("fresh complete recovery qualification is required")
	}
	required := []string{"deployment.recovery.target", "deployment.recovery.release", "deployment.recovery.storage", "deployment.recovery.bootstrap", "deployment.recovery.network", "deployment.recovery.inputs"}
	for index, g := range artifact.Qualification.Gates {
		if !g.Required || g.Status != "PASS" || g.ID != required[index] || g.EvidenceSHA256 != sha {
			return createProviderRecoveryBlock("recovery gate is not qualified")
		}
	}
	if err := verifyProviderRecoveryBaseline(ctx, cfg, store, p, operator); err != nil {
		return err
	}
	if err := qualifyProviderRecoveryStorage(ctx, cfg, operator); err != nil {
		return err
	}
	if *afterCanary {
		if err := verifyProviderRecoveryCanaryAbsence(ctx, cfg, old.Journal, operator); err != nil {
			return err
		}
	}
	if err := qualifyProviderRecoveryBootstrap(ctx, cfg, plan.Bootstrap, store, operator); err != nil {
		return err
	}
	if err := verifyEnvironmentImageSources(ctx, cfg, plan.Release, environmentCIImageCommand(cfg)); err != nil {
		return err
	}
	currentChecker, err := environmentPreflightCheckerSHA256()
	if err != nil || currentChecker != checkerSHA {
		return createProviderRecoveryBlock("recovery checker changed before execution")
	}
	e, err := forkProviderCreateRecovery(old, p, sha, deploymentplan.Digest(artifact))
	if err != nil {
		return err
	}
	defer e.Close()
	for _, r := range p.Receipts {
		if err := recordEnvironmentCreateProviderOwner(ctx, e, r); err != nil {
			return err
		}
	}
	err = executeEnvironmentCreateAfterProvider(ctx, cfg, &plan, p.Receipts, store, operator, e, os.Stdout)
	if err == nil {
		err = e.CompleteAndRelease(time.Now().UTC())
	}
	decision := "NO-GO"
	if err == nil {
		decision = "GO"
	}
	if *reportPath != "" {
		result := map[string]any{"schema": "rtk-cloud.provider-create-recovery-result/v1", "environment": "dev", "decision": decision, "review_sha256": sha, "original_plan_sha256": *original, "journal": e.JournalPath, "deployment_performed": true, "completed": err == nil}
		if err != nil {
			result["cause"] = err.Error()
		}
		if writeErr := store.write(strings.TrimPrefix(*reportPath, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(result), true); writeErr != nil {
			return writeErr
		}
	}
	fmt.Fprintf(os.Stdout, "%s environment=dev scope=provider-only-create-recovery completed=%t\n", decision, err == nil)
	return err
}

func mustProviderRecoveryJSON(v any) []byte { body, _ := json.Marshal(v); return body }

func forkProviderCreateRecovery(old *environmentCreateExecution, p environmentCreateProviderRecoveryPlan, sha, artifactSHA string) (*environmentCreateExecution, error) {
	if err := old.Check(context.Background()); err != nil {
		return nil, err
	}
	if deploymentplan.Digest(old.Journal) != p.OriginalJournalSHA256 || deploymentplan.Digest(old.owner) != p.OriginalOwnerSHA256 {
		return nil, createProviderRecoveryBlock("original evidence changed after recovery review")
	}
	parent, err := prepareEnvironmentCreateDirectories(old.Store, filepath.Join("deployment", "create", "executions", sha))
	if err != nil {
		return nil, err
	}
	prior, err := os.ReadDir(parent)
	if err != nil || len(prior) != 0 {
		return nil, createProviderRecoveryBlock("recovery attempt already exists; never automatically replay it")
	}
	directory, err := os.MkdirTemp(parent, "execution-")
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	entries := []environmentCreateJournalEntry{}
	for _, entry := range old.Journal.Entries {
		if entry.State == "completed" && entry.Phase == "provider" {
			entries = append(entries, entry)
		}
	}
	now := time.Now().UTC()
	token := fmt.Sprintf("%x", nonce[:])
	e := &environmentCreateExecution{Store: old.Store, Directory: directory, JournalPath: filepath.Join(directory, "journal.json")}
	relative, _ := filepath.Rel(e.Store.Root, e.JournalPath)
	e.owner = environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: old.owner.Stack, PlanSHA256: sha, OwnerToken: token, Journal: filepath.ToSlash(relative), StartedAt: now}
	e.Journal = environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: old.owner.Stack, PlanSHA256: sha, ArtifactSHA256: artifactSHA, OwnerToken: token, AdmittedAt: now, Entries: entries, OriginalJournal: p.OriginalJournal, OriginalJournalSHA256: p.OriginalJournalSHA256, ProviderMaterialPlanSHA256: environmentCreateHostMaterialPlan(old)}
	if err := e.save(); err != nil {
		return nil, err
	}
	fence := filepath.Join(e.Store.Root, "deployment/.execution-active")
	archive := filepath.Join(old.Directory, "owner-recovery-handoff")
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		return nil, createProviderRecoveryBlock("original handoff archive already exists")
	}
	if err := os.Mkdir(archive, 0700); err != nil {
		return nil, err
	}
	// Preserve exact original owner bytes, keep the directory and OS lock
	// occupied continuously, then atomically publish the new qualified owner.
	for _, name := range []string{"owner.json", "running.lock"} {
		raw, err := readBoundedEnvironmentInput(filepath.Join(fence, name), true, name == "running.lock")
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(e.Store.Root, filepath.Join(archive, name))
		if err := e.Store.write(rel, raw, false); err != nil {
			return nil, err
		}
	}
	if err := old.Check(context.Background()); err != nil {
		return nil, err
	}
	if err := e.Store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(e.owner), true); err != nil {
		return nil, err
	}
	e.ownerInfo, e.runningInfo, e.running = old.ownerInfo, old.runningInfo, old.running
	old.running = nil
	if err := e.Check(context.Background()); err != nil {
		_ = e.Close()
		return nil, err
	}
	for _, d := range []string{directory, archive, old.Directory, filepath.Dir(fence), fence} {
		if err := syncEnvironmentPreflightDirectory(d); err != nil {
			_ = e.Close()
			return nil, err
		}
	}
	return e, nil
}
