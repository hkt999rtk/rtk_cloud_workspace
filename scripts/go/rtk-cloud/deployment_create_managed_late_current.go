// PRIVATE ONLY. Stage-aware read-only continuity after a genuinely admitted new
// authority. Never reuses an old-source body checker after an accepted write.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

const managedLateAPINativeMaterial = "accepted-service-core-managed-application-native-api-servers"
const managedLateAPICurrentMaterial = "accepted-service-core-managed-application-current-api-servers"

func bindManagedLateFreshAPIServers(materials map[string]string, expected string) error {
	actual := materials[managedLateAPINativeMaterial]
	if !environmentPreflightDigest.MatchString(expected) {
		return managedCoreObservationBlock("late-original-api-csr-certificate-pin")
	}
	if !environmentPreflightDigest.MatchString(actual) {
		return managedCoreObservationBlock("late-fresh-api-csr-certificate-read")
	}
	if actual != expected {
		return managedCoreObservationBlock("late-fresh-api-csr-certificate-drift")
	}
	materials[managedLateAPICurrentMaterial] = actual
	return nil
}

type managedLateHostServerAuthority struct{ OriginalReview, ArtifactSHA, SourceSHA, InventorySHA string }

func managedLateOriginalHostAuthority(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) (managedLateHostServerAuthority, error) {
	var empty managedLateHostServerAuthority
	if r == nil || r.ManagedLateSession == nil {
		return empty, managedCoreObservationBlock("late-original-host-source")
	}
	late := r.ManagedLateSession
	source, err := authenticateManagedLateObservationSource(cfg, store, e, r, p, late, late.Mode)
	if err != nil {
		return empty, err
	}
	admitted, _, err := loadAdmittedCreateRecovery(store, e, late.OriginalRepairReview)
	original := admitted.Plan.Materials["accepted-service-core-managed-application-current-servers"]
	if err != nil || source.ManagedCorePartial.ArtifactSHA != deploymentplan.Digest(admitted) || !environmentPreflightDigest.MatchString(original) || source.ManagedTransportRecovery.Partial.ServerSHA != original {
		return empty, managedCoreObservationBlock("late-original-host-artifact")
	}
	return managedLateHostServerAuthority{late.OriginalRepairReview, deploymentplan.Digest(admitted), late.SourceSHA, original}, nil
}

type managedLateCurrentChild struct {
	Plan    *deploymentplan.Plan
	Journal deploymentplan.Journal
	Entry   environmentCreateJournalEntry
}

