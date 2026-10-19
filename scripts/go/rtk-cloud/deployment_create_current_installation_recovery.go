package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func currentInstallationReview(key string) string {
	const prefix = "kubernetes/application-current-installation-renewal-"
	if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(strings.TrimPrefix(key, prefix)) {
		return strings.TrimPrefix(key, prefix)
	}
	return ""
}

func loadCurrentInstallationFailure(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact) error {
	bad := func() error {
		return createProviderRecoveryBlock("current installation recovery requires the original nine completed resources, one refused API Job and two absent Jobs")
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	quiesce := currentQuiesceReview(entry.Key) != ""
	if quiesce {
		for _, v := range e.Journal.Entries {
			if currentInstallationReview(v.Key) != "" && v.State == "pending" {
				entry = v
			}
		}
	}
	review := currentInstallationReview(entry.Key)
	if review == "" || deploymentplan.Digest(a.Plan) != review || len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	plan, j, err := originalCreateChildAtIntent(store, e, entry)
	if err != nil || !knownCurrentInstallationFailure(plan, j, e.Journal.Stack) || entry.State != "pending" || len(entry.Receipt) != 0 {
		return bad()
	}
	r.RenewalBindingSHA = a.Plan.Materials["accepted-service-core-openbao-trust-original-plan"]
	if !environmentPreflightDigest.MatchString(r.RenewalBindingSHA) {
		return bad()
	}
	r.OpenBaoOriginalCandidate = &deploymentplan.Plan{}
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), r.OpenBaoOriginalCandidate) != nil || r.OpenBaoOriginalCandidate.SHA256() != a.Plan.Materials["accepted-service-core-openbao-trust-forward-plan"] || len(r.OpenBaoOriginalCandidate.Resources) != 16 {
		return bad()
	}
	r.InstallationForward, r.InstallationKey, r.InstallationPlan, r.InstallationJournal = true, entry.Key, plan, j
	if quiesce {
		return loadCurrentQuiesceFailure(store, e, r)
	}
	return nil
}

func knownCurrentInstallationFailure(plan *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if plan == nil || len(plan.Resources) != 12 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(j.Completed) != 9 || len(j.Owners) != 10 || !strings.HasPrefix(j.Failed, "Job/"+stack+"-video-cloud/pki-enroll-video-cloud-api-client-") || j.Owners[j.Failed] == "" {
		return false
	}
	completed := map[string]bool{}
	for _, id := range j.Completed {
		if completed[id] || j.Owners[id] == "" {
			return false
		}
		completed[id] = true
	}
	claims, jobs := 0, 0
	for _, v := range plan.Resources {
		switch v.Desired["kind"] {
		case "PersistentVolumeClaim":
			if !completed[v.ID] {
				return false
			}
			claims++
		case "Job":
			jobs++
			if completed[v.ID] {
				if !strings.Contains(v.ID, "/pki-enroll-factoryenroll-client-") && !strings.Contains(v.ID, "/pki-enroll-mqtt-pki-client-") && !strings.Contains(v.ID, "/pki-enroll-video-cloud-api-app-pki-client-") {
					return false
				}
			} else if v.ID != j.Failed && j.Owners[v.ID] != "" {
				return false
			}
		default:
			return false
		}
	}
	return claims == 6 && jobs == 6
}

func prepareCurrentInstallationForward(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	var stop, enroll []deploymentplan.Definition
	original, j, err := acceptedCreateStage(store, e, "kubernetes/mqtt-intermediate-trust")
	if err != nil {
		return err
	}
	for _, v := range original.Resources {
		if v.Desired["kind"] == "Deployment" {
			actual, err := b.Read(ctx, v.ID)
			expected := v.Desired
			if r.QuiescePendingPlan != nil {
				for _, paused := range r.QuiescePendingPlan.Resources {
					if paused.ID == v.ID {
						expected = paused.Desired
					}
				}
			}
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(expected, actual) {
				return createProviderRecoveryBlock("original MQTT identity owner changed")
			}
			object := deploymentplan.Clone(v.Desired)
			deploymentplan.Map(object["spec"])["replicas"] = 0
			stop = append(stop, deploymentplan.Definition{Phase: "workload", Object: object})
		}
	}
	if len(stop) != 2 {
		return createProviderRecoveryBlock("exact API and log identity owners are required")
	}
	for _, v := range r.InstallationPlan.Resources {
		if v.Desired["kind"] == "Job" && !currentInstallationContains(r.InstallationJournal.Completed, v.ID) {
			actual, err := b.Read(ctx, v.ID)
			if err != nil {
				return err
			}
			if uid := r.InstallationJournal.Owners[v.ID]; uid != "" {
				if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(v.Desired, actual) {
					return createProviderRecoveryBlock("refused native installation Job changed")
				}
			} else if actual != nil {
				return createProviderRecoveryBlock("unwritten installation Job is already present")
			}
			object := deploymentplan.Clone(v.Desired)
			if v.ID == r.InstallationJournal.Failed {
				deploymentplan.Map(object["metadata"])["name"] = deploymentplan.String(deploymentplan.Map(object["metadata"])["name"]) + "-forward"
			}
			enroll = append(enroll, deploymentplan.Definition{Phase: "bootstrap", Object: object})
		}
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("current-owner-quiesce", stop)}
	r.QuiesceCandidate, err = deploymentplan.Compile(inputs, stop, live)
	if err != nil {
		return err
	}
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("current-installation-forward", enroll)
	r.InstallationCandidate, err = deploymentplan.Compile(inputs, enroll, live)
	if err != nil {
		return err
	}
	if len(r.InstallationCandidate.Resources) != 3 {
		return createProviderRecoveryBlock("only the three unfinished native installation Jobs may run")
	}
	for _, v := range r.InstallationCandidate.Resources {
		if v.Action != "create" {
			return createProviderRecoveryBlock("forward installation already attempted")
		}
	}
	p.Materials["accepted-service-core-current-owner-quiesce-plan"] = r.QuiesceCandidate.SHA256()
	p.Materials["accepted-service-core-current-installation-forward-plan"] = r.InstallationCandidate.SHA256()
	return nil
}

