package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This adapter models only the native Log process and Kubernetes deletion. The
// actual four child plans, accepted journals, source checks and independent Log
// proof remain production code. It never creates a source or a GO admission.
type historicalLateLogBackend struct {
	historicalAcceptedScopedBackend
	fixture   *acceptedCoreExecutionContract
	late      *createManagedLateSessionRecovery
	job, pod  deploymentplan.Object
	logs      string
	attempted bool
}

func (b *historicalLateLogBackend) publish(t *testing.T) {
	t.Helper()
	objects := historicalNativeInventory(b.objects)
	if b.job != nil {
		objects[deploymentplan.ID(b.job)] = deploymentplan.Clone(b.job)
		objects[deploymentplan.ID(b.pod)] = deploymentplan.Clone(b.pod)
	}
	b.fixture.protocol["objects"] = objects
	if b.logs != "" && b.pod != nil {
		logs, _ := b.fixture.protocol["logs"].(map[string]string)
		if logs == nil {
			logs = map[string]string{}
		}
		logs[deploymentplan.String(deploymentplan.Map(b.pod["metadata"])["name"])] = b.logs
		b.fixture.protocol["logs"] = logs
	}
	b.fixture.publish(t)
}

func (b *historicalLateLogBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action != "retire" {
		return b.normalTailE2EBackend.Write(ctx, action, body, dry)
	}
	if err := b.e.Check(ctx); err != nil {
		return nil, err
	}
	id := deploymentplan.ID(body)
	original := b.objects[id]
	uid := deploymentplan.String(deploymentplan.Map(body["metadata"])["uid"])
	if id != b.late.LogHelperCandidate.Resources[0].ID || body["kind"] != "Job" || !environmentBootstrapOwnerUUID.MatchString(uid) || original == nil || deploymentplan.Map(original["metadata"])["uid"] != uid || !deploymentplan.Matches(b.late.LogHelperCandidate.Resources[0].Desired, original) {
		return nil, errors.New("original Log helper retirement UID or body differs")
	}
	if !dry {
		delete(b.objects, id)
		b.writes[id]++
		if b.job != nil && deploymentplan.Map(b.job["metadata"])["uid"] == uid {
			b.job, b.pod = nil, nil
		}
	}
	return deploymentplan.Clone(body), nil
}

func (b *historicalLateLogBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	if resource.Action == "retire" {
		if err := b.e.Check(ctx); err != nil {
			return err
		}
		if b.objects[resource.ID] != nil || b.job != nil {
			return errors.New("original Log helper deletion is incomplete")
		}
		return nil
	}
	// A helper process receives success only after its native identity/owner
	// checks and actual external ACK below. Generic Job-success inventory is
	// never used as this process's result.
	if resource.ID == b.late.LogHelperCandidate.Resources[0].ID {
		return b.run(ctx, resource)
	}
	return b.normalTailE2EBackend.Wait(ctx, resource)
}

