package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

type createDeviceBootstrapRecovery struct {
	RootID      string
	Original    *deploymentplan.Plan
	Journal     deploymentplan.Journal
	Definitions []deploymentplan.Definition
}

// Resume only the original unstarted Device request. A terminal Job is kept as
// evidence; a separately qualified forward Job uses the same UUID and recipe.
func prepareDeviceBootstrapOrderRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("Device order recovery requires the original unstarted Root UUID, terminal owned Job, absent ServiceAccount and absent native signer")
	}
	if r.PostServiceRenewal {
		return createProviderRecoveryBlock("Device order recovery requires the existing qualified bootstrap credential to remain valid")
	}
	d := r.DeviceBootstrapRecovery
	plan, j, err := originalPendingServicePlan(store, e, "kubernetes/device-root-ready", 2)
	if err != nil {
		return err
	}
	if j.AcceptanceStarted || j.Failed == "" || plan.Environment != cfg.Environment || plan.Stack != p.Plan.Stack || plan.ConfigurationSHA256 != p.Plan.ConfigurationSHA256 || plan.ReleaseSHA256 != deploymentplan.Digest(p.Plan.Release) {
		return bad()
	}
	var intent struct {
		Environment string   `json:"environment"`
		ID          string   `json:"issuer_id"`
		Consumers   []string `json:"bundle_consumers"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-intent.json"), true)
	if err != nil || json.Unmarshal(raw, &intent) != nil || intent.Environment != cfg.Environment || !environmentBootstrapOwnerUUID.MatchString(intent.ID) || deploymentplan.Digest(intent.Consumers) != deploymentplan.Digest([]string{"pkibroker", "video-cloud-api"}) {
		return bad()
	}
	rootIntentSHA := environmentPreflightBytesSHA256(raw)
	intentSHA, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return err
	}
	bound := false
	for _, v := range e.Journal.Entries {
		if v.Key == "device-root-bootstrap" && v.State == "pending" && len(v.Receipt) == 0 && v.IntentSHA256 == intentSHA {
			bound = true
		}
	}
	if !bound {
		return bad()
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	byID := map[string]deploymentplan.Object{}
	for _, v := range live {
		byID[deploymentplan.ID(v)] = v
	}
	var job deploymentplan.Object
	for _, v := range plan.Resources {
		actual := byID[v.ID]
		switch v.Desired["kind"] {
		case "Job":
			if v.ID != j.Failed || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
				return bad()
			}
			job = actual
			terminal := false
			for _, c := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
				condition := deploymentplan.Map(c)
				if condition["type"] == "Failed" && condition["status"] == "True" && condition["reason"] == "DeadlineExceeded" {
					terminal = true
				}
			}
			if !terminal {
				return bad()
			}
		case "ServiceAccount":
			if actual != nil || j.Owners[v.ID] != "" || v.ID != "ServiceAccount/"+p.Plan.Stack+"-video-cloud/pki-device-bootstrap" {
				return bad()
			}
		default:
			return bad()
		}
	}
	if job == nil {
		return bad()
	}
	uid := deploymentplan.Map(job["metadata"])["uid"]
	for _, v := range live {
		if v["kind"] == "Pod" {
			for _, owner := range certIssuerObjectList(deploymentplan.Map(v["metadata"])["ownerReferences"]) {
				if deploymentplan.Map(owner)["uid"] == uid {
					return bad()
				}
			}
		}
	}
	query := "SELECT json_build_object('issuers',(SELECT count(*) FROM pki_issuers WHERE id='" + intent.ID + "'))"
	raw, err = readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, query)
	var count struct {
		Issuers int `json:"issuers"`
	}
	if err != nil || json.Unmarshal(raw, &count) != nil || count.Issuers != 0 {
		return bad()
	}
	token, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/root-token"), true)
	if err != nil {
		return bad()
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao secrets list -format=json\n")
	var mounts map[string]any
	if err != nil || json.Unmarshal(raw, &mounts) != nil || mounts["pki-issuers/device/"+intent.ID+"/v1/"] != nil {
		return bad()
	}
	root, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "openbao_tls", true)
	if err != nil {
		return err
	}
	r.OpenBaoRoot, r.OpenBaoOperation = &root, &op
	i, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, root, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "openbao_tls"))
	if err != nil {
		return err
	}
	r.OpenBaoIntermediate = &i
	mqtt, _, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "mqtt", true)
	if err != nil {
		return err
	}
	r.MQTTRoot = &mqtt
	i, err = loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, mqtt, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "mqtt"))
	if err != nil {
		return err
	}
	r.MQTTIntermediate = &i
	defs, err := renderEnvironmentCreateDeviceBootstrapJob(ctx, cfg, p.Plan.Bootstrap, intent.ID, "ready")
	if err != nil {
		return err
	}
	for _, v := range defs {
		matched := false
		for _, old := range plan.Resources {
			if old.Desired["kind"] == v.Object["kind"] {
				matched = deploymentplan.Digest(old.Desired) == deploymentplan.Digest(v.Object)
			}
		}
		if !matched {
			return bad()
		}
	}
	for _, v := range defs {
		if v.Object["kind"] == "Job" {
			m := deploymentplan.Map(v.Object["metadata"])
			m["name"] = deploymentplan.String(m["name"]) + "-order-forward"
			if len(deploymentplan.String(m["name"])) > 63 {
				return bad()
			}
		}
	}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("device-root-order-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	d.RootID, d.Original, d.Journal, d.Definitions = intent.ID, plan, j, defs
	r.Candidate = candidate
	p.Materials["accepted-service-core-device-order-original-plan"] = plan.SHA256()
	p.Materials["accepted-service-core-device-order-original-journal"] = deploymentplan.Digest(j)
	p.Materials["accepted-service-core-device-order-root-intent"] = rootIntentSHA
	p.Materials["accepted-service-core-device-order-forward-plan"] = candidate.SHA256()
	bindAcceptedTransportRootMaterials(p.Materials)
	return e.Check(ctx)
}

func executeDeviceBootstrapForward(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	d := r.DeviceBootstrapRecovery
	if d == nil || d.Original == nil || !environmentBootstrapOwnerUUID.MatchString(d.RootID) {
		return environmentCreateBootstrapBlocker("Device forward checkpoint is not qualified")
	}
	stage := "device-root-ready-order-recovery-" + r.Review
	if err := apply(ctx, stage, d.Definitions); err != nil {
		return err
	}
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": d.Original.SHA256(), "original_root_issuer_id": d.RootID})
	if err := runtime.Execution.RecordReceipt("kubernetes/device-root-ready", receipt); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry("kubernetes/device-root-ready", time.Now().UTC())
}
