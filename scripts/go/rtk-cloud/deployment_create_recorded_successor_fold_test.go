package main

import (
	"context"
	"io"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestFoldCompletedRecordedSuccessorsRequiresContinuousSameOwner(t *testing.T) {
	obj := func(value string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "ca", "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"ca.crt": value}}
	}
	before, first, second, third := obj("before"), obj("first"), obj("second"), obj("third")
	id := deploymentplan.ID(before)
	base := []activeTransportRenewalResource{{ID: id, UID: "original", Before: before, Desired: first}}
	stage := func(before, after deploymentplan.Object, action string) completedRecordedSuccessorStage {
		p := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, UID: "original", Action: action, Baseline: before, Desired: after}}}
		return completedRecordedSuccessorStage{Plan: p, Journal: deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: "original"}, Accepted: true}}
	}
	stages := []completedRecordedSuccessorStage{stage(first, second, "update"), stage(second, second, "retain"), stage(second, third, "update")}
	result, err := foldCompletedRecordedSuccessors(base, stages)
	if err != nil || len(result) != 1 || deploymentplan.Digest(result[0].Before) != deploymentplan.Digest(before) || deploymentplan.Digest(result[0].Desired) != deploymentplan.Digest(third) || deploymentplan.Digest(base[0].Desired) != deploymentplan.Digest(first) {
		t.Fatal("completed chain not folded", err)
	}
	for _, mode := range []string{"pending", "wrong-owner", "drift", "retired", "changed-retain"} {
		t.Run(mode, func(t *testing.T) {
			steps := []completedRecordedSuccessorStage{stage(first, second, "update")}
			s := &steps[0]
			switch mode {
			case "pending":
				s.Journal.Accepted = false
			case "wrong-owner":
				s.Journal.Owners[id] = "foreign"
			case "drift":
				s.Plan.Resources[0].Baseline = obj("foreign")
				s.Journal.PlanSHA256 = s.Plan.SHA256()
			case "retired":
				s.Plan.Resources[0].Action = "retire"
				s.Journal.PlanSHA256 = s.Plan.SHA256()
			case "changed-retain":
				s.Plan.Resources[0].Action = "retain"
				s.Journal.PlanSHA256 = s.Plan.SHA256()
			}
			if _, err := foldCompletedRecordedSuccessors(base, steps); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestCompletedRecordedSuccessorFoldsNativeChildJournal(t *testing.T) {
	store, e, initial, b := freshStageFixture(t)
	check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
	execute := func(stage string, p *deploymentplan.Plan) {
		t.Helper()
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	execute("first", initial)
	id := initial.Resources[0].ID
	live := deploymentplan.Clone(b.objects[id])
	uid := deploymentplan.String(deploymentplan.Map(live["metadata"])["uid"])
	review := strings.Repeat("a", 64)
	key := "initial-transport-successor-" + review
	now := time.Now().UTC()
	if err := e.BeginEntry(key, "recovery", []byte(`{"source":"fixture"}`), now); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt(key, []byte(`{"accepted":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(key, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	base := []activeTransportRenewalResource{{ID: id, UID: uid, Before: initial.Resources[0].Desired, Desired: initial.Resources[0].Desired}}
	next := deploymentplan.Clone(live)
	deploymentplan.Map(next["data"])["root"] = "managed-overlap"
	p, err := deploymentplan.Compile(initial.Inputs, []deploymentplan.Definition{{Object: next, Phase: "configuration"}}, []deploymentplan.Object{live})
	if err != nil {
		t.Fatal(err)
	}
	execute("new-accepted-child", p)
	relative, err := filepath.Rel(store.Root, e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	proof := initialTransportSuccessorProof{Review: review, Journal: filepath.ToSlash(relative)}
	got, err := completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base)
	if err != nil || len(got) != 1 || deploymentplan.Map(got[0].Desired["data"])["root"] != "managed-overlap" {
		t.Fatal("native child not folded", err)
	}
	// The completed session reference follows the managed update. The folded
	// transport result already contains both, so managed must not run twice.
	reference := deploymentplan.Clone(b.objects[id])
	deploymentplan.Map(reference["data"])["bootstrap-session"] = "renewed-original-session"
	referencePlan, err := deploymentplan.Compile(initial.Inputs, []deploymentplan.Definition{{Object: reference, Phase: "configuration"}}, []deploymentplan.Object{b.objects[id]})
	if err != nil {
		t.Fatal(err)
	}
	execute("service-session-reference-fixture", referencePlan)
	got, err = completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"first", "new-accepted-child", "service-session-reference-fixture"} {
		if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/"+stage); err != nil {
			t.Fatal("accepted stage did not follow the complete native history", stage, err)
		}
	}
	// Exercise original execution as well as preflight qualification after the
	// managed update and session-reference handoff have both completed.
	originalWrites, originalLeases := deploymentplan.Digest(b.writes), b.leases
	for stage, original := range map[string]*deploymentplan.Plan{"first": initial, "new-accepted-child": p, "service-session-reference-fixture": referencePlan} {
		execute(stage, original)
	}
	if deploymentplan.Digest(b.writes) != originalWrites || b.leases != originalLeases {
		t.Fatal("resuming accepted history replayed a native child")
	}
	credentials, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/first")
	if err != nil {
		t.Fatal(err)
	}
	core := &deploymentplan.Plan{}
	coreJournal := deploymentplan.Journal{PlanSHA256: core.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	managed := []activeTransportRenewalResource{{ID: id, UID: uid, Before: p.Resources[0].Baseline, Desired: p.Resources[0].Desired}}
	if err := verifyIssuerCredentialOwners(t.Context(), b.Read, credentials, credentialJournal, core, coreJournal, got, managed); err == nil {
		t.Fatal("overlapping history did not reproduce the duplicate managed baseline failure")
	}
	if err := verifyIssuerCredentialOwnersAfterRecordedTransport(t.Context(), b.Read, credentials, credentialJournal, core, coreJournal, got, managed); err != nil {
		t.Fatal("completed session reference was rejected after authenticated history folding", err)
	}
	if deploymentplan.Map(got[0].Desired["data"])["bootstrap-session"] != "renewed-original-session" {
		t.Fatal("session reference was lost")
	}
	// A credential outside the transport chain still follows its managed write.
	only := deploymentplan.Clone(initial.Resources[0].Desired)
	deploymentplan.Map(only["metadata"])["name"] = "managed-only"
	onlyID := deploymentplan.ID(only)
	onlyNext := deploymentplan.Clone(only)
	deploymentplan.Map(onlyNext["data"])["root"] = "managed-only-update"
	credentials.Resources = append(credentials.Resources, deploymentplan.Resource{ID: onlyID, Action: "retain", UID: uid, Desired: only})
	credentialJournal.PlanSHA256 = credentials.SHA256()
	credentialJournal.Completed = append(credentialJournal.Completed, onlyID)
	credentialJournal.Owners[onlyID] = uid
	b.objects[onlyID] = deploymentplan.Clone(onlyNext)
	deploymentplan.Map(b.objects[onlyID]["metadata"])["uid"] = uid
	managed = append(managed, activeTransportRenewalResource{ID: onlyID, UID: uid, Before: only, Desired: onlyNext})
	if err := verifyIssuerCredentialOwnersAfterRecordedTransport(t.Context(), b.Read, credentials, credentialJournal, core, coreJournal, got, managed); err != nil {
		t.Fatal("non-overlapping managed credential write was lost", err)
	}
	managed[1].UID = "foreign-owner"
	if err := verifyIssuerCredentialOwnersAfterRecordedTransport(t.Context(), b.Read, credentials, credentialJournal, core, coreJournal, got, managed); err == nil {
		t.Fatal("accepted a foreign managed-only credential owner")
	}
	proof.Journal = "deployment/create/another-generation/journal.json"
	if _, err = completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base); err == nil {
		t.Fatal("accepted a journal outside the selected generation")
	}
}

// Device's original application write and q1 were recorded but their stages
// never passed readiness. q2 and the CA overlap later completed independently.
func TestRecordedOwnerChainDeviceWrittenQ1Q2AndCAOverlap(t *testing.T) {
	deployment := func(command string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api-pki", "namespace": "example"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "api", "image": "configured", "command": []any{command}}}}}}}
	}
	ca := func(value string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "openbao-ca", "namespace": "example"}, "data": deploymentplan.Object{"ca.crt": value}}
	}
	deviceBase, deviceApp, q1, q2 := deployment("transport"), deployment("application"), deployment("q1"), deployment("q2")
	oldCA, overlap := ca("bootstrap"), ca("bootstrap+managed")
	deviceID, caID := deploymentplan.ID(deviceBase), deploymentplan.ID(oldCA)
	base := []activeTransportRenewalResource{{ID: deviceID, UID: "device-uid", Before: deviceBase, Desired: deviceBase}, {ID: caID, UID: "ca-uid", Before: oldCA, Desired: oldCA}}
	step := func(before, after deploymentplan.Object, uid string, accepted bool) completedRecordedSuccessorStage {
		id := deploymentplan.ID(before)
		p := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, UID: uid, Action: "update", Baseline: before, Desired: after}}}
		j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: uid}, Accepted: accepted}
		if !accepted {
			j.Failed = id
		}
		return completedRecordedSuccessorStage{Plan: p, Journal: j, Written: !accepted}
	}
	stages := []completedRecordedSuccessorStage{step(deviceBase, deviceApp, "device-uid", false), step(deviceApp, q1, "device-uid", false), step(q1, q2, "device-uid", true), step(oldCA, overlap, "ca-uid", true)}
	got, err := foldCompletedRecordedSuccessors(base, stages)
	if err != nil || !deploymentplan.Matches(q2, got[0].Desired) || !deploymentplan.Matches(overlap, got[1].Desired) {
		t.Fatal("recorded same-owner chain failed", err)
	}
	for _, mode := range []string{"unrecorded", "wrong-uid", "wrong-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			invalid := append([]completedRecordedSuccessorStage(nil), stages...)
			switch mode {
			case "unrecorded":
				invalid[1].Journal.Completed = nil
				invalid[1].Journal.Failed = ""
				invalid[1].Journal.Owners = map[string]string{}
			case "wrong-uid":
				invalid[1].Journal.Owners = map[string]string{deviceID: "foreign"}
			case "wrong-predecessor":
				invalid[1] = step(deviceBase, q1, "device-uid", false)
			}
			if _, err := foldCompletedRecordedSuccessors(base, invalid); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestCompletedRecordedSuccessorAuthenticatesForwardAlias(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstarted-source", true: "recorded-write-prefix"}[written], func(t *testing.T) {
			store, e, initial, b := freshStageFixture(t)
			check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
			execute := func(stage string, p *deploymentplan.Plan) error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, check, check, func(context.Context) error { return nil }, io.Discard)
			}
			if err := execute("first", initial); err != nil {
				t.Fatal(err)
			}
			id := initial.Resources[0].ID
			live := deploymentplan.Clone(b.objects[id])
			uid := deploymentplan.String(deploymentplan.Map(live["metadata"])["uid"])
			review := strings.Repeat("a", 64)
			key := "initial-transport-successor-" + review
			now := time.Now().UTC()
			if err := e.BeginEntry(key, "recovery", []byte(`{"source":"fixture"}`), now); err != nil {
				t.Fatal(err)
			}
			if err := e.RecordReceipt(key, []byte(`{"accepted":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry(key, now); err != nil {
				t.Fatal(err)
			}
			base := []activeTransportRenewalResource{{ID: id, UID: uid, Before: initial.Resources[0].Desired, Desired: initial.Resources[0].Desired}}
			compile := func(value string) *deploymentplan.Plan {
				body := deploymentplan.Clone(b.objects[id])
				deploymentplan.Map(body["data"])["root"] = value
				p, err := deploymentplan.Compile(initial.Inputs, []deploymentplan.Definition{{Object: body, Phase: "configuration"}}, []deploymentplan.Object{b.objects[id]})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			source := compile("interrupted")
			if written {
				b.failure = "after-write-configuration"
				if err := execute("source", source); err == nil {
					t.Fatal("source did not stop after recording its write")
				}
				b.failure = ""
			} else {
				if err := saveEnvironmentCreateStageInput(t.Context(), store, e, "source", source); err != nil {
					t.Fatal(err)
				}
				intent, err := environmentCreateKubernetesStageIntent(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := e.BeginEntry("kubernetes/source", "source", intent, now); err != nil {
					t.Fatal(err)
				}
			}
			forward := compile("managed")
			if err := execute("forward", forward); err != nil {
				t.Fatal(err)
			}
			alias := map[string]string{"actual_qualified_forward_stage": "kubernetes/forward", "original_failed_plan_sha256": source.SHA256()}
			if err := e.RecordReceipt("kubernetes/source", mustProviderRecoveryJSON(alias)); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("kubernetes/source", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(store.Root, e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			proof := initialTransportSuccessorProof{Review: review, Journal: filepath.ToSlash(relative)}
			got, err := completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base)
			if err != nil || len(got) != 1 || deploymentplan.Map(got[0].Desired["data"])["root"] != "managed" {
				t.Fatal("authenticated completed forward was rejected", err)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if _, err := completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base); err != nil {
				t.Fatal(err)
			}
			assertRecoveryContractReadOnly(t, store, before)
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/source")
			original := append([]byte(nil), entry.Receipt...)
			for _, field := range []string{"actual_qualified_forward_stage", "original_failed_plan_sha256"} {
				bad := map[string]string{"actual_qualified_forward_stage": "kubernetes/forward", "original_failed_plan_sha256": source.SHA256()}
				bad[field] = "foreign"
				entry.Receipt = mustProviderRecoveryJSON(bad)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
				if _, err := completedRecordedTransportSuccessors(t.Context(), store, e, b, proof, base); err == nil {
					t.Fatal("changed forward alias was accepted", field)
				}
			}
			entry.Receipt = original
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecordedNativeTemplateRestoreKeepsOriginalOwnerAndBaseline(t *testing.T) {
	object := func(value string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "original"}, "data": deploymentplan.Object{"value": value}}
	}
	original, interrupted, next := object("original"), object("interrupted"), object("next")
	id := deploymentplan.ID(original)
	base := []activeTransportRenewalResource{{ID: id, UID: "original-uid", Before: original, Desired: original}}
	step := func(before, after deploymentplan.Object) completedRecordedSuccessorStage {
		p := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, UID: "original-uid", Action: "update", Baseline: before, Desired: after}}}
		return completedRecordedSuccessorStage{Plan: p, Journal: deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: "original-uid"}, Accepted: true}}
	}
	write := &activeTransportRenewalResource{ID: id, UID: "original-uid", Before: interrupted, Desired: original}
	stages := []completedRecordedSuccessorStage{step(original, interrupted), {NativeWrite: write}, step(original, next)}
	got, err := foldCompletedRecordedSuccessors(base, stages)
	if err != nil || deploymentplan.Digest(got[0].Desired) != deploymentplan.Digest(next) {
		t.Fatal("completed exact template restoration was omitted", err)
	}
	for _, mode := range []string{"owner", "baseline", "resource"} {
		bad := *write
		switch mode {
		case "owner":
			bad.UID = "foreign"
		case "baseline":
			bad.Before = next
		case "resource":
			bad.Desired = object("foreign")
			deploymentplan.Map(bad.Desired["metadata"])["name"] = "foreign"
		}
		stages[1].NativeWrite = &bad
		if _, err := foldCompletedRecordedSuccessors(base, stages); err == nil {
			t.Fatal("changed native restoration accepted", mode)
		}
	}
}

func TestRecordedRetainComparesDeclaredStateAgainstNativeDefaults(t *testing.T) {
	full := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "retained", "namespace": "example"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "api", "image": "configured", "imagePullPolicy": "IfNotPresent"}}}}}}
	minimal := deploymentplan.Clone(full)
	delete(deploymentplan.Map(minimal["spec"]), "replicas")
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(minimal)["containers"])[0])
	delete(container, "imagePullPolicy")
	id := deploymentplan.ID(full)
	p := &deploymentplan.Plan{Version: deploymentplan.Version, Resources: []deploymentplan.Resource{{ID: id, UID: "original", Action: "retain", Baseline: full, Desired: minimal}}}
	j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: "original"}, Accepted: true}
	got, err := foldCompletedRecordedSuccessors([]activeTransportRenewalResource{{ID: id, UID: "original", Before: full, Desired: full}}, []completedRecordedSuccessorStage{{Plan: p, Journal: j}})
	if err != nil || deploymentplan.Digest(got[0].Desired) != deploymentplan.Digest(full) {
		t.Fatal("retained qualified subset discarded or rejected native defaults", err)
	}
}

