package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createRecoveryCheckpoint struct {
	Owner         environmentCreateOwner
	Journal       environmentCreateJournal
	Profile, Flag string
}

// These selectors describe existing qualified recovery profiles. Local journal
// inspection only selects one; the original engine still verifies all native
// ownership, signing outcomes, identity material and six required gates.
func createRecoveryCheckpointProfile(j environmentCreateJournal) (string, string) {
	if len(j.Entries) > 374 && managedLateExpiredRenewalReview(j) != "" {
		return "managed-late-expired-unused-renewal", "--after-application-enrollment-trust"
	}
	if managedLatePostMintReview(j) != "" {
		return "managed-late-post-mint-continuation", "--after-application-enrollment-trust"
	}
	if managedLateSeedForwardReview(j) != "" {
		return "managed-late-seed-trust-forward", "--after-application-enrollment-trust"
	}
	if managedLateSessionACKCheckpointReview(j) != "" {
		return "managed-original-key-late-ack", "--after-application-enrollment-trust"
	}
	if managedCoreInterruptedForwardReview(j) != "" {
		return "managed-complete-issuer-interrupted-certissuer-forward", "--after-application-enrollment-trust"
	}
	if managedCorePendingAdmissionReview(j) != "" {
		return "managed-complete-issuer-pending-admission-forward", "--after-application-enrollment-trust"
	}
	if managedCorePartialReview(j) != "" {
		return "managed-complete-issuer-partial-forward", "--after-application-enrollment-trust"
	}
	if managedApplicationInterruptedReview(j) != "" {
		return "managed-application-interrupted-forward", "--after-application-enrollment-trust"
	}
	if managedApplicationPartialReview(j) != "" {
		return "managed-application-partial-rollout", "--after-application-enrollment-trust"
	}
	if managedTransportExportRetirementReview(j) != "" {
		return "managed-transports-api-export-retirement", "--after-application-enrollment-trust"
	}
	if managedTransportPostManagedReview(j) != "" {
		return "managed-transports-complete-api-export", "--after-application-enrollment-trust"
	}
	if managedTransportAcceptedForwardReview(j) != "" {
		return "managed-transports-accepted-openbao-audit", "--after-application-enrollment-trust"
	}
	if managedTransportInterruptedForwardReview(j) != "" {
		before := j
		before.Entries = j.Entries[:len(j.Entries)-2]
		if managedTransportInterruptedForwardReviewDepth(before, 1) != "" {
			return "managed-transports-written-openbao", "--after-application-enrollment-trust"
		}
		return "managed-transports-interrupted-forward", "--after-application-enrollment-trust"
	}
	if isManagedTransportPartialCheckpoint(j) {
		return "managed-transports-partial-rollout", "--after-application-enrollment-trust"
	}
	if isManagedTransportCheckpoint(j) {
		return "all-ca-active-managed-transport", "--after-application-enrollment-trust"
	}
	if isAppTrustCheckpoint(j) {
		return "app-ready-trust-broker-enforcement", "--after-application-enrollment-trust"
	}
	if isDeviceTrustCheckpoint(j) {
		return "device-ready-trust-service-policy", "--after-application-enrollment-trust"
	}
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, entry := range j.Entries {
		if seen[entry.Key] {
			return "", ""
		}
		seen[entry.Key] = true
		if entry.State == "completed" && !entry.CompletedAt.IsZero() {
			continue
		}
		if entry.State != "pending" || len(entry.Receipt) != 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return "", ""
		}
		pending[entry.Key] = true
	}
	if len(pending) == 1 && pending["kubernetes/service-session"] {
		return "service-session-order", "--at-service-session"
	}
	if len(pending) == 1 && pending["pki-reserve-openbao_tls-intermediate"] && len(j.Entries) > 0 && j.Entries[len(j.Entries)-1].Key == "pki-reserve-openbao_tls-intermediate" {
		return "openbao-intermediate-dns-policy", "--after-application-enrollment-trust"
	}
	if len(pending) == 2 && pending["device-root-bootstrap"] && pending["kubernetes/device-root-ready"] && j.Entries[len(j.Entries)-1].Key == "kubernetes/device-root-ready" {
		return "device-bootstrap-service-account-order", "--after-application-enrollment-trust"
	}
	if len(pending) == 1 && pending["kubernetes/service-managed-core"] {
		return "managed-core-order", "--after-managed-core-order"
	}
	if len(pending) == 1 && pending["kubernetes/application-client-enrollment"] {
		return "application-enrollment-trust", "--after-application-enrollment-trust"
	}
	if len(pending) >= 3 && len(pending) <= 10 && pending["kubernetes/openbao-root-trust"] && len(j.Entries) > 0 && currentQuiesceReview(j.Entries[len(j.Entries)-1].Key) != "" {
		app, pauses := 0, 0
		for key := range pending {
			if key == "kubernetes/openbao-root-trust" {
				continue
			}
			if currentInstallationReview(key) != "" {
				app++
			} else if currentQuiesceReview(key) != "" {
				pauses++
			} else {
				return "", ""
			}
		}
		if app == 1 && pauses == len(pending)-2 {
			return "current-owner-pause-acceptance", "--after-application-enrollment-trust"
		}
	}
	if len(pending) == 2 && pending["kubernetes/openbao-root-trust"] && len(j.Entries) > 0 && currentInstallationReview(j.Entries[len(j.Entries)-1].Key) != "" {
		return "current-application-identity-lock", "--after-application-enrollment-trust"
	}
	if len(pending) == 1 && pending["kubernetes/openbao-root-trust"] {
		return "openbao-trust-resource-name", "--after-application-enrollment-trust"
	}
	if len(pending) == 1 && pending["kubernetes/mqtt-root-trust"] {
		return "mqtt-root-trust-order", "--after-application-enrollment-trust"
	}
	if len(pending) == 2 && pending["kubernetes/mqtt-root-trust"] && len(j.Entries) > 0 && mqttTrustForwardReview(j.Entries[len(j.Entries)-1].Key) != "" {
		return "mqtt-trust-runtime-dependencies", "--after-application-enrollment-trust"
	}
	if len(pending) == 2 && pending["kubernetes/application-client-enrollment"] && len(j.Entries) > 0 {
		key := j.Entries[len(j.Entries)-1].Key
		if pending[key] && applicationIdentityLockReview(key) != "" {
			return "application-core-identity-lock", "--after-application-enrollment-trust"
		}
	}
	if len(pending) == 0 && len(j.Entries) > 0 && j.Entries[len(j.Entries)-1].Key == "kubernetes/service-core-enrollment" {
		return "accepted-service-core", "--after-service-core-enrollment"
	}
	if len(pending) == 1 && pending["kubernetes/service-issuer-server-enrollment"] {
		return "expired-bootstrap-enrollment", "--at-certissuer-server-enrollment"
	}
	if len(pending) == 2 && pending["kubernetes/service-issuer-server-enrollment"] {
		for key := range pending {
			prefix := "kubernetes/service-session-original-key-renewal-"
			if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(strings.TrimPrefix(key, prefix)) {
				return "bootstrap-renewal-permissions", "--after-bootstrap-renewal-permissions"
			}
		}
	}
	return "", ""
}

