package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initialTransportSuccessorCandidate struct {
	Schema       string                                `json:"schema"`
	Parent       string                                `json:"parent_plan_sha256"`
	OriginalSHA  map[string]string                     `json:"original_sha256"`
	Transports   map[string]environmentCreateTransport `json:"transports"`
	AuthorityKey map[string]string                     `json:"authority_keys"`
}

type initialTransportSuccessorPlan struct {
	Schema, Parent, OwnerSHA, JournalSHA, ConfigurationSHA, KubeconfigSHA, CheckerSHA, CandidateSHA string
	PodUID, StatefulSetUID                                                                          string
	Resources                                                                                       []activeTransportRenewalResource
}

// This temporary bootstrap-only process prepares future Device/App trust and
// is accepted by the normal create/health flow after its issuers exist. Its
// readiness is not an OpenBao transport recovery prerequisite.
func initialTransportBootstrapTrustHelper(resource activeTransportRenewalResource) bool {
	if resource.Desired["kind"] != "Deployment" {
		return false
	}
	containers := certIssuerObjectList(environmentPKIPod(resource.Desired)["containers"])
	if len(containers) != 1 {
		return false
	}
	container := deploymentplan.Map(containers[0])
	command := certIssuerObjectList(container["command"])
	return len(command) == 2 && deploymentplan.String(command[0]) == "/app/pkibroker" && deploymentplan.String(command[1]) == "--deployment-bootstrap-trust" && len(certIssuerObjectList(container["args"])) == 0
}

func executeInitialTransportSuccessor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, a initialTransportSuccessorArtifact, source environmentCreateJournal, candidate initialTransportSuccessorCandidate, check, reload, verify func() error) error {
	bad := func() error {
		return createProviderRecoveryBlock("initial transport recovery differs from its qualified original execution")
	}
	if e == nil || b == nil || check == nil || reload == nil || verify == nil || e.Check(ctx) != nil || check() != nil || e.Journal.OwnerToken != source.OwnerToken || e.Journal.PlanSHA256 != source.PlanSHA256 || len(e.Journal.Entries) < len(source.Entries) || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || deploymentplan.Digest(candidate) != a.Plan.CandidateSHA {
		return bad()
	}
	for i := range source.Entries {
		if deploymentplan.Digest(source.Entries[i]) != deploymentplan.Digest(e.Journal.Entries[i]) {
			return bad()
		}
	}
	review := deploymentplan.Digest(a)
	key := "initial-transport-successor-" + review
	proof := initialTransportSuccessorProof{Schema: "rtk-cloud.initial-transport-successor-proof/v1", Parent: a.Plan.Parent, Review: review, CandidateSHA: a.Plan.CandidateSHA, Journal: e.owner.Journal, Completed: true}
	allowed := map[string]bool{key: true}
	for _, resource := range a.Plan.Resources {
		part := key + "-" + strings.ReplaceAll(resource.ID, "/", "-")
		if allowed[part] || (resource.Desired["kind"] != "ConfigMap" && resource.Desired["kind"] != "Secret" && resource.Desired["kind"] != "Deployment") {
			return bad()
		}
		allowed[part] = true
	}
	var parent *environmentCreateJournalEntry
	seen := map[string]bool{}
	for i := len(source.Entries); i < len(e.Journal.Entries); i++ {
		entry := &e.Journal.Entries[i]
		if !allowed[entry.Key] || seen[entry.Key] {
			return bad()
		}
		seen[entry.Key] = true
		if entry.Key == key {
			parent = entry
		}
	}
	admittedAt := time.Now().UTC()
	if parent != nil {
		admittedAt = parent.StartedAt
		if parent.Phase != "recovery" || parent.IntentSHA256 != deploymentplan.Digest(initialTransportSuccessorIntent(proof)) {
			return bad()
		}
	}
	if validateInitialTransportSuccessorArtifact(a, review, source, admittedAt) != nil {
		return bad()
	}
	if err := stageInitialTransportSuccessorMaterial(cfg, store, a, candidate); err != nil {
		return err
	}
	if parent == nil {
		if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(initialTransportSuccessorIntent(proof)), admittedAt); err != nil {
			return err
		}
	}
	base := filepath.Join("deployment", "create", "transport-successor", review)
	oldCA, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment", "create", "material", a.Plan.Parent, "openbao-transport", "ca.crt"), true)
	if err != nil {
		return err
	}
	apply := func(resource activeTransportRenewalResource) error {
		return applyInitialTransportSuccessorResource(ctx, store, e, b, key, base, resource, string(oldCA), candidate.Transports["openbao"].CACert, check)
	}
	for _, resource := range a.Plan.Resources {
		if resource.Desired["kind"] != "Deployment" {
			if err := apply(resource); err != nil {
				return err
			}
		}
	}
	if e.Check(ctx) != nil || check() != nil {
		return bad()
	}
	if err := reload(); err != nil {
		return err
	}
	for _, resource := range a.Plan.Resources {
		if resource.Desired["kind"] == "Deployment" {
			if err := apply(resource); err != nil {
				return err
			}
		}
	}
	// Every consumer must receive the new trust before readiness is awaited:
	// one consumer may depend on another to recover its own identity.
	for _, resource := range a.Plan.Resources {
		if resource.Desired["kind"] == "Deployment" && !initialTransportBootstrapTrustHelper(resource) {
			if err := b.Wait(ctx, deploymentplan.Resource{ID: resource.ID, Action: "update", Phase: "workload", Desired: resource.Desired, UID: resource.UID}); err != nil {
				return err
			}
		}
	}
	if e.Check(ctx) != nil || check() != nil {
		return bad()
	}
	if err := verify(); err != nil {
		return err
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "completed.json"), mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	var state string
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			state = entry.State
		}
	}
	if state == "pending" {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
		state = "allocated"
	}
	if state == "allocated" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	} else if state != "completed" {
		return bad()
	}
	if err := validateInitialTransportSuccessorProof(a, proof, source, e.Journal); err != nil {
		return err
	}
	return writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment", "create", "material", a.Plan.Parent, "initial-transport-successor.json"), mustProviderRecoveryJSON(proof))
}

