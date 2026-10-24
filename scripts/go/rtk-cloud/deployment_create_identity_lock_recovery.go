package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

func applicationIdentityLockReview(key string) string {
	const prefix = "kubernetes/service-managed-core-trust-recovery-"
	sha := strings.TrimPrefix(key, prefix)
	if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(sha) {
		return sha
	}
	return ""
}

func loadApplicationIdentityLockSource(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) (createFoundationRecoveryArtifact, error) {
	var source createFoundationRecoveryArtifact
	bad := func() (createFoundationRecoveryArtifact, error) {
		return source, createProviderRecoveryBlock("identity lock recovery requires the exact admitted trust repair, fourteen completed resources and original failed Account Manager owner")
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	review := applicationIdentityLockReview(entry.Key)
	if review == "" || entry.State != "pending" || len(entry.Receipt) != 0 || len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	var plan deploymentplan.Plan
	var j deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || plan.SHA256() != sha || j.PlanSHA256 != sha || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(plan.Resources) != 15 || len(j.Completed) != 14 || len(j.Owners) != 15 || j.Failed != "Deployment/"+e.Journal.Stack+"-account-manager/account-manager" {
		return bad()
	}
	seen := map[string]bool{}
	for _, id := range j.Completed {
		if seen[id] || j.Owners[id] == "" || id == j.Failed {
			return bad()
		}
		seen[id] = true
	}
	for _, resource := range plan.Resources {
		if j.Owners[resource.ID] == "" || resource.ID != j.Failed && !seen[resource.ID] {
			return bad()
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || hash != entry.IntentSHA256 {
		return bad()
	}
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "qualification.json"), &source) != nil || deploymentplan.Digest(source.Plan) != review || source.Plan.ParentSHA != e.Journal.PlanSHA256 || source.Plan.Schema != "rtk-cloud.create-application-trust-recovery/v1" || source.Qualification.PlanSHA256 != review || source.Qualification.Decision != "GO" || len(source.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := source.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return bad()
		}
	}
	admitted := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-recovery-admission-"+review && entry.State == "completed" {
			var receipt map[string]string
			if json.Unmarshal(entry.Receipt, &receipt) == nil && receipt["qualified_recovery_sha256"] == review {
				admitted = true
			}
		}
	}
	if !admitted {
		return bad()
	}
	r.IdentityLockForward, r.PendingManagedKey, r.PendingManagedPlan, r.PendingManagedJournal = true, entry.Key, &plan, j
	return source, nil
}

func loadOriginalApplicationPlanAt(store secretStore, e *environmentCreateExecution, stack, sha string, completed ...bool) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var j deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, j, createProviderRecoveryBlock("original refused application plan and recorded lease must match the admitted trust repair")
	}
	found := false
	for _, lease := range e.Journal.KubernetesLeases {
		if strings.SplitN(lease.Holder, ":", 2)[0] == sha {
			found = true
		}
	}
	if !found || !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || plan.SHA256() != sha || !knownApplicationEnrollmentTrustFailure(&plan, j, stack) {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	digest, hashErr := environmentCreatePlanSHA256(intent)
	for _, entry := range e.Journal.Entries {
		allowed := entry.State == "pending" && len(entry.Receipt) == 0 || len(completed) == 1 && completed[0] && entry.State == "completed" && len(entry.Receipt) > 0
		if entry.Key == "kubernetes/application-client-enrollment" && allowed && err == nil && hashErr == nil && entry.IntentSHA256 == digest {
			return &plan, j, nil
		}
	}
	return bad()
}

func prepareApplicationIdentityLockRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	r.GrantImage = image
	p.Materials["accepted-service-core-trust-repair-selection"] = selection
	p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
	query := "SELECT COALESCE(json_agg(subject ORDER BY subject),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	issued, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, query)
	var subjects []string
	if err != nil || json.Unmarshal(issued, &subjects) != nil || !slices.Equal(subjects, []string{"service:account-manager", "service:certissuer", "service:pki-controller"}) {
		return createProviderRecoveryBlock("identity lock recovery cannot adopt unrecorded application issuance")
	}
	failed := r.PendingManagedPlan.Resources[len(r.PendingManagedPlan.Resources)-1]
	for _, resource := range r.PendingManagedPlan.Resources {
		if resource.ID == r.PendingManagedJournal.Failed {
			failed = resource
		}
	}
	if deploymentplan.Map(deploymentplan.Map(failed.Desired["spec"])["strategy"])["type"] == "Recreate" {
		return createProviderRecoveryBlock("identity lock recovery cannot replace an already exclusive rollout")
	}
	raw, err := b.command(ctx, nil, "--namespace", p.Plan.Stack+"-account-manager", "get", "pods", "-l", "app.kubernetes.io/name=account-manager", "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil {
		return createProviderRecoveryBlock("original failed Account Manager Pod cannot be inspected")
	}
	matched := 0
	for _, pod := range pods.Items {
		container := deploymentplan.Object{}
		for _, value := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
			c := deploymentplan.Map(value)
			if c["name"] == "pkimanagement" {
				container = c
			}
		}
		if mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID") != r.Ceremony.BootstrapSessionID {
			continue
		}
		refs := certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])
		if len(refs) != 1 {
			return createProviderRecoveryBlock("failed identity Pod owner changed")
		}
		ref := deploymentplan.Map(refs[0])
		if ref["kind"] != "ReplicaSet" || ref["controller"] != true {
			return createProviderRecoveryBlock("failed identity Pod requires its original ReplicaSet")
		}
		rs, err := b.Read(ctx, "ReplicaSet/"+p.Plan.Stack+"-account-manager/"+deploymentplan.String(ref["name"]))
		if err != nil || rs == nil || deploymentplan.Map(rs["metadata"])["uid"] != ref["uid"] {
			return createProviderRecoveryBlock("failed identity ReplicaSet changed")
		}
		parents := certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])
		if len(parents) != 1 || deploymentplan.Map(parents[0])["uid"] != r.PendingManagedJournal.Owners[r.PendingManagedJournal.Failed] {
			return createProviderRecoveryBlock("failed identity Pod is not owned by the original Deployment")
		}
		logs, err := b.command(ctx, nil, "--namespace", p.Plan.Stack+"-account-manager", "logs", deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), "--container", "pkimanagement", "--previous")
		if err != nil || strings.TrimSpace(string(logs)) != "Service identity already owned: resource temporarily unavailable" {
			return createProviderRecoveryBlock("Account Manager failure is not the exact retained identity lock conflict")
		}
		matched++
	}
	if matched != 1 {
		return createProviderRecoveryBlock("exactly one original failed retained identity Pod is required")
	}
	_, management, err := foundationRecoveryMaterials(store, e.Journal.PlanSHA256, time.Hour)
	if err != nil {
		return err
	}
	ca, err := parseFirstCertificatePEM([]byte(management.CACert))
	if err != nil || ca == nil {
		return createProviderRecoveryBlock("original management transport authority cannot be read")
	}
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: b, ServiceSessionRecovery: r, TransportCAPEM: management.CACert, TransportRootSHA256: environmentPreflightBytesSHA256(ca.Raw), TransportTrustConfigMap: "pki-create-transport-trust", TransportSecrets: map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport"}}
	listeners, _, err := loadInitialListenerCheckpoint(store, e, p, true)
	if err != nil {
		return err
	}
	for _, resource := range listeners.Resources {
		runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired)})
	}
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil {
		return err
	}
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p.Plan.Bootstrap, runtime, r.Ceremony)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = image.Reference
				}
			}
		}
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("core-exclusive-identity-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	if len(r.Candidate.Resources) != 15 {
		return createProviderRecoveryBlock("exclusive identity forward requires the exact original fifteen core resources")
	}
	updates := 0
	for _, resource := range r.Candidate.Resources {
		if resource.Action == "update" && resource.ID == r.PendingManagedJournal.Failed {
			expected := deploymentplan.Clone(failed.Desired)
			setEnvironmentCreateExclusiveAccountOwner(expected)
			if deploymentplan.Digest(expected) != deploymentplan.Digest(resource.Desired) {
				return createProviderRecoveryBlock("exclusive identity forward may change only Account Manager rollout strategy and owner-mode annotation")
			}
			updates++
		} else if resource.Action != "retain" {
			return createProviderRecoveryBlock("exclusive identity forward cannot change another core owner")
		}
	}
	if updates != 1 {
		return createProviderRecoveryBlock("one exact retained identity rollout change is required")
	}
	p.Materials["accepted-service-core-identity-lock-forward-plan"] = r.Candidate.SHA256()
	return nil
}
