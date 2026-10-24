package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateApplicationHistoryFixture(t *testing.T) (*createManagedApplicationPartialRecovery, map[string]deploymentplan.Resource) {
	t.Helper()
	_, _, partial, _ := managedApplicationThirdInterruptedFixture(t)
	// The original completed MQTT write changes bootstrap trust startup into
	// normal runtime. Final accepted applications merely retain that body.
	id := "Deployment/" + partial.Plan.Stack + "-video-cloud/mqtt-pki"
	var prior deploymentplan.Resource
	for index := range partial.Plan.Resources {
		resource := &partial.Plan.Resources[index]
		if resource.ID != id {
			continue
		}
		current := deploymentplan.Clone(resource.Desired)
		current["spec"] = deploymentplan.Object{
			"replicas": float64(1),
			"template": deploymentplan.Object{
				"spec": deploymentplan.Object{
					"containers": []any{deploymentplan.Object{"name": "pkibroker", "image": logACKNativeImage, "command": []any{"/app/pkibroker", "watch"}}},
				},
			},
		}
		resource.Desired = current
		for forward := partial.Interrupted; forward != nil; forward = forward.Previous {
			for i := range forward.Plan.Resources {
				if forward.Plan.Resources[i].ID == id {
					forward.Plan.Resources[i].Desired = deploymentplan.Clone(current)
					forward.Plan.Resources[i].Baseline = deploymentplan.Clone(current)
				}
			}
			forward.Journal.PlanSHA256 = forward.Plan.SHA256()
		}
		old := deploymentplan.Clone(current)
		container := certIssuerObjectList(environmentPKIPod(old)["containers"])[0]
		deploymentplan.Map(container)["command"] = []any{"/app/pkibroker", "--deployment-bootstrap-trust"}
		resource.Baseline = old
		prior = *resource
		prior.Desired = deploymentplan.Clone(old)
	}
	if prior.ID == "" || !environmentBootstrapOwnerUUID.MatchString(prior.UID) {
		t.Fatal("original owned MQTT fixture unavailable")
	}
	partial.Journal.PlanSHA256 = partial.Plan.SHA256()
	if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
		t.Fatal(err)
	}
	return partial, map[string]deploymentplan.Resource{id: prior}
}

func TestManagedLateApplicationOwnersIncludeOriginalWrittenRuntimeBeforeAcceptedRetains(t *testing.T) {
	partial, owners := managedLateApplicationHistoryFixture(t)
	latest := partial.Interrupted
	definitions, err := managedApplicationPartialDefinitions(partial)
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{}
	for index, resource := range latest.Plan.Resources {
		body := resource.Desired
		if index >= len(latest.Journal.Owners) {
			body = resource.Baseline
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
	}
	accepted, err := deploymentplan.Compile(latest.Plan.Inputs, definitions, live)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: accepted.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for _, resource := range accepted.Resources {
		journal.Completed = append(journal.Completed, resource.ID)
		journal.Owners[resource.ID] = resource.UID
	}
	// Skipping the original written generation cannot authorize the MQTT
	// retain against its older bootstrap body, despite the same original UID.
	without := map[string]deploymentplan.Resource{}
	for id, resource := range owners {
		without[id] = resource
	}
	if managedLateRecordedOwners(accepted, journal, without) == nil {
		t.Fatal("fixture failed to model missing original MQTT runtime authority")
	}
	before := deploymentplan.Digest(partial)
	if err := managedLateRecordedApplicationHistory(partial, owners); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		original := partial.Plan.Resources[index]
		if owners[original.ID].UID != partial.Journal.Owners[original.ID] || original.UID != "" || original.Baseline != nil {
			t.Fatal("actual original created PVC receipt lost or historical plan rewritten")
		}
	}
	for index, resource := range latest.Plan.Resources {
		if index >= len(latest.Journal.Owners) {
			continue
		}
		if owners[resource.ID].UID != latest.Journal.Owners[resource.ID] || deploymentplan.Digest(owners[resource.ID].Desired) != deploymentplan.Digest(resource.Desired) {
			t.Fatal("bounded latest genuine written body lost", resource.ID)
		}
	}
	if err := managedLateRecordedOwners(accepted, journal, owners); err != nil {
		t.Fatal("accepted current application retains rejected after original history", err)
	}
	if deploymentplan.Digest(partial) != before || partial.Interrupted.Journal.Accepted || partial.Interrupted.Previous.Journal.Accepted || partial.Interrupted.Previous.Previous.Journal.Accepted {
		t.Fatal("historical partial journals were modified or treated as accepted")
	}
}