// RuntimeProgress has already proved every literal stage/lease/recipe. This
// repeats its deterministic lease positions to expose allocated child bodies.
func managedLateCurrentChildren(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (map[string]managedLateCurrentChild, *environmentCreateExecution, *createFoundationRecoveryArtifact, error) {
	if late != nil && late.ExpiredRenewal != nil && late.SourceSHA == deploymentplan.Digest(e.Journal) {
		f := late.ExpiredRenewal
		if _, derived, err := loadManagedLateExpiredRenewalSource(f.PriorLate.OpenBaoTLSRepair.Plan.Configuration, store, e); err != nil || managedLateExpiredRenewalSourcePins(derived) != managedLateExpiredRenewalSourcePins(f) {
			return nil, nil, nil, managedLateExpiredRenewalBlock("current-source")
		}
		a, _, err := loadAdmittedCreateRecovery(store, e, f.PriorReview)
		return map[string]managedLateCurrentChild{}, e, &a, err
	}
	source, artifact, err := managedLateSessionRuntimeProgress(store, e, late)
	if err != nil {
		return nil, nil, nil, err
	}
	steps, err := managedLateProgressSteps(late)
	if err != nil {
		return nil, nil, nil, err
	}
	framed, _, frameErr := managedLatePostMintRuntimeFrame(store, e, late)
	if frameErr != nil {
		return nil, nil, nil, frameErr
	}
	f, entries, leases, frameErr := managedLateSeedForwardFrame(store, framed, late, source.Journal)
	if frameErr != nil {
		return nil, nil, nil, frameErr
	}
	children := map[string]managedLateCurrentChild{}
	stepIndex, leaseIndex := 0, len(source.Journal.KubernetesLeases)
	for _, entry := range entries[len(source.Journal.Entries)+1:] {
		if strings.HasPrefix(entry.Key, "openbao-managed-unseal-") {
			continue
		}
		if stepIndex >= len(steps) || entry.Key != steps[stepIndex].Key {
			return nil, nil, nil, managedCoreObservationBlock("late-child-position")
		}
		step := steps[stepIndex]
		if step.Kind == "native" {
			leaseIndex++
		} else if step.Kind != "inner" && step.Kind != "log-proof" {
			if leaseIndex >= len(leases) {
				return nil, nil, nil, managedCoreObservationBlock("late-child-lease")
			}
			var plan *deploymentplan.Plan
			var journal deploymentplan.Journal
			var err error
			if f != nil && entry.Key == "kubernetes/service-session-original-key-renewal-"+late.Review && entry.State == "completed" {
				plan, _, journal, err = readManagedLateEffectiveSeed(store, e, late)
			} else {
				plan, journal, err = readManagedLateProgressChild(store, e, entry, &leases[leaseIndex])
			}
			if err != nil {
				return nil, nil, nil, err
			}
			children[entry.Key] = managedLateCurrentChild{plan, journal, entry}
			leaseIndex++
		}
		stepIndex++
	}
	return children, source, artifact, nil
}

// A child may expose only its ordered completed prefix and at most one written
// resource awaiting readiness. Unwritten objects retain the previous authority.
func overlayManagedLateCurrentChild(owners map[string]deploymentplan.Resource, ready map[string]bool, child managedLateCurrentChild) error {
	p, j := child.Plan, child.Journal
	if p == nil || p.SHA256() != j.PlanSHA256 || len(j.Completed) > len(p.Resources) || j.Accepted && len(j.Completed) != len(p.Resources) || len(j.Owners) < len(j.Completed) || len(j.Owners) > len(j.Completed)+1 && !j.Accepted {
		return managedCoreObservationBlock("late-child-written-prefix")
	}
	seen := map[string]bool{}
	for index, resource := range p.Resources {
		if seen[resource.ID] {
			return managedCoreObservationBlock("late-duplicate-child-resource")
		}
		seen[resource.ID] = true
		completed := index < len(j.Completed)
		if completed && j.Completed[index] != resource.ID {
			return managedCoreObservationBlock("late-child-order")
		}
		uid := j.Owners[resource.ID]
		written := completed || index == len(j.Completed) && uid != ""
		if !written {
			if uid != "" {
				return managedCoreObservationBlock("late-unwritten-child-owner")
			}
			continue
		}
		previous, exists := owners[resource.ID]
		if !exists || !environmentBootstrapOwnerUUID.MatchString(uid) || uid != previous.UID || resource.UID != "" && resource.UID != uid || resource.Action != "update" && resource.Action != "retain" || !deploymentplan.Matches(previous.Desired, resource.Baseline) {
			return managedCoreObservationBlock("late-original-child-owner-" + resource.ID)
		}
		current := resource
		current.UID = uid
		owners[resource.ID] = current
		ready[resource.ID] = completed
	}
	for id := range j.Owners {
		if !seen[id] {
			return managedCoreObservationBlock("late-foreign-child-owner")
		}
	}
	return nil
}

// The cleanup child is a real five-object producer: one newly allocated CM and
// four original RBAC owners. Only the existing owners are returned here, from
// its accepted Desired bodies; no live body or fabricated CM owner is authority.
func managedLateAcceptedCleanupRBAC(late *createManagedLateSessionRecovery, children map[string]managedLateCurrentChild) (map[string]deploymentplan.Resource, error) {
	bad := func(stage string) (map[string]deploymentplan.Resource, error) {
		return nil, managedCoreObservationBlock("late-current-accepted-cleanup-" + stage)
	}
	if late == nil || !createOpenBaoACKCompletedChild(late.AcceptedCore, late.AcceptedCoreJournal) || late.ExpiredRenewal == nil && !environmentPreflightDigest.MatchString(late.Review) || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) {
		return bad("source")
	}
	key := "kubernetes/service-cleanup-binding-renewal-" + late.Review
	cleanup, exists := children[key]
	if !exists {
		return map[string]deploymentplan.Resource{}, nil
	}
	credentialKey := "kubernetes/service-credential-secrets-renewal-" + late.Review
	credentials, ok := children[credentialKey]
	for expectedKey, child := range map[string]managedLateCurrentChild{key: cleanup, credentialKey: credentials} {
		if !ok || child.Entry.Key != expectedKey || child.Entry.Phase != strings.TrimPrefix(expectedKey, "kubernetes/") || child.Entry.State != "completed" || child.Entry.StartedAt.IsZero() || child.Entry.CompletedAt.IsZero() || child.Entry.CompletedAt.Before(child.Entry.StartedAt) || !createOpenBaoACKCompletedChild(child.Plan, child.Journal) || !child.Journal.AcceptanceStarted {
			return bad("completed-credential-and-cleanup-children")
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if decodeEnvironmentJSON(child.Entry.Receipt, &receipt) != nil || receipt.Plan != child.Plan.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(child.Journal.Owners) {
			return bad("accepted-child-receipt")
		}
		for index, resource := range child.Plan.Resources {
			if child.Journal.Completed[index] != resource.ID {
				return bad("accepted-child-order")
			}
		}
		intent, err := environmentCreateKubernetesStageIntent(child.Plan)
		if err != nil || child.Entry.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) {
			return bad("child-intent")
		}
	}
	if cleanup.Entry.StartedAt.Before(credentials.Entry.CompletedAt) || validateManagedLateCredentialChild(late, credentials.Plan) != nil || validateManagedLateCleanupChild(late, cleanup.Plan, credentials.Journal) != nil {
		return bad("allocated-credential-and-qualified-producer")
	}
	stack := late.AcceptedCore.Stack
	ns := stack + "-video-cloud"
	ids := map[string]bool{"Role/" + ns + "/bootstrap-cleanup": true, "RoleBinding/" + ns + "/bootstrap-cleanup": true, "ClusterRole//" + stack + "-bootstrap-cleanup": true, "ClusterRoleBinding//" + stack + "-bootstrap-cleanup": true}
	original := map[string]deploymentplan.Resource{}
	for _, resource := range late.AcceptedCore.Resources {
		if ids[resource.ID] {
			if _, duplicate := original[resource.ID]; duplicate {
				return bad("original-rbac")
			}
			original[resource.ID] = resource
		}
	}
	if len(original) != 4 {
		return bad("original-rbac")
	}
	result := map[string]deploymentplan.Resource{}
	for _, resource := range cleanup.Plan.Resources {
		if !ids[resource.ID] {
			continue
		}
		source := original[resource.ID]
		if late.ExpiredRenewal != nil {
			var ok bool
			source, ok = managedLateRBACSource(late, resource.ID)
			if !ok {
				return bad("prior-accepted-cleanup-source")
			}
		}
		uid := cleanup.Journal.Owners[resource.ID]
		if !environmentBootstrapOwnerUUID.MatchString(uid) || source.UID != uid || late.AcceptedCoreJournal.Owners[resource.ID] != uid || resource.UID != uid || resource.Action != "update" && resource.Action != "retain" || !deploymentplan.Matches(source.Desired, resource.Baseline) {
			return bad("original-uid-and-baseline")
		}
		resource.Desired = deploymentplan.Clone(resource.Desired)
		resource.Baseline = deploymentplan.Clone(resource.Baseline)
		resource.Dependencies = slices.Clone(resource.Dependencies)
		result[resource.ID] = resource
	}
	if len(result) != 4 {
		return bad("exact-four-rbac-owners")
	}
	return result, nil
}