func stageInitialTransportSuccessorMaterial(cfg deploymentConfig, store secretStore, a initialTransportSuccessorArtifact, candidate initialTransportSuccessorCandidate) error {
	bad := func() error {
		return createProviderRecoveryBlock("successor material differs from the original custody snapshot or qualified consumers")
	}
	if cfg.Environment != store.Environment || cfg.Values["CLOUD_STACK_NAME"] == "" || !environmentPreflightDigest.MatchString(a.Plan.Parent) || candidate.Parent != a.Plan.Parent || deploymentplan.Digest(candidate) != a.Plan.CandidateSHA {
		return bad()
	}
	originalPath := filepath.Join(store.Root, "deployment", "create", a.Plan.Parent, "custody", "transport.json")
	original, err := readBoundedEnvironmentInputLimit(originalPath, true, false, 256<<10)
	if err != nil {
		return bad()
	}
	oldCA, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment", "create", "material", a.Plan.Parent, "openbao-transport", "ca.crt"), true)
	if err != nil {
		return bad()
	}
	expectedNS := map[string]bool{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud": false, cfg.Values["CLOUD_STACK_NAME"] + "-account-manager": false}
	snapshot := ""
	for _, resource := range a.Plan.Resources {
		if resource.Before["kind"] != "ConfigMap" || deploymentplan.Map(resource.Before["metadata"])["name"] != environmentCustodyTrustName {
			continue
		}
		namespace := deploymentplan.String(deploymentplan.Map(resource.Before["metadata"])["namespace"])
		seen, ok := expectedNS[namespace]
		if !ok || seen || resource.UID == "" || deploymentplan.Digest(jsonRawObject([]byte(deploymentplan.String(deploymentplan.Map(resource.Before["data"])["transport.json"])))) != deploymentplan.Digest(jsonRawObject(original)) {
			return bad()
		}
		derived, err := initialTransportCustodySuccessor(resource.Before, string(oldCA), candidate.Transports["openbao"].CACert)
		if err != nil || deploymentplan.Digest(derived) != deploymentplan.Digest(resource.Desired) {
			return bad()
		}
		value := deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["transport.json"])
		if snapshot != "" && snapshot != value {
			return bad()
		}
		snapshot, expectedNS[namespace] = value, true
	}
	if snapshot == "" {
		return bad()
	}
	for _, seen := range expectedNS {
		if !seen {
			return bad()
		}
	}
	base := filepath.Join("deployment", "create", "transport-successor", deploymentplan.Digest(a))
	for _, role := range []string{"management", "openbao"} {
		ca := candidate.Transports[role].CACert
		if ca == "" {
			return bad()
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, role+"-transport", "identities.json"), mustProviderRecoveryJSON(candidate.Transports[role])); err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, role+"-transport", "ca.crt"), []byte(ca)); err != nil {
			return err
		}
	}
	return writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "custody", "transport.json"), []byte(snapshot))
}

