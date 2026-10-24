package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func loadInitializedCreateAudit(cfg deploymentConfig, store secretStore, sha string, afterMigration, afterRoles, afterSecrets, atAccount, afterAccount bool, acceptedAccount ...bool) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, *deploymentplan.Plan, deploymentplan.Journal, error) {
	var artifact environmentCreateProviderRecoveryArtifact
	var child deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func(cause string) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, *deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, artifact, nil, journal, createProviderRecoveryBlock(cause)
	}
	if cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || !environmentPreflightDigest.MatchString(sha) {
		return bad("only the selected original dev initialization can use audit recovery")
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	if err := validateEnvironmentPreflightDirectories(store, fence); err != nil {
		return bad("original private execution fence unavailable")
	}
	lock, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return bad("original executor is running")
	}
	e := &environmentCreateExecution{Store: store, running: lock}
	loaded := false
	defer func() {
		if !loaded {
			e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return bad("original fence identity unavailable")
	}
	e.runningInfo, err = lock.Stat()
	if err != nil {
		return bad("original process lock unavailable")
	}
	if readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &e.owner) != nil || e.owner.Schema != environmentCreateOwnerSchema || e.owner.Environment != cfg.Environment || e.owner.Stack != cfg.Values["CLOUD_STACK_NAME"] || e.owner.PlanSHA256 != sha || !strings.HasPrefix(e.owner.Journal, "deployment/create/executions/"+sha+"/execution-") {
		return bad("original stopped dev owner differs")
	}
	e.JournalPath, err = privateDeploymentStatePath(cfg.Environment, e.owner.Journal)
	if err != nil {
		return bad("original journal path unavailable")
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if readEnvironmentPreflightJSON(e.JournalPath, &e.Journal) != nil || e.Journal.PlanSHA256 != sha || e.Journal.OwnerToken != e.owner.OwnerToken || !e.Journal.CompletedAt.IsZero() || len(e.Journal.Entries) == 0 {
		return bad("original stopped initialized journal differs")
	}
	if cfg.Environment == "dev" && len(e.Journal.Entries) == 374 && managedLatePostMintReview(e.Journal) != "" {
		e.lateSourceReuse, err = newManagedLateSourceReuse(store, e)
		if err != nil {
			return bad("original late ancestry input manifest unavailable")
		}
	}
	// Only the explicit completion-only profile admits a partially published
	// MQTT lifecycle. Its typed source guard authenticates the exact original
	// four entries; ordinary app recovery retains its existing strict selector.
	mqttCompletionOnly := len(acceptedAccount) == 12 && acceptedAccount[11]
	if mqttCompletionOnly && !mqttRootCRLCompletionNativeCheckpoint(store, e) {
		return bad("MQTT completion recovery requires its exact original four-step native checkpoint")
	}
	appCompletionOnly := len(acceptedAccount) == 13 && acceptedAccount[12]
	if appCompletionOnly && !managedLateAppCompletionNativeCheckpoint(store, e) {
		return bad("App completion recovery requires its exact imported original CRL and bounded consumer checkpoint")
	}
	if len(acceptedAccount) == 11 && acceptedAccount[10] && managedLateCompletedTailSelected(e.Journal) {
		if _, sourceErr := loadManagedLateCompletedTailSource(context.Background(), cfg, store, e); sourceErr != nil {
			return nil, artifact, nil, journal, sourceErr
		}
	}
	terminalCompletion := len(acceptedAccount) == 11 && acceptedAccount[10] && managedLateTerminalSourceSelected(e.Journal)
	if terminalCompletion {
		if _, terminalErr := loadManagedLateTerminalFinalizerSource(context.Background(), cfg, store, e); terminalErr != nil {
			return nil, artifact, nil, journal, fmt.Errorf("terminal finalizer requires exact original pending-operation proof: %w", terminalErr)
		}
	}
	normalTailCompletion := len(acceptedAccount) == 11 && acceptedAccount[10] && managedLateNormalTailSelected(e.Journal)
	pendingAccount := 0
	pendingReservation := 0
	for index, entry := range e.Journal.Entries {
		// The strong completed-tail loader above authenticated these two exact
		// pending stages and the fully applied helper's original child journal.
		if normalTailCompletion && managedLateRedisPrePauseSelected(e.Journal) && (index == 434 || index == 436) {
			continue
		}
		if normalTailCompletion && index == 431 {
			continue
		}
		if terminalCompletion && index == 395 {
			continue
		}
		if appCompletionOnly {
			// The typed checkpoint pins the original370 prefix and its literal App tail.
			if index >= 370 {
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return bad("App completion recovery requires every original source entry to remain completed")
			}
			continue
		}
		if mqttCompletionOnly {
			if index == len(e.Journal.Entries)-4 {
				// The source guard pins this original top and its genuine children.
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return bad("MQTT completion recovery permits only its authenticated original top to remain unfinished")
			}
			continue
		}
		if len(acceptedAccount) == 11 && acceptedAccount[10] {
			if managedLateSeedForwardReview(e.Journal) != "" && index == len(e.Journal.Entries)-1 && managedLateSeedForwardNativeCheckpoint(store, e) {
				continue
			}
			if stoppedReview := managedCoreInterruptedForwardReview(e.Journal); stoppedReview != "" && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 {
				if index == len(e.Journal.Entries)-4 && entry.Key == "kubernetes/managed-complete-issuer" || index == len(e.Journal.Entries)-1 && entry.Key == "kubernetes/managed-complete-issuer-forward-"+stoppedReview {
					continue
				}
			}

			if pendingReview := managedCorePendingAdmissionReview(e.Journal); pendingReview != "" {
				if index == len(e.Journal.Entries)-2 && entry.Key == "kubernetes/managed-complete-issuer" && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 || index == len(e.Journal.Entries)-1 && entry.Key == "openbao-recovery-admission-"+pendingReview && entry.Phase == "recovery" && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 {
					continue
				}
			}
			if managedCorePartialReview(e.Journal) != "" && index == len(e.Journal.Entries)-1 && entry.Key == "kubernetes/managed-complete-issuer" && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 {
				continue
			}
			if entry.State == "pending" && managedApplicationPendingForwardAlias(e.Journal, entry.Key) {
				continue
			}
			if managedApplicationPartialReview(e.Journal) != "" && index == len(e.Journal.Entries)-1 && entry.Key == "kubernetes/managed-application-owners" {
				continue
			}
			if managedTransportExportRetirementReview(e.Journal) != "" && index == len(e.Journal.Entries)-1 && entry.Key == "retire-pki-export-video-cloud-api-pki-"+e.Journal.PlanSHA256[:12] && entry.State == "pending" && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 {
				continue
			}
			if isManagedTransportPartialCheckpoint(e.Journal) && entry.State == "pending" && (entry.Key == "kubernetes/transport-managed-hosts" || strings.HasPrefix(entry.Key, "kubernetes/transport-managed-hosts-forward-") && (managedTransportInterruptedForwardReview(e.Journal) != "" || managedTransportAcceptedForwardReview(e.Journal) != "")) {
				continue
			}
			if isAppTrustCheckpoint(e.Journal) && entry.Key == "kubernetes/app-root-trust" {
				continue
			}
			if isDeviceTrustCheckpoint(e.Journal) && (entry.Key == "device-root-bootstrap" || entry.Key == "kubernetes/device-root-trust" || deviceTrustForwardReview(entry.Key) != "") {
				continue
			}
			if e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/device-root-ready" && (entry.Key == "device-root-bootstrap" || entry.Key == "kubernetes/device-root-ready") && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if entry.Key == "pki-reserve-openbao_tls-intermediate" && index == len(e.Journal.Entries)-1 && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if (entry.Key == "kubernetes/openbao-root-trust" && (e.Journal.Entries[len(e.Journal.Entries)-1].Key == entry.Key || currentInstallationReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" || currentQuiesceReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "") || entry.Key == "kubernetes/mqtt-root-trust" || mqttTrustForwardReview(entry.Key) != "" && e.Journal.Entries[len(e.Journal.Entries)-1].Key == entry.Key) && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if (entry.Key == e.Journal.Entries[len(e.Journal.Entries)-1].Key && (applicationIdentityLockReview(entry.Key) != "" || currentInstallationReview(entry.Key) != "") || currentQuiesceReview(entry.Key) != "") && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if (entry.Key == "kubernetes/application-client-enrollment" || currentQuiesceReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" && currentInstallationReview(entry.Key) != "") && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return bad("application trust recovery permits only its exact refused enrollment checkpoint")
			}
			continue
		}
		if len(acceptedAccount) == 10 && acceptedAccount[9] {
			if entry.Key == "kubernetes/service-managed-core" && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return bad("managed core order recovery permits only its exact failed rollout")
			}
			continue
		}
		if len(acceptedAccount) == 9 && acceptedAccount[8] {
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return bad("accepted Service core permits no uncertain or pending writes")
			}
			continue
		}
		if len(acceptedAccount) == 8 && acceptedAccount[7] {
			if entry.State == "pending" && len(entry.Receipt) == 0 && (entry.Key == "kubernetes/service-issuer-server-enrollment" || strings.HasPrefix(entry.Key, "kubernetes/service-session-original-key-renewal-")) {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("bootstrap permissions recovery permits only the original enrollment and one failed renewal")
			}
			continue
		}
		if len(acceptedAccount) == 7 && acceptedAccount[6] {
			if entry.Key == "kubernetes/service-issuer-server-enrollment" && entry.State == "pending" && len(entry.Receipt) == 0 {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("expired bootstrap recovery permits only the original pending CertIssuer server enrollment")
			}
			continue
		}
		if len(acceptedAccount) == 6 && acceptedAccount[5] {
			if entry.Key == "kubernetes/service-session" && entry.State == "pending" && len(entry.Receipt) == 0 {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("Service session recovery permits only its original pending unstarted Job stage")
			}
			continue
		}
		if len(acceptedAccount) == 5 && acceptedAccount[0] {
			if acceptedAccount[4] && (entry.Key == "kubernetes/service-root-trust" || strings.HasPrefix(entry.Key, "kubernetes/service-root-trust-admission-recovery-")) && entry.State == "pending" && len(entry.Receipt) == 0 {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("partial Service Root trust recovery permits only its two exact pending stages")
			}
			continue
		}
		if len(acceptedAccount) == 4 && acceptedAccount[0] {
			if acceptedAccount[3] && entry.Key == "kubernetes/service-root-trust" && entry.State == "pending" && len(entry.Receipt) == 0 {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("Service Root trust recovery permits only its original pending admission stage")
			}
			continue
		}
		if len(acceptedAccount) == 3 && acceptedAccount[0] {
			if acceptedAccount[2] && entry.Key == "pki-reserve-service-root" && entry.State == "pending" && len(entry.Receipt) == 0 {
				pendingReservation++
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("initial reservation recovery permits only its original pending no-write request")
			}
			continue
		}
		if len(acceptedAccount) == 1 && acceptedAccount[0] {
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("accepted Account checkpoint requires every original stage complete")
			}
			continue
		}
		if len(acceptedAccount) == 2 && acceptedAccount[0] {
			if acceptedAccount[1] && entry.Key == "kubernetes/pki-initial-listeners" && entry.State == "pending" && len(entry.Receipt) == 0 {
				continue
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() {
				return bad("initial listener recovery permits only its exact pending stage")
			}
			continue
		}
		if afterAccount && entry.State == "pending" && len(entry.Receipt) == 0 && (entry.Key == "kubernetes/account-bootstrap" || index == len(e.Journal.Entries)-1 && strings.HasPrefix(entry.Key, "kubernetes/account-admission-recovery-")) {
			pendingAccount++
			continue
		}
		if index == len(e.Journal.Entries)-1 {
			wanted := "openbao-audit-enable"
			if afterMigration {
				wanted = "kubernetes/pki-migrate"
			}
			if afterRoles {
				wanted = "kubernetes/database-runtime-roles"
			}
			if afterSecrets {
				wanted = "kubernetes/database-runtime-secrets"
			}
			if atAccount {
				wanted = "kubernetes/account-bootstrap"
			}
			if entry.Key != wanted || entry.State != "pending" || len(entry.Receipt) != 0 {
				return bad("audit must be the sole pending original step")
			}
		} else if entry.State != "completed" || entry.CompletedAt.IsZero() {
			return bad("preceding initialization and foundation stages must be complete")
		}
	}
	if len(acceptedAccount) == 3 && acceptedAccount[2] && pendingReservation != 1 {
		return bad("exactly one original no-write reservation must remain pending")
	}
	if len(acceptedAccount) == 4 && acceptedAccount[3] && pendingReservation != 1 {
		return bad("one exact original rejected Service Root trust stage required")
	}
	if len(acceptedAccount) == 5 && acceptedAccount[4] && pendingReservation != 2 {
		return bad("exact original admission and known partial Root trust stages required")
	}
	if len(acceptedAccount) == 6 && acceptedAccount[5] && pendingReservation != 1 {
		return bad("one exact original unstarted Service session stage required")
	}
	if len(acceptedAccount) == 7 && acceptedAccount[6] && pendingReservation != 1 {
		return bad("one exact CertIssuer server enrollment checkpoint required")
	}
	if len(acceptedAccount) == 8 && acceptedAccount[7] && pendingReservation != 2 {
		return bad("original enrollment and one exact failed renewal checkpoint required")
	}
	if afterAccount && !(len(acceptedAccount) >= 1 && acceptedAccount[0]) && pendingAccount != 2 {
		return bad("only the original admission and completed-write Account Manager acceptance failure may remain pending")
	}
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/recovery", sha, "qualification.json"), &artifact) != nil || artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != sha || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || artifact.Qualification.Decision != "GO" {
		return bad("original admitted create provenance unavailable")
	}
	var acceptedSHA string
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/foundation-recovery-") && entry.State == "completed" {
			var receipt struct {
				Owners map[string]string `json:"owners"`
				Plan   string            `json:"plan_sha256"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && environmentPreflightDigest.MatchString(receipt.Plan) {
				acceptedSHA = receipt.Plan
			}
		}
	}
	if acceptedSHA == "" || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", acceptedSHA, "plan.json"), &child) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", acceptedSHA, "journal.json"), &journal) != nil || child.SHA256() != acceptedSHA || journal.PlanSHA256 != acceptedSHA || !journal.Accepted || journal.InFlight != "" || journal.Failed != "" || len(child.Resources) != 33 || len(journal.Owners) != 33 || len(journal.Completed) != 33 {
		return bad("original accepted foundation verification unavailable")
	}
	if err := e.Check(context.Background()); err != nil {
		return bad("original initialized execution changed")
	}
	loaded = true
	return e, artifact, &child, journal, nil
}

func verifyInitializedOpenBaoIdentity(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, backend environmentKubernetesBackend) (string, []byte, error) {
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	if err != nil {
		return "", nil, err
	}
	var initialized struct {
		RootToken  string   `json:"root_token"`
		UnsealKeys []string `json:"unseal_keys_b64"`
	}
	if json.Unmarshal(raw, &initialized) != nil || len(initialized.UnsealKeys) != 1 || initialized.RootToken == "" {
		return "", nil, createProviderRecoveryBlock("original complete initialization response unavailable")
	}
	root, err := store.read("openbao/root-token")
	if err != nil || root != initialized.RootToken {
		return "", nil, createProviderRecoveryBlock("original root token differs")
	}
	unseal, err := store.read("openbao/unseal-key")
	if err != nil || unseal != initialized.UnsealKeys[0] {
		return "", nil, createProviderRecoveryBlock("original unseal key differs")
	}
	var proof struct {
		InitSHA   string `json:"init_sha256"`
		PodUID    string `json:"pod_uid"`
		SetUID    string `json:"statefulset_uid"`
		RootRef   string `json:"root_token_ref"`
		UnsealRef string `json:"unseal_ref"`
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-initialize" && entry.State == "completed" {
			if err := decodeEnvironmentJSON(entry.Receipt, &proof); err != nil {
				return "", nil, err
			}
		}
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	set, err := backend.Read(ctx, "StatefulSet/"+ns+"/openbao")
	if err != nil {
		return "", nil, err
	}
	pod, err := backend.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil || proof.InitSHA != environmentPreflightBytesSHA256(raw) || proof.RootRef != "openbao/root-token" || proof.UnsealRef != "openbao/unseal-key" || deploymentplan.String(deploymentplan.Map(set["metadata"])["uid"]) != proof.SetUID || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != proof.PodUID || validateOpenBaoPodOwner(set, pod) != nil {
		return "", nil, createProviderRecoveryBlock("original initialized Pod, StatefulSet or key response identity changed")
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, backend)
	if err != nil || !status.Initialized || status.Sealed {
		return "", nil, createProviderRecoveryBlock("the original initialized OpenBao is not unsealed")
	}
	audit, err := environmentCreateOpenBaoExec(ctx, cfg, backend, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
	var devices deploymentplan.Object
	if err != nil || json.Unmarshal(audit, &devices) != nil {
		return "", nil, createProviderRecoveryBlock("actual audit readback unavailable")
	}
	device := deploymentplan.Map(devices["deployment-audit/"])
	options := deploymentplan.Map(device["options"])
	if device["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
		return "", nil, createProviderRecoveryBlock("actual declarative redacted audit device differs")
	}
	return root, audit, e.Check(ctx)
}

func runEnvironmentCreateOpenBaoRecovery(args []string) error {
	return runEnvironmentCreateOpenBaoRecoveryContext(context.Background(), args, os.Stdout, os.Stderr, true)
}

func runEnvironmentCreateOpenBaoRecoveryContext(parentContext context.Context, args []string, out, diagnostics io.Writer, summary bool) (resultErr error) {
	fs := flag.NewFlagSet("recover-create-openbao", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	workspaceOption := fs.String("workspace", "", "selected original workspace")
	environmentRoot := fs.String("environment-root", "", "selected original maintained environment root")
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original parent")
	execute := fs.Bool("execute", false, "continue initialized original generation")
	review := fs.String("review-sha256", "", "fresh qualified review")
	confirm := fs.String("confirm", "", "exact stack")
	report := fs.String("report", "", "canonical report")
	timeout := fs.Duration("timeout", 90*time.Minute, "remaining timeout")
	acceptedAccount := fs.Bool("after-account-bootstrap", false, "continue only the fully accepted original Account Manager checkpoint before PKI writes")
	atListeners := fs.Bool("at-pki-listeners", false, "qualify original completed-write listeners lacking their initial restricted OpenBao login role")
	afterListeners := fs.Bool("after-pki-listeners", false, "continue only the actual accepted original listener checkpoint without replaying its writes")
	reserveRecovery := fs.Bool("recover-initial-reservation", false, "qualify the original failed no-write Root request and reload Account Manager's renewed TLS client")
	rootTrustRecovery := fs.Bool("at-service-root-trust-admission", false, "retain the original ready Root and qualify its originally unwritten rejected trust stage")
	partialRootTrust := fs.Bool("at-service-root-trust-rollout", false, "qualify known partial trust writes and complete the original missing verifier Secret binding")
	atServiceSession := fs.Bool("at-service-session", false, "qualify the original unstarted Service bootstrap Job and create its missing prerequisites first")
	renewServiceSession := fs.Bool("at-certissuer-server-enrollment", false, "retain original active PKI and renew an expired, unused bootstrap certificate with its original key before qualified forward enrollment")
	afterRenewalPermissions := fs.Bool("after-bootstrap-renewal-permissions", false, "qualify a failed original-key renewal with no native signing outcome and correct persistent directory permissions in a distinct forward Job")
	afterServiceCore := fs.Bool("after-service-core-enrollment", false, "retain actually accepted renewed core enrollment and continue managed hosts without replaying issuance")
	afterCoreOrder := fs.Bool("after-managed-core-order", false, "qualify original known partial managed-core rollout, renew its unused expired seed with the original key and fix host dependencies")
	afterApplicationTrust := fs.Bool("after-application-enrollment-trust", false, "qualify the original refused application enrollment and exact formal CI trust/expiry repair without changing original PKI")
	renewTransport := fs.Bool("renew-initial-transports", false, "qualify temporary transport renewal with original serving keys before any native issuer exists")
	migrateAccount := fs.Bool("account-schema-migrate", false, "qualify and execute the native initial migration on the verified empty Account Manager database")
	afterAccount := fs.Bool("after-account-startup", false, "verify original started Account Manager after its scoped initial email-format repair")
	emailProof := fs.String("email-key-repair-proof", "", "exact canonical verified initial-email repair proof")
	atAccount := fs.Bool("at-account-admission", false, "qualify original unwritten Account Manager resources after dry-run rejection")
	afterSecrets := fs.Bool("after-database-secrets", false, "verify original completed runtime credential Secrets without rewriting them")
	afterRoles := fs.Bool("after-database-roles", false, "retain original partial logins and complete only the missing verifier grant")
	afterMigration := fs.Bool("after-migration", false, "verify the original completed native migration without rerunning it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *migrateAccount {
		*afterAccount = true
	}
	if *afterRenewalPermissions {
		*renewServiceSession = true
	}
	if *afterServiceCore || *afterCoreOrder || *afterApplicationTrust {
		count := 0
		for _, enabled := range []bool{*afterServiceCore, *afterCoreOrder, *afterApplicationTrust} {
			if enabled {
				count++
			}
		}
		if count != 1 {
			return createProviderRecoveryBlock("select exactly one original core checkpoint")
		}
		if *atServiceSession || *renewServiceSession || *rootTrustRecovery || *partialRootTrust || *reserveRecovery || *atListeners || *afterListeners || *renewTransport {
			return createProviderRecoveryBlock("accepted Service core is a distinct continuation checkpoint")
		}
		*atServiceSession = true
		*acceptedAccount = true
	}
	if *atServiceSession {
		if *rootTrustRecovery || *partialRootTrust || *reserveRecovery || *atListeners || *afterListeners || *renewTransport {
			return createProviderRecoveryBlock("Service session recovery is a distinct completed-lineage checkpoint")
		}
		*acceptedAccount = true
	}
	if *renewServiceSession {
		if *atServiceSession || *rootTrustRecovery || *partialRootTrust || *reserveRecovery || *atListeners || *afterListeners || *renewTransport {
			return createProviderRecoveryBlock("expired bootstrap enrollment is a distinct original-generation checkpoint")
		}
		*atServiceSession = true
		*acceptedAccount = true
	}
	if *reserveRecovery {
		*afterListeners = true
	}
	if *rootTrustRecovery {
		*afterListeners = true
	}
	if *partialRootTrust {
		*rootTrustRecovery = true
		*afterListeners = true
	}
	if *rootTrustRecovery && *reserveRecovery {
		return createProviderRecoveryBlock("ready Root admission and absent reservation are distinct checkpoints")
	}
	if *renewTransport {
		*acceptedAccount = true
	}
	if *atListeners {
		*acceptedAccount = true
	}
	if *afterListeners {
		*acceptedAccount = true
	}
	if *afterListeners && (*atListeners || *renewTransport) {
		return createProviderRecoveryBlock("accepted listener continuation is a distinct operation")
	}
	if *acceptedAccount {
		*afterAccount = true
	}
	if *afterAccount && (*atAccount || *afterMigration || *afterRoles || *afterSecrets) || *atAccount && (*afterMigration || *afterRoles || *afterSecrets) || *afterMigration && (*afterRoles || *afterSecrets) || *afterRoles && *afterSecrets || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || !managedLateNormalTailCallerTimeoutAllowed(*timeout, *afterApplicationTrust) {
		return createProviderRecoveryBlock("exact dev parent and bounded timeout required")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOption != "" {
		workspace = *workspaceOption
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, *environmentRoot)
	if err != nil {
		return err
	}
	if *report != "" {
		if err := validateEnvironmentDeploymentReportPath(*env, *report); err != nil {
			return err
		}
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-openbao"}, args...)); err != nil {
		return err
	}
	commandStarted := time.Now().UTC()
	ctx, cancel := context.WithTimeout(parentContext, *timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	mode := "recovery-preflight"
	if *execute {
		mode = "recovery-execution"
	}
	ctx, closeMeasurements, err := openDeploymentMeasurements(ctx, cfg.Environment, mode)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeMeasurements(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	overallContext := ctx
	prepared := beginDeploymentMeasurement(ctx, "deployment.recovery.preparation")
	defer func() { prepared(resultErr) }()
	profile := []bool{*acceptedAccount, *atListeners}
	if *reserveRecovery {
		profile = append(profile, true)
	}
	if *rootTrustRecovery {
		profile = append(profile, false, true)
	}
	if *partialRootTrust {
		profile = append(profile, true)
	}
	if *atServiceSession {
		profile = []bool{true, false, false, false, false, true}
	}
	if *renewServiceSession {
		profile = []bool{true, false, false, false, false, false, true}
	}
	if *afterRenewalPermissions {
		profile = append(profile, true)
	}
	if *afterServiceCore {
		profile = []bool{true, false, false, false, false, false, false, false, true}
	}
	if *afterCoreOrder {
		profile = []bool{true, false, false, false, false, false, false, false, false, true}
	}
	if *afterApplicationTrust {
		profile = []bool{true, false, false, false, false, false, false, false, false, false, true}
	}
	e, original, child, journal, err := loadInitializedCreateAudit(cfg, store, *parent, *afterMigration, *afterRoles, *afterSecrets, *atAccount, *afterAccount, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 || deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("original configuration or operator values changed")
	}
	ctx, closeSupplementCaller, err := managedLateNormalTailSupplementCallerContext(ctx, store, e, *timeout, *afterApplicationTrust)
	if err != nil {
		return err
	}
	defer closeSupplementCaller()
	overallContext = ctx
	if err := beginManagedLateEarlyAdmittedSourcePhase(ctx, cfg, store, e); err != nil {
		return err
	}
	minimumLife := createRecoveryTransportMinimumLife(ctx, *timeout, *execute)
	if scopedMinimum, scoped, scopeErr := managedLatePostMintTransportMinimum(store, *parent, e); scopeErr != nil {
		return scopeErr
	} else if scoped {
		minimumLife = scopedMinimum
	}
	if *renewTransport {
		minimumLife = time.Minute
	}
	materials, management, err := foundationRecoveryMaterialsForExecution(store, *parent, e, minimumLife)
	if err != nil {
		return err
	}
	if err := verifyFoundationMaterialBindings(store, *parent, child); err != nil {
		return err
	}
	renewed, renewalProof, err := readInitialTransportRenewal(store, *parent, e)
	if err != nil {
		return err
	}
	if *renewTransport && renewalProof != nil {
		return createProviderRecoveryBlock("initial temporary transport renewal already completed; no replay allowed")
	}
	if *renewTransport && *atListeners {
		return createProviderRecoveryBlock("transport renewal and listener recovery are distinct operations")
	}
	for _, name := range []string{"init.json", "root-token", "unseal-key"} {
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao", name), true)
		if err != nil {
			return err
		}
		materials["openbao/"+name] = environmentPreflightBytesSHA256(raw)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	p := createFoundationRecoveryPlan{Schema: "rtk-cloud.create-openbao-recovery/v1", ParentSHA: *parent, ParentOwnerSHA: deploymentplan.Digest(e.owner), ParentJournalSHA: deploymentplan.Digest(e.Journal), FoundationSHA: child.SHA256(), FoundationJournalSHA: deploymentplan.Digest(journal), CheckerSHA: checker, KubeconfigSHA: original.Plan.KubeconfigSHA256, Materials: materials, Plan: original.Plan.Plan, Receipts: original.Plan.Receipts}
	var migration deploymentplan.Plan
	var migrationJournal deploymentplan.Journal
	if *afterMigration {
		p.Schema = "rtk-cloud.create-migration-recovery/v1"
		lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
		checkpoint := strings.SplitN(lease.Holder, ":", 2)[0]
		if !environmentPreflightDigest.MatchString(checkpoint) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", checkpoint, "plan.json"), &migration) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", checkpoint, "journal.json"), &migrationJournal) != nil || migration.SHA256() != checkpoint || migrationJournal.PlanSHA256 != checkpoint || migrationJournal.Accepted || !migrationJournal.AcceptanceStarted || migrationJournal.InFlight != "" || migrationJournal.Failed != "deployment.post-deploy.workloads" || len(migration.Resources) != 1 || len(migrationJournal.Owners) != 1 || len(migrationJournal.Completed) != 1 || migration.Resources[0].ID != "Job/"+p.Plan.Stack+"-video-cloud/pki-migrate" || migrationJournal.Completed[0] != migration.Resources[0].ID || migrationJournal.Owners[migration.Resources[0].ID] == "" {
			return createProviderRecoveryBlock("original completed migration lacks exact native write and verification evidence")
		}
		intent, err := environmentCreateKubernetesStageIntent(&migration)
		intentSHA, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 != intentSHA {
			return createProviderRecoveryBlock("original migration intent differs")
		}
		p.CheckpointPlanSHA, p.CheckpointJournalSHA = checkpoint, deploymentplan.Digest(migrationJournal)
	}

	var roleCheckpoint deploymentplan.Plan
	var roleJournal deploymentplan.Journal
	var passwords map[string]string
	if *afterRoles {
		p.Schema = "rtk-cloud.create-database-roles-recovery/v1"
		roleCheckpoint, roleJournal, passwords, err = loadCreateDatabaseCheckpoint(store, e, &p)
		if err != nil {
			return err
		}
	}
	var secretsCheckpoint deploymentplan.Plan
	var secretsJournal deploymentplan.Journal
	if *afterSecrets {
		p.Schema = "rtk-cloud.create-database-secrets-recovery/v1"
		secretsCheckpoint, secretsJournal, passwords, err = loadCreateDatabaseSecretsCheckpoint(store, e, &p)
		if err != nil {
			return err
		}
	}
	var accountCheckpoint *deploymentplan.Plan
	var accountJournal deploymentplan.Journal
	var emailReplacement string
	var emailProofSHA string
	accountSourcePendingKey := e.Journal.Entries[len(e.Journal.Entries)-1].Key
	if *atAccount || *afterAccount {
		p.Schema = "rtk-cloud.create-account-admission-recovery/v1"
		var sourceSHA, sourceIntent string
		for _, entry := range e.Journal.Entries {
			if entry.Key == "kubernetes/database-runtime-secrets" && entry.State == "completed" {
				var proof struct {
					OriginalSHA        string `json:"original_plan_sha256"`
					OriginalJournalSHA string `json:"original_failed_journal_sha256"`
					Stage              string `json:"actual_retained_verification_stage"`
				}
				if decodeEnvironmentJSON(entry.Receipt, &proof) != nil || !environmentPreflightDigest.MatchString(proof.OriginalSHA) || proof.Stage == "" {
					return createProviderRecoveryBlock("original accepted runtime Secret verification provenance unavailable")
				}
				sourceSHA, sourceIntent = proof.OriginalSHA, entry.IntentSHA256
			}
		}
		secretsCheckpoint, secretsJournal, passwords, err = loadCreateDatabaseSecretsEvidence(store, &p, sourceSHA, sourceIntent)
		if err != nil {
			return err
		}
		if *atAccount {
			raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
			if err != nil {
				return err
			}
			native, err := newEnvironmentKubernetesBackend(string(raw), *env)
			if err != nil {
				return err
			}
			definitions, err := renderEnvironmentCreateAccountFoundationWithRuntime(cfg, p.Plan.Release, operator, store)
			if err != nil {
				return err
			}
			live, err := native.Inventory(ctx, p.Plan.Stack)
			if err != nil {
				return err
			}
			accountCheckpoint, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: *env, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("account-bootstrap", definitions)}, definitions, live)
			if err != nil {
				return err
			}
			intent, err := environmentCreateKubernetesStageIntent(accountCheckpoint)
			intentSHA, hashErr := environmentCreatePlanSHA256(intent)
			if err != nil || hashErr != nil || !environmentPreflightDigest.MatchString(e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256) || len(accountCheckpoint.Resources) != 2 {
				return createProviderRecoveryBlock("original admission provenance or new exact two-resource candidate is incomplete")
			}
			for _, r := range accountCheckpoint.Resources {
				if r.Action != "create" || r.ID != "Deployment/"+p.Plan.Stack+"-account-manager/account-manager" && r.ID != "Service/"+p.Plan.Stack+"-account-manager/account-manager" {
					return createProviderRecoveryBlock("original Account Manager admission was not an unwritten two-resource create")
				}
			}
			p.CheckpointPlanSHA = accountCheckpoint.SHA256()
			p.CheckpointJournalSHA = ""
			p.Materials["original-account-admission-intent"] = e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256
			p.Materials["qualified-account-admission-intent"] = intentSHA
		} else {
			p.Schema = "rtk-cloud.create-account-startup-recovery/v1"
			if *acceptedAccount {
				accountCheckpoint, accountJournal, err = loadAcceptedInitialAccountCheckpoint(store, e, &p)
			} else {
				accountCheckpoint, accountJournal, err = loadOriginalAccountStartupCheckpoint(store, e, &p)
			}
			if err != nil {
				return err
			}
			emailReplacement, emailProofSHA, err = verifyOriginalAccountEmailRepair(store, e, *emailProof, child, journal, accountCheckpoint, accountJournal)
			if err != nil {
				return err
			}
			p.Materials["initial-email-repair-proof"] = emailProofSHA
			if *migrateAccount {
				p.Schema = "rtk-cloud.create-account-migration-recovery/v1"
			}
			if *acceptedAccount {
				p.Schema = "rtk-cloud.create-accepted-account-recovery/v1"
			}
			if *renewTransport {
				p.Schema = "rtk-cloud.create-initial-transport-renewal/v1"
			}
		}
	}
	var listenerCheckpoint *deploymentplan.Plan
	var listenerJournal deploymentplan.Journal
	var readyServiceRoot environmentCreateRegisteredIssuer
	var readyServiceOperation environmentCreateIssuerOperation
	var partialTrustPlan *deploymentplan.Plan
	var partialTrustJournal deploymentplan.Journal
	partialTrustParentKey := ""
	var serviceSession *createServiceSessionRecovery
	var admittedOnly *createManagedLateExpiredAdmittedOnly
	if *atServiceSession {
		raw, readErr := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if readErr != nil {
			return readErr
		}
		native, readErr := newEnvironmentKubernetesBackend(string(raw), *env)
		if readErr != nil {
			return readErr
		}
		native.RolloutTimeout, readErr = environmentRolloutTimeout(cfg.Values)
		if readErr != nil {
			return readErr
		}
		if *afterServiceCore || *afterCoreOrder || *afterApplicationTrust {
			if *afterApplicationTrust {
				if managedLateCompletedTailSelected(e.Journal) {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLateCompletedTailQualified(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = prepareManagedLateCompletedTail(ctx, cfg, store, e, native, &p)
					}
				} else if managedLateRemainingSelected(e.Journal) {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLateRemainingQualified(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = prepareManagedLateRemaining(ctx, cfg, store, e, native, &p)
					}
				} else if selected, selectErr := managedLateExpiredRenewalSelected(store, e); selectErr != nil {
					return selectErr
				} else if selected {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLateExpiredRenewalQualified(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = prepareManagedLateExpiredRenewal(ctx, cfg, store, e, native, &p)
					}
				} else if managedLatePostMintReview(e.Journal) != "" {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLatePostMintQualified(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = prepareManagedLatePostMint(ctx, cfg, store, e, native, &p)
					}
				} else if managedLateSeedForwardReview(e.Journal) != "" {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLateSeedForwardQualified(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = prepareManagedLateSeedForward(ctx, cfg, store, e, native, &p)
					}
				} else if managedLateSessionACKCheckpointReview(e.Journal) != "" {
					preparationContext, preparationCancel, budgetErr := managedLatePreparationContextForOperation(ctx, commandStarted, *execute)
					if budgetErr != nil {
						return budgetErr
					}
					defer preparationCancel()
					ctx = preparationContext
					if *execute {
						serviceSession, err = loadManagedLateQualifiedSession(ctx, cfg, store, e, &p, *review)
					} else {
						serviceSession, err = loadManagedLatePreparedSession(ctx, cfg, store, e, native, &p)
					}
				} else if managedCorePartialReview(e.Journal) != "" || managedCorePendingAdmissionReview(e.Journal) != "" {
					serviceSession, err = loadManagedCorePartialServiceCore(ctx, cfg, store, e, native, &p)
				} else {
					serviceSession, err = loadAcceptedServiceCore(ctx, cfg, store, e, native, operator, &p, false, true)
				}
			} else {
				serviceSession, err = loadAcceptedServiceCore(ctx, cfg, store, e, native, operator, &p, *afterCoreOrder)
			}
		} else {
			serviceSession, err = loadServiceSessionRecovery(ctx, cfg, store, e, native, operator, &p, *renewServiceSession, *afterRenewalPermissions)
		}
		if err != nil {
			return err
		}
		if len(e.Journal.Entries) == 375 && serviceSession.ManagedLateExpiredRenewal != nil {
			late := serviceSession.ManagedLateSession
			originalAdmission, originalSource, readErr := loadAdmittedCreateRecovery(store, e, late.Review)
			if readErr != nil || !managedLateExpiredAdmittedOnlyMembership(e, originalSource, originalAdmission, late, time.Now().UTC()) {
				return managedLateExpiredRenewalBlock("admitted-only-original-authority")
			}
			proof := managedLateExpiredAdmittedOnlyProof(e, originalAdmission, late)
			if p.Materials[managedLateExpiredAdmittedOnlyMaterial] != deploymentplan.Digest(proof) {
				return managedLateExpiredRenewalBlock("admitted-only-material")
			}
			admittedOnly = &proof
		}
		p.Schema = "rtk-cloud.create-service-session-order-recovery/v1"
		if *renewServiceSession {
			p.Schema = "rtk-cloud.create-expired-bootstrap-enrollment-recovery/v1"
		}
		if *afterServiceCore {
			p.Schema = "rtk-cloud.create-accepted-service-core-recovery/v1"
		}
		if *afterCoreOrder {
			p.Schema = "rtk-cloud.create-managed-core-order-recovery/v1"
		}
		if *afterApplicationTrust {
			p.Schema = "rtk-cloud.create-application-trust-recovery/v1"
		}
		if serviceSession.ManagedTransportRecovery != nil && serviceSession.ManagedTransportRecovery.Partial != nil {
			p.Schema = "rtk-cloud.create-managed-transports-partial-recovery/v1"
		}
		if serviceSession.ManagedApplicationPartial != nil {
			p.Schema = "rtk-cloud.create-managed-application-partial-recovery/v1"
		}
		if serviceSession.ManagedApplicationPartial != nil && serviceSession.ManagedApplicationPartial.Interrupted != nil {
			p.Schema = "rtk-cloud.create-managed-application-interrupted-recovery/v1"
		}
		if serviceSession.ManagedApplicationPartial != nil && serviceSession.ManagedApplicationPartial.Interrupted != nil && serviceSession.ManagedApplicationPartial.Interrupted.Previous != nil {
			p.Schema = "rtk-cloud.create-managed-application-app-role-recovery/v1"
		}
		if (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) && serviceSession.ManagedApplicationPartial != nil && serviceSession.ManagedApplicationPartial.Interrupted != nil && serviceSession.ManagedApplicationPartial.Interrupted.Previous != nil && serviceSession.ManagedApplicationPartial.Interrupted.Previous.Previous != nil {
			if *timeout > 30*time.Minute {
				return createOpenBaoACKBlock("declared-thirty-minute-bound")
			}
			if err := prepareCreateManagedOpenBaoACK(ctx, cfg, store, e, native, &p, serviceSession); err != nil {
				return err
			}
			p.Schema = "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1"
		}
		if serviceSession.ManagedCorePartial != nil && serviceSession.ManagedLateSession == nil {
			if *timeout > 30*time.Minute {
				return createOpenBaoACKBlock("declared-thirty-minute-core-tail-bound")
			}
			p.Schema = "rtk-cloud.create-managed-complete-issuer-partial-recovery/v1"
		}
		if serviceSession.ManagedLateSession != nil {
			if err := bindManagedLateBudgetMaterials(p.Materials); err != nil {
				return err
			}
			p.Schema = managedLateSessionSchema
			if serviceSession.ManagedLateSeedForward != nil {
				p.Schema = managedLateSeedForwardSchema
			}
			if serviceSession.ManagedLatePostMint != nil {
				p.Schema = managedLatePostMintSchema
			}
			if serviceSession.ManagedLateExpiredRenewal != nil {
				p.Schema = managedLateExpiredRenewalSchema
			}
			if serviceSession.normalTail != nil {
				p.Schema = managedLateNormalTailSchema
			}
		}
		listenerCheckpoint, listenerJournal, err = loadInitialListenerCheckpoint(store, e, &p, true)
		if err != nil {
			return err
		}
	}
	if *atListeners || *afterListeners {
		listenerCheckpoint, listenerJournal, err = loadInitialListenerCheckpoint(store, e, &p, *afterListeners)
		if err != nil {
			return err
		}
		p.Schema = "rtk-cloud.create-initial-listener-recovery/v1"
		if *afterListeners {
			p.Schema = "rtk-cloud.create-accepted-listener-recovery/v1"
		}
		if *reserveRecovery {
			if err := loadAcceptedSeparatedListeners(store, e, listenerCheckpoint, listenerJournal, &p); err != nil {
				return err
			}
			p.Schema = "rtk-cloud.create-initial-reservation-recovery/v1"
			for _, entry := range e.Journal.Entries {
				if strings.HasPrefix(entry.Key, "account-initial-transport-client-restart-") && entry.State == "completed" {
					p.Materials["verified-account-client-restart"] = deploymentplan.Digest(entry.Receipt)
				}
			}
		}
		if *rootTrustRecovery {
			if err := loadAcceptedSeparatedListeners(store, e, listenerCheckpoint, listenerJournal, &p, true); err != nil {
				return err
			}
			raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
			if err != nil {
				return err
			}
			native, err := newEnvironmentKubernetesBackend(string(raw), *env)
			if err != nil {
				return err
			}
			readyServiceRoot, readyServiceOperation, err = loadOriginalReadyServiceRoot(ctx, cfg, store, e, native, operator, &p)
			if err != nil {
				return err
			}
			p.Schema = "rtk-cloud.create-ready-service-root-admission-recovery/v1"
			p.Materials["original-rejected-service-root-trust-intent"] = e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256
			if *partialRootTrust {
				partialTrustPlan, partialTrustJournal, err = loadPartialServiceRootTrust(store, e, &p)
				if err != nil {
					return err
				}
				partialTrustParentKey = e.Journal.Entries[len(e.Journal.Entries)-1].Key
				p.Schema = "rtk-cloud.create-partial-service-root-trust-recovery/v1"
			}
		}
	}
	if *acceptedAccount && !*renewTransport {
		raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if err != nil {
			return err
		}
		native, err := newEnvironmentKubernetesBackend(string(raw), *env)
		if err != nil {
			return err
		}
		binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, native, operator)
		if err != nil {
			return err
		}
		p.Materials["initial-operator-binding-candidate"] = deploymentplan.Digest(binding)
	}
	var rootTrustCandidate *deploymentplan.Plan
	if *rootTrustRecovery {
		raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if err != nil {
			return err
		}
		native, err := newEnvironmentKubernetesBackend(string(raw), *env)
		if err != nil {
			return err
		}
		rt := environmentCreateBootstrapRuntime{Execution: e, Backend: native, TransportCAPEM: management.CACert, TransportTrustConfigMap: "pki-create-transport-trust", TransportSecrets: map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport"}}
		ca, err := parseFirstCertificatePEM([]byte(management.CACert))
		if err != nil || ca == nil {
			return createProviderRecoveryBlock("original qualified management authority unavailable")
		}
		rt.TransportRootSHA256 = environmentPreflightBytesSHA256(ca.Raw)
		for _, r := range listenerCheckpoint.Resources {
			rt.InitialListeners = append(rt.InitialListeners, deploymentplan.Definition{Object: r.Desired, Phase: r.Phase})
		}
		defs, err := renderEnvironmentCreateFirstTrustDefinitions(ctx, cfg, p.Plan.Bootstrap, rt, readyServiceRoot)
		if err != nil {
			return err
		}
		live, err := native.Inventory(ctx, p.Plan.Stack)
		if err != nil {
			return err
		}
		rootTrustCandidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: *env, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("service-root-trust", defs)}, defs, live)
		if err != nil {
			return err
		}
		if len(rootTrustCandidate.Resources) != 12 {
			return createProviderRecoveryBlock("exact initial Root trust recipe required")
		}
		p.Materials["qualified-ready-root-trust-plan"] = rootTrustCandidate.SHA256()
	}
	if (serviceSession == nil || (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil)) && (managedTransportOpenBaoPullRepair(serviceSession) || managedTransportOpenBaoAuditRepair(serviceSession) || managedTransportOpenBaoRetained(serviceSession)) {
		raw, readErr := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if readErr != nil {
			return readErr
		}
		native, readErr := newEnvironmentKubernetesBackend(string(raw), *env)
		if readErr != nil {
			return readErr
		}
		_, retained, readErr := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, native, serviceSession, false)
		if readErr != nil {
			return readErr
		}
		p.Materials["accepted-service-core-managed-partial-openbao-retained-prerequisites"] = deploymentplan.Digest(retained)
	}
	sha := deploymentplan.Digest(p)
	outputRelative := filepath.Join("deployment/create/openbao-recovery", sha)
	if _, statErr := os.Lstat(filepath.Join(store.Root, outputRelative)); os.IsNotExist(statErr) {
		if err := allowManagedLateSourceReuseOutput(e, outputRelative); err != nil {
			return err
		}
	} else if statErr != nil {
		return statErr
	}
	directory, err := prepareEnvironmentCreateDirectories(store, outputRelative)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "qualification.json")
	if *rootTrustRecovery {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "service-root-trust-plan.json"), mustProviderRecoveryJSON(rootTrustCandidate)); err != nil {
			return err
		}
	}
	if admittedOnly != nil {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateExpiredAdmittedOnlyFilename), mustProviderRecoveryJSON(admittedOnly)); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.redisDurable != nil {
		serviceSession.redisDurable.QualifiedReview = sha
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateRedisDurableFilename), mustProviderRecoveryJSON(serviceSession.redisDurable)); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.normalSupplement != nil {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateNormalTailSupplementFilename), mustProviderRecoveryJSON(serviceSession.normalSupplement)); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.normalSupplement != nil && serviceSession.normalSupplement.Extension != nil {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateNormalTailExtensionFilename), mustProviderRecoveryJSON(serviceSession.normalSupplement.Extension)); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.normalTail != nil {
		serviceSession.normalTail.Review = sha
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateNormalTailFilename), mustProviderRecoveryJSON(serviceSession.normalTail)); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.ManagedLateRemaining != nil && serviceSession.completedTail == nil {
		remaining := serviceSession.ManagedLateRemaining
		remaining.Review = sha
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(outputRelative, managedLateRemainingFilename), mustProviderRecoveryJSON(remaining)); err != nil {
			return err
		}
	}
	if *atServiceSession && !*afterServiceCore && admittedOnly == nil && serviceSession.ManagedLateRemaining == nil {
		if err := writeCreateServiceRecoveryCandidates(store, sha, serviceSession, *afterApplicationTrust); err != nil {
			return err
		}
	}
	if *atAccount {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "account-admission-plan.json"), mustProviderRecoveryJSON(accountCheckpoint)); err != nil {
			return err
		}
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != p.KubeconfigSHA {
		return createProviderRecoveryBlock("original Kubernetes identity differs")
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), *env)
	if err != nil {
		return err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	checks := []struct {
		id  string
		run func() error
	}{
		{"deployment.openbao.target", func() error {
			for _, resource := range p.Plan.Provider.Resources {
				actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, p.Receipts[resource.Key], operator)
				previous := p.Receipts[resource.Key]
				if err != nil || !ready || previous.PublicIP != "" && previous.PublicIP != actual.PublicIP {
					return createProviderRecoveryBlock("original provider allocation changed")
				}
			}
			lease, err := backend.Read(ctx, "Lease/"+p.Plan.Stack+"-video-cloud/rtk-deployment-execution")
			if err != nil {
				return createProviderRecoveryBlock("previous child lease cannot be read")
			}
			if serviceSession != nil && (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) && serviceSession.ManagedApplicationPartial != nil && serviceSession.ManagedApplicationPartial.Interrupted != nil {
				if err := verifyManagedApplicationInterruptedLease(ctx, e, backend, serviceSession); err != nil {
					return err
				}
			} else if lease != nil {
				return createProviderRecoveryBlock("previous child lease has not been released")
			}
			for _, resource := range child.Resources {
				actual, err := backend.Read(ctx, resource.ID)
				if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[resource.ID] {
					return createProviderRecoveryBlock("original accepted foundation owner changed: resource=" + resource.ID)
				}
				desired := resource.Desired
				if renewalProof != nil {
					if override, changed := initialTransportOverride(desired, renewed); changed {
						if renewalProof.Owners[resource.ID] != journal.Owners[resource.ID] {
							return createProviderRecoveryBlock("renewed transport owner differs")
						}
						desired = override
					}
				}
				if resource.ID == "ConfigMap/"+p.Plan.Stack+"-secrets/openbao-file" {
					desired = deploymentplan.Clone(desired)
					old := deploymentplan.String(deploymentplan.Map(desired["data"])["file.hcl"])
					deploymentplan.Map(desired["data"])["file.hcl"] = old + "audit \"file\" \"deployment-audit\" {\n  options {\n    file_path = \"/openbao/audit/audit.jsonl\"\n    log_raw = \"false\"\n  }\n}\n"
				}
				if (*afterSecrets || *atAccount || *afterAccount) && resource.ID == "Secret/"+p.Plan.Stack+"-video-cloud/pki-controller-database" {
					for _, r := range secretsCheckpoint.Resources {
						if r.ID == resource.ID && secretsJournal.Owners[r.ID] == journal.Owners[resource.ID] {
							desired = r.Desired
						}
					}
				}
				if *afterAccount && resource.ID == "Secret/"+p.Plan.Stack+"-account-manager/account-manager-runtime" {
					desired = deploymentplan.Clone(desired)
					deploymentplan.Map(desired["data"])["EMAIL_OUTBOX_ENCRYPTION_KEY"] = emailReplacement
				}
				if serviceSession != nil && serviceSession.ManagedTransportRecovery != nil && serviceSession.ManagedTransportRecovery.Partial != nil {
					override, changed, err := managedTransportPartialFoundationOverride(serviceSession.ManagedTransportRecovery.Partial, resource.ID, journal.Owners[resource.ID], desired)
					if err != nil {
						return err
					}
					if changed {
						desired = override
					}
				}
				if serviceSession != nil && serviceSession.ManagedCorePartial != nil && resource.ID == serviceSession.OpenBaoACK.Original.ID {
					if len(serviceSession.OpenBaoACK.Candidate.Resources) != 1 || serviceSession.OpenBaoACK.Original.UID != journal.Owners[resource.ID] {
						return createOpenBaoACKBlock("original-core-ack-foundation-owner")
					}
					desired = serviceSession.OpenBaoACK.Candidate.Resources[0].Desired
				}
				if serviceSession != nil && serviceSession.ManagedLateRemaining != nil && serviceSession.OpenBaoACK != nil && resource.ID == serviceSession.OpenBaoACK.Original.ID {
					original := resource
					original.UID = journal.Owners[resource.ID]
					original.Desired = desired
					current, _, currentErr := managedLateOpenBaoCurrentDesired(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: backend, ServiceSessionRecovery: serviceSession}, original)
					if currentErr != nil {
						return currentErr
					}
					desired = current
				}
				if !deploymentplan.Matches(desired, actual) {
					return createProviderRecoveryBlock("original foundation drift exceeds its exact qualified audit configuration: resource=" + resource.ID)
				}
				retained := resource
				retained.Action = "retain"
				retained.UID = journal.Owners[resource.ID]
				retained.Desired = desired
				if resource.ID == "StatefulSet/"+p.Plan.Stack+"-secrets/openbao" && managedTransportOpenBaoPullRepair(serviceSession) {
					// Its exact authenticated write is retained, but readiness is the
					// result of this qualified installer pull repair.
					continue
				}
				if err := backend.Wait(ctx, retained); err != nil {
					return err
				}
			}
			if (serviceSession == nil || (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil)) && (managedTransportOpenBaoPullRepair(serviceSession) || managedTransportOpenBaoAuditRepair(serviceSession) || managedTransportOpenBaoRetained(serviceSession)) {
				_, retained, verifyErr := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, backend, serviceSession, false)
				if verifyErr != nil {
					return verifyErr
				}
				if p.Materials["accepted-service-core-managed-partial-openbao-retained-prerequisites"] != deploymentplan.Digest(retained) {
					return createProviderRecoveryBlock("interrupted OpenBao prerequisite owner or custody evidence changed")
				}
			}
			_, _, err = verifyRecoveryInitializedOpenBao(ctx, cfg, store, e, backend, serviceSession)
			if err != nil {
				return err
			}
			return verifyFoundationVolumeCapacity(ctx, cfg, backend, child, p.Plan.Provider.WorkloadAllocations, operator)
		}},
		{"deployment.openbao.release", func() error { return verifyEnvironmentRelease(ctx, cfg, p.Plan.Release) }},
		{"deployment.openbao.storage", func() error {
			if err := qualifyProviderRecoveryStorage(ctx, cfg, operator); err != nil {
				return err
			}
			return validateEnvironmentCreateStorageAllocation(cfg)
		}},
		{"deployment.openbao.bootstrap", func() error {
			if err := qualifyEnvironmentCreateRecordedRuntime(store); err != nil {
				return err
			}
			var preserved []string
			if *rootTrustRecovery {
				preserved = []string{readyServiceRoot.ID}
			}
			if *atServiceSession {
				preserved = []string{serviceSession.Ceremony.Root.ID}
				if serviceSession.PostServiceResume && serviceSession.MQTTRoot != nil {
					preserved = append(preserved, serviceSession.MQTTRoot.ID)
					if serviceSession.OpenBaoRoot != nil {
						preserved = append(preserved, serviceSession.OpenBaoRoot.ID)
					}
					if serviceSession.ManagedTransportRecovery != nil {
						preserved = append(preserved, serviceSession.ManagedTransportRecovery.AppRoot.ID)
					} else if serviceSession.AppTrustRecovery != nil {
						preserved = append(preserved, serviceSession.AppTrustRecovery.Root.ID)
					}
				}
			}
			if serviceSession != nil && serviceSession.ManagedTransportRecovery != nil {
				if serviceSession.ManagedTransportRecovery.Partial != nil {
					pullCandidate := serviceSession.Candidate
					if serviceSession.ManagedApplicationPartial != nil {
						pullCandidate = serviceSession.ManagedApplicationPartial.Historical.PostManaged.Plan
					}
					if err := validateManagedOpenBaoImagePullSecret(cfg, pullCandidate, child, journal); err != nil {
						return err
					}
				}
				if err := qualifyEnvironmentCreateManagedTransportCapabilities(ctx, cfg, store, e, backend, operator, &p, serviceSession); err != nil {
					return err
				}
			} else if err := qualifyEnvironmentCreateBootstrapCapabilities(ctx, cfg, p.Plan.Bootstrap, store, operator, preserved...); err != nil {
				return err
			}
			if *afterSecrets || *atAccount || *afterAccount {
				if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, backend, &secretsCheckpoint, secretsJournal); err != nil {
					return err
				}
				if err := verifyCreateDatabaseLogins(ctx, cfg, backend, passwords, true); err != nil {
					return err
				}
				if *afterAccount {
					if *acceptedAccount {
						if !*rootTrustRecovery && !*atServiceSession {
							if err := verifyNoInitialIssuers(ctx, cfg, backend); err != nil {
								return err
							}
						}
						if !*renewTransport {
							if _, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, backend, operator); err != nil {
								return err
							}
						}
					}
					if *atServiceSession {
						if *afterServiceCore || *afterCoreOrder || *afterApplicationTrust {
							observed := p
							observed.Materials = map[string]string{}
							var current *createServiceSessionRecovery
							var err error
							if *afterApplicationTrust {
								if serviceSession.completedTail != nil {
									current = serviceSession
									err = verifyManagedLateCompletedTailCurrent(ctx, cfg, store, e, backend, current, &observed)
								} else if serviceSession.ManagedLateRemaining != nil {
									current = serviceSession
									if *execute && serviceSession.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
										current, err = loadManagedLateRemainingQualified(ctx, cfg, store, e, &observed, *review)
									} else {
										err = verifyManagedLateRemainingCurrent(ctx, cfg, store, e, backend, current, &observed)
									}
								} else if serviceSession.ManagedLateExpiredRenewal != nil {
									current = serviceSession
									err = verifyManagedLateExpiredRenewalCurrent(ctx, cfg, store, e, backend, current, &observed)
								} else if serviceSession.ManagedLatePostMint != nil {
									current = serviceSession
									err = verifyManagedLatePostMintCurrent(ctx, cfg, store, e, backend, current, &observed)
								} else if serviceSession.ManagedLateSeedForward != nil {
									current = serviceSession
									err = verifyManagedLateSeedForwardCurrent(ctx, cfg, store, e, backend, current, &observed)
								} else if serviceSession.ManagedLateSession != nil {
									current = serviceSession
									err = verifyManagedLateQualifiedCurrent(ctx, cfg, store, e, backend, current, &observed)
								} else if serviceSession.ManagedCorePartial != nil {
									current, err = loadManagedCorePartialServiceCore(ctx, cfg, store, e, backend, &observed)
								} else {
									current, err = loadAcceptedServiceCore(ctx, cfg, store, e, backend, operator, &observed, false, true)
								}
							} else {
								current, err = loadAcceptedServiceCore(ctx, cfg, store, e, backend, operator, &observed, *afterCoreOrder)
							}
							if err != nil {
								return err
							}
							if serviceSession.OpenBaoACK != nil && (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) {
								if err := prepareCreateManagedOpenBaoACK(ctx, cfg, store, e, backend, &observed, current); err != nil {
									return err
								}
							}
							if deploymentplan.Digest(current.Ceremony) != deploymentplan.Digest(serviceSession.Ceremony) {
								return createProviderRecoveryBlock("accepted native core session changed")
							}
							if key := firstManagedMaterialMismatchKey(p.Materials, observed.Materials); key != "" {
								return createProviderRecoveryBlock("accepted native core evidence changed: material=" + key)
							}
							if *afterCoreOrder || *afterApplicationTrust {
								if serviceSession.ManagedLateRemaining != nil {
									if serviceSession.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
										return verifyManagedLateTerminalFinalizerSource(ctx, cfg, store, e, serviceSession)
									}
									return admitManagedLateRemainingLogHelper(ctx, cfg, store, e, backend, serviceSession)
								}
								if serviceSession.ManagedLateExpiredRenewal != nil {
									return admitManagedLateExpiredRenewalCandidates(ctx, backend, serviceSession.ManagedLateSession)
								}
								if serviceSession.ManagedLatePostMint != nil {
									if err := serviceSession.ManagedLatePostMint.Candidate.Admit(ctx, backend); err != nil {
										return err
									}
									return admitManagedLatePostMintCandidates(ctx, backend, serviceSession.ManagedLateSession)
								}
								if serviceSession.ManagedLateSeedForward != nil {
									return serviceSession.ManagedLateSeedForward.Candidate.Admit(ctx, backend)
								}
								if serviceSession.ManagedLateSession != nil {
									return admitManagedLateSessionCandidates(ctx, backend, serviceSession.ManagedLateSession)
								}
								if serviceSession.RenewalCandidate != nil {
									if err := serviceSession.RenewalCandidate.Admit(ctx, backend); err != nil {
										return err
									}
								}
								if *afterApplicationTrust && serviceSession.GrantCandidate != nil {
									if err := serviceSession.GrantCandidate.Admit(ctx, backend); err != nil {
										return err
									}
								}
								if serviceSession.QuiesceCandidate != nil {
									if err := serviceSession.QuiesceCandidate.Admit(ctx, backend); err != nil {
										return err
									}
								}
								if serviceSession.InstallationCandidate != nil {
									if err := serviceSession.QuiesceCandidate.Admit(ctx, backend); err != nil {
										return err
									}
									if err := serviceSession.InstallationCandidate.Admit(ctx, backend); err != nil {
										return err
									}
								}
								if serviceSession.OpenBaoACK != nil && (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) {
									if err := serviceSession.OpenBaoACK.Candidate.Admit(ctx, backend); err != nil {
										return err
									}
								}
								return serviceSession.Candidate.Admit(ctx, backend)
							}
							return nil
						}
						if err := verifyServiceSessionRecovery(ctx, cfg, store, e, backend, serviceSession); err != nil {
							return err
						}
						return serviceSession.Candidate.Admit(ctx, backend)
					}
					if *atListeners {
						for _, r := range listenerCheckpoint.Resources {
							actual, err := backend.Read(ctx, r.ID)
							if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != listenerJournal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
								return createProviderRecoveryBlock("original initial listener owner or body changed")
							}
						}
						rootPath, _ := privateDeploymentStatePath(*env, "openbao/root-token")
						if err := verifyInitialListenerAuthenticationAbsent(ctx, cfg, environmentCreateBootstrapRuntime{Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: rootPath}}); err != nil {
							return err
						}
					}
					if *afterListeners {
						rootPath, _ := privateDeploymentStatePath(*env, "openbao/root-token")
						if *reserveRecovery {
							if err := verifyEmptyInitialPKIRegistry(ctx, cfg, backend); err != nil {
								return err
							}
						}
						if *rootTrustRecovery {
							observed := p
							observed.Materials = map[string]string{}
							root, op, err := loadOriginalReadyServiceRoot(ctx, cfg, store, e, backend, operator, &observed)
							if err != nil || deploymentplan.Digest(root) != deploymentplan.Digest(readyServiceRoot) || op != readyServiceOperation {
								return createProviderRecoveryBlock("original ready Root changed")
							}
						}
						if *partialRootTrust {
							if err := verifyPartialServiceRootTrust(ctx, backend, partialTrustPlan, partialTrustJournal); err != nil {
								return err
							}
						}
						if err := verifyInitialListenerAuthentication(ctx, cfg, environmentCreateBootstrapRuntime{Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: rootPath}}, *reserveRecovery || *rootTrustRecovery); err != nil {
							return err
						}
						if *rootTrustRecovery {
							if err := rootTrustCandidate.Admit(ctx, backend); err != nil {
								return err
							}
						}
						return verifyOriginalMigrationCheckpoint(ctx, cfg, store, backend, listenerCheckpoint, listenerJournal)
					}
					if *migrateAccount {
						return verifyEmptyInitialAccountDatabase(ctx, cfg, backend, accountCheckpoint, accountJournal)
					}
					return verifyOriginalMigrationCheckpoint(ctx, cfg, store, backend, accountCheckpoint, accountJournal)
				}
				if *atAccount {
					return accountCheckpoint.Admit(ctx, backend)
				}
				return nil
			}
			if *afterRoles {
				return verifyCreateDatabaseCheckpoint(ctx, cfg, store, backend, &roleCheckpoint, roleJournal, passwords, false)
			}
			if *afterMigration {
				return verifyOriginalMigrationCheckpoint(ctx, cfg, store, backend, &migration, migrationJournal)
			}
			return nil
		}},
		{"deployment.openbao.network", func() error {
			if _, err := environmentCreateDataPlaneNodeUpstreams(ctx, cfg, backend); err != nil {
				return err
			}
			return qualifyEnvironmentCreateDNS(ctx, cfg, p.Plan.DNS, operator)
		}},
		{"deployment.openbao.inputs", func() error {
			fresh, err := resolveDeploymentConfig(workspace, *env, "")
			if err != nil {
				return err
			}
			_, values, err := readEnvironmentCreateOperator(fresh)
			if err != nil {
				return err
			}
			currentMinimum := minimumLife
			if *execute && !*renewTransport {
				currentMinimum = createRecoveryTransportMinimumLife(ctx, *timeout, true)
			}
			if scopedMinimum, scoped, scopeErr := managedLatePostMintTransportMinimum(store, *parent, e); scopeErr != nil {
				return scopeErr
			} else if scoped {
				currentMinimum = scopedMinimum
			}
			current, _, err := foundationRecoveryMaterialsForExecution(store, *parent, e, currentMinimum)
			if err != nil {
				return err
			}
			for _, name := range []string{"init.json", "root-token", "unseal-key"} {
				raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao", name), true)
				if err != nil {
					return err
				}
				current["openbao/"+name] = environmentPreflightBytesSHA256(raw)
			}
			if *afterRoles || *afterSecrets || *atAccount || *afterAccount {
				if err := bindCreateDatabaseMaterial(store, *parent, current); err != nil {
					return err
				}
			}
			if *atAccount {
				current["original-account-admission-intent"] = p.Materials["original-account-admission-intent"]
				current["qualified-account-admission-intent"] = p.Materials["qualified-account-admission-intent"]
			}
			if *afterAccount {
				_, sha, err := verifyOriginalAccountEmailRepair(store, e, *emailProof, child, journal, accountCheckpoint, accountJournal)
				if err != nil {
					return err
				}
				current["initial-email-repair-proof"] = sha
			}
			if *acceptedAccount && !*renewTransport {
				binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, backend, operator)
				if err != nil {
					return err
				}
				current["initial-operator-binding-candidate"] = deploymentplan.Digest(binding)
			}
			if *reserveRecovery {
				for _, key := range []string{"accepted-initial-auth-separation-plan", "accepted-initial-auth-separation-journal", "original-failed-service-root-intent"} {
					current[key] = p.Materials[key]
				}
				if sha := p.Materials["verified-account-client-restart"]; sha != "" {
					current["verified-account-client-restart"] = sha
				}
			}
			if *rootTrustRecovery {
				for key, value := range p.Materials {
					if strings.HasPrefix(key, "original-service-root-") || strings.HasPrefix(key, "original-partial-root-trust-") || strings.HasPrefix(key, "accepted-initial-auth-separation-") || key == "original-rejected-service-root-trust-intent" || key == "qualified-ready-root-trust-plan" {
						current[key] = value
					}
				}
			}
			if *atServiceSession {
				for key, value := range p.Materials {
					if strings.HasPrefix(key, "original-service-session-") || strings.HasPrefix(key, "original-service-root-") || strings.HasPrefix(key, "original-failed-bootstrap-renewal-") || strings.HasPrefix(key, "accepted-service-core-") {
						current[key] = value
					}
				}
			}
			if deploymentplan.Digest(fresh) != p.Plan.ConfigurationSHA256 || deploymentplan.Digest(values) != p.Plan.OperatorSHA256 || deploymentplan.Digest(current) != deploymentplan.Digest(p.Materials) {
				return createProviderRecoveryBlock("original initialized inputs changed")
			}
			if err := verifyEnvironmentImageSources(ctx, fresh, p.Plan.Release, environmentCIImageCommand(fresh)); err != nil {
				return err
			}
			return e.Check(ctx)
		}},
	}
	if !*execute {
		prepared(nil)
		q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
		for _, check := range checks {
			err := runEnvironmentProgress(ctx, out, check.id, func(context.Context) error { return check.run() })
			gate := deploymentplan.GateResult{ID: check.id, Required: true, Status: "PASS", EvidenceSHA256: sha}
			if err != nil {
				gate.Status = "BLOCKED"
				gate.Cause = err.Error()
				q.Decision = "NO-GO"
			}
			q.Gates = append(q.Gates, gate)
		}
		if reuseErr := checkManagedLateSourceReuse(e); reuseErr != nil && q.Decision == "GO" {
			q.Decision = "NO-GO"
			q.Gates[len(q.Gates)-1].Status = "BLOCKED"
			q.Gates[len(q.Gates)-1].Cause = reuseErr.Error()
		}
		now := time.Now().UTC()
		validity := 15 * time.Minute
		if *renewTransport {
			validity = time.Minute
		}
		a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(validity)}
		if q.Decision == "GO" {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "qualifications", now.Format("20060102T150405.000000000Z")+".json"), mustProviderRecoveryJSON(a)); err != nil {
				return err
			}
			if err := store.write(strings.TrimPrefix(path, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(a), true); err != nil {
				return err
			}
		}
		if *report != "" {
			if err := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(a), true); err != nil {
				return err
			}
		}
		if summary {
			fmt.Fprintf(out, "%s environment=dev scope=initialized-openbao-recovery review-sha256=%s\n", q.Decision, sha)
		}
		if q.Decision != "GO" {
			return exitCode(1)
		}
		return nil
	}
	var a createFoundationRecoveryArtifact
	validity := 15 * time.Minute
	if *renewTransport {
		validity = time.Minute
	}
	if *confirm != p.Plan.Stack || *review != sha || readEnvironmentPreflightJSON(path, &a) != nil || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || deploymentplan.Digest(a.Plan) != sha || a.Qualification.PlanSHA256 != sha || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 || !a.ValidUntil.Equal(a.QualifiedAt.Add(validity)) || a.QualifiedAt.After(time.Now().UTC()) || !time.Now().Before(a.ValidUntil) {
		return createProviderRecoveryBlock("fresh complete initialized OpenBao recovery and exact confirmation required")
	}
	for index, gate := range a.Qualification.Gates {
		if !gate.Required || gate.Status != "PASS" || gate.ID != checks[index].id || gate.EvidenceSHA256 != sha {
			return createProviderRecoveryBlock("initialized recovery gate is not qualified")
		}
	}
	qualificationBytesSHA := ""
	if serviceSession != nil && serviceSession.ManagedLateSession != nil {
		qualificationBytesSHA, err = readManagedLateQualificationFingerprint(store, a)
		if err != nil {
			return err
		}
	}
	for _, index := range []int{0, 2, 3, 5} {
		prepared(nil)
		measured := beginDeploymentMeasurement(ctx, checks[index].id)
		err := checks[index].run()
		measured(err)
		if err != nil {
			return err
		}
	}
	if *renewTransport {
		if err := verifyNoInitialIssuers(ctx, cfg, backend); err != nil {
			return err
		}
		return renewEnvironmentInitialTransports(ctx, cfg, store, e, backend, child, journal, sha)
	}
	if serviceSession != nil && serviceSession.ManagedLateSession != nil && serviceSession.ManagedLatePostMint == nil && serviceSession.ManagedLateRemaining == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		nativeContext, nativeCancel, budgetErr := beginManagedLateNativeBudget(overallContext, p.Materials)
		if budgetErr != nil {
			return budgetErr
		}
		defer nativeCancel()
		ctx = nativeContext
	}
	if admittedOnly == nil && (serviceSession == nil || serviceSession.ManagedLateRemaining == nil) {
		if err := writeCreateRecoveryAdmissionSource(ctx, store, e, sha); err != nil {
			return err
		}
	}
	key := "openbao-recovery-admission-" + sha
	var root string
	var audit []byte
	coreRestoration := serviceSession != nil && serviceSession.ManagedCorePartial != nil && serviceSession.ManagedLateSession == nil
	lateAdmission := serviceSession != nil && serviceSession.ManagedLateSession != nil
	if coreRestoration || lateAdmission {
		// Read the actual retained initialization, audit and authentication before
		// adding the local admission. A refused read must not create a new
		// ambiguous pending entry. An earlier genuine pending admission is closed
		// only with its own exact receipt from this fresh native verification.
		root, audit, err = verifyRecoveryInitializedOpenBao(ctx, cfg, store, e, backend, serviceSession)
		if err != nil {
			return err
		}
		if coreRestoration {
			if err := completeManagedCorePriorAdmission(ctx, e, serviceSession.ManagedCorePartial, a, audit); err != nil {
				return err
			}
		}
	}
	admissionIntent := mustProviderRecoveryJSON(map[string]string{"review_sha256": sha, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": p.ParentJournalSHA})
	var installationOverall time.Time
	if serviceSession != nil && serviceSession.ManagedLatePostMint != nil {
		if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
			return err
		}
		var bounded bool
		installationOverall, bounded = overallContext.Deadline()
		if !bounded {
			return createOpenBaoACKBlock("late-installation-original-overall-budget")
		}
		if _, err := newManagedLateInstallationClock(serviceSession.ManagedLatePostMint, a, deploymentplan.Digest(e.owner), installationOverall, time.Now().UTC()); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.completedTail != nil {
		if serviceSession.normalTail != nil {
			var cancel context.CancelFunc
			ctx, cancel, err = admitManagedLateNormalTail(overallContext, cfg, store, e, serviceSession, a, qualificationBytesSHA, commandStarted)
			if err != nil {
				return err
			}
			defer cancel()
		} else {
			if err := admitManagedLateCompletedTail(ctx, cfg, store, e, serviceSession, a, qualificationBytesSHA); err != nil {
				return err
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(overallContext, serviceSession.completedTail.OverallDeadline)
			defer cancel()
		}
	} else if serviceSession != nil && serviceSession.ManagedLateRemaining != nil {
		remainingOverall, bounded := overallContext.Deadline()
		if !bounded {
			return createOpenBaoACKBlock("late-remaining-original-overall-budget")
		}
		if _, err := admitManagedLateRemaining(ctx, store, e, a, qualificationBytesSHA, serviceSession.ManagedLateRemaining, remainingOverall); err != nil {
			return err
		}
		nativeContext, nativeCancel, budgetErr := beginManagedLateRemainingBudget(overallContext, p.Materials, serviceSession.ManagedLateRemaining)
		if budgetErr != nil {
			return budgetErr
		}
		defer nativeCancel()
		ctx = nativeContext
	} else if admittedOnly != nil {
		if err := admitManagedLateExpiredAdmittedOnly(ctx, store, e, a, qualificationBytesSHA, *admittedOnly); err != nil {
			return err
		}
	} else if lateAdmission {
		if err := beginManagedLateParentAdmission(ctx, store, e, a, qualificationBytesSHA, admissionIntent, time.Now().UTC()); err != nil {
			return err
		}
	} else {
		if err := e.BeginEntry(key, "recovery", admissionIntent, time.Now().UTC()); err != nil {
			return err
		}
	}
	if !coreRestoration && !lateAdmission {
		root, audit, err = verifyRecoveryInitializedOpenBao(ctx, cfg, store, e, backend, serviceSession)
		if err != nil {
			return err
		}
	}
	proof := map[string]string{"audit_sha256": environmentPreflightBytesSHA256(audit), "qualified_recovery_sha256": sha}
	if serviceSession != nil && serviceSession.ManagedLatePostMint != nil {
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry == nil || entry.State != "pending" || entry.StartedAt.IsZero() {
			return createOpenBaoACKBlock("late-installation-first-admission")
		}
		clock, err := newManagedLateInstallationClock(serviceSession.ManagedLatePostMint, a, deploymentplan.Digest(e.owner), installationOverall, entry.StartedAt)
		if err != nil {
			return err
		}
		clockSHA, err := writeManagedLateInstallationClock(store, clock)
		if err != nil {
			return err
		}
		proof["installation_clock_sha256"] = clockSHA
	}
	if (serviceSession == nil || (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil)) && (managedTransportOpenBaoPullRepair(serviceSession) || managedTransportOpenBaoAuditRepair(serviceSession) || managedTransportOpenBaoRetained(serviceSession)) {
		_, retained, verifyErr := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, backend, serviceSession, false)
		if verifyErr != nil {
			return verifyErr
		}
		proof = map[string]string{"retained_openbao_prerequisites_sha256": deploymentplan.Digest(retained), "qualified_recovery_sha256": sha, "native_audit_authentication": "required-after-qualified-restoration"}
		if managedTransportOpenBaoRetained(serviceSession) {
			proof["native_audit_authentication"] = "verified-original-accepted-managed-state"
			proof["audit_sha256"] = environmentPreflightBytesSHA256(audit)
		}
	}
	if !*afterMigration && !*afterRoles && !*afterSecrets && !*atAccount && !*afterAccount {
		if err := e.RecordReceipt("openbao-audit-enable", mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
		if err := e.CompleteEntry("openbao-audit-enable", time.Now().UTC()); err != nil {
			return err
		}
	}
	if admittedOnly == nil && (serviceSession == nil || serviceSession.ManagedLateRemaining == nil) {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.ManagedLateSession != nil && serviceSession.completedTail == nil {
		if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, serviceSession.ManagedLateSession); err != nil {
			return err
		}
	}
	if serviceSession != nil && serviceSession.ManagedLatePostMint != nil {
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry == nil {
			return createOpenBaoACKBlock("late-installation-completed-admission")
		}
		clock, clockErr := validateManagedLateInstallationAdmission(store, e, serviceSession.ManagedLatePostMint, a, *entry)
		if clockErr != nil {
			return clockErr
		}
		serviceSession.ManagedLatePostMint.InstallationClock = clock
		nativeContext, nativeCancel, budgetErr := beginManagedLatePostMintBudget(overallContext, p.Materials, serviceSession.ManagedLatePostMint)
		if budgetErr != nil {
			return budgetErr
		}
		defer nativeCancel()
		ctx = nativeContext
	}
	if !*afterMigration && !*afterRoles && !*afterSecrets && !*atAccount && !*afterAccount {
		err = configureEnvironmentCreateOpenBaoKubernetesAuth(ctx, cfg, backend, e, root)
	}
	if err == nil {
		caPath, _ := privateDeploymentStatePath(*env, filepath.Join("deployment/create/material", *parent, "openbao-transport/ca.crt"))
		if renewalProof != nil {
			caPath, _ = privateDeploymentStatePath(*env, filepath.Join("deployment/create/transport-renewal", renewalProof.Review, "openbao-transport/ca.crt"))
		}
		rootPath, _ := privateDeploymentStatePath(*env, "openbao/root-token")
		runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: rootPath, URL: "https://openbao." + p.Plan.Stack + "-secrets.svc:8200", CACertPath: caPath}}
		apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
			return applyEnvironmentCreateStage(ctx, cfg, store, &p.Plan, e, stage, definitions, out)
		}
		var stop func()
		if *acceptedAccount {
			if *atServiceSession {
				runtime.InitialAuthSeparated = true
				runtime.ServiceSessionRecovery = serviceSession
				for _, r := range listenerCheckpoint.Resources {
					runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
				}
				updated, _, updateErr := initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
				if updateErr != nil {
					return updateErr
				}
				runtime.InitialListeners = updated
			}
			if *rootTrustRecovery {
				runtime.InitialAuthSeparated = true
				runtime.ServiceRootReady = &readyServiceRoot
				runtime.ServiceRootOperation = &readyServiceOperation
				runtime.ServiceRootTrustStage = "service-root-trust-admission-recovery-" + sha
				if *partialRootTrust {
					runtime.ServiceRootTrustAdditionalParentKey = partialTrustParentKey
				}
			}
			if *reserveRecovery {
				runtime.InitialAuthSeparated = true
				if p.Materials["verified-account-client-restart"] == "" {
					err = restartInitialAccountTransport(ctx, cfg, store, e, backend, accountCheckpoint, accountJournal, sha)
				}
			}
			if *afterListeners {
				for _, r := range listenerCheckpoint.Resources {
					runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
				}
			}
			if *atListeners {
				err = configureInitialListenerAuthentication(ctx, cfg, runtime)
				var retained []deploymentplan.Definition
				for _, r := range listenerCheckpoint.Resources {
					retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Retain: true})
					runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
				}
				stage := "pki-initial-listener-recovery-" + sha
				if err == nil {
					err = apply(ctx, stage, retained)
				}
				if err == nil {
					err = e.RecordReceipt("kubernetes/pki-initial-listeners", mustProviderRecoveryJSON(map[string]string{"original_plan_sha256": p.CheckpointPlanSHA, "original_failed_journal_sha256": p.CheckpointJournalSHA, "actual_retained_verification_stage": "kubernetes/" + stage}))
				}
				if err == nil {
					err = e.CompleteEntry("kubernetes/pki-initial-listeners", time.Now().UTC())
				}
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterAccount(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
			if err == nil && *reserveRecovery {
				runtime.PKIClient.ReservationRecoveryReview = sha
				var issuers any
				err = runtime.PKIClient.call(ctx, "POST", "/v1/platform/pki/issuers/search", "", map[string]any{}, &issuers)
			}
		} else if *afterAccount {
			if *migrateAccount {
				var migration []deploymentplan.Definition
				migration, err = renderEnvironmentCreateAccountMigration(cfg, p.Plan.Release)
				if err == nil {
					err = apply(ctx, "account-schema-migration-"+sha, migration)
				}
			}
			var retained []deploymentplan.Definition
			for _, r := range accountCheckpoint.Resources {
				retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Retain: true})
			}
			stage := "account-startup-recovery-" + sha
			if err == nil {
				err = apply(ctx, stage, retained)
			}
			if err == nil {
				proof := mustProviderRecoveryJSON(map[string]string{"original_plan_sha256": p.CheckpointPlanSHA, "original_failed_journal_sha256": p.CheckpointJournalSHA, "actual_retained_verification_stage": "kubernetes/" + stage, "email_key_repair_proof_sha256": emailProofSHA})
				for _, key := range []string{"kubernetes/account-bootstrap", accountSourcePendingKey} {
					if err = e.RecordReceipt(key, proof); err != nil {
						break
					}
					if err = e.CompleteEntry(key, time.Now().UTC()); err != nil {
						break
					}
				}
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterAccount(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
		} else if *atAccount {
			var definitions []deploymentplan.Definition
			for _, r := range accountCheckpoint.Resources {
				definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase})
			}
			stage := "account-admission-recovery-" + sha
			err = apply(ctx, stage, definitions)
			if err == nil {
				err = e.RecordReceipt("kubernetes/account-bootstrap", mustProviderRecoveryJSON(map[string]string{"original_admission_plan_sha256": p.CheckpointPlanSHA, "actual_qualified_creation_stage": "kubernetes/" + stage}))
			}
			if err == nil {
				err = e.CompleteEntry("kubernetes/account-bootstrap", time.Now().UTC())
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterAccount(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
		} else if *afterSecrets {
			var retained []deploymentplan.Definition
			for _, resource := range secretsCheckpoint.Resources {
				retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase, Retain: true})
			}
			stage := "database-secrets-recovery-" + sha
			err = apply(ctx, stage, retained)
			if err == nil {
				err = e.RecordReceipt("kubernetes/database-runtime-secrets", mustProviderRecoveryJSON(map[string]string{"original_plan_sha256": p.CheckpointPlanSHA, "original_failed_journal_sha256": p.CheckpointJournalSHA, "actual_retained_verification_stage": "kubernetes/" + stage}))
			}
			if err == nil {
				err = e.CompleteEntry("kubernetes/database-runtime-secrets", time.Now().UTC())
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterDatabase(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
		} else if *afterRoles {
			err = repairCreateDatabaseRoles(ctx, cfg, store, backend, &p, e, apply, sha, &roleCheckpoint, roleJournal, passwords)
			if err == nil {
				err = applyEnvironmentCreateDatabaseSecrets(ctx, &p.Plan, passwords, apply)
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterDatabase(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
		} else if *afterMigration {
			var retained []deploymentplan.Definition
			for _, resource := range migration.Resources {
				retained = append(retained, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase, Retain: true})
			}
			stage := "pki-migrate-recovery-" + sha
			err = apply(ctx, stage, retained)
			if err == nil {
				proof := map[string]string{"original_plan_sha256": p.CheckpointPlanSHA, "original_failed_journal_sha256": p.CheckpointJournalSHA, "actual_retained_verification_stage": "kubernetes/" + stage}
				err = e.RecordReceipt("kubernetes/pki-migrate", mustProviderRecoveryJSON(proof))
				if err == nil {
					err = e.CompleteEntry("kubernetes/pki-migrate", time.Now().UTC())
				}
			}
			if err == nil {
				runtime, stop, err = continueEnvironmentCreateAfterMigration(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
			}
		} else {
			runtime, stop, err = continueEnvironmentCreateAfterOpenBao(ctx, cfg, &p.Plan, store, operator, e, apply, runtime, management)
		}
		if err == nil {
			defer stop()
			if serviceSession != nil && serviceSession.ManagedLateSession == nil && serviceSession.ManagedCorePartial != nil && serviceSession.ManagedCorePartial.Action == managedCoreRestoreOnly {
				err = executeManagedCorePartialForward(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, runtime, apply)
			} else {
				err = executeEnvironmentCreateWithRuntime(ctx, cfg, &p.Plan, p.Receipts, store, operator, e, out, runtime, apply)
			}
			if err == nil {
				ctx, err = managedLateContextAfterFinalization(ctx, runtime)
			}
		}
	}
	restorationOnly := serviceSession != nil && serviceSession.ManagedLateSession == nil && serviceSession.ManagedCorePartial != nil && serviceSession.ManagedCorePartial.Action == managedCoreRestoreOnly
	if err == nil && !restorationOnly {
		err = e.CompleteAndRelease(time.Now().UTC())
	}
	completed := err == nil && !restorationOnly
	decision := "GO"
	if err != nil {
		decision = "NO-GO"
	}
	if *report != "" {
		result := map[string]any{"schema": "rtk-cloud.create-openbao-recovery-result/v1", "environment": *env, "decision": decision, "completed": completed, "restoration_completed": err == nil && restorationOnly, "review_sha256": sha, "parent_plan_sha256": *parent, "journal": e.JournalPath}
		if err != nil {
			result["cause"] = err.Error()
		}
		if writeErr := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(result), true); writeErr != nil {
			return writeErr
		}
	}
	if summary {
		if restorationOnly {
			fmt.Fprintf(out, "%s environment=dev scope=managed-complete-issuer-restoration completed=false restoration-completed=%t\n", decision, err == nil)
		} else {
			fmt.Fprintf(out, "%s environment=dev scope=initialized-openbao-recovery completed=%t\n", decision, completed)
		}
	}
	return err
}

func verifyOriginalMigrationCheckpoint(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend, original *deploymentplan.Plan, journal deploymentplan.Journal) error {
	objects, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	return verifyOriginalMigrationCheckpointInventory(ctx, store, original, journal, objects)
}

func verifyOriginalMigrationCheckpointInventory(ctx context.Context, store secretStore, original *deploymentplan.Plan, journal deploymentplan.Journal, objects []deploymentplan.Object) error {
	retained := *original
	retained.Resources = append([]deploymentplan.Resource(nil), original.Resources...)
	for index, resource := range retained.Resources {
		retained.Resources[index].UID = journal.Owners[resource.ID]
		retained.Resources[index].Action = "retain"
	}
	return verifyEnvironmentTargetWorkloads(ctx, &retained, environmentCreateStageInventory(&retained, objects), func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	})
}

// Qualification covers a complete future execution plus proof validity.
// During actual execution its fixed context deadline accounts for preparation
// already spent; rechecks must cover that same deadline, not reset the budget.
func createRecoveryTransportMinimumLife(ctx context.Context, budget time.Duration, executing bool) time.Duration {
	remaining := budget
	if executing {
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
	}
	return remaining + 15*time.Minute
}

// writeCreateServiceRecoveryCandidates preserves the already qualified concrete
// plans. Admission, source authentication and live fencing remain in the command.
func writeCreateServiceRecoveryCandidates(store secretStore, sha string, serviceSession *createServiceSessionRecovery, afterApplicationTrust bool) error {
	if f := serviceSession.ManagedLateExpiredRenewal; f != nil {
		if f.Candidate == nil {
			return managedLateExpiredRenewalBlock("qualified-archive")
		}
		f.Review = sha
		for name, value := range map[string]any{managedLateExpiredRenewalSourceFilename: f, managedLateExpiredRenewalPlanFilename: f.Candidate} {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, name), mustProviderRecoveryJSON(value)); err != nil {
				return err
			}
		}
	}
	if serviceSession.ManagedLatePostMint != nil {
		post := serviceSession.ManagedLatePostMint
		post.Review = sha
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, managedLatePostMintSourceFilename), mustProviderRecoveryJSON(post)); err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, managedLatePostMintPlanFilename), mustProviderRecoveryJSON(post.Candidate)); err != nil {
			return err
		}
	} else if serviceSession.ManagedLateSeedForward != nil {
		f := serviceSession.ManagedLateSeedForward
		f.Review = sha
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, managedLateSeedForwardSourceFilename), mustProviderRecoveryJSON(f)); err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, managedLateSeedForwardPlanFilename), mustProviderRecoveryJSON(f.Candidate)); err != nil {
			return err
		}
	} else if serviceSession.ManagedLateSession != nil {
		late := serviceSession.ManagedLateSession
		late.Review, serviceSession.Review = sha, sha
		for filename, candidate := range map[string]*deploymentplan.Plan{managedLateSessionOwnerPlanFilename: late.OwnerCandidate, "managed-late-session-custody-plan.json": late.CustodyCandidate, "managed-late-log-stop-plan.json": late.LogStopCandidate, "managed-late-log-helper-plan.json": late.LogHelperCandidate, "managed-late-log-retire-plan.json": late.LogRetireCandidate, "managed-late-log-restore-plan.json": late.LogRestoreCandidate} {
			if candidate == nil {
				return createOpenBaoACKBlock("late-qualified-plan-missing")
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, filename), mustProviderRecoveryJSON(candidate)); err != nil {
				return err
			}
		}
	} else if serviceSession.ManagedCorePartial != nil {
		serviceSession.Review = serviceSession.ManagedCorePartial.OriginalRepairReview
		serviceSession.ManagedCorePartial.ForwardReview = sha
		if proof := serviceSession.ManagedCorePartial.CertIssuerPrepare; proof != nil {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, managedCoreCertIssuerPrepareFilename), mustProviderRecoveryJSON(proof)); err != nil {
				return err
			}
		}

	} else {
		serviceSession.Review = sha
	}
	if serviceSession.ManagedLateSeedForward == nil && serviceSession.ManagedLatePostMint == nil {
		planName := "service-session-plan.json"
		if serviceSession.ManagedLateSession != nil {
			planName = managedLateSessionOwnerPlanFilename
		} else if serviceSession.ManagedCorePartial != nil {
			planName = "managed-complete-issuer-forward-plan.json"
		}
		if serviceSession.IdentityLockForward {
			planName = "service-managed-core-plan.json"
		}
		if (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) && serviceSession.PostServiceResume {
			planName = "mqtt-root-trust-plan.json"
			if serviceSession.PostTransportResume {
				planName = "openbao-root-trust-plan.json"
			}
		}
		if serviceSession.InstallationCandidate != nil {
			for name, plan := range map[string]*deploymentplan.Plan{"current-installation-plan.json": serviceSession.InstallationCandidate, "current-owner-quiesce-plan.json": serviceSession.QuiesceCandidate} {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, name), mustProviderRecoveryJSON(plan)); err != nil {
					return err
				}
			}
		}
		if serviceSession.RenewalCandidate != nil {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "used-bootstrap-renewal-plan.json"), mustProviderRecoveryJSON(serviceSession.RenewalCandidate)); err != nil {
				return err
			}
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, planName), mustProviderRecoveryJSON(serviceSession.Candidate)); err != nil {
			return err
		}
		if serviceSession.OpenBaoACK != nil && (serviceSession.ManagedCorePartial == nil && serviceSession.ManagedLateSession == nil) {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "openbao-host-ack-plan.json"), mustProviderRecoveryJSON(serviceSession.OpenBaoACK.Candidate)); err != nil {
				return err
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "openbao-host-ack-source.json"), mustProviderRecoveryJSON(serviceSession.OpenBaoACK)); err != nil {
				return err
			}
		}
		if afterApplicationTrust && serviceSession.GrantCandidate != nil {
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", sha, "service-trust-grants-plan.json"), mustProviderRecoveryJSON(serviceSession.GrantCandidate)); err != nil {
				return err
			}
		}
	}
	// The descriptor is the archive's commit marker. Publish and verify it only
	// after every referenced preview is durable, so a failed or interrupted
	// candidate write cannot expose a partially committed qualified source.
	if late := serviceSession.ManagedLateSession; late != nil {
		relative := filepath.Join("deployment/create/openbao-recovery", sha, "managed-late-session-source.json")
		if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(late)); err != nil {
			return err
		}
		var retained createManagedLateSessionRecovery
		if err := createOpenBaoACKCompletionRead(store, relative, &retained); err != nil {
			return err
		}
		if managedLateSessionSourceDigest(&retained) != managedLateSessionSourceDigest(late) {
			return createOpenBaoACKBlock("late-qualified-descriptor-round-trip")
		}
	}
	return nil
}
