package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productSessionRenewalCopyRetireSpec(r *productSessionRenewal, d deploymentplan.Definition) productSessionRenewalStep {
	intent := deploymentplan.Object{"source_sha256": r.SourceSHA256, "resource_id": deploymentplan.ID(d.Object), "uid": deploymentplan.Map(d.Object["metadata"])["uid"], "desired_sha256": deploymentplan.Digest(deploymentplan.Projection(d.Object)), "original_session_id": r.Source.OriginalSession.Session}
	receipt := deploymentplan.Clone(intent)
	receipt["absent"] = true
	suffix := ""
	if r.Source.Generation == 2 {
		suffix = "-" + deploymentplan.Digest(deploymentplan.ID(d.Object))[:12]
	}
	return productSessionRenewalStep{Key: "product-session-renewal-retire-" + r.Source.SessionID + "-" + strings.ToLower(deploymentplan.String(d.Object["kind"])) + suffix, Phase: "bootstrap", Intent: intent, Receipt: receipt}
}

// Secrets and cleanup ConfigMaps use the explicit PKI retirement lifecycle,
// rather than broadening the planner's ordinary resource retirement contract.
func retireProductSessionRenewalCopy(ctx context.Context, runtime environmentCreateBootstrapRuntime, d deploymentplan.Definition, spec productSessionRenewalStep) error {
	bad := func() error { return productSessionRenewalBlock("original credential copy conditional retirement") }
	id := deploymentplan.ID(d.Object)
	meta := deploymentplan.Map(d.Object["metadata"])
	uid := deploymentplan.String(meta["uid"])
	plural := ""
	switch d.Object["kind"] {
	case "Secret":
		plural = "secrets"
	case "ConfigMap":
		plural = "configmaps"
	default:
		return bad()
	}
	intent, ok := spec.Intent.(deploymentplan.Object)
	if runtime.Execution == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || !d.Retire || !ok || spec.Phase != "bootstrap" || intent["resource_id"] != id || intent["uid"] != uid || intent["desired_sha256"] != deploymentplan.Digest(deploymentplan.Projection(d.Object)) || !environmentPreflightDigest.MatchString(deploymentplan.String(intent["source_sha256"])) || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(intent["original_session_id"])) {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, spec.Key)
	if entry != nil {
		if entry.Phase != spec.Phase || entry.IntentSHA256 != deploymentplan.Digest(spec.Intent) || entry.StartedAt.IsZero() {
			return bad()
		}
		switch entry.State {
		case "pending":
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return bad()
			}
		case "allocated", "completed":
			if (entry.State == "completed") == entry.CompletedAt.IsZero() || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(spec.Receipt) {
				return bad()
			}
		default:
			return bad()
		}
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil {
		return err
	}
	if actual == nil {
		if entry == nil {
			return bad()
		}
	} else {
		observed := deploymentplan.Map(actual["metadata"])
		if entry != nil && entry.State != "pending" || observed["uid"] != uid || deploymentplan.String(observed["resourceVersion"]) == "" || !deploymentplan.Matches(deploymentplan.Projection(d.Object), actual) {
			return bad()
		}
		if entry == nil {
			if err = runtime.Execution.BeginEntry(spec.Key, spec.Phase, mustProviderRecoveryJSON(spec.Intent), time.Now().UTC()); err != nil {
				return err
			}
		}
		if err = runtime.Execution.Check(ctx); err != nil {
			return err
		}
		body, _ := json.Marshal(deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": uid, "resourceVersion": observed["resourceVersion"]}})
		path := "/api/v1/namespaces/" + deploymentplan.String(meta["namespace"]) + "/" + plural + "/" + deploymentplan.String(meta["name"])
		if _, err = runtime.Backend.command(ctx, body, "delete", "--raw="+path, "-f", "-"); err != nil {
			return productSessionRenewalBlock("original credential copy deletion response is uncertain")
		}
		actual, err = runtime.Backend.Read(ctx, id)
		if err != nil || actual != nil {
			return bad()
		}
	}
	if entry != nil && entry.State == "completed" {
		return runtime.Execution.Check(ctx)
	}
	if err = runtime.Execution.RecordReceipt(spec.Key, mustProviderRecoveryJSON(spec.Receipt)); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(spec.Key, time.Now().UTC())
}
