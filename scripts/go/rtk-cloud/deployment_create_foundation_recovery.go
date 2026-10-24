package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createFoundationRecoveryPlan struct {
	Schema               string                                      `json:"schema"`
	ParentSHA            string                                      `json:"parent_plan_sha256"`
	ParentOwnerSHA       string                                      `json:"parent_owner_sha256"`
	ParentJournalSHA     string                                      `json:"parent_journal_sha256"`
	FoundationSHA        string                                      `json:"foundation_plan_sha256"`
	FoundationJournalSHA string                                      `json:"foundation_journal_sha256"`
	CheckerSHA           string                                      `json:"checker_sha256"`
	KubeconfigSHA        string                                      `json:"kubeconfig_sha256"`
	CheckpointPlanSHA    string                                      `json:"checkpoint_plan_sha256,omitempty"`
	CheckpointJournalSHA string                                      `json:"checkpoint_journal_sha256,omitempty"`
	Materials            map[string]string                           `json:"material_sha256"`
	Plan                 environmentCreatePlan                       `json:"plan"`
	Receipts             map[string]environmentCreateProviderReceipt `json:"provider_receipts"`
}
type createFoundationRecoveryArtifact struct {
	Schema        string                       `json:"schema"`
	Plan          createFoundationRecoveryPlan `json:"plan"`
	Qualification deploymentplan.Qualification `json:"qualification"`
	QualifiedAt   time.Time                    `json:"qualified_at"`
	ValidUntil    time.Time                    `json:"valid_until"`
}

