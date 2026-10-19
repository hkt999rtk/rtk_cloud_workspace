package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceRootCRLQualificationValid(a createDeviceRootCRLArtifact) bool {
	review := deploymentplan.Digest(a.Plan)
	if a.Schema != createDeviceRootCRLSchema+"/artifact" || a.Plan.Schema != createDeviceRootCRLSchema || a.QualifiedAt.IsZero() || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createDeviceRootCRLGates) {
		return false
	}
	for i, g := range a.Qualification.Gates {
		if g.ID != createDeviceRootCRLGates[i] || g.Status != "PASS" || !g.Required || g.EvidenceSHA256 != review {
			return false
		}
	}
	return true
}

func deviceRootCRLJournalValid(a createDeviceRootCRLArtifact, j createDeviceRootCRLJournal, now time.Time) bool {
	p := a.Plan
	if !deviceRootCRLQualificationValid(a) || j.Schema != createDeviceRootCRLSchema+"/journal" || j.Parent != p.Parent || j.Review != deploymentplan.Digest(p) || j.SourceSHA != p.SourceSHA || j.OwnerSHA != p.OwnerSHA || j.StartedAt.Before(a.QualifiedAt) || !j.StartedAt.Before(a.ValidUntil) || j.StartedAt.After(now) || !j.Deadline.Equal(j.StartedAt.Add(30*time.Minute)) || j.Lease.ResourceID != "Lease/"+p.Configuration.Values["CLOUD_STACK_NAME"]+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(j.Lease.UID) || j.Lease.Holder == "" {
		return false
	}
	if j.CompletionReview == "" && !j.CompletionStartedAt.IsZero() || j.CompletionReview != "" && (!environmentPreflightDigest.MatchString(j.CompletionReview) || j.CompletionStartedAt.Before(j.StartedAt) || !j.CompletionStartedAt.Before(j.Deadline) || j.CompletionStartedAt.After(now)) {
		return false
	}
	stages := []string{"admitted", "rotating", "rotated", "imported", "api-verified", "consumers-verified", "completed"}
	index := -1
	for i, s := range stages {
		if s == j.Stage {
			index = i
		}
	}
	if index < 0 {
		return false
	}
	for i, t := range []time.Time{j.RotationStartedAt, j.RotatedAt, j.ImportedAt, j.CompletedAt} {
		required := i == 0 && index >= 1 || i == 1 && index >= 2 || i == 2 && index >= 3 || i == 3 && index == 6
		if required != (!t.IsZero()) || !t.IsZero() && (t.Before(j.StartedAt) || t.After(j.Deadline) || t.After(now)) {
			return false
		}
	}
	if index >= 2 {
		canonical, err := validateDeviceRootCRLSuccessor(p, []byte(j.Signed.PEM), j.RotationStartedAt, j.RotatedAt)
		if err != nil || deploymentplan.Digest(canonical) != deploymentplan.Digest(j.Signed) {
			return false
		}
	} else if j.Signed.IssuerID != "" {
		return false
	}
	if index >= 3 && j.ImportedAt.Before(j.RotatedAt) || index >= 2 && j.RotatedAt.Before(j.RotationStartedAt) || index == 6 && (j.CompletedAt.Before(j.ImportedAt) || !j.LeaseReleased) {
		return false
	}
	return deviceRootCRLEvidenceValid(p, j, index)
}

func deviceRootCRLEvidenceValid(p createDeviceRootCRLPlan, j createDeviceRootCRLJournal, stage int) bool {
	count := 0
	if stage >= 4 {
		count = 1
	}
	if stage >= 5 {
		count = 2
	}
	expected := map[string]string{}
	if len(p.Consumers) != 2 {
		return false
	}
	for _, target := range p.Consumers[:count] {
		expected["consumer-owner-"+target.ID] = deploymentplan.Digest(target.Desired)
		expected["consumer-Pod-"+target.ID] = deploymentplan.Digest(target.PodUID)
		expected["consumer-volume-"+target.ClaimID] = deploymentplan.Digest(p.Volumes[target.ClaimID])
		expected["cache-"+target.ID] = ""
		expected["native-ACKs-"+p.Root.ID] = ""
	}
	if len(expected) != len(j.Evidence) {
		return false
	}
	for k, v := range expected {
		actual := j.Evidence[k]
		if !environmentPreflightDigest.MatchString(actual) || v != "" && v != actual {
			return false
		}
	}
	return true
}

