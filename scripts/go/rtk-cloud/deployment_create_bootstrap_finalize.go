package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func removeEnvironmentCreateCredentialCopy(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace string, expiredSource ...bool) error {
	uid := s.CredentialUIDs[namespace]
	allowSource := len(expiredSource) == 1 && expiredSource[0] && runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.Renewal && s.BootstrapSessionID == runtime.ServiceSessionRecovery.Intent.SessionID
	if r := runtime.ServiceSessionRecovery; r != nil && r.StaleRenewal != nil && len(expiredSource) == 1 && expiredSource[0] && s.BootstrapSessionID == r.StaleRenewal.Cleanup.SessionID && uid == r.StaleRenewal.UIDs[namespace] {
		allowSource = true
	}
	if r := runtime.ServiceSessionRecovery; r != nil && len(expiredSource) == 1 && expiredSource[0] {
		for _, observed := range r.StaleSeeds {
			if s.BootstrapSessionID == observed.Cleanup.SessionID && uid == observed.UIDs[namespace] {
				allowSource = true
			}
		}
	}
	if len(expiredSource) == 1 && expiredSource[0] && runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.ManagedLateSession != nil {
		var err error
		allowSource, err = allowManagedLateExpiredCredentialCopy(ctx, cfg, runtime, s, namespace)
		if err != nil {
			return err
		}
	}
	if namespace == s.BootstrapCredentialNamespace && !allowSource || !slices.Contains(s.BootstrapCredentialNamespaces, namespace) || !environmentBootstrapOwnerUUID.MatchString(uid) {
		return environmentCreateBootstrapBlocker("only the recorded namespace copy may be retired by this bootstrap owner")
	}
	if completed, err := managedLateCompletedCredentialRetirement(ctx, cfg, runtime, s, namespace); err != nil {
		return err
	} else if completed {
		return nil
	}
	id := "Secret/" + namespace + "/" + s.BootstrapCredentialSecret
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil || actual == nil {
		return environmentCreateBootstrapBlocker("original bootstrap copy cannot be inspected before exact retirement")
	}
	meta := deploymentplan.Map(actual["metadata"])
	labels := deploymentplan.Map(meta["labels"])
	if meta["uid"] != uid || deploymentplan.String(meta["resourceVersion"]) == "" || labels["rtk.cloud/bootstrap-session"] != s.BootstrapSessionID || labels["rtk.realtek.com/stack"] != cfg.Values["CLOUD_STACK_NAME"] {
		return environmentCreateBootstrapBlocker("original namespace bootstrap credential copy changed; preserve the replacement")
	}
	key := "bootstrap-copy-retire-" + namespace + "-" + s.BootstrapSessionID
	intent, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": uid, "session_id": s.BootstrapSessionID})
	if err := runtime.Execution.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		return err
	}
	body, _ := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": uid, "resourceVersion": meta["resourceVersion"]}})
	if _, err := runtime.Backend.command(ctx, body, "delete", "--raw=/api/v1/namespaces/"+namespace+"/secrets/"+s.BootstrapCredentialSecret, "-f", "-"); err != nil {
		return environmentCreateBootstrapBlocker("original namespace copy deletion outcome requires journal reconciliation")
	}
	readback, err := runtime.Backend.Read(ctx, id)
	if err != nil || readback != nil {
		return environmentCreateBootstrapBlocker("original namespace credential copy retirement is not verified")
	}
	proof, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": uid, "absent": true})
	if err := runtime.Execution.RecordReceipt(key, proof); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

type environmentCreateBootstrapFinalization struct {
	Environment   string    `json:"environment"`
	SessionID     string    `json:"session_id"`
	LeafSHA256    string    `json:"bootstrap_leaf_sha256"`
	CRLSHA256     string    `json:"crl_sha256"`
	CleanupSHA256 string    `json:"cleanup_sha256"`
	FinalizedAt   time.Time `json:"finalized_at"`
}

