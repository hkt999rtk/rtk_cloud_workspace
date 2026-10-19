package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createDeviceTrustRecovery struct {
	SourceReview   string
	Root           environmentCreateRegisteredIssuer
	Original       *deploymentplan.Plan
	Journal        deploymentplan.Journal
	Definitions    []deploymentplan.Definition
	ForwardKeys    []string               `json:"ForwardKeys,omitempty"`
	ForwardPlan    *deploymentplan.Plan   `json:"ForwardPlan,omitempty"`
	ForwardJournal deploymentplan.Journal `json:"-"`
}

// A native READY receipt distinguishes this continuation from absent-issuer
// bootstrap recovery. Neither an allocated UUID nor a completed Job proves it.
func isDeviceTrustCheckpoint(j environmentCreateJournal) bool {
	boundary := len(j.Entries) - 1
	for boundary >= 0 {
		if deviceTrustForwardReview(j.Entries[boundary].Key) != "" {
			e := j.Entries[boundary]
			if e.State != "pending" || !e.CompletedAt.IsZero() || len(e.Receipt) != 0 || !environmentPreflightDigest.MatchString(e.IntentSHA256) {
				return false
			}
			boundary--
			continue
		}
		if !strings.HasPrefix(j.Entries[boundary].Key, "openbao-recovery-admission-") {
			break
		}
		e := j.Entries[boundary]
		review := strings.TrimPrefix(e.Key, "openbao-recovery-admission-")
		var receipt map[string]string
		if !environmentPreflightDigest.MatchString(review) || e.State != "completed" || e.CompletedAt.IsZero() || json.Unmarshal(e.Receipt, &receipt) != nil || receipt["qualified_recovery_sha256"] != review || !environmentPreflightDigest.MatchString(receipt["audit_sha256"]) {
			return false
		}
		boundary--
	}
	if boundary < 0 || j.Entries[boundary].Key != "kubernetes/device-root-trust" {
		return false
	}
	seen := map[string]bool{}
	root, trust := false, false
	for _, e := range j.Entries {
		if seen[e.Key] {
			return false
		}
		seen[e.Key] = true
		if e.State == "completed" && !e.CompletedAt.IsZero() && len(e.Receipt) != 0 {
			continue
		}
		if !environmentPreflightDigest.MatchString(e.IntentSHA256) || !e.CompletedAt.IsZero() {
			return false
		}
		switch e.Key {
		case "device-root-bootstrap":
			var receipt struct {
				Environment string `json:"environment"`
				ID          string `json:"issuer_id"`
				Reference   string `json:"signer_reference"`
				Root        string `json:"root_sha256"`
				Bundle      string `json:"bundle_version"`
			}
			if e.State != "allocated" || json.Unmarshal(e.Receipt, &receipt) != nil || receipt.Environment != j.Environment || !environmentBootstrapOwnerUUID.MatchString(receipt.ID) || receipt.Reference != "pki-issuers/device/"+receipt.ID+"/v1" || !environmentPreflightDigest.MatchString(receipt.Root) || !environmentPreflightDigest.MatchString(receipt.Bundle) {
				return false
			}
			root = true
		case "kubernetes/device-root-trust":
			if e.State != "pending" || len(e.Receipt) != 0 {
				return false
			}
			trust = true
		default:
			if deviceTrustForwardReview(e.Key) == "" || e.State != "pending" || len(e.Receipt) != 0 {
				return false
			}
		}
	}
	return root && trust
}

