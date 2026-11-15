package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func ciTransportRecoveryBlock(t *testing.T, err error) {
	t.Helper()
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.create.recovery" {
		t.Fatalf("expected fenced create recovery blocker, got %v", err)
	}
}

// The fake native API is the only substituted boundary. Candidate generation,
// parent journaling, custody staging and completed-proof readers are production.
func ciTransportSuccessorFixture(t *testing.T) (deploymentConfig, secretStore, *environmentCreateExecution, initialTransportSuccessorArtifact, environmentCreateJournal, initialTransportSuccessorCandidate, *initialSuccessorFlowBackend) {
	t.Helper()
	cfg, store, operator, admitted := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, admitted, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	parent, now := e.Journal.PlanSHA256, time.Now().UTC()
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(store.Root, "deployment/create/material", parent, "openbao-transport/ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := renderEnvironmentCustodyTrust(cfg, parent, string(old), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := renderEnvironmentCustodyTrust(cfg, parent, candidate.Transports["openbao"].CACert, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create", parent, "custody/transport.json"), []byte(deploymentplan.String(deploymentplan.Map(before[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+strings.Repeat("b", 64), "continuation", json.RawMessage(`{"original":true}`), now); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kubernetes/device-root-active", "kubernetes/app-root-trust", "kubernetes/app-intermediate-trust"} {
		body := json.RawMessage(`{"original":true}`)
		if err := e.BeginEntry(key, "bootstrap", body, now); err != nil {
			t.Fatal(err)
		}
		if err := e.RecordReceipt(key, body); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry(key, now); err != nil {
			t.Fatal(err)
		}
	}
	role := deploymentplan.Object{"role": "certissuer-dev", "audience": "openbao"}
	if err := e.BeginEntry("openbao-role-certissuer-dev-"+deploymentplan.Digest(role)[:12], "bootstrap", mustProviderRecoveryJSON(role), now); err != nil {
		t.Fatal(err)
	}
	source := e.Journal
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: parent, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate)}
	b := &initialSuccessorFlowBackend{objects: map[string]deploymentplan.Object{}, expected: map[string]deploymentplan.Object{}}
	for i := range before {
		id := deploymentplan.ID(before[i].Object)
		uid := "original-" + id
		plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: id, UID: uid, Before: before[i].Object, Desired: after[i].Object})
		live := deploymentplan.Clone(before[i].Object)
		deploymentplan.Map(live["metadata"])["uid"], deploymentplan.Map(live["metadata"])["resourceVersion"] = uid, "17"
		b.objects[id] = live
	}
	certificate, err := parseFirstCertificatePEM(old)
	if err != nil {
		t.Fatal(err)
	}
	container := deploymentplan.Object{"name": "controller", "image": "retained-original-package", "env": environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao", "PKI_STATE_BOOTSTRAP_ROOT_SHA256": environmentPreflightBytesSHA256(certificate.Raw), "PKI_STATE_TRANSPORT_TRUST": environmentCustodyTrustPath + "/transport.json"})}
	workload := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}}}}}
	desired, err := initialTransportCustodySuccessor(workload, string(old), candidate.Transports["openbao"].CACert)
	if err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(workload)
	plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: id, UID: "original-controller", Before: workload, Desired: desired})
	live := deploymentplan.Clone(workload)
	deploymentplan.Map(live["metadata"])["uid"], deploymentplan.Map(live["metadata"])["resourceVersion"] = "original-controller", "23"
	b.objects[id], b.expected[id] = live, desired
	a := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/transport-successor", deploymentplan.Digest(a))
	for path, value := range map[string]any{filepath.Join(base, "qualification.json"): a, filepath.Join(base, "source-journal.json"): source} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, store, e, a, source, candidate, b
}

func TestCITransportSuccessorPublishesOnlyAfterReloadAndNativeVerification(t *testing.T) {
	cfg, store, e, a, source, candidate, backend := ciTransportSuccessorFixture(t)
	parent := e.Journal.PlanSHA256
	original, err := os.ReadFile(filepath.Join(store.Root, "deployment/create", parent, "custody/transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	reloadErr, verifyErr := errors.New("reload interrupted"), errors.New("native identity verification interrupted")
	check := func() error { return nil }
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, backend, a, source, candidate, check, func() error { return reloadErr }, func() error { t.Fatal("verification ran before reload"); return nil }); !errors.Is(err, reloadErr) {
		t.Fatal(err)
	}
	if backend.writes != 2 {
		t.Fatalf("reload failure crossed workload boundary: %d writes", backend.writes)
	}
	if _, complete, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || complete {
		t.Fatalf("reload failure published trust: %v %v", complete, err)
	}
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, backend, a, source, candidate, check, check, func() error { return verifyErr }); !errors.Is(err, verifyErr) {
		t.Fatal(err)
	}
	if backend.writes != 3 {
		t.Fatalf("retry replayed accepted trust objects: %d", backend.writes)
	}
	if _, complete, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || complete {
		t.Fatalf("unverified native owner published trust: %v %v", complete, err)
	}
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, backend, a, source, candidate, check, check, check); err != nil {
		t.Fatal(err)
	}
	if backend.writes != 3 {
		t.Fatal("verified retry rewrote native owners")
	}
	if _, complete, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || !complete {
		t.Fatalf("verified native recovery unavailable: %v %v", complete, err)
	}
	resources, err := completedInitialTransportSuccessorOriginalResources(store, parent)
	if err != nil || len(resources) != 3 {
		t.Fatalf("lost original trust resources: %d %v", len(resources), err)
	}
	for _, role := range []string{"openbao", "management"} {
		path, pem, err := initialTransportAuthorityPath(store, parent, role)
		if err != nil || pem != candidate.Transports[role].CACert || !strings.Contains(path, deploymentplan.Digest(a)) {
			t.Fatalf("wrong completed authority for %s: %s %v", role, path, err)
		}
	}
	old, err := os.ReadFile(filepath.Join(store.Root, "deployment/create", parent, "custody/transport.json"))
	if err != nil || !bytes.Equal(original, old) {
		t.Fatal("recovery replaced original custody snapshot", err)
	}
	path, _, err := initialTransportAuthorityPath(store, parent, "openbao")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("foreign public authority"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = initialTransportAuthorityPath(store, parent, "openbao")
	ciTransportRecoveryBlock(t, err)
}

