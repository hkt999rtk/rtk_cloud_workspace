package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailProductSessionForwardSchema = "rtk-cloud.create-normal-tail-product-session-forward/v1"
const createNormalTailProductSessionForwardStage = "workload-product-session-forward"

// The first forward remains a literal, unaccepted native execution. This
// separately saved target binds its zero-write outcome to the real renewal.
type createNormalTailProductSessionForward struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256                    string
	OriginalPlanSHA256, OriginalJournalSHA256                           string
	PreviousPlanSHA256, PreviousJournalSHA256, PreviousDescriptorSHA256 string
	RenewalSHA256, ForwardStage                                         string
	Target                                                              createNormalTailTarget
}

type createNormalTailProductSessionForwardReceipt struct {
	Schema                string `json:"schema"`
	PreviousPlanSHA256    string `json:"previous_plan_sha256"`
	PreviousJournalSHA256 string `json:"previous_journal_sha256"`
	RenewalSHA256         string `json:"renewal_sha256"`
	ForwardStage          string `json:"forward_stage"`
	ForwardPlanSHA256     string `json:"forward_plan_sha256"`
	ForwardJournalSHA256  string `json:"forward_journal_sha256"`
}

func createNormalTailProductSessionForwardPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-product-session-forward.json")
}

func productSessionForwardBlocked(stage ...string) error {
	cause := "product session workload recovery requires its original zero-write forward, exact renewal and unchanged retained effects"
	if len(stage) != 0 {
		cause += ": stage=" + stage[0]
	}
	return createProviderRecoveryBlock(cause)
}

func productSessionForwardFailure(stage, resource string, err error) error {
	var original *deploymentplan.Blocker
	if errors.As(err, &original) {
		copy := *original
		copy.Cause += ": stage=" + stage
		if copy.Resource == "" {
			copy.Resource = resource
		}
		return &copy
	}
	blocker := productSessionForwardBlocked(stage).(*deploymentplan.Blocker)
	blocker.Resource = resource
	return blocker
}

func productSessionForwardKnownZeroWrite(p *deploymentplan.Plan, j deploymentplan.Journal) bool {
	return p != nil && len(p.Resources) != 0 && j.PlanSHA256 == p.SHA256() && !j.Accepted && !j.AcceptanceStarted && len(j.Completed) == 0 && len(j.Owners) == 0 && j.InFlight == "" && j.Failed == p.Resources[0].ID
}

// Retain a genuine accepted preparation response; the public desired recipe is
// never replaced with an arbitrary current Kubernetes object.
func productSessionForwardRetain(r deploymentplan.Resource) (deploymentplan.Resource, error) {
	if !environmentBootstrapOwnerUUID.MatchString(r.UID) || r.Baseline == nil || deploymentplan.Map(r.Baseline["metadata"])["uid"] != r.UID || !deploymentplan.Matches(r.Desired, r.Baseline) {
		return r, productSessionForwardBlocked()
	}
	r.Action = "retain"
	r.Dependencies = nil // its original accepted child already ordered these inputs
	return r, nil
}

func captureProductSessionForwardDependencies(ctx context.Context, dependencies []deploymentplan.Resource, read func(context.Context, string) (deploymentplan.Object, error)) ([]deploymentplan.Resource, error) {
	result := slices.Clone(dependencies)
	for i, r := range result {
		actual, err := read(ctx, r.ID)
		if err != nil {
			return nil, productSessionForwardFailure("dependency-readback", r.ID, err)
		}
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(r.Desired, actual) {
			return nil, productSessionForwardFailure("dependency-native-owner-or-body", r.ID, nil)
		}
		result[i].Baseline = deploymentplan.Projection(actual)
	}
	return result, ctx.Err()
}

