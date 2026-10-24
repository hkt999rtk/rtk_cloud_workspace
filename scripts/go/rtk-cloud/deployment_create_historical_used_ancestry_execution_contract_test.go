package main

import (
	"context"
	"crypto/ecdsa"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Historical used seeds are signed with the original public key before any
// immutable observation is recorded. Their later expiry is part of the source
// being replayed. The native current-installation commands and child ownership
// are identical to the maintained used-renewal producer.
func publishHistoricalUsedRenewal(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery, review string, expired, interruptInstallation bool, expiresAt ...time.Time) {
	t.Helper()
	seed := *r
	seed.Candidate = r.RenewalCandidate
	f.publishHistoricalCoreRenewal(t, &seed, review, expired, expiresAt...)
	runtime := f.runtime
	runtime.Execution, runtime.ExecutionSHA256, runtime.Backend, runtime.ServiceSessionRecovery = f.e, f.e.Journal.PlanSHA256, f.backend, r
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = r.GrantImage.Reference
				}
			}
		}
	}
	f.acceptedHistoricalStage(t, "service-managed-used-renewal-"+review, defs)
	// The original installation failure preceded the explicit quiesce repair.
	// The later DNS renewal retains that accepted repair and executes its exact
	// two original owners before reenrolling current identities.
	if r.QuiesceCandidate != nil && !interruptInstallation {
		var stop []deploymentplan.Definition
		for _, v := range r.QuiesceCandidate.Resources {
			stop = append(stop, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
		}
		f.acceptedHistoricalStage(t, "used-installation-owner-quiesce-"+review, stop)
	}
	enroll, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for _, d := range enroll {
		if d.Object["kind"] == "Job" {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			path := mqttFoundationEnv(c, "PKI_CLIENT_IDENTITY_STATE")
			command := certIssuerObjectList(c["command"])
			if path == "" || strings.ContainsAny(path, "'\n\r") || len(command) != 2 {
				t.Fatal("retained native installation command changed")
			}
			subject := deploymentplan.String(command[1])
			if !strings.HasPrefix(subject, "service:") {
				t.Fatal("retained native installation subject changed")
			}
			c["command"] = []any{"/bin/sh", "-ec", "test -s " + path + " && grep -q '\"current\"' " + path + " && ! grep -q '\"pending\"' " + path + " && /app/serviceidentity-bootstrap " + subject}
			if strings.Contains(deploymentplan.ID(d.Object), "/pki-enroll-video-cloud-api-client-") {
				failed = deploymentplan.ID(d.Object)
			}
		}
	}
	stage := "application-current-installation-renewal-" + review
	if interruptInstallation {
		original, journal := f.historicalSerialInterruptedStage(t, "kubernetes/"+stage, enroll, failed)
		if !knownCurrentInstallationFailure(original, journal, f.p.Plan.Stack) {
			t.Fatalf("original installation interruption differs: completed=%d owners=%d failed=%s", len(journal.Completed), len(journal.Owners), journal.Failed)
		}
	} else {
		f.acceptedHistoricalStage(t, stage, enroll)
	}
	f.runtime = runtime
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
}

