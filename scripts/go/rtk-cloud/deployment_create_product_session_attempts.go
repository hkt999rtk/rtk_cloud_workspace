package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

const productSessionAttemptSchema = "rtk-cloud.product-session-forward-attempt/v1"
const productSessionTokenFailure = "bootstrap OpenBao client: read service account token file: open /run/openbao-auth/token: permission denied"

type environmentProductSessionAttempt struct {
	Schema, Scope, SourceSHA256, PreviousStage, PreviousPlanSHA256, PreviousJournalSHA256, JobUID, PodUID, NoMintSHA256 string
	ObservedAt                                                                                                          time.Time
	Job, Pod                                                                                                            deploymentplan.Object
}

// A failed native child remains failed. This parent receipt records only that
// its exact known pre-mint outcome was superseded by a genuinely accepted child.
type environmentProductSessionSuperseded struct {
	Outcome             string `json:"outcome"`
	FailedPlanSHA256    string `json:"failed_plan_sha256"`
	FailedJournalSHA256 string `json:"failed_journal_sha256"`
	WinnerStage         string `json:"winner_stage"`
	WinnerPlanSHA256    string `json:"winner_plan_sha256"`
	SourceSHA256        string `json:"source_sha256"`
}

func productSessionSuperseded(entry environmentCreateJournalEntry, p *deploymentplan.Plan, j deploymentplan.Journal, f *environmentProductSessionForward) (environmentProductSessionSuperseded, error) {
	var v environmentProductSessionSuperseded
	if entry.State == "pending" && len(entry.Receipt) == 0 && entry.CompletedAt.IsZero() {
		return v, nil
	}
	if (entry.State != "allocated" && entry.State != "completed") || decodeEnvironmentJSON(entry.Receipt, &v) != nil || v.Outcome != "superseded-pre-mint-failure" || v.FailedPlanSHA256 != p.SHA256() || v.FailedJournalSHA256 != deploymentplan.Digest(j) || v.SourceSHA256 != deploymentplan.Digest(f) || !environmentPreflightDigest.MatchString(v.WinnerPlanSHA256) || v.WinnerStage == entry.Phase || v.WinnerStage == "" || entry.State == "allocated" && !entry.CompletedAt.IsZero() || entry.State == "completed" && entry.CompletedAt.Before(entry.StartedAt) {
		return v, productSessionForwardBlock("failed parent has no genuine supersession outcome")
	}
	return v, nil
}
func productSessionInitialDefinitions(l *createNormalTailProductLifecycle, original *deploymentplan.Plan, f *environmentProductSessionForward, saved *deploymentplan.Plan) ([]deploymentplan.Definition, error) {
	defs, err := productSessionForwardDefinitions(l, original, f)
	if err != nil {
		return nil, err
	}
	// A historical saved 0440 child is never rewritten. New forward children
	// use the readable projected token immediately, without a deliberate failure.
	if saved != nil && saved.BindingsSHA256 == environmentCreateStageBindingsSHA(productSessionForwardStage(l, "forward"), defs) {
		return defs, nil
	}
	return bindProductSessionForwardReadableToken(defs)
}

