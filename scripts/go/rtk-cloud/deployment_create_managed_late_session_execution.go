// Execute the admitted original-key late session under native ownership fences.
// Source verification and the execution lease remain attached to every write.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Root's implementation must authenticate the fresh admitted write recipe,
// acquire/record/check a scoped Kubernetes lease, check e.Check before each
// native write and release with bounded cleanup. Release it before apply
// acquires its own child lease. The ordinary parent file fence alone is not a
// substitute. Existing policy/role helpers provide durable intent/readback.
type managedLateSessionNativeWrite func(context.Context, string, deploymentplan.Object, func(context.Context) error) error

func verifyManagedLateSessionRetainedDependencies(ctx context.Context, b environmentKubernetesBackend, late *createManagedLateSessionRecovery) error {
	if late == nil || len(late.TrustOwners) != 3 || len(late.VerifierOwners) != 3 {
		return environmentCreateBootstrapBlocker("late session requires three original trust and verifier owners")
	}
	for _, owners := range []map[string]deploymentplan.Resource{late.TrustOwners, late.VerifierOwners} {
		for id, owner := range owners {
			actual, err := b.Read(ctx, id)
			if err != nil || actual == nil || owner.ID != id || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || deploymentplan.Map(actual["metadata"])["uid"] != owner.UID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(owner.Desired, actual) {
				return environmentCreateBootstrapBlocker("late session retained public trust or verifier owner changed: " + id)
			}
		}
	}
	return nil
}

