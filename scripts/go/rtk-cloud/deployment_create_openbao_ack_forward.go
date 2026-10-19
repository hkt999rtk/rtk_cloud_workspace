package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createOpenBaoACKRecovery struct {
	Image                                                                        environmentReleaseImage
	SelectionSHA, ManifestSHA, OriginalPlanSHA, OriginalJournalSHA, SourcePodUID string
	Original                                                                     deploymentplan.Resource
	Candidate                                                                    *deploymentplan.Plan
	SourceProcess                                                                deploymentplan.Object
}

func createOpenBaoACKBlock(stage string) error {
	return createProviderRecoveryBlock("original-generation OpenBao installed ACK repair requires exact formal role, original owner/custody and genuine native readback: stage=" + stage)
}

func createOpenBaoACKDefinitions(ack *createOpenBaoACKRecovery) ([]deploymentplan.Definition, error) {
	if ack == nil || !environmentPreflightDigest.MatchString(ack.SelectionSHA) || !environmentPreflightDigest.MatchString(ack.ManifestSHA) || !environmentPreflightDigest.MatchString(ack.OriginalPlanSHA) || !environmentPreflightDigest.MatchString(ack.OriginalJournalSHA) || !environmentBootstrapOwnerUUID.MatchString(ack.SourcePodUID) || ack.Original.ID != "StatefulSet/video-cloud-dev-secrets/openbao" || !environmentBootstrapOwnerUUID.MatchString(ack.Original.UID) || ack.Image.Reference == "" || validateManagedOpenBaoAuditProcess(ack.SourceProcess) != nil {
		return nil, createOpenBaoACKBlock("qualified-source")
	}
	object := deploymentplan.Clone(ack.Original.Desired)
	pod := environmentPKIPod(object)
	init, err := environmentPKINamedFields(pod["initContainers"])
	if err != nil {
		return nil, err
	}
	containers, err := environmentPKINamedFields(pod["containers"])
	if err != nil {
		return nil, err
	}
	install, sidecar, provider := init["openbao-pki-install"], containers["openbao-pki"], containers["openbao"]
	if len(init) != 1 || len(containers) != 2 || install == nil || sidecar == nil || provider == nil || install["image"] != sidecar["image"] || !strings.HasPrefix(deploymentplan.String(sidecar["image"]), "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:") || install["image"] == ack.Image.Reference || pod["shareProcessNamespace"] != true {
		return nil, createOpenBaoACKBlock("original-role-recipe")
	}
	command := certIssuerObjectList(provider["command"])
	if len(command) != 3 {
		return nil, createOpenBaoACKBlock("original-provider-command")
	}
	script, ok := command[2].(string)
	old := `printf '%s\n' "$$" > /run/openbao-pki/private/openbao.pid`
	if !ok || strings.Count(script, old) != 1 {
		return nil, createOpenBaoACKBlock("original-provider-pid-producer")
	}
	command[2] = strings.Replace(script, old, `printf '%s\n' "$$$$" > /run/openbao-pki/private/openbao.pid`, 1)
	provider["command"] = command
	install["image"], sidecar["image"] = ack.Image.Reference, ack.Image.Reference
	return []deploymentplan.Definition{{Phase: "workload", Object: object}}, nil
}

