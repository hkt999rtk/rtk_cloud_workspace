package main

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const productPublicCRLStage = "product-initial-crls"

// Reuse the application initializer: public records come from the registered
// issuer in the rendered manifest, never from an empty replacement or signing.
func renderCreateProductPublicCRLs(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil, nil
	}
	products := map[string]bool{}
	apps := map[string]bool{}
	for _, a := range environmentCreateApplicationOwners() {
		apps[a.Name] = true
	}
	for _, product := range p.ProductIdentities {
		name := environmentCustodyProductWorkload(strings.TrimPrefix(product.Subject, "service:"))
		if name == "" {
			return nil, environmentCreateBootstrapBlocker("product public CRL owner is absent from the maintained catalog")
		}
		if !apps[name] {
			products[name] = true
		}
	}
	var selected []deploymentplan.Definition
	allowed := map[string]environmentCreateRegisteredIssuer{}
	for _, d := range definitions {
		if d.Object["kind"] == "Deployment" && products[deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])] {
			selected = append(selected, d)
		}
		if d.Object["kind"] != "ConfigMap" {
			continue
		}
		selected = append(selected, d)
		for _, v := range deploymentplan.Map(d.Object["data"]) {
			var entries []struct {
				Issuer deploymentplan.Object `json:"issuer"`
				State  string                `json:"state_path"`
			}
			if json.Unmarshal([]byte(deploymentplan.String(v)), &entries) != nil {
				continue
			}
			for _, entry := range entries {
				doc := entry.Issuer
				id := deploymentplan.String(doc["issuer_id"])
				if id == "" || entry.State == "" {
					continue
				}
				if old, ok := allowed[id]; ok && old.Document["certificate_fingerprint_sha256"] != doc["certificate_fingerprint_sha256"] {
					return nil, environmentCreateBootstrapBlocker("product public CRL manifests disagree on original issuer")
				}
				allowed[id] = environmentCreateRegisteredIssuer{ID: id, Environment: deploymentplan.String(doc["environment"]), Domain: deploymentplan.String(doc["trust_domain"]), Status: deploymentplan.String(doc["status"]), Document: doc}
			}
		}
	}
	var issuers []environmentCreateRegisteredIssuer
	for _, v := range allowed {
		issuers = append(issuers, v)
	}
	sort.Slice(issuers, func(i, j int) bool { return issuers[i].ID < issuers[j].ID })
	return renderEnvironmentCreateApplicationCRLInitialization(ctx, cfg, p, runtime, service, selected, issuers)
}

// Read the literal known parent effects, including a partially written normal
// workload. Only selected product owners and their referenced public ConfigMaps
// enter this source; readiness is the following consumer's responsibility.
func recordedProductCRLDefinitions(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, p environmentCreateBootstrapPlan) ([]deploymentplan.Definition, error) {
	history, _, err := serviceSessionOwnerHistory(ctx, store, e)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, product := range p.ProductIdentities {
		wanted["Deployment/"+p.Stack+"-video-cloud/"+environmentCustodyProductWorkload(strings.TrimPrefix(product.Subject, "service:"))] = true
	}
	known := map[string]bool{}
	for _, s := range history {
		for n, r := range s.Plan.Resources {
			if wanted[r.ID] && (n < len(s.Journal.Completed) && s.Journal.Completed[n] == r.ID || s.Journal.Failed == r.ID && s.Journal.Owners[r.ID] != "") {
				known[r.ID] = true
			}
		}
	}
	ids := []string{}
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	sources, err := loadServiceSessionCoreSources(ctx, store, e, b, ids)
	if err != nil {
		return nil, err
	}
	var defs []deploymentplan.Definition
	configs := map[string]bool{}
	for _, source := range sources {
		for _, r := range source.Plan.Resources {
			if !source.SelectedIDs[r.ID] {
				continue
			}
			defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
			pod := environmentPKIPod(r.Desired)
			needed := map[string]bool{}
			for _, raw := range certIssuerObjectList(pod["containers"]) {
				c := deploymentplan.Map(raw)
				for _, f := range certIssuerObjectList(c["env"]) {
					v := deploymentplan.Map(f)
					if !strings.HasSuffix(deploymentplan.String(v["name"]), "_CRL_MANIFEST") || v["value"] == nil {
						continue
					}
					path := deploymentplan.String(v["value"])
					for _, mr := range certIssuerObjectList(c["volumeMounts"]) {
						m := deploymentplan.Map(mr)
						mount := deploymentplan.String(m["mountPath"])
						if mount != "" && (path == mount || strings.HasPrefix(path, strings.TrimSuffix(mount, "/")+"/")) {
							needed[deploymentplan.String(m["name"])] = true
						}
					}
				}
			}
			for _, vr := range certIssuerObjectList(pod["volumes"]) {
				v := deploymentplan.Map(vr)
				name := deploymentplan.String(deploymentplan.Map(v["configMap"])["name"])
				if name != "" && needed[deploymentplan.String(v["name"])] {
					configs["ConfigMap/"+p.Stack+"-video-cloud/"+name] = true
				}
			}
		}
	}
	ids = nil
	for id := range configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		sources, err = loadServiceSessionCoreSources(ctx, store, e, b, ids)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			for _, r := range source.Plan.Resources {
				if source.SelectedIDs[r.ID] {
					defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
				}
			}
		}
	}
	return defs, e.Check(ctx)
}

