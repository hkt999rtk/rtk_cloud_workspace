package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPlannedCanonicalSecretMirrorRequiresExactCreateProducer(t *testing.T) {
	const canonical = "isolated-canonical-value"
	name := "video-cloud-dev-support/zammad-postgres-password"
	desired := environmentCreateSecret("video-cloud-dev-support", "zammad-postgres-password", map[string]string{"password": canonical}).Object
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "create"}, Resources: []deploymentplan.Resource{{ID: "Secret/" + name, Action: "create", Desired: desired}}}
	store := secretStore{Environment: "dev", checkRuntime: &deploymentCheckRuntime{preflightPlan: p}}
	if ok, err := verifyPlannedSecretStoreBinding(store, name, "password", canonical); err != nil || !ok {
		t.Fatal("genuine future canonical mirror required live native output", err)
	}
	for _, mode := range []string{"data", "wrong-canonical", "missing-key", "foreign-env", "replaced-owner", "baseline", "foreign-id", "duplicate", "malformed-data", "non-string"} {
		t.Run(mode, func(t *testing.T) {
			q := *p
			q.Resources = append([]deploymentplan.Resource(nil), p.Resources...)
			q.Resources[0].Desired = deploymentplan.Clone(desired)
			switch mode {
			case "data":
				q.Resources[0].Desired["data"] = deploymentplan.Object{"password": base64.StdEncoding.EncodeToString([]byte(canonical))}
				delete(q.Resources[0].Desired, "stringData")
			case "wrong-canonical":
				deploymentplan.Map(q.Resources[0].Desired["stringData"])["password"] = "wrong-private-value"
			case "missing-key":
				delete(deploymentplan.Map(q.Resources[0].Desired["stringData"]), "password")
			case "foreign-env":
				q.Environment = "staging"
			case "replaced-owner":
				q.Resources[0].UID = "11111111-1111-4111-8111-111111111111"
			case "baseline":
				q.Resources[0].Baseline = deploymentplan.Clone(desired)
			case "foreign-id":
				deploymentplan.Map(q.Resources[0].Desired["metadata"])["name"] = "foreign"
			case "duplicate":
				q.Resources = append(q.Resources, q.Resources[0])
			case "malformed-data":
				delete(q.Resources[0].Desired, "stringData")
				q.Resources[0].Desired["data"] = deploymentplan.Object{"password": "invalid"}
			case "non-string":
				deploymentplan.Map(q.Resources[0].Desired["stringData"])["password"] = float64(1)
			}
			selected := store
			selected.checkRuntime = &deploymentCheckRuntime{preflightPlan: &q}
			ok, err := verifyPlannedSecretStoreBinding(selected, name, "password", canonical)
			if mode == "data" {
				if err != nil || !ok {
					t.Fatal(err)
				}
				return
			}
			if err == nil || ok {
				t.Fatal("foreign future producer bypassed native check")
			}
			if strings.Contains(err.Error(), canonical) || strings.Contains(err.Error(), "wrong-private-value") {
				t.Fatal("private value escaped validation")
			}
		})
	}
	for _, action := range []string{"retain", "update", "retire"} {
		q := *p
		q.Resources = append([]deploymentplan.Resource(nil), p.Resources...)
		q.Resources[0].Action = action
		selected := store
		selected.checkRuntime = &deploymentCheckRuntime{preflightPlan: &q}
		if ok, err := verifyPlannedSecretStoreBinding(selected, name, "password", canonical); err != nil || ok {
			t.Fatal("existing mirror bypassed live comparison", action, err)
		}
	}
	if ok, err := verifyPlannedSecretStoreBinding(secretStore{Environment: "dev"}, name, "password", canonical); err != nil || ok {
		t.Fatal("ordinary check behavior changed", err)
	}
}