func TestCITransportSuccessorRejectsChangedOwnerBeforeNativeWrites(t *testing.T) {
	for _, mode := range []string{"configuration", "candidate", "source-prefix", "duplicate-resource", "unsupported-resource", "lease"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, a, source, candidate, b := ciTransportSuccessorFixture(t)
			check := func() error { return nil }
			switch mode {
			case "configuration":
				cfg.Values = appendMap(cfg.Values, map[string]string{"CLOUD_STACK_NAME": "foreign-stack"})
			case "candidate":
				candidate.Parent = strings.Repeat("f", 64)
			case "source-prefix":
				source.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
				source.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "duplicate-resource":
				a.Plan.Resources = append(a.Plan.Resources, a.Plan.Resources[0])
			case "unsupported-resource":
				a.Plan.Resources[0].Desired = deploymentplan.Clone(a.Plan.Resources[0].Desired)
				a.Plan.Resources[0].Desired["kind"] = "Job"
			case "lease":
				check = func() error { return errors.New("lost fenced lease") }
			}
			err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, b, a, source, candidate, check, func() error { t.Fatal("reload crossed rejection"); return nil }, func() error { t.Fatal("verification crossed rejection"); return nil })
			ciTransportRecoveryBlock(t, err)
			if b.writes != 0 {
				t.Fatal("rejected successor changed native resources")
			}
		})
	}
}

func TestCITransportResourcePreservesNativeDefaultsAndRejectsUnrelatedEdits(t *testing.T) {
	for _, item := range []struct{ kind, name, key string }{{"Secret", "temporary-tls", "tls.crt"}, {"ConfigMap", "pki-create-transport-trust", "ca.crt"}, {"ConfigMap", "pki-create-openbao-tls-consumer-trust", "roots.pem"}, {"ConfigMap", "pki-create-service-trust", "client-roots.pem"}, {"ConfigMap", environmentCustodyTrustName, "transport.json"}} {
		t.Run(item.name, func(t *testing.T) {
			before := deploymentplan.Object{"apiVersion": "v1", "kind": item.kind, "metadata": deploymentplan.Object{"name": item.name, "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{item.key: "old-public-bytes", "unrelated": "preserved"}}
			desired := deploymentplan.Clone(before)
			deploymentplan.Map(desired["data"])[item.key] = "new-public-bytes"
			resource := activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "native-original-owner", Before: before, Desired: desired}
			actual := deploymentplan.Clone(before)
			meta := deploymentplan.Map(actual["metadata"])
			meta["uid"], meta["resourceVersion"], meta["annotations"] = "native-original-owner", "91", deploymentplan.Object{"native.example/default": "keep"}
			actual["status"] = deploymentplan.Object{"native": "observed"}
			body, err := initialTransportSuccessorResourceBody(resource, actual, "", "")
			if err != nil || deploymentplan.Map(body["metadata"])["resourceVersion"] != "91" || deploymentplan.Map(body["data"])[item.key] != "new-public-bytes" || deploymentplan.Map(body["data"])["unrelated"] != "preserved" || body["status"] != nil {
				t.Fatalf("lost native preconditions or bounded trust update: %#v %v", body, err)
			}
			if deploymentplan.Map(actual["data"])[item.key] != "old-public-bytes" {
				t.Fatal("resource body changed the observation in place")
			}
			deploymentplan.Map(desired["data"])["unrelated"] = "foreign"
			_, err = initialTransportSuccessorResourceBody(resource, actual, "", "")
			ciTransportRecoveryBlock(t, err)
			deploymentplan.Map(desired["data"])["unrelated"] = "preserved"
			meta["uid"] = "replacement-owner"
			_, err = initialTransportSuccessorResourceBody(resource, actual, "", "")
			ciTransportRecoveryBlock(t, err)
		})
	}
}

func ciTransportStaleFixture(t *testing.T) (secretStore, *environmentCreateExecution, *stalePodTestBackend, staleIdentityPodProof, deploymentplan.Resource) {
	t.Helper()
	store, e, _, base := freshStageFixture(t)
	p, j, target, objects, now := staleIdentityPodFixture(t)
	candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(p, j, target, objects, now)
	if err != nil {
		t.Fatal(err)
	}
	q := staleIdentityPodProof{Schema: staleIdentityPodProofSchema, Environment: store.Environment, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), SourceJournal: e.Journal, SourceJournalSHA: deploymentplan.Digest(e.Journal), MountReview: deploymentplan.Digest("original-mount-review"), RetainStage: "retained-stage", RetainPlanSHA: p.SHA256(), RetainJournalSHA: deploymentplan.Digest(j), DeploymentID: target.ID, DeploymentUID: target.UID, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate, QualifiedAt: now, ExecutionDeadline: now.Add(time.Hour)}
	b := &stalePodTestBackend{freshStageBackend: base, replacement: deploymentplan.Clone(objects[2])}
	for _, obj := range objects {
		b.objects[deploymentplan.ID(obj)] = deploymentplan.Clone(obj)
	}
	meta := deploymentplan.Map(b.replacement["metadata"])
	meta["name"], meta["uid"] = "mqtt-replacement", "66666666-6666-4666-8666-666666666666"
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(b.replacement["status"])["conditions"])[0])["status"] = "True"
	for _, lease := range leases {
		b.leases = append(b.leases, lease.ID)
	}
	return store, e, b, q, target
}

