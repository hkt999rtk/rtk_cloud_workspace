package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productParserUnsealBackend struct {
	*productProtocolBackend
	f           *productProtocolFixture
	interrupted bool
}

func (b *productParserUnsealBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if environmentOnDeleteOpenBao(r) && !b.interrupted {
		b.interrupted = true
		controller := b.objects[r.ID]
		deploymentplan.Map(controller["metadata"])["generation"] = float64(2)
		controller["status"] = deploymentplan.Object{"observedGeneration": float64(2), "updateRevision": "actual-target-revision", "currentRevision": "old-ondelete-revision"}
		uid := "88888888-8888-4888-8888-888888888888"
		pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": b.f.p.Stack + "-secrets", "name": "openbao-0", "uid": uid, "resourceVersion": "3", "labels": deploymentplan.Object{"controller-revision-hash": "actual-target-revision"}, "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": r.UID}}}, "spec": deploymentplan.Clone(environmentPKIPod(r.Desired)), "status": deploymentplan.Object{"phase": "Running"}}
		b.objects[deploymentplan.ID(pod)] = pod
		if err := unsealEnvironmentCreateOpenBaoRestart(ctx, b.f.cfg, b.f.store, b.f.runtime, pod); err != nil {
			return err
		}
		pod["status"] = deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}
		return errors.New("isolated interruption after genuine original-share unseal")
	}
	return b.productProtocolBackend.Wait(ctx, r)
}

