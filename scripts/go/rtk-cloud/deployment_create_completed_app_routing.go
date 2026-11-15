package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func readCompletedOriginalAppRouting(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend) (string, error) {
	_, sha, _, err := readOriginalAppRoutingPublication(ctx, store, e, backend)
	return sha, err
}

// The accepted immutable native declaration is the publication authority. Its
// local file/proof may still be interrupted; observation never completes them.
func readOriginalAppRoutingPublication(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend) (environmentAppRoutingBinding, string, bool, error) {
	var empty environmentAppRoutingBinding
	bad := func() (environmentAppRoutingBinding, string, bool, error) {
		return empty, "", false, environmentCreateBootstrapBlocker("original App routing publication, native owners or local proof differs")
	}
	if ctx.Err() != nil {
		return empty, "", false, ctx.Err()
	}
	if e == nil || e.Store != store || backend == nil || e.Check(ctx) != nil {
		return bad()
	}
	path, err := store.safePath("pki/app/routing.json")
	canonical, canonicalErr := privateDeploymentStatePath(store.Environment, "pki/app/routing.json")
	if err != nil || canonicalErr != nil || path != canonical {
		return bad()
	}
	entries, dirErr := os.ReadDir(filepath.Dir(path))
	if dirErr != nil && !errors.Is(dirErr, os.ErrNotExist) {
		return bad()
	}
	if dirErr == nil {
		if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return bad()
		}
		for _, entry := range entries {
			if entry.Name() != "issuer.json" && entry.Name() != "routing.json" || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return bad()
			}
		}
	}
	var stage, proof *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		entry := &e.Journal.Entries[i]
		switch entry.Key {
		case "kubernetes/native-app-routing-profile":
			if stage != nil {
				return bad()
			}
			stage = entry
		case "native-app-routing-profile-proof":
			if proof != nil {
				return bad()
			}
			proof = entry
		}
	}
	var raw []byte
	_, err = os.Lstat(path)
	present := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) || !present && proof != nil {
		return bad()
	}
	if !present && (stage == nil || stage.State != "completed") && proof == nil {
		return empty, "", false, e.Check(ctx)
	}
	if present {
		raw, err = readBoundedEnvironmentFile(path, true)
		if err != nil {
			return bad()
		}
	}
	if stage == nil || stage.State != "completed" || stage.StartedAt.IsZero() || stage.CompletedAt.IsZero() || stage.CompletedAt.Before(stage.StartedAt) {
		return bad()
	}
	plan, journal, err := managedTransportPostAcceptedStage(store, e, stage.Key)
	if err != nil || len(plan.Resources) != 1 {
		return bad()
	}
	saved, err := readEnvironmentCreateStageInput(store, e, "native-app-routing-profile")
	if err != nil || saved != nil && saved.SHA256() != plan.SHA256() {
		return bad()
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, "native-app-routing-profile", plan)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return bad()
	}
	ns := e.Journal.Stack + "-video-cloud"
	cmID, serviceID, deploymentID := "ConfigMap/"+ns+"/"+environmentAppRoutingDeclaration, "Service/"+ns+"/video-cloud-api-app-pki", "Deployment/"+ns+"/video-cloud-api-app-pki"
	resource := plan.Resources[0]
	var binding environmentAppRoutingBinding
	if resource.ID != cmID || resource.Phase != "configuration" || decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["binding.json"])), &binding) != nil || binding.DeclarationUID != "" || binding.Schema != "rtk-cloud.native-app-routing/v1" || binding.Environment != store.Environment || binding.Stack != e.Journal.Stack || binding.Mode != "internal-service" || binding.CreatePlanSHA256 != e.Journal.PlanSHA256 || !environmentBootstrapOwnerUUID.MatchString(binding.ServiceUID) || !environmentBootstrapOwnerUUID.MatchString(binding.DeploymentUID) {
		return bad()
	}
	definition := environmentAppRoutingDefinition(binding)
	if plan.BindingsSHA256 != environmentCreateStageBindingsSHA("native-app-routing-profile", []deploymentplan.Definition{definition}) || !deploymentplan.Matches(definition.Object, resource.Desired) {
		return bad()
	}
	binding.DeclarationUID = journal.Owners[cmID]
	if !environmentBootstrapOwnerUUID.MatchString(binding.DeclarationUID) {
		return bad()
	}
	if present {
		var file environmentAppRoutingBinding
		if decodeEnvironmentJSON(raw, &file) != nil || file != binding {
			return bad()
		}
	}
	if proof != nil {
		if proof.Phase != "bootstrap" || proof.StartedAt.IsZero() || proof.StartedAt.Before(stage.CompletedAt) || proof.IntentSHA256 != activeTransportJournalDigest(binding) {
			return bad()
		}
		switch proof.State {
		case "pending":
			if len(proof.Receipt) != 0 || !proof.CompletedAt.IsZero() {
				return bad()
			}
		case "allocated", "completed":
			var receipt environmentAppRoutingBinding
			if decodeEnvironmentJSON(proof.Receipt, &receipt) != nil || receipt != binding || proof.State == "allocated" && !proof.CompletedAt.IsZero() || proof.State == "completed" && (proof.CompletedAt.IsZero() || proof.CompletedAt.Before(proof.StartedAt)) {
				return bad()
			}
		default:
			return bad()
		}
	}
	actual, err := backend.Read(ctx, cmID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != binding.DeclarationUID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
		return bad()
	}
	service, err := backend.Read(ctx, serviceID)
	if err != nil || service == nil || deploymentplan.ID(service) != serviceID || deploymentplan.Map(service["metadata"])["uid"] != binding.ServiceUID || deploymentplan.Map(service["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(environmentAppInternalServiceDependencies(map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack})[0].Object), deploymentplan.Projection(service)) {
		return bad()
	}
	listener, err := backend.Read(ctx, deploymentID)
	if err != nil || listener == nil || deploymentplan.ID(listener) != deploymentID || deploymentplan.Map(listener["metadata"])["uid"] != binding.DeploymentUID || deploymentplan.Map(listener["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(deploymentplan.Map(listener["metadata"])["labels"])["rtk.realtek.com/stack"] != e.Journal.Stack {
		return bad()
	}
	sha := ""
	if present {
		observed := environmentIdentityBindings{Environment: store.Environment, Stack: e.Journal.Stack}
		if observeEnvironmentAppRouting(&observed, actual, []deploymentplan.Object{service}) != nil || observed.AppRouting == nil || *observed.AppRouting != binding {
			return bad()
		}
		after, err := readBoundedEnvironmentFile(path, true)
		if err != nil || !bytes.Equal(raw, after) {
			return bad()
		}
		sha = environmentPreflightBytesSHA256(raw)
	}
	return binding, sha, true, e.Check(ctx)
}

func finishOriginalAppRoutingPublication(ctx context.Context, store secretStore, e *environmentCreateExecution, binding environmentAppRoutingBinding, fileSHA string) error {
	if fileSHA == "" {
		if _, err := writeEnvironmentCreateBootstrapJSON(store, "pki/app/routing.json", binding); err != nil {
			return err
		}
	}
	key := "native-app-routing-profile-proof"
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry != nil && entry.State == "completed" {
		return e.Check(ctx)
	}
	raw := mustProviderRecoveryJSON(binding)
	if entry == nil {
		if err := e.BeginEntry(key, "bootstrap", raw, time.Now().UTC()); err != nil {
			return err
		}
	}
	if entry == nil || entry.State == "pending" {
		if err := e.RecordReceipt(key, raw); err != nil {
			return err
		}
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