// Native defaulted fields are observation snapshots, never desired inputs.
// A saved snapshot must still match the accepted recipe and its exact owner.
func savedProductSessionForwardDependencies(dependencies []deploymentplan.Resource, target *deploymentplan.Plan) ([]deploymentplan.Resource, error) {
	if target == nil {
		return nil, productSessionForwardBlocked()
	}
	byID := map[string]deploymentplan.Resource{}
	for _, r := range target.Resources {
		if _, ok := byID[r.ID]; ok {
			return nil, productSessionForwardBlocked()
		}
		byID[r.ID] = r
	}
	result := slices.Clone(dependencies)
	for i, r := range result {
		saved, ok := byID[r.ID]
		if !ok || saved.UID != r.UID || saved.Baseline == nil || deploymentplan.Map(saved.Baseline["metadata"])["uid"] != r.UID || deploymentplan.Map(saved.Baseline["metadata"])["deletionTimestamp"] != nil || deploymentplan.Digest(saved.Baseline) != deploymentplan.Digest(deploymentplan.Projection(saved.Baseline)) || !deploymentplan.Matches(r.Desired, saved.Baseline) {
			return nil, productSessionForwardBlocked()
		}
		result[i].Baseline = deploymentplan.Clone(saved.Baseline)
	}
	return result, nil
}

// The original MQTT projection can append settings after the ACK fields.
// Validate through the maintained resolver on a clone, then retain every
// original env slot and change only the acknowledged session value.
func productSessionForwardACKInPlace(object deploymentplan.Object, outputs map[string]environmentCreateProductIdentity, consumers map[string]int, old, renewed environmentCreateServiceCeremony) error {
	expected := deploymentplan.Clone(object)
	if err := resolveCreateNormalTailProductACK(expected, outputs, consumers, old, true); err != nil {
		return err
	}
	pod, wanted := environmentPKIPod(object), environmentPKIPod(expected)
	if pod == nil {
		return nil
	}
	volumes := map[string]bool{}
	namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		id := "Secret/" + namespace + "/" + deploymentplan.String(deploymentplan.Map(v["secret"])["secretName"])
		if _, ok := outputs[id]; ok {
			volumes[deploymentplan.String(v["name"])] = true
		}
	}
	var sessions []deploymentplan.Object
	for i, raw := range certIssuerObjectList(pod["containers"]) {
		container := deploymentplan.Map(raw)
		selected := false
		for _, mount := range certIssuerObjectList(container["volumeMounts"]) {
			selected = selected || volumes[deploymentplan.String(deploymentplan.Map(mount)["name"])]
		}
		if !selected {
			continue
		}
		want := deploymentplan.Map(certIssuerObjectList(wanted["containers"])[i])
		actualFields, err := environmentPKINamedFields(container["env"])
		wantedFields, wantErr := environmentPKINamedFields(want["env"])
		if err != nil || wantErr != nil {
			return productSessionForwardBlocked("unique-original-ACK-fields")
		}
		for _, key := range []string{"PKI_BOOTSTRAP_CONTROLLER_URL", "PKI_BOOTSTRAP_ISSUER_ID", "PKI_BOOTSTRAP_SESSION_ID"} {
			if actualFields[key] == nil || deploymentplan.Digest(actualFields[key]) != deploymentplan.Digest(wantedFields[key]) {
				return productSessionForwardBlocked("exact-original-ACK-values")
			}
		}
		want["env"] = container["env"]
		sessions = append(sessions, actualFields["PKI_BOOTSTRAP_SESSION_ID"])
	}
	if deploymentplan.Digest(expected) != deploymentplan.Digest(object) {
		return productSessionForwardBlocked("original-ACK-only-projection")
	}
	for _, field := range sessions {
		field["value"] = renewed.BootstrapSessionID
	}
	return nil
}

