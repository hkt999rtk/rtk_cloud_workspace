package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestServiceSessionSIDForwardChangesOnlyRecordedDeploymentEnv(t *testing.T) {
	old := "11111111-1111-4111-8111-111111111111"
	next := "22222222-2222-4222-8222-222222222222"
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	makeObject := func(kind, name string) deploymentplan.Object {
		containers := []any{
			deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old}, deploymentplan.Object{"name": "UNCHANGED", "value": "keep"}}, "volumeMounts": []any{deploymentplan.Object{"name": "pki", "mountPath": "/secret"}}},
			deploymentplan.Object{"name": "sidecar", "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old}}},
		}
		pod := deploymentplan.Object{"containers": containers, "volumes": []any{deploymentplan.Object{"name": "pki", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original"}}}}
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": pod}}}
	}

	a, b, job := makeObject("Deployment", "a"), makeObject("Deployment", "b"), makeObject("Job", "old-job")
	other := makeObject("Deployment", "unrelated")
	for _, raw := range certIssuerObjectList(environmentPKIPod(other)["containers"]) {
		for _, item := range certIssuerObjectList(deploymentplan.Map(raw)["env"]) {
			v := deploymentplan.Map(item)
			if v["name"] == "PKI_BOOTSTRAP_SESSION_ID" {
				v["value"] = "33333333-3333-4333-8333-333333333333"
			}
		}
	}
	job["apiVersion"] = "batch/v1"
	defs := []deploymentplan.Definition{{Object: a, Phase: "workload"}, {Object: b, Phase: "workload"}, {Object: job, Phase: "workload"}, {Object: other, Phase: "workload"}}
	source, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: source.SHA256(), Owners: map[string]string{}}
	live := []deploymentplan.Object{}
	for _, r := range source.Resources {
		if deploymentplan.Map(r.Desired["metadata"])["name"] == "b" {
			continue
		}
		o := deploymentplan.Clone(r.Desired)
		uid := "uid-" + deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])
		deploymentplan.Map(o["metadata"])["uid"] = uid
		j.Owners[r.ID] = uid
		j.Completed = append(j.Completed, r.ID)
		live = append(live, o)
	}
	base := deploymentplan.Digest(source)
	baseline := deploymentplan.Digest(live)
	candidate, pending, err := compileServiceSessionSIDForward([]serviceSessionRecordedPlan{{Plan: source, Journal: j}}, live, old, next, "sid-update")
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || len(candidate.Resources) != 1 || candidate.Resources[0].ID != deploymentplan.ID(a) || candidate.Resources[0].UID != "uid-a" || candidate.Resources[0].Action != "update" {
		t.Fatal("wrong candidate")
	}
	if len(pending) != 2 {
		t.Fatal("missing job/unwritten pending", pending)
	}
	expect := deploymentplan.Clone(source.Resources[0].Desired)
	if deploymentplan.ID(expect) != deploymentplan.ID(a) {
		for _, r := range source.Resources {
			if r.ID == deploymentplan.ID(a) {
				expect = deploymentplan.Clone(r.Desired)
			}
		}
	}
	for _, raw := range certIssuerObjectList(environmentPKIPod(expect)["containers"]) {
		c := deploymentplan.Map(raw)
		for _, item := range certIssuerObjectList(c["env"]) {
			v := deploymentplan.Map(item)
			if v["name"] == "PKI_BOOTSTRAP_SESSION_ID" {
				v["value"] = next
			}
		}
	}
	if deploymentplan.Digest(candidate.Resources[0].Desired) != deploymentplan.Digest(expect) || deploymentplan.Digest(source) != base || deploymentplan.Digest(live) != baseline {
		t.Fatal("changed unrelated state or source")
	}
	for _, mode := range []string{"replacement", "drift", "unrecorded", "duplicate slot"} {
		t.Run(mode, func(t *testing.T) {
			objects := make([]deploymentplan.Object, len(live))
			for i, o := range live {
				objects[i] = deploymentplan.Clone(o)
			}
			rec := serviceSessionRecordedPlan{Plan: source, Journal: j}
			var target deploymentplan.Object
			for _, object := range objects {
				if deploymentplan.ID(object) == deploymentplan.ID(a) {
					target = object
				}
			}
			if target == nil {
				t.Fatal("missing fixture target")
			}
			switch mode {
			case "replacement":
				deploymentplan.Map(target["metadata"])["uid"] = "replaced"
			case "drift":
				deploymentplan.Map(target["spec"])["replicas"] = float64(9)
			case "unrecorded":
				o := deploymentplan.Clone(target)
				deploymentplan.Map(o["metadata"])["name"] = "foreign"
				objects = append(objects, o)
			case "duplicate slot":
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(target)["containers"])[0])
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": old})
			}
			if _, _, err := compileServiceSessionSIDForward([]serviceSessionRecordedPlan{rec}, objects, old, next, "sid-update"); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func sessionSIDRecordedDeploymentFixture(t *testing.T, name, release string) (serviceSessionRecordedPlan, deploymentplan.Object) {
	t.Helper()
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: release, BindingsSHA256: strings.Repeat("c", 64)}
	body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": in.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "example/" + name + "@sha256:" + release, "env": []any{deploymentplan.Object{"name": "PKI_BOOTSTRAP_SESSION_ID", "value": "11111111-1111-4111-8111-111111111111"}}}}}}}}
	p, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: body, Phase: "workload"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(body)
	uid := "original-" + name
	live := deploymentplan.Clone(p.Resources[0].Desired)
	deploymentplan.Map(live["metadata"])["uid"] = uid
	return serviceSessionRecordedPlan{Stage: "source-" + name, Plan: p, Journal: deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: uid}, Accepted: true}}, live
}