func currentInstallationContains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func executeCurrentInstallationForward(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	definitions := func(plan *deploymentplan.Plan) []deploymentplan.Definition {
		var d []deploymentplan.Definition
		for _, v := range plan.Resources {
			d = append(d, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
		}
		return d
	}
	if err := apply(ctx, "current-owner-quiesce-"+r.Review, definitions(r.QuiesceCandidate)); err != nil {
		return err
	}
	// A zero-replica rollout may finish while a terminating Pod still holds its lock.
	for _, prior := range r.QuiesceHistory {
		if err := runtime.Execution.RecordReceipt(prior.Key, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/current-owner-quiesce-" + r.Review, "original_failed_plan_sha256": prior.Plan.SHA256()})); err != nil {
			return err
		}
		if err := runtime.Execution.CompleteEntry(prior.Key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := waitCurrentIdentityOwnersStopped(ctx, p.Stack, runtime.Backend, r.QuiesceCandidate); err != nil {
		return err
	}
	stage := "current-installation-lock-forward-" + r.Review
	if err := apply(ctx, stage, definitions(r.InstallationCandidate)); err != nil {
		return err
	}
	if err := runtime.Execution.RecordReceipt(r.InstallationKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.InstallationPlan.SHA256(), "renewed_session_id": r.Ceremony.BootstrapSessionID})); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(r.InstallationKey, time.Now().UTC())
}

func waitCurrentIdentityOwnersStopped(ctx context.Context, stack string, b environmentKubernetesBackend, plan *deploymentplan.Plan) error {
	for _, v := range plan.Resources {
		selector, err := currentIdentityOwnerSelector(stack, v.Desired)
		if err != nil {
			return err
		}
		if _, err := b.command(ctx, nil, "--namespace", stack+"-video-cloud", "wait", "--for=delete", "pod", "--selector", selector, "--timeout=120s"); err != nil {
			return err
		}
	}
	return nil
}

// Preserve the exact originally admitted baselines when reconstructing an
// unwritten request after a subsequent qualified seed renewal changed core.
func originalOpenBaoRequestInventory(r *createServiceSessionRecovery) []deploymentplan.Object {
	var live []deploymentplan.Object
	for _, v := range r.OpenBaoOriginalCandidate.Resources {
		if v.Baseline != nil {
			live = append(live, deploymentplan.Clone(v.Baseline))
		}
	}
	return live
}

func prepareUsedInstallationQuiesce(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	plan, j, err := acceptedCreateStage(e.Store, e, "kubernetes/mqtt-intermediate-trust")
	if r.OpenBaoIntermediateRejected {
		plan, j, err = acceptedCreateStage(e.Store, e, "kubernetes/current-owner-quiesce-"+r.CompletedTransportReview)
	}
	if err != nil {
		return err
	}
	var defs []deploymentplan.Definition
	for _, v := range plan.Resources {
		if v.Desired["kind"] == "Deployment" {
			actual, err := b.Read(ctx, v.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
				return createProviderRecoveryBlock("original MQTT installation owner changed")
			}
			object := deploymentplan.Clone(v.Desired)
			deploymentplan.Map(object["spec"])["replicas"] = 0
			defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: object})
		}
	}
	if len(defs) != 2 {
		return createProviderRecoveryBlock("original API and log identity owners are required")
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.QuiesceCandidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("used-installation-owner-quiesce", defs)}, defs, live)
	if err == nil {
		p.Materials["accepted-service-core-current-owner-quiesce-plan"] = r.QuiesceCandidate.SHA256()
	}
	return err
}

