package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is an external publication protocol, with independent immutable run and
// artifact identities. Original service-repair publications remain available.
func installHistoricalACKPublication(t *testing.T, f *acceptedCoreExecutionContract) environmentReleaseImage {
	t.Helper()
	const repo = "hkt999rtk/rtk_video_cloud"
	revision := deploymentplan.Digest("isolated-formal-openbao-ack-source")[:40]
	imageManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"layers":[]}`
	digest := "sha256:" + environmentPreflightBytesSHA256([]byte(imageManifest))
	manifest := mustProviderRecoveryJSON(map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": revision, "run_id": "177", "run_attempt": "1", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": "sha256:" + deploymentplan.Digest("isolated-original-app-image"), "images": map[string]any{"openbao_pki": map[string]any{"image": "ghcr.io/" + repo + "/video-cloud-openbao-pki", "digest": digest}}})
	image, err := selectEnvironmentPublishedImage(manifest, repo, 177, 1, revision, "ghcr.io/"+repo+"/video-cloud-openbao-pki", false)
	if err != nil {
		t.Fatal(err)
	}
	selection := createOpenBaoACKImageSelection{Schema: "rtk-cloud.create-openbao-host-ack-selection/v1", Parent: f.e.Journal.PlanSHA256, Role: "LKE_OPENBAO_PKI_IMAGE", Revision: revision, RunID: 177, Attempt: 1, ManifestSHA: environmentPreflightBytesSHA256(manifest)}
	f.write(t, createOpenBaoACKImageSelectionPath, selection)
	if err := f.store.write(filepath.Join("deployment/repairs/openbao-host-ack", revision, "image-manifest.json"), manifest, false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.write("operator/env/GHCR_PULL_TOKEN", []byte("isolated-fake-gh-only"), true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.write("operator/env/GHCR_PULL_USERNAME", []byte("isolated-registry-user"), true); err != nil {
		t.Fatal(err)
	}
	if f.protocol["image_manifests"] == nil {
		f.protocol["image_manifests"] = map[string]string{}
	}
	f.protocol["image_manifests"].(map[string]string)[image.Reference] = imageManifest
	archive := publicationZIP(t, "image-manifest.json", manifest)
	f.protocol["ack_ci_run"] = map[string]any{"id": 177, "head_sha": revision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": 1, "path": ".github/workflows/release.yml"}
	f.protocol["ack_ci_listing"] = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 199, "name": "image-manifest", "expired": false, "size_in_bytes": len(archive)}}}
	f.protocol["ack_ci_archive"] = base64.StdEncoding.EncodeToString(archive)
	f.publish(t)
	digests, err := environmentRuntimeImageDigests(t.Context(), f.store, image.Reference)
	if err != nil || !digests[digest] {
		t.Fatal("formal OpenBao ACK image lacks exact digest-bound OCI metadata", err)
	}
	script := `#!/usr/bin/env python3
import sys,os,json,base64
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
requests={'repos/hkt999rtk/rtk_video_cloud/actions/runs/177':'ack_ci_run','repos/hkt999rtk/rtk_video_cloud/actions/runs/177/artifacts?per_page=100':'ack_ci_listing','repos/hkt999rtk/rtk_video_cloud/actions/artifacts/199/zip':'ack_ci_archive','repos/hkt999rtk/rtk_video_cloud/actions/runs/77':'ci_run','repos/hkt999rtk/rtk_video_cloud/actions/runs/77/artifacts?per_page=100':'ci_listing','repos/hkt999rtk/rtk_video_cloud/actions/artifacts/99/zip':'ci_archive'}
a=sys.argv[1:]
if len(a)!=2 or a[0]!='api' or a[1] not in requests or os.environ.get('GH_TOKEN')!='isolated-fake-gh-only':sys.exit('unqualified isolated formal publication request')
k=requests[a[1]]
if k not in d:sys.exit('selected immutable publication unavailable')
if k.endswith('_archive'):sys.stdout.buffer.write(base64.b64decode(d[k]))
else:print(json.dumps(d[k]))
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return image
}

