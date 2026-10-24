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

type createAppTrustRecovery struct {
	SourceReview              string
	Root, Device, DeviceReady environmentCreateRegisteredIssuer
	Operation                 environmentCreateIssuerOperation
	Original                  *deploymentplan.Plan
	Journal                   deploymentplan.Journal
	Definitions               []deploymentplan.Definition
}

func isAppTrustCheckpoint(j environmentCreateJournal) bool {
	if len(j.Entries) == 0 || j.Entries[len(j.Entries)-1].Key != "kubernetes/app-root-trust" {
		return false
	}
	seen := map[string]bool{}
	device, root := false, false
	for _, e := range j.Entries {
		if seen[e.Key] {
			return false
		}
		seen[e.Key] = true
		if e.Key == "kubernetes/app-root-trust" {
			if e.State != "pending" || len(e.Receipt) != 0 || !e.CompletedAt.IsZero() || !environmentPreflightDigest.MatchString(e.IntentSHA256) {
				return false
			}
			continue
		}
		if e.State != "completed" || e.CompletedAt.IsZero() || len(e.Receipt) == 0 {
			return false
		}
		if e.Key == "device-root-bootstrap" {
			device = true
		}
		if e.Key == "pki-reserve-app-root" {
			root = true
		}
	}
	return device && root && seen["kubernetes/device-root-active"] && seen["kubernetes/device-root-trust"]
}

func knownAppTrustFailure(p *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if p == nil || len(p.Resources) != 7 || p.SHA256() != j.PlanSHA256 || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+stack+"-video-cloud/mqtt-pki" || len(j.Completed) != 5 || len(j.Owners) != 6 {
		return false
	}
	prefix := stack + "-video-cloud/"
	required := map[string]bool{"ConfigMap/" + prefix + "pki-create-app-trust": true, "ConfigMap/" + prefix + "pki-create-device-trust": true, "Secret/" + prefix + "factoryenroll-runtime": true, "Secret/" + prefix + "video-cloud-runtime": true, "Secret/" + prefix + "video-cloud-workers-runtime": true}
	done := map[string]bool{}
	for _, id := range j.Completed {
		if !required[id] || done[id] || j.Owners[id] == "" {
			return false
		}
		done[id] = true
	}
	seen := map[string]bool{}
	for _, v := range p.Resources {
		if seen[v.ID] || deploymentplan.ID(v.Desired) != v.ID {
			return false
		}
		seen[v.ID] = true
		switch {
		case required[v.ID]:
			if !done[v.ID] {
				return false
			}
		case v.ID == j.Failed:
			if v.Action != "update" || v.UID != j.Owners[v.ID] || v.UID == "" {
				return false
			}
		case v.ID == "Deployment/"+prefix+"video-cloud-api-app-pki":
			if v.Action != "create" || j.Owners[v.ID] != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func loadOriginalAppTrustPlan(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var found *deploymentplan.Plan
	var j deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, j, createProviderRecoveryBlock("App trust continuation requires five completed configurations, the exact owned failed broker, unwritten App API and original parent intent")
	}
	if !isAppTrustCheckpoint(e.Journal) {
		return bad()
	}
	want := e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256
	seen := map[string]bool{}
	for _, lease := range e.Journal.KubernetesLeases {
		sha := strings.SplitN(lease.Holder, ":", 2)[0]
		if !environmentPreflightDigest.MatchString(sha) || seen[sha] {
			continue
		}
		seen[sha] = true
		var p deploymentplan.Plan
		var state deploymentplan.Journal
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &p) != nil || p.SHA256() != sha || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &state) != nil || !knownAppTrustFailure(&p, state, e.Journal.Stack) {
			continue
		}
		intent, err := environmentCreateKubernetesStageIntent(&p)
		digest, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || digest != want {
			continue
		}
		if found != nil {
			return bad()
		}
		found, j = &p, state
	}
	if found == nil {
		return bad()
	}
	return found, j, nil
}

func loadActivatedDeviceForAppTrust(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan) (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer, error) {
	var ready, active environmentCreateRegisteredIssuer
	bad := func() (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer, error) {
		return ready, active, createProviderRecoveryBlock("App trust continuation requires unchanged original Device identity and its accepted ACTIVE native Job")
	}
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device")
	var r, a map[string]any
	if readExecutionJSON(filepath.Join(base, "root-ready.json"), &r) != nil || readExecutionJSON(filepath.Join(base, "root-active.json"), &a) != nil {
		return bad()
	}
	var err error
	ready, err = environmentCreateRegisteredIssuerFromMap(r, "")
	if err != nil {
		return bad()
	}
	active, err = environmentCreateRegisteredIssuerFromMap(a, "")
	if err != nil || validateEnvironmentCreateDeviceRoot(cfg, ready) != nil || validateEnvironmentCreateDeviceRoot(cfg, active) != nil || ready.Status != "ready" || active.Status != "active" || ready.ID != active.ID || ready.Certificate != active.Certificate || ready.ChainPEM != active.ChainPEM || ready.Reference != active.Reference || ready.BundleVersion != active.BundleVersion {
		return bad()
	}
	plan, j, err := acceptedCreateStage(store, e, "kubernetes/device-root-active")
	if err != nil || len(plan.Resources) != 2 {
		return bad()
	}
	bound := false
	for _, v := range plan.Resources {
		if v.Desired["kind"] == "Job" {
			actual, err := b.Read(ctx, v.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
				return bad()
			}
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(v.Desired)["containers"])[0])
			raw, err := b.command(ctx, nil, "--namespace", p.Plan.Stack+"-video-cloud", "logs", "job/"+deploymentplan.String(deploymentplan.Map(v.Desired["metadata"])["name"]), "--container", deploymentplan.String(c["name"]), "--tail=1")
			var result struct {
				Issuer map[string]any `json:"issuer"`
			}
			bound = err == nil && json.Unmarshal(raw, &result) == nil && deploymentplan.Digest(result.Issuer) == deploymentplan.Digest(a)
		}
	}
	if !bound {
		return bad()
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return bad()
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return bad()
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return bad()
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return bad()
	}
	actual, err := client.ReadIssuer(ctx, active.ID)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(a) {
		return bad()
	}
	p.Materials["accepted-service-core-app-trust-device-active"] = deploymentplan.Digest(a)
	return ready, active, nil
}

func prepareAppTrustRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	d := r.AppTrustRecovery
	bad := func() error {
		return createProviderRecoveryBlock("App trust recovery requires its unchanged original READY offline Root, encrypted custody, activated Device and exact partial owner bodies")
	}
	if d == nil || d.Original == nil || r.PostServiceRenewal {
		return bad()
	}
	original, j := d.Original, d.Journal
	if original.Environment != cfg.Environment || original.Stack != p.Plan.Stack || original.ConfigurationSHA256 != p.Plan.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(p.Plan.Release) {
		return bad()
	}
	ready, device, err := loadActivatedDeviceForAppTrust(ctx, cfg, store, e, b, operator, p)
	if err != nil {
		return err
	}
	root, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "app")
	if err != nil {
		return err
	}
	d.Root, d.Operation, d.Device, d.DeviceReady = root, op, device, ready
	bao, baoOp, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "openbao_tls", true)
	if err != nil {
		return err
	}
	r.OpenBaoRoot, r.OpenBaoOperation = &bao, &baoOp
	baoIntermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, bao, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "openbao_tls"))
	if err != nil {
		return err
	}
	r.OpenBaoIntermediate = &baoIntermediate
	mqtt, _, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "mqtt", true)
	if err != nil {
		return err
	}
	r.MQTTRoot = &mqtt
	mqttIntermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, mqtt, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "mqtt"))
	if err != nil {
		return err
	}
	r.MQTTIntermediate = &mqttIntermediate
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	byID := map[string]deploymentplan.Object{}
	for _, v := range live {
		byID[deploymentplan.ID(v)] = v
	}
	owners, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, p.Plan.Bootstrap, p.Plan.Release, store, operator, r.Ceremony)
	if err != nil {
		return err
	}
	owners, err = configureEnvironmentCreateDeviceConsumers(cfg, p.Plan.Bootstrap, ready, owners)
	if err != nil {
		return err
	}
	owners, err = configureEnvironmentCreateAppConsumers(p.Plan.Bootstrap, root, nil, owners)
	if err != nil {
		return err
	}
	expected := environmentCreateApplicationDefinitionsFor(owners, "video-cloud-api-app-pki", "mqtt-pki")
	// Only the missing native enforcement flag differs from the recorded recipe.
	for _, d := range expected {
		if deploymentplan.ID(d.Object) == j.Failed {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pkibroker" {
					c["env"] = environmentCreateServiceWithoutEnv(c["env"], "PKI_BROKER_APP_PKI_ENABLED")
				}
			}
		}
	}
	canonical, err := deploymentplan.Compile(original.Inputs, expected, live)
	if err != nil {
		return err
	}
	intended := map[string]deploymentplan.Object{}
	for _, v := range canonical.Resources {
		intended[v.ID] = v.Desired
	}
	var defs []deploymentplan.Definition
	for _, v := range original.Resources {
		if deploymentplan.Digest(v.Desired) != deploymentplan.Digest(intended[v.ID]) {
			return bad()
		}
		actual := byID[v.ID]
		uid := j.Owners[v.ID]
		if uid != "" {
			if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(v.Desired, actual) {
				return bad()
			}
		} else if actual != nil {
			return bad()
		}
		obj := deploymentplan.Clone(v.Desired)
		if v.ID == j.Failed {
			for _, value := range certIssuerObjectList(environmentPKIPod(obj)["containers"]) {
				c := deploymentplan.Map(value)
				if c["name"] == "pkibroker" {
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BROKER_APP_PKI_ENABLED": "true"}))
				}
			}
		}
		defs = append(defs, deploymentplan.Definition{Phase: v.Phase, Object: obj, Retain: slices.Contains(j.Completed, v.ID)})
	}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("app-trust-broker-enforcement-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	d.Definitions = defs
	r.Candidate = candidate
	p.Materials["accepted-service-core-app-trust-forward-plan"] = candidate.SHA256()
	p.Materials["accepted-service-core-app-trust-root"] = deploymentplan.Digest(root.Document)
	bindAcceptedTransportRootMaterials(p.Materials)
	for key, value := range p.Materials {
		if strings.HasPrefix(key, "original-app-root-") {
			p.Materials["accepted-service-core-"+key] = value
			delete(p.Materials, key)
		}
	}
	return e.Check(ctx)
}

func executeAppTrustForward(ctx context.Context, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.AppTrustRecovery == nil || r.AppTrustRecovery.Original == nil {
		return environmentCreateBootstrapBlocker("App trust continuation is not qualified")
	}
	d := r.AppTrustRecovery
	stage := "app-root-trust-broker-forward-" + r.Review
	if err := apply(ctx, stage, d.Definitions); err != nil {
		return err
	}
	if err := runtime.Execution.RecordReceipt("kubernetes/app-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": d.Original.SHA256(), "original_root_issuer_id": d.Root.ID})); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry("kubernetes/app-root-trust", time.Now().UTC())
}