func loadStoppedFoundationCreate(cfg deploymentConfig, store secretStore, sha string) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, *deploymentplan.Plan, deploymentplan.Journal, error) {
	var artifact environmentCreateProviderRecoveryArtifact
	var child deploymentplan.Plan
	var childJournal deploymentplan.Journal
	bad := func(cause string) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, *deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, artifact, nil, childJournal, createProviderRecoveryBlock(cause)
	}
	if cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || !environmentPreflightDigest.MatchString(sha) {
		return bad("only the selected stopped dev foundation can use this profile")
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	if err := validateEnvironmentPreflightDirectories(store, fence); err != nil {
		return bad("canonical original execution fence unavailable")
	}
	file, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return bad("original executor is running or its process lock is unverifiable")
	}
	e := &environmentCreateExecution{Store: store, running: file}
	loaded := false
	defer func() {
		if !loaded {
			e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return bad("original fence identity unavailable")
	}
	e.runningInfo, err = file.Stat()
	if err != nil {
		return bad("original process lock unavailable")
	}
	if readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &e.owner) != nil || e.owner.Schema != environmentCreateOwnerSchema || e.owner.Environment != "dev" || e.owner.Stack != cfg.Values["CLOUD_STACK_NAME"] || e.owner.PlanSHA256 != sha || !strings.HasPrefix(e.owner.Journal, "deployment/create/executions/"+sha+"/execution-") || filepath.Clean(e.owner.Journal) != e.owner.Journal {
		return bad("original selected foundation owner differs")
	}
	e.JournalPath, err = privateDeploymentStatePath("dev", e.owner.Journal)
	if err != nil {
		return bad("original journal path unavailable")
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if readEnvironmentPreflightJSON(e.JournalPath, &e.Journal) != nil || e.Journal.OwnerToken != e.owner.OwnerToken || e.Journal.PlanSHA256 != sha || !e.Journal.CompletedAt.IsZero() {
		return bad("original stopped journal differs")
	}
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/recovery", sha, "qualification.json"), &artifact) != nil || artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || artifact.Plan.RecoveryProfile != "completed-storage-canary" || deploymentplan.Digest(artifact.Plan) != sha || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || artifact.Qualification.Decision != "GO" {
		return bad("original admitted storage recovery provenance unavailable")
	}
	last := environmentCreateJournalEntry{}
	var priorReviews []string
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/foundation" {
			last = entry
			continue
		}
		if strings.HasPrefix(entry.Key, "foundation-recovery-admission-") || strings.HasPrefix(entry.Key, "kubernetes/foundation-recovery-") {
			candidate := strings.TrimPrefix(strings.TrimPrefix(entry.Key, "foundation-recovery-admission-"), "kubernetes/foundation-recovery-")
			if !environmentPreflightDigest.MatchString(candidate) || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return bad("previous foundation recovery has unknown or completed mutations")
			}
			if strings.HasPrefix(entry.Key, "foundation-recovery-admission-") {
				if createProviderContains(priorReviews, candidate) {
					return bad("duplicate previous recovery admission")
				}
				priorReviews = append(priorReviews, candidate)
			} else if len(priorReviews) == 0 || priorReviews[len(priorReviews)-1] != candidate {
				return bad("previous recovery stage lacks its exact admission")
			}
			continue
		}
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.Phase != "provider" && entry.Phase != "storage" && entry.Phase != "namespace" {
			return bad("only original completed provider, storage and namespace stages may precede foundation recovery")
		}
	}
	if last.Key == "" || last.State != "pending" || len(last.Receipt) != 0 || len(e.Journal.Entries) == 0 || len(priorReviews) > 2 || len(e.Journal.KubernetesLeases) != 1+len(priorReviews) || len(priorReviews) == 0 && e.Journal.Entries[len(e.Journal.Entries)-1].Key != last.Key {
		return bad("foundation is not the sole pending initial Kubernetes stage")
	}
	lease := e.Journal.KubernetesLeases[0]
	parts := strings.SplitN(lease.Holder, ":", 2)
	if len(parts) != 2 || !environmentPreflightDigest.MatchString(parts[0]) || lease.UID == "" {
		return bad("original foundation lease receipt unavailable")
	}
	dir := filepath.Join(store.Root, "deployment/rollback", parts[0])
	if readExecutionJSON(filepath.Join(dir, "plan.json"), &child) != nil || readExecutionJSON(filepath.Join(dir, "journal.json"), &childJournal) != nil || child.SHA256() != parts[0] || child.Environment != "dev" || child.Stack != e.Journal.Stack || childJournal.PlanSHA256 != parts[0] || childJournal.Accepted || childJournal.AcceptanceStarted || childJournal.InFlight != "" || childJournal.Failed != "StatefulSet/"+cfg.Values["CLOUD_STACK_NAME"]+"-secrets/openbao" || len(child.Resources) != 33 || len(childJournal.Owners) != 33 || len(childJournal.Completed) != 32 {
		return bad("only the exact initial foundation readiness failure has qualified owner evidence")
	}
	for index, id := range childJournal.Completed {
		if child.Resources[index].ID != id {
			return bad("original completed foundation order differs")
		}
	}
	if child.Resources[32].ID != childJournal.Failed {
		return bad("failed foundation resource is not its final original write")
	}
	for attempt, priorReview := range priorReviews {
		var previous createFoundationRecoveryArtifact
		var before environmentCreateJournal
		base := filepath.Join(store.Root, "deployment/create/foundation-recovery", priorReview)
		if readEnvironmentPreflightJSON(filepath.Join(base, "qualification.json"), &previous) != nil || previous.Schema != "rtk-cloud.create-foundation-recovery-artifact/v1" || deploymentplan.Digest(previous.Plan) != priorReview || previous.Plan.ParentSHA != sha || previous.Qualification.Decision != "GO" || previous.Plan.FoundationSHA != child.SHA256() || previous.Plan.FoundationJournalSHA != deploymentplan.Digest(childJournal) || readEnvironmentPreflightJSON(filepath.Join(base, "source-journal.json"), &before) != nil || deploymentplan.Digest(before) != previous.Plan.ParentJournalSHA || len(e.Journal.Entries) != len(before.Entries)+2*(len(priorReviews)-attempt) {
			return bad("previous stopped recovery lacks immutable qualification and parent snapshots")
		}
		for index, entry := range before.Entries {
			if deploymentplan.Digest(entry) != deploymentplan.Digest(e.Journal.Entries[index]) {
				return bad("previous parent stages changed")
			}
		}
		admission, stage := e.Journal.Entries[len(before.Entries)], e.Journal.Entries[len(before.Entries)+1]
		if admission.Key != "foundation-recovery-admission-"+priorReview || stage.Key != "kubernetes/foundation-recovery-"+priorReview || admission.StartedAt.Before(previous.QualifiedAt) || admission.StartedAt.After(previous.ValidUntil) {
			return bad("previous recovery admission is unbound or expired")
		}
		retainedSHA := strings.SplitN(e.Journal.KubernetesLeases[attempt+1].Holder, ":", 2)[0]
		var retained deploymentplan.Plan
		var retainedJournal deploymentplan.Journal
		if !environmentPreflightDigest.MatchString(retainedSHA) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", retainedSHA, "plan.json"), &retained) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", retainedSHA, "journal.json"), &retainedJournal) != nil || retained.SHA256() != retainedSHA || retainedJournal.PlanSHA256 != retainedSHA || retainedJournal.Accepted || !retainedJournal.AcceptanceStarted || retainedJournal.InFlight != "" || retainedJournal.Failed != "deployment.post-deploy.workloads" || len(retained.Resources) != 33 || len(retainedJournal.Owners) != 33 || len(retainedJournal.Completed) != 33 {
			return bad("previous verification is not the exact stopped all-retain acceptance failure")
		}
		intent, err := environmentCreateKubernetesStageIntent(&retained)
		intentSHA, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || stage.IntentSHA256 != intentSHA {
			return bad("previous retained stage intent differs")
		}
		for index, resource := range retained.Resources {
			original := child.Resources[index]
			if resource.Action != "retain" || resource.ID != original.ID || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(original.Desired) || resource.UID != childJournal.Owners[resource.ID] || retainedJournal.Owners[resource.ID] != resource.UID || retainedJournal.Completed[index] != resource.ID {
				return bad("previous verification contains changed owners or resource writes")
			}
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&child)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || intentSHA != last.IntentSHA256 || child.ConfigurationSHA256 != artifact.Plan.Plan.ConfigurationSHA256 {
		return bad("original foundation intent changed")
	}
	if err := e.Check(context.Background()); err != nil {
		return bad("original execution identity changed")
	}
	loaded = true
	return e, artifact, &child, childJournal, nil
}