func productSessionAttemptStage(l *createNormalTailProductLifecycle, previous *deploymentplan.Plan) string {
	return "product-identities-" + productIdentityScope(l)[:12] + "-service-session-attempt-" + previous.SHA256()[:12]
}
func productSessionAttemptPath(l *createNormalTailProductLifecycle, previous *deploymentplan.Plan) string {
	return filepath.Join(filepath.Dir(productSessionForwardPath(l)), "session-forward-attempt-"+previous.SHA256()+".json")
}
func productSessionAttemptDefinitions(l *createNormalTailProductLifecycle, original *deploymentplan.Plan, f *environmentProductSessionForward, previous *deploymentplan.Plan) ([]deploymentplan.Definition, error) {
	defs, err := productSessionForwardDefinitions(l, original, f)
	if err != nil {
		return nil, err
	}
	if previous == nil || len(previous.Resources) != 1 || previous.Resources[0].Desired["kind"] != "Job" {
		return nil, productSessionForwardBlock("attempt previous job")
	}
	deploymentplan.Map(defs[0].Object["metadata"])["name"] = deploymentplan.Map(original.Resources[2].Desired["metadata"])["name"].(string) + "-a" + previous.SHA256()[:12]
	return bindProductSessionForwardReadableToken(defs)
}
func productSessionAttemptFailed(p *deploymentplan.Plan, j deploymentplan.Journal) bool {
	return p != nil && len(p.Resources) == 1 && p.Resources[0].Desired["kind"] == "Job" && p.Resources[0].Action == "create" && j.PlanSHA256 == p.SHA256() && j.Failed == p.Resources[0].ID && j.InFlight == "" && !j.Accepted && !j.AcceptanceStarted && len(j.Completed) == 0 && len(j.Owners) == 1 && environmentBootstrapOwnerUUID.MatchString(j.Owners[p.Resources[0].ID])
}
func readProductSessionAttempt(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, f *environmentProductSessionForward, stage string, p *deploymentplan.Plan, j deploymentplan.Journal) (*environmentProductSessionAttempt, error) {
	path, err := store.safePath(productSessionAttemptPath(l, p))
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var a environmentProductSessionAttempt
	if createOpenBaoACKCompletionRead(store, productSessionAttemptPath(l, p), &a) != nil || a.Schema != productSessionAttemptSchema || a.Scope != productIdentityScope(l) || a.SourceSHA256 != deploymentplan.Digest(f) || a.PreviousStage != stage || a.PreviousPlanSHA256 != p.SHA256() || a.PreviousJournalSHA256 != deploymentplan.Digest(j) || !productSessionAttemptFailed(p, j) || a.JobUID != j.Owners[p.Resources[0].ID] || !environmentPreflightDigest.MatchString(a.NoMintSHA256) || validateBootstrapTerminalTLSJob(p.Resources[0].Desired, a.JobUID, a.PodUID, p.Stack+"-video-cloud", a.Job, a.Pod, []byte(productSessionTokenFailure), productSessionTokenFailure) != nil {
		return nil, productSessionForwardBlock("original pre-mint attempt proof changed")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil || a.ObservedAt.Before(entry.StartedAt) || a.ObservedAt.After(time.Now().UTC()) {
		return nil, productSessionForwardBlock("failed attempt parent is not genuine pending")
	}
	if _, err := productSessionSuperseded(*entry, p, j, f); err != nil {
		return nil, err
	}
	actual, actualJournal, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil || actual.SHA256() != p.SHA256() || deploymentplan.Digest(actualJournal) != deploymentplan.Digest(j) {
		return nil, productSessionForwardBlock("failed attempt plan or lease changed")
	}
	return &a, nil
}
func observeProductSessionAttempt(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, f *environmentProductSessionForward, stage string, p *deploymentplan.Plan, j deploymentplan.Journal, s environmentCreateServiceCeremony) (*environmentProductSessionAttempt, error) {
	if !productSessionAttemptFailed(p, j) || runtime.Execution.Check(ctx) != nil {
		return nil, productSessionForwardBlock("definite original attempt outcome required")
	}
	b := runtime.Backend
	job, err := b.Read(ctx, p.Resources[0].ID)
	if err != nil {
		return nil, err
	}
	raw, err := b.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "get", "pods", "--selector", "job-name="+deploymentplan.String(deploymentplan.Map(p.Resources[0].Desired["metadata"])["name"]), "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return nil, productSessionForwardBlock("one original failed attempt pod")
	}
	pod := pods.Items[0]
	uid := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(p.Resources[0].Desired)["containers"])[0])
	logs, err := b.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "logs", "pod/"+deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), "--container", deploymentplan.String(container["name"]))
	if err != nil || validateBootstrapTerminalTLSJob(p.Resources[0].Desired, j.Owners[p.Resources[0].ID], uid, p.Stack+"-video-cloud", job, pod, logs, productSessionTokenFailure) != nil {
		return nil, productSessionForwardBlock("attempt is not the known pre-login token failure")
	}
	query, err := productSessionForwardNoMintQuery(cfg, l, s)
	if err != nil {
		return nil, err
	}
	counts, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || !managedLateSeedForwardNoMintCountsValid(counts) {
		return nil, productSessionForwardBlock("failed attempt minted or has unknown registry state")
	}
	a := &environmentProductSessionAttempt{Schema: productSessionAttemptSchema, Scope: productIdentityScope(l), SourceSHA256: deploymentplan.Digest(f), PreviousStage: stage, PreviousPlanSHA256: p.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(j), JobUID: j.Owners[p.Resources[0].ID], PodUID: uid, NoMintSHA256: environmentPreflightBytesSHA256(counts), Job: job, Pod: pod, ObservedAt: time.Now().UTC()}
	return a, runtime.Execution.Check(ctx)
}