func prepareCreateManagedOpenBaoACK(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	if r == nil || r.ManagedApplicationPartial == nil || r.ManagedApplicationPartial.Interrupted == nil || r.ManagedApplicationPartial.Interrupted.Previous == nil || r.ManagedApplicationPartial.Interrupted.Previous.Previous == nil {
		return nil
	}
	if r.ManagedApplicationPartial.Interrupted.Previous.Previous.Previous != nil || cfg.Environment != "dev" || e.Check(ctx) != nil {
		return createOpenBaoACKBlock("bounded-third-source")
	}
	image, selection, manifest, err := loadCreateOpenBaoACKRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	post := r.ManagedApplicationPartial.Historical.PostManaged
	if post == nil || post.Plan == nil {
		return createOpenBaoACKBlock("original-host-authority")
	}
	ack := &createOpenBaoACKRecovery{Image: image, SelectionSHA: selection, ManifestSHA: manifest, OriginalPlanSHA: post.Plan.SHA256(), OriginalJournalSHA: deploymentplan.Digest(post.Journal)}
	for _, resource := range post.Plan.Resources {
		if resource.ID == "StatefulSet/"+e.Journal.Stack+"-secrets/openbao" {
			ack.Original = resource
		}
	}
	pod, err := b.Read(ctx, "Pod/"+e.Journal.Stack+"-secrets/openbao-0")
	if err != nil || pod == nil {
		return createOpenBaoACKBlock("original-ready-pod")
	}
	ack.SourcePodUID = deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	ack.SourceProcess, err = readManagedOpenBaoAuditProcess(ctx, cfg, b)
	if err != nil {
		return err
	}
	defs, err := createOpenBaoACKDefinitions(ack)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	ack.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("openbao-host-ack-forward", defs)}, defs, live)
	if err != nil {
		return err
	}
	if len(ack.Candidate.Resources) != 1 || ack.Candidate.Resources[0].Action != "update" || ack.Candidate.Resources[0].UID != ack.Original.UID || !deploymentplan.Matches(ack.Original.Desired, ack.Candidate.Resources[0].Baseline) {
		return createOpenBaoACKBlock("one-original-owner-update")
	}
	p.Materials["accepted-service-core-openbao-ack-selection"] = selection
	p.Materials["accepted-service-core-openbao-ack-manifest"] = manifest
	p.Materials["accepted-service-core-openbao-ack-image"] = deploymentplan.Digest(image)
	p.Materials["accepted-service-core-openbao-ack-original-plan"] = ack.OriginalPlanSHA
	p.Materials["accepted-service-core-openbao-ack-original-journal"] = ack.OriginalJournalSHA
	p.Materials["accepted-service-core-openbao-ack-candidate-plan"] = ack.Candidate.SHA256()
	p.Materials["accepted-service-core-openbao-ack-source-pod"] = deploymentplan.Digest(ack.SourcePodUID)
	p.Materials["accepted-service-core-openbao-ack-source-process"] = deploymentplan.Digest(ack.SourceProcess)
	p.Materials["accepted-service-core-openbao-ack-original-release"] = deploymentplan.Digest(p.Plan.Release)
	p.Materials["accepted-service-core-openbao-ack-source"] = deploymentplan.Digest(ack)
	r.OpenBaoACK = ack
	return e.Check(ctx)
}