// Only five registration session values and authenticated renewal preparation
// resources may differ. Product private state, transport, placement, images and
// every unrelated original resource retain their literal prior intent.
func buildCreateNormalTailProductSessionForward(cfg deploymentConfig, l *createNormalTailProductLifecycle, prior *deploymentplan.Plan, j deploymentplan.Journal, renewal *productSessionRenewal, dependencies []deploymentplan.Resource) (*deploymentplan.Plan, error) {
	if l == nil || renewal == nil || !productSessionForwardKnownZeroWrite(prior, j) || len(l.Outputs) != 5 || renewal.ScopeSHA256 != productIdentityScope(l) || renewal.Original.PlanSHA256 != productIdentityScope(l) || renewal.Installed.PlanSHA256 != productIdentityScope(l) || deploymentplan.Digest(renewal.Original.Outputs) != deploymentplan.Digest(renewal.Installed.Outputs) || renewal.Original.Ceremony.BootstrapSessionID == renewal.Installed.Ceremony.BootstrapSessionID || deploymentplan.Digest(renewal.Original.Ceremony.Root) != deploymentplan.Digest(renewal.Installed.Ceremony.Root) || deploymentplan.Digest(renewal.Original.Ceremony.Intermediate) != deploymentplan.Digest(renewal.Installed.Ceremony.Intermediate) {
		return nil, productSessionForwardBlocked("renewal-scope-and-permanent-outputs")
	}
	oldService, newService := renewal.Original.Ceremony, renewal.Installed.Ceremony
	if deploymentplan.Digest(oldService.Root) != deploymentplan.Digest(l.Root) || oldService.BootstrapClaim != newService.BootstrapClaim || oldService.CustodyReader != newService.CustodyReader || oldService.BootstrapCredentialNamespace != newService.BootstrapCredentialNamespace || !slices.Equal(oldService.BootstrapCredentialNamespaces, newService.BootstrapCredentialNamespaces) || oldService.CleanupBinding.NamespaceUID != newService.CleanupBinding.NamespaceUID || oldService.CleanupBinding.KubernetesCA != newService.CleanupBinding.KubernetesCA {
		return nil, productSessionForwardBlocked("original-custody-and-Root")
	}
	if renewal.Source.ParentSHA256 != l.ParentSHA256 || renewal.Source.OwnerSHA256 != l.OwnerSHA256 || renewal.Source.ScopeSHA256 != productIdentityScope(l) || renewal.Source.OriginalForwardPlanSHA256 != prior.SHA256() || renewal.Source.OriginalForwardJournalSHA256 != deploymentplan.Digest(j) || renewal.Source.OriginalInstalledSHA256 != deploymentplan.Digest(renewal.Original) {
		return nil, productSessionForwardBlocked("previous-forward-source")
	}
	for _, installed := range []environmentProductIdentitySession{renewal.Original, renewal.Installed} {
		if installed.Schema != productIdentitySessionSchema || installed.ParentSHA256 != l.ParentSHA256 || installed.OwnerSHA256 != l.OwnerSHA256 || len(installed.Outputs) != len(l.Outputs) || !environmentBootstrapOwnerUUID.MatchString(installed.Ceremony.BootstrapSessionID) || validateEnvironmentBootstrapCleanupBinding(&installed.Ceremony.CleanupBinding, cfg.Environment) != nil || installed.Ceremony.CleanupBinding.SessionID != installed.Ceremony.BootstrapSessionID {
			return nil, productSessionForwardBlocked("installed-session-and-cleanup")
		}
		for i, output := range l.Outputs {
			r := installed.Outputs[i]
			if r.ResourceID != output.ID || r.Subject != output.Subject || !environmentBootstrapOwnerUUID.MatchString(r.SecretUID) || !environmentBootstrapOwnerUUID.MatchString(r.ClaimUID) || !environmentPreflightDigest.MatchString(r.CertificateSHA256) {
				return nil, productSessionForwardBlocked("permanent-output-identity")
			}
		}
	}
	var next deploymentplan.Plan
	if json.Unmarshal(mustProviderRecoveryJSON(prior), &next) != nil {
		return nil, productSessionForwardBlocked()
	}
	outputs := map[string]environmentCreateProductIdentity{}
	for _, p := range l.Outputs {
		outputs[p.ID] = p
	}
	consumers := map[string]int{}
	for i := range next.Resources {
		r := &next.Resources[i]
		before := deploymentplan.Digest(r.Desired)
		if err := productSessionForwardACKInPlace(r.Desired, outputs, consumers, renewal.Original.Ceremony, renewal.Installed.Ceremony); err != nil {
			return nil, productSessionForwardFailure("original-ACK-projection", r.ID, err)
		}
		if deploymentplan.Digest(r.Desired) != before && (r.Action != "create" || r.UID != "" || r.Baseline != nil) {
			return nil, productSessionForwardFailure("unwritten-product-owner", r.ID, nil)
		}
	}
	for id := range outputs {
		if consumers[id] != 1 {
			return nil, productSessionForwardFailure("unique-product-consumer", id, nil)
		}
	}
	index := map[string]int{}
	for i, r := range next.Resources {
		if _, ok := index[r.ID]; ok {
			return nil, productSessionForwardBlocked()
		}
		index[r.ID] = i
	}
	seen := map[string]bool{}
	var additional []deploymentplan.Resource
	for _, resource := range dependencies {
		if seen[resource.ID] {
			return nil, productSessionForwardFailure("duplicate-renewal-dependency", resource.ID, nil)
		}
		seen[resource.ID] = true
		r, err := productSessionForwardRetain(resource)
		if err != nil {
			return nil, productSessionForwardFailure("accepted-renewal-dependency", resource.ID, err)
		}
		if i, exists := index[r.ID]; exists {
			old := &next.Resources[i]
			if old.UID != r.UID || old.Action == "create" || old.Action == "retire" {
				return nil, productSessionForwardFailure("original-dependency-owner", r.ID, nil)
			}
			if r.ID == "Deployment/"+prior.Stack+"-video-cloud/pki-controller" {
				// Preserve the already-qualified MQTT authorization and selected
				// release while changing this exact cleanup binding projection.
				want := deploymentplan.Clone(old.Desired)
				if _, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(cfg), want, renewal.Installed.Ceremony.CleanupBinding); err != nil {
					return nil, productSessionForwardFailure("renewed-Controller-cleanup", r.ID, err)
				}
				r.Desired = want
				if !deploymentplan.Matches(want, r.Baseline) {
					r.Action = "update"
				}
			}
			r.Dependencies = slices.Clone(old.Dependencies)
			*old = r
		} else {
			additional = append(additional, r)
		}
	}
	if !seen["Deployment/"+prior.Stack+"-video-cloud/pki-controller"] || !seen["ConfigMap/"+prior.Stack+"-video-cloud/"+renewal.Installed.Ceremony.CleanupBinding.ConfigMapName] {
		return nil, productSessionForwardBlocked("renewal-Controller-and-cleanup-map")
	}
	sort.Slice(additional, func(i, j int) bool { return additional[i].ID < additional[j].ID })
	next.Resources = append(next.Resources, additional...)
	return &next, nil
}