func managedLateAcceptedCoreDefinitions(late *createManagedLateSessionRecovery, current map[string]deploymentplan.Resource) ([]deploymentplan.Definition, error) {
	if late == nil || !createOpenBaoACKCompletedChild(late.AcceptedCore, late.AcceptedCoreJournal) {
		return nil, environmentCreateBootstrapBlocker("late current core accepted source missing")
	}
	var definitions []deploymentplan.Definition
	for _, original := range late.AcceptedCore.Resources {
		desired := original.Desired
		if resource, ok := current[original.ID]; ok {
			if resource.UID != late.AcceptedCoreJournal.Owners[original.ID] {
				return nil, environmentCreateBootstrapBlocker("late current core owner replaced")
			}
			desired = resource.Desired
		}
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(desired), Phase: original.Phase, Dependencies: slices.Clone(original.Dependencies)})
	}
	return definitions, nil
}

func managedLateCurrentOwners(r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, children map[string]managedLateCurrentChild) (map[string]deploymentplan.Resource, map[string]bool, error) {
	owners := map[string]deploymentplan.Resource{}
	ready := map[string]bool{}
	if r == nil || r.ManagedCorePartial == nil || late == nil || late.AcceptedCore == nil {
		return nil, nil, managedCoreObservationBlock("late-current-accepted-source")
	}
	for _, item := range []struct {
		p *deploymentplan.Plan
		j deploymentplan.Journal
	}{{late.AcceptedCore, late.AcceptedCoreJournal}, {r.ManagedCorePartial.Apps, r.ManagedCorePartial.AppsJournal}, {r.ManagedCorePartial.Routing, r.ManagedCorePartial.RoutingJournal}} {
		if item.p == nil || !createOpenBaoACKCompletedChild(item.p, item.j) {
			return nil, nil, managedCoreObservationBlock("late-current-original-accepted-plan")
		}
		for _, resource := range item.p.Resources {
			resource.UID = item.j.Owners[resource.ID]
			owners[resource.ID] = resource
			ready[resource.ID] = true
		}
	}
	owners[late.Custody.ID] = late.Custody
	ready[late.Custody.ID] = true
	// The accepted original736 host is independently authenticated by the source
	// loader and supersedes the historical managed transport body.
	for id, resource := range late.Owners {
		owners[id] = resource
		ready[id] = true
	}
	if f := late.ExpiredRenewal; f != nil {
		for id, resource := range f.PriorOwners {
			owners[id], ready[id] = resource, true
		}
	}
	cleanup, err := managedLateAcceptedCleanupRBAC(late, children)
	if err != nil {
		return nil, nil, err
	}
	for id, resource := range cleanup {
		previous, exists := owners[id]
		if !exists || previous.UID != resource.UID || !deploymentplan.Matches(previous.Desired, resource.Baseline) {
			return nil, nil, managedCoreObservationBlock("late-current-cleanup-source-owner-" + id)
		}
		owners[id], ready[id] = resource, true
	}
	for _, base := range []string{"service-custody-reader-renewal", "managed-late-session-owners", "managed-late-log-stop", "managed-late-log-restore"} {
		if child, ok := children["kubernetes/"+base+"-"+late.Review]; ok {
			if err := overlayManagedLateCurrentChild(owners, ready, child); err != nil {
				return nil, nil, err
			}
		}
	}
	return owners, ready, nil
}