func (b *historicalLateLogBackend) run(ctx context.Context, resource deploymentplan.Resource) (result error) {
	f, late := b.fixture, b.late
	if err := f.e.Check(ctx); err != nil {
		return err
	}
	job := f.native.objects[resource.ID]
	uid := deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"])
	if job == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || !deploymentplan.Matches(late.LogHelperCandidate.Resources[0].Desired, job) {
		return errors.New("native Log helper differs from exact written Job")
	}
	if b.attempted {
		if b.job == nil || b.pod == nil || deploymentplan.Map(b.job["metadata"])["uid"] != uid {
			return errors.New("native Log helper process cannot restart")
		}
		identity, err := readLogACKDraftMarkers([]byte(b.logs))
		sessions, _ := f.protocol["historical_sessions"].(map[string]managedSessionState)
		producer := *late.LogProducer
		producer.FreshReview, producer.NewSession = late.Review, sessions[late.NewSessionID]
		_, proofErr := verifyLogACKDraftCompleted(&producer, uid, deploymentplan.String(deploymentplan.Map(b.pod["metadata"])["uid"]), b.job, []deploymentplan.Object{b.pod}, producer.NewSession, identity, producer.OriginalClientsSHA, identity.StateSHA256, time.Now().UTC())
		if err != nil || proofErr != nil {
			return errors.New("native Log helper previous process was not genuinely complete")
		}
		return nil
	}
	b.attempted = true
	ns := deploymentplan.String(deploymentplan.Map(job["metadata"])["namespace"])
	objects := historicalNativeInventory(f.native.objects)
	helperPod := objects["Pod/"+ns+"/"+deploymentplan.String(deploymentplan.Map(job["metadata"])["name"])+"-pod"]
	if helperPod == nil {
		return errors.New("native Log helper descendant missing")
	}
	b.job, b.pod = deploymentplan.Clone(job), deploymentplan.Clone(helperPod)
	b.job["status"] = deploymentplan.Object{"active": float64(1)}
	b.pod["status"] = deploymentplan.Object{"phase": "Running"}
	defer func() {
		if result == nil {
			return
		}
		b.job["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
		b.pod["status"] = deploymentplan.Object{"phase": "Failed"}
	}()
	original := f.native.objects[late.LogProducer.Original.ID]
	if original == nil || deploymentplan.Map(original["metadata"])["uid"] != late.LogProducer.Original.UID || envObjectReplicas(original) != 0 {
		return errors.New("original Log business process is not quiesced")
	}
	var pods []deploymentplan.Object
	for _, object := range objects {
		if object["kind"] == "Pod" && deploymentplan.Map(object["metadata"])["namespace"] == ns {
			pods = append(pods, object)
		}
	}
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend}
	active, err := managedLateLogClaimActivePods(ctx, runtime, late, pods, ns, "video-cloud-logingester-service-state")
	if err != nil {
		return err
	}
	podUID := deploymentplan.String(deploymentplan.Map(helperPod["metadata"])["uid"])
	if err := managedLateLogClaimHolders(active, ns, "video-cloud-logingester-service-state", podUID); err != nil {
		return err
	}
	identityState, ok := f.protocol["historical_log_identity"].(map[string]string)
	if !ok || identityState["claim_uid"] != late.LogProducer.OriginalClaimUID {
		return errors.New("native original Log claim identity missing")
	}
	identityFile := identityState["path"]
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(identityFile), ".owner"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("native original Log owner lock remains held")
	}
	held := true
	defer func() {
		if held {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
	}()
	started := time.Now().UTC()
	identity, err := historicalLateLogPrivateIdentity(f, identityFile)
	if err != nil || verifyLogACKDraftIdentity(identity, late.LogProducer.Identity) != nil {
		return errors.New("native original Log private identity changed")
	}
	sessions, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
	state, found := sessions[late.NewSessionID]
	if !ok || !found {
		return errors.New("native Log selected session missing")
	}
	producer := *late.LogProducer
	producer.FreshReview, producer.NewSession = late.Review, state
	if err := validateLogACKDraftSource(&producer, producer.NewSession.Subjects, started); err != nil {
		return err
	}
	if slices.Contains(state.Installed, logACKSubject) {
		return errors.New("native Log ACK already existed before helper")
	}
	declared := managedNamedField(environmentPKIPod(job)["containers"], "app")
	if mqttFoundationEnv(declared, "PKI_BOOTSTRAP_SESSION_ID") != state.Session {
		return errors.New("native helper selected a different session")
	}
	// The modeled native process inspects unchanged bytes, releases its actual
	// exclusive file lock, and then publishes its terminal process/ACK result.
	after, err := historicalLateLogPrivateIdentity(f, identityFile)
	if err != nil || deploymentplan.Digest(after) != deploymentplan.Digest(identity) {
		return errors.New("native helper changed original private state")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		return err
	}
	held = false
	finished := time.Now().UTC()
	if !finished.After(started) {
		return errors.New("native helper process did not advance its clock")
	}
	b.job, b.pod = deploymentplan.Clone(job), deploymentplan.Clone(helperPod)
	b.job["status"] = deploymentplan.Object{"succeeded": float64(1), "completionTime": finished.Format(time.RFC3339Nano), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	b.pod["status"] = deploymentplan.Object{"phase": "Succeeded", "containerStatuses": []any{deploymentplan.Object{"name": "app", "restartCount": float64(0), "containerID": "containerd://original-log-helper-" + uid, "imageID": "docker-pullable://" + deploymentplan.String(declared["image"]), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed", "containerID": "containerd://original-log-helper-" + uid, "startedAt": started.Format(time.RFC3339Nano), "finishedAt": finished.Format(time.RFC3339Nano)}}}}}
	deploymentplan.Map(b.pod["metadata"])["labels"] = deploymentplan.Object{"job-name": deploymentplan.Map(job["metadata"])["name"]}
	public, _ := json.Marshal(identity)
	b.logs = "RTK_LOG_ACK_PUBLIC_STATE " + string(public) + "\nRTK_LOG_ACK_OWNER_RELEASED\n"
	state.Installed = append(slices.Clone(state.Installed), logACKSubject)
	slices.Sort(state.Installed)
	sessions[state.Session] = state
	return nil
}

func historicalLateLogPrivateIdentity(f *acceptedCoreExecutionContract, path string) (logACKNativeIdentity, error) {
	var empty logACKNativeIdentity
	raw, err := readBoundedEnvironmentFile(path, true)
	var state struct {
		Version int    `json:"version"`
		Subject string `json:"subject"`
		Current *struct {
			Key   string `json:"private_key_pem"`
			Chain string `json:"certificate_chain_pem"`
		} `json:"current"`
		Pending any `json:"pending"`
	}
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Subject != logACKSubject || state.Current == nil || state.Pending != nil {
		return empty, errors.New("native private Log state unavailable")
	}
	pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
	leaf, leafErr := parseFirstCertificatePEM([]byte(state.Current.Chain))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	now := time.Now()
	if err != nil || leafErr != nil || issuerErr != nil || len(pair.Certificate) != 3 || leaf.CheckSignatureFrom(issuer) != nil || leaf.Subject.CommonName != logACKSubject || leaf.NotBefore.After(now) || !leaf.NotAfter.After(now) || environmentPreflightBytesSHA256(pair.Certificate[2]) != f.service.Root.RootFingerprint {
		return empty, errors.New("native original Log signed identity differs")
	}
	return logACKNativeIdentity{Subject: logACKSubject, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), PublicKeySHA256: environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo), RootSHA256: f.service.Root.RootFingerprint, StateSHA256: environmentPreflightBytesSHA256(raw)}, nil
}

// Wrap the caller's real seed/owner adapter. Only the exact four Log lifecycle
// stages use the deletion/process backend; unrelated stages keep their caller.
func installHistoricalLateLogExecution(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery, base environmentCreateBootstrapApply) environmentCreateBootstrapApply {
	apply, _ := installHistoricalLateLogExecutionBackend(t, device, r, base)
	return apply
}

func installHistoricalLateLogExecutionBackend(t *testing.T, device *deviceTrustExecutionContract, r *createServiceSessionRecovery, base environmentCreateBootstrapApply) (environmentCreateBootstrapApply, *historicalLateLogBackend) {
	t.Helper()
	f, late := device.acceptedCoreExecutionContract, r.ManagedLateSession
	if late == nil || validateManagedLateLogCandidates(late) != nil || base == nil {
		t.Fatal("exact qualified Log lifecycle and base adapter required")
	}
	b := &historicalLateLogBackend{historicalAcceptedScopedBackend: historicalAcceptedScopedBackend{normalTailE2EBackend: f.native, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}, fixture: f, late: late}
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,os,sys
original=%q
a=sys.argv[1:]
if 'get' in a and a[a.index('get')+1]=='pods':
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
 ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 rows=[o for o in d['objects'].values() if o['kind']=='Pod' and o['metadata'].get('namespace')==ns]
 for flag in ('-l','--selector'):
  if flag in a:
   for selector in a[a.index(flag)+1].split(','):
    key,value=selector.split('=',1);rows=[o for o in rows if o['metadata'].get('labels',{}).get(key)==value]
 print(json.dumps({'apiVersion':'v1','kind':'PodList','metadata':{},'items':rows}));sys.exit(0)
os.execv(original,[original]+a)
`, original)
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		kinds := map[string]*deploymentplan.Plan{"stop": late.LogStopCandidate, "existing-ack": late.LogHelperCandidate, "retire": late.LogRetireCandidate, "restore": late.LogRestoreCandidate}
		var preview *deploymentplan.Plan
		for kind, p := range kinds {
			if stage == "managed-late-log-"+kind+"-"+late.Review {
				preview = p
			}
		}
		if preview == nil {
			if err := base(ctx, stage, defs); err != nil {
				return err
			}
			b.publish(t)
			return nil
		}
		var live []deploymentplan.Object
		for _, o := range f.native.objects {
			live = append(live, o)
		}
		p, err := deploymentplan.Compile(preview.Inputs, defs, live)
		if err != nil {
			return err
		}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
		fence := func(ctx context.Context, p *deploymentplan.Plan) error {
			return b.CheckParentExecution(ctx, p, f.store.Root)
		}
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
		err = executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, b, qualify, fence, accept, f.e.Check, io.Discard)
		b.publish(t)
		return err
	}
	return apply, b
}

// Establish only the original native Log owner and signed private identity.
// This helper never publishes a prospective session to the native registry.
func newHistoricalOriginalLogNativeContract(t *testing.T, f *acceptedCoreExecutionContract) (*createLogACKHelperDraft, *createManagedLateSessionRecovery, *deploymentplan.Plan) {
	t.Helper()
	a, late := managedLateLogNativeContractFixture(t)
	certificate, key, csr := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, logACKSubject, nil)
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	if err != nil {
		t.Fatal(err)
	}
	row := managedSessionClient{Subject: logACKSubject, Caller: logACKSubject, Request: "isolated-original-log-client", Issuer: f.service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "succeeded", CSR: csr, Certificate: certificate}
	f.protocol["historical_clients"] = []managedSessionClient{row}
	f.protocol["historical_client_keys"] = map[string]string{row.Request: key}
	ns := f.p.Plan.Stack + "-video-cloud"
	body := deploymentplan.Clone(a.Original.Desired)
	body["apiVersion"] = "apps/v1"
	deploymentplan.Map(body["spec"])["replicas"] = float64(1)
	c := managedNamedField(environmentPKIPod(body)["containers"], "app")
	c["env"] = environmentCreateEnv(map[string]string{"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE": logACKStatePath, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256": f.service.Root.RootFingerprint, "PKI_BOOTSTRAP_SESSION_ID": f.session})
	c["volumeMounts"] = []any{environmentCreateServiceMount("private", "/var/lib/video-cloud-log-service", false)}
	environmentPKIPod(body)["volumes"] = []any{environmentCreateServiceVolume("private", "persistentVolumeClaim", "video-cloud-logingester-service-state")}
	original := f.stage(t, "kubernetes/native-log-original-owner", []deploymentplan.Definition{environmentCreateServiceClaimDefinition(f.cfg, ns, "video-cloud-logingester-service-state"), {Phase: "workload", Object: body}})
	device := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: &postServiceMQTTExecutionContract{acceptedCoreExecutionContract: f}}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	installHistoricalLogIdentityProtocol(t, device)
	identityState := f.protocol["historical_log_identity"].(map[string]string)
	identity, err := historicalLateLogPrivateIdentity(f, identityState["path"])
	if err != nil {
		t.Fatal(err)
	}
	actual := f.native.objects[deploymentplan.ID(body)]
	a.Parent, a.OriginalSession, a.OriginalServiceIssuer = f.e.Journal.PlanSHA256, f.session, f.service.Intermediate.ID
	a.Original = deploymentplan.Resource{ID: deploymentplan.ID(body), UID: deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]), Desired: body}
	a.Identity, a.LeafPEM, a.OriginalPrivateBytesSHA = identity, certificate, identity.StateSHA256
	a.OriginalClaimUID = identityState["claim_uid"]
	a.SourcePodUID = deploymentplan.String(deploymentplan.Map(historicalNativeInventory(f.native.objects)["Pod/"+ns+"/video-cloud-logingester-pod"]["metadata"])["uid"])
	late.ParentSHA, late.OriginalSession, late.LogProducer, late.AcceptedCore = a.Parent, a.OriginalSession, a, original
	late.LogNotDueUntil = leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	return a, late, original
}

// This fixture qualifies only the native four-stage adapter. The current seed
// row and original issuance are real signed native model inputs; it does not
// manufacture or claim a complete late-source admission or public preflight.
func newHistoricalLateLogNativeContract(t *testing.T) (*acceptedCoreExecutionContract, *createManagedLateSessionRecovery, environmentCreateBootstrapApply, *historicalLateLogBackend) {
	t.Helper()
	f := newAcceptedCoreExecutionContract(t)
	a, late, original := newHistoricalOriginalLogNativeContract(t, f)
	actual := f.native.objects[a.Original.ID]
	leaf, err := parseFirstCertificatePEM([]byte(a.LeafPEM))
	if err != nil {
		t.Fatal(err)
	}
	device := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: &postServiceMQTTExecutionContract{acceptedCoreExecutionContract: f}}
	_, state := historicalTransportSeedLeaf(t, f, "ee000001-1111-4111-8111-111111111111")
	for _, subject := range state.Subjects {
		if subject != logACKSubject {
			state.Installed = append(state.Installed, subject)
		}
	}
	a.NewSession = state
	late.ParentSHA, late.OriginalSession, late.NewSessionID, late.LogProducer = a.Parent, a.OriginalSession, state.Session, a
	late.Review, late.AcceptedCore = deploymentplan.Digest("isolated-native-log-lifecycle-review"), original
	late.LogNotDueUntil = leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	a.FreshReview = late.Review
	if err := validateLogACKDraftSource(a, state.Subjects, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	helper, err := renderLogACKDraftHelper(a)
	if err != nil {
		t.Fatal(err)
	}
	late.LogStopCandidate, err = managedLateCompile(f.p.Plan, "managed-late-log-stop", []deploymentplan.Definition{renderLogACKDraftQuiesce(a)}, []deploymentplan.Object{actual})
	if err != nil {
		t.Fatal(err)
	}
	late.LogHelperCandidate, err = managedLateCompile(f.p.Plan, "managed-late-log-existing-ack", []deploymentplan.Definition{helper}, nil)
	if err != nil {
		t.Fatal(err)
	}
	late.LogRetireCandidate = &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: late.LogHelperCandidate.Inputs, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(helper.Object), Phase: "retirement", Action: "retire", Desired: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired), Baseline: deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired)}}}
	late.LogRetireCandidate.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-log-retire", []deploymentplan.Definition{{Phase: "retirement", Object: helper.Object, Retire: true}})
	stopped := deploymentplan.Clone(actual)
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	late.LogRestoreCandidate, err = managedLateCompile(f.p.Plan, "managed-late-log-restore", []deploymentplan.Definition{renderLogACKDraftRestore(a)}, []deploymentplan.Object{stopped})
	if err != nil {
		t.Fatal(err)
	}
	f.protocol["historical_sessions"] = map[string]managedSessionState{state.Session: state}
	apply, backend := installHistoricalLateLogExecutionBackend(t, device, &createServiceSessionRecovery{ManagedLateSession: late}, func(context.Context, string, []deploymentplan.Definition) error {
		return errors.New("unexpected unrelated stage")
	})
	backend.publish(t)
	return f, late, apply, backend
}

func TestHistoricalLateLogNativeLifecyclePreservesIdentityAndFencesRetirement(t *testing.T) {
	for _, mode := range []string{"complete", "held-private-lock", "pending-identity", "foreign-claim-holder", "changed-new-session", "foreign-helper-retirement"} {
		t.Run(mode, func(t *testing.T) {
			f, late, apply, b := newHistoricalLateLogNativeContract(t)
			producer := late.LogProducer
			stage := func(kind string, definition deploymentplan.Definition) error {
				return apply(t.Context(), "managed-late-log-"+kind+"-"+late.Review, []deploymentplan.Definition{definition})
			}
			identityState := f.protocol["historical_log_identity"].(map[string]string)
			privateBefore, err := readBoundedEnvironmentFile(identityState["path"], true)
			if err != nil {
				t.Fatal(err)
			}
			if err := stage("stop", renderLogACKDraftQuiesce(producer)); err != nil {
				t.Fatal("actual stop", err)
			}
			if _, _, err := managedLateLogAccepted(f.store, f.e, late, "stop", ""); err != nil {
				t.Fatal("accepted original stop", err)
			}
			switch mode {
			case "held-private-lock":
				lock, err := os.OpenFile(filepath.Join(filepath.Dir(identityState["path"]), ".owner"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			case "pending-identity":
				var state map[string]any
				if json.Unmarshal(privateBefore, &state) != nil {
					t.Fatal("private state decode")
				}
				state["pending"] = map[string]any{"request_id": "native-in-progress"}
				if err := os.WriteFile(identityState["path"], mustProviderRecoveryJSON(state), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-claim-holder":
				pod := managedLateClaimPodFixture()
				deploymentplan.Map(pod["metadata"])["name"] = "foreign-native-holder"
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["volumes"])[0])["persistentVolumeClaim"])["claimName"] = "video-cloud-logingester-service-state"
				f.native.objects[deploymentplan.ID(pod)] = pod
			case "changed-new-session":
				states := f.protocol["historical_sessions"].(map[string]managedSessionState)
				state := states[late.NewSessionID]
				state.Status = "revoked"
				states[late.NewSessionID] = state
			}
			helper, err := renderLogACKDraftHelper(producer)
			if err != nil {
				t.Fatal(err)
			}
			result := stage("existing-ack", helper)
			positive := mode == "complete" || mode == "foreign-helper-retirement"
			if (result == nil) != positive {
				t.Fatal("actual native helper result", mode, result)
			}
			state := f.protocol["historical_sessions"].(map[string]managedSessionState)[late.NewSessionID]
			if !positive {
				if slices.Contains(state.Installed, logACKSubject) || b.logs != "" || deploymentplan.Map(b.job["status"])["succeeded"] != nil || envObjectReplicas(f.native.objects[producer.Original.ID]) != 0 {
					t.Fatal("failed native process forged ACK/success or restored business owner")
				}
				if b.Wait(t.Context(), late.LogHelperCandidate.Resources[0]) == nil {
					t.Fatal("failed native helper silently restarted")
				}
				return
			}
			identity, err := readLogACKDraftMarkers([]byte(b.logs))
			if err != nil {
				t.Fatal(err)
			}
			observed := *producer
			observed.FreshReview = late.Review
			uid := deploymentplan.String(deploymentplan.Map(b.job["metadata"])["uid"])
			podUID := deploymentplan.String(deploymentplan.Map(b.pod["metadata"])["uid"])
			if _, err := verifyLogACKDraftCompleted(&observed, uid, podUID, b.job, []deploymentplan.Object{b.pod}, state, identity, producer.OriginalClientsSHA, identity.StateSHA256, time.Now().UTC()); err != nil {
				t.Fatal("independent real process proof", err)
			}
			before := deploymentplan.Digest([]any{b.job, b.pod, state, b.logs})
			if err := b.Wait(t.Context(), late.LogHelperCandidate.Resources[0]); err != nil {
				t.Fatal("read-only terminal observation", err)
			}
			if deploymentplan.Digest([]any{b.job, b.pod, f.protocol["historical_sessions"].(map[string]managedSessionState)[late.NewSessionID], b.logs}) != before {
				t.Fatal("recheck repeated process or rewrote lifecycle")
			}
			retirement := deploymentplan.Clone(helper.Object)
			deploymentplan.Map(retirement["metadata"])["uid"] = uid
			if mode == "foreign-helper-retirement" {
				deploymentplan.Map(retirement["metadata"])["uid"] = "aa000001-1111-4111-8111-111111111111"
				if _, err := b.Write(t.Context(), "retire", retirement, false); err == nil {
					t.Fatal("foreign helper UID deleted")
				}
				if f.native.objects[deploymentplan.ID(helper.Object)] == nil || b.job == nil {
					t.Fatal("foreign retire changed original")
				}
				return
			}
			if err := stage("retire", deploymentplan.Definition{Phase: "retirement", Object: retirement, Retire: true}); err != nil {
				t.Fatal("exact helper retirement", err)
			}
			if _, _, err := managedLateLogAccepted(f.store, f.e, late, "retire", uid); err != nil {
				t.Fatal("accepted exact retirement", err)
			}
			if b.job != nil || b.pod != nil || f.native.objects[deploymentplan.ID(helper.Object)] != nil {
				t.Fatal("helper remains after retirement")
			}
			if err := stage("restore", renderLogACKDraftRestore(producer)); err != nil {
				t.Fatal("restore original business owner", err)
			}
			restored := f.native.objects[producer.Original.ID]
			if deploymentplan.Map(restored["metadata"])["uid"] != producer.Original.UID || envObjectReplicas(restored) != 1 || mqttFoundationEnv(managedNamedField(environmentPKIPod(restored)["containers"], "app"), "PKI_BOOTSTRAP_SESSION_ID") != late.NewSessionID {
				t.Fatal("original business owner/SID restoration changed")
			}
			after, err := readBoundedEnvironmentFile(identityState["path"], true)
			if err != nil || string(after) != string(privateBefore) || !managedLateLogExactSet(state.Installed, state.Subjects) {
				t.Fatal("original private identity or exact native nine ACKs changed", err)
			}
		})
	}
}