func inspectCreateRecoveryCheckpoint(cfg deploymentConfig, store secretStore) (createRecoveryCheckpoint, error) {
	var checkpoint createRecoveryCheckpoint
	bad := func() (createRecoveryCheckpoint, error) {
		return checkpoint, errors.New("original create owner and private journal cannot be safely inspected")
	}
	if cfg.Environment != store.Environment || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return bad()
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	if validateEnvironmentPreflightDirectories(store, fence) != nil || readExecutionJSON(filepath.Join(fence, "owner.json"), &checkpoint.Owner) != nil {
		return bad()
	}
	o := checkpoint.Owner
	if o.Schema != environmentCreateOwnerSchema || o.Environment != cfg.Environment || o.Stack != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(o.PlanSHA256) || !environmentPreflightDigest.MatchString(o.OwnerToken) || filepath.Clean(o.Journal) != o.Journal || !strings.HasPrefix(o.Journal, "deployment/create/executions/"+o.PlanSHA256+"/execution-") || filepath.Base(o.Journal) != "journal.json" {
		return bad()
	}
	path, err := store.safePath(o.Journal)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readExecutionJSON(path, &checkpoint.Journal) != nil {
		return bad()
	}
	j := checkpoint.Journal
	if j.Schema != environmentCreateJournalSchema || j.Environment != o.Environment || j.Stack != o.Stack || j.PlanSHA256 != o.PlanSHA256 || j.OwnerToken != o.OwnerToken || !j.CompletedAt.IsZero() || len(j.Entries) == 0 {
		return bad()
	}
	checkpoint.Profile, checkpoint.Flag = createRecoveryCheckpointProfile(j)
	if checkpoint.Profile == "managed-late-post-mint-continuation" {
		selected, selectErr := managedLateExpiredRenewalSelected(store, &environmentCreateExecution{Store: store, Journal: j, owner: o})
		if selectErr != nil {
			return bad()
		}
		if selected {
			checkpoint.Profile = "managed-late-expired-unused-renewal"
		}
	}
	return checkpoint, nil
}

