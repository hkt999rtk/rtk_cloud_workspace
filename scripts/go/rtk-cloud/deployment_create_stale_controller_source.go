package main

import (
	"context"
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// The latest completed parent-owned child is authoritative for this one
// Controller; earlier unrelated stage histories are not recovery prerequisites.
func staleControllerRecordedSource(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) (string, *deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Resource, error) {
	bad := func() (string, *deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Resource, error) {
		return "", nil, deploymentplan.Journal{}, deploymentplan.Resource{}, createProviderRecoveryBlock("Controller recovery requires its latest recorded original owner")
	}
	if e == nil || e.Store != store || b == nil || e.Check(ctx) != nil {
		return bad()
	}
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	for i := len(e.Journal.Entries) - 1; i >= 0; i-- {
		entry := e.Journal.Entries[i]
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Owners[id] == "" {
			continue
		}
		plan, j, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil || receipt.Plan != plan.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) {
			return bad()
		}
		for idx, r := range plan.Resources {
			if r.ID != id {
				continue
			}
			uid := j.Owners[id]
			if idx >= len(j.Completed) || j.Completed[idx] != id || uid == "" || uid != receipt.Owners[id] || r.Desired["kind"] != "Deployment" || r.Action == "retire" || r.UID != "" && r.UID != uid {
				return bad()
			}
			r.UID = uid
			live, err := b.Read(ctx, id)
			if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid || !deploymentplan.Matches(r.Desired, live) || e.Check(ctx) != nil {
				return bad()
			}
			return strings.TrimPrefix(entry.Key, "kubernetes/"), plan, j, r, nil
		}
		return bad()
	}
	return bad()
}

// An expired session can be replayed only for the Controller subject already
// installed in that exact registry session; no new ACK is manufactured here.
func verifyStaleControllerOriginalACK(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, target deploymentplan.Resource) error {
	bad := func() error {
		return createProviderRecoveryBlock("Controller original bootstrap session ACK is unavailable")
	}
	pod := environmentPKIPod(target.Desired)
	c := managedNamedField(pod["containers"], "pki-controller")
	if c == nil {
		return bad()
	}
	sid := mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")
	root := mqttFoundationEnv(c, "PKI_SERVICE_CLIENT_ROOT_SHA256")
	if !environmentBootstrapOwnerUUID.MatchString(sid) || !environmentPreflightDigest.MatchString(root) {
		return bad()
	}
	query := "SELECT json_build_object('status',status,'root',root_sha256,'ack','service:pki-controller'=ANY(installed_subjects)) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + sid + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var state struct {
		Status string `json:"status"`
		Root   string `json:"root"`
		ACK    bool   `json:"ack"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil || (state.Status != "active" && state.Status != "sealed" && state.Status != "expired") || state.Root != root || !state.ACK {
		return bad()
	}
	return nil
}