func applyInitialTransportSuccessorResource(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, key, base string, resource activeTransportRenewalResource, oldCA, newCA string, check func() error) error {
	if e == nil || check == nil || e.Check(ctx) != nil || check() != nil {
		return createProviderRecoveryBlock("initial transport successor lost its original execution or lease")
	}
	part := key + "-" + strings.ReplaceAll(resource.ID, "/", "-")
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == part {
			if entry != nil {
				return createProviderRecoveryBlock("duplicate temporary transport resource intent")
			}
			entry = &e.Journal.Entries[i]
		}
	}
	actual, err := b.Read(ctx, resource.ID)
	if err != nil {
		return err
	}
	write, err := initialTransportSuccessorNeedsWrite(resource, entry, actual)
	if err != nil {
		return err
	}
	body, err := initialTransportSuccessorResourceBody(resource, actual, oldCA, newCA)
	if err != nil {
		return err
	}
	intent := initialTransportSuccessorResourceIntent(resource)
	state := "pending"
	if entry == nil {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "original", strings.ReplaceAll(resource.ID, "/", "-")+".json"), mustProviderRecoveryJSON(actual)); err != nil {
			return err
		}
		if err := e.BeginEntry(part, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
			return err
		}
	} else {
		state = entry.State
	}
	if write {
		if _, err := b.Write(ctx, "update", body, true); err != nil {
			return err
		}
		if e.Check(ctx) != nil || check() != nil {
			return createProviderRecoveryBlock("initial transport successor lost its original execution or lease")
		}
		if _, err := b.Write(ctx, "update", body, false); err != nil {
			return err
		}
	}
	readback, err := b.Read(ctx, resource.ID)
	if err != nil || readback == nil || deploymentplan.Map(readback["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, readback) {
		return createProviderRecoveryBlock("temporary transport successor readback differs from its qualified owner or content")
	}
	if state == "pending" {
		if err := e.RecordReceipt(part, mustProviderRecoveryJSON(intent)); err != nil {
			return err
		}
	}
	if state != "completed" {
		return e.CompleteEntry(part, time.Now().UTC())
	}
	return e.Check(ctx)
}

func initialTransportSuccessorNeedsWrite(resource activeTransportRenewalResource, entry *environmentCreateJournalEntry, actual deploymentplan.Object) (bool, error) {
	bad := func() (bool, error) {
		return false, createProviderRecoveryBlock("temporary transport resource outcome differs from its original or admitted successor")
	}
	if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID {
		return bad()
	}
	before, after := deploymentplan.Matches(resource.Before, actual), deploymentplan.Matches(resource.Desired, actual)
	if before == after {
		return bad()
	}
	if entry == nil {
		if !before {
			return bad()
		}
		return true, nil
	}
	intent := initialTransportSuccessorResourceIntent(resource)
	if entry.IntentSHA256 != deploymentplan.Digest(intent) {
		return bad()
	}
	switch entry.State {
	case "pending":
		if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
			return bad()
		}
		return before, nil
	case "allocated", "completed":
		if !after || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(intent) || (entry.State == "allocated") != entry.CompletedAt.IsZero() {
			return bad()
		}
		return false, nil
	default:
		return bad()
	}
}