func createRecoveryPreflightCommand(cfg deploymentConfig, c createRecoveryCheckpoint) string {
	return "./scripts/check-deployment-preflight.sh --environment " + cfg.Environment + " --operation create"
}

// A fence is never evidence of an empty target. Only explicit create preflight
// performs this dispatch; ordinary execution cannot silently become recovery.
func createPreflightHasCheckpoint(cfg deploymentConfig) (bool, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(filepath.Join(store.Root, "deployment/.execution-active"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, nil // inaccessible or malformed owners also stay fenced
}

func resolveRecordedCreateEmailProof(store secretStore, c createRecoveryCheckpoint, explicit string) (string, error) {
	bad := func() (string, error) {
		return "", errors.New("original journal-bound email repair proof is missing, ambiguous or unsafe; retain the original generation and restore its exact evidence")
	}
	expected := ""
	for _, e := range c.Journal.Entries {
		if e.Key == "kubernetes/account-bootstrap" && e.State == "completed" && !e.CompletedAt.IsZero() {
			var receipt struct {
				SHA string `json:"email_key_repair_proof_sha256"`
			}
			if json.Unmarshal(e.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.SHA) {
				return bad()
			}
			expected = receipt.SHA
		}
	}
	if expected == "" {
		return bad()
	}
	check := func(path string) bool {
		if filepath.Clean(path) != path || !strings.HasPrefix(path, store.Root+"/deployment/repairs/account-email-format-") || filepath.Base(path) != "verified.json" {
			return false
		}
		checked, err := store.safePath(strings.TrimPrefix(path, store.Root+"/"))
		if err != nil || checked != path || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return false
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || environmentPreflightBytesSHA256(raw) != expected {
			return false
		}
		var proof struct {
			Schema      string `json:"schema"`
			Environment string `json:"environment"`
			Parent      string `json:"parent_plan_sha256"`
			Completed   bool   `json:"completed"`
			OnlyEmail   bool   `json:"only_email_secret_field_changed"`
		}
		return json.Unmarshal(raw, &proof) == nil && proof.Schema == "rtk-cloud.initial-email-key-format-repair/v1" && proof.Environment == store.Environment && proof.Parent == c.Owner.PlanSHA256 && proof.Completed && proof.OnlyEmail
	}
	if explicit != "" {
		if check(explicit) {
			return explicit, nil
		}
		return bad()
	}
	base := filepath.Join(store.Root, "deployment/repairs")
	if validateEnvironmentPreflightDirectories(store, base) != nil {
		return bad()
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) > 512 {
		return bad()
	}
	selected := ""
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "account-email-format-") {
			continue
		}
		path := filepath.Join(base, entry.Name(), "verified.json")
		if !check(path) {
			continue
		}
		if selected != "" {
			return bad()
		}
		selected = path
	}
	if selected == "" {
		return bad()
	}
	return selected, nil
}