func managedLateOriginalSessionCurrent(current, saved managedSessionState, newActive bool, subjects []string, now time.Time) bool {
	if current.Session != saved.Session || current.Issuer != saved.Issuer || current.Root != saved.Root || current.Fingerprint != saved.Fingerprint || !current.Expires.Equal(saved.Expires) || current.Expires.IsZero() || current.Expires.After(now) || current.Status != "active" && current.Status != "expired" || !slices.Equal(current.Subjects, subjects) || !slices.Equal(saved.Subjects, subjects) {
		return false
	}
	installed := slices.Clone(current.Installed)
	original := slices.Clone(saved.Installed)
	slices.Sort(installed)
	slices.Sort(original)
	if len(installed) != 8 || !slices.Equal(installed, original) || slices.Contains(installed, "service:video-cloud-logingester") {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range installed {
		if seen[subject] || !slices.Contains(subjects, subject) {
			return false
		}
		seen[subject] = true
	}
	expectedOthers := 0
	if newActive {
		expectedOthers = 1
	}
	return current.OtherActive == expectedOthers
}

func managedLateNewSessionCurrent(current managedSessionState, late *createManagedLateSessionRecovery, certificate string, subjects []string, now time.Time) bool {
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	ica, icaErr := parseFirstCertificatePEM([]byte(late.SourceCeremony.Intermediate.Certificate))
	if err != nil || icaErr != nil || current.Session != late.NewSessionID || current.Issuer != late.SourceCeremony.Intermediate.ID || current.Root != late.SourceCeremony.Root.RootFingerprint || current.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || current.Status != "active" || current.OtherActive != 0 || !current.Expires.Equal(leaf.NotAfter) || !current.Expires.After(now) || leaf.NotBefore.After(now) || leaf.CheckSignatureFrom(ica) != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) || !slices.Equal(current.Subjects, subjects) {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range current.Installed {
		if seen[subject] || !slices.Contains(subjects, subject) {
			return false
		}
		seen[subject] = true
	}
	return true
}

// One fresh boundary snapshot checks every current body. Only the authenticated
// ready prefix enters the shared retained target verifier; written resources
// still awaiting acceptance never acquire invented readiness authority.
func verifyManagedLateCurrentOwnerInventory(ctx context.Context, store secretStore, inputs deploymentplan.Inputs, owners map[string]deploymentplan.Resource, ready map[string]bool, objects []deploymentplan.Object) (map[string]string, error) {
	live := map[string]deploymentplan.Object{}
	for _, object := range objects {
		id := deploymentplan.ID(object)
		if live[id] != nil {
			return nil, managedCoreObservationBlock("late-current-duplicate-owner-inventory")
		}
		live[id] = object
	}
	retained := &deploymentplan.Plan{Inputs: inputs}
	retained.Operation = "scoped"
	journal := deploymentplan.Journal{Owners: map[string]string{}}
	fingerprints := map[string]string{}
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		resource, actual := owners[id], live[id]
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return nil, managedCoreObservationBlock("late-current-owner-" + id)
		}
		fingerprints[id] = deploymentplan.Digest(deploymentplan.Projection(actual))
		if ready[id] {
			resource.Action = "retain"
			retained.Resources = append(retained.Resources, resource)
			journal.Owners[id] = resource.UID
		}
	}
	if err := verifyOriginalMigrationCheckpointInventory(ctx, store, retained, journal, objects); err != nil {
		return nil, err
	}
	return fingerprints, ctx.Err()
}

func verifyManagedLateSessionCurrentNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, late *createManagedLateSessionRecovery) error {
	if ctx == nil || e == nil || r == nil || p == nil || p.Materials == nil || r.ManagedLateSession == nil || managedLateSessionSourceDigest(r.ManagedLateSession) != managedLateSessionSourceDigest(late) || e.Check(ctx) != nil {
		return managedCoreObservationBlock("late-current-native-profile")
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, late); err != nil {
		return err
	}
	children, captured, admitted, err := managedLateCurrentChildren(store, e, late)
	if err != nil {
		return err
	}
	sourcePlan := createFoundationRecoveryPlan{Plan: admitted.Plan.Plan, Materials: map[string]string{}}
	source, loaded, err := loadManagedLateEffectiveSource(cfg, store, captured, &sourcePlan, late)
	if err != nil || loaded.SourceSHA != late.SourceSHA {
		return managedCoreObservationBlock("late-current-native-original-source")
	}
	source.ManagedLateSession = late
	owners, ready, err := managedLateCurrentOwners(source, late, children)
	if err != nil {
		return err
	}
	if err := managedLateAppRootCRLReadyExceptions(ctx, store, e, r, ready); err != nil {
		return err
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return managedCoreObservationBlock("late-current-owner-inventory")
	}
	fingerprints, err := verifyManagedLateCurrentOwnerInventory(ctx, store, late.AcceptedCore.Inputs, owners, ready, objects)
	if err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, source, p); err != nil {
		return err
	}
	if err := verifyManagedCoreAllVolumes(ctx, e, b, p.Materials); err != nil {
		return err
	}
	if p.Materials["accepted-service-core-managed-complete-current-native-volumes"] != admitted.Plan.Materials["accepted-service-core-managed-complete-current-native-volumes"] {
		return managedCoreObservationBlock("late-current-eighteen-volume-pins")
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, source.ManagedApplicationPartial, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, source, p.Materials); err != nil {
		return err
	}
	if err := bindManagedLateFreshAPIServers(p.Materials, admitted.Plan.Materials[managedLateAPICurrentMaterial]); err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, late.SourceCeremony, subjects)
	if err != nil || clients != late.OriginalClientsSHA || clients != admitted.Plan.Materials["accepted-service-core-managed-application-current-clients"] {
		return managedCoreObservationBlock("late-current-eleven-clients")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"mqtt": *source.MQTTRoot, "openbao_tls": *source.OpenBaoRoot}
	icas := map[string]environmentCreateRegisteredIssuer{"mqtt": *source.MQTTIntermediate, "openbao_tls": *source.OpenBaoIntermediate}
	bootstrap := admitted.Plan.Plan.Bootstrap
	hosts, err := readManagedTransportServers(ctx, cfg, runtime, bootstrap, roots, icas)
	if err != nil || validateManagedCoreOriginalHostServerDigest(hosts, source.ManagedTransportRecovery.Partial.ServerSHA) != nil || hosts != admitted.Plan.Materials["accepted-service-core-managed-application-current-servers"] {
		return managedCoreObservationBlock("late-current-original-host-servers")
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p); err != nil {
		return err
	}
	if p.Materials["accepted-service-core-managed-active-registry"] != admitted.Plan.Materials["accepted-service-core-managed-active-registry"] {
		return managedCoreObservationBlock("late-current-nine-ca-pins")
	}
	key, err := b.command(ctx, nil, "--namespace", late.SourceCeremony.BootstrapCredentialNamespace, "exec", "deployment/"+late.SourceCeremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.key")
	if err != nil || environmentPreflightBytesSHA256(key) != late.OriginalKeySHA {
		return managedCoreObservationBlock("late-current-original-key")
	}
	csr, err := b.command(ctx, nil, "--namespace", late.SourceCeremony.BootstrapCredentialNamespace, "exec", "deployment/"+late.SourceCeremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.csr")
	if err != nil || environmentPreflightBytesSHA256(csr) != late.OriginalCSRSHA {
		return managedCoreObservationBlock("late-current-original-csr")
	}
	seedAccepted := false
	newActive := false
	if child, ok := children["kubernetes/service-session-original-key-renewal-"+late.Review]; ok && child.Entry.State == "completed" {
		seedAccepted = true
		cert, err := b.command(ctx, nil, "--namespace", late.SourceCeremony.BootstrapCredentialNamespace, "exec", "deployment/"+late.SourceCeremony.CustodyReader, "--container", "custody", "--", "cat", late.NewCertificatePath)
		pair, pairErr := tls.X509KeyPair(cert, key)
		newState, raw, stateErr := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
		if err != nil || pairErr != nil || !managedLateBootstrapChainMatches(pair.Certificate, late.SourceCeremony) || stateErr != nil || !managedLateNewSessionCurrent(newState, late, string(cert), subjects, time.Now().UTC()) {
			return managedCoreObservationBlock("late-current-genuine-new-authority")
		}
		if post := r.ManagedLatePostMint; post != nil && post.InstallationClock == nil && len(newState.Installed) != 0 {
			return managedCoreObservationBlock("late-first-installation-native-already-installed")
		}
		if custody, ok := children["kubernetes/service-custody-reader-renewal-"+late.Review]; ok && custody.Entry.State == "completed" {
			var observed createManagedLateSeedObservation
			if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), &observed) != nil || observed.KeySHA != late.OriginalKeySHA || observed.CSRSHA != late.OriginalCSRSHA || observed.CertificatePEM != string(cert) || observed.Session.Fingerprint != newState.Fingerprint || !observed.Session.Expires.Equal(newState.Expires) || observed.SeedPlanSHA != child.Plan.SHA256() || observed.SeedJournalSHA != deploymentplan.Digest(child.Journal) {
				return managedCoreObservationBlock("late-current-accepted-seed-observation")
			}
		}
		p.Materials["accepted-service-core-managed-late-current-session"] = environmentPreflightBytesSHA256(raw)
		newActive = true
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil || !managedLateOriginalSessionForScope(old, late, newActive, subjects, time.Now().UTC()) {
		return managedCoreObservationBlock("late-current-original-expired-eight")
	}
	if f := late.ExpiredRenewal; f != nil {
		ancestor, _, ancestorErr := readManagedSessionState(ctx, cfg, runtime, f.PriorLate.OriginalSession)
		if ancestorErr != nil || !managedLateOriginalSessionCurrent(ancestor, f.PriorLate.OriginalSessionState, newActive, subjects, time.Now().UTC()) {
			return managedCoreObservationBlock("late-current-retained-e0-history")
		}
	}
	if !seedAccepted && r.Ceremony.BootstrapSessionID != late.OriginalSession || seedAccepted && r.Ceremony.BootstrapSessionID != late.OriginalSession && r.Ceremony.BootstrapSessionID != late.NewSessionID {
		return managedCoreObservationBlock("late-current-session-transition")
	}
	if err := verifyManagedLateCurrentCredentialCopies(ctx, cfg, store, e, b, late, children); err != nil {
		return err
	}
	if _, _, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, managedLateObserveACKReady); err != nil {
		return err
	}
	if err := verifyManagedLateCurrentCRLConsumers(ctx, cfg, store, e, b, late, owners, ready); err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-late-current-owners"] = deploymentplan.Digest(fingerprints)
	return e.Check(ctx)
}

