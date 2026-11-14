package main

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const serviceSessionReferenceKey = "bootstrap-session"

type serviceSessionReference struct {
	Stage, SourceStage, SourcePlanSHA, SourceJournalSHA, UID string
	HostID, HostStage, HostPlanSHA, HostJournalSHA, HostUID  string
	Plan                                                     *deploymentplan.Plan
}

// Resolve the projected ConfigMap from the original host's environment and
// read-only whole-directory volume, rather than inventing a ConfigMap name.
func serviceSessionTrustMapID(host deploymentplan.Object) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("original OpenBao trust projection is unavailable")
	}
	if host == nil || host["kind"] != "StatefulSet" {
		return bad()
	}
	pod := environmentPKIPod(host)
	containers, err := environmentPKINamedFields(pod["containers"])
	if err != nil || containers["openbao-pki"] == nil {
		return bad()
	}
	container := containers["openbao-pki"]
	env, err := environmentPKINamedFields(container["env"])
	if err != nil || env["OPENBAO_PKI_HOST_RENEWAL_TLS_CA"] == nil {
		return bad()
	}
	ca := env["OPENBAO_PKI_HOST_RENEWAL_TLS_CA"]
	if ca["valueFrom"] != nil {
		return bad()
	}
	path := deploymentplan.String(ca["value"])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return bad()
	}
	var mount deploymentplan.Object
	for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if m["mountPath"] == filepath.Dir(path) {
			if mount != nil || m["readOnly"] != true || deploymentplan.String(m["subPath"]) != "" || deploymentplan.String(m["subPathExpr"]) != "" {
				return bad()
			}
			mount = m
		}
	}
	if mount == nil {
		return bad()
	}
	volumes, err := environmentPKINamedFields(pod["volumes"])
	if err != nil {
		return bad()
	}
	volume := volumes[deploymentplan.String(mount["name"])]
	projection := deploymentplan.Map(volume["configMap"])
	name := deploymentplan.String(projection["name"])
	namespace := deploymentplan.String(deploymentplan.Map(host["metadata"])["namespace"])
	if items, present := projection["items"]; present && items != nil {
		list, valid := items.([]any)
		if !valid || len(list) != 0 {
			return bad()
		}
	}
	if name == "" || strings.Contains(name, "/") || namespace == "" || strings.Contains(namespace, "/") {
		return bad()
	}
	return "ConfigMap/" + namespace + "/" + name, nil
}

// The native ConfigMap update is compiled from its authenticated original
// owner and full current data, changing only the public session reference.
func prepareServiceSessionReference(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, original, previous, next string, priorSessions ...string) (*serviceSessionReference, error) {
	bad := func() (*serviceSessionReference, error) {
		return nil, createProviderRecoveryBlock("original OpenBao service-trust ConfigMap reference is unavailable")
	}
	if e == nil || b == nil || !environmentBootstrapOwnerUUID.MatchString(original) || !environmentBootstrapOwnerUUID.MatchString(previous) || !environmentBootstrapOwnerUUID.MatchString(next) || next == original || next == previous || e.Check(ctx) != nil {
		return bad()
	}
	hostID := "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
	hostSources, err := loadServiceSessionCoreSources(ctx, store, e, b, []string{hostID})
	if err != nil || len(hostSources) != 1 {
		return bad()
	}
	hostSource := hostSources[0]
	host, err := b.Read(ctx, hostID)
	if err != nil || host == nil || deploymentplan.Map(host["metadata"])["uid"] != hostSource.Journal.Owners[hostID] {
		return bad()
	}
	id, err := serviceSessionTrustMapID(host)
	if err != nil {
		return bad()
	}
	sources, err := loadServiceSessionCoreSources(ctx, store, e, b, []string{id})
	if err != nil || len(sources) != 1 {
		return bad()
	}
	source := sources[0]
	current, err := b.Read(ctx, id)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != source.Journal.Owners[id] {
		return bad()
	}
	data := deploymentplan.Map(current["data"])
	if data == nil {
		return bad()
	}
	if prior, ok := data[serviceSessionReferenceKey]; ok && (prior != original && prior != previous && !slices.Contains(priorSessions, deploymentplan.String(prior))) {
		return bad()
	}
	desired := deploymentplan.Projection(current)
	deploymentplan.Map(desired["metadata"])["uid"] = source.Journal.Owners[id]
	deploymentplan.Map(desired["data"])[serviceSessionReferenceKey] = next
	stage := "service-session-reference-" + deploymentplan.Digest(next)[:16]
	defs := []deploymentplan.Definition{{Phase: "configuration", Object: desired}}
	in := source.Plan.Inputs
	in.Operation = "scoped"
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	plan, err := deploymentplan.Compile(in, defs, []deploymentplan.Object{current})
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "update" || plan.Resources[0].UID != source.Journal.Owners[id] {
		return bad()
	}
	return &serviceSessionReference{Stage: stage, SourceStage: source.Stage, SourcePlanSHA: source.Plan.SHA256(), SourceJournalSHA: deploymentplan.Digest(source.Journal), UID: plan.Resources[0].UID, HostID: hostID, HostStage: hostSource.Stage, HostPlanSHA: hostSource.Plan.SHA256(), HostJournalSHA: deploymentplan.Digest(hostSource.Journal), HostUID: hostSource.Journal.Owners[hostID], Plan: plan}, nil
}