func foundationRecoveryMaterials(store secretStore, parentSHA string, minimumLives ...time.Duration) (map[string]string, environmentCreateTransport, error) {
	return foundationRecoveryMaterialsForExecution(store, parentSHA, nil, minimumLives...)
}

func foundationRecoveryMaterialsForExecution(store secretStore, parentSHA string, command *environmentCreateExecution, minimumLives ...time.Duration) (map[string]string, environmentCreateTransport, error) {
	var management environmentCreateTransport
	minimumLife := 150 * time.Minute
	if len(minimumLives) == 1 && minimumLives[0] > 0 {
		minimumLife = minimumLives[0]
	}
	hashes := map[string]string{}
	renewed, proof, err := readInitialTransportRenewal(store, parentSHA, command)
	if err != nil {
		return nil, management, err
	}
	historicalTransport, scopeErr := managedLatePostMintHistoricalTransportScope(store, parentSHA, command)
	if scopeErr != nil || historicalTransport && (proof == nil || proof.Schema != activeTransportRenewalSchema) {
		return nil, management, activeTransportError()
	}
	if proof != nil {
		for path, sha := range proof.Material {
			hashes[path] = sha
		}
		hashes["initial-transport-renewal-proof"] = deploymentplan.Digest(proof)
	}
	for _, role := range []string{"management", "openbao"} {
		base := filepath.Join("deployment/create/material", parentSHA, role+"-transport")
		if proof != nil {
			base = filepath.Join("deployment/create/transport-renewal", proof.Review, role+"-transport")
		}
		for _, name := range []string{"identities.json", "ca.crt"} {
			raw, err := readProviderStorageKeyFile(filepath.Join(store.Root, base, name))
			if err != nil {
				return nil, management, createProviderRecoveryBlock("original transport material is missing; it cannot be regenerated")
			}
			hashes[filepath.Join(base, name)] = environmentPreflightBytesSHA256(raw)
		}
		var transport environmentCreateTransport
		if readEnvironmentPreflightJSON(filepath.Join(store.Root, base, "identities.json"), &transport) != nil {
			return nil, management, createProviderRecoveryBlock("original transport identities are unreadable")
		}
		if proof != nil && deploymentplan.Digest(transport) != deploymentplan.Digest(renewed[role]) {
			return nil, management, createProviderRecoveryBlock("renewed temporary transport differs from its qualified proof")
		}
		cert, err := parseFirstCertificatePEM([]byte(transport.CACert))
		ca, readErr := store.read(filepath.Join(base, "ca.crt"))
		if err != nil || cert == nil || readErr != nil || strings.TrimSpace(transport.CACert) != ca || !cert.IsCA || cert.Subject.CommonName != "rtk-create-dev-"+role || !historicalTransport && cert.NotAfter.Before(time.Now().Add(minimumLife)) {
			return nil, management, createProviderRecoveryBlock("original transport authority differs or cannot cover qualification and execution")
		}
		if role == "management" {
			management = transport
		}
	}
	for _, name := range []string{"access.key", "access.pub", "refresh.key", "refresh.pub"} {
		rel := filepath.Join("pki/account-manager-jwt", name)
		raw, err := readProviderStorageKeyFile(filepath.Join(store.Root, rel))
		if err != nil {
			return nil, management, createProviderRecoveryBlock("original Account Manager signer is missing; it cannot be regenerated")
		}
		hashes[rel] = environmentPreflightBytesSHA256(raw)
	}
	if _, err := ensureEnvironmentCreateAccountSigner(store); err != nil {
		return nil, management, err
	}
	if err := qualifyEnvironmentCreateRecordedRuntime(store); err != nil {
		return nil, management, err
	}
	for _, relative := range []string{"inventory.json", "deployment/storage/create-allocation.json"} {
		raw, err := readProviderStorageKeyFile(filepath.Join(store.Root, relative))
		if err != nil {
			return nil, management, err
		}
		hashes[relative] = environmentPreflightBytesSHA256(raw)
	}
	runtime, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return nil, management, err
	}
	hashes["runtime-snapshot"] = deploymentplan.Digest(runtime)
	return hashes, management, nil
}