// Qualification uses the existing parent lock and reads an already prepared
// candidate. Each exact update is admitted by Kubernetes before execution.
func qualifyInitialTransportSuccessor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, candidate initialTransportSuccessorCandidate) (initialTransportSuccessorPlan, error) {
	var plan initialTransportSuccessorPlan
	parent, ok := initialTransportSuccessorCheckpoint(e.Journal)
	if !ok || candidate.Schema != "rtk-cloud.initial-transport-successor/v1" || candidate.Parent != e.Journal.PlanSHA256 || store.Environment != cfg.Environment || len(candidate.OriginalSHA) != 2 || len(candidate.Transports) != 2 || len(candidate.AuthorityKey) != 2 {
		return plan, createProviderRecoveryBlock("original interrupted bootstrap and successor candidate are required")
	}
	if _, err := verifyIssuerCoreContinuationAdmission(store, e, parent); err != nil {
		return plan, err
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return plan, err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return plan, err
	}
	old := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		path := filepath.Join(store.Root, "deployment", "create", "material", candidate.Parent, role+"-transport", "identities.json")
		raw, err := readBoundedEnvironmentFile(path, true)
		var material environmentCreateTransport
		if err != nil || environmentPreflightBytesSHA256(raw) != candidate.OriginalSHA[role] || json.Unmarshal(raw, &material) != nil || verifyInitialTransportSuccessor(material, candidate.Transports[role], []byte(candidate.AuthorityKey[role]), time.Now().UTC()) != nil {
			return plan, createProviderRecoveryBlock("successor candidate differs from original temporary transport: " + role)
		}
		old[role] = material
	}
	pod, set, err := originalCreateOpenBaoFoundationIdentity(ctx, cfg, store, e, b, "kubernetes/foundation")
	if err != nil {
		return plan, err
	}
	if err := verifyActiveTransportMounted(ctx, cfg, b, old["openbao"], candidate.Transports["openbao"], false); err != nil {
		return plan, err
	}
	resources, err := initialTransportSuccessorOwners(ctx, cfg, store, e, b, old, candidate.Transports)
	if err != nil {
		return plan, err
	}
	for _, resource := range resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil {
			return plan, err
		}
		body, err := initialTransportSuccessorResourceBody(resource, actual, old["openbao"].CACert, candidate.Transports["openbao"].CACert)
		if err != nil {
			return plan, err
		}
		if _, err := b.Write(ctx, "update", body, true); err != nil {
			return plan, err
		}
	}
	plan = initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: candidate.Parent, OwnerSHA: deploymentplan.Digest(e.owner), JournalSHA: deploymentplan.Digest(e.Journal), ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: environmentPreflightBytesSHA256(kube), CheckerSHA: checker, CandidateSHA: deploymentplan.Digest(candidate), PodUID: pod, StatefulSetUID: set, Resources: resources}
	return plan, e.Check(ctx)
}

// Select the recorded bootstrap cutover after App trust completed and the
// original CertIssuer role write became uncertain. The pending role must later
// be reconciled with OpenBao, not treated as completed by this selection.
func initialTransportSuccessorCheckpoint(j environmentCreateJournal) (environmentCreateJournalEntry, bool) {
	var parent environmentCreateJournalEntry
	if len(j.Entries) == 0 {
		return parent, false
	}
	last := j.Entries[len(j.Entries)-1]
	prefix := "openbao-role-certissuer-" + j.Environment + "-"
	if !strings.HasPrefix(last.Key, prefix) || len(strings.TrimPrefix(last.Key, prefix)) != 12 || last.Phase != "bootstrap" || last.State != "pending" || last.StartedAt.IsZero() || !last.CompletedAt.IsZero() || len(last.Receipt) != 0 || !environmentPreflightDigest.MatchString(last.IntentSHA256) || strings.TrimPrefix(last.Key, prefix) != last.IntentSHA256[:12] {
		return parent, false
	}
	seen := map[string]bool{}
	for _, entry := range j.Entries {
		if seen[entry.Key] {
			return parent, false
		}
		seen[entry.Key] = true
		if entry.Key == last.Key {
			continue
		}
		if strings.HasPrefix(entry.Key, issuerEnrollmentContinuationPrefix) && entry.State == "pending" {
			if parent.Key != "" || entry.Phase != "continuation" || entry.StartedAt.IsZero() || len(entry.Receipt) != 0 || !environmentPreflightDigest.MatchString(strings.TrimPrefix(entry.Key, issuerEnrollmentContinuationPrefix)) {
				return parent, false
			}
			parent = entry
			continue
		}
		if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return parent, false
		}
	}
	return parent, parent.Key != "" && seen["kubernetes/device-root-active"] && seen["kubernetes/app-root-trust"] && seen["kubernetes/app-intermediate-trust"]
}