func TestProductParserRecoveryOriginalStatusAndNestedUnsealResume(t *testing.T) {
	f := newProductProtocolFixture(t, true)
	ns := f.p.Stack + "-secrets"
	var source deploymentplan.Object
	for _, r := range f.l.SourceTrust {
		if r.Baseline["kind"] == "ConfigMap" {
			source = deploymentplan.Clone(r.Baseline)
			break
		}
	}
	deploymentplan.Map(source["metadata"])["namespace"] = ns
	deploymentplan.Map(source["metadata"])["uid"] = "66666666-6666-4666-8666-666666666666"
	f.b.objects[deploymentplan.ID(source)] = source
	f.l.SourceOwners[deploymentplan.ID(source)] = deploymentplan.String(deploymentplan.Map(source["metadata"])["uid"])
	f.l.SourceTrust = append(f.l.SourceTrust, deploymentplan.Resource{ID: deploymentplan.ID(source), Baseline: deploymentplan.Clone(source)})
	f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(source), Phase: "configuration"})
	id := "StatefulSet/" + ns + "/openbao"
	uid := "77777777-7777-4777-8777-777777777777"
	set := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "openbao", "uid": uid, "resourceVersion": "1", "generation": float64(1), "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack}}, "status": deploymentplan.Object{"readyReplicas": float64(1)}, "spec": deploymentplan.Object{"replicas": float64(1), "serviceName": "openbao", "updateStrategy": deploymentplan.Object{"type": "OnDelete"}, "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "openbao"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "openbao"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": f.p.Image, "volumeMounts": []any{deploymentplan.Object{"name": "service-trust", "mountPath": "/run/service-trust", "readOnly": true}}}, deploymentplan.Object{"name": "openbao-pki", "image": f.p.Image}}, "volumes": []any{deploymentplan.Object{"name": "service-trust", "configMap": deploymentplan.Object{"name": "pki-create-service-trust"}}}}}}}
	f.b.objects[id] = set
	f.l.SourceOwners[id] = uid
	f.l.SourceTrust = append(f.l.SourceTrust, deploymentplan.Resource{ID: id, Baseline: deploymentplan.Clone(set)})
	f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(set), Phase: "workload"})
	for i := range f.l.SourceTrust {
		if f.l.SourceTrust[i].Baseline["kind"] != "ConfigMap" {
			f.l.SourceTrust[i].Baseline["status"] = deploymentplan.Object{"observedGeneration": float64(1), "readyReplicas": float64(1)}
		}
	}
	share := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	init := mustProviderRecoveryJSON(deploymentplan.Object{"unseal_keys_b64": []string{share}, "root_token": "isolated-original-token"})
	for path, raw := range map[string][]byte{"openbao/init.json": init, "openbao/unseal-key": []byte(share), "openbao/root-token": []byte("isolated-original-token")} {
		if err := f.store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.e.BeginEntry("openbao-initialize", "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"original": id}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.RecordReceipt("openbao-initialize", mustProviderRecoveryJSON(deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(init), "statefulset_uid": uid, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var sealed atomic.Bool
	sealed.Store(true)
	var unseals atomic.Int32
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/boundary/") {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request struct {
				Args []string `json:"args"`
			}
			_ = json.Unmarshal(raw, &request)
			joined := strings.Join(request.Args, " ")
			if strings.Contains(joined, "bao status -format=json") || strings.Contains(joined, "bao write -format=json sys/unseal") {
				if strings.Contains(joined, "sys/unseal") {
					sealed.Store(false)
					unseals.Add(1)
				}
				body := mustProviderRecoveryJSON(deploymentplan.Object{"initialized": true, "sealed": sealed.Load()})
				_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(body), "code": 0})
				return
			}
		}
		f.serve(w, r)
	})
	digest := deploymentplan.Digest("frozen-real-status-inputs")
	p := createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), HandoffSHA256: deploymentplan.Digest(f.h.Pins), ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(f.cfg), ClusterSHA256: digest, IdentitySHA256: digest, Release: environmentRelease{Environment: "dev", Stack: f.p.Stack}, Bindings: environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}, Products: f.l, Definitions: f.l.SourceDefinitions}
	checks := map[string]func(context.Context) error{}
	for _, gate := range deploymentplan.RequiredGateIDs(false) {
		checks[gate] = f.e.Check
	}
	q, err := qualifyCreateNormalTailProgram(t.Context(), &p, checks, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := createNormalTailProgramArtifact{Program: p, Qualification: q, CheckerSHA256: checker, Generation: digest, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	if err = saveCreateNormalTailProgramArtifact(f.store, f.e, a); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvironmentPreflightActive(f.store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: digest, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(p), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(p) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	admission, err := admitCreateNormalTailProgramWithFence(t.Context(), f.cfg, f.h, a, time.Hour, time.Now().UTC(), f.e.Check)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.ProductIdentityScope = productIdentityScope(f.l)
	f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
	ready := f.next
	ready.Document = deploymentplan.Clone(ready.Document)
	ready.Status, ready.Document["status"] = "ready", "ready"
	defs, err := productIdentityTrustStageDefinitions(f.l, ready)
	if err != nil {
		t.Fatal(err)
	}
	full := "product-identities-" + productIdentityScope(f.l)[:12] + "-successor-trust"
	var live []deploymentplan.Object
	for _, o := range f.b.objects {
		live = append(live, o)
	}
	child, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveCreateNormalTailStagePlan(t.Context(), f.store, f.e, full, child); err != nil {
		t.Fatal(err)
	}
	start := len(f.e.Journal.Entries)
	backend := &productParserUnsealBackend{productProtocolBackend: f.b, f: f}
	run := func() error {
		return executeCreateNormalTailStageBackend(t.Context(), f.store, f.h, full, child, backend, func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, f.store.Root)
		}, func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, io.Discard, func(context.Context) error { return nil })
	}
	if err = run(); err == nil || !strings.Contains(err.Error(), "after genuine original-share unseal") {
		t.Fatal("expected genuine interrupted child", err)
	}
	entries := f.e.Journal.Entries[start:]
	if len(entries) != 2 || entries[0].State != "pending" || entries[1].State != "completed" || unseals.Load() != 1 {
		t.Fatal("not the actual pending-parent/completed-native shape", len(entries), unseals.Load())
	}
	read := func(e *environmentCreateExecution) *productStageReader {
		r := &productStageReader{store: f.store, e: e, l: f.l, entries: e.Journal.Entries[start:], admission: admission}
		r.kube("successor-trust", defs, nil)
		return r
	}
	if r := read(f.e); r.err != nil || r.n != 2 {
		t.Fatal("status-bearing native baseline or genuine nested receipt rejected", r.err, r.n)
	}
	originalJournal, _, err := child.ReadExecution(f.store.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"foreign-key", "foreign-controller", "missing-owner", "foreign-owner", "sealed", "unknown-pending", "outside-parent"} {
		t.Run(mode, func(t *testing.T) {
			view := *f.e
			view.Journal = f.e.Journal
			view.Journal.Entries = append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
			e := &view.Journal.Entries[start+1]
			j := originalJournal
			j.Owners = map[string]string{}
			for k, v := range originalJournal.Owners {
				j.Owners[k] = v
			}
			switch mode {
			case "foreign-key":
				e.Key = "openbao-managed-unseal-99999999-9999-4999-8999-999999999999"
			case "foreign-controller":
				e.IntentSHA256 = activeTransportJournalDigest(deploymentplan.Object{"pod_uid": "88888888-8888-4888-8888-888888888888", "controller_uid": "99999999-9999-4999-8999-999999999999", "revision": "actual-target-revision", "original_init_sha256": environmentPreflightBytesSHA256(init), "unseal_sha256": environmentPreflightBytesSHA256([]byte(share))})
			case "missing-owner":
				delete(j.Owners, id)
			case "foreign-owner":
				j.Owners[id] = "99999999-9999-4999-8999-999999999999"
			case "sealed":
				e.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"pod_uid": "88888888-8888-4888-8888-888888888888", "revision": "actual-target-revision", "initialized": true, "sealed": true})
			case "unknown-pending":
				e.State = "pending"
				e.Receipt = nil
				e.CompletedAt = time.Time{}
			case "outside-parent":
				e.StartedAt = entries[0].StartedAt.Add(-time.Second)
			}
			path := filepath.Join("deployment/rollback", child.SHA256(), "journal.json")
			if err := f.store.write(path, mustProviderRecoveryJSON(j), true); err != nil {
				t.Fatal(err)
			}
			if r := read(&view); r.err == nil {
				t.Fatal("unbound nested receipt was accepted")
			}
			if err := f.store.write(path, mustProviderRecoveryJSON(originalJournal), true); err != nil {
				t.Fatal(err)
			}
			if unseals.Load() != 1 {
				t.Fatal("history reader repeated native unseal")
			}
		})
	}
	writes := map[string]int{}
	for id, n := range f.b.writes {
		writes[id] = n
	}
	if err = run(); err != nil {
		t.Fatal("same original child did not resume readiness", err)
	}
	if r := read(f.e); r.err != nil || r.n != 2 || !f.e.Journal.Entries[start].CompletedAt.After(f.e.Journal.Entries[start+1].CompletedAt) {
		t.Fatal("completed outer interval rejected", r.err, r.n)
	}
	for id, n := range writes {
		if f.b.writes[id] != n {
			t.Fatal("repeated already written resource", id)
		}
	}
	if unseals.Load() != 1 {
		t.Fatal("native unseal was replayed")
	}
}