// Compatibility continuation uses the same shared producer as a new create.
// The immutable native child is stored at preflight and never regenerated from
// partial Job status on retry. It owns public records only, with no private-key
// Lease or bootstrap credential, and cannot count as whole-environment success.
func tryUnifiedProductPublicCRLs(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, parent *environmentCreatePlan, out, diagnostics io.Writer) (bool, error) {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return false, nil
	}
	// Accepted initialization (including a verified failure resolution) is
	// already native evidence; do not reselect a tool or add a CI dependency.
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+productPublicCRLStage); entry != nil && entry.State == "completed" {
		_, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, native, entry.Key)
		return err != nil, err
	}
	tool, err := selectFailedForwardTool(ctx, store, e, environmentCIImageCommand(cfg))
	if err != nil {
		return true, err
	}
	stage := productPublicCRLStage + "-" + tool.Image.SourceRevision[:12]
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage); entry != nil && entry.State == "completed" {
		if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, native, entry.Key); err != nil {
			return true, err
		}
		if old := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+productPublicCRLStage); old != nil && old.State != "completed" {
			source, err := readEnvironmentCreateStageInput(store, e, productPublicCRLStage)
			if err != nil {
				return true, err
			}
			next, _, err := acceptedCreateStage(store, e, entry.Key)
			if err != nil {
				return true, err
			}
			if o.Action == "create" {
				if err := completeProductCRLForward(ctx, store, e, source, stage, next); err != nil {
					return true, err
				}
			} else {
				return true, createProviderRecoveryBlock("resume the accepted product CRL successor to complete its original failure resolution")
			}
		}
		return false, nil
	}
	programme, err := readFreshProductProgram(store, e)
	if err != nil {
		return true, err
	}
	installed, err := readProductInstalled(store, &programme.Lifecycle)
	if err != nil {
		return true, err
	}
	p := programme.Lifecycle.Bootstrap
	defs, err := recordedProductCRLDefinitions(ctx, cfg, store, e, native, p)
	if err != nil {
		return true, err
	}
	// This is the original separate public transport authority, not a restored key.
	_, pem, err := readIssuerEnrollmentInitialTrust(store, e.Journal.PlanSHA256)
	if err != nil {
		return true, err
	}
	cert, err := parseFirstCertificatePEM([]byte(pem))
	if err != nil {
		return true, err
	}
	runtime := environmentCreateBootstrapRuntime{TransportTrustConfigMap: "pki-create-transport-trust", TransportCAPEM: pem, TransportRootSHA256: environmentPreflightBytesSHA256(cert.Raw), TransportSecrets: map[string]string{}}
	for _, role := range []string{"pki-controller", "certissuer", "account-manager"} {
		runtime.TransportSecrets[role] = "pki-create-" + role + "-transport"
	}
	jobs, err := renderCreateProductPublicCRLs(ctx, cfg, p, runtime, installed.Ceremony, defs)
	if err != nil {
		return true, err
	}
	if len(jobs) == 0 {
		return false, nil
	}
	jobs, err = productPublicCRLToolJobs(jobs, tool)
	if err != nil {
		return true, err
	}
	source, err := readEnvironmentCreateStageInput(store, e, productPublicCRLStage)
	if err != nil {
		return true, err
	}
	if source != nil {
		if err := source.AdmitFailedForwardResumable(ctx, native, store.Root); err != nil {
			return true, err
		}
	}
	binding, err := readEnvironmentCustodyBootstrapBinding(cfg, store, e.Journal.PlanSHA256, e)
	if err != nil {
		return true, err
	}
	// Use only the authenticated original scope with its current managed transport.
	jobs, err = projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, "", jobs)
	if err != nil {
		return true, err
	}
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return true, err
	}
	if plan == nil {
		live, err := native.Inventory(ctx, p.Stack)
		if err != nil {
			return true, err
		}
		in := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, jobs)}
		plan, err = deploymentplan.Compile(in, jobs, live)
		if err != nil {
			return true, err
		}
		if err = plan.AdmitResumable(ctx, native, store.Root); err != nil {
			return true, err
		}
		if err = saveEnvironmentCreateStageInput(ctx, store, e, stage, plan); err != nil {
			return true, err
		}
	} else if err = verifyFailedForwardDefinitions(plan, jobs); err != nil {
		return true, err
	}
	if err = plan.AdmitResumable(ctx, native, store.Root); err != nil {
		return true, err
	}
	deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
	if err != nil {
		return true, err
	}
	if o.Action == "create" {
		if o.Confirm != parent.Stack || o.PlanSHA256 != plan.SHA256() {
			return true, createProviderRecoveryBlock("execute the exact preflight product public CRL initialization plan")
		}
		bounded, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if err = executeCompiledEnvironmentCreateStage(bounded, cfg, store, parent, e, stage, plan, native, out); err != nil {
			return true, err
		}
		if source != nil {
			if err = completeProductCRLForward(bounded, store, e, source, stage, plan); err != nil {
				return true, err
			}
		}
	}
	report.Scope = "original-generation-product-public-crls"
	report.Decision = "GO"
	if o.Action == "create" {
		report.Decision, report.ReusedPreflight = "PASS", true
	}
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: plan.SHA256()}
	for _, r := range plan.Resources {
		report.Plan.Resources = append(report.Plan.Resources, deploymentplan.SummaryResource{ID: r.ID, Phase: r.Phase, Action: r.Action})
	}
	now := time.Now().UTC()
	report.QualifiedAt, report.EvidenceValidUntil, report.ExecutionDeadline = &now, &deadline, &deadline
	report.QualificationSHA256, report.Completed = plan.SHA256(), false
	report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.recovery.product-public-crls", Status: "PASS", Required: true, EvidenceSHA256: plan.SHA256()})
	_, _ = io.WriteString(out, "Public CRL initialization only; original session, deployment and full health remain incomplete.\n")
	return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