func TestCITransportStalePodRefusesRenewedLeaseOrChangedOwnerWithoutRetirement(t *testing.T) {
	for _, mode := range []string{"renewed-lease", "replacement-uid", "source-journal", "deadline", "target-owner"} {
		t.Run(mode, func(t *testing.T) {
			store, e, b, q, target := ciTransportStaleFixture(t)
			switch mode {
			case "renewed-lease":
				deploymentplan.Map(b.objects[q.Leases[0].ID]["spec"])["renewTime"] = time.Now().UTC().Format(time.RFC3339Nano)
			case "replacement-uid":
				deploymentplan.Map(b.objects[q.PodID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "source-journal":
				q.SourceJournalSHA = strings.Repeat("f", 64)
			case "deadline":
				q.QualifiedAt = time.Now().Add(-time.Hour)
				q.ExecutionDeadline = time.Now().Add(-time.Second)
			case "target-owner":
				target.UID = "99999999-9999-4999-8999-999999999999"
			}
			ciTransportRecoveryBlock(t, executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target))
			if b.deletes != 0 || b.objects[q.PodID] == nil {
				t.Fatal("rejected stale proof retired a native Pod")
			}
			if createOpenBaoACKCompletionEntry(e.Journal, "stale-identity-pod-"+deploymentplan.Digest(q)) != nil {
				t.Fatal("rejected stale proof recorded a restart intent")
			}
		})
	}
}

func TestCITransportStalePodCompletionBindsReplacementAndNeverDeletesIt(t *testing.T) {
	store, e, b, q, target := ciTransportStaleFixture(t)
	if err := executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target); err != nil {
		t.Fatal(err)
	}
	key := "stale-identity-pod-" + deploymentplan.Digest(q)
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	var receipt map[string]any
	if entry == nil || entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["original_pod_uid"] != q.PodUID || receipt["replacement_pod_uid"] != "66666666-6666-4666-8666-666666666666" {
		t.Fatalf("missing exact native replacement receipt: %#v", entry)
	}
	if err := executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target); err != nil {
		t.Fatal("completed observation retry", err)
	}
	if b.deletes != 1 || b.objects[deploymentplan.ID(b.replacement)] == nil {
		t.Fatal("completed retry deleted the replacement")
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	entry.Receipt = json.RawMessage(`{"replacement_pod_uid":"foreign"}`)
	ciTransportRecoveryBlock(t, executeStaleIdentityPodRestart(t.Context(), store, e, b, b, q, target))
	if b.deletes != 1 {
		t.Fatal("changed completion receipt replayed deletion")
	}
}

func TestCITransportCoreEnvelopeIsImmutableAndRejectsForeignDispatch(t *testing.T) {
	store, e, plan, _ := freshStageFixture(t)
	original, application, qualification := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	review, err := writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, original, qualification, application)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, original, qualification, application); err != nil || again != review {
		t.Fatal("immutable envelope retry", err)
	}
	for _, bad := range []struct {
		parent, original, qualification, application string
		images                                       []string
	}{{"../foreign", original, qualification, application, nil}, {e.Journal.PlanSHA256, original, "", application, nil}, {e.Journal.PlanSHA256, original, qualification, application, []string{"not-a-digest"}}, {e.Journal.PlanSHA256, original, qualification, application, []string{strings.Repeat("d", 64), strings.Repeat("e", 64)}}} {
		_, err := writeCurrentCoreForwardEnvelope(store, bad.parent, bad.original, bad.qualification, bad.application, bad.images...)
		ciTransportRecoveryBlock(t, err)
	}
	envelope := &currentCoreForwardEnvelope{Schema: currentCoreForwardSchema, OriginalReview: original, ApplicationReview: application, Qualification: qualification}
	if got, err := currentCoreApplicationDispatchReview(original, review, envelope); err != nil || got != application {
		t.Fatalf("combined review replaced separately qualified application: %s %v", got, err)
	}
	envelope.ApplicationReview = ""
	if got, err := currentCoreApplicationDispatchReview(original, review, envelope); err != nil || got != original {
		t.Fatal("application-free envelope changed original review", got, err)
	}
	envelope.ApplicationReview = "foreign"
	_, err = currentCoreApplicationDispatchReview(original, review, envelope)
	ciTransportRecoveryBlock(t, err)
	path := filepath.Join(store.Root, "deployment/create/issuer-enrollment-continuation", review, "current-core-forward.json")
	if err := os.WriteFile(path, []byte(`{"Schema":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, original, qualification, application)
	ciTransportRecoveryBlock(t, err)
	_, err = loadCurrentCoreForwardEnvelope(t.Context(), store, e, review, original, &environmentCreatePlan{}, environmentCreateBootstrapRuntime{})
	ciTransportRecoveryBlock(t, err)
	q := &currentCoreForwardQualification{Schema: currentCoreForwardSchema, Parent: e.Journal.PlanSHA256, Source: e.Journal, Candidate: plan}
	if err := writeCurrentCoreForwardQualification(store, q); err != nil {
		t.Fatal(err)
	}
	saved, err := readCurrentCoreForward(store, e, deploymentplan.Digest(q))
	if err != nil || saved.Parent != e.Journal.PlanSHA256 {
		t.Fatal("qualification round trip", err)
	}
	qpath := filepath.Join(store.Root, currentCoreForwardBase(q.Parent), deploymentplan.Digest(q), "qualification.json")
	if err := os.WriteFile(qpath, []byte(`{"Schema":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ciTransportRecoveryBlock(t, writeCurrentCoreForwardQualification(store, q))
	_, err = readCurrentCoreForward(store, e, deploymentplan.Digest(q))
	ciTransportRecoveryBlock(t, err)
	var report unifiedDeploymentReport
	now := time.Now().UTC()
	got, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, nil, &report, store.Environment, now, now.Add(time.Minute))
	if err != nil || got != application || report.Decision != "GO" || len(report.Plan.Resources) != 6 || report.Plan.PlanSHA256 != application {
		t.Fatalf("remaining create scope lost: %#v %v", report, err)
	}
}

func TestCITransportCommandGuardsRejectUnreviewedOrCrossEnvironmentWrites(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(t.TempDir(), "isolated-config"))
	parent := strings.Repeat("a", 64)
	for _, args := range [][]string{{}, {"--environment", "staging", "--parent-plan-sha256", parent, "--prepare"}, {"--environment", "dev", "--parent-plan-sha256", parent, "--prepare", "--qualify"}, {"--environment", "dev", "--parent-plan-sha256", parent, "--execute"}, {"--environment", "dev", "--parent-plan-sha256", parent, "--prepare", "--timeout", "31m"}, {"--environment", "dev", "--parent-plan-sha256", parent, "--prepare", "--confirm", "video-cloud-dev"}} {
		ciTransportRecoveryBlock(t, runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), args, io.Discard, io.Discard))
	}
	for _, args := range [][]string{{}, {"--environment", "dev", "--execute"}, {"--environment", "dev", "--execute", "--reconcile-observed", "--review-sha256", parent, "--confirm", "video-cloud-dev"}, {"--environment", "dev", "--review-sha256", parent}, {"--environment", "dev", "--timeout", "0s"}} {
		ciTransportRecoveryBlock(t, runCreateStaleIdentityPodRecovery(args))
	}
	for _, args := range [][]string{{}, {"--environment", "dev", "--role", "LKE_ACCOUNT_MANAGER_IMAGE"}, {"--environment", "dev", "--parent-plan-sha256", parent, "--image", "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:latest"}} {
		ciTransportRecoveryBlock(t, runCreateWorkloadImageForward(args))
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("RTK_CLOUD_CONFIG_ROOT"), "dev")); !os.IsNotExist(err) {
		t.Fatalf("invalid command established environment state: %v", err)
	}
}

