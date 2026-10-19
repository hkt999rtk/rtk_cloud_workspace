package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

func loadPostServiceMQTTSource(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) (string, createFoundationRecoveryArtifact, error) {
	var source createFoundationRecoveryArtifact
	bad := func() (string, createFoundationRecoveryArtifact, error) {
		return "", source, createProviderRecoveryBlock("MQTT trust order recovery requires its original one written API owner, four unwritten prerequisites and accepted core/application repair receipts")
	}
	if len(e.Journal.Entries) == 0 || len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	if isManagedTransportCheckpoint(e.Journal) {
		return loadManagedTransportRecoverySource(store, e, r)
	}
	if isAppTrustCheckpoint(e.Journal) {
		review := ""
		for _, v := range e.Journal.Entries {
			if x := deviceTrustForwardReview(v.Key); x != "" && v.State == "completed" {
				review = x
			}
		}
		a, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil || a.Plan.Materials["accepted-service-core-device-trust-forward-plan"] == "" {
			return bad()
		}
		historical := *e
		historical.Journal = before
		identity, artifact, err := loadPostServiceMQTTSource(store, &historical, r)
		if err != nil {
			return "", source, err
		}
		r.AppTrustRecovery = &createAppTrustRecovery{SourceReview: review}
		return identity, artifact, nil
	}
	if isDeviceTrustCheckpoint(e.Journal) {
		review := ""
		for _, v := range e.Journal.Entries {
			const prefix = "kubernetes/device-root-ready-order-recovery-"
			if strings.HasPrefix(v.Key, prefix) && v.State == "completed" {
				review = strings.TrimPrefix(v.Key, prefix)
			}
		}
		a, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil {
			return "", source, err
		}
		if a.Plan.Materials["accepted-service-core-device-order-forward-plan"] == "" {
			return bad()
		}
		historical := *e
		historical.Journal = before
		identity, artifact, err := loadPostServiceMQTTSource(store, &historical, r)
		if err != nil {
			return "", source, err
		}
		r.DeviceBootstrapRecovery = nil
		r.DeviceTrustRecovery = &createDeviceTrustRecovery{SourceReview: review}
		return identity, artifact, nil
	}
	if e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/device-root-ready" {
		review := ""
		for _, v := range e.Journal.Entries {
			const prefix = "kubernetes/service-managed-used-renewal-"
			if strings.HasPrefix(v.Key, prefix) && v.State == "completed" {
				review = strings.TrimPrefix(v.Key, prefix)
			}
		}
		a, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil {
			return "", source, err
		}
		if a.Plan.Materials["accepted-service-core-openbao-dns-original-request"] == "" {
			return bad()
		}
		historical := *e
		historical.Journal = before
		identity, artifact, err := loadPostServiceMQTTSource(store, &historical, r)
		if err != nil {
			return "", source, err
		}
		r.CompletedIssuerReview = review
		r.UsedRenewalHistory = append(r.UsedRenewalHistory, review)
		r.DeviceBootstrapRecovery = &createDeviceBootstrapRecovery{}
		return identity, artifact, nil
	}
	if e.Journal.Entries[len(e.Journal.Entries)-1].Key == "pki-reserve-openbao_tls-intermediate" {
		review := ""
		for _, v := range e.Journal.Entries {
			const prefix = "kubernetes/openbao-root-trust-name-recovery-"
			if strings.HasPrefix(v.Key, prefix) && v.State == "completed" {
				review = strings.TrimPrefix(v.Key, prefix)
			}
		}
		_, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil {
			return "", source, err
		}
		historical := *e
		historical.Journal = before
		identity, artifact, err := loadPostServiceMQTTSource(store, &historical, r)
		if err != nil {
			return "", source, err
		}
		r.OpenBaoIntermediateRejected, r.CompletedTransportReview = true, review
		return identity, artifact, nil
	}
	if e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/openbao-root-trust" || currentInstallationReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" || currentQuiesceReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" {
		review := ""
		for _, v := range e.Journal.Entries {
			const prefix = "kubernetes/service-managed-used-renewal-"
			if strings.HasPrefix(v.Key, prefix) && v.State == "completed" {
				review = strings.TrimPrefix(v.Key, prefix)
			}
		}
		artifact, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil {
			return "", source, err
		}
		if currentInstallationReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" || currentQuiesceReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" {
			installationReview := ""
			for _, v := range e.Journal.Entries {
				if currentInstallationReview(v.Key) != "" && v.State == "pending" {
					installationReview = currentInstallationReview(v.Key)
				}
			}
			if installationReview != review {
				return bad()
			}
			if err := loadCurrentInstallationFailure(store, e, r, artifact); err != nil {
				return "", source, err
			}
		}
		r.UsedRenewalHistory = []string{review}
		// Walk only admitted native renewals to the original MQTT interruption.
		for len(before.Entries) > 0 && before.Entries[len(before.Entries)-1].Key == "kubernetes/openbao-root-trust" {
			prior := ""
			for _, v := range before.Entries {
				if strings.HasPrefix(v.Key, "kubernetes/service-managed-used-renewal-") && v.State == "completed" {
					prior = strings.TrimPrefix(v.Key, "kubernetes/service-managed-used-renewal-")
				}
			}
			if prior == "" || prior == review {
				return bad()
			}
			_, previous, err := loadAdmittedCreateRecovery(store, e, prior)
			if err != nil {
				return "", source, err
			}
			r.UsedRenewalHistory = append([]string{prior}, r.UsedRenewalHistory...)
			before = previous
		}
		for _, v := range e.Journal.Entries {
			if v.Key == "kubernetes/mqtt-root-trust" && v.State == "completed" {
				var receipt map[string]string
				if json.Unmarshal(v.Receipt, &receipt) != nil {
					return bad()
				}
				r.MQTTAcceptedStage = receipt["actual_qualified_forward_stage"]
			}
		}
		if mqttTrustForwardReview(r.MQTTAcceptedStage) == "" {
			return bad()
		}
		if _, _, err := loadAdmittedCreateRecovery(store, e, mqttTrustForwardReview(r.MQTTAcceptedStage)); err != nil {
			return "", source, err
		}
		for _, stage := range []string{r.MQTTAcceptedStage, "kubernetes/mqtt-intermediate-trust"} {
			plan, _, err := acceptedCreateStage(store, e, stage)
			if err != nil || len(plan.Resources) != 5 {
				return bad()
			}
		}
		r.PostTransportResume, r.UsedRenewalReview = true, review
		historical := *e
		historical.Journal = before
		e = &historical
	}
	var currentPlan *deploymentplan.Plan
	var currentJournal deploymentplan.Journal
	if review := mqttTrustForwardReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key); review != "" {
		var artifact createFoundationRecoveryArtifact
		var before environmentCreateJournal
		base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
		if readExecutionJSON(filepath.Join(base, "qualification.json"), &artifact) != nil || deploymentplan.Digest(artifact.Plan) != review || artifact.Plan.ParentSHA != e.Journal.PlanSHA256 || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 || readExecutionJSON(filepath.Join(base, "source-journal.json"), &before) != nil || deploymentplan.Digest(before) != artifact.Plan.ParentJournalSHA || before.OwnerToken != e.Journal.OwnerToken {
			return bad()
		}
		admitted := false
		for _, v := range e.Journal.Entries {
			if v.Key == "openbao-recovery-admission-"+review && v.State == "completed" {
				var receipt map[string]string
				if json.Unmarshal(v.Receipt, &receipt) == nil && receipt["qualified_recovery_sha256"] == review && v.IntentSHA256 == deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": artifact.Plan.ParentJournalSHA}) {
					admitted = true
				}
			}
		}
		if !admitted {
			return bad()
		}
		currentPlan, currentJournal = &deploymentplan.Plan{}, deploymentplan.Journal{}
		sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
		entry := e.Journal.Entries[len(e.Journal.Entries)-1]
		var candidate deploymentplan.Plan
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), currentPlan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &currentJournal) != nil || currentPlan.SHA256() != sha || currentJournal.PlanSHA256 != sha || currentJournal.Accepted || currentJournal.AcceptanceStarted || currentJournal.InFlight != "" || len(currentPlan.Resources) != 5 || len(currentJournal.Completed) != 3 || len(currentJournal.Owners) != 4 || currentJournal.Failed != "Deployment/"+e.Journal.Stack+"-video-cloud/video-cloud-api" || currentJournal.Owners[currentJournal.Failed] == "" || readExecutionJSON(filepath.Join(base, "mqtt-root-trust-plan.json"), &candidate) != nil || candidate.SHA256() != artifact.Plan.Materials["accepted-service-core-mqtt-forward-plan"] {
			return bad()
		}
		intent, err := environmentCreateKubernetesStageIntent(currentPlan)
		digest, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || digest != entry.IntentSHA256 || len(candidate.Resources) != 5 {
			return bad()
		}
		seen := map[string]bool{}
		for _, id := range currentJournal.Completed {
			if seen[id] || currentJournal.Owners[id] == "" || !strings.HasPrefix(id, "Secret/") && !strings.HasPrefix(id, "ConfigMap/") {
				return bad()
			}
			seen[id] = true
		}
		for i, resource := range currentPlan.Resources {
			if resource.ID != candidate.Resources[i].ID || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(candidate.Resources[i].Desired) || resource.Phase != candidate.Resources[i].Phase {
				return bad()
			}
			if resource.ID != currentJournal.Failed && resource.Desired["kind"] == "Deployment" && currentJournal.Owners[resource.ID] != "" {
				return bad()
			}
		}
		r.UsedRenewalReview, r.MQTTForwardKey = review, entry.Key
		r.UsedRenewalHistory = []string{review}
		historical := *e
		historical.Journal = before
		e = &historical
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if entry.Key != "kubernetes/mqtt-root-trust" || entry.State != "pending" || len(entry.Receipt) != 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	var plan deploymentplan.Plan
	var j deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || plan.SHA256() != sha || j.PlanSHA256 != sha || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(plan.Resources) != 5 || len(j.Completed) != 0 || len(j.Owners) != 1 || j.Failed != "Deployment/"+e.Journal.Stack+"-video-cloud/video-cloud-api" || j.Owners[j.Failed] == "" {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	digest, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || digest != entry.IntentSHA256 {
		return bad()
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		if original := applicationIdentityLockReview(entry.Key); original != "" && entry.State == "completed" {
			var receipt map[string]string
			if json.Unmarshal(entry.Receipt, &receipt) != nil || !strings.HasPrefix(receipt["actual_qualified_forward_stage"], "kubernetes/service-managed-core-identity-recovery-") {
				return bad()
			}
			r.CompletedManagedStage = receipt["actual_qualified_forward_stage"]
			review = original
		}
	}
	current := strings.TrimPrefix(r.CompletedManagedStage, "kubernetes/service-managed-core-identity-recovery-")
	if !environmentPreflightDigest.MatchString(current) || review == "" {
		return bad()
	}
	r.ApplicationStage = "kubernetes/application-client-enrollment-trust-recovery-" + current
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "qualification.json"), &source) != nil || deploymentplan.Digest(source.Plan) != review || source.Plan.ParentSHA != e.Journal.PlanSHA256 || source.Qualification.PlanSHA256 != review || source.Qualification.Decision != "GO" || len(source.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := source.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return bad()
		}
	}
	for _, stage := range []struct {
		key  string
		size int
	}{{r.CompletedManagedStage, 15}, {r.ApplicationStage, 12}} {
		p, _, err := acceptedCreateStage(store, e, stage.key)
		if err != nil || len(p.Resources) != stage.size {
			return bad()
		}
	}
	matched := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/application-client-enrollment" && entry.State == "completed" {
			var receipt map[string]string
			if json.Unmarshal(entry.Receipt, &receipt) == nil && receipt["actual_qualified_forward_stage"] == r.ApplicationStage && receipt["original_failed_plan_sha256"] == source.Plan.Materials["accepted-service-core-application-plan"] {
				matched = true
			}
		}
	}
	if !matched {
		return bad()
	}
	r.PostServiceResume, r.MQTTPendingPlan, r.MQTTPendingJournal = true, &plan, j
	if r.PostTransportResume {
		r.OriginalMQTTPlan = &plan
	}
	if currentPlan != nil {
		r.OriginalMQTTPlan = &plan
		r.MQTTPendingPlan, r.MQTTPendingJournal = currentPlan, currentJournal
	}
	return review, source, nil
}

func prepareMQTTTrustOrderRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	root, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "mqtt")
	if err != nil {
		return err
	}
	r.MQTTRoot, r.MQTTOperation = &root, &op
	for key, value := range p.Materials {
		if strings.HasPrefix(key, "original-mqtt-root-") {
			p.Materials["accepted-service-core-mqtt-"+key] = value
			delete(p.Materials, key)
		}
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	r.GrantImage = image
	p.Materials["accepted-service-core-trust-repair-selection"] = selection
	p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
	var defs []deploymentplan.Definition
	for _, resource := range r.MQTTPendingPlan.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil {
			return err
		}
		if uid := r.MQTTPendingJournal.Owners[resource.ID]; uid != "" {
			if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(resource.Desired, actual) {
				return createProviderRecoveryBlock("original waiting MQTT API owner changed")
			}
		} else if actual != nil {
			return createProviderRecoveryBlock("unattempted MQTT prerequisite or consumer is already present")
		}
		phase := "configuration"
		if resource.Desired["kind"] == "Deployment" {
			phase = "workload"
		} else if resource.Desired["kind"] == "PersistentVolumeClaim" {
			phase = "storage"
		}
		object := deploymentplan.Clone(resource.Desired)
		if r.MQTTForwardKey != "" && object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
				removeCreateBootstrapWorkerSecretRefs(deploymentplan.Map(v))
			}
		}
		defs = append(defs, deploymentplan.Definition{Phase: phase, Object: object})
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("mqtt-trust-prerequisites-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	if len(r.Candidate.Resources) != 5 {
		return createProviderRecoveryBlock("MQTT trust forward needs the exact five original resources")
	}
	for _, resource := range r.Candidate.Resources {
		want := "create"
		if r.MQTTPendingJournal.Owners[resource.ID] != "" {
			want = "retain"
		}
		if resource.ID == r.MQTTPendingJournal.Failed && r.MQTTForwardKey != "" {
			want = "update"
		}
		if resource.Action != want {
			return createProviderRecoveryBlock("MQTT trust forward cannot replace an original body or owner")
		}
	}
	p.Materials["accepted-service-core-mqtt-partial-plan"] = r.MQTTPendingPlan.SHA256()
	p.Materials["accepted-service-core-mqtt-partial-journal"] = deploymentplan.Digest(r.MQTTPendingJournal)
	p.Materials["accepted-service-core-mqtt-forward-plan"] = r.Candidate.SHA256()
	if r.PostServiceRenewal {
		return prepareUsedMQTTBootstrapRenewal(ctx, cfg, store, e, b, p, r)
	}
	return nil
}

func mqttTrustForwardReview(key string) string {
	const prefix = "kubernetes/mqtt-root-trust-order-recovery-"
	sha := strings.TrimPrefix(key, prefix)
	if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(sha) {
		return sha
	}
	return ""
}