// Native finalization is explicitly resumable: its first accepted request can
// seal/revoke/publish while consumers are still installing the new CRL. Only a
// definite 409 plus the same sealed or genuinely expired completed session and real receipt progress
// permits a later request to that same finalizer. A separately qualified terminal
// continuation may resume its exact pending intent; transport failures never retry.
func finalizeEnvironmentCreateServiceSession(ctx context.Context, client *environmentCreatePKIClient, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, operator map[string]string) error {
	if err := validateEnvironmentBootstrapCleanupBinding(&s.CleanupBinding, cfg.Environment); err != nil {
		return err
	}
	if managedLatePostNativeCleanupRequested(runtime) {
		return finishManagedLatePostNativeCleanup(ctx, cfg, store, runtime, s)
	}
	key := environmentCreateServiceFinalizationKey(runtime)
	intent, _ := json.Marshal(deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding})
	if managedLateTerminalFinalizerRequested(runtime) {
		if err := verifyManagedLateTerminalFinalizerMutation(ctx, cfg, store, runtime); err != nil {
			return err
		}
		if runtime.Execution == nil || !managedLateTerminalFinalizerPendingIntent(runtime.Execution.Journal, s) {
			return createOpenBaoACKBlock("terminal-finalizer-original-pending-intent")
		}
	} else {
		if err := expireCompletedRenewedBootstrapSession(ctx, cfg, runtime, s); err != nil {
			return err
		}
		for _, namespace := range s.BootstrapCredentialNamespaces {
			if namespace != s.BootstrapCredentialNamespace {
				if err := removeEnvironmentCreateCredentialCopy(ctx, cfg, runtime, s, namespace); err != nil {
					return err
				}
			}
		}
		if runtime.ProductIdentityScope != "" && createOpenBaoACKCompletionEntry(runtime.Execution.Journal, key) != nil {
			entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, key)
			sha, _ := environmentCreatePlanSHA256(intent)
			if entry.Phase != "bootstrap" || entry.State != "pending" || len(entry.Receipt) != 0 || entry.IntentSHA256 != sha {
				return environmentCreateBootstrapBlocker("original product finalization intent changed or has an unreconciled receipt")
			}
		} else if err := runtime.Execution.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
			return err
		}
	}
	var finalized environmentCreateBootstrapFinalization
	for attempt := 0; ; attempt++ {
		if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
			return err
		}
		err := client.call(ctx, http.MethodPost, "/v1/platform/pki/deployment-bootstrap/"+s.BootstrapSessionID+"/finalize", "", nil, &finalized)
		if err == nil {
			break
		}
		var rejected *environmentCreatePKIHTTPFailure
		if !errors.As(err, &rejected) || rejected.Status != http.StatusConflict {
			return err
		}
		if attempt >= 1 {
			return environmentCreateBootstrapBlocker("same-session native finalizer still rejects completion after genuine CRL receipts; preserve its original partial evidence")
		}
		query := "SELECT json_build_object('status',status,'issuer_id',issuer_id,'root_sha256',root_sha256,'complete',subjects <@ installed_subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'"
		raw, inspectErr := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		var session struct {
			Status   string    `json:"status"`
			Issuer   string    `json:"issuer_id"`
			Root     string    `json:"root_sha256"`
			Complete bool      `json:"complete"`
			Expires  time.Time `json:"expires_at"`
		}
		if inspectErr != nil || decodeEnvironmentJSON(raw, &session) != nil || session.Status != "sealed" && session.Status != "expired" || session.Status == "expired" && (session.Expires.IsZero() || session.Expires.After(time.Now().UTC())) || session.Issuer != s.Intermediate.ID || session.Root != s.Root.RootFingerprint || !session.Complete {
			return environmentCreateBootstrapBlocker("native finalization rejection has no confirmed original sealed-session progress; no retry is permitted")
		}
		// The CRL is issued by the native controller under the original online
		// intermediate key. All configured full Service consumers must install it.
		var crl struct {
			Issuer string    `json:"issuer_id"`
			Digest string    `json:"crl_sha256"`
			Number string    `json:"crl_number"`
			PEM    string    `json:"crl_pem"`
			This   time.Time `json:"this_update"`
			Next   time.Time `json:"next_update"`
		}
		if err := client.call(ctx, http.MethodGet, "/v1/platform/pki/issuers/"+s.Intermediate.ID+"/crl", "", nil, &crl); err != nil {
			return err
		}
		if crl.Issuer != s.Intermediate.ID || !environmentPreflightDigest.MatchString(crl.Digest) || !crl.Next.After(time.Now().UTC()) {
			return environmentCreateBootstrapBlocker("native same-session revocation CRL is unavailable")
		}
		consumers, err := managedTransportServiceConsumerIDs(findEnvironmentCreateDomainPolicy(p, "service").Consumers)
		if err != nil {
			return err
		}
		if err := waitEnvironmentCreateCRLAcks(ctx, cfg, runtime, s.Intermediate.ID, crl.Digest, consumers); err != nil {
			return err
		}
	}
	if finalized.Environment != cfg.Environment || finalized.SessionID != s.BootstrapSessionID || !environmentPreflightDigest.MatchString(finalized.LeafSHA256) || !environmentPreflightDigest.MatchString(finalized.CRLSHA256) || !environmentPreflightDigest.MatchString(finalized.CleanupSHA256) || finalized.FinalizedAt.IsZero() {
		return environmentCreateBootstrapBlocker("native finalization lacks exact original complete revocation and cleanup proof")
	}
	// This command owns the original quiescent session PVC. It verifies the real
	// native finalization, writes its durable tombstone, then unlinks those exact
	// original key/CSR/certificate files. PVC lineage itself remains retained.
	if err := prepareRenewedBootstrapCleanup(ctx, cfg, runtime, s, finalized); err != nil {
		return err
	}
	if err := prepareProductSessionRenewalCleanup(ctx, cfg, store, runtime, s, finalized); err != nil {
		return err
	}
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "env", "PKI_BOOTSTRAP_CERT_PATH=/run/pki-bootstrap/bootstrap.crt", "/app/serviceidentity-bootstrap", "cleanup-credentials"); err != nil {
		return environmentCreateBootstrapBlocker("native original bootstrap custody cleanup has not completed")
	}
	marker, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	var observed struct {
		Schema       string                                 `json:"schema"`
		Finalization environmentCreateBootstrapFinalization `json:"finalization"`
		Root         string                                 `json:"root_sha256"`
		Files        map[string]string                      `json:"file_sha256"`
	}
	if err != nil || decodeEnvironmentJSON(marker, &observed) != nil || observed.Schema != "rtk-cloud.bootstrap-credentials/v1" || observed.Finalization != finalized || observed.Root != s.Root.RootFingerprint || len(observed.Files) != 3 {
		return environmentCreateBootstrapBlocker("actual preserved session PVC tombstone differs from native finalization")
	}
	for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		if !environmentPreflightDigest.MatchString(observed.Files[name]) {
			return environmentCreateBootstrapBlocker("native custody tombstone lacks original material hashes")
		}
	}
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", `test ! -e /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt`); err != nil {
		return environmentCreateBootstrapBlocker("original session key/CSR/certificate absence is not verified")
	}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		actual, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		if err != nil || actual != nil {
			return environmentCreateBootstrapBlocker("all recorded bootstrap credential copies must be absent after finalization")
		}
	}
	return recordEnvironmentCreateServiceFinalization(ctx, cfg, store, runtime, s, finalized, marker)
}