func TestManagedLateApplicationOwnerHistoryRejectsChangedOriginalAuthority(t *testing.T) {
	base, originals := managedLateApplicationHistoryFixture(t)
	for _, test := range []struct {
		name string
		edit func(*createManagedApplicationPartialRecovery, map[string]deploymentplan.Resource)
	}{
		{"original journal hash", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			p.Journal.PlanSHA256 = strings.Repeat("9", 64)
		}},
		{"foreign predecessor UID", func(_ *createManagedApplicationPartialRecovery, owners map[string]deploymentplan.Resource) {
			for id, resource := range owners {
				resource.UID = "99999999-1111-4111-8111-111111111111"
				owners[id] = resource
			}
		}},
		{"changed predecessor recipe", func(_ *createManagedApplicationPartialRecovery, owners map[string]deploymentplan.Resource) {
			for id, resource := range owners {
				deploymentplan.Map(environmentPKIPod(resource.Desired)["containers"].([]any)[0])["image"] = "foreign"
				owners[id] = resource
			}
		}},
		{"original allocated claim UID", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			p.Journal.Owners[p.Plan.Resources[0].ID] = "99999999-1111-4111-8111-111111111111"
		}},
		{"latest completed UID", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			p.Interrupted.Journal.Owners[p.Interrupted.Plan.Resources[21].ID] = "99999999-1111-4111-8111-111111111111"
		}},
		{"accepted historical child", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			p.Interrupted.Journal.Accepted = true
		}},
		{"missing original MQTT write", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			delete(p.Journal.Owners, p.Plan.Resources[21].ID)
		}},
		{"fourth forward", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			p.Interrupted.Previous.Previous.Previous = p.Interrupted
		}},
		{"wrong intermediate recipe", func(p *createManagedApplicationPartialRecovery, _ map[string]deploymentplan.Resource) {
			f := p.Interrupted.Previous
			deploymentplan.Map(environmentPKIPod(f.Plan.Resources[22].Desired)["containers"].([]any)[0])["image"] = "foreign"
			f.Journal.PlanSHA256 = f.Plan.SHA256()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var partial createManagedApplicationPartialRecovery
			raw, _ := json.Marshal(base)
			if err := json.Unmarshal(raw, &partial); err != nil {
				t.Fatal(err)
			}
			owners := map[string]deploymentplan.Resource{}
			raw, _ = json.Marshal(originals)
			if err := json.Unmarshal(raw, &owners); err != nil {
				t.Fatal(err)
			}
			test.edit(&partial, owners)
			if managedLateRecordedApplicationHistory(&partial, owners) == nil {
				t.Fatal("changed historical authority accepted")
			}
		})
	}
}

func TestManagedLatePartialAllocationRequiresActualCreateReceiptAndNoPriorOwner(t *testing.T) {
	uid := "11111111-1111-4111-8111-111111111111"
	object := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "original-trust", "namespace": "isolated"}}
	resource := deploymentplan.Resource{ID: deploymentplan.ID(object), Action: "create", Desired: object}
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{resource.ID: uid}}
	owners := map[string]deploymentplan.Resource{}
	if managedLateRecordedPartialOwners(plan, journal, owners) == nil {
		t.Fatal("Core partial helper must not admit empty planned creation UID")
	}
	if err := managedLateRecordedWrittenOwners(plan, journal, owners, true); err != nil || owners[resource.ID].UID != uid {
		t.Fatal("genuine allocation receipt rejected", err)
	}
	if managedLateRecordedWrittenOwners(plan, journal, owners, true) == nil {
		t.Fatal("same original create cannot replace an existing original owner")
	}
	for _, action := range []string{"update", "retain"} {
		changed := *plan
		changed.Resources = slices.Clone(plan.Resources)
		changed.Resources[0].Action = action
		changedJournal := journal
		changedJournal.PlanSHA256 = changed.SHA256()
		if managedLateRecordedWrittenOwners(&changed, changedJournal, map[string]deploymentplan.Resource{}, true) == nil {
			t.Fatal("non-create empty UID accepted", action)
		}
	}
}