func currentQuiesceReview(key string) string {
	const prefix = "kubernetes/current-owner-quiesce-"
	if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(strings.TrimPrefix(key, prefix)) {
		return strings.TrimPrefix(key, prefix)
	}
	return ""
}

type createQuiesceCheckpoint struct {
	Key     string
	Plan    *deploymentplan.Plan
	Journal deploymentplan.Journal
}

func originalCreateChildAtIntent(store secretStore, e *environmentCreateExecution, entry environmentCreateJournalEntry) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("recorded original child plan and native journal must match the parent intent")
	}
	seen := map[string]bool{}
	for i := len(e.Journal.KubernetesLeases) - 1; i >= 0; i-- {
		sha := strings.SplitN(e.Journal.KubernetesLeases[i].Holder, ":", 2)[0]
		if seen[sha] {
			continue
		}
		seen[sha] = true
		if !environmentPreflightDigest.MatchString(sha) {
			return bad()
		}
		var p deploymentplan.Plan
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &p) != nil || p.SHA256() != sha {
			return bad()
		}
		intent, err := environmentCreateKubernetesStageIntent(&p)
		digest, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil {
			return bad()
		}
		if digest != entry.IntentSHA256 {
			continue
		}
		var j deploymentplan.Journal
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || j.PlanSHA256 != sha {
			return bad()
		}
		return &p, j, nil
	}
	return bad()
}

func loadCurrentQuiesceFailure(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("original owner pause requires both completed writes, unchanged UIDs and only its recorded acceptance failure")
	}
	for _, entry := range e.Journal.Entries {
		review := currentQuiesceReview(entry.Key)
		if review == "" || entry.State != "pending" {
			continue
		}
		if len(r.QuiesceHistory) >= 8 || len(entry.Receipt) != 0 {
			return bad()
		}
		a, _, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil {
			return err
		}
		if a.Plan.Materials["accepted-service-core-current-installation-plan"] != r.InstallationPlan.SHA256() {
			return bad()
		}
		plan, j, err := originalCreateChildAtIntent(store, e, entry)
		if err != nil || j.Accepted || !j.AcceptanceStarted || j.InFlight != "" || (j.Failed != "deployment.post-deploy.workloads" && j.Failed != "deployment.bootstrap.pki") || len(plan.Resources) != 2 || len(j.Completed) != 2 || len(j.Owners) != 2 {
			return bad()
		}
		var candidate deploymentplan.Plan
		if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "current-owner-quiesce-plan.json"), &candidate) != nil || candidate.SHA256() != a.Plan.Materials["accepted-service-core-current-owner-quiesce-plan"] || len(candidate.Resources) != 2 {
			return bad()
		}
		for i, v := range plan.Resources {
			if _, err := currentIdentityOwnerSelector(e.Journal.Stack, v.Desired); err != nil {
				return err
			}
			if deploymentplan.Map(v.Desired["spec"])["replicas"] != float64(0) || j.Owners[v.ID] == "" || !currentInstallationContains(j.Completed, v.ID) || candidate.Resources[i].ID != v.ID || deploymentplan.Digest(candidate.Resources[i].Desired) != deploymentplan.Digest(v.Desired) {
				return bad()
			}
			if r.QuiescePendingPlan != nil {
				if r.QuiescePendingJournal.Owners[v.ID] != j.Owners[v.ID] || deploymentplan.Digest(r.QuiescePendingPlan.Resources[i].Desired) != deploymentplan.Digest(v.Desired) {
					return bad()
				}
			}
		}
		r.QuiesceHistory = append(r.QuiesceHistory, createQuiesceCheckpoint{entry.Key, plan, j})
		r.QuiescePendingKey, r.QuiescePendingPlan, r.QuiescePendingJournal = entry.Key, plan, j
	}
	if len(r.QuiesceHistory) == 0 {
		return bad()
	}
	return nil
}

func currentIdentityOwnerSelector(stack string, object deploymentplan.Object) (string, error) {
	bad := func() (string, error) {
		return "", environmentCreateBootstrapBlocker("original identity owner selector changed")
	}
	meta := deploymentplan.Map(object["metadata"])
	name := deploymentplan.String(meta["name"])
	selector := deploymentplan.Map(deploymentplan.Map(object["spec"])["selector"])
	labels := deploymentplan.Map(selector["matchLabels"])
	if object["kind"] != "Deployment" || meta["namespace"] != stack+"-video-cloud" || (name != "video-cloud-api" && name != "video-cloud-logingester") || len(certIssuerObjectList(selector["matchExpressions"])) != 0 || (labels["app.kubernetes.io/name"] != name && labels["app"] != name) {
		return bad()
	}
	var values []string
	for key, v := range labels {
		value, ok := v.(string)
		if !ok || value == "" || strings.ContainsAny(key+value, ",=\n\r") {
			return bad()
		}
		values = append(values, key+"="+value)
	}
	slices.Sort(values)
	return strings.Join(values, ","), nil
}