// Keep future generations ordered before the first product workload wait.
func initializeCreateProductPublicCRLs(ctx context.Context, cfg deploymentConfig, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, definitions []deploymentplan.Definition, apply environmentCreateBootstrapApply) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" || runtime.ServiceCeremony == nil {
		return nil
	}
	jobs, err := renderCreateProductPublicCRLs(ctx, cfg, parent.Bootstrap, runtime, *runtime.ServiceCeremony, definitions)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}
	return apply(ctx, productPublicCRLStage, jobs)
}

// A native Service-only initializer cannot initialize MQTT/App CRLs. Use the
// existing generation-bound formal repair tool, preserving every runtime image.
func productPublicCRLToolJobs(jobs []deploymentplan.Definition, tool failedForwardTool) ([]deploymentplan.Definition, error) {
	if _, err := canonicalFailedForwardCapabilities(tool.Capabilities); err != nil {
		return nil, err
	}
	if !immutableEnvironmentImage.MatchString(tool.Image.Reference) || !deploymentUpgradeSHA.MatchString(tool.Image.SourceRevision) {
		return nil, createProviderRecoveryBlock("product public CRL helper requires its exact formal native tool")
	}
	result := make([]deploymentplan.Definition, len(jobs))
	for i, d := range jobs {
		result[i] = d
		result[i].Object = deploymentplan.Clone(d.Object)
		if d.Object["kind"] != "Job" {
			return nil, createProviderRecoveryBlock("product public CRL tool may replace only native initializer Jobs")
		}
		c := managedNamedField(environmentPKIPod(result[i].Object)["containers"], "enroll")
		args := certIssuerObjectList(c["command"])
		if len(args) < 5 || args[0] != "/app/serviceidentity-bootstrap" || args[1] != "initialize-public-crl" {
			return nil, createProviderRecoveryBlock("product public CRL helper command differs")
		}
		c["image"] = tool.Image.Reference
		meta := deploymentplan.Map(result[i].Object["metadata"])
		name := deploymentplan.String(meta["name"])
		if len(name) > 50 {
			name = strings.TrimRight(name[:50], "-")
		}
		meta["name"] = name + "-" + tool.Image.SourceRevision[:12]
	}
	return result, nil
}

type productCRLForwardReceipt struct{ Schema, OriginalPlan, OriginalJournal, ForwardStage, ForwardPlan string }

const productCRLForwardSchema = "rtk-cloud.product-public-crl-forward/v1"

func productCRLForwardReceiptMatches(raw json.RawMessage) bool {
	var r productCRLForwardReceipt
	return json.Unmarshal(raw, &r) == nil && r.Schema == productCRLForwardSchema
}