func TestRecordedRetainAcceptsKubernetesAllocatedServiceFields(t *testing.T) {
	const uid = "346e6133-4e39-4036-9230-ac1a2ccb473a"
	original := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "certissuer", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"port": float64(443), "targetPort": float64(18445)}}, "selector": deploymentplan.Object{"app": "certissuer"}}}
	spec := deploymentplan.Map(original["spec"])
	spec["clusterIP"] = "10.128.1.9"
	spec["clusterIPs"] = []any{"10.128.1.9"}
	spec["ipFamilies"] = []any{"IPv4"}
	spec["ipFamilyPolicy"] = "SingleStack"
	observed := deploymentplan.Clone(original)
	deploymentplan.Map(observed["metadata"])["uid"] = uid
	deploymentplan.Map(observed["spec"])["type"] = "ClusterIP"
	retainedIntent := deploymentplan.Clone(original)
	deploymentplan.Map(retainedIntent["spec"])["type"] = "ClusterIP"
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	plan, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: retainedIntent, Phase: "routing"}}, []deploymentplan.Object{observed})
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "retain" {
		t.Fatal("native Service allocation should be retained", err)
	}
	id := deploymentplan.ID(original)
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{id}, Owners: map[string]string{id: uid}}
	base := []activeTransportRenewalResource{sessionRecordedOwnerBase(id, uid, original)}
	folded, err := foldCompletedRecordedSuccessors(base, []completedRecordedSuccessorStage{{Plan: plan, Journal: journal}})
	if err != nil || len(folded) != 1 || !deploymentplan.Matches(original, folded[0].Desired) {
		t.Fatal("accepted no-write Service defaults broke the original owner history", err)
	}
	changed := *plan
	changed.Resources = append([]deploymentplan.Resource(nil), plan.Resources...)
	changed.Resources[0].Desired = deploymentplan.Clone(plan.Resources[0].Desired)
	deploymentplan.Map(changed.Resources[0].Desired["spec"])["clusterIP"] = "10.128.1.10"
	journal.PlanSHA256 = changed.SHA256()
	if _, err := foldCompletedRecordedSuccessors(base, []completedRecordedSuccessorStage{{Plan: &changed, Journal: journal}}); err == nil {
		t.Fatal("retain accepted an address different from its recorded native baseline")
	}
}