func knownDeviceTrustFailure(plan *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if plan == nil || len(plan.Resources) != 6 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+stack+"-video-cloud/mqtt-pki" || len(j.Completed) != 4 || len(j.Owners) != 5 {
		return false
	}
	prefix := stack + "-video-cloud/"
	expected := map[string]bool{"ConfigMap/" + prefix + "pki-create-device-trust": true, "Secret/" + prefix + "factoryenroll-runtime": true, "Secret/" + prefix + "video-cloud-runtime": true, "Secret/" + prefix + "video-cloud-workers-runtime": true}
	done := map[string]bool{}
	for _, id := range j.Completed {
		if !expected[id] || done[id] || j.Owners[id] == "" {
			return false
		}
		done[id] = true
	}
	ids := map[string]bool{}
	for _, v := range plan.Resources {
		if ids[v.ID] || deploymentplan.ID(v.Desired) != v.ID {
			return false
		}
		ids[v.ID] = true
		switch {
		case expected[v.ID]:
			if !done[v.ID] {
				return false
			}
		case v.ID == j.Failed:
			if j.Owners[v.ID] == "" || v.Action != "create" {
				return false
			}
		case v.ID == "Deployment/"+prefix+"video-cloud-api-pki":
			if j.Owners[v.ID] != "" || v.Action != "create" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func loadOriginalDeviceTrustPlan(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var found *deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, journal, createProviderRecoveryBlock("Device trust requires the original four completed configuration resources, written failed broker and unwritten API, bound to its original parent intent")
	}
	if !isDeviceTrustCheckpoint(e.Journal) {
		return bad()
	}
	intentSHA := ""
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/device-root-trust" {
			intentSHA = entry.IntentSHA256
		}
	}
	seen := map[string]bool{}
	for _, lease := range e.Journal.KubernetesLeases {
		sha := strings.SplitN(lease.Holder, ":", 2)[0]
		if !environmentPreflightDigest.MatchString(sha) || seen[sha] {
			continue
		}
		seen[sha] = true
		var candidate deploymentplan.Plan
		var state deploymentplan.Journal
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &candidate) != nil || candidate.SHA256() != sha || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &state) != nil || !knownDeviceTrustFailure(&candidate, state, e.Journal.Stack) {
			continue
		}
		intent, err := environmentCreateKubernetesStageIntent(&candidate)
		digest, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || digest != intentSHA {
			continue
		}
		if found != nil {
			return bad()
		}
		found, journal = &candidate, state
	}
	if found == nil {
		return bad()
	}
	return found, journal, nil
}

// Expand the Service CRL receipt policy only after application enrollment. The
// broker is already a selected Device/App consumer with its own managed key.
// No route bypass, static credential or new identity is introduced.
func configureCreateBrokerServiceConsumer(p environmentCreateBootstrapPlan, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if !slices.Contains(findEnvironmentCreateDomainPolicy(p, "device").BundleConsumers, "pkibroker") {
		return nil, environmentCreateBootstrapBlocker("broker Service trust requires its selected native Device consumer policy")
	}
	var out []deploymentplan.Definition
	seen := false
	for _, d := range definitions {
		d.Object = deploymentplan.Clone(d.Object)
		if deploymentplan.ID(d.Object) == "Deployment/"+p.Stack+"-video-cloud/pki-controller" {
			if seen {
				return nil, environmentCreateBootstrapBlocker("duplicate broker Service policy owner")
			}
			seen = true
			containers := certIssuerObjectList(environmentPKIPod(d.Object)["containers"])
			if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != "pki-controller" {
				return nil, environmentCreateBootstrapBlocker("broker Service policy requires its exact native Controller")
			}
			c := deploymentplan.Map(containers[0])
			ids := strings.Split(mqttFoundationEnv(c, "PKI_REQUIRED_CONSUMERS_SERVICE"), ",")
			used := map[string]bool{}
			for _, id := range ids {
				if !environmentCreateConsumerID.MatchString(id) || used[id] {
					return nil, environmentCreateBootstrapBlocker("original Service consumer policy is invalid")
				}
				used[id] = true
			}
			if !used["pkibroker"] {
				ids = append(ids, "pkibroker")
			}
			slices.Sort(ids)
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": strings.Join(ids, ",")}))
		}
		out = append(out, d)
	}
	if !seen {
		return nil, environmentCreateBootstrapBlocker("broker Service policy requires its original Controller owner")
	}
	return out, nil
}

func prepareDeviceTrustRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("Device trust continuation requires its actual READY Root, unchanged OpenBao signer, original completed owners and native broker identity; root creation is forbidden")
	}
	d := r.DeviceTrustRecovery
	if d == nil || r.PostServiceRenewal {
		return createProviderRecoveryBlock("Device trust continuation requires the qualified existing bootstrap session; an expired session needs its own original-key renewal qualification")
	}
	// An admission-only failure has no forward Kubernetes write. Authenticate
	// every trailing admission and its exact source prefix before retrying.
	for i := len(e.Journal.Entries) - 1; i >= 0 && strings.HasPrefix(e.Journal.Entries[i].Key, "openbao-recovery-admission-"); i-- {
		review := strings.TrimPrefix(e.Journal.Entries[i].Key, "openbao-recovery-admission-")
		a, before, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil || !isDeviceTrustCheckpoint(before) || len(before.Entries) != i || deploymentplan.Digest(before.Entries) != deploymentplan.Digest(e.Journal.Entries[:i]) || a.Plan.Materials["accepted-service-core-device-trust-source"] != d.SourceReview || a.Plan.Materials["accepted-service-core-device-trust-forward-plan"] == "" {
			return bad()
		}
	}
	plan, j, err := loadOriginalDeviceTrustPlan(store, e)
	if err != nil {
		return err
	}
	if plan.Environment != cfg.Environment || plan.Stack != p.Plan.Stack || plan.ConfigurationSHA256 != p.Plan.ConfigurationSHA256 || plan.ReleaseSHA256 != deploymentplan.Digest(p.Plan.Release) {
		return bad()
	}
	var intent struct {
		Environment string   `json:"environment"`
		ID          string   `json:"issuer_id"`
		Consumers   []string `json:"bundle_consumers"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-intent.json"), true)
	if err != nil || json.Unmarshal(raw, &intent) != nil || intent.Environment != cfg.Environment || !environmentBootstrapOwnerUUID.MatchString(intent.ID) || !slices.Equal(intent.Consumers, []string{"pkibroker", "video-cloud-api"}) {
		return bad()
	}
	intentSHA, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		return err
	}
	var saved map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-ready.json"), &saved) != nil {
		return bad()
	}
	root, err := environmentCreateRegisteredIssuerFromMap(saved, "")
	if err != nil || validateEnvironmentCreateDeviceRoot(cfg, root) != nil || root.Status != "ready" || root.ID != intent.ID {
		return bad()
	}
	bound := false
	for _, v := range e.Journal.Entries {
		if v.Key == "device-root-bootstrap" && v.State == "allocated" && v.IntentSHA256 == intentSHA {
			expected := deploymentplan.Object{"environment": root.Environment, "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion}
			var receipt map[string]any
			bound = json.Unmarshal(v.Receipt, &receipt) == nil && deploymentplan.Digest(receipt) == deploymentplan.Digest(expected)
		}
	}
	if !bound {
		return bad()
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return err
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return err
	}
	actual, err := client.ReadIssuer(ctx, root.ID)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(saved) {
		return bad()
	}
	token, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/root-token"), true)
	if err != nil {
		return bad()
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, append([]byte(strings.TrimSpace(string(token))), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao secrets list -format=json\n")
	var mounts map[string]any
	if err != nil || json.Unmarshal(raw, &mounts) != nil || mounts[root.Reference+"/"] == nil {
		return bad()
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, append([]byte(strings.TrimSpace(string(token))), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json "+root.Reference+"/issuer/default\n")
	if err != nil {
		return createProviderRecoveryBlock("original Device provider issuer/key binding cannot be inspected")
	}
	signerSHA, err := deviceTrustSignerObservation(raw, root)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-device-trust-signer"] = signerSHA
	p.Materials["accepted-service-core-device-trust-root"] = deploymentplan.Digest(saved)
	p.Materials["accepted-service-core-device-trust-intent"] = intentSHA
	bao, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "openbao_tls", true)
	if err != nil {
		return err
	}
	r.OpenBaoRoot, r.OpenBaoOperation = &bao, &op
	intermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, bao, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "openbao_tls"))
	if err != nil {
		return err
	}
	r.OpenBaoIntermediate = &intermediate
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
	var defs []deploymentplan.Definition
	for _, v := range plan.Resources {
		a := byID[v.ID]
		desired := v.Desired
		uid := j.Owners[v.ID]
		if d.ForwardPlan != nil {
			for _, forward := range d.ForwardPlan.Resources {
				if forward.ID == v.ID && d.ForwardJournal.Owners[v.ID] != "" {
					desired = forward.Desired
					uid = d.ForwardJournal.Owners[v.ID]
				}
			}
		}
		if uid != "" {
			if a == nil || deploymentplan.Map(a["metadata"])["uid"] != uid || !deploymentplan.Matches(desired, a) {
				return bad()
			}
		} else if a != nil {
			return bad()
		}
		defs = append(defs, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired), Retain: slices.Contains(j.Completed, v.ID)})
	}
	// Reconstruct the originally admitted consumers from maintained recipes.
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: b, ServiceSessionRecovery: r}
	owners, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, p.Plan.Bootstrap, p.Plan.Release, store, operator, r.Ceremony)
	if err != nil {
		return err
	}
	owners, err = configureEnvironmentCreateDeviceConsumers(cfg, p.Plan.Bootstrap, root, owners)
	if err != nil {
		return err
	}
	expected := environmentCreateApplicationDefinitionsFor(owners, "video-cloud-api-pki", "mqtt-pki")
	restoreOriginalDeviceTrustCA(expected, plan)
	if len(expected) != len(plan.Resources) {
		return bad()
	}
	canonical, err := deploymentplan.Compile(plan.Inputs, expected, live)
	if err != nil {
		return err
	}
	expectedByID := map[string]deploymentplan.Object{}
	for _, v := range canonical.Resources {
		expectedByID[v.ID] = v.Desired
	}
	for _, v := range plan.Resources {
		if deploymentplan.Digest(v.Desired) != deploymentplan.Digest(expectedByID[v.ID]) {
			return bad()
		}
	}
	// The original native Controller recipe was accepted in the latest transport
	// trust stage. Derive only the explicit Service consumer addition from it.
	core, cj, err := acceptedCreateStage(store, e, "kubernetes/openbao-intermediate-trust")
	if err != nil {
		return err
	}
	var controller deploymentplan.Definition
	for _, v := range core.Resources {
		if v.ID == "Deployment/"+p.Plan.Stack+"-video-cloud/pki-controller" {
			a := byID[v.ID]
			desired, uid := v.Desired, cj.Owners[v.ID]
			if d.ForwardPlan != nil {
				for _, forward := range d.ForwardPlan.Resources {
					if forward.ID == v.ID {
						desired, uid = forward.Desired, d.ForwardJournal.Owners[v.ID]
					}
				}
			}
			if a == nil || deploymentplan.Map(a["metadata"])["uid"] != uid || !deploymentplan.Matches(desired, a) {
				return bad()
			}
			controller = deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Clone(v.Desired)}
		}
	}
	updated, err := configureCreateBrokerServiceConsumer(p.Plan.Bootstrap, []deploymentplan.Definition{controller})
	if err != nil {
		return err
	}
	controller = updated[0]
	defs = deviceTrustForwardDefinitions(defs, controller, j)
	for _, d := range defs {
		if deploymentplan.ID(d.Object) == "Deployment/"+p.Plan.Stack+"-video-cloud/video-cloud-api-pki" {
			deploymentplan.Map(environmentPKIPod(d.Object)["securityContext"])["supplementalGroups"] = []any{float64(0)}
			if err := projectIncomingBootstrapManifest(d.Object, "device"); err != nil {
				return err
			}
		}
	}
	if err := prepareDeviceBrokerTrustDirectory(defs, p.Plan.Bootstrap.Images["LKE_ALPINE_IMAGE"]); err != nil {
		return err
	}
	query := "SELECT json_build_object('broker',(SELECT count(*) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject='service:pkibroker' AND status='succeeded' AND revoked_at IS NULL),'issuer_id',(SELECT issuer_id FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject='service:pkibroker' AND status='succeeded' AND revoked_at IS NULL LIMIT 1))"
	raw, err = readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var broker struct {
		Count  int    `json:"broker"`
		Issuer string `json:"issuer_id"`
	}
	if err != nil || json.Unmarshal(raw, &broker) != nil || broker.Count != 1 || broker.Issuer != r.Ceremony.Intermediate.ID {
		return bad()
	}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("device-trust-service-policy-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	d.Root, d.Original, d.Journal, d.Definitions = root, plan, j, defs
	r.Candidate = candidate
	p.Materials["accepted-service-core-device-trust-original-plan"] = plan.SHA256()
	p.Materials["accepted-service-core-device-trust-original-journal"] = deploymentplan.Digest(j)
	p.Materials["accepted-service-core-device-trust-forward-plan"] = candidate.SHA256()
	bindAcceptedTransportRootMaterials(p.Materials)
	return e.Check(ctx)
}

func mustStringJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func executeDeviceTrustForward(ctx context.Context, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.DeviceTrustRecovery == nil || r.DeviceTrustRecovery.Original == nil {
		return environmentCreateBootstrapBlocker("Device trust continuation is not qualified")
	}
	d := r.DeviceTrustRecovery
	stage := "device-root-trust-policy-forward-" + r.Review
	if err := apply(ctx, stage, d.Definitions); err != nil {
		return err
	}
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": d.Original.SHA256(), "original_root_issuer_id": d.Root.ID})
	if err := runtime.Execution.RecordReceipt("kubernetes/device-root-trust", receipt); err != nil {
		return err
	}
	for _, key := range d.ForwardKeys {
		if err := runtime.Execution.RecordReceipt(key, receipt); err != nil {
			return err
		}
		if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return runtime.Execution.CompleteEntry("kubernetes/device-root-trust", time.Now().UTC())
}

// Device bootstrap creates its own native operation. Authenticate the actual
// accepted Job result and compare exact registry identities, rather than copying
// an earlier issuer/operation count or guessing an operation from a Job suffix.
func verifyDeviceTrustRegistry(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) error {
	bad := func() error {
		return createProviderRecoveryBlock("Device trust registry differs from original reservation receipts or the accepted native READY Job result")
	}
	review := p.Materials["accepted-service-core-device-trust-source"]
	stage, j, err := acceptedCreateStage(store, e, "kubernetes/device-root-ready-order-recovery-"+review)
	if err != nil {
		return err
	}
	var result struct {
		Result struct {
			ID string `json:"operation_id"`
		} `json:"result"`
		Issuer map[string]any `json:"issuer"`
	}
	var output []byte
	for _, v := range stage.Resources {
		if v.Desired["kind"] == "Job" {
			actual, err := b.Read(ctx, v.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
				return bad()
			}
			containers := certIssuerObjectList(environmentPKIPod(v.Desired)["containers"])
			if len(containers) != 1 {
				return bad()
			}
			output, err = b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "logs", "job/"+deploymentplan.String(deploymentplan.Map(v.Desired["metadata"])["name"]), "--container", deploymentplan.String(deploymentplan.Map(containers[0])["name"]))
			if err != nil || json.Unmarshal(output, &result) != nil || !environmentBootstrapOwnerUUID.MatchString(result.Result.ID) {
				return bad()
			}
		}
	}
	var saved map[string]any
	if len(output) == 0 || readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-ready.json"), &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(result.Issuer) {
		return bad()
	}
	device := deploymentplan.String(saved["issuer_id"])
	if !environmentBootstrapOwnerUUID.MatchString(device) {
		return bad()
	}
	issuers, operations, err := deviceTrustRegistryIdentities(e.Journal, device, result.Result.ID)
	if err != nil {
		return err
	}
	query := "SELECT json_build_object('issuers',(SELECT json_agg(id ORDER BY id) FROM pki_issuers),'operations',(SELECT json_agg(json_build_object('id',id,'issuer_id',issuer_id) ORDER BY id) FROM pki_operations))"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, query)
	var registry struct {
		Issuers    []string `json:"issuers"`
		Operations []struct {
			ID     string `json:"id"`
			Issuer string `json:"issuer_id"`
		} `json:"operations"`
	}
	if err != nil || json.Unmarshal(raw, &registry) != nil || len(registry.Issuers) != len(issuers) || len(registry.Operations) != len(operations) {
		return bad()
	}
	for _, id := range registry.Issuers {
		if !issuers[id] {
			return bad()
		}
		delete(issuers, id)
	}
	for _, op := range registry.Operations {
		if operations[op.ID] != op.Issuer || op.Issuer == "" {
			return bad()
		}
		delete(operations, op.ID)
	}
	if len(issuers) != 0 || len(operations) != 0 {
		return bad()
	}
	p.Materials["accepted-service-core-device-trust-native-ready-result"] = environmentPreflightBytesSHA256(output)
	p.Materials["accepted-service-core-device-trust-registry"] = deploymentplan.Digest(registry)
	return nil
}

func deviceTrustRegistryIdentities(j environmentCreateJournal, device, nativeOperation string) (map[string]bool, map[string]string, error) {
	bad := func() (map[string]bool, map[string]string, error) {
		return nil, nil, createProviderRecoveryBlock("original reservation aliases must carry identical native operation, issuer and request-digest receipts")
	}
	if !environmentBootstrapOwnerUUID.MatchString(device) || !environmentBootstrapOwnerUUID.MatchString(nativeOperation) {
		return bad()
	}
	issuers := map[string]bool{device: true}
	operations := map[string]string{nativeOperation: device}
	receipts := map[string]string{}
	for _, entry := range j.Entries {
		if !strings.HasPrefix(entry.Key, "pki-reserve-") || entry.State != "completed" {
			continue
		}
		var op environmentCreateIssuerOperation
		if json.Unmarshal(entry.Receipt, &op) != nil || !environmentBootstrapOwnerUUID.MatchString(op.ID) || !environmentBootstrapOwnerUUID.MatchString(op.IssuerID) || !environmentPreflightDigest.MatchString(op.Digest) {
			return bad()
		}
		digest := deploymentplan.Digest(op)
		if operations[op.ID] != "" {
			if operations[op.ID] != op.IssuerID || receipts[op.ID] != digest {
				return bad()
			}
			continue
		}
		if issuers[op.IssuerID] {
			return bad()
		}
		operations[op.ID] = op.IssuerID
		issuers[op.IssuerID] = true
		receipts[op.ID] = digest
	}
	if len(issuers) < 2 || len(issuers) != len(operations) {
		return bad()
	}
	return issuers, operations, nil
}

// Changing only the broker's public policy revision triggers an owned rollout
// after the Controller admits it, instead of waiting on an old CrashLoop timer.
func deviceTrustForwardDefinitions(original []deploymentplan.Definition, controller deploymentplan.Definition, j deploymentplan.Journal) []deploymentplan.Definition {
	out := make([]deploymentplan.Definition, 0, len(original)+1)
	policySHA := deploymentplan.Digest(controller.Object)
	for _, d := range original {
		d.Object = deploymentplan.Clone(d.Object)
		d.Dependencies = slices.Clone(d.Dependencies)
		if d.Object["kind"] == "Deployment" {
			d.Dependencies = append(d.Dependencies, deploymentplan.ID(controller.Object))
			if deploymentplan.ID(d.Object) == j.Failed {
				for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
					c := deploymentplan.Map(value)
					if c["name"] == "pkibroker" {
						c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BROKER_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}))
					}
				}
				setCreateBundleRollout(d.Object, "service-consumer-policy", policySHA)
			}
		}
		out = append(out, d)
	}
	controller.Object = deploymentplan.Clone(controller.Object)
	out = append(out, controller)
	return out
}

func deviceTrustSignerObservation(raw []byte, root environmentCreateRegisteredIssuer) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("Device provider must retain the registered READY Root certificate, actual issuer/key UUIDs and certificate/CRL signing usage")
	}
	var observation struct {
		Data struct {
			Certificate string   `json:"certificate"`
			Chain       []string `json:"ca_chain"`
			Issuer      string   `json:"issuer_id"`
			Key         string   `json:"key_id"`
			Usage       string   `json:"usage"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &observation) != nil {
		return bad()
	}
	d := observation.Data
	for _, id := range []string{d.Issuer, d.Key} {
		if !environmentBootstrapOwnerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return bad()
		}
	}
	certificate, err := parseFirstCertificatePEM([]byte(d.Certificate))
	if err != nil || certificate == nil || environmentPreflightBytesSHA256(certificate.Raw) != root.RootFingerprint || len(d.Chain) != 1 || strings.TrimSpace(d.Chain[0]) != strings.TrimSpace(d.Certificate) {
		return bad()
	}
	usage := map[string]bool{}
	for _, v := range strings.Split(d.Usage, ",") {
		usage[strings.TrimSpace(v)] = true
	}
	if !usage["issuing-certificates"] || !usage["crl-signing"] {
		return bad()
	}
	return deploymentplan.Digest(map[string]string{"provider_issuer_id": d.Issuer, "provider_key_id": d.Key, "certificate_sha256": root.RootFingerprint}), nil
}
