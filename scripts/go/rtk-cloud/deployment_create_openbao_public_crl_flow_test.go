package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the original public observations are isolated. The ordinary admission,
// source/native child journals, real kubectl adapter, Job/Pod/image acceptance,
// lease and private qualification files are the production implementations.
func TestOpenBaoPublicCRLProducerOrdinaryBoundaryAndRecordedJobResume(t *testing.T) {
	base := newNativeCreateStageContract(t, "normal", func(path string) {
		writeTestFile(t, path, readTestFile(t, path)+"\nPKI_STATE_BACKEND=openbao\nPKI_STATE_KV_MOUNT=identities\nPKI_STATE_LEASE_DURATION=30s\n")
	})
	cfg := base.cfg
	operator, err := base.store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	_, p, recipeRuntime, service := createServiceRecipeFixture(t, "dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	for key, value := range operator {
		if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
		if err := store.write("runtime/"+name, []byte("isolated-original-"+name), false); err != nil {
			t.Fatal(err)
		}
	}
	recipeRuntime.Backend.Store = store
	p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parent := base.parent
	_, _, parent.Release = completeEnvironmentRenderFixture("dev")
	parent.Release.Schema, parent.Release.Environment, parent.Release.Stack, parent.Release.WorkspaceRevision = "rtk-cloud.release/v1", cfg.Environment, parent.Stack, strings.Repeat("a", 40)
	for role, image := range parent.Release.Images {
		if strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/") {
			image.SourceRevision = strings.Repeat("b", 40)
			image.RetainedPlanSHA256 = ""
			image.OTARegistration = nil // This native public-cache boundary has no OTA registration producer.
			parent.Release.Images[role] = image
		}
	}
	parent.Bootstrap = p
	parent.ConfigurationSHA256 = deploymentplan.Digest(cfg)
	parent.OperatorSHA256 = deploymentplan.Digest(operator)
	proofs := coreContractPublicationProofs(t, &parent.Release)
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parentSHA, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(parent))
	if err != nil {
		t.Fatal(err)
	}
	qualification := deploymentplan.Qualification{PlanSHA256: parentSHA, Decision: "GO"}
	for _, id := range environmentCreateRequiredGates {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest("isolated native boundary " + id)})
	}
	at := time.Now().UTC().Add(-time.Second)
	for i := range proofs {
		proofs[i].ObservedAt = at.Add(-time.Second)
	}
	if err := validateEnvironmentPreflightReleaseProofs(environmentPreflightProof{Environment: cfg.Environment, Stack: parent.Stack, QualifiedAt: at, ReleaseProofs: proofs}, parent.Release); err != nil {
		t.Fatal("ordinary fixture formal release", err)
	}
	a, err := publishEnvironmentCreateArtifact(t.Context(), cfg, generation, mustProviderRecoveryJSON(parent), parent.OperatorSHA256, qualification, proofs, at)
	if err != nil {
		t.Fatal(err)
	}
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, parent.OperatorSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	root, intermediate := service.Root, service.Intermediate
	root.Domain, intermediate.Domain = "openbao_tls", "openbao_tls"
	root.Document, intermediate.Document = deploymentplan.Clone(root.Document), deploymentplan.Clone(intermediate.Document)
	root.Document["trust_domain"], intermediate.Document["trust_domain"] = "openbao_tls", "openbao_tls"
	core, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, parent.Bootstrap, recipeRuntime, service)
	if err != nil {
		t.Fatal(err)
	}
	core, err = applyEnvironmentCreateManagedOpenBaoConsumers(parent.Bootstrap, service, map[string]environmentCreateRegisteredIssuer{"openbao_tls": root}, map[string]environmentCreateRegisteredIssuer{"openbao_tls": intermediate}, core)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, parent.Bootstrap, e.Journal.PlanSHA256, root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	objects := map[string]deploymentplan.Object{}
	for i, ns := range lkeNamespaces(environmentCreateProviderValues(cfg)) {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns.Name, "uid": fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1), "labels": deploymentplan.Object{"kubernetes.io/metadata.name": ns.Name}}}
		objects[deploymentplan.ID(body)] = body
	}
	native, dir, _ := unifiedSessionNativeBackend(t, store, objects, nil)
	native.RolloutTimeout = time.Second
	protocolPath := filepath.Join(dir, "unified-kubectl.py")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.ReplaceAll(string(protocol), "'fixture-' + uuid.uuid4().hex", "str(uuid.uuid4())")
	code = strings.Replace(code, "if kind == 'namespace': items", "if kind == 'persistentvolumes': items = []\n            elif kind == 'namespace': items", 1)
	code = strings.Replace(code, "if os.path.exists(os.path.join(root, 'fail-ready')): fail('not ready')", `if os.path.exists(os.path.join(root, 'fail-ready')): fail('not ready')
        jobs = [v for v in a if v.startswith('Job/')]
        if jobs:
            ns=a[a.index('--namespace')+1]; name=jobs[0].split('/',1)[1]; file=path('Job',ns,name); job=load(file)
            if any(c.get('type') == 'Complete' and c.get('status') == 'True' for c in job.get('status',{}).get('conditions',[])) and load(path('Pod',ns,name+'-pod')) is not None: sys.exit(0)
            import datetime
            now=datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
            job['status']={'conditions':[{'type':'Complete','status':'True'}], 'startTime':now,'completionTime':now,'succeeded':1}
            save(file,job)
            spec=job['spec']['template']['spec']; statuses=[]
            for c in spec['containers']:
                statuses.append({'name':c['name'],'image':c['image'],'imageID':'docker-pullable://'+c['image'],'containerID':'containerd://isolated-'+c['name'],'ready':False,'started':False,'restartCount':0,'state':{'terminated':{'exitCode':0,'reason':'Completed','containerID':'containerd://isolated-'+c['name'],'startedAt':now,'finishedAt':now}}})
            pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':name+'-pod','namespace':ns,'uid':str(uuid.uuid4()),'labels':job['spec']['template'].get('metadata',{}).get('labels',{}),'ownerReferences':[{'apiVersion':'batch/v1','kind':'Job','name':name,'uid':job['metadata']['uid'],'controller':True}]},'spec':spec,'status':{'phase':'Succeeded','containerStatuses':statuses}}
            pod['status']['initContainerStatuses']=[{'name':c['name'],'image':c['image'],'imageID':'docker-pullable://'+c['image'],'containerID':'containerd://isolated-'+c['name'],'ready':False,'started':False,'restartCount':0,'state':{'terminated':{'exitCode':0,'reason':'Completed','containerID':'containerd://isolated-'+c['name'],'startedAt':now,'finishedAt':now}}} for c in spec.get('initContainers',[])]
            save(path('Pod',ns,name+'-pod'),pod)`, 1)
	if err := os.WriteFile(protocolPath, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	// Establish real accepted original source owners without enabling the new
	// provider consumer manifests before their producer has run.
	var prior []deploymentplan.Definition
	for _, d := range core {
		if d.Object["kind"] == "Deployment" && (deploymentplan.Map(d.Object["metadata"])["name"] == "certissuer" || deploymentplan.Map(d.Object["metadata"])["name"] == "pki-controller") {
			d.Object = deploymentplan.Clone(d.Object)
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			c["env"] = environmentCreateServiceWithoutEnv(c["env"], "OPENBAO_SERVER_CRL_MANIFEST", "OPENBAO_SERVER_PKI_ROOT_SHA256")
			prior = append(prior, d)
		}
	}
	scope := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": parent.Stack + "-video-cloud", "name": "original-custody-scope"}, "data": deploymentplan.Object{"binding_sha256": deploymentplan.Digest(binding), "public_lineage": strings.Repeat("original-public-lineage", 16000)}}
	prior = append(prior, deploymentplan.Definition{Object: scope, Phase: "configuration"})
	runStage := func(stage string, defs []deploymentplan.Definition) *deploymentplan.Plan {
		t.Helper()
		live, err := native.Inventory(t.Context(), parent.Stack)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live)
		if err != nil {
			t.Fatal(err)
		}
		check := func(ctx context.Context, p *deploymentplan.Plan) error { return e.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, backend, check, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(stage, err)
		}
		return plan
	}
	runStage("fixture-original-core", prior)
	observedScope, err := native.Read(t.Context(), deploymentplan.ID(scope))
	if err != nil || observedScope == nil {
		t.Fatal("original accepted custody scope", err)
	}
	if err := e.BeginEntry("fixture-public-lineage", "bootstrap", mustProviderRecoveryJSON(scope), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt("fixture-public-lineage", mustProviderRecoveryJSON(observedScope)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("fixture-public-lineage", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:` + strings.Repeat("c", 64) + `"},"layers":[]}`)
	image := environmentReleaseImage{Reference: fmt.Sprintf("ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:%x", sha256.Sum256(manifest)), SourceRevision: strings.Repeat("b", 40)}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
	publication := coreContractPublicationProofs(t, &release)[0]
	caps, _ := canonicalFailedForwardCapabilities([]byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2","initial-openbao-public-crl-custody-v1"]}`))
	archive := publicationZIP(t, "image-manifest.json", publication.Manifest)
	command := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			return caps, nil
		}
		target := args[len(args)-1]
		if strings.HasSuffix(target, fmt.Sprintf("/runs/%d", publication.RunID)) {
			return []byte(fmt.Sprintf(`{"id":%d,"head_sha":%q,"head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":%d}`, publication.RunID, image.SourceRevision, publication.RunAttempt)), nil
		}
		if strings.Contains(target, "/artifacts?per_page") {
			return []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":%d,"name":"image-manifest","size_in_bytes":%d,"expired":false}]}`, publication.ArtifactID, len(archive))), nil
		}
		return archive, nil
	}
	capture := newEnvironmentReleaseProofCapture(t.Context())
	if _, err := verifyEnvironmentPublication(capture, publication.Repository, publication.RunID, image, false, command); err != nil {
		t.Fatal(err)
	}
	publication = snapshotVerifiedReleaseProofs(capture, release)[0]
	docker := filepath.Join(dir, "docker")
	meta := filepath.Join(dir, "oci.json")
	if err := os.WriteFile(meta, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nif test \"$1\" = info; then printf '[{\"Name\":\"buildx\",\"Path\":\"/isolated/docker-buildx\"}]'; else cat \"$RTK_PUBLIC_CRL_OCI\"; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_PUBLIC_CRL_OCI", meta)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	records := []createRootCRLRecord{{IssuerID: root.ID, Digest: strings.Repeat("a", 64)}, {IssuerID: intermediate.ID, Digest: strings.Repeat("b", 64)}}
	reads := openBaoPublicCRLObservations{tool: func(context.Context, deploymentConfig, secretStore, *environmentCreateExecution, *environmentCreatePlan) (failedForwardTool, error) {
		return failedForwardTool{Image: image, Publication: publication, Capabilities: caps}, nil
	}, custody: func(_ deploymentConfig, _ secretStore, _ string, _ ...*environmentCreateExecution) (*environmentCustodyBinding, error) {
		if _, _, err := managedTransportPostAcceptedStage(store, e, "kubernetes/fixture-original-core"); err != nil {
			return nil, err
		}
		return binding, nil
	}, records: func(context.Context, deploymentConfig, environmentCreateBootstrapRuntime, []createOpenBaoPublicCRLPurpose) ([]createRootCRLRecord, error) {
		return records, nil
	}, command: command}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native, Custody: binding}
	flag := filepath.Join(dir, "fail-ready")
	if err := os.WriteFile(flag, []byte("one original wait interruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvironmentCreateOpenBaoPublicCRLsWithObservations(t.Context(), cfg, store, &parent, runtime, service, core, []openBaoPublicCRLObservations{reads}); err == nil {
		t.Fatal("known native Job wait interruption was ignored")
	}
	q, review, err := readSelectedOpenBaoPublicCRL(store, e)
	if err != nil || q == nil {
		t.Fatal("ordinary create did not freeze original producer", err)
	}
	proofPath := filepath.Join(store.Root, openBaoPublicCRLBase(q.Parent), review, "qualification.json")
	proofBytes, err := os.ReadFile(proofPath)
	if err != nil || len(proofBytes) <= 256<<10 {
		t.Fatal("large authenticated lineage was not preserved by proof codec", len(proofBytes), err)
	}
	j, exists, err := q.Plan.ReadExecution(store.Root)
	if err != nil || !exists || len(j.Owners) == 0 || j.Failed == "" {
		t.Fatal("native interruption lacks recorded write owner", err)
	}
	firstOwners := map[string]string{}
	for id, uid := range j.Owners {
		firstOwners[id] = uid
	}
	// A newly signed current registry record requalifies this same frozen
	// producer. It never substitutes a new helper, native Job or original clock.
	oldReview, oldPath, oldProof := review, proofPath, append([]byte(nil), proofBytes...)
	pendingJob, err := native.Read(t.Context(), j.Failed)
	if err != nil || pendingJob == nil || !strings.HasPrefix(j.Failed, "Job/") {
		t.Fatal("original wait interruption did not retain its native Job", err)
	}
	records = append([]createRootCRLRecord(nil), records...)
	records[0].Digest = strings.Repeat("d", 64)
	q, review, err = prepareOpenBaoPublicCRLQualification(t.Context(), cfg, store, &parent, runtime, service, core, reads)
	if err != nil || review == oldReview || q.Plan.SHA256() != j.PlanSHA256 || q.Tool.Image.Reference != image.Reference {
		t.Fatal("signed-record refresh changed immutable producer or reused stale proof", err)
	}
	stillPending, err := native.Read(t.Context(), j.Failed)
	if err != nil || deploymentplan.Digest(stillPending) != deploymentplan.Digest(pendingJob) {
		t.Fatal("qualification changed or completed the pending native Job", err)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatal("qualification required a future native readiness observation", err)
	}
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil || string(oldBytes) != string(oldProof) {
		t.Fatal("requalification replaced original immutable proof", err)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvironmentCreateOpenBaoPublicCRLsWithObservations(t.Context(), cfg, store, &parent, runtime, service, core, []openBaoPublicCRLObservations{reads}); err != nil {
		t.Fatal("same frozen producer did not resume", err)
	}
	again, againReview, err := readSelectedOpenBaoPublicCRL(store, e)
	if err != nil || againReview != review || again.Plan.SHA256() != q.Plan.SHA256() {
		t.Fatal("retry replaced selected plan/helper", err)
	}
	j, _, err = q.Plan.ReadExecution(store.Root)
	if err != nil || !j.Accepted || len(j.Owners) < 1 {
		t.Fatal("actual producer was not accepted", err)
	}
	for id, uid := range firstOwners {
		if j.Owners[id] != uid {
			t.Fatal("retry replaced original recorded Job", id)
		}
	}
	runStage("managed-complete-issuer", core)
	before := createBootstrapStateSnapshot(t, store.Root)
	completed, completedReview, err := prepareOpenBaoPublicCRLRecoveryQualification(t.Context(), cfg, store, &parent, runtime)
	if err != nil || completedReview != review || completed.Plan.SHA256() != q.Plan.SHA256() {
		t.Fatal("completed facade observation still requires a failed core source", err)
	}
	expired := *q
	expired.ValidUntil = time.Now().UTC().Add(-time.Second)
	if err := verifyOpenBaoPublicCRLQualification(t.Context(), cfg, store, &parent, runtime, &expired, deploymentplan.Digest(expired), reads); err != nil {
		t.Fatal("completed producer cannot be observed after TTL/core completion", err)
	}
	if err := ensureEnvironmentCreateOpenBaoPublicCRLsWithObservations(t.Context(), cfg, store, &parent, runtime, service, core, []openBaoPublicCRLObservations{reads}); err != nil {
		t.Fatal("completed producer replayed or failed after real core advancement", err)
	}
	assertRecoveryContractReadOnly(t, store, before)
}