// Failed attempts stay failed, with their original parent requests and UIDs.
// A successor is allowed only by a definite, typed pre-mint native response.
func executeProductSessionAttempts(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, original *deploymentplan.Plan, f *environmentProductSessionForward, s environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (string, *deploymentplan.Plan, deploymentplan.Journal, error) {
	stage := productSessionForwardStage(l, "forward")
	initial, err := readCreateNormalTailPreparationPlan(store, runtime.Execution, stage)
	if err != nil {
		return "", nil, deploymentplan.Journal{}, err
	}
	defs, err := productSessionInitialDefinitions(l, original, f, initial)
	type failedAttempt struct {
		stage string
		p     *deploymentplan.Plan
		j     deploymentplan.Journal
	}
	var failures []failedAttempt
	for remaining := len(runtime.Execution.Journal.Entries) + 1; remaining > 0; remaining-- {
		if err != nil {
			return "", nil, deploymentplan.Journal{}, err
		}
		p, err := readCreateNormalTailPreparationPlan(store, runtime.Execution, stage)
		if err != nil {
			return "", nil, deploymentplan.Journal{}, err
		}
		if p != nil {
			j, exists, err := p.ReadExecution(store.Root)
			if err != nil {
				return "", nil, j, err
			}
			if exists && j.Failed != "" {
				a, err := readProductSessionAttempt(store, runtime.Execution, l, f, stage, p, j)
				if err != nil {
					return "", nil, j, err
				}
				if a == nil {
					a, err = observeProductSessionAttempt(ctx, cfg, runtime, l, f, stage, p, j, s)
					if err != nil {
						return "", nil, j, err
					}
					if _, err = writeProductArtifact(store, productSessionAttemptPath(l, p), a); err != nil {
						return "", nil, j, err
					}
				}
				failures = append(failures, failedAttempt{stage, p, j})
				defs, err = productSessionAttemptDefinitions(l, original, f, p)
				stage = productSessionAttemptStage(l, p)
				continue
			}
		}
		var written bool
		if p != nil {
			current, exists, err := p.ReadExecution(store.Root)
			if err != nil {
				return "", nil, current, err
			}
			if exists && current.InFlight != "" {
				return "", nil, current, productSessionForwardBlock("unknown attempt write is not replayed")
			}
			written = exists && len(current.Owners) > 0
		}
		if !written {
			if err := verifyProductSessionAttemptWrite(ctx, cfg, runtime, l, f, s, defs); err != nil {
				return "", nil, deploymentplan.Journal{}, err
			}
		}
		// The existing standard executor owns absent, partial and accepted states;
		// it refuses unknown writes and never adopts a preexisting unrecorded Job.
		if err := apply(ctx, strings.TrimPrefix(stage, "product-identities-"+productIdentityScope(l)[:12]+"-"), defs); err != nil {
			return "", nil, deploymentplan.Journal{}, err
		}
		accepted, j, err := acceptedCreateStage(store, runtime.Execution, "kubernetes/"+stage)
		if err != nil || accepted == nil || len(accepted.Resources) != 1 || accepted.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, defs) {
			return "", nil, j, productSessionForwardBlock("attempt has no genuine accepted winner")
		}
		for _, failed := range failures {
			key := "kubernetes/" + failed.stage
			entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, key)
			if entry == nil {
				return "", nil, j, productSessionForwardBlock("failed parent intent disappeared")
			}
			want := environmentProductSessionSuperseded{"superseded-pre-mint-failure", failed.p.SHA256(), deploymentplan.Digest(failed.j), stage, accepted.SHA256(), deploymentplan.Digest(f)}
			prior, checkErr := productSessionSuperseded(*entry, failed.p, failed.j, f)
			if checkErr != nil || prior.Outcome != "" && prior != want {
				return "", nil, j, productSessionForwardBlock("failed parent supersession changed")
			}
			if entry.State == "pending" {
				if err = runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(want)); err != nil {
					return "", nil, j, err
				}
			}
			if entry.State != "completed" {
				if err = runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
					return "", nil, j, err
				}
			}
		}
		return stage, accepted, j, nil
	}
	return "", nil, deploymentplan.Journal{}, productSessionForwardBlock("attempt chain is not finite original history")
}

func verifyProductSessionAttemptWrite(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, f *environmentProductSessionForward, s environmentCreateServiceCeremony, defs []deploymentplan.Definition) error {
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	query, err := productSessionForwardNoMintQuery(cfg, l, s)
	if err != nil {
		return err
	}
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || !managedLateSeedForwardNoMintCountsValid(raw) {
		return productSessionForwardBlock("new attempt requires original no-mint registry")
	}
	for id, uid := range map[string]string{"PersistentVolumeClaim/" + s.BootstrapCredentialNamespace + "/" + s.BootstrapClaim: f.ClaimUID, "ServiceAccount/" + s.BootstrapCredentialNamespace + "/pki-product-bootstrap-" + productIdentityScope(l)[:12]: f.ServiceAccountUID} {
		actual, err := runtime.Backend.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid {
			return productSessionForwardBlock("original attempt claim or account changed")
		}
	}
	trust, err := runtime.Backend.Read(ctx, f.Trust.ID)
	if err != nil || validateProductIdentityOpenBaoTrust(l, trust, time.Now().UTC()) != nil {
		return productSessionForwardBlock("new attempt managed trust changed")
	}
	actual, err := runtime.Backend.Read(ctx, deploymentplan.ID(defs[0].Object))
	if err != nil || actual != nil {
		return productSessionForwardBlock("unrecorded new attempt is not adopted")
	}
	return runtime.Execution.Check(ctx)
}