func createCheckpointPreflightHint(cfg deploymentConfig, store secretStore) error {
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return &deploymentplan.Blocker{ID: "deployment.create.checkpoint", Cause: "an execution fence exists but its original create owner or private journal cannot be safely inspected", Next: "inspect the selected canonical owner and journal; preserve the fence and original identities; never substitute another worktree or environment"}
	}
	if cfg.Environment != "dev" || checkpoint.Flag == "" {
		return &deploymentplan.Blocker{ID: "deployment.create.checkpoint", Cause: "the original generation has an unfinished or uncertain checkpoint without a supported recovery preflight profile", Next: "inspect its original private journal and qualify a supported recovery for that exact checkpoint; do not delete the fence or restart fresh creation"}
	}
	return &deploymentplan.Blocker{ID: "deployment.create.checkpoint", Cause: "unfinished original create generation (parent=" + checkpoint.Owner.PlanSHA256 + ", checkpoint=" + checkpoint.Profile + "); fresh creation cannot replace its owner", Next: "run " + createRecoveryPreflightCommand(cfg, checkpoint) + "; this only qualifies recovery, preserves the fence and original OpenBao/PKI, and does not deploy"}
}

type createRecoveryPreflightRunner func(context.Context, []string, io.Writer, io.Writer) error

func runEnvironmentCreateRecoveryPreflight(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) error {
	return runCreateRecoveryPreflightWithRunner(ctx, cfg, o, report, out, diagnostics, func(ctx context.Context, args []string, out, diagnostics io.Writer) error {
		return runEnvironmentCreateOpenBaoRecoveryContext(ctx, args, out, diagnostics, false)
	})
}