func verifyManagedLateCurrentCredentialCopies(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, late *createManagedLateSessionRecovery, children map[string]managedLateCurrentChild) error {
	if late != nil && late.ExpiredRenewal != nil {
		if err := verifyManagedLateCurrentCredentialCopies(ctx, cfg, store, e, b, late.ExpiredRenewal.PriorLate, nil); err != nil {
			return err
		}
	}
	for _, ns := range late.SourceCeremony.BootstrapCredentialNamespaces {
		old, err := b.Read(ctx, "Secret/"+ns+"/service-bootstrap-"+late.OriginalSession)
		if err != nil || old == nil || deploymentplan.Map(old["metadata"])["uid"] != late.SourceCeremony.CredentialUIDs[ns] || deploymentplan.Map(old["metadata"])["deletionTimestamp"] != nil {
			return managedCoreObservationBlock("late-current-original-copy")
		}
		if err := validateManagedLateOriginalCredentialCopy(old, ns, late); err != nil {
			return err
		}
	}
	if child, ok := children["kubernetes/service-credential-secrets-renewal-"+late.Review]; ok {
		for _, resource := range child.Plan.Resources {
			uid := child.Journal.Owners[resource.ID]
			if uid == "" {
				continue
			}
			actual, err := b.Read(ctx, resource.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
				return managedCoreObservationBlock("late-current-new-copy")
			}
		}
	}
	return nil
}