func verifyCreateManagedOpenBaoACKBootstrapEntry(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.OpenBaoACK == nil || e == nil || e.Check(ctx) != nil {
		return createOpenBaoACKBlock("bounded-native-bootstrap-entry")
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(time.Now().Add(20*time.Minute)) {
		return createOpenBaoACKBlock("twenty-minute-native-bootstrap-limit")
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	var observed createServiceSessionObserved
	var saved managedSessionState
	path := filepath.Join(e.Store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.ManagedTransportRecovery.Partial.SourceReview, "observed.json")
	if err != nil || readExecutionJSON(path, &observed) != nil || json.Unmarshal(observed.Session, &saved) != nil || !managedApplicationSessionCurrentWithLifetime(state, saved, r.Ceremony, subjects, time.Now().UTC(), managedApplicationTrustACKSessionLifetime) || !state.Expires.After(deadline.Add(5*time.Minute)) {
		return createOpenBaoACKBlock("original-live-session-and-native-phase-margin")
	}
	return e.Check(ctx)
}

func executeCreateManagedOpenBaoACK(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.OpenBaoACK == nil {
		return nil
	}
	ack := r.OpenBaoACK
	e := runtime.Execution
	if e == nil || e.Check(ctx) != nil {
		return createOpenBaoACKBlock("exclusive-owner")
	}
	admitted, _, err := loadAdmittedCreateRecovery(e.Store, e, r.Review)
	if err != nil || admitted.Plan.Schema != "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1" || admitted.Plan.Materials["accepted-service-core-openbao-ack-candidate-plan"] != ack.Candidate.SHA256() {
		return createOpenBaoACKBlock("actual-go-admission")
	}
	if _, _, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, e.Store, e, runtime.Backend, r, false); err != nil {
		return err
	}
	process, err := readManagedOpenBaoAuditProcess(ctx, cfg, runtime.Backend)
	if err != nil || deploymentplan.Digest(process) != deploymentplan.Digest(ack.SourceProcess) {
		return createOpenBaoACKBlock("unchanged-original-provider")
	}
	defs, err := createOpenBaoACKDefinitions(ack)
	if err != nil {
		return err
	}
	live, err := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	fresh, err := deploymentplan.Compile(ack.Candidate.Inputs, defs, live)
	if err != nil {
		return err
	}
	if fresh.SHA256() != ack.Candidate.SHA256() {
		return createOpenBaoACKBlock("unchanged-original-baseline")
	}
	if err := apply(ctx, "openbao-host-ack-forward-"+r.Review, defs); err != nil {
		return err
	}
	_, err = verifyCreateManagedOpenBaoACKNative(ctx, cfg, runtime)
	return err
}

func verifyCreateManagedOpenBaoACKNative(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) (deploymentplan.Object, error) {
	r, e, b := runtime.ServiceSessionRecovery, runtime.Execution, runtime.Backend
	if r == nil || r.OpenBaoACK == nil || e == nil || e.Check(ctx) != nil {
		return nil, createOpenBaoACKBlock("native-original-owner")
	}
	ack := r.OpenBaoACK
	roleReview := managedLateOriginalACKReview(r)
	plan, journal, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/openbao-host-ack-forward-"+roleReview)
	if err != nil || len(plan.Resources) != 1 || len(journal.Completed) != 1 || len(journal.Owners) != 1 || plan.Resources[0].ID != ack.Original.ID || journal.Owners[ack.Original.ID] != ack.Original.UID {
		return nil, createOpenBaoACKBlock("accepted-role-child")
	}
	normalized := *plan
	normalized.BindingsSHA256 = ack.Candidate.BindingsSHA256
	if normalized.SHA256() != ack.Candidate.SHA256() {
		return nil, createOpenBaoACKBlock("exact-qualified-role-child")
	}
	desired, lateProof, err := managedLateOpenBaoCurrentDesired(ctx, cfg, runtime, plan.Resources[0])
	if err != nil {
		return nil, err
	}
	set, err := b.Read(ctx, ack.Original.ID)
	if err != nil || set == nil || deploymentplan.Map(set["metadata"])["uid"] != ack.Original.UID || !deploymentplan.Matches(desired, set) {
		return nil, createOpenBaoACKBlock("native-statefulset")
	}
	ns := e.Journal.Stack + "-secrets"
	pod, err := b.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil || pod == nil || validateOpenBaoPodOwner(set, pod) != nil || !managedOpenBaoPodMatches(desired, pod) {
		return nil, createOpenBaoACKBlock("native-owned-replacement")
	}
	uid := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	revision := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"])
	if uid == ack.SourcePodUID || !environmentBootstrapOwnerUUID.MatchString(uid) || revision == "" || revision != deploymentplan.Map(set["status"])["updateRevision"] || deploymentplan.Map(set["status"])["observedGeneration"] != deploymentplan.Map(set["metadata"])["generation"] {
		return nil, createOpenBaoACKBlock("native-new-revision")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(e.Store.Root, "openbao/init.json"), true)
	if err != nil {
		return nil, err
	}
	share, err := e.Store.read("openbao/unseal-key")
	if err != nil {
		return nil, err
	}
	unsealIntent := deploymentplan.Object{"pod_uid": uid, "controller_uid": ack.Original.UID, "revision": revision, "original_init_sha256": environmentPreflightBytesSHA256(raw), "unseal_sha256": environmentPreflightBytesSHA256([]byte(share))}
	unsealReceipt := deploymentplan.Object{"pod_uid": uid, "revision": revision, "initialized": true, "sealed": false}
	count := 0
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-managed-unseal-"+uid {
			count++
			var got deploymentplan.Object
			if entry.Phase != "openbao-unseal" || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(unsealIntent) || decodeEnvironmentJSON(entry.Receipt, &got) != nil || deploymentplan.Digest(got) != deploymentplan.Digest(unsealReceipt) {
				return nil, createOpenBaoACKBlock("genuine-original-share-unseal")
			}
		}
	}
	if count != 1 {
		return nil, createOpenBaoACKBlock("genuine-original-share-unseal")
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, b)
	if err != nil || !status.Initialized || status.Sealed {
		return nil, createOpenBaoACKBlock("retained-initialized-store")
	}
	root, err := e.Store.read("openbao/root-token")
	if err != nil {
		return nil, err
	}
	audit, err := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
	if err != nil {
		return nil, err
	}
	var devices deploymentplan.Object
	if decodeEnvironmentJSON(audit, &devices) != nil || len(devices) != 1 {
		return nil, createOpenBaoACKBlock("original-audit")
	}
	d := deploymentplan.Map(devices["deployment-audit/"])
	options := deploymentplan.Map(d["options"])
	if d["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
		return nil, createOpenBaoACKBlock("original-audit")
	}
	auditBound := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-audit-enable" {
			var proof map[string]string
			if entry.State == "completed" && decodeEnvironmentJSON(entry.Receipt, &proof) == nil && proof["audit_sha256"] == environmentPreflightBytesSHA256(audit) {
				auditBound = true
			}
		}
	}
	if !auditBound {
		return nil, createOpenBaoACKBlock("original-audit-proof")
	}
	process, err := readCreateOpenBaoACKProcess(ctx, cfg, b, ack.SourceProcess)
	if err != nil {
		return nil, err
	}
	auth := *r
	auth.Intent = r.RenewedIntent
	if err := verifyServiceSessionAuthentication(ctx, cfg, e.Store, e, b, &auth); err != nil {
		return nil, err
	}
	proof := deploymentplan.Object{"parent": e.Journal.PlanSHA256, "review": roleReview, "session_id": r.Ceremony.BootstrapSessionID, "statefulset_uid": ack.Original.UID, "pod_uid": uid, "child_plan_sha256": plan.SHA256(), "process_sha256": deploymentplan.Digest(process), "original_init_sha256": environmentPreflightBytesSHA256(raw), "audit_sha256": environmentPreflightBytesSHA256(audit)}
	for key, value := range lateProof {
		proof[key] = value
	}
	return proof, e.Check(ctx)
}