func runCreateRecoveryPreflightWithRunner(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer, run createRecoveryPreflightRunner) error {
	report.Scope, report.Operation, report.Completed = "original-generation-recovery", "create", false
	checkedReportPath := ""
	// Preserve a validated requested report even when ownership inspection fails
	// before the native recovery checker can produce qualification evidence.
	if o.Report != "" && validateEnvironmentDeploymentReportPath(cfg.Environment, o.Report) == nil {
		checkedReportPath = o.Report
	}
	blocked := func(cause, next string) error {
		report.Decision = "NO-GO"
		report.Gates = []deploymentplan.GateResult{{ID: "deployment.create.recovery.preflight", Required: true, Status: "BLOCKED", Cause: cause, Next: next, Impact: "original generation remains fenced; no deployment or identity replacement occurred", Recheck: "repeat complete recovery preflight for the selected original generation"}}
		return emitUnifiedDeploymentReport(out, diagnostics, checkedReportPath, *report, exitCode(1))
	}
	if o.Action != "preflight" || o.Operation != "create" || o.Confirm != "" || o.PlanSHA256 != "" || cfg.Environment != "dev" || run == nil {
		return blocked("recovery preflight requires the selected dev checkpoint and forbids execution", "use only the preflight script with --operation create; do not pass deployment confirmation or execution flags")
	}
	if ctx.Err() != nil {
		return blocked("recovery preflight was cancelled before qualification", "repeat full preflight when ready; original execution remains fenced")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return blocked("selected canonical environment store unavailable", "restore access to the selected original store; never search another environment or worktree")
	}
	c, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return blocked("original create owner and private journal cannot be safely inspected", "retain the fence and inspect original canonical ownership")
	}
	if c.Flag == "" {
		return blocked("unfinished or uncertain checkpoint has no supported recovery preflight profile", "inspect the original private journal; unknown or partial native outcomes require separately qualified recovery")
	}
	if o.ParentPlanSHA256 != "" && o.ParentPlanSHA256 != c.Owner.PlanSHA256 {
		return blocked("requested parent differs from the original selected create owner", createRecoveryPreflightCommand(cfg, c))
	}
	o.EmailKeyRepairProof, err = resolveRecordedCreateEmailProof(store, c, o.EmailKeyRepairProof)
	if err != nil {
		return blocked("original journal-bound repair proof cannot be resolved safely", "restore the original recorded proof in the selected canonical store; do not generate or borrow another generation's evidence")
	}
	reportPath := o.Report
	if reportPath == "" {
		reportPath, err = privateDeploymentStatePath(cfg.Environment, "deployment/reports/create-recovery-preflight.json")
	}
	if err != nil || validateEnvironmentDeploymentReportPath(cfg.Environment, reportPath) != nil {
		return blocked("recovery qualification report must use the selected canonical private report path", "use a path under the selected environment's deployment/reports directory")
	}
	checkedReportPath = reportPath
	// A failed new attempt cannot leave an old GO at the current report path.
	// Historical qualification artifacts and original execution records remain.
	if ensurePrivateDirectory(filepath.Dir(reportPath)) != nil || store.write(strings.TrimPrefix(reportPath, store.Root+string(filepath.Separator)), mustProviderRecoveryJSON(map[string]any{"schema": "rtk-cloud.create-recovery-preflight-pending/v1", "decision": "NO-GO", "completed": false, "environment": cfg.Environment}), true) != nil {
		return blocked("current recovery report cannot be invalidated safely", "restore access to the selected private reports directory; preserve original execution evidence")
	}
	args := []string{"--environment", cfg.Environment, "--workspace", cfg.Workspace, "--parent-plan-sha256", c.Owner.PlanSHA256, c.Flag, "--email-key-repair-proof", o.EmailKeyRepairProof, "--timeout", o.Timeout.String(), "--report", reportPath}
	if cfg.EnvironmentRoot != "" {
		args = append(args, "--environment-root", cfg.EnvironmentRoot)
	}
	fmt.Fprintf(out, "Create preflight mode: resume-original-generation parent-plan-sha256=%s checkpoint=%s\n", c.Owner.PlanSHA256, c.Profile)
	fmt.Fprintln(out, "Preserve the original execution fence and OpenBao/PKI identities; no deployment, renewal or cleanup is executed.")
	started := time.Now().UTC()
	err = run(ctx, args, out, diagnostics)
	var artifact createFoundationRecoveryArtifact
	checker, checkerErr := environmentPreflightCheckerSHA256()
	fresh := readExecutionJSON(reportPath, &artifact) == nil && artifact.Schema == "rtk-cloud.create-openbao-recovery-artifact/v1" && artifact.Plan.ParentSHA == c.Owner.PlanSHA256 && artifact.Plan.ParentOwnerSHA == deploymentplan.Digest(c.Owner) && artifact.Plan.ParentJournalSHA == deploymentplan.Digest(c.Journal) && checkerErr == nil && artifact.Plan.CheckerSHA == checker && artifact.Qualification.PlanSHA256 == deploymentplan.Digest(artifact.Plan) && !artifact.QualifiedAt.Before(started) && !artifact.QualifiedAt.After(time.Now().UTC()) && artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(15*time.Minute)) && time.Now().Before(artifact.ValidUntil) && len(artifact.Qualification.Gates) == 6
	if !fresh {
		var nativeBlocker *deploymentplan.Blocker
		if errors.As(err, &nativeBlocker) {
			return blocked(nativeBlocker.Cause, nativeBlocker.Next)
		}
		return blocked("complete current-checker recovery qualification evidence is unavailable; previous GO is not reused", "inspect the reported native blocker and retain original ownership; repeat full recovery preflight")
	}
	report.Gates = slices.Clone(artifact.Qualification.Gates)
	report.QualifiedAt, report.EvidenceValidUntil = &artifact.QualifiedAt, &artifact.ValidUntil
	report.QualificationSHA256 = artifact.Qualification.PlanSHA256
	report.Decision = "NO-GO"
	if err == nil && artifact.Qualification.Decision == "GO" {
		expected := []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"}
		for index, g := range report.Gates {
			if !g.Required || g.Status != "PASS" || g.ID != expected[index] || g.EvidenceSHA256 != artifact.Qualification.PlanSHA256 {
				return blocked("a required recovery gate lacks current complete PASS evidence", "repeat full recovery preflight; partial qualification is not GO")
			}
		}
		report.Decision = "GO"
	}
	if report.Decision != "GO" && err == nil {
		err = exitCode(1)
	}
	// The requested report is the original engine's full private qualification
	// artifact. Do not overwrite that resumable evidence with the public summary.
	return emitUnifiedDeploymentReport(out, diagnostics, "", *report, err)
}