func newHistoricalUsedMQTTOpenBaoCheckpoint(t *testing.T, setup ...coreExecutionContractHooks) (*postServiceMQTTExecutionContract, *createServiceSessionRecovery, map[string]*ecdsa.PrivateKey) {
	t.Helper()
	hooks := historicalOriginalFoundationHooks(t)
	if len(setup) > 1 {
		t.Fatal("one original pre-source hook composition is required")
	}
	if len(setup) == 1 {
		hooks = setup[0]
	}
	core, identityReview := newHistoricalAcceptedCoreApplicationsWithHooks(t, true, hooks, historicalCompleteDomainPolicy(t))
	core.backend.RolloutTimeout = time.Minute
	core.runtime.Backend = core.backend
	// The original application completion enrolled these final Service clients
	// before either managed transport root was created.
	clients, err := renderEnvironmentCreateTransportClientDefinitions(context.Background(), core.cfg, core.p.Plan.Bootstrap, core.runtime, core.service, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	core.acceptedHistoricalStage(t, "transport-client-enrollment", clients)
	keys := map[string]*ecdsa.PrivateKey{}
	root := core.trustAuthorityWithID(t, "mqtt", 21, "14c1a269-e32e-4eac-aa93-db5ad8e85f7e", false, keys)
	core.installHistoricalAuthorityProtocol(t)
	base := finishHistoricalPostServiceMQTT(t, core, identityReview, &root)
	core.protocol["objects"] = historicalNativeInventory(core.native.objects)
	core.publish(t)
	p := core.p
	p.Materials = map[string]string{}
	before := createBootstrapStateSnapshot(t, core.store.Root)
	r, err := loadAcceptedServiceCore(context.Background(), core.cfg, core.store, core.e, core.backend, core.operator, &p, true, true)
	if err != nil {
		t.Fatal("full expired MQTT source", err)
	}
	assertRecoveryContractReadOnly(t, core.store, before)
	if !r.PostServiceRenewal || r.RenewalCandidate == nil || r.Candidate == nil {
		t.Fatal("full source did not prepare actual used seed renewal")
	}
	core.p = p
	r.Review, _ = core.admitRecovery(t, "rtk-cloud.create-mqtt-trust-order-recovery/v1", p.Materials)
	publishHistoricalUsedRenewal(t, core, r, r.Review, true, false)
	var forward []deploymentplan.Definition
	for _, v := range r.Candidate.Resources {
		forward = append(forward, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	stage := "mqtt-root-trust-order-recovery-" + r.Review
	core.acceptedHistoricalStage(t, stage, forward)
	if err := core.e.RecordReceipt("kubernetes/mqtt-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": base.original.SHA256()})); err != nil {
		t.Fatal(err)
	}
	if err := core.e.CompleteEntry("kubernetes/mqtt-root-trust", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	mqtt, ica := core.activateHistoricalTrustAuthority(t, root, 21, keys)
	core.acceptedHistoricalStage(t, "mqtt-intermediate-trust", historicalMQTTDefinitions(t, core, mqtt, ica))
	bao := core.trustAuthorityWithID(t, "openbao_tls", 22, "734487b8-abcf-40be-a813-8b52c64f7954", false, keys)
	core.runtime.Backend = core.backend
	core.runtime.OpenBao.CACertPath = filepath.Join(core.store.Root, "deployment/create/material", core.e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	core.publish(t)
	original := core.rejectHistoricalOpenBaoName(t, core.service, bao)
	p = core.p
	p.Materials = map[string]string{}
	before = createBootstrapStateSnapshot(t, core.store.Root)
	r, err = loadAcceptedServiceCore(context.Background(), core.cfg, core.store, core.e, core.backend, core.operator, &p, true, true)
	if err != nil {
		t.Fatal("complete used MQTT ancestry at original OpenBao boundary", err)
	}
	assertRecoveryContractReadOnly(t, core.store, before)
	if !r.PostTransportResume || !r.PostServiceRenewal || len(r.UsedRenewalHistory) != 1 || r.RenewalBindingSHA != original.SHA256() || r.RenewalCandidate == nil {
		t.Fatal("OpenBao recovery lost admitted first used renewal and original no-write binding")
	}
	core.p = p
	return base, r, keys
}

// The second used seed reached an authentic current-installation lock failure.
// Only a separately admitted quiesce/unfinished-job forward may complete that
// original attempt, after which the still-unwritten OpenBao name is corrected.
func advanceHistoricalOpenBaoInstallation(t *testing.T, base *postServiceMQTTExecutionContract, r *createServiceSessionRecovery) *createServiceSessionRecovery {
	t.Helper()
	f := base.acceptedCoreExecutionContract
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-openbao-trust-name-recovery/v1", f.p.Materials)
	f.write(t, filepath.Join("deployment/create/openbao-recovery", r.Review, "openbao-root-trust-plan.json"), r.Candidate)
	publishHistoricalUsedRenewal(t, f, r, r.Review, true, true)
	p := f.p
	p.Materials = map[string]string{}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	current, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
	if err != nil {
		t.Fatal("full original current-installation failure source", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if !current.InstallationForward || len(current.UsedRenewalHistory) != 2 || current.QuiesceCandidate == nil || current.InstallationCandidate == nil || len(current.InstallationCandidate.Resources) != 3 {
		t.Fatal("full current-installation recovery lost two original used seeds and unfinished Jobs")
	}
	f.p = p
	current.Review, _ = f.admitRecovery(t, "rtk-cloud.create-openbao-current-installation-recovery/v1", p.Materials)
	runtime := f.runtime
	runtime.Execution, runtime.ExecutionSHA256, runtime.Backend, runtime.ServiceSessionRecovery = f.e, f.e.Journal.PlanSHA256, f.backend, current
	if err := executeCurrentInstallationForward(context.Background(), f.cfg, p.Plan.Bootstrap, f.store, runtime, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, defs)
		return nil
	}); err != nil {
		t.Fatal("exact native owner quiesce and remaining installation", err)
	}
	var corrected []deploymentplan.Definition
	for _, v := range current.Candidate.Resources {
		corrected = append(corrected, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	stage := "openbao-root-trust-name-recovery-" + current.Review
	f.acceptedHistoricalStage(t, stage, corrected)
	if err := f.e.RecordReceipt("kubernetes/openbao-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": p.Materials["accepted-service-core-openbao-trust-original-plan"], "original_ready_root": current.OpenBaoRoot.ID})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/openbao-root-trust", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current.CompletedTransportReview = current.Review
	accepted, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/current-installation-lock-forward-"+current.Review)
	if err != nil || !journal.Accepted || len(accepted.Resources) != 3 {
		t.Fatal("full installation continuation not accepted", err)
	}
	for id, uid := range current.InstallationJournal.Owners {
		if deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != uid {
			t.Fatal("original installation owner changed", id)
		}
	}
	f.runtime = runtime
	return current
}

func newHistoricalDeviceAppExecutionContract(t *testing.T, setup ...coreExecutionContractHooks) (*deviceTrustExecutionContract, *createServiceSessionRecovery, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	base, r, keys := newHistoricalUsedMQTTOpenBaoCheckpoint(t, setup...)
	assertHistoricalAdmittedGeneration(t, base.acceptedCoreExecutionContract, "used-mqtt-openbao-checkpoint")
	current := advanceHistoricalOpenBaoInstallation(t, base, r)
	assertHistoricalAdmittedGeneration(t, base.acceptedCoreExecutionContract, "openbao-installation")
	// Choose the eventual native expiry before the final used seed is signed or
	// any immutable history exists. Device/App qualify while it is still live;
	// the transport test may later observe its genuine expiry without rewrites.
	advanceHistoricalOpenBaoDNS(t, base, current, keys, 3*time.Minute)
	assertHistoricalAdmittedGeneration(t, base.acceptedCoreExecutionContract, "openbao-dns")
	device := newHistoricalDeviceOrderExecutionContract(t, base, keys, true)
	assertHistoricalAdmittedGeneration(t, device.acceptedCoreExecutionContract, "device-order")
	recovery, roots, intermediates := advanceHistoricalDeviceApp(t, device)
	assertHistoricalAdmittedGeneration(t, device.acceptedCoreExecutionContract, "device-app")
	core := device.acceptedCoreExecutionContract
	certificate, err := parseFirstCertificatePEM([]byte(core.certificate))
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := environmentCreateBootstrapSubjects(core.cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := managedSessionState{Session: core.session, Issuer: core.service.Intermediate.ID, Root: core.service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(certificate.Raw), Status: "active", Expires: certificate.NotAfter, Subjects: subjects, Installed: []string{}}
	publishHistoricalDNSInstalledSubjects(t, core, &state)
	states, _ := core.protocol["historical_sessions"].(map[string]managedSessionState)
	if states == nil {
		states = map[string]managedSessionState{}
	}
	states[core.session] = state
	core.protocol["historical_sessions"] = states
	installHistoricalTransportSessionProtocol(t, core, map[string]managedSessionState{core.session: state})
	core.runtime.Execution, core.runtime.ExecutionSHA256, core.runtime.Backend = core.e, core.e.Journal.PlanSHA256, core.backend
	core.runtime.OpenBao.RootTokenPath = filepath.Join(core.store.Root, "openbao/root-token")
	if err := configureEnvironmentCreateCompleteSignerRole(context.Background(), core.cfg, core.runtime, roots, intermediates); err != nil {
		t.Fatal("complete original certissuer signing role", err)
	}
	assertHistoricalAdmittedGeneration(t, core, "complete-signer-role")
	if !isManagedTransportCheckpoint(core.e.Journal) {
		t.Fatal("full source did not reach original managed transport checkpoint")
	}
	installHistoricalLateCRLProtocol(t, device, roots, intermediates)
	return device, recovery, roots, intermediates
}

func TestHistoricalDeviceAppSourceRetainsCompleteSameKeyRenewalAncestry(t *testing.T) {
	device, recovery, roots, intermediates := newHistoricalDeviceAppExecutionContract(t)
	if recovery.AppTrustRecovery == nil || recovery.DeviceTrustRecovery == nil || recovery.CompletedIssuerReview == "" || len(recovery.UsedRenewalHistory) != 3 {
		t.Fatal("complete App source lost earlier genuine used-session history")
	}
	if roots["device"].ID != device.root.ID || intermediates["app"].RootFingerprint != roots["app"].RootFingerprint {
		t.Fatal("complete App source changed original active lineage")
	}
	for id, record := range device.protocol["historical_crls"].(map[string]managedLateCRLRecord) {
		snapshot, _, err := readCreateRootCRLNativeSnapshot(t.Context(), device.cfg, device.e, device.backend, id)
		if err != nil || snapshot.Prior.IssuerID != id || snapshot.Prior.Digest != record.Digest || snapshot.Prior.PEM != record.PEM || len(snapshot.Bindings) != 0 || len(snapshot.Certificates) != 0 {
			t.Fatal("native original CRL publication changed its signed authority", id, err)
		}
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) ORDER BY number),'[]'::json) FROM pki_crls WHERE issuer_id='" + openBaoTLSOriginalIntermediateID + "' AND number>=3"
	raw, err := readEnvironmentCreateRegistryJSON(t.Context(), device.cfg, environmentCreateBootstrapRuntime{Execution: device.e, Backend: device.backend}, query)
	var history []createRootCRLRecord
	expected := device.protocol["historical_crls"].(map[string]managedLateCRLRecord)[openBaoTLSOriginalIntermediateID]
	if err != nil || decodeEnvironmentJSON(raw, &history) != nil || len(history) != 1 || history[0].IssuerID != expected.IssuerID || history[0].Digest != expected.Digest || history[0].Number != "3" {
		t.Fatal("native original OpenBaoTLS ICA history differs from its current signed inventory", err)
	}
}

// Model successful native installation while the final used DNS seed is still
// live. Three runtime controllers and two completed client enrollment Jobs bind
// these acknowledgments to actual accepted owners. The immutable issuance
// observation remains unchanged; later native reads age this session naturally.
func publishHistoricalDNSInstalledSubjects(t *testing.T, f *acceptedCoreExecutionContract, state *managedSessionState) {
	t.Helper()
	if state == nil || state.Session != f.session || state.Status != "active" || !state.Expires.After(time.Now()) || len(state.Subjects) != 9 || len(state.Installed) != 0 {
		t.Fatal("five historical installations require the actual live DNS seed")
	}
	owners := map[string]string{}
	for _, prefix := range []string{"service-managed-used-renewal-", "application-current-installation-renewal-"} {
		found := false
		for index := len(f.e.Journal.Entries) - 1; index >= 0; index-- {
			entry := f.e.Journal.Entries[index]
			if !strings.HasPrefix(entry.Key, "kubernetes/"+prefix) {
				continue
			}
			plan, journal, err := acceptedCreateStage(f.store, f.e, entry.Key)
			if err != nil || !journal.Accepted {
				t.Fatal("original DNS installation stage is not accepted", err)
			}
			for _, resource := range plan.Resources {
				owners[resource.ID] = journal.Owners[resource.ID]
			}
			found = true
			break
		}
		if !found {
			t.Fatal("original DNS installation stage is absent", prefix)
		}
	}
	ns := f.p.Plan.Stack + "-video-cloud/"
	for _, subject := range []string{"account-manager", "certissuer", "pki-controller", "pkibroker", "video-cloud-api"} {
		id := "Deployment/" + ns + subject
		if subject == "account-manager" {
			id = "Deployment/" + f.p.Plan.Stack + "-account-manager/account-manager"
		}
		if subject == "pkibroker" || subject == "video-cloud-api" {
			name := subject
			if subject == "pkibroker" {
				name = "mqtt-pki"
			}
			id = "Job/" + ns + "pki-enroll-" + name + "-client-" + strings.ReplaceAll(state.Session, "-", "")[:12]
		}
		owner := f.native.objects[id]
		if owner == nil || owners[id] == "" || deploymentplan.Map(owner["metadata"])["uid"] != owners[id] || !environmentBootstrapOwnerUUID.MatchString(owners[id]) {
			t.Fatal("historical installed subject lacks its accepted native owner", subject)
		}
		selected := false
		for _, raw := range certIssuerObjectList(environmentPKIPod(owner)["containers"]) {
			container := deploymentplan.Map(raw)
			if owner["kind"] == "Job" {
				for _, part := range certIssuerObjectList(container["command"]) {
					if strings.Contains(deploymentplan.String(part), "/app/serviceidentity-bootstrap service:"+subject) {
						selected = true
					}
				}
			} else {
				for _, rawEnv := range certIssuerObjectList(container["env"]) {
					env := deploymentplan.Map(rawEnv)
					if strings.Contains(deploymentplan.String(env["name"]), "BOOTSTRAP_SESSION_ID") && env["value"] == state.Session {
						selected = true
					}
				}
			}
		}
		if !selected {
			t.Fatal("historical installed owner did not select the original DNS subject/session", subject)
		}
		state.Installed = append(state.Installed, "service:"+subject)
	}
}
