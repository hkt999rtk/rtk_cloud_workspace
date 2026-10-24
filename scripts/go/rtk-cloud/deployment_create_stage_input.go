package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateStageInput struct {
	ParentSHA256, Stage string
	Plan                *deploymentplan.Plan
}

func environmentCreateStageInputPath(e *environmentCreateExecution, stage string) string {
	return filepath.Join(e.Directory, "stage-inputs", stage+".json")
}

func readEnvironmentCreateStageInput(store secretStore, e *environmentCreateExecution, stage string) (*deploymentplan.Plan, error) {
	if e == nil || e.Store != store || !secretEnvironmentPattern.MatchString(stage) {
		return nil, environmentCreateBootstrapBlocker("original stage input scope unavailable")
	}
	path := environmentCreateStageInputPath(e, stage)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var a environmentCreateStageInput
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readEnvironmentPreflightJSON(path, &a) != nil || a.ParentSHA256 != e.Journal.PlanSHA256 || a.Stage != stage || a.Plan == nil || a.Plan.Operation != "scoped" || a.Plan.Environment != e.Journal.Environment || a.Plan.Stack != e.Journal.Stack || len(a.Plan.Resources) == 0 {
		return nil, environmentCreateBootstrapBlocker("original immutable stage input changed")
	}
	return a.Plan, nil
}

func saveEnvironmentCreateStageInput(ctx context.Context, store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan) error {
	if e == nil || e.Store != store || p == nil || !secretEnvironmentPattern.MatchString(stage) || p.Operation != "scoped" || p.Environment != e.Journal.Environment || p.Stack != e.Journal.Stack || len(p.Resources) == 0 {
		return environmentCreateBootstrapBlocker("original compiled stage input unavailable")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	if original, err := readEnvironmentCreateStageInput(store, e, stage); err != nil {
		return err
	} else if original != nil {
		if original.SHA256() != p.SHA256() {
			return environmentCreateBootstrapBlocker("original compiled stage input changed")
		}
		return nil
	}
	relative, err := filepath.Rel(store.Root, environmentCreateStageInputPath(e, stage))
	if err != nil || filepath.IsAbs(relative) {
		return environmentCreateBootstrapBlocker("stage input is outside selected original execution")
	}
	return store.write(relative, mustProviderRecoveryJSON(environmentCreateStageInput{ParentSHA256: e.Journal.PlanSHA256, Stage: stage, Plan: p}), false)
}

func readEnvironmentCreateStageProgress(store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan) (deploymentplan.Journal, bool, error) {
	if e == nil || p == nil {
		return deploymentplan.Journal{}, false, environmentCreateBootstrapBlocker("original stage progress unavailable")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil {
		return j, false, err
	}
	if entry == nil {
		if exists {
			return j, false, environmentCreateBootstrapBlocker("child execution has no original parent request")
		}
		return j, false, nil
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		return j, false, err
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.Phase != stage || entry.IntentSHA256 != sha || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
		return j, false, environmentCreateBootstrapBlocker("original stage request changed")
	}
	if !exists {
		if entry.State != "pending" || len(entry.Receipt) != 0 {
			return j, false, environmentCreateBootstrapBlocker("native stage receipt has no child journal")
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256()) {
				return j, false, environmentCreateBootstrapBlocker("native stage lease has no child journal")
			}
		}
		return j, false, nil
	}
	matched := false
	for _, lease := range e.Journal.KubernetesLeases {
		if managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256()) {
			matched = true
		}
	}
	if !matched {
		return j, false, environmentCreateBootstrapBlocker("original child journal has no recorded native lease")
	}
	if entry.State != "pending" {
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if !j.Accepted || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Plan != p.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) {
			return j, false, environmentCreateBootstrapBlocker("original parent receipt differs from accepted child")
		}
	}
	return j, true, nil
}

// Shared fresh-create stages keep the original compiled input and child write
// journal across admission, readiness, acceptance and parent receipt failures.
func executeEnvironmentCreateStagePlan(ctx context.Context, store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, capture func(context.Context) error, out io.Writer) error {
	if e == nil || p == nil || backend == nil || qualify == nil || fence == nil || accept == nil || capture == nil {
		return environmentCreateBootstrapBlocker("original stage execution adapters unavailable")
	}
	if _, _, err := readEnvironmentCreateStageProgress(store, e, stage, p); err != nil {
		return err
	}
	if err := saveEnvironmentCreateStageInput(ctx, store, e, stage, p); err != nil {
		return err
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		return err
	}
	key := "kubernetes/" + stage
	if createOpenBaoACKCompletionEntry(e.Journal, key) == nil {
		if err := e.BeginEntry(key, stage, intent, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := runEnvironmentProgress(ctx, out, "deployment.create.kubernetes."+stage, func(ctx context.Context) error {
		return p.ExecuteResumable(ctx, backend, p.SHA256(), store.Root, qualify, fence, accept)
	}); err != nil {
		return err
	}
	journal, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || !journal.Accepted || journal.InFlight != "" || journal.Failed != "" || len(journal.Owners) != len(p.Resources) {
		return environmentCreateBlocker("deployment.create.stage", "Kubernetes stage lacks original accepted owner evidence: "+stage)
	}
	receipt, _ := json.Marshal(deploymentplan.Object{"plan_sha256": p.SHA256(), "owners": journal.Owners})
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry != nil && entry.State == "completed" {
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(receipt)) {
			return environmentCreateBootstrapBlocker("accepted stage parent receipt changed")
		}
		return e.Check(ctx)
	}
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	if err := capture(ctx); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
