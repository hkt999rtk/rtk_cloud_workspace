package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestCreateNormalTailRuntimePartialRequiresLiteralPrefixAndKnownNext(t *testing.T) {
	p := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: "ConfigMap/ns/a"}, {ID: "ConfigMap/ns/b"}, {ID: "ConfigMap/ns/c"}}}
	uid := "11111111-aaaa-4aaa-8aaa-111111111111"
	base := deploymentplan.Journal{PlanSHA256: p.SHA256(), Completed: []string{p.Resources[0].ID}, Owners: map[string]string{p.Resources[0].ID: uid}}
	clone := func() deploymentplan.Journal {
		j := base
		j.Completed = append([]string(nil), base.Completed...)
		j.Owners = map[string]string{}
		for k, v := range base.Owners {
			j.Owners[k] = v
		}
		return j
	}
	if !createNormalTailRuntimePartial(p, base) {
		t.Fatal("exact known stopped prefix refused")
	}
	next := clone()
	next.Owners[p.Resources[1].ID] = uid
	if !createNormalTailRuntimePartial(p, next) {
		t.Fatal("exact known next native response refused")
	}
	for _, name := range []string{"nonprefix", "foreign-owner", "missing-owner", "unknown-write", "accepted", "accepting", "wrong-plan", "foreign-next"} {
		t.Run(name, func(t *testing.T) {
			j := clone()
			switch name {
			case "nonprefix":
				j.Completed[0] = p.Resources[1].ID
				delete(j.Owners, p.Resources[0].ID)
				j.Owners[p.Resources[1].ID] = uid
			case "foreign-owner":
				delete(j.Owners, p.Resources[0].ID)
				j.Owners["ConfigMap/ns/foreign"] = uid
			case "missing-owner":
				delete(j.Owners, p.Resources[0].ID)
			case "unknown-write":
				j.InFlight = p.Resources[1].ID
			case "accepted":
				j.Accepted = true
			case "accepting":
				j.AcceptanceStarted = true
			case "wrong-plan":
				j.PlanSHA256 = "foreign"
			case "foreign-next":
				j.Owners[p.Resources[2].ID] = uid
			}
			if createNormalTailRuntimePartial(p, j) {
				t.Fatal("unqualified partial source accepted")
			}
		})
	}
}