func validServiceSessionReference(q secondSessionAdmission) bool {
	r := q.Reference
	if r == nil || r.Plan == nil || !secretEnvironmentPattern.MatchString(r.Stage) || !secretEnvironmentPattern.MatchString(r.SourceStage) || !secretEnvironmentPattern.MatchString(r.HostStage) || !environmentPreflightDigest.MatchString(r.SourcePlanSHA) || !environmentPreflightDigest.MatchString(r.SourceJournalSHA) || !environmentPreflightDigest.MatchString(r.HostPlanSHA) || !environmentPreflightDigest.MatchString(r.HostJournalSHA) || !environmentBootstrapOwnerUUID.MatchString(r.UID) || !environmentBootstrapOwnerUUID.MatchString(r.HostUID) || r.Stage != "service-session-reference-"+deploymentplan.Digest(q.Renewal.RenewedIntent.SessionID)[:16] {
		return false
	}
	p := r.Plan
	if p.Version != deploymentplan.Version || p.Environment != q.Environment || p.Stack != q.SourceJournal.Stack || p.Operation != "scoped" || len(p.Resources) != 1 {
		return false
	}
	resource := p.Resources[0]
	ownerFound := false
	for _, batch := range q.Batches {
		for _, pending := range batch.Pending {
			if pending.ID == r.HostID && pending.Reason == "non-deployment" {
				ownerFound = true
			}
		}
	}
	parts := strings.Split(r.HostID, "/")
	if !ownerFound || len(parts) != 3 || parts[0] != "StatefulSet" || parts[1] == "" || parts[2] == "" || !strings.HasPrefix(resource.ID, "ConfigMap/"+parts[1]+"/") || resource.Action != "update" || resource.UID != r.UID || resource.Baseline == nil || deploymentplan.ID(resource.Desired) != resource.ID {
		return false
	}
	before := deploymentplan.Clone(resource.Baseline)
	after := deploymentplan.Clone(resource.Desired)
	beforeData, afterData := deploymentplan.Map(before["data"]), deploymentplan.Map(after["data"])
	if beforeData == nil || afterData == nil || afterData[serviceSessionReferenceKey] != q.Renewal.RenewedIntent.SessionID {
		return false
	}
	if prior, ok := beforeData[serviceSessionReferenceKey]; ok && prior != q.Renewal.OriginalState.Session && prior != q.Renewal.Intent.SessionID && !slices.Contains(serviceSessionBatchPrevious(q), deploymentplan.String(prior)) {
		return false
	}
	delete(beforeData, serviceSessionReferenceKey)
	delete(afterData, serviceSessionReferenceKey)
	return deploymentplan.Digest(beforeData) == deploymentplan.Digest(afterData) && deploymentplan.Matches(before, after) && deploymentplan.Matches(after, before)
}

// Source evidence is immutable even after the reference itself is updated.
func verifyServiceSessionReferenceSource(store secretStore, e *environmentCreateExecution, q secondSessionAdmission) error {
	if e == nil || !validServiceSessionReference(q) || e.Check(context.Background()) != nil {
		return createProviderRecoveryBlock("original OpenBao service-trust ConfigMap evidence changed")
	}
	r := q.Reference
	p, j, err := serviceSessionReferenceRecordedSource(store, e, r.SourceStage, map[string]bool{})
	if err != nil || p == nil || p.SHA256() != r.SourcePlanSHA || deploymentplan.Digest(j) != r.SourceJournalSHA || j.Owners[r.Plan.Resources[0].ID] != r.UID {
		return createProviderRecoveryBlock("original OpenBao service-trust ConfigMap source changed")
	}
	hostPlan, hostJournal, err := serviceSessionReferenceNativeSource(store, e, r.HostStage)
	if err != nil || hostPlan == nil || hostPlan.SHA256() != r.HostPlanSHA || deploymentplan.Digest(hostJournal) != r.HostJournalSHA || hostJournal.Owners[r.HostID] != r.HostUID {
		return createProviderRecoveryBlock("original OpenBao trust projection owner changed")
	}
	hostChecked, exists, err := readEnvironmentCreateStageProgress(store, e, r.HostStage, hostPlan)
	if err != nil || !exists || deploymentplan.Digest(hostChecked) != r.HostJournalSHA {
		return createProviderRecoveryBlock("original OpenBao trust projection parent or child changed")
	}
	var host deploymentplan.Object
	for _, resource := range hostPlan.Resources {
		if resource.ID == r.HostID {
			if host != nil {
				return createProviderRecoveryBlock("original OpenBao trust projection is ambiguous")
			}
			host = resource.Desired
		}
	}
	id, err := serviceSessionTrustMapID(host)
	if err != nil || id != r.Plan.Resources[0].ID {
		return createProviderRecoveryBlock("original OpenBao trust projection target changed")
	}
	return nil
}

// Execute the admitted single-key ConfigMap update through the original
// Kubernetes lease and child journal. Resource.Body preserves the current UID,
// resourceVersion and all other data; Kubernetes replace rejects stale writes.
func executeServiceSessionReference(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q secondSessionAdmission, out io.Writer) error {
	if e == nil || b == nil || out == nil || !validSecondSessionAdmission(store, e, q) || verifyServiceSessionReferenceSource(store, e, q) != nil {
		return createProviderRecoveryBlock("original OpenBao service-trust reference requires its admitted source")
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return p.AdmitResumable(ctx, b, store.Root)
	}
	return executeEnvironmentCreateStagePlan(ctx, store, e, q.Reference.Stage, q.Reference.Plan, b, qualify, check, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.Verify(ctx, b)
	}, func(context.Context) error { return nil }, out)
}