func managedLatePostNativeCleanupRequested(runtime environmentCreateBootstrapRuntime) bool {
	r := runtime.ServiceSessionRecovery
	return r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.PostNativeCleanup != nil
}

// A separately admitted cleanup continuation never calls the native finalizer
// or custody producer again. Their original accepted outcome remains authority.
func finishManagedLatePostNativeCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) error {
	if !managedLatePostNativeCleanupRequested(runtime) {
		return createOpenBaoACKBlock("post-native-cleanup-profile")
	}
	if err := verifyManagedLatePostNativeCleanupMutation(ctx, cfg, store, runtime); err != nil {
		return err
	}
	post := runtime.ServiceSessionRecovery.ManagedLateRemaining.PostNativeCleanup
	marker, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	var tombstone managedLateCustodyTombstone
	if err != nil || environmentPreflightBytesSHA256(marker) != post.TombstoneSHA || decodeEnvironmentJSON(marker, &tombstone) != nil || !managedLateFinalizationEqual(tombstone.Finalization, post.Native) {
		return createOpenBaoACKBlock("post-native-cleanup-original-tombstone")
	}
	return recordEnvironmentCreateServiceFinalization(ctx, cfg, store, runtime, s, post.Native, marker)
}

func recordEnvironmentCreateServiceFinalization(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, finalized environmentCreateBootstrapFinalization, marker []byte) error {
	if err := retireExpiredServiceBootstrapCopies(ctx, cfg, store, runtime, s); err != nil {
		return err
	}
	if err := retireProductSessionRenewalOriginal(ctx, cfg, store, runtime, s, finalized); err != nil {
		return err
	}
	writer := writeEnvironmentCreateBootstrapJSON
	if runtime.ProductIdentityScope != "" {
		writer = writeProductArtifact
	}
	if _, err := writer(store, filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json"), deploymentplan.Object{"native_finalization": finalized, "custody_tombstone": json.RawMessage(marker), "credential_uids": s.CredentialUIDs}); err != nil {
		return err
	}
	proof, _ := json.Marshal(deploymentplan.Object{"native_finalization": finalized, "custody_tombstone_sha256": environmentPreflightBytesSHA256(marker), "copies_absent": true})
	key := environmentCreateServiceFinalizationKey(runtime)
	if err := runtime.Execution.RecordReceipt(key, proof); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

func environmentCreateServiceFinalizationKey(runtime environmentCreateBootstrapRuntime) string {
	if runtime.ProductIdentityScope != "" {
		return "product-identities-" + runtime.ProductIdentityScope + "-service-session-finalize"
	}
	return "service-session-finalize"
}

func managedLateTerminalFinalizerRequested(runtime environmentCreateBootstrapRuntime) bool {
	r := runtime.ServiceSessionRecovery
	return r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil
}

// This is an additional local intent check after the typed terminal authority
// and its actual native progress have been independently authenticated.
func managedLateTerminalFinalizerPendingIntent(j environmentCreateJournal, s environmentCreateServiceCeremony) bool {
	if len(j.Entries) != 396 || !j.CompletedAt.IsZero() {
		return false
	}
	entry := createOpenBaoACKCompletionEntry(j, "service-session-finalize")
	intent := deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding}
	return entry != nil && entry == &j.Entries[len(j.Entries)-1] && entry.Phase == "bootstrap" && entry.State == "pending" && !entry.StartedAt.IsZero() && entry.CompletedAt.IsZero() && len(entry.Receipt) == 0 && entry.IntentSHA256 == activeTransportJournalDigest(intent)
}