// The preparer consumes already-validated historical stage models. This narrow
// contract obtains the host from a real accepted seven-resource native stage;
// the full connected history separately exercises the public source loaders.
func newHistoricalACKPreparationExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	defs, err := renderEnvironmentCreateTransportDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, roots, intermediates, f.p.Plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	plan := f.acceptedHistoricalStage(t, "isolated-native-ack-original-host", historicalTransportHostDefinitions(defs, 2))
	var journal deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", plan.SHA256(), "journal.json"), &journal); err != nil {
		t.Fatal(err)
	}
	pod := historicalManagedOpenBaoPod(t, f, true)
	installHistoricalManagedOpenBaoProtocol(t, f)
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod); err != nil {
		t.Fatal(err)
	}
	historical := &createManagedTransportPartialRecovery{PostManaged: &createManagedTransportPostManaged{Plan: plan, Journal: journal}}
	partial := &createManagedApplicationPartialRecovery{Historical: historical, Interrupted: &createManagedApplicationInterruptedForward{Previous: &createManagedApplicationInterruptedForward{Previous: &createManagedApplicationInterruptedForward{}}}}
	r := &createServiceSessionRecovery{Ceremony: f.service, ManagedApplicationPartial: partial}
	installHistoricalACKPublication(t, f)
	return f, r
}