func verifyFoundationMaterialBindings(store secretStore, parentSHA string, child *deploymentplan.Plan) error {
	transports := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		var material environmentCreateTransport
		if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/material", parentSHA, role+"-transport/identities.json"), &material); err != nil {
			return err
		}
		transports[role] = material
	}
	seen := map[string]bool{}
	for _, resource := range child.Resources {
		name := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])
		var expected map[string]string
		if name == "account-manager-jwt" && resource.Desired["kind"] == "Secret" {
			expected = map[string]string{}
			for _, key := range []string{"access.key", "access.pub", "refresh.key", "refresh.pub"} {
				raw, err := readProviderStorageKeyFile(filepath.Join(store.Root, "pki/account-manager-jwt", key))
				if err != nil {
					return err
				}
				expected[key] = string(raw)
			}
		} else if name == "openbao-transport-tls" && resource.Desired["kind"] == "Secret" {
			material := transports["openbao"]
			identity, ok := material.Identities["openbao"]
			if !ok {
				return createProviderRecoveryBlock("original OpenBao transport identity is missing")
			}
			expected = map[string]string{"ca.crt": material.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}
		} else if strings.HasPrefix(name, "pki-create-") && strings.HasSuffix(name, "-transport") && resource.Desired["kind"] == "Secret" {
			role := strings.TrimSuffix(strings.TrimPrefix(name, "pki-create-"), "-transport")
			material := transports["management"]
			identity, ok := material.Identities[role]
			if !ok {
				return createProviderRecoveryBlock("original management transport identity is missing")
			}
			expected = map[string]string{"ca.crt": material.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}
		}
		if expected == nil {
			continue
		}
		if seen[name] {
			return createProviderRecoveryBlock("original material binding is duplicated")
		}
		seen[name] = true
		actual := deploymentplan.Map(resource.Desired["data"])
		if len(actual) != len(expected) {
			return createProviderRecoveryBlock("original material projection differs")
		}
		for key, want := range expected {
			raw, err := base64.StdEncoding.DecodeString(deploymentplan.String(actual[key]))
			if err != nil || string(raw) != want {
				return createProviderRecoveryBlock("original local material differs from its recorded foundation Secret: " + name)
			}
		}
	}
	if len(seen) != 7 {
		return createProviderRecoveryBlock("complete original transport and signer bindings are required")
	}
	return nil
}