func waitEnvironmentCreateCRLAcks(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, id, digest string, consumers []string) error {
	if !environmentBootstrapOwnerUUID.MatchString(id) || !environmentPreflightDigest.MatchString(digest) || len(consumers) == 0 {
		return environmentCreateBootstrapBlocker("native current CRL and exact full consumer policy required")
	}
	for _, consumer := range consumers {
		if !environmentCreateConsumerID.MatchString(consumer) {
			return environmentCreateBootstrapBlocker("native current CRL consumer policy is malformed")
		}
	}
	query := "SELECT COALESCE(json_agg(consumer_id ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + id + "' AND digest='" + digest + "'"
	for {
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		if err != nil {
			return err
		}
		var actual []string
		if json.Unmarshal(raw, &actual) != nil {
			return environmentCreateBootstrapBlocker("native current CRL receipt inventory is malformed")
		}
		complete := true
		for _, consumer := range consumers {
			if !slices.Contains(actual, consumer) {
				complete = false
				break
			}
		}
		if complete {
			return nil
		}
		select {
		case <-ctx.Done():
			return environmentCreateBootstrapBlocker("deployment execution timeout ended before full native CRL consumption completed")
		case <-time.After(5 * time.Second):
		}
	}
}

func environmentCreatePolicyNames(runtime environmentCreateBootstrapRuntime, suffixes ...string) []string {
	var names []string
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "openbao-policy-create-") {
			continue
		}
		for _, suffix := range suffixes {
			if strings.HasSuffix(entry.Key, suffix) {
				names = append(names, strings.TrimPrefix(entry.Key, "openbao-policy-"))
				break
			}
		}
	}
	slices.Sort(names)
	return names
}