func readDeviceRootCRLArtifact(store secretStore, e *environmentCreateExecution, review string, recoveries ...*createServiceSessionRecovery) (createDeviceRootCRLArtifact, error) {
	var a createDeviceRootCRLArtifact
	var source environmentCreateJournal
	var owner environmentCreateOwner
	base := deviceRootCRLBase(review)
	if len(recoveries) > 1 || !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || !deviceRootCRLQualificationValid(a) || deploymentplan.Digest(a.Plan) != review || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-journal.json"), &source) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || deploymentplan.Digest(source) != a.Plan.SourceSHA || deploymentplan.Digest(owner) != a.Plan.OwnerSHA || deploymentplan.Digest(e.owner) != a.Plan.OwnerSHA || len(source.Entries) != 374 || len(e.Journal.Entries) < 374 || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) {
		return a, deviceRootCRLError("immutable-qualified-source")
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:374]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != a.Plan.SourceSHA {
		return a, deviceRootCRLError("unchanged-original-source-prefix")
	}
	if len(recoveries) == 0 {
		if len(e.Journal.Entries) != 374 {
			return a, deviceRootCRLError("standalone-current-source374")
		}
		if _, _, err := loadDeviceRootCRLSource(a.Plan.Configuration, store, e, &a.Plan); err != nil {
			return a, err
		}
	} else {
		r := recoveries[0]
		if r == nil || r.ManagedLateExpiredRenewal == nil || r.ManagedLateSession == nil || r.ManagedLateExpiredRenewal.SourceSHA != a.Plan.SourceSHA || managedLateExpiredRenewalSourcePins(r.ManagedLateExpiredRenewal) != a.Plan.SourcePins {
			return a, deviceRootCRLError("authenticated-current-expired-renewal-source")
		}
		if _, _, _, err := managedLateCurrentChildren(store, e, r.ManagedLateSession); err != nil {
			return a, err
		}
	}
	return a, nil
}

func saveDeviceRootCRLJournal(store secretStore, a createDeviceRootCRLArtifact, j createDeviceRootCRLJournal) error {
	if !deviceRootCRLJournalValid(a, j, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, a, j) != nil {
		return deviceRootCRLError("standalone-journal-state")
	}
	return store.write(filepath.Join(deviceRootCRLBase(j.Review), "journal.json"), mustProviderRecoveryJSON(j), true)
}

func deviceRootCRLSnapshotMatches(p createDeviceRootCRLPlan, current createRootCRLNativeSnapshot, signed *createRootCRLRecord) bool {
	if signed != nil && deploymentplan.Digest(current.Prior) == deploymentplan.Digest(*signed) {
		current.Prior = p.Snapshot.Prior
	}
	return deploymentplan.Digest(current) == deploymentplan.Digest(p.Snapshot)
}

func deviceRootCRLProviderRecord(root environmentCreateRegisteredIssuer, raw []byte) (createRootCRLRecord, error) {
	// Reuse the strict parser before accepting any provider bytes as an outcome.
	var out createRootCRLRecord
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return out, deviceRootCRLError("provider-signed-CRL")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.Number == nil {
		return out, deviceRootCRLError("provider-signed-CRL")
	}
	return createRootCRLRecord{root.ID, environmentPreflightBytesSHA256(crl.Raw), crl.Number.String(), string(raw), crl.ThisUpdate, crl.NextUpdate}, nil
}

