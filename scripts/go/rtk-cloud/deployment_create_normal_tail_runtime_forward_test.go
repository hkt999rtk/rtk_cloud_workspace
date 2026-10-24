package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func runtimeBillingPartialFixture(t *testing.T) (secretStore, createNormalTailRuntimeSource, createNormalTailRuntimeInputs, *deploymentplan.Plan, deploymentplan.Journal, map[string]deploymentplan.Object) {
	t.Helper()
	cfg, store, release, snapshot := billingConfigurationFixture(t, "dev")
	snapshot.Operator["PAYPAL_ENABLED"] = "true"
	for _, key := range []string{"paypal-client-id", "paypal-client-secret", "paypal-webhook-id"} {
		delete(snapshot.Secrets, key)
	}
	defs := billingConfigurationOriginal(t, cfg, store, release, snapshot)
	// These are frozen historical recipes, before the maintained finite correction.
	byID := map[string]deploymentplan.Object{}
	for _, d := range defs {
		byID[deploymentplan.ID(d.Object)] = d.Object
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	ns := stack + "-billing"
	ids := []string{"Secret/" + ns + "/billing-runtime", "Deployment/" + ns + "/billing", "Deployment/" + ns + "/billing-payment-worker", "Deployment/" + ns + "/payment-simulator"}
	p := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: stack, Operation: "create", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest("original bindings")}}
	observed := map[string]deploymentplan.Object{}
	secretUID := "11111111-1111-4111-8111-111111111111"
	billingUID := "22222222-2222-4222-8222-222222222222"
	for i, id := range ids {
		o := deploymentplan.Clone(byID[id])
		if o["kind"] == "Secret" {
			data := deploymentplan.Object{}
			for k, v := range deploymentplan.Map(o["stringData"]) {
				data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
			}
			o["data"] = data
			delete(o, "stringData")
		}
		if o == nil {
			t.Fatal("maintained Billing recipe absent", id)
		}
		r := deploymentplan.Resource{ID: id, Phase: "workload", Action: "create", Desired: o}
		if i == 0 {
			r.Phase = "configuration"
			r.Action = "retain"
			r.UID = secretUID
			r.Baseline = deploymentplan.Clone(o)
			deploymentplan.Map(r.Baseline["metadata"])["uid"] = secretUID
		}
		p.Resources = append(p.Resources, r)
		if i < 2 {
			current := deploymentplan.Clone(o)
			uid := secretUID
			if i == 1 {
				uid = billingUID
			}
			deploymentplan.Map(current["metadata"])["uid"] = uid
			deploymentplan.Map(current["metadata"])["resourceVersion"] = "7"
			observed[id] = current
		}
	}
	j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{ids[0]}, Owners: map[string]string{ids[0]: secretUID, ids[1]: billingUID}, Failed: ids[1]}
	source := createNormalTailRuntimeSource{Schema: createNormalTailRuntimeSourceSchema, PreviousPlanSHA256: p.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(j), Configuration: cfg, Release: release, Snapshot: snapshot}
	inputs := createNormalTailRuntimeSelectedInputs(snapshot)
	inputs.Operator["PAYPAL_ENABLED"] = createNormalTailRuntimeValue{Present: true, Value: "false"}
	return store, source, inputs, p, j, observed
}
func TestCreateNormalTailRuntimeForwardFiniteBillingPreservesKnownOwners(t *testing.T) {
	store, source, inputs, p, j, observed := runtimeBillingPartialFixture(t)
	beforeP, beforeJ := deploymentplan.Digest(p), deploymentplan.Digest(j)
	next, err := buildCreateNormalTailRuntimeForward(store, source, inputs, p, j, observed)
	if err != nil {
		t.Fatal(err)
	}
	if next.ConfigurationSHA256 == p.ConfigurationSHA256 || next.Resources[0].Action != "update" || next.Resources[1].Action != "update" || next.Resources[0].UID != j.Owners[p.Resources[0].ID] || next.Resources[1].UID != j.Owners[p.Resources[1].ID] {
		t.Fatal("known Secret/Billing conditional updates missing")
	}
	for _, r := range next.Resources[2:] {
		if r.Action != "create" || r.UID != "" || r.Baseline != nil || deploymentplan.Map(r.Desired["spec"])["replicas"] != float64(0) {
			t.Fatal("unwritten providerless worker was adopted or activated", r.ID)
		}
	}
	if beforeP != deploymentplan.Digest(p) || beforeJ != deploymentplan.Digest(j) {
		t.Fatal("literal prior evidence mutated")
	}
	if err = validateCreateNormalTailRuntimeForward(store, source, inputs, p, j, next); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"known-uid", "known-body", "unknown-create", "foreign-desired", "input-release", "uncertain-native-write", "previous-journal", "foreign-baseline"} {
		t.Run(mode, func(t *testing.T) {
			var pp deploymentplan.Plan
			json.Unmarshal(mustProviderRecoveryJSON(p), &pp)
			var jj deploymentplan.Journal
			json.Unmarshal(mustProviderRecoveryJSON(j), &jj)
			var nn deploymentplan.Plan
			json.Unmarshal(mustProviderRecoveryJSON(next), &nn)
			oo := map[string]deploymentplan.Object{}
			for id, o := range observed {
				oo[id] = deploymentplan.Clone(o)
			}
			var gotErr error
			switch mode {
			case "known-uid":
				deploymentplan.Map(oo[p.Resources[1].ID]["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			case "known-body":
				deploymentplan.Map(oo[p.Resources[1].ID]["spec"])["replicas"] = float64(9)
			case "unknown-create":
				oo[p.Resources[2].ID] = deploymentplan.Clone(p.Resources[2].Desired)
			case "foreign-desired":
				deploymentplan.Map(nn.Resources[1].Desired["spec"])["replicas"] = float64(9)
			case "input-release":
				nn.ReleaseSHA256 = deploymentplan.Digest("foreign")
			case "uncertain-native-write":
				jj.InFlight = p.Resources[2].ID
			case "previous-journal":
				jj.Failed = p.Resources[2].ID
			case "foreign-baseline":
				deploymentplan.Map(nn.Resources[0].Baseline["data"])["FOREIGN"] = "ZW1wdHk="
			}
			if mode == "known-uid" || mode == "known-body" || mode == "unknown-create" {
				_, gotErr = buildCreateNormalTailRuntimeForward(store, source, inputs, &pp, jj, oo)
			} else {
				gotErr = validateCreateNormalTailRuntimeForward(store, source, inputs, &pp, jj, &nn)
			}
			if gotErr == nil {
				t.Fatal("foreign runtime correction accepted")
			}
		})
	}
}
func TestCreateNormalTailRuntimeForwardEnabledPayPalNeedsCanonicalCredentials(t *testing.T) {
	store, source, inputs, p, j, observed := runtimeBillingPartialFixture(t)
	inputs.Operator["PAYPAL_ENABLED"] = createNormalTailRuntimeValue{Present: true, Value: "true"}
	if _, err := buildCreateNormalTailRuntimeForward(store, source, inputs, p, j, observed); err == nil {
		t.Fatal("missing externally-owned PayPal inputs accepted")
	}
	for key := range inputs.Secrets {
		inputs.Secrets[key] = createNormalTailRuntimeValue{Present: true, Value: "synthetic-external-" + key}
	}
	if _, err := buildCreateNormalTailRuntimeForward(store, source, inputs, p, j, observed); err != nil {
		t.Fatal("complete canonical PayPal inputs refused", err)
	}
}
