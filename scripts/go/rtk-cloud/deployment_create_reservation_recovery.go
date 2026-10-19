package main

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func verifyEmptyInitialPKIRegistry(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend) error {
	script := "export PGPASSWORD=\"$POSTGRES_PASSWORD\"; psql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud -c 'SELECT (SELECT count(*) FROM pki_issuers), (SELECT count(*) FROM pki_operations)'"
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", script)
	if err != nil || strings.TrimSpace(string(raw)) != "0|0" {
		return createProviderRecoveryBlock("initial reservation recovery requires genuinely empty issuer and operation registries; uncertain writes cannot replay")
	}
	return nil
}

func loadAcceptedSeparatedListeners(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, journal deploymentplan.Journal, p *createFoundationRecoveryPlan, skipReservation ...bool) error {
	var sha string
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/certissuer-initial-auth-separation" && entry.State == "completed" {
			var receipt struct {
				Plan string `json:"plan_sha256"`
			}
			if json.Unmarshal(entry.Receipt, &receipt) == nil {
				sha = receipt.Plan
			}
		}
	}
	var plan deploymentplan.Plan
	var accepted deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &accepted) != nil || plan.SHA256() != sha || accepted.PlanSHA256 != sha || !accepted.Accepted || accepted.InFlight != "" || accepted.Failed != "" || len(plan.Resources) != 1 || len(accepted.Completed) != 1 {
		return createProviderRecoveryBlock("original accepted initial auth separation is unavailable")
	}
	var defs []deploymentplan.Definition
	for _, r := range original.Resources {
		defs = append(defs, deploymentplan.Definition{Object: r.Desired, Phase: r.Phase})
	}
	cfg := deploymentConfig{Environment: p.Plan.Environment, Values: map[string]string{"CLOUD_STACK_NAME": p.Plan.Stack}}
	updated, selected, err := initialCertIssuerAuthDefinitions(cfg, defs)
	if err != nil {
		return err
	}
	r := plan.Resources[0]
	if r.Action != "update" || r.ID != deploymentplan.ID(selected[0].Object) || accepted.Owners[r.ID] != journal.Owners[r.ID] || !sameEnvironmentCreateDefinitions(selected, []deploymentplan.Definition{{Object: r.Desired, Phase: r.Phase}}) {
		return createProviderRecoveryBlock("initial auth separation changed more than the original role reference or owner")
	}
	for index, r := range original.Resources {
		for _, d := range updated {
			if deploymentplan.ID(d.Object) == r.ID {
				original.Resources[index].Desired = d.Object
			}
		}
	}
	p.Materials["accepted-initial-auth-separation-plan"] = sha
	p.Materials["accepted-initial-auth-separation-journal"] = deploymentplan.Digest(accepted)
	if len(skipReservation) == 1 && skipReservation[0] {
		return nil
	}
	request := environmentCreateIssuerRequest{Environment: p.Plan.Environment, Domain: "service", Kind: "root"}
	raw, _ := json.Marshal(request)
	intent, _ := environmentCreatePlanSHA256(raw)
	matched := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "pki-reserve-service-root" && entry.State == "pending" && len(entry.Receipt) == 0 && entry.IntentSHA256 == intent {
			matched = true
		}
	}
	if !matched {
		return createProviderRecoveryBlock("original failed no-write Service Root reservation intent differs")
	}
	p.Materials["original-failed-service-root-intent"] = intent
	return nil
}

func restartInitialAccountTransport(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, account *deploymentplan.Plan, journal deploymentplan.Journal, review string) (resultErr error) {
	lease, err := b.AcquireExecution(ctx, &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", BindingsSHA256: review}})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	if err := verifyEmptyInitialPKIRegistry(ctx, cfg, b); err != nil {
		return err
	}
	objects, err := b.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	var deployment deploymentplan.Resource
	for _, r := range account.Resources {
		actual, err := b.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return createProviderRecoveryBlock("original Account Manager owner or body changed before client restart")
		}
		if r.Desired["kind"] == "Deployment" {
			deployment = r
			deployment.UID = journal.Owners[r.ID]
			deployment.Action = "retain"
		}
	}
	owned := environmentCreateStageInventory(account, objects)
	var pod deploymentplan.Object
	for _, o := range owned {
		if o["kind"] == "Pod" && initialAccountPodReady(o) {
			if pod != nil {
				return createProviderRecoveryBlock("initial client restart requires one exact owned ready Pod")
			}
			pod = o
		}
	}
	if pod == nil {
		return createProviderRecoveryBlock("original ready Account Manager Pod is unavailable")
	}
	meta := deploymentplan.Map(pod["metadata"])
	uid, rv := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	if uid == "" || rv == "" {
		return createProviderRecoveryBlock("original Account Manager Pod delete preconditions unavailable")
	}
	path := "/api/v1/namespaces/" + url.PathEscape(deploymentplan.String(meta["namespace"])) + "/pods/" + url.PathEscape(deploymentplan.String(meta["name"]))
	key := "account-initial-transport-client-restart-" + review
	intent := map[string]string{"pod_uid": uid, "resource_version": rv, "deployment_uid": deployment.UID, "review_sha256": review}
	if err := e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		return err
	}
	body := deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": uid, "resourceVersion": rv}, "dryRun": []any{"All"}}
	if _, err := b.command(ctx, mustProviderRecoveryJSON(body), "delete", "--raw="+path, "-f", "-"); err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	if err := lease.Check(ctx); err != nil {
		return err
	}
	delete(body, "dryRun")
	if _, err := b.command(ctx, mustProviderRecoveryJSON(body), "delete", "--raw="+path, "-f", "-"); err != nil {
		return err
	}
	if err := b.Wait(ctx, deployment); err != nil {
		return err
	}
	if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, b, account, journal); err != nil {
		return err
	}
	if err := verifyEmptyInitialPKIRegistry(ctx, cfg, b); err != nil {
		return err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

func initialAccountPodReady(pod deploymentplan.Object) bool {
	status := deploymentplan.Map(pod["status"])
	if status["phase"] != "Running" {
		return false
	}
	for _, condition := range certIssuerObjectList(status["conditions"]) {
		c := deploymentplan.Map(condition)
		if c["type"] == "Ready" && c["status"] == "True" {
			return true
		}
	}
	return false
}