func runEnvironmentCreateDeviceRootCRLRecovery(args []string) error {
	return runEnvironmentCreateDeviceRootCRLRecoveryContext(context.Background(), args, os.Stdout, os.Stderr)
}

func runEnvironmentCreateDeviceRootCRLRecoveryContext(parentCtx context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-device-root-crl", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original parent")
	workspaceOpt := fs.String("workspace", "", "workspace")
	execute := fs.Bool("execute", false, "execute exact qualified Device CRL repair")
	qualify := fs.Bool("qualify", false, "qualify only (default)")
	review := fs.String("review-sha256", "", "qualified review")
	completeRotation := fs.String("complete-rotation-review-sha256", "", "original admitted rotating review; qualify import and ACK completion without rotation")
	confirm := fs.String("confirm", "", "original stack")
	timeout := fs.Duration("timeout", 30*time.Minute, "bounded repair")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute || *execute && *qualify || !*execute && *review != "" || *completeRotation != "" && !environmentPreflightDigest.MatchString(*completeRotation) {
		return deviceRootCRLError("arguments")
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-device-root-crl"}, args...)); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOpt != "" {
		workspace = *workspaceOpt
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parentCtx, *timeout)
	defer cancel()
	profile := make([]bool, 11)
	for i := range profile {
		profile[i] = true
	}
	e, original, _, _, err := loadInitializedCreateAudit(cfg, store, *parent, false, false, false, false, false, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 || deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 {
		return deviceRootCRLError("original-configuration-and-operator")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return deviceRootCRLError("original-selected-kubeconfig")
	}
	b, err := newEnvironmentCreateCRLBackend(string(kube), *env, cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	if *completeRotation != "" {
		a, err := readDeviceRootCRLArtifact(store, e, *completeRotation)
		if err != nil || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) {
			return deviceRootCRLError("completion-original-source-and-inputs")
		}
		if !*execute {
			return qualifyDeviceRootCRLCompletion(ctx, cfg, store, e, b, operator, a, checker, out)
		}
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] {
			return deviceRootCRLError("confirmation")
		}
		fresh, _, err := loadDeviceRootCRLCompletionArtifact(store, a, *review)
		if err != nil || fresh.Plan.CheckerSHA != checker || fresh.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || fresh.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) {
			return deviceRootCRLError("completion-current-checker-and-inputs")
		}
		return executeDeviceRootCRL(ctx, cfg, store, e, b, operator, a, out, &fresh)
	}
	if *execute {
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] {
			return deviceRootCRLError("confirmation")
		}
		a, err := readDeviceRootCRLArtifact(store, e, *review)
		if err != nil {
			return err
		}
		if a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) {
			return deviceRootCRLError("current-qualified-inputs")
		}
		return executeDeviceRootCRL(ctx, cfg, store, e, b, operator, a, out)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, deviceRootCRLPointer(*parent))); err == nil || !os.IsNotExist(err) {
		return deviceRootCRLError("existing-or-unresolved-completion")
	}
	r, source, err := loadDeviceRootCRLSource(cfg, store, e, nil)
	if err != nil {
		return err
	}
	p := createDeviceRootCRLPlan{Schema: createDeviceRootCRLSchema, Parent: *parent, SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), SourcePins: managedLateExpiredRenewalSourcePins(source), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), Configuration: cfg, KubeconfigSHA: environmentPreflightBytesSHA256(kube), Root: r.ManagedTransportRecovery.Device}
	p.Snapshot, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil {
		return err
	}
	if _, err := deviceRootCRLUnion(p.Root, p.Snapshot); err != nil {
		return err
	}
	proof, provider, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, p.Root, false)
	if err != nil {
		return err
	}
	providerRecord, err := deviceRootCRLProviderRecord(p.Root, provider)
	if err != nil || deploymentplan.Digest(providerRecord) != deploymentplan.Digest(p.Snapshot.Prior) {
		return deviceRootCRLError("exact-original-registry-provider-prior3")
	}
	p.ProviderReview, err = deviceRootCRLProviderAncestor(store, e, proof)
	if err != nil {
		return err
	}
	p.ProviderSHA = proof
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/material", p.Parent, "operator-binding.json"), &p.Operator) != nil {
		return deviceRootCRLError("original-human-binding")
	}
	client, err := appRootCRLHumanClient(ctx, cfg, store, e, b, operator, p.Operator)
	if err != nil {
		return err
	}
	nativeRoot, err := client.ReadIssuer(ctx, p.Root.ID)
	if err != nil || deploymentplan.Digest(nativeRoot) != deploymentplan.Digest(p.Root.Document) {
		return deviceRootCRLError("authenticated-original-registry-issuer")
	}
	p.NativeEvidence, err = observeDeviceRootCRLNative(ctx, cfg, store, e, b, p, nil)
	if err != nil {
		return err
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	p.Controller, p.ConfigMap, p.Consumers, err = deviceRootCRLTargets(cfg, r, objects)
	if err != nil {
		return err
	}
	_, p.Volumes, err = readManagedLateAppRootCRLVolumes(e, p.Consumers, p.NativeEvidence)
	if err != nil {
		return err
	}
	for i, t := range p.Consumers {
		if err := verifyManagedLateAppRootCRLPodImages(ctx, t, objects, func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		}); err != nil {
			return err
		}
		raw, err := readDeviceRootCRLCache(ctx, cfg, b, p, t, p.Snapshot.Prior)
		if err != nil {
			return err
		}
		p.Consumers[i].StateSHA[t.Paths[0]] = environmentPreflightBytesSHA256(raw)
	}
	reviewSHA := deploymentplan.Digest(p)
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, reviewSHA)); err != nil {
		return err
	}
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || !deviceRootCRLSnapshotMatches(p, fresh, nil) {
		return deviceRootCRLError("qualification-revocations-changed")
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	q := deploymentplan.Qualification{PlanSHA256: reviewSHA, Decision: "GO"}
	for _, id := range createDeviceRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: reviewSHA})
	}
	a := createDeviceRootCRLArtifact{createDeviceRootCRLSchema + "/artifact", p, q, now, now.Add(15 * time.Minute)}
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": e.Journal, "source-owner.json": e.owner} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(deviceRootCRLBase(reviewSHA), name), value); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-device-root-crl review-sha256=%s prior-number=3 next-number=monotonic-successor consumers=2\n", reviewSHA)
	return nil
}

