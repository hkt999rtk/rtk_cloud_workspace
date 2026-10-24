package main

import (
	"encoding/base64"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A reviewed create producer is checked against its canonical input before its
// mirror exists. Existing mirrors always continue through the live-value check.
func verifyPlannedSecretStoreBinding(store secretStore, name, key, canonical string) (bool, error) {
	if store.checkRuntime == nil || store.checkRuntime.preflightPlan == nil {
		return false, nil
	}
	p := store.checkRuntime.preflightPlan
	if p.Environment != store.Environment || p.Stack != "video-cloud-"+store.Environment {
		return false, errors.New("planned canonical mirror belongs to another selected environment")
	}
	id := "Secret/" + name
	var found *deploymentplan.Resource
	for i := range p.Resources {
		if p.Resources[i].ID == id {
			if found != nil {
				return false, errors.New("planned canonical mirror has duplicate producers")
			}
			found = &p.Resources[i]
		}
	}
	if found == nil || found.Action != "create" {
		return false, nil
	}
	bad := func() (bool, error) {
		return false, errors.New("planned canonical mirror does not match its selected canonical credential: " + id + ":" + key)
	}
	if found.UID != "" || found.Baseline != nil || deploymentplan.ID(found.Desired) != id || deploymentplan.Map(found.Desired["metadata"])["uid"] != nil {
		return bad()
	}
	value, present := deploymentplan.Map(found.Desired["stringData"])[key]
	if present {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) != canonical {
			return bad()
		}
	} else {
		encoded, ok := deploymentplan.Map(found.Desired["data"])[key].(string)
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || err != nil || strings.TrimSpace(string(raw)) != canonical {
			return bad()
		}
	}
	return true, nil
}