func readCreateNormalTailPreviousForwardSource(store secretStore, e *environmentCreateExecution, f *createNormalTailWorkloadForward) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if e == nil || e.Store != store || f == nil || f.Target.Plan == nil {
		return nil, deploymentplan.Journal{}, productSessionForwardBlocked()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+f.ForwardStage)
	if entry == nil || entry.Phase != f.ForwardStage || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
		return nil, deploymentplan.Journal{}, productSessionForwardBlocked()
	}
	intent, err := environmentCreateKubernetesStageIntent(f.Target.Plan)
	if err != nil {
		return nil, deploymentplan.Journal{}, err
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || sha != entry.IntentSHA256 || entry.StartedAt.IsZero() || entry.State == "pending" && (len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero()) || entry.State == "allocated" && (len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero()) || entry.State == "completed" && (len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt)) {
		return nil, deploymentplan.Journal{}, productSessionForwardBlocked()
	}
	p, j, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil || p == nil || p.SHA256() != f.Target.Plan.SHA256() {
		return nil, j, productSessionForwardBlocked()
	}
	verified, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(verified) != deploymentplan.Digest(j) || !productSessionForwardKnownZeroWrite(p, j) {
		return nil, j, productSessionForwardBlocked()
	}
	return p, j, nil
}

// The literal failed child is readable after supersession only when the saved
// descriptor, exact response and genuinely accepted successor all authenticate.
// No parent state or receipt is reset to masquerade as a pending request.
func readCreateNormalTailPreviousForward(store secretStore, e *environmentCreateExecution, f *createNormalTailWorkloadForward) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	p, j, err := readCreateNormalTailPreviousForwardSource(store, e, f)
	if err != nil {
		return nil, j, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+f.ForwardStage)
	if entry.State != "pending" {
		original, err := readCreateNormalTailFrozenTarget(store, e)
		if err != nil {
			return nil, j, err
		}
		_, originalJournal, err := readCreateNormalTailWorkloadOriginal(store, e, original)
		if err != nil {
			return nil, j, err
		}
		d, err := readCreateNormalTailProductSessionForward(store, e, original, originalJournal, f)
		if err != nil || d == nil {
			return nil, j, productSessionForwardBlocked()
		}
	}
	return p, j, nil
}