func executeDeviceRootCRL(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, a createDeviceRootCRLArtifact, out io.Writer, completions ...*createDeviceRootCRLCompletionArtifact) (resultErr error) {
	p := a.Plan
	review := deploymentplan.Digest(p)
	path := filepath.Join(deviceRootCRLBase(review), "journal.json")
	var j createDeviceRootCRLJournal
	_, statErr := os.Lstat(filepath.Join(store.Root, path))
	resuming := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	var completion *createDeviceRootCRLCompletionArtifact
	var completionSource createDeviceRootCRLJournal
	if len(completions) > 1 || len(completions) == 1 && completions[0] == nil {
		return deviceRootCRLError("one-explicit-completion-authority")
	}
	if len(completions) == 1 {
		completion = completions[0]
		loaded, source, err := loadDeviceRootCRLCompletionArtifact(store, a, deploymentplan.Digest(completion.Plan))
		if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(completion) || !resuming {
			return deviceRootCRLError("exact-admitted-rotation-completion")
		}
		completionSource = source
	}
	if resuming {
		if createOpenBaoACKCompletionRead(store, path, &j) != nil || !deviceRootCRLJournalValid(a, j, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, a, j) != nil {
			return deviceRootCRLError("existing-standalone-journal")
		}
		if completion == nil && j.CompletionReview != "" {
			return deviceRootCRLError("explicit-completion-authority-required")
		}
		if completion != nil {
			if j.CompletionReview == "" {
				if deploymentplan.Digest(j) != deploymentplan.Digest(completionSource) || !time.Now().UTC().Before(completion.ValidUntil) {
					return deviceRootCRLError("fresh-unchanged-completion-admission")
				}
			} else if j.CompletionReview != deploymentplan.Digest(completion.Plan) || !deviceRootCRLCompletionMembership(*completion, completionSource, j) {
				return deviceRootCRLError("same-recorded-completion-authority")
			}
		}
		if j.Stage == "completed" {
			return publishDeviceRootCRLCompletion(ctx, cfg, store, e, b, a, j)
		}
		if !time.Now().Before(j.Deadline) {
			return deviceRootCRLError("original-repair-deadline")
		}
	} else if !time.Now().Before(a.ValidUntil) || a.QualifiedAt.After(time.Now()) {
		return deviceRootCRLError("fresh-sixGO")
	}
	var signed *createRootCRLRecord
	if j.Signed.IssuerID != "" {
		signed = &j.Signed
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || !deviceRootCRLSnapshotMatches(p, current, signed) {
		return deviceRootCRLError("reviewed-registry-and-revocation-union")
	}
	observedSigned := signed
	if signed != nil && deploymentplan.Digest(current.Prior) != deploymentplan.Digest(*signed) {
		observedSigned = nil
	}
	if _, err := observeDeviceRootCRLNative(ctx, cfg, store, e, b, p, observedSigned); err != nil {
		return err
	}
	client, err := appRootCRLHumanClient(ctx, cfg, store, e, b, operator, p.Operator)
	if err != nil {
		return err
	}
	if !resuming && (!time.Now().Before(a.ValidUntil) || e.Check(ctx) != nil) {
		return deviceRootCRLError("immediate-sixGO-before-repair-admission")
	}
	leaseReview := review
	if completion != nil {
		leaseReview = deploymentplan.Digest(completion.Plan)
		fresh, captured, err := loadDeviceRootCRLCompletionArtifact(store, a, leaseReview)
		var actual createDeviceRootCRLJournal
		if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(completion) || deploymentplan.Digest(captured) != deploymentplan.Digest(completionSource) || createOpenBaoACKCompletionRead(store, path, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(j) {
			return deviceRootCRLError("immediate-unchanged-completion-evidence")
		}
		if j.CompletionReview == "" && !time.Now().UTC().Before(completion.ValidUntil) || !time.Now().UTC().Before(j.Deadline) {
			return deviceRootCRLError("immediate-fresh-completion-before-admission")
		}
		if j.CompletionReview == "" {
			j.CompletionReview, j.CompletionStartedAt = leaseReview, time.Now().UTC()
		}
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	// A command may reuse only immutable pre-mutation derivations. The native
	// lease is this repair's first write; later source loads must derive afresh.
	invalidateManagedLateSourceReuse(e)
	lease, err := b.AcquireExecution(ctx, rootCRLLeasePlan(cfg, leaseReview))
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if released {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		releaseErr := lease.Release(cleanup)
		resultErr = errors.Join(resultErr, releaseErr)
		if releaseErr == nil {
			j.LeaseReleased = true
			if j.Stage != "completed" {
				resultErr = errors.Join(resultErr, saveDeviceRootCRLJournal(store, a, j))
			}
		}
	}()
	if !resuming {
		now := time.Now().UTC()
		j = createDeviceRootCRLJournal{Schema: createDeviceRootCRLSchema + "/journal", Parent: p.Parent, Review: review, SourceSHA: p.SourceSHA, OwnerSHA: p.OwnerSHA, Stage: "admitted", StartedAt: now, Deadline: now.Add(30 * time.Minute), Lease: lease.Owner(), Evidence: map[string]string{}}
	} else {
		if !j.LeaseReleased && j.Lease.UID != lease.Owner().UID {
			return deviceRootCRLError("unknown-prior-native-lease")
		}
		j.Lease = lease.Owner()
		j.LeaseReleased = false
	}
	if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, j.Deadline)
	defer cancel()
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}
	if completion != nil && j.Stage == "rotating" {
		if err := guard(); err != nil {
			return err
		}
		proof, raw, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, p.Root, false)
		record, recordErr := deviceRootCRLProviderRecord(p.Root, raw)
		if err != nil || recordErr != nil || proof != p.ProviderSHA || validateDeviceRootCRLRotationReadback(p, completionSource, record, time.Now().UTC()) != nil || deploymentplan.Digest(record) != deploymentplan.Digest(completion.Plan.Signed) {
			return deviceRootCRLError("completion-exact-once-rotated-provider-outcome")
		}
		j.Signed, j.Stage, j.RotatedAt = record, "rotated", time.Now().UTC()
		if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS deployment.device-root-crl.qualified-rotation-readback crl-number=%s\n", record.Number)
	}
	if j.Stage == "admitted" || j.Stage == "rotating" {
		if completion != nil {
			return deviceRootCRLError("completion-never-rotates")
		}
		wasRotating := j.Stage == "rotating"
		if err := guard(); err != nil {
			return err
		}
		proof, raw, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, p.Root, false)
		if err != nil || proof != p.ProviderSHA {
			return deviceRootCRLError("original-provider-key-before-rotation")
		}
		record, err := deviceRootCRLProviderRecord(p.Root, raw)
		if err != nil {
			return err
		}
		if record.Number == "3" {
			if wasRotating || deploymentplan.Digest(record) != deploymentplan.Digest(p.Snapshot.Prior) {
				return deviceRootCRLError("uncertain-rotation-never-repeat")
			}
			j.Stage = "rotating"
			j.RotationStartedAt = time.Now().UTC()
			if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
				return err
			}
			// The provider call can fail after success. Always reconcile its public
			// current CRL; no failure path repeats rotation.
			_, _, _ = readDeviceRootCRLProvider(ctx, cfg, store, e, b, p.Root, true)
			proof, raw, err = readDeviceRootCRLProvider(ctx, cfg, store, e, b, p.Root, false)
			if err != nil || proof != p.ProviderSHA {
				return deviceRootCRLError("rotation-outcome-unresolved")
			}
		} else if !wasRotating {
			return deviceRootCRLError("unadmitted-provider-outcome")
		}
		j.Signed, err = validateDeviceRootCRLSuccessor(p, raw, j.RotationStartedAt, time.Now().UTC())
		if err != nil {
			return err
		}
		j.Stage = "rotated"
		j.RotatedAt = time.Now().UTC()
		if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS deployment.device-root-crl.same-key-rotation elapsed=%.3fs\n", time.Since(j.RotationStartedAt).Seconds())
	}
	if j.Stage == "rotated" {
		started := time.Now()
		if err := guard(); err != nil {
			return err
		}
		current, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
		if err != nil || !deviceRootCRLSnapshotMatches(p, current, &j.Signed) {
			return deviceRootCRLError("pre-import-current-union")
		}
		if deploymentplan.Digest(current.Prior) != deploymentplan.Digest(j.Signed) {
			if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
				return err
			}
			var imported createRootCRLRecord
			importErr := client.call(ctx, "POST", "/v1/platform/pki/issuers/"+p.Root.ID+"/crl", "", map[string]string{"crl_pem": j.Signed.PEM}, &imported)
			// Read actual registry outcome even after a lost HTTP reply.
			current, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
			if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(j.Signed) || !deviceRootCRLSnapshotMatches(p, current, &j.Signed) {
				return errors.Join(deviceRootCRLError("human-import-outcome-unresolved"), importErr)
			}
		}
		j.Stage = "imported"
		j.ImportedAt = time.Now().UTC()
		if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS deployment.device-root-crl.human-import elapsed=%.3fs\n", time.Since(started).Seconds())
	}
	for i, target := range p.Consumers {
		if i == 0 && (j.Stage == "api-verified" || j.Stage == "consumers-verified") || i == 1 && j.Stage == "consumers-verified" {
			continue
		}
		started := time.Now()
		for {
			if err := guard(); err != nil {
				return err
			}
			evidence, err := observeDeviceRootCRLConsumer(ctx, cfg, store, e, b, p, target, j.Signed)
			if err == nil {
				for k, v := range evidence {
					if old := j.Evidence[k]; old != "" && old != v {
						return deviceRootCRLError("conflicting-native-evidence")
					}
					j.Evidence[k] = v
				}
				break
			}
			select {
			case <-ctx.Done():
				return deviceRootCRLError("current-consumer-ACK-deadline")
			case <-time.After(5 * time.Second):
			}
		}
		j.Stage = []string{"api-verified", "consumers-verified"}[i]
		if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS deployment.device-root-crl.consumer-%d elapsed=%.3fs\n", i+1, time.Since(started).Seconds())
	}
	if _, err := observeDeviceRootCRLNative(ctx, cfg, store, e, b, p, &j.Signed); err != nil {
		return err
	}
	if err := observeDeviceRootCRLCompletedConsumers(ctx, cfg, store, e, b, p, j); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	invalidateManagedLateSourceReuse(e)
	if err := lease.Release(ctx); err != nil {
		return err
	}
	released = true
	j.LeaseReleased = true
	j.Stage = "completed"
	j.CompletedAt = time.Now().UTC()
	if err := saveDeviceRootCRLJournal(store, a, j); err != nil {
		return err
	}
	if err := publishDeviceRootCRLCompletion(ctx, cfg, store, e, b, a, j); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-device-root-crl review-sha256=%s crl-number=%s consumers=2 elapsed=%.3fs\n", review, j.Signed.Number, time.Since(j.StartedAt).Seconds())
	return nil
}

