package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createServiceSessionIntent struct {
	SessionID  string   `json:"session_id"`
	Claim      string   `json:"claim"`
	Subjects   []string `json:"subjects"`
	IssuerID   string   `json:"issuer_id"`
	RootSHA    string   `json:"root_sha256"`
	Namespaces []string `json:"credential_namespaces"`
}

type createServiceSessionRecovery struct {
	redisDurable                 *createManagedLateRedisDurable
	normalTail                   *createManagedLateNormalTail
	normalSupplement             *createManagedLateNormalTailSupplement
	completedTail                *createManagedLateCompletedTail
	ManagedLateRemaining         *createManagedLateRemaining              `json:"ManagedLateRemaining,omitempty"`
	ManagedLateExpiredRenewal    *createManagedLateExpiredRenewal         `json:"ManagedLateExpiredRenewal,omitempty"`
	ManagedLateSeedForward       *createManagedLateSeedForward            `json:"ManagedLateSeedForward,omitempty"`
	ManagedLatePostMint          *createManagedLatePostMint               `json:"ManagedLatePostMint,omitempty"`
	ManagedLateSession           *createManagedLateSessionRecovery        `json:"ManagedLateSession,omitempty"`
	ManagedCorePartial           *createManagedCorePartialRecovery        `json:"ManagedCorePartial,omitempty"`
	OpenBaoACK                   *createOpenBaoACKRecovery                `json:"OpenBaoACK,omitempty"`
	ManagedHistoricalSeedCleanup *createManagedHistoricalSeedCleanup      `json:"ManagedHistoricalSeedCleanup,omitempty"`
	ManagedApplicationPartial    *createManagedApplicationPartialRecovery `json:"ManagedApplicationPartial,omitempty"`
	ManagedTransportRecovery     *createManagedTransportRecovery          `json:"ManagedTransportRecovery,omitempty"`
	AppTrustRecovery             *createAppTrustRecovery                  `json:"AppTrustRecovery,omitempty"`
	DeviceTrustRecovery          *createDeviceTrustRecovery               `json:"DeviceTrustRecovery,omitempty"`
	CoreAccepted                 bool
	OrderForward                 bool
	PartialCore                  *deploymentplan.Plan
	PartialCoreJournal           deploymentplan.Journal
	StaleRenewal                 *createServiceSessionObserved
	ApplicationForward           bool
	GrantImage                   environmentReleaseImage
	GrantDefinitions             []deploymentplan.Definition
	GrantCandidate               *deploymentplan.Plan
	SourceRenewalStage           string
	IdentityLockForward          bool
	PendingManagedKey            string
	PendingManagedPlan           *deploymentplan.Plan
	PendingManagedJournal        deploymentplan.Journal
	PostTransportResume          bool
	OpenBaoIntermediateRejected  bool
	CompletedTransportReview     string
	DeviceBootstrapRecovery      *createDeviceBootstrapRecovery
	CompletedIssuerReview        string
	OpenBaoIntermediate          *environmentCreateRegisteredIssuer
	MQTTIntermediate             *environmentCreateRegisteredIssuer
	OpenBaoRoot                  *environmentCreateRegisteredIssuer
	OpenBaoOperation             *environmentCreateIssuerOperation
	MQTTAcceptedStage            string
	PostServiceResume            bool
	ApplicationStage             string
	CompletedManagedStage        string
	UsedRenewalReview            string
	UsedRenewalHistory           []string
	InstallationForward          bool
	InstallationKey              string
	InstallationPlan             *deploymentplan.Plan
	InstallationJournal          deploymentplan.Journal
	InstallationCandidate        *deploymentplan.Plan
	QuiesceCandidate             *deploymentplan.Plan
	QuiesceHistory               []createQuiesceCheckpoint
	QuiescePendingKey            string
	QuiescePendingPlan           *deploymentplan.Plan
	QuiescePendingJournal        deploymentplan.Journal
	OpenBaoOriginalCandidate     *deploymentplan.Plan
	OriginalMQTTPlan             *deploymentplan.Plan
	MQTTForwardKey               string
	MQTTPendingPlan              *deploymentplan.Plan
	MQTTPendingJournal           deploymentplan.Journal
	MQTTRoot                     *environmentCreateRegisteredIssuer
	MQTTOperation                *environmentCreateIssuerOperation
	PostServiceRenewal           bool
	RenewalBindingSHA            string
	RenewalCandidate             *deploymentplan.Plan
	StaleSeeds                   []createServiceSessionObserved
	Renewal                      bool
	PermissionsForward           bool
	FailedRenewal                *deploymentplan.Plan
	FailedRenewalJournal         deploymentplan.Journal
	FailedRenewalKey             string
	RenewedIntent                createServiceSessionIntent
	RenewedCertPath              string
	OriginalKeySHA               string
	OriginalCSRSHA               string
	Intent                       createServiceSessionIntent
	Ceremony                     environmentCreateServiceCeremony
	Original                     *deploymentplan.Plan
	Journal                      deploymentplan.Journal
	Trust                        *deploymentplan.Plan
	TrustJournal                 deploymentplan.Journal
	Candidate                    *deploymentplan.Plan
	Review                       string
}

func loadServiceSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, renewal ...bool) (*createServiceSessionRecovery, error) {
	r := &createServiceSessionRecovery{Original: &deploymentplan.Plan{}, Trust: &deploymentplan.Plan{}}
	r.Renewal = len(renewal) >= 1 && renewal[0]
	r.PermissionsForward = len(renewal) == 2 && renewal[1]
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createProviderRecoveryBlock("Service session recovery requires its original unstarted Job, absent prerequisites and accepted active Service lineage")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	wantResources := 3
	wantStage := "kubernetes/service-session"
	if r.Renewal {
		wantResources = 2
		wantStage = "kubernetes/service-issuer-server-enrollment"
	}
	if r.PermissionsForward {
		var err error
		r.Original, r.Journal, err = originalPendingServicePlan(store, e, wantStage, 2)
		if err != nil {
			return nil, err
		}
		sha = r.Original.SHA256()
		for _, entry := range e.Journal.Entries {
			if strings.HasPrefix(entry.Key, "kubernetes/service-session-original-key-renewal-") && entry.State == "pending" {
				if r.FailedRenewalKey != "" {
					return bad()
				}
				r.FailedRenewalKey = entry.Key
			}
		}
		r.FailedRenewal, r.FailedRenewalJournal, err = originalPendingServicePlan(store, e, r.FailedRenewalKey, 1)
		if err != nil {
			return nil, err
		}
	}
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), r.Original) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &r.Journal) != nil || r.Original.SHA256() != sha || r.Journal.PlanSHA256 != sha || r.Journal.Accepted || r.Journal.InFlight != "" || len(r.Original.Resources) != wantResources || len(r.Journal.Owners) != 1 || len(r.Journal.Completed) != 0 {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(r.Original)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if r.PermissionsForward {
		for _, value := range e.Journal.Entries {
			if value.Key == wantStage {
				entry = value
			}
		}
	}
	if err != nil || hashErr != nil || entry.Key != wantStage || entry.State != "pending" || len(entry.Receipt) != 0 || entry.IntentSHA256 != hash {
		return bad()
	}
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if readExecutionJSON(filepath.Join(base, "session-intent.json"), &r.Intent) != nil || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || r.Intent.Claim != "pki-service-bootstrap-state" || !slices.Equal(r.Intent.Namespaces, []string{p.Plan.Stack + "-video-cloud", p.Plan.Stack + "-account-manager", p.Plan.Stack + "-secrets"}) {
		return bad()
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil || !slices.Equal(subjects, r.Intent.Subjects) {
		return bad()
	}
	root, _, err := loadOriginalReadyServiceRoot(ctx, cfg, store, e, b, operator, p, true)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if readExecutionJSON(filepath.Join(base, "intermediate-active.json"), &document) != nil {
		return bad()
	}
	intermediate, err := environmentCreateRegisteredIssuerFromMap(document, root.RootFingerprint)
	if err != nil || intermediate.Status != "active" || intermediate.Domain != "service" || intermediate.Kind != "intermediate" || intermediate.Environment != cfg.Environment || document["parent_issuer_id"] != root.ID || intermediate.ID != r.Intent.IssuerID || r.Intent.RootSHA != root.RootFingerprint {
		return bad()
	}
	bound := false
	for _, entry := range e.Journal.Entries {
		var receipt map[string]any
		if strings.HasPrefix(entry.Key, "pki-activate-") && entry.State == "completed" && json.Unmarshal(entry.Receipt, &receipt) == nil && deploymentplan.Digest(receipt) == deploymentplan.Digest(document) {
			bound = true
		}
		if entry.Key == "pki-initial-root-crl-service" && entry.State == "completed" && json.Unmarshal(entry.Receipt, &receipt) == nil {
			file, ok := receipt["crl_file"].(string)
			path, pathErr := store.safePath(file)
			raw, readErr := readBoundedEnvironmentFile(path, true)
			if !ok || pathErr != nil || readErr != nil || receipt["issuer_id"] != root.ID || receipt["crl_pem_sha256"] != environmentPreflightBytesSHA256(raw) {
				return bad()
			}
			r.Ceremony.RootCRLFile = file
			r.Ceremony.RootCRLPEM = string(raw)
		}
	}
	if !bound || r.Ceremony.RootCRLFile == "" {
		return bad()
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return nil, err
	}
	user, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return nil, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, user.UserID)
	if err != nil {
		return nil, err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return nil, err
	}
	actual, err := client.ReadIssuer(ctx, intermediate.ID)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(document) {
		return bad()
	}
	r.Ceremony.Root, r.Ceremony.Intermediate, r.Ceremony.RootCRLSecret = root, intermediate, "pki-create-service-root-crl"
	authentication := *r
	if r.PermissionsForward {
		authentication.Intent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, r.Original.SHA256(), r.Intent.SessionID)
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, &authentication); err != nil {
		return nil, err
	}
	var trustSHA string
	for _, entry := range e.Journal.Entries {
		var receipt struct {
			Plan string `json:"plan_sha256"`
		}
		if entry.Key == "kubernetes/service-active-listeners" && entry.State == "completed" && json.Unmarshal(entry.Receipt, &receipt) == nil {
			trustSHA = receipt.Plan
		}
	}
	if !environmentPreflightDigest.MatchString(trustSHA) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", trustSHA, "plan.json"), r.Trust) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", trustSHA, "journal.json"), &r.TrustJournal) != nil || r.Trust.SHA256() != trustSHA || r.TrustJournal.PlanSHA256 != trustSHA || !r.TrustJournal.Accepted || r.TrustJournal.InFlight != "" || r.TrustJournal.Failed != "" || len(r.Trust.Resources) != 12 || len(r.TrustJournal.Owners) != 12 || len(r.TrustJournal.Completed) != 12 {
		return bad()
	}
	if r.Renewal {
		return prepareExpiredServiceSessionRecovery(ctx, cfg, store, e, b, p, r)
	}
	var definitions []deploymentplan.Definition
	jobCount := 0
	for _, resource := range r.Original.Resources {
		if resource.Action != "create" {
			return bad()
		}
		kind := deploymentplan.String(resource.Desired["kind"])
		switch kind {
		case "Job":
			jobCount++
			if r.Journal.Failed != resource.ID || r.Journal.Owners[resource.ID] == "" || deploymentplan.Map(resource.Desired["spec"])["activeDeadlineSeconds"] != float64(900) {
				return bad()
			}
		case "ServiceAccount":
			if resource.ID != "ServiceAccount/"+p.Plan.Stack+"-video-cloud/pki-service-bootstrap" || r.Journal.Owners[resource.ID] != "" {
				return bad()
			}
		case "PersistentVolumeClaim":
			if resource.ID != "PersistentVolumeClaim/"+p.Plan.Stack+"-video-cloud/"+r.Intent.Claim || r.Journal.Owners[resource.ID] != "" {
				return bad()
			}
		default:
			return bad()
		}
		object := deploymentplan.Clone(resource.Desired)
		if kind == "Job" {
			// Preserve the terminal, never-executed Job. A distinct executor
			// consumes the same original unissued session intent after qualification.
			deploymentplan.Map(object["metadata"])["name"] = "pki-service-bootstrap-forward-" + sha[:12]
		}
		if kind == "PersistentVolumeClaim" {
			deploymentplan.Map(object["spec"])["storageClassName"] = firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain")
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: environmentResourcePhase(kind)})
	}
	if jobCount != 1 {
		return bad()
	}
	if err := verifyServiceSessionRecovery(ctx, cfg, store, e, b, r); err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("service-session-order-recovery", definitions)}, definitions, live)
	if err != nil {
		return nil, err
	}
	for _, resource := range r.Candidate.Resources {
		if resource.Action != "create" {
			return bad()
		}
	}
	p.Materials["original-service-session-plan"] = sha
	p.Materials["original-service-session-journal"] = deploymentplan.Digest(r.Journal)
	p.Materials["original-service-session-intent"] = deploymentplan.Digest(r.Intent)
	p.Materials["original-service-session-intermediate"] = deploymentplan.Digest(document)
	p.Materials["original-service-session-trust-plan"] = trustSHA
	p.Materials["original-service-session-trust-journal"] = deploymentplan.Digest(r.TrustJournal)
	p.Materials["original-service-session-forward-plan"] = r.Candidate.SHA256()
	return r, nil
}

func verifyServiceSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	if r.Renewal {
		if err := verifyExpiredServiceSessionRecovery(ctx, cfg, store, e, b, r); err != nil {
			return err
		}
		if r.PermissionsForward {
			if r.Candidate == nil || len(r.Candidate.Resources) != 1 {
				return createProviderRecoveryBlock("qualified permissions forward plan required")
			}
			object := deploymentplan.Clone(r.Candidate.Resources[0].Desired)
			deploymentplan.Map(object["metadata"])["name"] = "pki-bootstrap-renew-" + r.Original.SHA256()[:12]
			if err := verifyFailedOriginalKeyRenewal(ctx, cfg, b, r, deploymentplan.Definition{Object: object}, e.Journal.PlanSHA256); err != nil {
				return err
			}
			authentication := *r
			authentication.Intent = r.RenewedIntent
			return verifyServiceSessionAuthentication(ctx, cfg, store, e, b, &authentication)
		}
		return nil
	}
	bad := func() error {
		return createProviderRecoveryBlock("original Service session Job has started, changed, or acquired unqualified state")
	}
	for _, resource := range r.Original.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil {
			return err
		}
		if resource.Desired["kind"] != "Job" {
			if actual != nil {
				return bad()
			}
			continue
		}
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.Journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return bad()
		}
		status := deploymentplan.Map(actual["status"])
		if !serviceSessionJobNeverRan(status) {
			return createProviderRecoveryBlock("original unstarted Job must reach native DeadlineExceeded without any Pod execution before a distinct forward executor can be qualified")
		}
		name := deploymentplan.String(deploymentplan.Map(actual["metadata"])["name"])
		raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "get", "pods", "-l", "job-name="+name, "-o", "json")
		var pods struct {
			Items []json.RawMessage `json:"items"`
		}
		if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 0 {
			return bad()
		}
	}
	for _, resource := range r.Trust.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.TrustJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return bad()
		}
	}
	query := "SELECT count(*) FROM pki_deployment_bootstrap_sessions"
	script := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c '" + query + "'"
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", script)
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		return bad()
	}
	if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, b, r.Trust, r.TrustJournal); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return err
	}
	return e.Check(ctx)
}

func verifyServiceSessionRecordedAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, string, string, error) {
	if r != nil && r.ManagedLateSession != nil {
		return managedLateRecordedAuthentication(ctx, cfg, store, e, b, r)
	}
	if r != nil && r.ManagedCorePartial != nil && e != nil {
		if _, err := managedCoreAuthenticationSource(ctx, store, e, r); err == nil {
			if r.ManagedCorePartial.PriorForward != nil {
				return managedCoreReadyControllerAuthentication(ctx, cfg, store, e, b, r)
			}
			return managedCoreRecordedAuthentication(ctx, cfg, store, e, b, r)
		} else if len(e.Journal.Entries) == 0 || !strings.HasPrefix(e.Journal.Entries[len(e.Journal.Entries)-1].Key, "kubernetes/managed-complete-issuer-forward-") || !managedCorePartialNativeCheckpoint(store, e) {
			return "", "", "", err
		}
		// Only the independently authenticated accepted Core forward reaches
		// the ordinary now-Ready Controller path below.
	}
	return verifyServiceSessionRecordedAuthenticationNative(ctx, cfg, store, e, b, r)
}

// The source-specific caller independently checks its original current owner.
func verifyServiceSessionRecordedAuthenticationNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, string, string, error) {
	bad := func() error {
		return createProviderRecoveryBlock("original Service session policy or exclusive OpenBao role changed")
	}
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", r.Ceremony.Intermediate.ID)
	var native environmentCreateOpenBaoIssuerPolicies
	if err != nil || decodeEnvironmentJSON(raw, &native) != nil || native.IssuerID != r.Ceremony.Intermediate.ID || native.Environment != cfg.Environment || native.ServiceClientSignerPolicy == "" {
		return "", "", "", bad()
	}
	name := "rtk-" + cfg.Environment + "-service-bootstrap-" + r.Intent.SessionID
	role := "pki-service-bootstrap-" + cfg.Environment
	intent := deploymentplan.Object{"role": role, "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{name}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	policyBound, roleBound := false, false
	for _, entry := range e.Journal.Entries {
		var receipt deploymentplan.Object
		if entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil {
			continue
		}
		if entry.Key == "openbao-policy-"+name && receipt["environment"] == cfg.Environment && receipt["policy"] == name && receipt["policy_sha256"] == environmentPreflightBytesSHA256([]byte(native.ServiceClientSignerPolicy)) {
			policyBound = true
		}
		if entry.Key == "openbao-role-"+role+"-"+deploymentplan.Digest(intent)[:12] && deploymentplan.Digest(receipt) == deploymentplan.Digest(intent) {
			roleBound = true
		}
	}
	if !policyBound || !roleBound {
		return "", "", "", bad()
	}
	return name, role, native.ServiceClientSignerPolicy, e.Check(ctx)
}

func verifyServiceSessionAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("original Service session policy or exclusive OpenBao role changed")
	}
	name, role, policy, err := verifyServiceSessionRecordedAuthentication(ctx, cfg, store, e, b, r)
	if err != nil {
		return err
	}
	token, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/root-token"), true)
	if err != nil {
		return bad()
	}
	if r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.PostNativeCleanup != nil {
		if err := verifyManagedLatePostNativeAuthenticationSource(ctx, cfg, store, e, b, r, name); err != nil {
			return err
		}
		if err := verifyManagedLatePostNativePolicyAbsent(ctx, cfg, b, token, name); err != nil {
			return err
		}
	} else {
		raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+name+"\n")
		if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(policy) {
			return bad()
		}
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+role+"\n")
	var observed struct {
		Data struct {
			Accounts   []string `json:"bound_service_account_names"`
			Namespaces []string `json:"bound_service_account_namespaces"`
			Policies   []string `json:"token_policies"`
			Audience   string   `json:"audience"`
			TTL        int      `json:"token_ttl"`
			MaxTTL     int      `json:"token_max_ttl"`
			NoDefault  bool     `json:"token_no_default_policy"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &observed) != nil || !slices.Equal(observed.Data.Accounts, []string{"pki-service-bootstrap"}) || !slices.Equal(observed.Data.Namespaces, []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}) || !slices.Equal(observed.Data.Policies, []string{name}) || observed.Data.Audience != "openbao" || observed.Data.TTL != 900 || observed.Data.MaxTTL != 900 || !observed.Data.NoDefault {
		return bad()
	}
	return nil
}

func serviceSessionJobNeverRan(status deploymentplan.Object) bool {
	terminal := false
	for _, value := range certIssuerObjectList(status["conditions"]) {
		condition := deploymentplan.Map(value)
		if condition["status"] != "True" || condition["reason"] != "DeadlineExceeded" || condition["type"] != "Failed" && condition["type"] != "FailureTarget" {
			return false
		}
		if condition["type"] == "Failed" {
			terminal = true
		}
	}
	if !terminal {
		return false
	}
	for _, key := range []string{"active", "failed", "succeeded", "ready", "terminating"} {
		if value := status[key]; value != nil && value != float64(0) {
			return false
		}
	}
	for _, value := range deploymentplan.Map(status["uncountedTerminatedPods"]) {
		if len(certIssuerObjectList(value)) != 0 {
			return false
		}
	}
	return true
}

func applyRecoveredServiceSession(ctx context.Context, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.Candidate == nil || !environmentPreflightDigest.MatchString(r.Review) {
		return environmentCreateBootstrapBlocker("exact Service session recovery qualification required")
	}
	var definitions []deploymentplan.Definition
	for _, resource := range r.Candidate.Resources {
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase})
	}
	stage := "service-session-order-recovery-" + r.Review
	if err := apply(ctx, stage, definitions); err != nil {
		return err
	}
	if err := runtime.Execution.RecordReceipt("kubernetes/service-session", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_session_id": r.Intent.SessionID, "original_job_plan_sha256": r.Original.SHA256()})); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry("kubernetes/service-session", time.Now().UTC())
}