func verifyFoundationRecoveryTarget(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p createFoundationRecoveryPlan, child *deploymentplan.Plan, journal deploymentplan.Journal, operator map[string]string) error {
	for _, r := range p.Plan.Provider.Resources {
		original := p.Receipts[r.Key]
		actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, r, original, operator)
		if err != nil || !ready || original.PublicIP != "" && original.PublicIP != actual.PublicIP || original.PrivateIP != "" && original.PrivateIP != actual.PrivateIP {
			return createProviderRecoveryBlock("an original provider allocation changed")
		}
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return err
	}
	if environmentPreflightBytesSHA256(raw) != p.KubeconfigSHA {
		return createProviderRecoveryBlock("original canonical Kubernetes access identity changed")
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	lease, err := backend.Read(ctx, "Lease/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/rtk-deployment-execution")
	if err != nil || lease != nil {
		return createProviderRecoveryBlock("foundation recovery requires its original child execution lease to have been released")
	}
	seen := map[string]bool{}
	for _, r := range child.Resources {
		if r.Action != "create" || journal.Owners[r.ID] == "" || seen[r.ID] {
			return createProviderRecoveryBlock("foundation original write owners are incomplete")
		}
		seen[r.ID] = true
		actual, err := backend.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return createProviderRecoveryBlock("original foundation owner or desired body drifted: " + r.ID)
		}
		retained := r
		retained.Action = "retain"
		retained.UID = journal.Owners[r.ID]
		if err := backend.Wait(ctx, retained); err != nil {
			return err
		}
	}
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "namespace" {
			continue
		}
		var proof struct {
			Resource string `json:"resource_id"`
			UID      string `json:"uid"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &proof) != nil || proof.Resource != entry.Key || proof.UID == "" {
			return createProviderRecoveryBlock("original namespace receipt unavailable")
		}
		actual, err := backend.Read(ctx, entry.Key)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != proof.UID {
			return createProviderRecoveryBlock("original namespace owner changed")
		}
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, backend)
	if err != nil || status.Initialized || !status.Sealed {
		return createProviderRecoveryBlock("initial OpenBao state changed; this profile cannot resume an initialization ceremony")
	}
	if err := verifyFoundationVolumeCapacity(ctx, cfg, backend, child, p.Plan.Provider.WorkloadAllocations, operator); err != nil {
		return err
	}
	return e.Check(ctx)
}

func verifyFoundationVolumeCapacity(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, child *deploymentplan.Plan, workloadAllocations int, operator map[string]string) error {
	var claims []deploymentplan.Resource
	volumes := map[string]bool{}
	for _, resource := range child.Resources {
		if resource.Desired["kind"] != "StatefulSet" {
			continue
		}
		metadata := deploymentplan.Map(resource.Desired["metadata"])
		for _, raw := range certIssuerObjectList(deploymentplan.Map(resource.Desired["spec"])["volumeClaimTemplates"]) {
			name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(raw)["metadata"])["name"])
			for ordinal := 0; ordinal < envObjectReplicas(resource.Desired); ordinal++ {
				id := "PersistentVolumeClaim/" + deploymentplan.String(metadata["namespace"]) + "/" + name + "-" + deploymentplan.String(metadata["name"]) + "-" + strconv.Itoa(ordinal)
				claim, err := backend.Read(ctx, id)
				if err != nil || claim == nil {
					return createProviderRecoveryBlock("original foundation durable claim is unavailable")
				}
				if !deploymentplan.Matches(deploymentplan.Object{"spec": deploymentplan.Map(raw)["spec"]}, claim) {
					return createProviderRecoveryBlock("generated foundation claim differs from the original StatefulSet claim template")
				}
				volume := deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"])
				pv, err := backend.Read(ctx, "PersistentVolume//"+volume)
				if err != nil || pv == nil || volumes[volume] || deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["driver"] != "linodebs.csi.linode.com" || deploymentplan.Map(pv["spec"])["persistentVolumeReclaimPolicy"] != "Retain" {
					return createProviderRecoveryBlock("foundation volume is foreign, duplicated or does not preserve Retain")
				}
				volumes[volume] = true
				// Read qualification binds the already generated allocation to its
				// original template and native UID. It is not a rendered desired
				// resource, ownership adoption, label mutation or replacement.
				claims = append(claims, deploymentplan.Resource{ID: id, Phase: "storage", Action: "retain", UID: deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), Baseline: deploymentplan.Projection(claim)})
			}
		}
	}
	if len(claims) != 3 || workloadAllocations < len(claims) {
		return createProviderRecoveryBlock("original three foundation allocations cannot be verified")
	}
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", ConfigurationSHA256: child.ConfigurationSHA256, ReleaseSHA256: child.ReleaseSHA256, BindingsSHA256: deploymentplan.Digest(claims)}, Resources: claims}
	bindings, err := backend.storageBindings(ctx, plan)
	if err != nil || len(bindings) != len(claims) {
		return createProviderRecoveryBlock("original claim UID and bound volume claimRef cannot be verified")
	}
	current := 0
	for _, path := range []string{"/linode/instances", "/volumes", "/nodebalancers"} {
		items, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], path)
		if err != nil {
			return err
		}
		current += len(items)
	}
	account, err := readLKEAccountState(cfg.Environment, true)
	if err != nil {
		return err
	}
	if account["LKE_ACTIVE_SERVICE_LIMIT"] != "unlimited" {
		limit, err := strconv.Atoi(account["LKE_ACTIVE_SERVICE_LIMIT"])
		if err != nil || limit <= 0 || current+workloadAllocations-len(bindings) > limit {
			return createProviderRecoveryBlock("confirmed account capacity cannot cover remaining allocations after retaining original foundation volumes")
		}
	}
	return ctx.Err()
}

func runEnvironmentCreateFoundationRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-foundation", flag.ContinueOnError)
	env := fs.String("environment", "", "selected dev environment")
	parentSHA := fs.String("parent-plan-sha256", "", "original stopped foundation parent")
	review := fs.String("review-sha256", "", "fresh qualified recovery digest")
	execute := fs.Bool("execute", false, "continue only a separately qualified original foundation")
	confirm := fs.String("confirm", "", "exact stack")
	report := fs.String("report", "", "private canonical report")
	timeout := fs.Duration("timeout", 90*time.Minute, "remaining bootstrap and acceptance timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parentSHA) || *timeout <= 0 || *timeout > 90*time.Minute {
		return createProviderRecoveryBlock("selected dev parent and a bounded recovery timeout are required")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		return err
	}
	if *report != "" {
		if err := validateEnvironmentDeploymentReportPath("dev", *report); err != nil {
			return err
		}
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-foundation"}, args...)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	e, original, child, journal, err := loadStoppedFoundationCreate(cfg, store, *parentSHA)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 || deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("foundation configuration or operator values changed")
	}
	materials, management, err := foundationRecoveryMaterials(store, *parentSHA)
	if err != nil {
		return err
	}
	if err := verifyFoundationMaterialBindings(store, *parentSHA, child); err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	p := createFoundationRecoveryPlan{Schema: "rtk-cloud.create-foundation-recovery/v1", ParentSHA: *parentSHA, ParentOwnerSHA: deploymentplan.Digest(e.owner), ParentJournalSHA: deploymentplan.Digest(e.Journal), FoundationSHA: child.SHA256(), FoundationJournalSHA: deploymentplan.Digest(journal), CheckerSHA: checker, KubeconfigSHA: original.Plan.KubeconfigSHA256, Materials: materials, Plan: original.Plan.Plan, Receipts: original.Plan.Receipts}
	sha := deploymentplan.Digest(p)
	directory, err := prepareEnvironmentCreateDirectories(store, filepath.Join("deployment/create/foundation-recovery", sha))
	if err != nil {
		return err
	}
	artifactPath := filepath.Join(directory, "qualification.json")
	checks := []struct {
		id  string
		run func() error
	}{
		{"deployment.foundation.target", func() error { return verifyFoundationRecoveryTarget(ctx, cfg, store, e, p, child, journal, operator) }},
		{"deployment.foundation.release", func() error { return verifyEnvironmentRelease(ctx, cfg, p.Plan.Release) }},
		{"deployment.foundation.storage", func() error {
			if err := qualifyProviderRecoveryStorage(ctx, cfg, operator); err != nil {
				return err
			}
			return validateEnvironmentCreateStorageAllocation(cfg)
		}},
		{"deployment.foundation.bootstrap", func() error { return qualifyEnvironmentCreateBootstrap(ctx, cfg, p.Plan.Bootstrap, store, operator) }},
		{"deployment.foundation.network", func() error { return qualifyEnvironmentCreateDNS(ctx, cfg, p.Plan.DNS, operator) }},
		{"deployment.foundation.inputs", func() error {
			fresh, err := resolveDeploymentConfig(workspace, "dev", "")
			if err != nil {
				return err
			}
			_, values, err := readEnvironmentCreateOperator(fresh)
			if err != nil {
				return err
			}
			current, _, err := foundationRecoveryMaterials(store, *parentSHA)
			if err != nil {
				return err
			}
			if deploymentplan.Digest(fresh) != p.Plan.ConfigurationSHA256 || deploymentplan.Digest(values) != p.Plan.OperatorSHA256 || deploymentplan.Digest(current) != deploymentplan.Digest(p.Materials) {
				return createProviderRecoveryBlock("original foundation inputs changed")
			}
			if err := verifyFoundationMaterialBindings(store, *parentSHA, child); err != nil {
				return err
			}
			if err := verifyEnvironmentImageSources(ctx, fresh, p.Plan.Release, environmentCIImageCommand(fresh)); err != nil {
				return err
			}
			return e.Check(ctx)
		}},
	}
	if !*execute {
		q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
		for _, check := range checks {
			err := runEnvironmentProgress(ctx, os.Stdout, check.id, func(context.Context) error { return check.run() })
			gate := deploymentplan.GateResult{ID: check.id, Required: true, Status: "PASS", EvidenceSHA256: sha}
			if err != nil {
				gate.Status = "BLOCKED"
				gate.Cause = err.Error()
				q.Decision = "NO-GO"
				var blocker *deploymentplan.Blocker
				if errors.As(err, &blocker) {
					fmt.Fprintf(os.Stdout, "  cause: %s\n", blocker.Cause)
				}
			}
			q.Gates = append(q.Gates, gate)
		}
		now := time.Now().UTC()
		artifact := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-foundation-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
		if q.Decision == "GO" {
			rel := filepath.Join("deployment/create/foundation-recovery", sha, "qualifications", now.Format("20060102T150405.000000000Z")+".json")
			if err := writeEnvironmentCreateDerivedPrivateFile(store, rel, mustProviderRecoveryJSON(artifact)); err != nil {
				return err
			}
			if err := store.write(strings.TrimPrefix(artifactPath, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(artifact), true); err != nil {
				return err
			}
		}
		if *report != "" {
			if err := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(artifact), true); err != nil {
				return err
			}
		}
		fmt.Printf("%s environment=dev scope=initial-foundation-recovery review-sha256=%s\n", q.Decision, sha)
		if q.Decision != "GO" {
			return exitCode(1)
		}
		return nil
	}
	var artifact createFoundationRecoveryArtifact
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] || *review != sha || readEnvironmentPreflightJSON(artifactPath, &artifact) != nil || artifact.Schema != "rtk-cloud.create-foundation-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != sha || artifact.Qualification.PlanSHA256 != sha || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != len(checks) || artifact.QualifiedAt.After(time.Now().UTC()) || !artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(time.Hour)) || !time.Now().Before(artifact.ValidUntil) {
		return createProviderRecoveryBlock("fresh complete foundation recovery qualification and exact confirmation are required")
	}
	for index, gate := range artifact.Qualification.Gates {
		if !gate.Required || gate.Status != "PASS" || gate.ID != checks[index].id || gate.EvidenceSHA256 != sha {
			return createProviderRecoveryBlock("foundation gate is not qualified")
		}
	}
	for _, index := range []int{0, 2, 3, 5} {
		if err := checks[index].run(); err != nil {
			return err
		}
	}
	// Preserve the failed parent byte-for-byte before appending new actual
	// forward-recovery evidence. Its failed child journal is never overwritten.
	for _, source := range []struct{ name, path string }{{"source-owner.json", filepath.Join(store.Root, "deployment/.execution-active/owner.json")}, {"source-journal.json", e.JournalPath}} {
		raw, err := readBoundedEnvironmentFile(source.path, true)
		if err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/foundation-recovery", sha, source.name), raw); err != nil {
			return err
		}
	}
	if err := e.BeginEntry("foundation-recovery-admission-"+sha, "recovery", mustProviderRecoveryJSON(map[string]any{"review_sha256": sha, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": p.ParentJournalSHA, "original_child_journal_sha256": p.FoundationJournalSHA}), time.Now().UTC()); err != nil {
		return err
	}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		return applyEnvironmentCreateStage(ctx, cfg, store, &p.Plan, e, stage, definitions, os.Stdout)
	}
	var retained []deploymentplan.Definition
	for _, resource := range child.Resources {
		retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase, Retain: true})
	}
	stage := "foundation-recovery-" + sha
	if err := apply(ctx, stage, retained); err != nil {
		return err
	}
	proof := map[string]any{"review_sha256": sha, "original_foundation_plan_sha256": p.FoundationSHA, "original_failed_journal_sha256": p.FoundationJournalSHA, "actual_retained_verification_stage": "kubernetes/" + stage}
	keys := []string{"kubernetes/foundation", "foundation-recovery-admission-" + sha}
	for _, entry := range e.Journal.Entries {
		if entry.State == "pending" && (strings.HasPrefix(entry.Key, "foundation-recovery-admission-") || strings.HasPrefix(entry.Key, "kubernetes/foundation-recovery-")) && entry.Key != "foundation-recovery-admission-"+sha {
			keys = append(keys, entry.Key)
		}
	}
	for _, key := range keys {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return err
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		return err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: backend}
	runtime, stop, err := continueEnvironmentCreateFoundation(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
	if err == nil {
		defer stop()
		err = executeEnvironmentCreateWithRuntime(ctx, cfg, &p.Plan, p.Receipts, store, operator, e, os.Stdout, runtime, apply)
	}
	if err == nil {
		err = e.CompleteAndRelease(time.Now().UTC())
	}
	decision := "GO"
	if err != nil {
		decision = "NO-GO"
	}
	if *report != "" {
		result := map[string]any{"schema": "rtk-cloud.create-foundation-recovery-result/v1", "environment": "dev", "decision": decision, "completed": err == nil, "review_sha256": sha, "parent_plan_sha256": *parentSHA, "journal": e.JournalPath}
		if err != nil {
			result["cause"] = err.Error()
		}
		if writeErr := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(result), true); writeErr != nil {
			return writeErr
		}
	}
	fmt.Printf("%s environment=dev scope=initial-foundation-recovery completed=%t\n", decision, err == nil)
	return err
}