func observeDeviceRootCRLCompletedConsumers(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p createDeviceRootCRLPlan, j createDeviceRootCRLJournal) error {
	for _, t := range p.Consumers {
		evidence, err := observeDeviceRootCRLConsumer(ctx, cfg, store, e, b, p, t, j.Signed)
		if err != nil {
			return err
		}
		for k, v := range evidence {
			if j.Evidence[k] != v {
				return deviceRootCRLError("current-completed-native-evidence")
			}
		}
	}
	return nil
}

func publishDeviceRootCRLCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createDeviceRootCRLArtifact, j createDeviceRootCRLJournal) error {
	if !deviceRootCRLJournalValid(a, j, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, a, j) != nil || j.Stage != "completed" {
		return deviceRootCRLError("genuine-completed-native-journal")
	}
	if err := beginDeviceRootCRLSourcePhase(store, e); err != nil {
		return err
	}
	if _, _, err := loadDeviceRootCRLSource(cfg, store, e, &a.Plan); err != nil {
		return err
	}
	if err := observeDeviceRootCRLCompletedConsumers(ctx, cfg, store, e, b, a.Plan, j); err != nil {
		return err
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	invalidateManagedLateSourceReuse(e)
	for _, path := range []string{filepath.Join(deviceRootCRLBase(j.Review), "completed.json"), deviceRootCRLPointer(j.Parent)} {
		if err := writeMQTTRootCRLImmutable(store, path, j); err != nil {
			return err
		}
	}
	return e.Check(ctx)
}

func verifyDeviceRootCRLCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createDeviceRootCRLArtifact, j createDeviceRootCRLJournal) error {
	var mirror, pointer createDeviceRootCRLJournal
	if !deviceRootCRLJournalValid(a, j, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, a, j) != nil || j.Stage != "completed" || createOpenBaoACKCompletionRead(store, filepath.Join(deviceRootCRLBase(j.Review), "completed.json"), &mirror) != nil || createOpenBaoACKCompletionRead(store, deviceRootCRLPointer(j.Parent), &pointer) != nil || deploymentplan.Digest(j) != deploymentplan.Digest(mirror) || deploymentplan.Digest(j) != deploymentplan.Digest(pointer) {
		return deviceRootCRLError("authenticated-completion-pointer")
	}
	if _, err := observeDeviceRootCRLNative(ctx, cfg, store, e, b, a.Plan, &j.Signed); err != nil {
		return err
	}
	for _, t := range a.Plan.Consumers {
		evidence, err := observeDeviceRootCRLConsumer(ctx, cfg, store, e, b, a.Plan, t, j.Signed)
		if err != nil {
			return err
		}
		for k, v := range evidence {
			if j.Evidence[k] != v {
				return deviceRootCRLError("current-completed-native-evidence")
			}
		}
	}
	return e.Check(ctx)
}