func TestCITransportPublicCRLToolPreservesPurposeAndRequiresNativeJobs(t *testing.T) {
	tool := failedForwardTool{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("a", 40)}, Capabilities: []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`)}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": strings.Repeat("original-", 8), "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": "original-package", "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/public/root.json", "original-issuer", "original-fingerprint"}, "env": environmentCreateEnv(map[string]string{"PKI_STATE_RECORD_PREFIX": "original/identity"})}}, "restartPolicy": "Never"}}}}
	before := deploymentplan.Digest(job)
	jobs, err := productPublicCRLToolJobs([]deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, tool)
	if err != nil || len(jobs) != 1 {
		t.Fatal(err)
	}
	c := managedNamedField(environmentPKIPod(jobs[0].Object)["containers"], "enroll")
	if c["image"] != tool.Image.Reference || mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != "original/identity" || deploymentplan.Digest(job) != before || len(deploymentplan.String(deploymentplan.Map(jobs[0].Object["metadata"])["name"])) > 63 {
		t.Fatal("formal tool changed original purpose, identity or Job")
	}
	for _, mode := range []string{"non-job", "wrong-purpose", "mutable-image", "missing-revision"} {
		bad := deploymentplan.Clone(job)
		selected := tool
		switch mode {
		case "non-job":
			bad["kind"] = "Deployment"
		case "wrong-purpose":
			managedNamedField(environmentPKIPod(bad)["containers"], "enroll")["command"] = []any{"/app/serviceidentity-bootstrap", "enroll", "private", "issuer", "fingerprint"}
		case "mutable-image":
			selected.Image.Reference = "image:latest"
		case "missing-revision":
			selected.Image.SourceRevision = ""
		}
		_, err := productPublicCRLToolJobs([]deploymentplan.Definition{{Object: bad}}, selected)
		ciTransportRecoveryBlock(t, err)
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	if initialized, err := renderCreateProductPublicCRLs(t.Context(), deploymentConfig{Values: map[string]string{}}, environmentCreateBootstrapPlan{}, environmentCreateBootstrapRuntime{}, environmentCreateServiceCeremony{}, nil); err != nil || len(initialized) != 0 {
		t.Fatal("non-OpenBao renderer introduced native work", err)
	}
	_, err = initialTransportCustodySnapshotPath(store, strings.Repeat("a", 64), "/foreign/authority")
	ciTransportRecoveryBlock(t, err)
}

func TestCITransportPublicCRLForwardAcceptsOnlyNewSamePurposeJobAndKeepsFailedSource(t *testing.T) {
	store, e, template, backend := freshStageFixture(t)
	ns := e.Journal.Stack + "-video-cloud"
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "original-public-records", "namespace": ns}, "data": deploymentplan.Object{"public": "retained-original"}}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "original-public-crl", "namespace": ns}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "enroll", "image": "original-tool", "command": []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/public/root.json", "original-issuer", "original-root"}, "env": environmentCreateEnv(map[string]string{"PKI_STATE_RECORD_PREFIX": "original/identity"})}}}}}}
	defs := []deploymentplan.Definition{{Object: config, Phase: "configuration"}, {Object: job, Phase: "workload"}}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(productPublicCRLStage, defs)
	source, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	run := func(stage string, p *deploymentplan.Plan) error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, backend, fence, fence, fence, func(context.Context) error { return nil }, io.Discard)
	}
	backend.failure = "after-write-workload"
	if err := run(productPublicCRLStage, source); err == nil {
		t.Fatal("fixture failed to retain native failed Job")
	}
	j, exists, err := source.ReadExecution(store.Root)
	if err != nil || !exists || j.Accepted || j.Failed != deploymentplan.ID(job) {
		t.Fatalf("source is not the recorded failed native initializer: %#v %v", j, err)
	}
	originalJournal := deploymentplan.Digest(j)
	originalJob := deploymentplan.Digest(backend.objects[deploymentplan.ID(job)])
	nextJob := deploymentplan.Clone(job)
	deploymentplan.Map(nextJob["metadata"])["name"] = "reviewed-public-crl-successor"
	managedNamedField(environmentPKIPod(nextJob)["containers"], "enroll")["image"] = "reviewed-public-crl-tool"
	// Retain the literal accepted ConfigMap, including its original revision
	// annotation; a new input template is not evidence of the old native owner.
	nextDefs := []deploymentplan.Definition{{Object: deploymentplan.Clone(source.Resources[0].Desired), Phase: "configuration", Retain: true}, {Object: nextJob, Phase: "workload"}}
	stage := productPublicCRLStage + "-reviewed-native-tool"
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, nextDefs)
	next, err := deploymentplan.Compile(in, nextDefs, []deploymentplan.Object{backend.objects[deploymentplan.ID(config)]})
	if err != nil {
		t.Fatal(err)
	}
	backend.failure, backend.failed = "", false
	if err := run(stage, next); err != nil {
		t.Fatal(err)
	}
	raw := mustProviderRecoveryJSON(productCRLForwardReceipt{Schema: productCRLForwardSchema, OriginalPlan: source.SHA256(), OriginalJournal: originalJournal, ForwardStage: "kubernetes/" + stage, ForwardPlan: next.SHA256()})
	if err := validateProductCRLForwardReceipt(store, e, source, j, raw); err != nil {
		t.Fatal("accepted same-purpose native successor rejected", err)
	}
	if err := completeProductCRLForward(t.Context(), store, e, source, stage, next); err != nil {
		t.Fatal(err)
	}
	if err := completeProductCRLForward(t.Context(), store, e, source, stage, next); err != nil {
		t.Fatal("completed receipt retry", err)
	}
	unchanged, _, err := source.ReadExecution(store.Root)
	if err != nil || deploymentplan.Digest(unchanged) != originalJournal || deploymentplan.Digest(backend.objects[deploymentplan.ID(job)]) != originalJob || backend.writes[deploymentplan.ID(job)] != 1 || backend.writes[deploymentplan.ID(nextJob)] != 1 || backend.writes[deploymentplan.ID(config)] != 1 {
		t.Fatal("logical recovery rewrote failed source, public records or original Job", err)
	}
	for _, mode := range []string{"foreign-source", "foreign-plan", "foreign-stage", "accepted-source", "missing-child"} {
		t.Run(mode, func(t *testing.T) {
			r := productCRLForwardReceipt{Schema: productCRLForwardSchema, OriginalPlan: source.SHA256(), OriginalJournal: originalJournal, ForwardStage: "kubernetes/" + stage, ForwardPlan: next.SHA256()}
			journal := j
			switch mode {
			case "foreign-source":
				r.OriginalJournal = strings.Repeat("f", 64)
			case "foreign-plan":
				r.ForwardPlan = strings.Repeat("f", 64)
			case "foreign-stage":
				r.ForwardStage = "kubernetes/unrelated"
			case "accepted-source":
				journal.Accepted = true
			case "missing-child":
				r.ForwardStage = "kubernetes/" + productPublicCRLStage + "-missing"
			}
			ciTransportRecoveryBlock(t, validateProductCRLForwardReceipt(store, e, source, journal, mustProviderRecoveryJSON(r)))
		})
	}
}

func TestCITransportCurrentCoreRecipeRetainsOldCleanupOwnerAndRejectsScopeExpansion(t *testing.T) {
	cfg, p, _, ceremony := createServiceRecipeFixture(t, "dev")
	old := ceremony.CleanupBinding
	container := deploymentplan.Object{"name": "pki-controller", "image": p.Image, "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": old.SessionID})}
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"serviceAccountName": "pki-controller", "automountServiceAccountToken": false, "containers": []any{container}}}}}
	controls, err := applyEnvironmentBootstrapCleanup(cfg.Values, controller, old)
	if err != nil {
		t.Fatal(err)
	}
	defs := append(controls, deploymentplan.Definition{Object: controller, Phase: "workload"})
	in := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("reviewed-release"), BindingsSHA256: environmentCreateStageBindingsSHA("original-core", defs)}
	source, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: source.SHA256(), Failed: deploymentplan.ID(controller), Owners: map[string]string{}}
	var owners []activeTransportRenewalResource
	for i, r := range source.Resources {
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)
		live := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(live["metadata"])["uid"] = uid
		j.Owners[r.ID] = uid
		owners = append(owners, activeTransportRenewalResource{ID: r.ID, UID: uid, Desired: live})
		if r.ID != j.Failed {
			j.Completed = append(j.Completed, r.ID)
		}
	}
	current := old
	current.SessionID, current.SecretUID = "66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777"
	current.ConfigMapName = "pki-bootstrap-cleanup-binding-" + current.SessionID[:12]
	nextController := deploymentplan.Clone(controller)
	managedNamedField(managedNamedField(environmentPKIPod(nextController)["containers"], "pki-controller")["env"], "PKI_BOOTSTRAP_SESSION_ID")["value"] = current.SessionID
	nextControls, err := applyEnvironmentBootstrapCleanup(cfg.Values, nextController, current)
	if err != nil {
		t.Fatal(err)
	}
	nextDefs := append(nextControls, deploymentplan.Definition{Object: nextController, Phase: "workload"})
	candidate, err := compileCurrentCoreForwardRecipe(cfg, source, j, owners, current, current.SessionID, nextDefs, "current-core-ci")
	if err != nil {
		t.Fatal("exact current session recipe rejected", err)
	}
	oldMapID := "ConfigMap/" + p.Stack + "-video-cloud/pki-bootstrap-cleanup-binding"
	newMapID := "ConfigMap/" + p.Stack + "-video-cloud/" + current.ConfigMapName
	seenOld, seenNew := false, false
	for _, r := range candidate.Resources {
		if r.ID == oldMapID {
			seenOld = true
			if r.Action != "retain" || r.UID != j.Owners[r.ID] {
				t.Fatal("old immutable cleanup owner was recreated or rewritten")
			}
		}
		if r.ID == newMapID {
			seenNew = true
			if r.Action != "create" || r.UID != "" {
				t.Fatal("new session adopted an existing cleanup owner")
			}
		}
	}
	if !seenOld || !seenNew {
		t.Fatal("successor omitted original or new cleanup binding")
	}
	for _, mode := range []string{"replicas", "duplicate-owner", "foreign-owner", "accepted-source", "uncertain-source", "foreign-session"} {
		t.Run(mode, func(t *testing.T) {
			changed := make([]deploymentplan.Definition, len(nextDefs))
			for i, d := range nextDefs {
				changed[i] = d
				changed[i].Object = deploymentplan.Clone(d.Object)
			}
			facts := append([]activeTransportRenewalResource(nil), owners...)
			journal, binding := j, current
			switch mode {
			case "replicas":
				deploymentplan.Map(changed[len(changed)-1].Object["spec"])["replicas"] = float64(2)
			case "duplicate-owner":
				facts = append(facts, facts[0])
			case "foreign-owner":
				facts[0].UID = "99999999-9999-4999-8999-999999999999"
			case "accepted-source":
				journal.Accepted = true
			case "uncertain-source":
				journal.InFlight = source.Resources[0].ID
			case "foreign-session":
				binding.SessionID = "99999999-9999-4999-8999-999999999999"
			}
			_, err := compileCurrentCoreForwardRecipe(cfg, source, journal, facts, binding, current.SessionID, changed, "current-core-ci")
			if mode == "duplicate-owner" {
				if err == nil || !strings.Contains(err.Error(), "duplicate live resource") {
					t.Fatalf("ambiguous native inventory admitted: %v", err)
				}
			} else {
				ciTransportRecoveryBlock(t, err)
			}
		})
	}
}

func TestCITransportSuccessorOwnersRequireCompleteRecordedNativeTrustInventory(t *testing.T) {
	cfg, store, e, a, _, candidate, flow := ciTransportSuccessorFixture(t)
	old := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		var material environmentCreateTransport
		if err := readExecutionJSON(filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, role+"-transport/identities.json"), &material); err != nil {
			t.Fatal(err)
		}
		old[role] = material
	}
	var defs []deploymentplan.Definition
	for _, r := range a.Plan.Resources {
		phase := "configuration"
		if r.Before["kind"] == "Deployment" {
			phase = "workload"
		}
		defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(r.Before), Phase: phase})
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	obj := func(kind, namespace, name string, data deploymentplan.Object) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "data": data}
	}
	for _, object := range []deploymentplan.Object{
		obj("ConfigMap", ns, "pki-create-transport-trust", deploymentplan.Object{"ca.crt": old["management"].CACert}),
		obj("ConfigMap", ns, "pki-openbao-transport-ca", deploymentplan.Object{"ca.crt": old["openbao"].CACert}),
		obj("ConfigMap", ns, "pki-create-openbao-tls-consumer-trust", deploymentplan.Object{"roots.pem": "retained-prefix\n" + old["openbao"].CACert}),
		obj("ConfigMap", cfg.Values["CLOUD_STACK_NAME"]+"-secrets", "pki-create-service-trust", deploymentplan.Object{"client-roots.pem": old["management"].CACert + "\nretained-suffix"}),
		obj("ConfigMap", ns, "unrelated-public-data", deploymentplan.Object{"value": "not-an-authority"}),
	} {
		defs = append(defs, deploymentplan.Definition{Object: object, Phase: "configuration"})
	}
	secret, _ := initialTransportOverride(obj("Secret", ns, "openbao-transport-tls", deploymentplan.Object{}), old)
	defs = append(defs, deploymentplan.Definition{Object: secret, Phase: "configuration"})
	in := deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("frozen-native-release"), BindingsSHA256: environmentCreateStageBindingsSHA("original-transport-owners", defs)}
	p, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "original-transport-owners", p, api, fence, fence, fence, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	objects := map[string]deploymentplan.Object{}
	for id, object := range api.objects {
		objects[id] = deploymentplan.Clone(object)
	}
	for _, namespace := range []string{ns, cfg.Values["CLOUD_STACK_NAME"] + "-account-manager", cfg.Values["CLOUD_STACK_NAME"] + "-secrets"} {
		objects["Namespace//"+namespace] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace, "uid": "namespace-" + namespace}}
	}
	native, dir, calls := unifiedSessionNativeBackend(t, store, objects, nil)
	before := deploymentplan.Digest(e.Journal)
	resources, err := initialTransportSuccessorOwners(t.Context(), cfg, store, e, native, old, candidate.Transports)
	if err != nil || len(resources) != 8 {
		t.Fatalf("complete original trust inventory not selected: %d %v", len(resources), err)
	}
	seenSecret, seenDeployment := false, false
	for _, r := range resources {
		if r.UID != deploymentplan.Map(objects[r.ID]["metadata"])["uid"] || !deploymentplan.Matches(r.Before, objects[r.ID]) {
			t.Fatal("successor lost recorded native owner", r.ID)
		}
		switch r.Desired["kind"] {
		case "ConfigMap":
			if seenSecret || seenDeployment {
				t.Fatal("public trust follows consumer restart")
			}
		case "Secret":
			seenSecret = true
			if seenDeployment {
				t.Fatal("serving transport follows consumer restart")
			}
			if deploymentplan.Map(r.Desired["data"])["tls.key"] != deploymentplan.Map(r.Before["data"])["tls.key"] {
				t.Fatal("temporary recovery replaced original serving private key")
			}
		case "Deployment":
			seenDeployment = true
		}
	}
	if !seenSecret || !seenDeployment || deploymentplan.Digest(e.Journal) != before {
		t.Fatal("read-only qualification lost native classes or modified original journal")
	}
	foreign := obj("ConfigMap", ns, "unrecorded-trust-copy", deploymentplan.Object{"roots.pem": old["openbao"].CACert})
	foreignPath := filepath.Join(dir, strings.ReplaceAll(deploymentplan.ID(foreign), "/", "_")+".json")
	if err := os.WriteFile(foreignPath, mustProviderRecoveryJSON(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = initialTransportSuccessorOwners(t.Context(), cfg, store, e, native, old, candidate.Transports)
	ciTransportRecoveryBlock(t, err)
	if !strings.Contains(err.Error(), "unaccounted original trust reference") {
		t.Fatal("unrecorded native trust did not identify the missing owner boundary", err)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if !strings.HasPrefix(call, "get ") {
			t.Fatalf("qualification performed a native mutation: %s", call)
		}
	}
	if flow.writes != 0 {
		t.Fatal("owner discovery crossed recovery execution")
	}
}

func TestCITransportCommandsPreserveOriginalGenerationAtLaterUnavailableBoundaries(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	kube, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, e, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"LINODE_TOKEN": "isolated-original-operator"}, kubeconfig: kube})
	e, _ = providerExecutionContractFork(t, e, parent, "")
	api := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, leases: len(e.Journal.KubernetesLeases)}}
	runStage := func(stage, name string) *deploymentplan.Plan {
		t.Helper()
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": e.Journal.Stack + "-video-cloud"}, "data": deploymentplan.Object{"original": "isolated-native-checkpoint"}}
		defs := []deploymentplan.Definition{{Object: body, Phase: "configuration"}}
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
		if err != nil {
			t.Fatal(err)
		}
		fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, api, fence, fence, fence, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := runStage("service-managed-enrollment-issuer", "original-issuer-checkpoint")
	proof := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), Release: first.ReleaseSHA256, FirstStage: first.SHA256()}
	review := deploymentplan.Digest(proof)
	now := time.Now().UTC()
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+review, "continuation", mustProviderRecoveryJSON(proof), now); err != nil {
		t.Fatal(err)
	}
	proof.QualifiedAt, proof.ValidUntil = now, now.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"device-root-active", "app-root-trust", "app-intermediate-trust"} {
		runStage(stage, "original-"+stage)
	}
	role := deploymentplan.Object{"role": "certissuer-dev", "audience": "openbao", "ttl": "15m"}
	if err := e.BeginEntry("openbao-role-certissuer-dev-"+deploymentplan.Digest(role)[:12], "bootstrap", mustProviderRecoveryJSON(role), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	sha := e.Journal.PlanSHA256
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, sha, role, map[string][]string{role: {role + ".isolated.svc"}}, time.Now().UTC().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	originalJournal, originalOwner := readTestFile(t, e.JournalPath), readTestFile(t, filepath.Join(store.Root, "deployment/.execution-active/owner.json"))
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--environment=dev", "--parent-plan-sha256=" + sha, "--prepare"}
	var output, diagnostics bytes.Buffer
	if err := runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), args, &output, &diagnostics); err != nil {
		t.Fatal("original qualified preparation", err)
	}
	if !strings.Contains(output.String(), "scope=initial-temporary-transport-preparation") {
		t.Fatal("preparation claimed another scope", output.String())
	}
	candidatePath := filepath.Join(store.Root, "deployment/create", sha, "transport-successor/candidate.json")
	prepared := readTestFile(t, candidatePath)
	if err := runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), args, io.Discard, io.Discard); err != nil || readTestFile(t, candidatePath) != prepared {
		t.Fatal("prepare retry replaced retained authority", err)
	}
	// No initialized host is part of this explicitly earlier fixture. A later
	// qualification must refuse it without generating identity or native writes.
	err = runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), []string{"--environment=dev", "--parent-plan-sha256=" + sha, "--qualify"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "bounded regular environment input") {
		t.Fatalf("uninitialized OpenBao accepted for native transport recovery: %v", err)
	}
	err = runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), []string{"--environment=dev", "--parent-plan-sha256=" + sha, "--execute", "--review-sha256=" + strings.Repeat("f", 64), "--confirm=" + e.Journal.Stack}, io.Discard, io.Discard)
	ciTransportRecoveryBlock(t, err)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!/bin/sh\nprintf '%s\\n' unexpected-native-call >> '"+calls+"'\nexit 71\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	for _, args := range [][]string{{"--environment=dev"}, {"--environment=dev", "--controller"}} {
		if err := runCreateStaleIdentityPodRecovery(args); err == nil {
			t.Fatal("earlier generation admitted stale identity repair")
		}
	}
	imageArgs := []string{"--environment=dev", "--parent-plan-sha256=" + sha, "--image=ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), "--source-revision=" + strings.Repeat("b", 40), "--run-id=70", "--run-attempt=2", "--manifest-sha256=" + strings.Repeat("c", 64), "--confirm=" + e.Journal.Stack}
	err = runCreateWorkloadImageForward(imageArgs)
	ciTransportRecoveryBlock(t, err)
	if !strings.Contains(err.Error(), "selected OpenBao identity backend") {
		t.Fatal("runtime image command crossed the wrong source boundary", err)
	}
	if readTestFile(t, e.JournalPath) != originalJournal || readTestFile(t, filepath.Join(store.Root, "deployment/.execution-active/owner.json")) != originalOwner || readTestFile(t, candidatePath) != prepared {
		t.Fatal("scoped command changed original owner, journal or retained temporary authority")
	}
	if raw, err := os.ReadFile(calls); err == nil && len(raw) > 0 {
		t.Fatal("earlier checkpoint refusal contacted native Kubernetes", string(raw))
	}
}

// The original initialized host is an explicit historical fixture boundary;
// preparation, qualification, execution and retry below use the public command.
// Kubernetes and the running OpenBao process are the only external simulators.
func TestCITransportQualifiedCommandKeepsInitializedHostAndOriginalServingKey(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	kube, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, e, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"ISOLATED_OPERATOR": "original"}, kubeconfig: kube})
	e, _ = providerExecutionContractFork(t, e, parent, "")
	sha, ns := e.Journal.PlanSHA256, e.Journal.Stack+"-secrets"
	old := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		old[role], err = createEnvironmentTransport(store, sha, role, map[string][]string{role: {role + ".isolated.svc"}}, time.Now().UTC().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	api := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, leases: len(e.Journal.KubernetesLeases)}}
	run := func(stage string, defs []deploymentplan.Definition) *deploymentplan.Plan {
		t.Helper()
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
		if err != nil {
			t.Fatal(err)
		}
		fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, api, fence, fence, fence, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
		return p
	}
	trust, err := renderEnvironmentCustodyTrust(cfg, sha, old["openbao"].CACert, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create", sha, "custody/transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	leaf := old["openbao"].Identities["openbao"]
	secret := environmentCreateSecret(ns, "openbao-transport-tls", map[string]string{"ca.crt": old["openbao"].CACert, "tls.crt": leaf.Certificate, "tls.key": leaf.Key})
	ca := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-openbao-transport-ca", "namespace": ns}, "data": deploymentplan.Object{"ca.crt": old["openbao"].CACert}}, Phase: "configuration"}
	set := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": ns, "labels": deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack}}, "spec": deploymentplan.Object{"replicas": float64(1), "serviceName": "openbao", "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "quay.io/openbao/openbao@sha256:" + strings.Repeat("a", 64)}}}}}}
	foundation := append(trust, secret, ca, deploymentplan.Definition{Object: set, Phase: "workload"})
	run("foundation", foundation)
	set = deploymentplan.Clone(api.objects[deploymentplan.ID(set)])
	setUID := deploymentplan.String(deploymentplan.Map(set["metadata"])["uid"])
	podUID := "33333333-3333-4333-8333-333333333333"
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": podUID, "resourceVersion": "1", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": setUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(set))}
	init := mustProviderRecoveryJSON(deploymentplan.Object{"root_token": "isolated-original-root", "unseal_keys_b64": []string{base64.StdEncoding.EncodeToString([]byte(strings.Repeat("u", 32)))}})
	for path, raw := range map[string][]byte{"openbao/init.json": init, "openbao/root-token": []byte("isolated-original-root"), "openbao/unseal-key": []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("u", 32))))} {
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	initialization := deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(init), "pod_uid": podUID, "statefulset_uid": setUID, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"}
	if err := e.BeginEntry("openbao-initialize", "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"pod_uid": podUID, "statefulset_uid": setUID}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt("openbao-initialize", mustProviderRecoveryJSON(initialization)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	checkpoint := func(stage string) *deploymentplan.Plan {
		return run(stage, []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "checkpoint-" + stage, "namespace": e.Journal.Stack + "-video-cloud"}, "data": deploymentplan.Object{"original": "checkpoint"}}, Phase: "configuration"}})
	}
	first := checkpoint("service-managed-enrollment-issuer")
	proof := issuerEnrollmentContinuationProof{Parent: sha, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), FirstStage: first.SHA256(), Release: first.ReleaseSHA256}
	continuation := deploymentplan.Digest(proof)
	now := time.Now().UTC()
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+continuation, "continuation", mustProviderRecoveryJSON(proof), now); err != nil {
		t.Fatal(err)
	}
	proof.QualifiedAt, proof.ValidUntil = now, now.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", continuation, "qualification.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"device-root-active", "app-root-trust", "app-intermediate-trust"} {
		checkpoint(stage)
	}
	role := deploymentplan.Object{"role": "certissuer-dev", "audience": "openbao"}
	if err := e.BeginEntry("openbao-role-certissuer-dev-"+deploymentplan.Digest(role)[:12], "bootstrap", mustProviderRecoveryJSON(role), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	objects := map[string]deploymentplan.Object{deploymentplan.ID(pod): pod}
	for id, o := range api.objects {
		objects[id] = deploymentplan.Clone(o)
	}
	for _, namespace := range []string{ns, e.Journal.Stack + "-video-cloud", e.Journal.Stack + "-account-manager"} {
		objects["Namespace//"+namespace] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace, "uid": "native-" + namespace}}
	}
	_, dir, calls := unifiedSessionNativeBackend(t, store, objects, nil)
	// The provider already froze these original bytes. Adapt only the external
	// simulator to the exact canonical identity the real command will reconstruct.
	frozen, err := newEnvironmentKubernetesBackendForConfig(string(kube), cfg)
	if err != nil {
		t.Fatal(err)
	}
	protocolPath := filepath.Join(dir, "unified-kubectl.py")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
	if strings.Count(string(protocol), sentinel) != 1 {
		t.Fatal("native fixture has no exact frozen-identity boundary")
	}
	protocol = []byte(strings.Replace(string(protocol), sentinel, "    if f.read() != bytes.fromhex('"+fmt.Sprintf("%x", frozen.KubeconfigContent)+"'): sys.exit(72)", 1))
	if err := os.WriteFile(protocolPath, protocol, 0600); err != nil {
		t.Fatal(err)
	}
	// Model the process observing its actual mounted Secret, not expected test
	// hashes. This also makes a missing native TLS write fail qualification/retry.
	wrapper := `import base64,hashlib,json,os,sys
a=sys.argv[1:];root=os.environ['RTK_LATE_NATIVE_OBJECTS'];script=a[-1]
if 'exec' in a:
 with open(a[1],'rb') as f:identity=f.read()
 if b'token: private-frozen-token\n' not in identity:sys.exit(72)
 with open(os.path.join(root,'transport-exec.log'),'a') as log:
  if script.endswith('sha256sum /openbao/transport/tls.crt /openbao/transport/ca.crt /openbao/transport/tls.key'):
   with open(os.path.join(root,'Secret_'+os.environ['RTK_CI_TRANSPORT_NAMESPACE']+'_openbao-transport-tls.json')) as f:data=json.load(f)['data']
   for key in ('tls.crt','ca.crt','tls.key'):print(hashlib.sha256(base64.b64decode(data[key])).hexdigest()+'  /openbao/transport/'+key)
   log.write('mounted-hashes\n')
  elif 'kill -HUP 1' in script:log.write('reload-original-process\n')
  elif 'bao status -format=json' in script:print('{"initialized":true,"sealed":false}');log.write('status\n')
  elif 'bao audit list -format=json' in script:
   sys.stdin.read();print('{"deployment-audit/":{"type":"file","options":{"file_path":"/openbao/audit/audit.jsonl","log_raw":"false"}}}');log.write('audit\n')
  else:sys.exit(74)
else:os.execv(sys.executable,[sys.executable,os.environ['RTK_UNIFIED_KUBECTL']]+a)
`
	wrapped := filepath.Join(dir, "transport-kubectl.py")
	if err := os.WriteFile(wrapped, []byte(wrapper), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nexec \"$RTK_UNIFIED_PYTHON\" \"$RTK_CI_TRANSPORT_WRAPPER\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CI_TRANSPORT_WRAPPER", wrapped)
	t.Setenv("RTK_CI_TRANSPORT_NAMESPACE", ns)
	originalOwner := readTestFile(t, filepath.Join(store.Root, "deployment/.execution-active/owner.json"))
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	command := func(phase string, extra ...string) string {
		t.Helper()
		var out, diagnostics bytes.Buffer
		args := append([]string{"--environment=dev", "--parent-plan-sha256=" + sha, "--" + phase}, extra...)
		if err := runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), args, &out, &diagnostics); err != nil {
			t.Fatalf("%s command: %v\n%s", phase, err, diagnostics.String())
		}
		return out.String()
	}
	command("prepare")
	qualified := command("qualify")
	qualification := ""
	for _, field := range strings.Fields(qualified) {
		if strings.HasPrefix(field, "review-sha256=") {
			qualification = strings.TrimPrefix(field, "review-sha256=")
		}
	}
	if !environmentPreflightDigest.MatchString(qualification) {
		t.Fatal("native qualification omitted its exact review", qualified)
	}
	logBefore, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logBefore)), "\n") {
		if strings.HasPrefix(line, "replace ") && !strings.Contains(line, "--dry-run=server") {
			t.Fatal("qualification changed native trust", line)
		}
	}
	if _, complete, err := readCompletedInitialTransportSuccessor(store, sha); err != nil || complete {
		t.Fatal("qualification published recovery completion", err)
	}
	command("execute", "--review-sha256="+qualification, "--confirm="+e.Journal.Stack)
	if _, complete, err := readCompletedInitialTransportSuccessor(store, sha); err != nil || !complete {
		t.Fatal("native command failed to publish exact completion", err)
	}
	beforeRetry := createBootstrapStateSnapshot(t, store.Root)
	command("execute", "--review-sha256="+qualification, "--confirm="+e.Journal.Stack)
	if readTestFile(t, filepath.Join(store.Root, "openbao/init.json")) != string(init) || readTestFile(t, filepath.Join(store.Root, "openbao/root-token")) != "isolated-original-root" || readTestFile(t, filepath.Join(store.Root, "deployment/.execution-active/owner.json")) != originalOwner || readTestFile(t, store.KubeconfigPath()) != string(kube) {
		t.Fatal("transport retry replaced initialized authority or parent owner")
	}
	var actual deploymentplan.Object
	if err := readExecutionJSON(filepath.Join(dir, "Secret_"+ns+"_openbao-transport-tls.json"), &actual); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Map(actual["data"])["tls.key"] != base64.StdEncoding.EncodeToString([]byte(leaf.Key)) {
		t.Fatal("recovery replaced original serving private key")
	}
	if beforeRetry["openbao/init.json"] != createBootstrapStateSnapshot(t, store.Root)["openbao/init.json"] {
		t.Fatal("idempotent retry changed original initialization")
	}
	processLog := readTestFile(t, filepath.Join(dir, "transport-exec.log"))
	if strings.Count(processLog, "reload-original-process") != 2 || !strings.Contains(processLog, "audit") {
		t.Fatal("native reload was not verified against original process", processLog)
	}
}