func TestServiceSessionJoinsIndependentlyRecordedReleaseBindings(t *testing.T) {
	a, la := sessionSIDRecordedDeploymentFixture(t, "api", strings.Repeat("b", 64))
	b, lb := sessionSIDRecordedDeploymentFixture(t, "manager", strings.Repeat("d", 64))
	sources := []serviceSessionRecordedPlan{a, b}
	selected, err := selectLatestServiceSessionOwners(sources)
	if err != nil {
		t.Fatal("same-generation recorded releases were rejected", err)
	}
	old, next := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	p, pending, err := compileServiceSessionSIDForward(selected, []deploymentplan.Object{la, lb}, old, next, "session-forward")
	if err != nil || p == nil || len(p.Resources) != 2 || len(pending) != 0 {
		t.Fatal("independently bound release owners could not renew", err)
	}
	for _, r := range p.Resources {
		want := deploymentplan.Clone(r.Baseline)
		if _, err := serviceSessionSIDSlots(want, old, next); err != nil || !deploymentplan.Matches(r.Desired, want) {
			t.Fatal("session renewal changed the selected owner image/settings", err)
		}
	}
	bad := *b.Plan
	bad.ConfigurationSHA256 = strings.Repeat("e", 64)
	b.Plan = &bad
	b.Journal.PlanSHA256 = bad.SHA256()
	if _, _, err := compileServiceSessionSIDForward([]serviceSessionRecordedPlan{a, b}, []deploymentplan.Object{la, lb}, old, next, "session-forward"); err == nil {
		t.Fatal("changed original configuration binding was accepted")
	}
}