func verifyManagedLateDeviceRootCRLFreshness(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, recoveries ...*createServiceSessionRecovery) (map[string]string, error) {
	var j createDeviceRootCRLJournal
	if createOpenBaoACKCompletionRead(store, deviceRootCRLPointer(e.Journal.PlanSHA256), &j) != nil {
		return nil, deviceRootCRLError("completed-current-successor-proof")
	}
	a, err := readDeviceRootCRLArtifact(store, e, j.Review, recoveries...)
	if err != nil {
		return nil, err
	}
	var mirror, runtime createDeviceRootCRLJournal
	if !deviceRootCRLJournalValid(a, j, time.Now().UTC()) || validateDeviceRootCRLCompletionJournal(store, a, j) != nil || j.Stage != "completed" || createOpenBaoACKCompletionRead(store, filepath.Join(deviceRootCRLBase(j.Review), "completed.json"), &mirror) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(deviceRootCRLBase(j.Review), "journal.json"), &runtime) != nil || deploymentplan.Digest(j) != deploymentplan.Digest(mirror) || deploymentplan.Digest(j) != deploymentplan.Digest(runtime) {
		return nil, deviceRootCRLError("durable-completed-proof")
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(j.Signed) || !deviceRootCRLSnapshotMatches(a.Plan, current, &j.Signed) {
		return nil, deviceRootCRLError("current-Device-successor-registry-union")
	}
	provider, raw, err := readDeviceRootCRLProvider(ctx, cfg, store, e, b, a.Plan.Root, false)
	if err != nil || provider != a.Plan.ProviderSHA {
		return nil, deviceRootCRLError("current-original-provider")
	}
	if record, err := deviceRootCRLProviderRecord(a.Plan.Root, raw); err != nil || deploymentplan.Digest(record) != deploymentplan.Digest(j.Signed) {
		return nil, deviceRootCRLError("current-provider-successor")
	}
	currentPlan := a.Plan
	if len(recoveries) == 1 {
		r := recoveries[0]
		children, _, _, err := managedLateCurrentChildren(store, e, r.ManagedLateSession)
		if err != nil {
			return nil, err
		}
		owners, ready, err := managedLateCurrentOwners(r, r.ManagedLateSession, children)
		if err != nil {
			return nil, err
		}
		currentPlan.Consumers = append([]createRootCRLStateTarget(nil), a.Plan.Consumers...)
		for i, t := range a.Plan.Consumers {
			owner, ok := owners[t.ID]
			if !ok || owner.UID != t.UID || !ready[t.ID] {
				return nil, deviceRootCRLError("qualified-current-consumer-owner")
			}
			target, err := managedLateCurrentCRLTarget(ctx, cfg, b, owner, t)
			if err != nil {
				return nil, err
			}
			currentPlan.Consumers[i] = target
		}
		if owner, ok := owners[a.Plan.Controller.ID]; !ok || owner.UID != a.Plan.Controller.UID {
			return nil, deviceRootCRLError("qualified-current-controller-owner")
		} else {
			currentPlan.Controller = owner
		}
	}
	for _, target := range currentPlan.Consumers {
		if _, err := observeDeviceRootCRLConsumer(ctx, cfg, store, e, b, currentPlan, target, j.Signed); err != nil {
			return nil, err
		}
	}
	return map[string]string{"device-root-crl-completion": deploymentplan.Digest(j), "device-root-crl-current": j.Signed.Digest}, nil
}