func readCreateNormalTailProductSessionForward(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, previous *createNormalTailWorkloadForward) (*createNormalTailProductSessionForward, error) {
	d, err := readCreateNormalTailProductSessionForwardSource(store, e, original, originalJournal, previous)
	if err != nil || d == nil {
		return d, err
	}
	if err := validateCreateNormalTailProductSessionSupersession(store, e, previous, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Authenticate the immutable derivation independently from logical completion.
// A later exact runtime correction may supersede this truthful failed child.
func readCreateNormalTailProductSessionForwardSource(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, previous *createNormalTailWorkloadForward) (*createNormalTailProductSessionForward, error) {
	path := createNormalTailProductSessionForwardPath(e)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailProductSessionForwardStage) != nil {
			return nil, productSessionForwardBlocked()
		}
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	var d createNormalTailProductSessionForward
	if validateEnvironmentPreflightDirectories(store, e.Directory) != nil || readEnvironmentPreflightJSON(path, &d) != nil || previous == nil || d.Schema != createNormalTailProductSessionForwardSchema || d.ParentSHA256 != e.Journal.PlanSHA256 || d.OwnerSHA256 != deploymentplan.Digest(e.owner) || d.ProgramSHA256 != deploymentplan.Digest(program) || d.OriginalPlanSHA256 != original.SHA256() || d.OriginalJournalSHA256 != deploymentplan.Digest(originalJournal) || d.PreviousPlanSHA256 != previous.Target.Plan.SHA256() || d.PreviousDescriptorSHA256 != deploymentplan.Digest(previous) || d.ForwardStage != createNormalTailProductSessionForwardStage {
		return nil, productSessionForwardBlocked()
	}
	_, j, err := readCreateNormalTailPreviousForwardSource(store, e, previous)
	if err != nil || !productSessionForwardKnownZeroWrite(previous.Target.Plan, j) || d.PreviousJournalSHA256 != deploymentplan.Digest(j) {
		return nil, productSessionForwardBlocked()
	}
	renewal, err := readProductSessionRenewalFirst(store, e, program.Products)
	if err != nil || renewal == nil || d.RenewalSHA256 != deploymentplan.Digest(renewal) {
		return nil, productSessionForwardBlocked()
	}
	cfg := deploymentConfig{Environment: store.Environment, Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	deps, err := productSessionRenewalDependencyResources(cfg, store, e, program.Products, renewal)
	if err != nil {
		return nil, err
	}
	deps, err = savedProductSessionForwardDependencies(deps, d.Target.Plan)
	if err != nil {
		return nil, err
	}
	want, err := buildCreateNormalTailProductSessionForward(cfg, program.Products, previous.Target.Plan, j, renewal, deps)
	if err != nil || d.Target.Plan == nil || want.SHA256() != d.Target.Plan.SHA256() || deploymentplan.Digest(d.Target.Release) != deploymentplan.Digest(previous.Target.Release) || deploymentplan.Digest(d.Target.Bindings) != deploymentplan.Digest(previous.Target.Bindings) || deploymentplan.Digest(d.Target.Pins) != deploymentplan.Digest(previous.Target.Pins) || d.Target.Schema != previous.Target.Schema || d.Target.ParentSHA256 != previous.Target.ParentSHA256 || d.Target.OwnerSHA256 != previous.Target.OwnerSHA256 {
		return nil, productSessionForwardBlocked()
	}
	return &d, nil
}

func prepareCreateNormalTailProductSessionForward(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, previous *createNormalTailWorkloadForward, original *deploymentplan.Plan, originalJournal deploymentplan.Journal) (*compiledEnvironmentDeployment, error) {
	e, store := h.Execution, h.Execution.Store
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	renewal, err := readProductSessionRenewalFirst(store, e, program.Products)
	if err != nil {
		return nil, err
	}
	if renewal == nil {
		return c, nil
	}
	d, err := readCreateNormalTailProductSessionForward(store, e, original, originalJournal, previous)
	if err != nil {
		return nil, err
	}
	if d == nil {
		_, j, err := readCreateNormalTailPreviousForward(store, e, previous)
		if err != nil {
			return nil, err
		}
		deps, err := productSessionRenewalDependencyResources(cfg, store, e, program.Products, renewal)
		if err != nil {
			return nil, productSessionForwardFailure("prepare-dependency-derivation", "", err)
		}
		deps, err = captureProductSessionForwardDependencies(ctx, deps, c.Backend.Read)
		if err != nil {
			return nil, productSessionForwardFailure("prepare-native-dependencies", "", err)
		}
		next, err := buildCreateNormalTailProductSessionForward(cfg, program.Products, previous.Target.Plan, j, renewal, deps)
		if err != nil {
			return nil, productSessionForwardFailure("prepare-target-build", "", err)
		}
		if _, exists, err := next.ReadExecution(store.Root); err != nil || exists {
			return nil, productSessionForwardBlocked()
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if strings.HasPrefix(lease.Holder, next.SHA256()+":") {
				return nil, productSessionForwardBlocked()
			}
		}
		target := previous.Target
		target.Plan = next
		d = &createNormalTailProductSessionForward{Schema: createNormalTailProductSessionForwardSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: deploymentplan.Digest(program), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(originalJournal), PreviousPlanSHA256: previous.Target.Plan.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(j), PreviousDescriptorSHA256: deploymentplan.Digest(previous), RenewalSHA256: deploymentplan.Digest(renewal), ForwardStage: createNormalTailProductSessionForwardStage, Target: target}
		if err = e.Check(ctx); err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(store.Root, createNormalTailProductSessionForwardPath(e))
		if err != nil {
			return nil, err
		}
		if err = store.write(rel, mustProviderRecoveryJSON(d), false); err != nil {
			return nil, err
		}
	}
	next := *c
	next.Plan = d.Target.Plan
	next.Release = d.Target.Release
	next.Bindings = d.Target.Bindings
	return &next, nil
}

func executeCreateNormalTailProductSessionForwardBackend(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, d *createNormalTailProductSessionForward, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error, phaseFences ...deploymentplan.PhaseFence) error {
	if d == nil || c.Plan.SHA256() != d.Target.Plan.SHA256() {
		return productSessionForwardBlocked()
	}
	if err := executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, d.ForwardStage, c.Plan, backend, qualify, fence, accept, out, capture, phaseFences...); err != nil {
		return err
	}
	j, exists, err := c.Plan.ReadExecution(store.Root)
	if err != nil || !exists || !j.Accepted {
		return productSessionForwardBlocked()
	}
	receipt := createNormalTailProductSessionForwardReceipt{Schema: createNormalTailProductSessionForwardSchema, PreviousPlanSHA256: d.PreviousPlanSHA256, PreviousJournalSHA256: d.PreviousJournalSHA256, RenewalSHA256: d.RenewalSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	if err := completeCreateNormalTailSupersededWorkload(ctx, h.Execution, "kubernetes/"+createNormalTailWorkloadForwardStage, receipt); err != nil {
		return err
	}
	logical := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: d.OriginalPlanSHA256, OriginalJournalSHA256: d.OriginalJournalSHA256, ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(j)}
	return completeCreateNormalTailSupersededWorkload(ctx, h.Execution, "kubernetes/workload", logical)
}

func completeCreateNormalTailSupersededWorkload(ctx context.Context, e *environmentCreateExecution, key string, receipt any) error {
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return productSessionForwardBlocked()
	}
	raw := mustProviderRecoveryJSON(receipt)
	if entry.State == "completed" {
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(raw)) {
			return productSessionForwardBlocked()
		}
		return e.Check(ctx)
	}
	if entry.State == "pending" {
		if err := e.RecordReceipt(key, raw); err != nil {
			return err
		}
	} else if entry.State != "allocated" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(raw)) {
		return productSessionForwardBlocked()
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
