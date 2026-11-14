package main

import (
	"context"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// tryUnifiedServiceSessionContinuation selects only the original q2 boundary.
// It leaves all other create paths unchanged and never starts a renewal until
// the exact original OpenBao host successor has been independently verified.
func tryUnifiedServiceSessionContinuation(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, original environmentCreatePlan, operator map[string]string, prior environmentCreateJournalEntry, admitted issuerEnrollmentContinuationProof, out, diagnostics io.Writer) (bool, error) {
	if e == nil || admitted.PartialSession == nil || e.Check(ctx) != nil {
		return false, nil
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	head, _, err := sessionLineageHead(store, e)
	if err != nil {
		return true, err
	}
	var q2 createInitializerMountForwardProof
	newGeneration := false
	if head != nil && head.Batches != nil {
		if !validSecondSessionAdmission(store, e, *head) {
			return true, createProviderRecoveryBlock("admitted Service session continuation changed")
		}
		review := deploymentplan.Digest(*head)
		completed, err := completedServiceSessionBatch(store, e, *head, review)
		if err != nil {
			return true, err
		}
		if completed {
			if err := ensureFreshProductSessionSuccessor(ctx, cfg, store, e, native, *head, review); err != nil {
				return true, err
			}
			state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: native}, head.Renewal.RenewedIntent.SessionID)
			if err != nil {
				return true, err
			}
			now := time.Now().UTC()
			if completedServiceSessionAction(*head, state, now) == serviceSessionContinueCreate {
				if err := completeServiceSessionTrustForward(ctx, store, e, *head, review, now); err != nil {
					return true, err
				}
				return false, nil
			}
			terminal, terminalErr := observeFreshProductTerminalSession(ctx, cfg, store, e, native, *head, review, state, now)
			if terminalErr != nil {
				return true, terminalErr
			}
			if terminal != nil {
				return false, nil
			}
			// An expired completed successor is a predecessor for the same
			// native renewal flow. Unknown or inconsistent state still stops.
			if completedServiceSessionAction(*head, state, now) != serviceSessionRenew {
				return true, createProviderRecoveryBlock("completed Service session no longer has its original active consumer ACKs")
			}
			newGeneration = true
		} else {
			// The mint may have finished while the owner batch or ACK wait was
			// interrupted. Only inspect expiry after the exact Job was accepted.
			stage := "kubernetes/service-session-original-key-renewal-" + review
			entry := createOpenBaoACKCompletionEntry(e.Journal, stage)
			if entry != nil && entry.State == "completed" {
				plan, journal, err := acceptedCreateStage(store, e, stage)
				if err != nil || plan == nil || plan.SHA256() != head.Renewal.Candidate.SHA256() || !journal.Accepted {
					return true, createProviderRecoveryBlock("interrupted Service session Job evidence changed")
				}
				state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: native}, head.Renewal.RenewedIntent.SessionID)
				if err != nil {
					return true, err
				}
				newGeneration = issuerEnrollmentSessionExpired(head.Renewal.RenewedIntent, state, time.Now().UTC())
			}
		}
		// After a batch write the older live desired body has legitimately
		// advanced; the admitted plan and native journals are rechecked by
		// resumeServiceSessionBatchesQualification instead.
		found := false
		for _, entry := range head.SourceJournal.Entries {
			const prefix = "initializer-mount-forward-"
			if entry.Phase != "initializer-mount-forward" || len(entry.Key) <= len(prefix) || entry.Key[:len(prefix)] != prefix {
				continue
			}
			if found {
				return true, createProviderRecoveryBlock("original Service session q2 admission is ambiguous")
			}
			q2, _, _, err = loadInitializerMountForwardBundle(store, e, entry.Key[len(prefix):])
			if err != nil {
				return true, err
			}
			found = true
		}
		if !found {
			return true, createProviderRecoveryBlock("admitted Service session lacks its original q2")
		}
	} else {
		var found bool
		q2, found, err = serviceSessionBoundary(ctx, store, e, backend)
		if err != nil || !found {
			return found || err != nil, err
		}
		intent := admitted.PartialSession.RenewedIntent
		state, _, readErr := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: native}, intent.SessionID)
		if readErr != nil {
			return true, readErr
		}
		if !issuerEnrollmentSessionExpired(intent, state, time.Now().UTC()) {
			return false, nil
		}
	}
	q1, _, _, err := loadInitializerForwardBundle(store, e, q2.InitializerQualificationSHA256)
	if err != nil {
		return true, err
	}
	_, originalReview, err := authenticatedInitializerForwardAdmission(store, e, q1)
	if err != nil || originalReview == "" {
		return true, createProviderRecoveryBlock("original Service session admission is unavailable")
	}
	frozen, found, err := readIssuerResumeDeadline(store, e, originalReview, prior)
	if err != nil || !found || q2.ExecutionDeadline.Before(frozen) {
		return true, createProviderRecoveryBlock("original Service session historical deadline changed")
	}
	deadline, found, err := effectiveIssuerDeadline(ctx, store, e, originalReview, prior)
	if err != nil || !found || q2.ExecutionDeadline.After(deadline) || !time.Now().Before(deadline) {
		return true, createProviderRecoveryBlock("original Service session execution deadline is unavailable or expired")
	}
	ready := func(ctx context.Context) error {
		_, err := verifyOpenBaoInPlaceReady(ctx, cfg, store, e, native)
		return err
	}
	if err := ready(ctx); err != nil {
		return true, err
	}
	images, _, err := loadCreateVideoRepairImages(ctx, cfg, store, e.Journal.PlanSHA256, issuerLeaseMetadataRepair, issuerLeaseMetadataRepairSchema, "video-cloud-api", "video-cloud-emqx-pki", "video-cloud-openbao-pki")
	if err != nil {
		return true, err
	}
	forward, replacements, err := issuerEnrollmentForwardPlan(original, images)
	if err != nil {
		return true, err
	}
	logImages, _, err := loadCreateVideoRepairImages(ctx, cfg, store, e.Journal.PlanSHA256, mqttLogTrustRepair, mqttLogTrustRepairSchema, "video-cloud-api")
	if err != nil {
		return true, err
	}
	next, logReplacements, err := issuerEnrollmentForwardPlan(forward, logImages)
	if err != nil {
		return true, err
	}
	for priorImage, image := range replacements {
		if newer := logReplacements[image]; newer != "" {
			replacements[priorImage] = newer
		}
	}
	forward = next
	if deploymentplan.Digest(forward.Release) != admitted.Release {
		return true, createProviderRecoveryBlock("original Service session release evidence changed")
	}
	runtime, err := prepareIssuerEnrollmentRuntime(ctx, cfg, store, e, native, operator, original, replacements, true, true, true)
	if err != nil || runtime.AcceptedIssuerEnrollment == nil {
		if err != nil {
			return true, err
		}
		return true, createProviderRecoveryBlock("original Service identity ceremony is unavailable")
	}
	material := createFoundationRecoveryPlan{Plan: forward, Materials: map[string]string{"device-custody-continuation": e.Journal.PlanSHA256}}
	bao, _, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, native, operator, &material, "openbao_tls", true)
	if err != nil {
		return true, err
	}
	return true, dispatchServiceSessionBatches(ctx, cfg, o, report, store, e, &forward, native, runtime, *runtime.AcceptedIssuerEnrollment, bao, deadline, ready, newGeneration, out, diagnostics)
}

// Classify only a completed batch: the next renewal is allowed exclusively
// when the same native session is expired and has no competing active session.
type completedServiceSessionActionType uint8

const (
	serviceSessionInvalid completedServiceSessionActionType = iota
	serviceSessionContinueCreate
	serviceSessionRenew
)

func completedServiceSessionAction(q secondSessionAdmission, state managedSessionState, now time.Time) completedServiceSessionActionType {
	if sessionExistingConsumersACKed(q, state, now) {
		return serviceSessionContinueCreate
	}
	if issuerEnrollmentSessionExpired(q.Renewal.RenewedIntent, state, now) {
		return serviceSessionRenew
	}
	return serviceSessionInvalid
}