func TestHistoricalOpenBaoACKPreparationBindsOnlyActualOriginalHostAndFormalRole(t *testing.T) {
	f, r := newHistoricalACKPreparationExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	p := f.p
	p.Materials = map[string]string{}
	if err := prepareCreateManagedOpenBaoACK(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal("original native ACK candidate", err)
	}
	ack := r.OpenBaoACK
	if ack == nil || len(ack.Candidate.Resources) != 1 || ack.Candidate.Resources[0].Action != "update" || ack.Candidate.Resources[0].UID != ack.Original.UID || !strings.HasPrefix(ack.Image.Reference, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:") || p.Materials["accepted-service-core-openbao-ack-source"] != deploymentplan.Digest(ack) {
		t.Fatal("qualified ACK changed original host or formal role")
	}
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("qualification wrote native resources")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	for _, mode := range []string{"publication-rerun", "native-owner", "native-body", "malformed-pod-identity", "provider-process"} {
		t.Run(mode, func(t *testing.T) {
			saved := mustProviderRecoveryJSON(f.protocol)
			nativePath := os.Getenv("RTK_CORE_CONTRACT_NATIVE")
			defer func() {
				if err := os.WriteFile(nativePath, saved, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			var changed map[string]any
			if err := json.Unmarshal(saved, &changed); err != nil {
				t.Fatal(err)
			}
			objects := deploymentplan.Map(changed["objects"])
			switch mode {
			case "publication-rerun":
				deploymentplan.Map(changed["ack_ci_run"])["run_attempt"] = 2
			case "native-owner":
				deploymentplan.Map(deploymentplan.Map(objects[ack.Original.ID])["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "native-body":
				deploymentplan.Map(deploymentplan.Map(objects[ack.Original.ID])["spec"])["replicas"] = float64(2)
			case "malformed-pod-identity":
				deploymentplan.Map(deploymentplan.Map(objects["Pod/"+f.p.Plan.Stack+"-secrets/openbao-0"])["metadata"])["uid"] = "foreign-pod"
			case "provider-process":
				deploymentplan.Map(changed["historical_bao_process"])["executable"] = "/bin/foreign"
			}
			if err := os.WriteFile(nativePath, mustProviderRecoveryJSON(changed), 0600); err != nil {
				t.Fatal(err)
			}

			candidate := *r
			candidate.OpenBaoACK = nil
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareCreateManagedOpenBaoACK(t.Context(), f.cfg, f.store, f.e, f.backend, &p, &candidate); err == nil || candidate.OpenBaoACK != nil {
				t.Fatal("changed native/publication source produced executable ACK", mode, err)
			}
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused qualification wrote native owners")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

// The process observer reports the new process only from the current native
// template after the actual conditional host update and original-share unseal.
func installHistoricalACKProcessProtocol(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
if "stat -c '%u %g %a' /run/openbao-pki/private/openbao.pid" in joined and 'for item in /proc/[0-9]*/comm' not in joined:
 with open(os.environ['RTK_HISTORICAL_FOUNDATION_STATE']) as source:state=json.load(source)
 if state['sealed']:sys.exit('original native store remains sealed')
 ns=a[a.index('--namespace')+1]
 host=d['objects']['StatefulSet/'+ns+'/openbao'];spec=host['spec']['template']['spec']
 main=[x for x in spec['containers'] if x['name']=='openbao']
 if len(main)!=1 or '"$$$$" > /run/openbao-pki/private/openbao.pid' not in main[0]['command'][2]:sys.exit('current host has no corrected PID producer')
 process=d['historical_bao_process']
 if process['pid_marker_sha256']!=d['historical_ack_marker']:sys.exit('native PID marker differs')
 print(json.dumps(process))
else:os.execv(PARENT,[PARENT]+a)
`
	script = strings.ReplaceAll(script, "PARENT", strconv.Quote(parent))
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Model only the external restart caused by the actual admitted host update.
// The shared callback exercises the same native producer in the narrow test and
// the complete historical chain, including original-share unseal receipts.
func historicalACKNativeApply(t *testing.T, f *acceptedCoreExecutionContract, runtime environmentCreateBootstrapRuntime) environmentCreateBootstrapApply {
	t.Helper()
	installHistoricalACKProcessProtocol(t, f)
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, defs)
		if strings.HasPrefix(stage, "openbao-host-ack-forward-") {
			return restartHistoricalManagedOpenBaoPod(ctx, t, f, runtime)
		}
		return nil
	}
}

// Model the native StatefulSet controller's OnDelete replacement and the
// operator's original-share unseal after an accepted OpenBao template update.
func restartHistoricalManagedOpenBaoPod(ctx context.Context, t *testing.T, f *acceptedCoreExecutionContract, runtime environmentCreateBootstrapRuntime) error {
	t.Helper()
	pod := historicalManagedOpenBaoPod(t, f, true)
	statePath := os.Getenv("RTK_HISTORICAL_FOUNDATION_STATE")
	var state map[string]any
	if err := readExecutionJSON(statePath, &state); err != nil {
		return err
	}
	state["sealed"] = true
	if err := os.WriteFile(statePath, mustProviderRecoveryJSON(state), 0600); err != nil {
		return err
	}
	old := deploymentplan.Map(f.protocol["historical_bao_process"])
	pid, err := strconv.Atoi(deploymentplan.String(old["pid"]))
	if err != nil {
		return err
	}
	process := deploymentplan.Clone(old)
	process["pid"], process["start_time"] = strconv.Itoa(pid+1), strconv.Itoa(223456+pid)
	process["pid_marker_sha256"] = environmentPreflightBytesSHA256([]byte(strconv.Itoa(pid+1) + "\n"))
	f.protocol["historical_bao_process"], f.protocol["historical_ack_marker"] = process, process["pid_marker_sha256"]
	f.publish(t)
	return unsealEnvironmentCreateOpenBaoRestart(ctx, f.cfg, f.store, runtime, pod)
}

// The short lifetime selects the historical stage contract. It intentionally
// does not claim the separate public preflight's longer current-session margin.
// Every preserved source/candidate/native proof below is produced by its actual
// reader, renderer or executor; the only admission is an explicit test input.
func appendHistoricalManagedApplicationACKAndCore(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery, afterCore func(*testing.T, *deviceTrustExecutionContract, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer)) *createManagedCorePartialRecovery {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	partial := r.ManagedApplicationPartial
	if partial == nil || partial.Interrupted == nil || partial.Interrupted.Previous == nil || partial.Interrupted.Previous.Previous == nil {
		t.Fatal("three original interrupted application sources required")
	}
	installHistoricalACKPublication(t, f)
	p := f.p
	p.Materials = map[string]string{}
	for key, value := range f.p.Materials {
		p.Materials[key] = value
	}
	latest := partial.Interrupted
	for key, value := range map[string]string{"source": latest.SourceSHA, "admission": latest.ArtifactSHA, "plan": latest.Plan.SHA256(), "journal": deploymentplan.Digest(latest.Journal)} {
		p.Materials["accepted-service-core-managed-application-interrupted-"+key] = value
	}
	if err := qualifyManagedApplicationInterruptedLease(t.Context(), f.e, f.backend, r, &p); err != nil {
		t.Fatal("original last interrupted lease", err)
	}
	if latest.LeaseSHA != deploymentplan.Digest(deploymentplan.Object(nil)) {
		t.Fatal("last application interruption must already have released its native lease")
	}
	if err := prepareManagedApplicationPartialCandidate(t.Context(), f.cfg, f.e, f.backend, &p, r); err != nil {
		t.Fatal("actual application completion candidate", err)
	}
	if err := prepareCreateManagedOpenBaoACK(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal("actual ACK candidate", err)
	}
	d := r.ManagedTransportRecovery
	roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "device": d.Device, "app": d.AppRoot, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Intermediate, "app": d.AppIntermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	runtime := f.runtime
	runtime.ServiceSessionRecovery, runtime.Backend = r, f.backend
	servers, err := readManagedTransportServers(t.Context(), f.cfg, runtime, p.Plan.Bootstrap, roots, intermediates)
	if err != nil {
		t.Fatal("original native signed host inventory", err)
	}
	p.Materials["accepted-service-core-managed-application-current-servers"] = servers
	r.Review, _ = f.admitRecovery(t, "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1", p.Materials)
	base := filepath.Join("deployment/create/openbao-recovery", r.Review)
	f.write(t, filepath.Join(base, "source-owner.json"), f.e.owner)
	f.write(t, filepath.Join(base, "openbao-root-trust-plan.json"), r.Candidate)
	f.write(t, filepath.Join(base, "openbao-host-ack-source.json"), r.OpenBaoACK)
	if err := releaseManagedApplicationInterruptedLeaseWithBackend(t.Context(), runtime, historicalNativeLeaseAPI{f.native}); err != nil {
		t.Fatal("original absent lease recheck", err)
	}
	apply := historicalACKNativeApply(t, f, runtime)
	if err := executeCreateManagedOpenBaoACK(t.Context(), f.cfg, runtime, apply); err != nil {
		t.Fatal("actual retained ACK host execution", err)
	}
	if err := executeManagedApplicationPartialForward(t.Context(), runtime, apply); err != nil {
		t.Fatal("actual application completion and alias closure", err)
	}
	publishHistoricalEightInstalledSubjects(t, f)
	if err := publishEnvironmentCreateAppRouting(t.Context(), f.cfg, p.Plan.Bootstrap, runtime, apply); err != nil {
		t.Fatal("actual retained App routing", err)
	}
	runtime.InitialListeners, err = configureCreateBrokerServiceConsumer(p.Plan.Bootstrap, runtime.InitialListeners)
	if err != nil {
		t.Fatal(err)
	}
	core, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), f.cfg, p.Plan.Bootstrap, runtime, r.Ceremony)
	if err != nil {
		t.Fatal(err)
	}
	core, err = applyEnvironmentCreateManagedOpenBaoConsumers(p.Plan.Bootstrap, r.Ceremony, roots, intermediates, core)
	if err != nil {
		t.Fatal(err)
	}
	core, err = configureEnvironmentCreateFinalIssuer(p.Plan.Bootstrap, roots, intermediates, core)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range core {
		if deploymentplan.ID(definition.Object) == "Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller" {
			c := mqttFoundationPrimary(definition.Object)
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_FIXED_DEVICE_ROOT_TRUST": "false"}))
		}
	}
	plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/managed-complete-issuer", core, "Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller")
	if err := validateManagedCorePartialJournal(plan, journal); err != nil {
		t.Fatal("actual final Core interruption", err)
	}
	if afterCore != nil {
		// Core publishes the root-plus-ICA TLS trust before its workloads.
		// Consumers can acknowledge that manifest only after its real write;
		// the earlier application forward still retains the root-only profile.
		afterCore(t, device, roots, intermediates)
	}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	_, _, loaded, err := loadManagedCorePartialInitialSource(f.store, f.e)
	if err != nil || loaded == nil || loaded.Plan.SHA256() != plan.SHA256() {
		for index, entry := range f.e.Journal.Entries {
			if index >= len(f.e.Journal.Entries)-7 || entry.State != "completed" {
				t.Logf("Core checkpoint entry %d: key=%s phase=%s state=%s completed=%t receipt=%t", index, entry.Key, entry.Phase, entry.State, !entry.CompletedAt.IsZero(), len(entry.Receipt) != 0)
			}
		}
		t.Fatal("full original stopped Core source", err)
	}
	r.ManagedCorePartial = loaded
	f.p, f.runtime, f.service = p, runtime, r.Ceremony
	return loaded
}

func assertHistoricalOriginalServerRequests(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil {
		t.Fatal("historical server request inspection requires the authenticated transport source")
	}
	runtime := f.runtime
	runtime.Execution, runtime.Backend, runtime.ServiceSessionRecovery = f.e, f.backend, r
	state, _, err := readManagedSessionState(t.Context(), f.cfg, runtime, r.Ceremony.BootstrapSessionID)
	query := "SELECT COALESCE(json_agg(json_build_object('domain',domain,'request_id',request_id) ORDER BY domain),'[]'::json) FROM pki_server_issuances WHERE environment='" + f.cfg.Environment + "' AND domain IN ('mqtt','openbao_tls')"
	raw, readErr := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, runtime, query)
	var requests []struct {
		Domain  string `json:"domain"`
		Request string `json:"request_id"`
	}
	decodeErr := decodeEnvironmentJSON(raw, &requests)
	if err != nil || readErr != nil || decodeErr != nil || state.Session != r.Ceremony.BootstrapSessionID || len(requests) != 2 || requests[0].Domain != "mqtt" || requests[0].Request != state.Session+":emqx-pki:server" || requests[1].Domain != "openbao_tls" || requests[1].Request != state.Session+":openbao:server" {
		t.Fatalf("two original server requests differ: state=%q ceremony=%q requests=%+v stateErr=%v readErr=%v decodeErr=%v", state.Session, r.Ceremony.BootstrapSessionID, requests, err, readErr, decodeErr)
	}
}

func TestHistoricalCoreSourceRetainsTwoOriginalServerRequestIDs(t *testing.T) {
	hooks := historicalCompleteOriginalSessionHooks(t)
	device, r := newHistoricalManagedApplicationExecutionContract(t, hooks)
	f := device.acceptedCoreExecutionContract
	assertHistoricalOriginalServerRequests(t, f, r)
	appendHistoricalManagedApplicationACKAndCore(t, device, r, publishHistoricalPriorCRLConsumerCaches)
	installManagedCoreNativeAuthenticationProtocol(t, f)
	assertHistoricalOriginalServerRequests(t, f, r)
}

// Native consumers can acknowledge installation only while the exact issued
// session is live and their actual owners reference that session. The stopped
// Log owner deliberately never acknowledges. This changes only the simulated
// native registry response, never the immutable producer observation.
func publishHistoricalEightInstalledSubjects(t *testing.T, f *acceptedCoreExecutionContract, selectedSession ...string) {
	t.Helper()
	session := f.session
	if len(selectedSession) > 1 {
		t.Fatal("native installation requires one explicitly selected session")
	}
	if len(selectedSession) == 1 {
		session = selectedSession[0]
	}
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		t.Fatal("native installation session is invalid")
	}
	states := f.protocol["historical_sessions"].(map[string]managedSessionState)
	state, ok := states[session]
	if !ok || state.Session != session || state.Status != "active" || !state.Expires.After(time.Now()) || len(state.Subjects) != 9 || len(state.Installed) != 0 {
		t.Fatal("native installation requires its actual live original nine-subject session")
	}
	ns := f.p.Plan.Stack + "-video-cloud/"
	owners := map[string]string{"service:account-manager": "Deployment/" + f.p.Plan.Stack + "-account-manager/account-manager", "service:certissuer": "Deployment/" + ns + "certissuer", "service:emqx-pki": "Deployment/" + ns + "mqtt-pki", "service:factory-enroll": "Deployment/" + ns + "factoryenroll", "service:openbao": "StatefulSet/" + f.p.Plan.Stack + "-secrets/openbao", "service:pki-controller": "Deployment/" + ns + "pki-controller", "service:pkibroker": "Deployment/" + ns + "mqtt-pki", "service:video-cloud-api": "Deployment/" + ns + "video-cloud-api"}
	for _, subject := range state.Subjects {
		if subject == "service:video-cloud-logingester" {
			continue
		}
		id, exists := owners[subject]
		owner := f.native.objects[id]
		if !exists || owner == nil || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])) {
			t.Fatal("native installed subject lacks its actual original workload", subject)
		}
		current := false
		for _, group := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(environmentPKIPod(owner)[group]) {
				for _, value := range certIssuerObjectList(deploymentplan.Map(raw)["env"]) {
					setting := deploymentplan.Map(value)
					if strings.Contains(deploymentplan.String(setting["name"]), "BOOTSTRAP_SESSION_ID") && setting["value"] == state.Session {
						current = true
					}
				}
			}
		}
		if !current {
			t.Fatal("native installed consumer has not selected the original current session", subject)
		}
		state.Installed = append(state.Installed, subject)
	}
	if len(state.Installed) != 8 {
		t.Fatal("native consumers did not produce exactly eight original installation acknowledgments")
	}
	states[state.Session] = state
	f.publish(t)
}

// Return the actual accepted restore-only suffix. The elapsed wait observes the
// original signed expiry; it cannot renew a session or authorize finalization.
func newHistoricalAcceptedCoreRestoreExecutionContract(t *testing.T, hooks ...coreExecutionContractHooks) (*deviceTrustExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	device, r := newHistoricalManagedApplicationExecutionContract(t, hooks...)
	f := device.acceptedCoreExecutionContract
	appendHistoricalManagedApplicationACKAndCore(t, device, r, publishHistoricalPriorCRLConsumerCaches)
	installManagedCoreNativeAuthenticationProtocol(t, f)
	t.Logf("original interrupted Core chronology: entries=%d leases=%d Core resources=%d accepted App resources=%d", len(f.e.Journal.Entries), len(f.e.Journal.KubernetesLeases), len(r.ManagedCorePartial.Plan.Resources), len(r.ManagedCorePartial.Apps.Resources))
	state := f.protocol["historical_sessions"].(map[string]managedSessionState)[f.session]
	if remaining := time.Until(state.Expires); remaining > 0 {
		t.Logf("waiting %.1f seconds for the unchanged signed seed expiry", remaining.Seconds())
		timer := time.NewTimer(remaining + 20*time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal("original signed expiry was not observed before test cancellation")
		}
	}
	p := f.p
	p.Materials = map[string]string{}
	current, err := loadManagedCorePartialServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, &p)
	if err != nil || current.ManagedCorePartial.Action != managedCoreRestoreOnly || current.Candidate == nil {
		t.Fatal("full original expired eight-ACK Core qualification", err)
	}
	core := current.ManagedCorePartial
	core.ForwardReview, _ = f.admitRecovery(t, managedCorePartialSchema, p.Materials)
	base := filepath.Join("deployment/create/openbao-recovery", core.ForwardReview)
	f.write(t, filepath.Join(base, "source-owner.json"), f.e.owner)
	f.write(t, filepath.Join(base, managedCorePartialForwardPlanFilename), current.Candidate)
	runtime := f.runtime
	runtime.ServiceSessionRecovery, runtime.Backend = current, f.backend
	if err := executeManagedCorePartialForward(t.Context(), f.cfg, runtime, func(_ context.Context, stage string, definitions []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, definitions)
		return nil
	}); err != nil {
		t.Fatal("actual original Core restore-only forward", err)
	}
	if err := verifyManagedCorePartialCompletedStages(t.Context(), f.cfg, runtime); err != nil {
		t.Fatal("actual accepted Core and routing readback", err)
	}
	if !f.e.Journal.CompletedAt.IsZero() {
		t.Fatal("expired eight-subject restore marked the original deployment complete")
	}
	if !managedCorePartialNativeCheckpoint(f.store, f.e) {
		t.Fatal("accepted Core restore does not authenticate its complete original source")
	}
	// Managed transport and application continuation append genuine issuer
	// reservations after the Device/App trust fixtures first publish their
	// registry view.  The native database advances with those reservations in
	// production, so refresh the connected fixture from the complete journal
	// before later CRL recovery rechecks the exact native registry.
	device.publishTrust(t)
	f.runtime, f.p, f.service = runtime, p, current.Ceremony
	assertHistoricalManagedCoreRestore(t, device, current)
	t.Logf("qualified actual accepted Core restore with expired eight-subject session and all original owners: entries=%d leases=%d accepted Core resources=%d accepted App resources=%d", len(f.e.Journal.Entries), len(f.e.Journal.KubernetesLeases), len(current.Candidate.Resources), len(current.ManagedCorePartial.Apps.Resources))
	return device, current
}

func assertHistoricalManagedCoreRestore(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	f := device.acceptedCoreExecutionContract
	core := r.ManagedCorePartial
	plan, journal, err := managedTransportPostAcceptedStage(f.store, f.e, "kubernetes/managed-complete-issuer-forward-"+core.ForwardReview)
	if err != nil || !managedCoreSameQualifiedPlan(plan, r.Candidate) || len(journal.Owners) != 16 {
		t.Fatal("full Core source lacks its actual accepted sixteen original owners", err)
	}
	for _, resource := range plan.Resources {
		if resource.UID == "" || journal.Owners[resource.ID] != resource.UID || deploymentplan.Map(f.native.objects[resource.ID]["metadata"])["uid"] != resource.UID {
			t.Fatal("Core restoration replaced an original native owner", resource.ID)
		}
	}
	state, _, err := readManagedSessionState(t.Context(), f.cfg, f.runtime, f.session)
	if err != nil || state.Status != "expired" || state.Expires.After(time.Now()) || len(state.Installed) != 8 || slices.Contains(state.Installed, "service:video-cloud-logingester") {
		t.Fatal("restore-only changed the expired original eight-ACK session", err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	if err := verifyManagedCorePartialCompletedStages(t.Context(), f.cfg, f.runtime); err != nil {
		t.Fatal("independent accepted Core readback", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("completed Core verifier wrote native owners")
	}
}

func TestHistoricalOpenBaoACKNativeReplacementPreservesOriginalInitialization(t *testing.T) {
	f, r := newHistoricalACKPreparationExecutionContract(t)
	p := f.p
	p.Materials = map[string]string{}
	if err := prepareCreateManagedOpenBaoACK(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal("original native ACK candidate", err)
	}
	ack := r.OpenBaoACK
	definitions, err := createOpenBaoACKDefinitions(ack)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]string{}
	for _, path := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
		original[path] = readTestFile(t, filepath.Join(f.store.Root, path))
	}
	r.Review = deploymentplan.Digest("isolated-native-ack-replacement")
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	apply := historicalACKNativeApply(t, f, runtime)
	// Reinstalling the process observer keeps its immutable predecessor; it
	// must neither intercept the old /proc scan nor delegate to itself.
	installHistoricalACKProcessProtocol(t, f)
	processBeforeWrite, err := readManagedOpenBaoAuditProcess(t.Context(), f.cfg, f.backend)
	if err != nil || deploymentplan.Digest(processBeforeWrite) != deploymentplan.Digest(ack.SourceProcess) {
		t.Fatal("ACK observer changed the original process before the admitted host write", err)
	}
	if err := apply(t.Context(), "openbao-host-ack-forward-"+r.Review, definitions); err != nil {
		t.Fatal("actual admitted host replacement", err)
	}
	set := f.native.objects[ack.Original.ID]
	pod := f.native.objects["Pod/"+f.p.Plan.Stack+"-secrets/openbao-0"]
	if deploymentplan.Map(set["metadata"])["uid"] != ack.Original.UID || deploymentplan.Map(pod["metadata"])["uid"] == ack.SourcePodUID || validateOpenBaoPodOwner(set, pod) != nil {
		t.Fatal("native ACK did not preserve its controller and replace only its Pod")
	}
	for path, bytes := range original {
		if readTestFile(t, filepath.Join(f.store.Root, path)) != bytes {
			t.Fatal("native ACK replaced original initialization material", path)
		}
	}
	proof, err := readCreateOpenBaoACKProcess(t.Context(), f.cfg, f.backend, ack.SourceProcess)
	if err != nil || proof["pid"] == ack.SourceProcess["pid"] || proof["start_time"] == ack.SourceProcess["start_time"] {
		t.Fatal("new process did not prove its original executable and private marker", err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	for _, mode := range []string{"changed-executable", "changed-process-uid", "changed-marker", "invalid-pid"} {
		t.Run(mode, func(t *testing.T) {
			saved := mustProviderRecoveryJSON(f.protocol)
			defer func() {
				if err := os.WriteFile(os.Getenv("RTK_CORE_CONTRACT_NATIVE"), saved, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			var changed map[string]any
			if err := json.Unmarshal(saved, &changed); err != nil {
				t.Fatal(err)
			}
			process := deploymentplan.Map(changed["historical_bao_process"])
			switch mode {
			case "changed-executable":
				process["executable_sha256"] = deploymentplan.Digest("foreign executable")
			case "changed-process-uid":
				process["uid"] = "0"
			case "changed-marker":
				process["pid_marker_sha256"] = deploymentplan.Digest("foreign marker")
			case "invalid-pid":
				process["pid"] = "1"
				process["pid_marker_sha256"] = environmentPreflightBytesSHA256([]byte(deploymentplan.String(process["pid"]) + "\n"))
				changed["historical_ack_marker"] = process["pid_marker_sha256"]
			}
			if err := os.WriteFile(os.Getenv("RTK_CORE_CONTRACT_NATIVE"), mustProviderRecoveryJSON(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCreateOpenBaoACKProcess(t.Context(), f.cfg, f.backend, ack.SourceProcess); err == nil {
				t.Fatal("changed native process was accepted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused process observation wrote native resources")
			}
		})
	}
}
