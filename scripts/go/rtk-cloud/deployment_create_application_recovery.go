package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
)

func knownApplicationEnrollmentTrustFailure(plan *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if plan == nil || len(plan.Resources) != 12 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(j.Completed) != 6 || len(j.Owners) != 7 || !strings.HasPrefix(j.Failed, "Job/"+stack+"-video-cloud/pki-enroll-factoryenroll-client-") {
		return false
	}
	seen := map[string]bool{}
	for _, id := range j.Completed {
		if seen[id] || !strings.HasPrefix(id, "PersistentVolumeClaim/"+stack+"-video-cloud/") || j.Owners[id] == "" {
			return false
		}
		seen[id] = true
	}
	for _, r := range plan.Resources {
		if r.Desired["kind"] == "PersistentVolumeClaim" {
			if !seen[r.ID] {
				return false
			}
			continue
		}
		if r.Desired["kind"] != "Job" {
			return false
		}
		if r.ID == j.Failed {
			if j.Owners[r.ID] == "" {
				return false
			}
		} else if j.Owners[r.ID] != "" || seen[r.ID] {
			return false
		}
	}
	return true
}

func originalPendingApplicationPlan(store secretStore, e *environmentCreateExecution, stack string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var j deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, j, createProviderRecoveryBlock("original application enrollment must have six owned claims, one terminal refused Factory Job and no other attempted enrollment")
	}
	if len(e.Journal.KubernetesLeases) == 0 || len(e.Journal.Entries) == 0 {
		return bad()
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if entry.Key != "kubernetes/application-client-enrollment" || entry.State != "pending" || len(entry.Receipt) != 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || plan.SHA256() != sha || !knownApplicationEnrollmentTrustFailure(&plan, j, stack) {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	digest, digestErr := environmentCreatePlanSHA256(intent)
	if err != nil || digestErr != nil || digest != entry.IntentSHA256 {
		return bad()
	}
	return &plan, j, nil
}

func loadServiceTrustRepairImage(ctx context.Context, cfg deploymentConfig, store secretStore, parent string) (environmentReleaseImage, string, error) {
	var selection struct {
		Schema      string `json:"schema"`
		Parent      string `json:"parent_plan_sha256"`
		Revision    string `json:"source_revision"`
		RunID       int64  `json:"run_id"`
		Attempt     int    `json:"run_attempt"`
		ManifestSHA string `json:"manifest_sha256"`
	}
	path := filepath.Join(store.Root, "deployment/repairs/service-bootstrap/selection.json")
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || json.Unmarshal(raw, &selection) != nil || selection.Schema != "rtk-cloud.create-service-trust-repair-selection/v1" || selection.Parent != parent || !deploymentUpgradeSHA.MatchString(selection.Revision) || selection.RunID <= 0 || selection.Attempt <= 0 || !environmentPreflightDigest.MatchString(selection.ManifestSHA) {
		return environmentReleaseImage{}, "", createProviderRecoveryBlock("exact selected-generation formal CI Service trust repair publication is required")
	}
	manifest, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/repairs/service-bootstrap", selection.Revision, "image-manifest.json"), true)
	if err != nil || environmentPreflightBytesSHA256(manifest) != selection.ManifestSHA {
		return environmentReleaseImage{}, "", createProviderRecoveryBlock("recorded repair manifest bytes changed")
	}
	repo := "hkt999rtk/rtk_video_cloud"
	fresh, attempt, err := readEnvironmentPublication(ctx, repo, selection.RunID, selection.Revision, false, environmentCIImageCommand(cfg))
	if err != nil || attempt != selection.Attempt || environmentPreflightBytesSHA256(fresh) != selection.ManifestSHA {
		return environmentReleaseImage{}, "", createProviderRecoveryBlock("repair publication lacks exact current successful formal CI provenance")
	}
	image, err := selectEnvironmentPublishedImage(manifest, repo, selection.RunID, selection.Attempt, selection.Revision, "ghcr.io/"+repo+"/video-cloud-api", false)
	return image, environmentPreflightBytesSHA256(raw), err
}

func prepareApplicationTrustRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("application trust recovery requires the original terminal TLS refusal, unchanged claims and no application issuance or competing state")
	}
	failed, err := b.Read(ctx, r.PartialCoreJournal.Failed)
	if err != nil || failed == nil {
		return bad()
	}
	status := deploymentplan.Map(failed["status"])
	terminal := false
	for _, v := range certIssuerObjectList(status["conditions"]) {
		c := deploymentplan.Map(v)
		if c["type"] == "Failed" && c["status"] == "True" && c["reason"] == "BackoffLimitExceeded" {
			terminal = true
		}
	}
	if !terminal || status["failed"] != float64(1) || status["active"] != nil && status["active"] != float64(0) || status["succeeded"] != nil && status["succeeded"] != float64(0) {
		return bad()
	}
	name := deploymentplan.String(deploymentplan.Map(failed["metadata"])["name"])
	logs, err := b.command(ctx, nil, "--namespace", p.Plan.Stack+"-video-cloud", "logs", "job/"+name, "--container", "enroll")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(logs)), "Service client enrollment refused: Post \"https://certissuer."+p.Plan.Stack+"-video-cloud.svc:18445/") || !strings.HasSuffix(strings.TrimSpace(string(logs)), "remote error: tls: bad certificate") {
		return bad()
	}
	query := "SELECT COALESCE(json_agg(subject ORDER BY subject),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, query)
	var subjects []string
	if err != nil || json.Unmarshal(raw, &subjects) != nil || !slices.Equal(subjects, []string{"service:account-manager", "service:certissuer", "service:pki-controller"}) {
		return bad()
	}
	image, selectionSHA, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	r.GrantImage = image
	p.Materials["accepted-service-core-trust-repair-selection"] = selectionSHA
	p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
	// Reuse the original reviewed native grants recipe with only a formal fixed
	// image and a distinct name. It does not create or replace runtime logins.
	grantPlan, _, err := acceptedCreateStage(store, e, "kubernetes/pki-grants")
	if err != nil {
		return err
	}
	if len(grantPlan.Resources) != 1 || grantPlan.Resources[0].Desired["kind"] != "Job" {
		return bad()
	}
	grant := deploymentplan.Clone(grantPlan.Resources[0].Desired)
	deploymentplan.Map(grant["metadata"])["name"] = "pki-grants-trust-forward-" + r.PartialCore.SHA256()[:12]
	for _, v := range certIssuerObjectList(environmentPKIPod(grant)["containers"]) {
		c := deploymentplan.Map(v)
		if c["image"] != p.Plan.Bootstrap.Image {
			return bad()
		}
		c["image"] = image.Reference
	}
	r.GrantDefinitions = []deploymentplan.Definition{{Phase: "migration", Object: grant}}
	// The accepted current renewal is already bound by the managed-core forward
	// receipt. Earlier failed stages can have forward receipts, not child plans.
	if !strings.HasPrefix(r.SourceRenewalStage, "kubernetes/service-session-original-key-renewal-") {
		return bad()
	}
	renewal, _, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if err != nil {
		return err
	}
	if len(renewal.Resources) != 1 || renewal.Resources[0].Desired["kind"] != "Job" {
		return bad()
	}
	original := deploymentplan.Clone(renewal.Resources[0].Desired)
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	r.RenewedIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, r.PartialCore.SHA256(), r.Ceremony.BootstrapSessionID)
	definition, err := originalKeyRenewalDefinition(original, e.Journal.PlanSHA256, r.PartialCore.SHA256(), r.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	container = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
	container["image"] = image.Reference
	container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_BOOTSTRAP_EXPIRES_AT")
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_TTL": "8h"}))
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + r.PartialCore.SHA256()[:12] + "/bootstrap.crt"
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("application-trust-original-key-renewal", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil {
		return err
	}
	if len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("application trust renewal already exists; inspect actual native progress")
	}
	r.GrantCandidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("application-trust-native-grants", r.GrantDefinitions)}, r.GrantDefinitions, live)
	if err != nil {
		return err
	}
	if len(r.GrantCandidate.Resources) != 1 || r.GrantCandidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("application trust grants already exist; inspect actual native progress")
	}
	p.Materials["accepted-service-core-application-grant-plan"] = r.GrantCandidate.SHA256()
	p.Materials["accepted-service-core-application-renewal-plan"] = r.Candidate.SHA256()
	p.Materials["accepted-service-core-application-grants"] = deploymentplan.Digest(r.GrantDefinitions)
	return nil
}