// This executor creates a distinct native session, then exactly three immutable
// credentials. It never invokes enrollment, regenerates a key/CSR or replaces
// the public trust CMs/verifier Secrets. New UIDs are observed after publication.
func executeManagedLateSessionSeed(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery, apply environmentCreateBootstrapApply, native managedLateSessionNativeWrite) (environmentCreateServiceCeremony, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	var empty environmentCreateServiceCeremony
	bad := func(stage string) (environmentCreateServiceCeremony, error) {
		return empty, environmentCreateBootstrapBlocker("late same-key session executor requires exact separately admitted native progress: stage=" + stage)
	}
	if r == nil || e == nil || late == nil || late.Renewal == nil || native == nil || apply == nil || !environmentPreflightDigest.MatchString(late.Review) || r.Review != late.Review || late.OriginalRepairReview == late.Review || late.NewSessionID == late.OriginalSession || r.RenewedIntent.SessionID != late.NewSessionID || r.RenewalCandidate == nil || len(r.RenewalCandidate.Resources) != 1 || r.RenewalCandidate.SHA256() != late.Renewal.CandidateSHA256 || e.Check(ctx) != nil {
		return bad("fresh-qualified-session")
	}
	admitted, captured, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil || !managedLateSchemaMatches(admitted.Plan.Schema, late) || deploymentplan.Digest(captured) != late.SourceSHA || admitted.Plan.Materials["accepted-service-core-late-session-original-key-seed-plan"] != r.RenewalCandidate.SHA256() || admitted.Plan.Materials["accepted-service-core-late-session-original-enrollment-session"] != deploymentplan.Digest(late.OriginalEnrollmentSession) {
		return bad("actual-complete-go")
	}
	if _, err := managedLateActiveBudget(ctx); err != nil {
		return empty, err
	}
	s := r.Ceremony
	sessionPolicy := "rtk-" + cfg.Environment + "-service-bootstrap-" + late.NewSessionID
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return empty, err
	}
	before, _, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil {
		return empty, err
	}
	if late.ExpiredRenewal != nil {
		if err := verifyManagedLateExpiredRenewalCurrent(ctx, cfg, store, e, runtime.Backend, r, &createFoundationRecoveryPlan{Plan: admitted.Plan.Plan, Materials: admitted.Plan.Materials}); err != nil {
			return empty, err
		}
	}
	if r.ManagedLateSeedForward != nil {
		if err := executeManagedLateSeedTrustForward(ctx, cfg, store, runtime, apply); err != nil {
			return empty, err
		}
	} else {
		// NEW REQUIRED SEAM: authenticates captured accepted core plus only the
		// exact fresh admission/current stage progress, and rechecks live 16/26/9CA/
		// 11clients/4servers/18volumes/original custody/expired-eight seed evidence.
		if err := verifyManagedLateSessionRuntimeSource(ctx, cfg, store, runtime, late); err != nil {
			return empty, err
		}
		if err := verifyManagedLateSessionRetainedDependencies(ctx, runtime.Backend, late); err != nil {
			return empty, err
		}
		if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, late.Renewal); err != nil {
			return empty, err
		}
		if err != nil || !managedLateSessionMayRenewForScope(before, s, subjects, late, time.Now().UTC()) || !managedLateOriginalSessionUnchanged(late.OriginalSessionState, before, time.Now().UTC()) {
			return bad("original-expired-eight")
		}
		policyRaw, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
		var policy environmentCreateOpenBaoIssuerPolicies
		if err != nil || decodeEnvironmentJSON(policyRaw, &policy) != nil || policy.Environment != cfg.Environment || policy.IssuerID != s.Intermediate.ID || environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)) != late.SignerPolicySHA {
			return bad("qualified-original-signer-policy")
		}
		s.BootstrapSessionID = late.NewSessionID
		s.BootstrapCredentialSecret = "service-bootstrap-" + late.NewSessionID
		if err := native(ctx, "late-seed-authorization-"+late.Review, late.SeedAuthorization, func(guarded context.Context) error {
			actual, err := readManagedLateOpenBaoRole(guarded, cfg, store, runtime.Backend, "pki-service-bootstrap-"+cfg.Environment)
			if err != nil || deploymentplan.Digest(actual) != late.OriginalSignerRoleSHA {
				return environmentCreateBootstrapBlocker("qualified original late signer role changed before authorization")
			}
			// Native lease adapter rechecks the exact qualified old signer role SHA
			// before admitting only this new session policy on the existing account.
			if err := installEnvironmentCreateOpenBaoPolicy(guarded, cfg, runtime, sessionPolicy, policy.ServiceClientSignerPolicy); err != nil {
				return err
			}
			return configureEnvironmentCreateOpenBaoRole(guarded, cfg, runtime, "pki-service-bootstrap-"+cfg.Environment, []string{"pki-service-bootstrap"}, []string{sessionPolicy})
		}); err != nil {
			return empty, err
		}
		invalidateManagedLateSourceReuse(e)
		if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", late.Review, "intent.json"), r.RenewedIntent); err != nil {
			return empty, err
		}
		seed := r.RenewalCandidate.Resources[0]
		if len(r.RenewalCandidate.Resources) != 1 || seed.Action != "create" || seed.Desired["kind"] != "Job" {
			return bad("one-qualified-signing-job")
		}
		stage := "service-session-original-key-renewal-" + late.Review
		if err := apply(ctx, stage, []deploymentplan.Definition{{Phase: seed.Phase, Object: deploymentplan.Clone(seed.Desired), Dependencies: slices.Clone(seed.Dependencies)}}); err != nil {
			return empty, err
		}
	}
	s.BootstrapSessionID = late.NewSessionID
	s.BootstrapCredentialSecret = "service-bootstrap-" + late.NewSessionID
	accepted, _, seedJournal, err := readManagedLateEffectiveSeed(store, e, late)
	if err != nil || len(accepted.Resources) != 1 || len(seedJournal.Completed) != 1 || seedJournal.Completed[0] != accepted.Resources[0].ID || !environmentBootstrapOwnerUUID.MatchString(seedJournal.Owners[accepted.Resources[0].ID]) {
		return bad("real-native-signing-job")
	}
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, late.Renewal); err != nil {
		return empty, err
	}
	// Clone the actual original custody owner. Only the certificate path and
	// global SID change; its image, PVC, private paths and original UID remain.
	reader := deploymentplan.Clone(late.Custody.Desired)
	if late.Custody.ID != "Deployment/"+s.BootstrapCredentialNamespace+"/"+s.CustodyReader || !environmentBootstrapOwnerUUID.MatchString(late.Custody.UID) {
		return bad("original-custody-reader")
	}
	custody := managedNamedField(environmentPKIPod(reader)["containers"], "custody")
	if custody == nil {
		return bad("original-custody-container")
	}
	custody["env"] = mergeEnvironmentNamedFields(custody["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": late.NewSessionID, "PKI_BOOTSTRAP_CERT_PATH": late.NewCertificatePath}))
	if err := apply(ctx, "service-custody-reader-renewal-"+late.Review, []deploymentplan.Definition{{Phase: "bootstrap", Object: reader}}); err != nil {
		return empty, err
	}
	material := map[string][]byte{}
	for name, path := range map[string]string{"bootstrap.key": "/run/pki-bootstrap/bootstrap.key", "bootstrap.csr": "/run/pki-bootstrap/bootstrap.csr", "bootstrap.crt": late.NewCertificatePath} {
		value, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil || len(value) == 0 || len(value) > 64<<10 {
			return bad("native-custody-material")
		}
		material[name] = value // private bytes stay in memory, never report/stdout
	}
	identity, err := tls.X509KeyPair(material["bootstrap.crt"], material["bootstrap.key"])
	if err != nil || !managedLateBootstrapChainMatches(identity.Certificate, s) || environmentPreflightBytesSHA256(material["bootstrap.key"]) != late.OriginalKeySHA || environmentPreflightBytesSHA256(material["bootstrap.csr"]) != late.OriginalCSRSHA || environmentPreflightBytesSHA256(identity.Certificate[len(identity.Certificate)-1]) != s.Root.RootFingerprint {
		return bad("same-original-key-csr-new-certificate")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	current, sessionRaw, readErr := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if err != nil || readErr != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || current.Session != late.NewSessionID || current.Issuer != s.Intermediate.ID || current.Root != s.Root.RootFingerprint || current.Status != "active" || current.OtherActive != 0 || current.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !current.Expires.Equal(leaf.NotAfter) || !current.Expires.After(time.Now().UTC()) || !slices.Equal(current.Subjects, subjects) || len(current.Installed) != 0 {
		return bad("real-new-native-session")
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil || old.Status != "expired" || old.Session != before.Session || old.Issuer != before.Issuer || old.Root != before.Root || old.Fingerprint != before.Fingerprint || !old.Expires.Equal(before.Expires) || !slices.Equal(old.Subjects, before.Subjects) || !slices.Equal(old.Installed, before.Installed) {
		return bad("unchanged-old-eight-session")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, s, subjects)
	if err != nil || clients != late.OriginalClientsSHA {
		return bad("unchanged-eleven-client-inventory")
	}
	seedObservation := createManagedLateSeedObservation{Schema: managedLateSessionSchema, Parent: late.ParentSHA, Review: late.Review, Session: current, CertificatePEM: string(material["bootstrap.crt"]), KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, SeedPlanSHA: accepted.SHA256(), SeedJournalSHA: deploymentplan.Digest(seedJournal), ObservedAt: time.Now().UTC()}
	invalidateManagedLateSourceReuse(e)
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), seedObservation); err != nil {
		return empty, err
	}
	var secrets []deploymentplan.Definition
	for _, namespace := range s.BootstrapCredentialNamespaces {
		definition := environmentCreateSecret(namespace, s.BootstrapCredentialSecret, map[string]string{"tls.crt": string(material["bootstrap.crt"]), "tls.key": string(material["bootstrap.key"]), "ca.crt": s.Root.Certificate})
		definition.Object["immutable"] = true
		deploymentplan.Map(definition.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack, "rtk.cloud/bootstrap-session": late.NewSessionID}
		secrets = append(secrets, definition)
	}
	if len(secrets) != 3 {
		return bad("exact-three-new-credential-secrets")
	}
	if err := verifyManagedLateSeedBudget(ctx, leaf.NotAfter, current.Expires); err != nil {
		return empty, err
	}
	if err := apply(ctx, "service-credential-secrets-renewal-"+late.Review, secrets); err != nil {
		return empty, err
	}
	s.CredentialUIDs = map[string]string{}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		actual, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		uid := deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
		if err != nil || actual == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || actual["immutable"] != true {
			return bad("actual-new-secret-owner")
		}
		for name, want := range map[string][]byte{"tls.crt": material["bootstrap.crt"], "tls.key": material["bootstrap.key"], "ca.crt": []byte(s.Root.Certificate)} {
			value, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(actual["data"])[name]))
			if err != nil || !slices.Equal(value, want) {
				return bad("actual-new-secret-material")
			}
		}
		s.CredentialUIDs[namespace] = uid
	}
	namespace, err := runtime.Backend.Read(ctx, "Namespace//"+s.BootstrapCredentialNamespace)
	if err != nil || namespace == nil || deploymentplan.Map(namespace["metadata"])["uid"] != r.Ceremony.CleanupBinding.NamespaceUID {
		return bad("original-namespace-owner")
	}
	s.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: cfg.Environment, SessionID: late.NewSessionID, NamespaceUID: r.Ceremony.CleanupBinding.NamespaceUID, SecretUID: s.CredentialUIDs[s.BootstrapCredentialNamespace], PolicySHA256: late.SignerPolicySHA, KubernetesCA: r.Ceremony.CleanupBinding.KubernetesCA, ConfigMapName: "pki-bootstrap-cleanup-binding-" + late.NewSessionID[:12]}
	if err := validateEnvironmentBootstrapCleanupBinding(&s.CleanupBinding, cfg.Environment); err != nil {
		return empty, err
	}
	cleanupDefinitions, err := managedLateSessionCleanupDefinitions(late, s)
	if err != nil || len(cleanupDefinitions) != 5 {
		return bad("unique-real-cleanup-binding")
	}
	// One new CM, one exact Role target update and three original retained RBAC
	// owners. Existing public trust and verifier definitions never enter this set.
	if err := apply(ctx, "service-cleanup-binding-renewal-"+late.Review, cleanupDefinitions); err != nil {
		return empty, err
	}
	cleanupPolicy := "create-" + late.NewSessionID + "-cleanup-controller"
	cleanupText := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+sessionPolicy)
	if err := native(ctx, "late-cleanup-authorization-"+late.Review, late.CleanupAuthorization, func(guarded context.Context) error {
		actual, err := readManagedLateOpenBaoRole(guarded, cfg, store, runtime.Backend, "pki-controller-"+cfg.Environment)
		if err != nil || deploymentplan.Digest(actual) != late.OriginalControllerRoleSHA {
			return environmentCreateBootstrapBlocker("qualified original late cleanup controller role changed before authorization")
		}
		if err := installEnvironmentCreateOpenBaoPolicy(guarded, cfg, runtime, cleanupPolicy, cleanupText); err != nil {
			return err
		}
		policies := append(slices.Clone(late.ControllerPolicies), cleanupPolicy)
		slices.Sort(policies)
		return configureEnvironmentCreateOpenBaoRole(guarded, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, slices.Compact(policies))
	}); err != nil {
		return empty, err
	}
	if err := verifyManagedLateSessionRetainedDependencies(ctx, runtime.Backend, late); err != nil {
		return empty, err
	}
	invalidateManagedLateSourceReuse(e)
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", late.Review, "observed.json"), createServiceSessionObserved{Cleanup: s.CleanupBinding, UIDs: s.CredentialUIDs, Reader: s.CustodyReader, Session: json.RawMessage(sessionRaw)}); err != nil {
		return empty, err
	}
	r.Renewal = true // only after real new certificate/session/UID readback
	r.Ceremony = s
	return s, e.Check(ctx)
}