// The candidate is a single private, exclusive artifact. A retry reads and
// validates it, preserving the exact keys generated by the first attempt.
func prepareInitialTransportSuccessor(store secretStore, parent string, now time.Time) (initialTransportSuccessorCandidate, error) {
	return initialTransportSuccessorCandidateState(store, parent, now, true)
}

// Qualification reads an existing candidate. It never generates a key.
func readInitialTransportSuccessorCandidate(store secretStore, parent string, now time.Time) (initialTransportSuccessorCandidate, error) {
	return initialTransportSuccessorCandidateState(store, parent, now, false)
}

func initialTransportSuccessorCandidateState(store secretStore, parent string, now time.Time, prepare bool) (initialTransportSuccessorCandidate, error) {
	var candidate initialTransportSuccessorCandidate
	bad := func() (initialTransportSuccessorCandidate, error) {
		return candidate, createProviderRecoveryBlock("original temporary transport or retained successor candidate is inconsistent")
	}
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	old := map[string]environmentCreateTransport{}
	originalSHA := map[string]string{}
	for _, role := range []string{"management", "openbao"} {
		relative := filepath.Join("deployment", "create", "material", parent, role+"-transport", "identities.json")
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
		var transport environmentCreateTransport
		if err != nil || json.Unmarshal(raw, &transport) != nil {
			return bad()
		}
		old[role], originalSHA[role] = transport, environmentPreflightBytesSHA256(raw)
	}
	relative := filepath.Join("deployment", "create", parent, "transport-successor", "candidate.json")
	path, err := store.safePath(relative)
	if err != nil {
		return bad()
	}
	if raw, err := readBoundedEnvironmentFile(path, true); err == nil {
		if json.Unmarshal(raw, &candidate) != nil {
			return bad()
		}
	} else if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return bad()
	} else {
		if !prepare {
			return bad()
		}
		candidate = initialTransportSuccessorCandidate{Schema: "rtk-cloud.initial-transport-successor/v1", Parent: parent, OriginalSHA: originalSHA, Transports: map[string]environmentCreateTransport{}, AuthorityKey: map[string]string{}}
		for _, role := range []string{"management", "openbao"} {
			transport, key, err := renewInitialTransportMaterial(old[role], now)
			if err != nil {
				return bad()
			}
			candidate.Transports[role], candidate.AuthorityKey[role] = transport, string(key)
		}
		if err := store.write(relative, mustProviderRecoveryJSON(candidate), false); err != nil {
			return bad()
		}
	}
	if candidate.Schema != "rtk-cloud.initial-transport-successor/v1" || candidate.Parent != parent || deploymentplan.Digest(candidate.OriginalSHA) != deploymentplan.Digest(originalSHA) || len(candidate.Transports) != 2 || len(candidate.AuthorityKey) != 2 {
		return bad()
	}
	for _, role := range []string{"management", "openbao"} {
		if err := verifyInitialTransportSuccessor(old[role], candidate.Transports[role], []byte(candidate.AuthorityKey[role]), now); err != nil {
			return bad()
		}
	}
	return candidate, nil
}