func validateManagedLateOriginalCredentialCopy(secret deploymentplan.Object, namespace string, late *createManagedLateSessionRecovery) error {
	bad := func() error { return managedCoreObservationBlock("late-current-original-copy-material") }
	if secret == nil || late == nil || late.Renewal == nil || late.Renewal.OriginalSession != late.OriginalSession || !environmentPreflightDigest.MatchString(late.Renewal.OriginalCertificateSHA256) || secret["immutable"] != true || deploymentplan.ID(secret) != "Secret/"+namespace+"/service-bootstrap-"+late.OriginalSession {
		return bad()
	}
	meta := deploymentplan.Map(secret["metadata"])
	labels := deploymentplan.Map(meta["labels"])
	if meta["uid"] != late.SourceCeremony.CredentialUIDs[namespace] || labels["rtk.cloud/bootstrap-session"] != late.OriginalSession {
		return bad()
	}
	data := deploymentplan.Map(secret["data"])
	if len(data) != 3 {
		return bad()
	}
	values := map[string][]byte{}
	for _, name := range []string{"tls.key", "tls.crt", "ca.crt"} {
		raw, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data[name]))
		if err != nil || len(raw) == 0 {
			return bad()
		}
		values[name] = raw
	}
	pair, err := tls.X509KeyPair(values["tls.crt"], values["tls.key"])
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, late.SourceCeremony) || environmentPreflightBytesSHA256(values["tls.crt"]) != late.Renewal.OriginalCertificateSHA256 || environmentPreflightBytesSHA256(values["tls.key"]) != late.OriginalKeySHA || string(values["ca.crt"]) != late.SourceCeremony.Root.Certificate {
		return bad()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	ica, icaErr := parseFirstCertificatePEM([]byte(late.SourceCeremony.Intermediate.Certificate))
	if err != nil || icaErr != nil || leaf.CheckSignatureFrom(ica) != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(leaf.Raw) != late.OriginalSessionState.Fingerprint || !leaf.NotAfter.Equal(late.OriginalSessionState.Expires) {
		return bad()
	}
	return nil
}