// A failed immutable Job is history, not a successful initialization. Resolve
// its parent request only through separately accepted replacement Jobs for the
// exact same public purposes; the literal failed child remains untouched.
func validateProductCRLForwardReceipt(store secretStore, e *environmentCreateExecution, source *deploymentplan.Plan, j deploymentplan.Journal, raw json.RawMessage) error {
	boundary := "source-receipt"
	bad := func() error {
		return createProviderRecoveryBlock("product public CRL failure requires its exact accepted native successor and untouched source journal; boundary=" + boundary)
	}
	var r productCRLForwardReceipt
	if decodeEnvironmentJSON(raw, &r) != nil || r.Schema != productCRLForwardSchema || source == nil || r.OriginalPlan != source.SHA256() || r.OriginalJournal != deploymentplan.Digest(j) || j.Accepted || j.InFlight != "" || j.Failed == "" || j.AcceptanceStarted || len(j.Completed) >= len(source.Resources) || !strings.HasPrefix(r.ForwardStage, "kubernetes/"+productPublicCRLStage+"-") {
		return bad()
	}
	boundary = "accepted-successor"
	next, nj, err := acceptedCreateStage(store, e, r.ForwardStage)
	if err != nil || next.SHA256() != r.ForwardPlan || len(next.Resources) != len(source.Resources) {
		return bad()
	}
	jobs := map[string]deploymentplan.Resource{}
	others := map[string]deploymentplan.Resource{}
	for _, n := range next.Resources {
		if n.Desired["kind"] == "Job" {
			c := managedNamedField(environmentPKIPod(n.Desired)["containers"], "enroll")
			key := deploymentplan.Digest(c["command"]) + "/" + deploymentplan.Digest(c["env"])
			if jobs[key].ID != "" {
				return bad()
			}
			jobs[key] = n
		} else {
			others[n.ID] = n
		}
	}
	for i, old := range source.Resources {
		if i < len(j.Completed) && j.Completed[i] != old.ID {
			return bad()
		}
		if old.Desired["kind"] != "Job" {
			boundary = "native-non-job-owner"
			n, ok := others[old.ID]
			uid := old.UID
			if j.Owners[old.ID] != "" {
				uid = j.Owners[old.ID]
			}
			if !ok || !deploymentplan.Matches(old.Desired, n.Desired) {
				return bad()
			}
			if uid != "" {
				if n.Action != "retain" || n.UID != uid || nj.Owners[n.ID] != uid {
					return bad()
				}
			} else if old.Action != "create" || n.Action != "create" || n.UID != "" || nj.Owners[n.ID] == "" {
				return bad()
			}
			delete(others, old.ID)
			continue
		}
		boundary = "native-job-purpose"
		c := managedNamedField(environmentPKIPod(old.Desired)["containers"], "enroll")
		key := deploymentplan.Digest(c["command"]) + "/" + deploymentplan.Digest(c["env"])
		n, ok := jobs[key]
		if !ok || n.ID == old.ID || n.Action != "create" || nj.Owners[n.ID] == "" {
			return bad()
		}
		boundary = "native-job-body"
		copy := deploymentplan.Clone(n.Desired)
		deploymentplan.Map(copy["metadata"])["name"] = deploymentplan.Map(old.Desired["metadata"])["name"]
		managedNamedField(environmentPKIPod(copy)["containers"], "enroll")["image"] = c["image"]
		if !deploymentplan.Matches(old.Desired, copy) || !deploymentplan.Matches(copy, old.Desired) {
			return bad()
		}
		delete(jobs, key)
	}
	boundary = "remaining-native-inventory"
	if len(jobs) != 0 || len(others) != 0 {
		return bad()
	}
	return nil
}
func completeProductCRLForward(ctx context.Context, store secretStore, e *environmentCreateExecution, source *deploymentplan.Plan, stage string, next *deploymentplan.Plan) error {
	if source == nil {
		return nil
	}
	j, ok, err := source.ReadExecution(store.Root)
	if err != nil || !ok || j.Accepted {
		return createProviderRecoveryBlock("original product CRL failed journal unavailable")
	}
	raw := mustProviderRecoveryJSON(productCRLForwardReceipt{Schema: productCRLForwardSchema, OriginalPlan: source.SHA256(), OriginalJournal: deploymentplan.Digest(j), ForwardStage: "kubernetes/" + stage, ForwardPlan: next.SHA256()})
	if err := validateProductCRLForwardReceipt(store, e, source, j, raw); err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+productPublicCRLStage)
	if entry == nil {
		return createProviderRecoveryBlock("original product CRL parent request unavailable")
	}
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, raw) {
			return createProviderRecoveryBlock("original product CRL resolution changed")
		}
		return e.Check(ctx)
	}
	if entry.State == "pending" {
		if err := e.RecordReceipt(entry.Key, raw); err != nil {
			return err
		}
	} else if entry.State != "allocated" || !samePersistedJSONReceipt(entry.Receipt, raw) {
		return createProviderRecoveryBlock("original product CRL resolution differs")
	}
	return e.CompleteEntry(entry.Key, time.Now().UTC())
}