// Compile trust and transport projections from accepted stage records. Live
// resources provide a UID and resource version, never configuration intent.
func initialTransportSuccessorOwners(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, old, next map[string]environmentCreateTransport) ([]activeTransportRenewalResource, error) {
	bad := func() ([]activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("temporary transport successor requires every original accepted trust owner and projection")
	}
	latest := map[string]deploymentplan.Resource{}
	owners := map[string]string{}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		var receipt struct {
			Plan string `json:"plan_sha256"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
			continue
		}
		plan, journal, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil {
			return nil, err
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		sha, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || sha != entry.IntentSHA256 {
			return bad()
		}
		for _, resource := range plan.Resources {
			uid := journal.Owners[resource.ID]
			if previous := owners[resource.ID]; previous != "" && previous != uid {
				return bad()
			}
			latest[resource.ID], owners[resource.ID] = resource, uid
		}
	}
	oldCA, newCA := old["openbao"].CACert, next["openbao"].CACert
	if oldCA == "" || newCA == "" || old["management"].CACert == "" || next["management"].CACert == "" {
		return bad()
	}
	var result []activeTransportRenewalResource
	covered := map[string]bool{}
	for id, resource := range latest {
		before, changed := initialTransportOverride(resource.Desired, old)
		var desired deploymentplan.Object
		var err error
		if changed {
			desired, _ = initialTransportOverride(resource.Desired, next)
		} else if resource.Desired["kind"] == "ConfigMap" && deploymentplan.Map(resource.Desired["metadata"])["name"] == "pki-create-openbao-tls-consumer-trust" {
			before = resource.Desired
			desired, err = initialTransportReplacePublicCA(before, "roots.pem", oldCA, newCA)
			if err != nil {
				return nil, err
			}
			changed = true
		} else if resource.ID == "ConfigMap/"+cfg.Values["CLOUD_STACK_NAME"]+"-secrets/pki-create-service-trust" {
			before = resource.Desired
			desired, err = initialTransportReplacePublicCA(before, "client-roots.pem", old["management"].CACert, next["management"].CACert)
			if err != nil {
				return nil, err
			}
			changed = true
		} else if resource.Desired["kind"] == "ConfigMap" && deploymentplan.Map(resource.Desired["metadata"])["name"] == environmentCustodyTrustName {
			before = resource.Desired
			desired, err = initialTransportCustodySuccessor(before, oldCA, newCA)
			if err != nil {
				return nil, err
			}
			changed = true
		} else if resource.Desired["kind"] == "Deployment" {
			pod := environmentPKIPod(resource.Desired)
			for _, field := range []string{"containers", "initContainers"} {
				for _, value := range certIssuerObjectList(pod[field]) {
					if mqttFoundationEnv(deploymentplan.Map(value), "PKI_STATE_BOOTSTRAP_ROOT_SHA256") != "" {
						before = resource.Desired
						desired, err = initialTransportCustodySuccessor(before, oldCA, newCA)
						if err != nil {
							return nil, err
						}
						changed = true
					}
				}
			}
		}
		if !changed {
			continue
		}
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != owners[id] || !deploymentplan.Matches(before, actual) {
			return bad()
		}
		result = append(result, activeTransportRenewalResource{ID: id, UID: owners[id], Before: before, Desired: desired})
		covered[id] = true
	}
	if len(result) == 0 {
		return bad()
	}
	objects, err := b.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, err
	}
	byID := make(map[string]deploymentplan.Object, len(objects))
	for _, object := range objects {
		byID[deploymentplan.ID(object)] = object
	}
	for _, object := range objects {
		if object["kind"] != "Deployment" && object["kind"] != "StatefulSet" && object["kind"] != "DaemonSet" && object["kind"] != "Job" && object["kind"] != "CronJob" && object["kind"] != "ConfigMap" && object["kind"] != "Secret" {
			continue
		}
		if object["kind"] == "Job" && deploymentplan.HistoricalTerminalWorkload(deploymentplan.ID(object), byID, cfg.Values["CLOUD_STACK_NAME"]) {
			continue
		}
		if initialTransportReferencesAuthority(object, old["management"].CACert, oldCA) && !covered[deploymentplan.ID(object)] {
			return nil, createProviderRecoveryBlock("temporary transport has an unaccounted original trust reference: " + deploymentplan.ID(object))
		}
	}
	priority := map[string]int{"ConfigMap": 0, "Secret": 1, "Deployment": 2}
	sort.Slice(result, func(i, j int) bool {
		left, right := priority[deploymentplan.String(result[i].Desired["kind"])], priority[deploymentplan.String(result[j].Desired["kind"])]
		if left != right {
			return left < right
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func initialTransportReferencesAuthority(object deploymentplan.Object, managementCA, openbaoCA string) bool {
	cas := []string{strings.TrimSpace(managementCA), strings.TrimSpace(openbaoCA)}
	var pins []string
	for _, ca := range cas {
		certificate, err := parseFirstCertificatePEM([]byte(ca))
		if err != nil || certificate == nil {
			return true
		}
		pins = append(pins, environmentPreflightBytesSHA256(certificate.Raw))
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return true
	}
	for _, pin := range pins {
		if strings.Contains(string(raw), pin) {
			return true
		}
	}
	for _, value := range deploymentplan.Map(object["data"]) {
		data := deploymentplan.String(value)
		if object["kind"] == "Secret" {
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				continue
			}
			data = string(decoded)
		}
		for _, ca := range cas {
			if strings.Contains(data, ca) || strings.Contains(data, strings.ReplaceAll(ca, "\n", "\\n")) {
				return true
			}
		}
	}
	return false
}

func initialTransportReplacePublicCA(original deploymentplan.Object, key, oldCA, newCA string) (deploymentplan.Object, error) {
	result := deploymentplan.Clone(original)
	data := deploymentplan.Map(result["data"])
	value := deploymentplan.String(data[key])
	oldCA, newCA = strings.TrimSpace(oldCA), strings.TrimSpace(newCA)
	if oldCA == "" || newCA == "" || oldCA == newCA || strings.Count(value, oldCA) != 1 {
		return nil, createProviderRecoveryBlock("original public trust bundle does not contain the exact temporary authority once")
	}
	data[key] = strings.Replace(value, oldCA, newCA, 1)
	return result, nil
}

// Build a resourceVersion-bound update while retaining Kubernetes defaults and
// unrelated metadata. Only the previously qualified pin or data keys change.
func initialTransportSuccessorResourceBody(resource activeTransportRenewalResource, actual deploymentplan.Object, oldCA, newCA string) (deploymentplan.Object, error) {
	if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Before, actual) && !deploymentplan.Matches(resource.Desired, actual) {
		return nil, createProviderRecoveryBlock("temporary transport owner changed during successor application: " + resource.ID)
	}
	body := deploymentplan.Clone(actual)
	if resource.Desired["kind"] == "Deployment" {
		expected, err := initialTransportCustodySuccessor(resource.Before, oldCA, newCA)
		if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(resource.Desired) {
			return nil, createProviderRecoveryBlock("temporary custody workload update exceeds its qualified pin")
		}
		livePod, desiredPod := environmentPKIPod(body), environmentPKIPod(resource.Desired)
		for _, field := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(desiredPod[field]) {
				wanted := deploymentplan.Map(raw)
				pin := mqttFoundationEnv(wanted, "PKI_STATE_BOOTSTRAP_ROOT_SHA256")
				if pin == "" {
					continue
				}
				live := managedNamedField(livePod[field], deploymentplan.String(wanted["name"]))
				env := managedNamedField(live["env"], "PKI_STATE_BOOTSTRAP_ROOT_SHA256")
				if env == nil {
					return nil, createProviderRecoveryBlock("original custody workload pin is unavailable")
				}
				env["value"] = pin
			}
		}
	} else {
		before, desired := deploymentplan.Clone(resource.Before), deploymentplan.Clone(resource.Desired)
		beforeData, desiredData := deploymentplan.Map(before["data"]), deploymentplan.Map(desired["data"])
		delete(before, "data")
		delete(desired, "data")
		if beforeData == nil || desiredData == nil || deploymentplan.Digest(before) != deploymentplan.Digest(desired) || len(beforeData) != len(desiredData) {
			return nil, createProviderRecoveryBlock("temporary transport update exceeds its original resource scope")
		}
		allowed := map[string]bool{}
		switch deploymentplan.String(resource.Desired["kind"]) {
		case "Secret":
			allowed = map[string]bool{"ca.crt": true, "tls.crt": true, "tls.key": true}
		case "ConfigMap":
			switch deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"]) {
			case "pki-create-transport-trust", "pki-openbao-transport-ca":
				allowed["ca.crt"] = true
			case "pki-create-openbao-tls-consumer-trust":
				allowed["roots.pem"] = true
			case "pki-create-service-trust":
				allowed["client-roots.pem"] = true
			case environmentCustodyTrustName:
				allowed["transport.json"] = true
			}
		}
		for key, previous := range beforeData {
			value, exists := desiredData[key]
			if !exists || (deploymentplan.Digest(previous) != deploymentplan.Digest(value) && !allowed[key]) {
				return nil, createProviderRecoveryBlock("temporary transport update changes unrelated resource data")
			}
		}
		data := deploymentplan.Map(body["data"])
		if data == nil {
			return nil, createProviderRecoveryBlock("original temporary transport data is unavailable")
		}
		for key, value := range deploymentplan.Map(resource.Desired["data"]) {
			data[key] = value
		}
	}
	delete(body, "status")
	return body, nil
}

// Derive one bounded trust update from the accepted original object. The
// caller must independently bind its UID and baseline to recorded ownership.
func initialTransportCustodySuccessor(original deploymentplan.Object, oldCA, newCA string) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, createProviderRecoveryBlock("original custody trust does not match the temporary authority")
	}
	oldCert, err := parseFirstCertificatePEM([]byte(oldCA))
	if err != nil || oldCert == nil || !oldCert.IsCA || oldCert.CheckSignatureFrom(oldCert) != nil {
		return bad()
	}
	newCert, err := parseFirstCertificatePEM([]byte(newCA))
	if err != nil || newCert == nil || !newCert.IsCA || newCert.CheckSignatureFrom(newCert) != nil || oldCert.Equal(newCert) || oldCert.Subject.String() != newCert.Subject.String() {
		return bad()
	}
	oldPin, newPin := environmentPreflightBytesSHA256(oldCert.Raw), environmentPreflightBytesSHA256(newCert.Raw)
	result := deploymentplan.Clone(original)
	switch deploymentplan.String(result["kind"]) {
	case "ConfigMap":
		if deploymentplan.Map(result["metadata"])["name"] != environmentCustodyTrustName {
			return bad()
		}
		data := deploymentplan.Map(result["data"])
		var snapshot environmentCustodyTransportTrust
		if json.Unmarshal([]byte(deploymentplan.String(data["transport.json"])), &snapshot) != nil || snapshot.Schema != "rtk.custody.transport/v1" || snapshot.ManagedOnly || snapshot.BootstrapPEM != oldCA || snapshot.Environment == "" || !environmentPreflightDigest.MatchString(snapshot.Generation) {
			return bad()
		}
		snapshot.BootstrapPEM = newCA
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return bad()
		}
		data["transport.json"] = string(raw)
	case "Deployment":
		pod := environmentPKIPod(result)
		found := 0
		for _, field := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(pod[field]) {
				container := deploymentplan.Map(raw)
				for _, item := range certIssuerObjectList(container["env"]) {
					env := deploymentplan.Map(item)
					if env["name"] != "PKI_STATE_BOOTSTRAP_ROOT_SHA256" {
						continue
					}
					if env["value"] != oldPin || mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" || mqttFoundationEnv(container, "PKI_STATE_TRANSPORT_TRUST") != environmentCustodyTrustPath+"/transport.json" {
						return bad()
					}
					env["value"], found = newPin, found+1
				}
			}
		}
		if found == 0 {
			return bad()
		}
	default:
		return bad()
	}
	return result, nil
}