func TestServiceSessionRecognizesNativeControllerDescendants(t *testing.T) {
	source, live := sessionSIDRecordedDeploymentFixture(t, "api", strings.Repeat("b", 64))
	owner := deploymentplan.Map(live["metadata"])
	ns := owner["namespace"]
	rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "api-rs", "uid": "original-rs", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": owner["name"], "uid": owner["uid"], "controller": true}}}, "spec": deploymentplan.Clone(deploymentplan.Map(live["spec"]))}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": "api-pod", "uid": "original-pod", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "api-rs", "uid": "original-rs", "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(live))}
	old, next := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	for _, mode := range []string{"native", "recorded-parent-without-session", "standalone", "foreign-uid", "foreign-api-version", "non-controller"} {
		t.Run(mode, func(t *testing.T) {
			child := deploymentplan.Clone(pod)
			recorded, controller := source, deploymentplan.Clone(live)
			switch mode {
			case "recorded-parent-without-session":
				// An admitted parent update can retire its session before native
				// Pods/ReplicaSets roll over. The same recorded UID still owns them.
				if _, err := serviceSessionSIDSlots(controller, old, next); err != nil {
					t.Fatal(err)
				}
				body := deploymentplan.Clone(controller)
				delete(deploymentplan.Map(body["metadata"]), "uid")
				p, err := deploymentplan.Compile(source.Plan.Inputs, []deploymentplan.Definition{{Object: body, Phase: "workload"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				recorded.Plan = p
				recorded.Journal.PlanSHA256 = p.SHA256()
				recorded.SelectedIDs = map[string]bool{}
			case "standalone":
				delete(deploymentplan.Map(child["metadata"]), "ownerReferences")
			case "foreign-uid":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(child["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			case "foreign-api-version":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(child["metadata"])["ownerReferences"])[0])["apiVersion"] = "apps/v2"
			case "non-controller":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(child["metadata"])["ownerReferences"])[0])["controller"] = false
			}
			p, pending, err := compileServiceSessionSIDForward([]serviceSessionRecordedPlan{recorded}, []deploymentplan.Object{controller, rs, child}, old, next, "session-forward")
			if mode == "recorded-parent-without-session" {
				if err != nil || p != nil || len(pending) != 0 {
					t.Fatal("native descendants of an authenticated unselected owner were rejected", err)
				}
			} else if mode == "native" {
				if err != nil || p == nil || len(p.Resources) != 1 {
					t.Fatal("native descendants became independent owner blockers", err)
				}
			} else if err == nil {
				t.Fatal("unowned inherited session was accepted", mode)
			}
		})
	}
}

func TestServiceSessionRetainsAuthenticatedFailedBootstrapJob(t *testing.T) {
	source, live := sessionSIDRecordedDeploymentFixture(t, "api", strings.Repeat("b", 64))
	old, next := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	job := deploymentplan.Clone(live)
	job["apiVersion"], job["kind"] = "batch/v1", "Job"
	meta := deploymentplan.Map(job["metadata"])
	meta["name"], meta["uid"] = "failed-bootstrap", "original-failed-job"
	job["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": meta["namespace"], "name": "failed-bootstrap-pod", "uid": "11111111-2222-4333-8444-555555555555", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "name": meta["name"], "uid": meta["uid"], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(job)), "status": deploymentplan.Object{"phase": "Failed"}}
	history := serviceSessionHistoricalJob{ID: deploymentplan.ID(job), UID: "original-failed-job", verified: true, failedPodUID: "11111111-2222-4333-8444-555555555555", desired: deploymentplan.Clone(job)}
	for _, mode := range []string{"original", "unrecorded", "active-job", "active-pod", "foreign-pod", "foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			j, p := deploymentplan.Clone(job), deploymentplan.Clone(pod)
			proofs := []serviceSessionHistoricalJob{history}
			switch mode {
			case "unrecorded":
				proofs = nil
			case "active-job":
				deploymentplan.Map(j["status"])["active"] = float64(1)
			case "active-pod":
				deploymentplan.Map(p["status"])["phase"] = "Running"
			case "foreign-pod":
				deploymentplan.Map(p["metadata"])["uid"] = "foreign"
			case "foreign-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			}
			plan, _, err := compileServiceSessionSIDForward([]serviceSessionRecordedPlan{source}, []deploymentplan.Object{live, j, p}, old, next, "session-forward", proofs)
			if mode == "original" {
				if err != nil || plan == nil || len(plan.Resources) != 1 || plan.Resources[0].ID != deploymentplan.ID(live) {
					t.Fatal("retained failure blocked its unrelated active owner", err)
				}
			} else if err == nil {
				t.Fatal("unproved or active failed producer was ignored", mode)
			}
		})
	}
}
