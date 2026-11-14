package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Resume the original unseal operation after an interrupted managed-host
// rollout. It cannot initialize OpenBao or select another generation.
func runCreateManagedOpenBaoUnseal(args []string) error {
	fs := flag.NewFlagSet("recover-create-managed-openbao-unseal", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	parent := fs.String("parent-plan-sha256", "", "original create generation")
	confirm := fs.String("confirm", "", "selected stack")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || !environmentPreflightDigest.MatchString(*parent) || *confirm != "video-cloud-"+*env {
		return createProviderRecoveryBlock("select the exact environment, original create generation and stack")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, original, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("original operator binding changed")
	}
	old, err := readEnvironmentCreateStageInput(store, e, "transport-managed-hosts")
	if err != nil || old == nil {
		return createProviderRecoveryBlock("original managed host stage is unavailable")
	}
	stage := managedAllocationForwardStage(old)
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || plan == nil {
		return createProviderRecoveryBlock("qualified managed host continuation is unavailable")
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
	id := "StatefulSet/" + *confirm + "-secrets/openbao"
	if err != nil || !exists || journal.Accepted || journal.InFlight != "" || !slices.Contains(journal.Completed, id) || journal.Owners[id] == "" {
		return createProviderRecoveryBlock("original managed OpenBao write is not safely recorded")
	}
	var target deploymentplan.Resource
	for _, resource := range plan.Resources {
		if resource.ID == id {
			target = resource
		}
	}
	if target.ID == "" || target.UID != journal.Owners[id] {
		return createProviderRecoveryBlock("original OpenBao owner differs from its write receipt")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original Kubernetes identity changed")
	}
	b, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	controller, err := b.Read(ctx, id)
	if err != nil || controller == nil || deploymentplan.Map(controller["metadata"])["uid"] != target.UID || !deploymentplan.Matches(target.Desired, controller) {
		return createProviderRecoveryBlock("managed OpenBao controller differs from the recorded write")
	}
	pod, err := b.Read(ctx, "Pod/"+*confirm+"-secrets/openbao-0")
	if err != nil || pod == nil || validateOpenBaoPodOwner(controller, pod) != nil {
		return createProviderRecoveryBlock("original OpenBao replacement Pod is unavailable")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return createProviderRecoveryBlock("interrupted managed host lease is unavailable")
	}
	lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	liveLease, err := b.Read(ctx, lease.ResourceID)
	if err != nil || liveLease == nil || deploymentplan.Map(liveLease["metadata"])["uid"] != lease.UID || deploymentplan.Map(liveLease["spec"])["holderIdentity"] != lease.Holder || !strings.HasPrefix(lease.Holder, plan.SHA256()+":") {
		return createProviderRecoveryBlock("interrupted managed host lease differs from its original owner")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: *parent, Backend: b}
	if err := unsealEnvironmentCreateOpenBaoRestart(ctx, cfg, store, runtime, pod); err != nil {
		return err
	}
	intent := deploymentplan.Object{"resource": lease.ResourceID, "uid": lease.UID, "holder": lease.Holder, "interrupted_plan_sha256": plan.SHA256()}
	key := "managed-openbao-lease-release-" + deploymentplan.Digest(intent)
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		return err
	}
	if err := (&environmentExecutionLease{backend: b, id: lease.ResourceID, uid: lease.UID, holder: lease.Holder}).Release(ctx); err != nil {
		return err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
		return err
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "PASS: original OpenBao unsealed and interrupted managed-host lease released")
	return nil
}