// Cache probes use only current bodies authorized by RuntimeProgress. The
// completion artifact and its original Pod/custody provenance remain immutable.
func verifyManagedLateCurrentCRLConsumers(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, late *createManagedLateSessionRecovery, owners map[string]deploymentplan.Resource, ready map[string]bool) error {
	mqtt, mqttDone, err := readCreateMQTTRootCRLRecovery(store, late.ParentSHA)
	tlsRoot, tlsDone, tlsErr := readCreateOpenBaoTLSRootCRLRecovery(store, late.ParentSHA)
	if err != nil || tlsErr != nil || mqtt == nil || mqttDone == nil || tlsRoot == nil || tlsDone == nil || deploymentplan.Digest(mqtt) != deploymentplan.Digest(late.MQTTRepair) || deploymentplan.Digest(tlsRoot) != deploymentplan.Digest(late.OpenBaoTLSRepair) {
		return managedCoreObservationBlock("late-current-authentic-crl-completions")
	}
	// Each original private claim stays live; a scale-zero Log is guarded by the
	// exclusive native helper and is not treated as a running CRL consumer here.
	for _, target := range mqtt.Plan.Consumers {
		resource, ok := owners[target.ID]
		if !ok || resource.UID != target.UID {
			return managedCoreObservationBlock("late-current-mqtt-consumer-owner")
		}
		if deploymentplan.Map(resource.Desired["spec"])["replicas"] == float64(0) || deploymentplan.Map(resource.Desired["spec"])["replicas"] == 0 {
			continue
		}
		if !ready[target.ID] {
			continue
		}
		current, err := managedLateCurrentCRLTarget(ctx, cfg, b, resource, target)
		if err != nil {
			return err
		}
		if _, err := observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, *mqtt, current); err != nil {
			return err
		}
	}
	for _, target := range tlsRoot.Plan.Consumers {
		resource, ok := owners[target.ID]
		if !ok || resource.UID != target.UID {
			return managedCoreObservationBlock("late-current-tls-consumer-owner")
		}
		if !ready[target.ID] {
			continue
		}
		current, err := managedLateCurrentCRLTarget(ctx, cfg, b, resource, target)
		if err != nil {
			return err
		}
		if _, err := observeOpenBaoTLSRootCRLConsumer(ctx, cfg, store, e, b, *tlsRoot, current); err != nil {
			return err
		}
	}
	return e.Check(ctx)
}

func managedLateCurrentCRLTarget(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, resource deploymentplan.Resource, target createRootCRLStateTarget) (createRootCRLStateTarget, error) {
	bad := func() (createRootCRLStateTarget, error) {
		return target, managedCoreObservationBlock("late-current-native-cache-pod")
	}
	if resource.ID != target.ID || resource.UID != target.UID {
		return bad()
	}
	name := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])
	ns := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["namespace"])
	raw, err := b.command(ctx, nil, "--namespace", ns, "get", "pods", "-l", "app.kubernetes.io/name="+name, "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return bad()
	}
	pod := pods.Items[0]
	meta := deploymentplan.Map(pod["metadata"])
	refs := certIssuerObjectList(meta["ownerReferences"])
	if len(refs) != 1 || meta["deletionTimestamp"] != nil || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(meta["uid"])) {
		return bad()
	}
	ref := deploymentplan.Map(refs[0])
	if ref["kind"] != "ReplicaSet" || ref["controller"] != true {
		return bad()
	}
	rs, err := b.Read(ctx, "ReplicaSet/"+ns+"/"+deploymentplan.String(ref["name"]))
	rsmeta := deploymentplan.Map(rs["metadata"])
	rsrefs := certIssuerObjectList(rsmeta["ownerReferences"])
	if err != nil || rs == nil || rsmeta["uid"] != ref["uid"] || rsmeta["deletionTimestamp"] != nil || len(rsrefs) != 1 {
		return bad()
	}
	owner := deploymentplan.Map(rsrefs[0])
	if owner["kind"] != "Deployment" || owner["controller"] != true || owner["uid"] != resource.UID || owner["name"] != name {
		return bad()
	}
	expected := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(resource.Desired["spec"])["template"]}}
	actual := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(rs["spec"])["template"]}}
	if !deploymentplan.Matches(expected, actual) {
		return bad()
	}
	target.Desired = resource.Desired
	target.PodName = deploymentplan.String(meta["name"])
	target.PodUID = deploymentplan.String(meta["uid"])
	if verifyCreateRootCRLTargetOwners(ctx, cfg, b, target) != nil {
		return bad()
	}
	return target, nil
}