// Exact current owner definitions, changing existing literal global session
// IDs only. All init/container images, commands, per-prefix private state and
// root pins are preserved. Controller additionally selects the newly observed
// cleanup CM, using the common producer with the actual new Secret UID.
func managedLateSessionOwnerDefinitions(late *createManagedLateSessionRecovery, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if late == nil || len(late.Owners) != 10 || late.AcceptedCore == nil || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) || service.BootstrapSessionID != late.NewSessionID || service.BootstrapSessionID == late.OriginalEnrollmentSession {
		return nil, environmentCreateBootstrapBlocker("late SID propagation requires ten exact original owner definitions and a distinct qualified session")
	}
	stack := late.AcceptedCore.Stack
	ns := stack + "-video-cloud"
	ids := managedLateNativeOwnerIDs(stack)
	var definitions []deploymentplan.Definition
	for _, id := range ids {
		owner, ok := late.Owners[id]
		if !ok || owner.ID != id || !environmentBootstrapOwnerUUID.MatchString(owner.UID) {
			return nil, environmentCreateBootstrapBlocker("late SID propagation lacks original owner: " + id)
		}
		object := deploymentplan.Clone(owner.Desired)
		if id == "Deployment/"+ns+"/video-cloud-logingester" {
			// The exclusive helper owns the eventual Log SID update. Retaining
			// the original template keeps its stop baseline genuinely observed.
			definitions = append(definitions, deploymentplan.Definition{Phase: owner.Phase, Object: object, Dependencies: slices.Clone(owner.Dependencies), Retain: true})
			continue
		}
		pod, changed := environmentPKIPod(object), 0
		for _, section := range []string{"initContainers", "containers"} {
			for _, value := range certIssuerObjectList(pod[section]) {
				container := deploymentplan.Map(value)
				fields, err := environmentPKINamedFields(container["env"])
				if err != nil {
					return nil, err
				}
				field := fields["PKI_BOOTSTRAP_SESSION_ID"]
				if field == nil {
					continue // provider/business containers are untouched
				}
				if field["valueFrom"] != nil || field["value"] != managedLateOriginalWorkloadSession(late) {
					return nil, environmentCreateBootstrapBlocker("late native owner has a different or indirect original session: " + id)
				}
				field["value"] = late.NewSessionID
				changed++
			}
		}
		if changed == 0 {
			return nil, environmentCreateBootstrapBlocker("late native owner has no original global session binding: " + id)
		}
		if id == "Deployment/"+ns+"/pki-controller" {
			volume := managedNamedField(pod["volumes"], "bootstrap-cleanup")
			config := deploymentplan.Map(volume["configMap"])
			oldName := managedLateOriginalWorkloadCleanup(late).ConfigMapName
			if oldName == "" {
				oldName = "pki-bootstrap-cleanup-binding"
			}
			if volume == nil || config["name"] != oldName {
				return nil, environmentCreateBootstrapBlocker("late controller lost its exact original cleanup CM reference")
			}
			// New CM NAME is known before allocation. The Secret UID belongs to
			// its later runtime data producer and is never fabricated in preview.
			config["name"] = "pki-bootstrap-cleanup-binding-" + late.NewSessionID[:12]
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: owner.Phase, Object: object, Dependencies: slices.Clone(owner.Dependencies)})
	}
	// Preserve dependency edges: include their exact retain-only configuration
	// closure in the actual scoped plan rather than stripping dependencies here.
	return definitions, nil
}