func readCreateOpenBaoACKProcess(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, original deploymentplan.Object) (deploymentplan.Object, error) {
	script := `set -eu
[ "$(id -u)" = 100 ]
[ "$(stat -c '%u %g %a' /run/openbao-pki/private/openbao.pid)" = '100 1000 600' ]
IFS= read -r pid < /run/openbao-pki/private/openbao.pid
case "$pid" in ''|*[!0-9]*) exit 65 ;; esac
[ "$pid" -gt 1 ]
proc=/proc/$pid
[ "$(readlink "$proc/exe")" = /bin/bao ]
[ "$(cat "$proc/comm")" = bao ]
[ "$(tr '\000' '\n' < "$proc/cmdline")" = "$(printf 'bao\nserver\n-config=/openbao/config/file.hcl')" ]
[ "$(awk '/^Uid:/ {print $2,$3,$4,$5}' "$proc/status")" = '100 100 100 100' ]
start=$(awk '{print $22}' "$proc/stat")
exe_sha=$(sha256sum "$proc/exe" | awk '{print $1}')
printf '{"pid":"%s","start_time":"%s","executable_sha256":"%s","executable":"/bin/bao","uid":"100","pid_marker_sha256":"%s"}\n' "$pid" "$start" "$exe_sha" "$(sha256sum /run/openbao-pki/private/openbao.pid | awk '{print $1}')"
`
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script)
	var proof deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(raw, &proof) != nil || len(proof) != 6 || proof["executable"] != "/bin/bao" || proof["uid"] != "100" || proof["executable_sha256"] != original["executable_sha256"] {
		return nil, createOpenBaoACKBlock("native-provider-pid")
	}
	for _, field := range []string{"pid", "start_time"} {
		s := deploymentplan.String(proof[field])
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != s || n == 0 || field == "pid" && (n <= 1 || n > 1<<31-1) {
			return nil, createOpenBaoACKBlock("native-provider-pid")
		}
	}
	if proof["pid_marker_sha256"] != environmentPreflightBytesSHA256([]byte(deploymentplan.String(proof["pid"])+"\n")) {
		return nil, createOpenBaoACKBlock("native-provider-pid-marker")
	}
	return proof, nil
}