func TestManagedLateTransportOwnersPreserveActualPartialPolicyBeforeAcceptedRetains(t *testing.T) {
	store, e, _ := managedTransportPostManagedFixture(t)
	base, prior, accepted, post, err := loadManagedTransportPostManaged(store, e)
	if err != nil {
		t.Fatal(err)
	}
	original, journal, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil {
		t.Fatal(err)
	}
	partial := &createManagedTransportPartialRecovery{Original: original, Journal: journal, PriorForward: prior, AcceptedForward: accepted, PostManaged: post}
	// The older transport fixtures use symbolic owner strings. This isolated
	// collector fixture models real Kubernetes UUID receipts consistently across
	// its already loaded generations; production owner guards remain unchanged.
	ids := map[string]string{}
	for _, frame := range []struct {
		plan    *deploymentplan.Plan
		journal *deploymentplan.Journal
	}{{partial.Original, &partial.Journal}, {prior.Previous.Plan, &prior.Previous.Journal}, {prior.Plan, &prior.Journal}, {accepted.Plan, &accepted.Journal}, {post.Plan, &post.Journal}} {
		for index := range frame.plan.Resources {
			resource := &frame.plan.Resources[index]
			uid := ids[resource.ID]
			if uid == "" {
				uid = fmt.Sprintf("%08d-1111-4111-8111-111111111111", len(ids)+1)
				ids[resource.ID] = uid
			}
			if resource.UID != "" {
				resource.UID = uid
			}
			if frame.journal.Owners[resource.ID] != "" {
				frame.journal.Owners[resource.ID] = uid
			}
		}
		frame.journal.PlanSHA256 = frame.plan.SHA256()
	}
	initial := map[string]deploymentplan.Resource{}
	for _, resource := range original.Resources {
		previous := resource
		previous.Desired = deploymentplan.Clone(resource.Baseline)
		initial[resource.ID] = previous
	}
	// The Controller and Service-trust CM enter the seven-resource policy
	// forward from their independently accepted issuer-stage baselines.
	for _, resource := range prior.Plan.Resources {
		if _, exists := initial[resource.ID]; exists || strings.HasSuffix(resource.ID, "/mqtt-pki-config") || strings.HasSuffix(resource.ID, "/mqtt-pki-runtime") {
			continue
		}
		previous := resource
		previous.Desired = deploymentplan.Clone(resource.Baseline)
		initial[resource.ID] = previous
	}
	owners := map[string]deploymentplan.Resource{}
	for id, resource := range initial {
		owners[id] = resource
	}
	if managedLateRecordedOwners(post.Plan, post.Journal, owners) == nil {
		t.Fatal("fixture did not expose skipped genuine transport writes")
	}
	owners = map[string]deploymentplan.Resource{}
	for id, resource := range initial {
		owners[id] = resource
	}
	before := deploymentplan.Digest(partial)
	if err := managedLateRecordedTransportHistory(partial, owners); err != nil {
		t.Fatal(err)
	}
	for _, resource := range post.Plan.Resources {
		if owners[resource.ID].UID != post.Journal.Owners[resource.ID] || deploymentplan.Digest(owners[resource.ID].Desired) != deploymentplan.Digest(resource.Desired) {
			t.Fatal("latest accepted transport body lost", resource.ID)
		}
	}
	if deploymentplan.Digest(partial) != before || prior.Journal.Accepted || prior.Previous.Journal.Accepted {
		t.Fatal("historical stopped transport children rewritten")
	}
	for _, test := range []struct {
		name string
		edit func(*createManagedTransportPartialRecovery)
	}{
		{"missing first write", func(p *createManagedTransportPartialRecovery) { delete(p.Journal.Owners, p.Original.Resources[1].ID) }},
		{"changed seven owner", func(p *createManagedTransportPartialRecovery) {
			p.PriorForward.Journal.Owners[p.PriorForward.Plan.Resources[0].ID] = "99999999-1111-4111-8111-111111111111"
		}},
		{"invented partial acceptance", func(p *createManagedTransportPartialRecovery) { p.PriorForward.Journal.Accepted = true }},
		{"missing first forward", func(p *createManagedTransportPartialRecovery) { p.PriorForward.Previous = nil }},
		{"third forward", func(p *createManagedTransportPartialRecovery) { p.PriorForward.Previous.Previous = p.PriorForward }},
		{"accepted artifact pin", func(p *createManagedTransportPartialRecovery) { p.AcceptedForward.ArtifactSHA = "" }},
		{"post source pin", func(p *createManagedTransportPartialRecovery) { p.PostManaged.SourceSHA = "" }},
		{"accepted false", func(p *createManagedTransportPartialRecovery) { p.AcceptedForward.Journal.Accepted = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed createManagedTransportPartialRecovery
			raw, _ := json.Marshal(partial)
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("fixture decode")
			}
			owners := map[string]deploymentplan.Resource{}
			raw, _ = json.Marshal(initial)
			if json.Unmarshal(raw, &owners) != nil {
				t.Fatal("owner fixture decode")
			}
			test.edit(&changed)
			if managedLateRecordedTransportHistory(&changed, owners) == nil {
				t.Fatal("changed historical transport authority accepted")
			}
		})
	}
}