// The common producer supplies only the new immutable CM. Original RBAC
// objects come from the accepted16 snapshot, preserving metadata and every
// unrelated field. The Role changes exactly one Secret resourceNames target.
func managedLateSessionCleanupDefinitions(late *createManagedLateSessionRecovery, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("late cleanup requires exact accepted original RBAC and actual new session binding")
	}
	if late == nil || late.AcceptedCore == nil || service.BootstrapSessionID != late.NewSessionID || validateEnvironmentBootstrapCleanupBinding(&service.CleanupBinding, service.Root.Environment) != nil {
		return bad()
	}
	stack := late.AcceptedCore.Stack
	ns := stack + "-video-cloud"
	controller := deploymentplan.Clone(late.Owners["Deployment/"+ns+"/pki-controller"].Desired)
	common, err := applyEnvironmentBootstrapCleanup(map[string]string{"CLOUD_ENV_NAME": service.Root.Environment, "CLOUD_STACK_NAME": stack}, controller, service.CleanupBinding)
	if err != nil || len(common) != 5 || deploymentplan.ID(common[0].Object) != "ConfigMap/"+ns+"/pki-bootstrap-cleanup-binding-"+late.NewSessionID[:12] {
		return bad()
	}
	definitions := []deploymentplan.Definition{common[0]}
	ids := []string{"Role/" + ns + "/bootstrap-cleanup", "RoleBinding/" + ns + "/bootstrap-cleanup", "ClusterRole//" + stack + "-bootstrap-cleanup", "ClusterRoleBinding//" + stack + "-bootstrap-cleanup"}
	for _, id := range ids {
		var source *deploymentplan.Resource
		for index := range late.AcceptedCore.Resources {
			resource := &late.AcceptedCore.Resources[index]
			if resource.ID == id {
				if source != nil {
					return bad()
				}
				source = resource
			}
		}
		if source == nil || late.AcceptedCoreJournal.Owners[id] != source.UID || !environmentBootstrapOwnerUUID.MatchString(source.UID) {
			return bad()
		}
		if late.ExpiredRenewal != nil {
			selected, ok := managedLateRBACSource(late, id)
			if !ok {
				return bad()
			}
			source = &selected
		}
		object := deploymentplan.Clone(source.Desired)
		if id == ids[0] {
			rules := certIssuerObjectList(object["rules"])
			if len(rules) != 1 {
				return bad()
			}
			rule := deploymentplan.Map(rules[0])
			expected := deploymentplan.Object{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"service-bootstrap-" + late.OriginalSession}, "verbs": []any{"get", "delete"}}
			if deploymentplan.Digest(rule) != deploymentplan.Digest(expected) {
				return bad()
			}
			rule["resourceNames"] = []any{"service-bootstrap-" + late.NewSessionID}
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: source.Phase, Object: object, Dependencies: slices.Clone(source.Dependencies)})
	}
	return definitions, nil
}