func completeCreateManagedOpenBaoACK(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, p environmentCreateBootstrapPlan, release environmentRelease) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.OpenBaoACK == nil {
		return nil
	}
	e := runtime.Execution
	proof, err := verifyCreateManagedOpenBaoACKNative(ctx, cfg, runtime)
	if err != nil {
		return err
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	if r.ManagedLateSession != nil {
		late := r.ManagedLateSession
		if err != nil || !managedApplicationSessionCurrentWithLifetime(state, state, r.Ceremony, subjects, time.Now().UTC(), managedApplicationFinalizationSessionLifetime) {
			return createOpenBaoACKBlock("genuine-nine-new-session-acks")
		}
		if err := verifyManagedLateSessionRuntimeSource(ctx, cfg, e.Store, runtime, late); err != nil {
			return err
		}
		if err := verifyManagedLateAcceptedOwners(ctx, cfg, runtime); err != nil {
			return err
		}
		log, logErr := readManagedLateLogCompletion(e.Store, e, late, time.Now().UTC())
		if logErr != nil || log == nil || !managedLateLogSessionHashesMatch(raw, log.NativeSessionSHA) {
			return createOpenBaoACKBlock("actual-exclusive-log-new-session-acks")
		}
		proof["late_helper_proof_sha256"] = deploymentplan.Digest(log)
		a, _, loadErr := loadAdmittedCreateRecovery(e.Store, e, late.Review)
		if loadErr != nil {
			return loadErr
		}
		for field, material := range map[string]string{"late_api_servers_sha256": "accepted-service-core-managed-application-current-api-servers", "late_ca_registry_sha256": "accepted-service-core-managed-active-registry", "late_all_volumes_sha256": "accepted-service-core-managed-complete-current-native-volumes"} {
			value := a.Plan.Materials[material]
			if !environmentPreflightDigest.MatchString(value) {
				return createOpenBaoACKBlock("late-qualified-native-pins")
			}
			proof[field] = value
		}
	} else if r.ManagedCorePartial != nil {
		if err != nil {
			return err
		}
		admitted, source, loadErr := loadAdmittedCreateRecovery(e.Store, e, r.ManagedCorePartial.ForwardReview)
		if loadErr != nil || r.ManagedCorePartial.Action == managedCoreRestoreOnly || admitted.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest("finalize-complete-original-session") || admitted.Plan.Schema != managedCorePartialSchema || managedCorePartialReview(source) != r.Review || !managedCorePartialNativeCheckpoint(e.Store, e) {
			return managedCorePartialBlock("actual-finalization-admission")
		}
		var original createServiceSessionObserved
		var saved managedSessionState
		path := filepath.Join(e.Store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.ManagedTransportRecovery.Partial.SourceReview, "observed.json")
		if readExecutionJSON(path, &original) != nil || json.Unmarshal(original.Session, &saved) != nil {
			return managedCorePartialBlock("original-nine-ack-session-source")
		}
		descriptor, descriptorErr := managedCoreSessionComplete(state, saved, r.Ceremony, subjects, time.Now().UTC())
		if descriptorErr != nil || admitted.Plan.Materials["accepted-service-core-managed-complete-current-session"] != deploymentplan.Digest(descriptor) {
			return managedCorePartialBlock("qualified-complete-original-session")
		}
		if err := verifyManagedCorePartialCompletedStages(ctx, cfg, runtime); err != nil {
			return err
		}
	} else if err != nil || !managedApplicationSessionCurrentWithLifetime(state, state, r.Ceremony, subjects, time.Now().UTC(), managedApplicationFinalizationSessionLifetime) || len(state.Installed) != len(subjects) {
		return createOpenBaoACKBlock("genuine-nine-original-session-acks")
	}
	installed := slices.Clone(state.Installed)
	slices.Sort(installed)
	if !slices.Equal(installed, subjects) {
		return createOpenBaoACKBlock("genuine-nine-original-session-acks")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil || clients != r.ManagedTransportRecovery.Partial.ClientsSHA {
		return createOpenBaoACKBlock("unchanged-original-eleven-clients")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	servers, err := readManagedTransportServers(ctx, cfg, runtime, p, roots, issuers)
	if err != nil || servers != r.ManagedTransportRecovery.Partial.ServerSHA {
		return createOpenBaoACKBlock("unchanged-original-host-servers")
	}
	proof["subjects"], proof["installed_subjects"] = subjects, installed
	proof["session_sha256"], proof["clients_sha256"], proof["servers_sha256"] = environmentPreflightBytesSHA256(raw), clients, servers
	admitted, _, err := loadAdmittedCreateRecovery(e.Store, e, r.Review)
	if err != nil {
		return err
	}
	observed := admitted.Plan
	observed.Materials = map[string]string{}
	for name, value := range admitted.Plan.Materials {
		observed.Materials[name] = value
	}
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, runtime.Backend, r, &observed); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, runtime.Backend, r, observed.Materials); err != nil {
		return err
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, e.Store, e, runtime.Backend, &observed); err != nil {
		return err
	}
	volumes, err := managedLateOriginalVolumePins(observed.Materials)
	if err != nil {
		return err
	}
	proof["volume_sha256"] = deploymentplan.Digest(volumes)
	roleReview := managedLateOriginalACKReview(r)
	key := "openbao-host-ack-verified-" + roleReview
	body := mustProviderRecoveryJSON(proof)
	if err := e.BeginEntry(key, "openbao-host-ack-verification", body, time.Now().UTC()); err != nil {
		return err
	}
	if err := e.RecordReceipt(key, body); err != nil {
		return err
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		return err
	}
	return publishCreateOpenBaoACKCompletion(ctx, cfg, e.Store, e.Journal.PlanSHA256, roleReview, release, r.OpenBaoACK, proof)
}
