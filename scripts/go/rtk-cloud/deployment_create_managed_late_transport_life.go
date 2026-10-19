package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only this already minted session's exact first-installation or admitted
// installation frame may read completed temporary transport as history. The
// remaining live TLS authority is checked independently under managed PKI.
// This pure reader grants no signing, import, session or certificate mutation.
func managedLatePostMintHistoricalTransportScope(store secretStore, parent string, command ...*environmentCreateExecution) (bool, error) {
	var owner environmentCreateOwner
	var journal environmentCreateJournal
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), &owner) != nil || owner.PlanSHA256 != parent || readExecutionJSON(filepath.Join(store.Root, owner.Journal), &journal) != nil || journal.PlanSHA256 != parent || journal.OwnerToken != owner.OwnerToken {
		return false, nil
	}
	e := &environmentCreateExecution{Store: store, owner: owner, Journal: journal}
	var sourceErr error
	e, sourceErr = managedLateTransportSourceExecution(store, parent, e, command...)
	if sourceErr != nil {
		return true, sourceErr
	}
	if managedLateCompletedTailSelected(e.Journal) {
		if len(command) != 1 || command[0] == nil {
			return true, activeTransportError()
		}
		var late createManagedLateSessionRecovery
		review := managedLateExpiredRenewalReview(e.Journal)
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.ExpiredRenewal == nil || late.ExpiredRenewal.PriorLate == nil || late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair == nil {
			return true, activeTransportError()
		}
		_, err := loadManagedLateCompletedTailSource(context.Background(), late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair.Plan.Configuration, store, command[0])
		return true, err
	}
	if managedLateTerminalSourceSelected(e.Journal) {
		if len(e.Journal.Entries) > 400 {
			if err := verifyManagedLateRecordedResidualCleanupProgress(store, e); err != nil {
				return true, err
			}
		}
		var late createManagedLateSessionRecovery
		review := managedLateExpiredRenewalReview(e.Journal)
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.ExpiredRenewal == nil {
			return true, activeTransportError()
		}
		view := *e
		view.Journal = e.Journal
		view.Journal.Entries = view.Journal.Entries[:392]
		if _, _, err := managedLateSessionRuntimeProgress(store, &view, &late); err != nil {
			return true, err
		}
		return true, nil
	}
	// A separately admitted expired-unused renewal keeps the complete374 App
	// proof in its immutable source. Authenticate its own exact progress before
	// any older temporary authority is read as completed historical content.
	if len(journal.Entries) > 374 {
		entry := journal.Entries[374]
		review := strings.TrimPrefix(entry.Key, "openbao-recovery-admission-")
		if entry.Key == "openbao-recovery-admission-"+review && environmentPreflightDigest.MatchString(review) {
			var artifact createFoundationRecoveryArtifact
			if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &artifact) == nil && artifact.Plan.Schema == managedLateExpiredRenewalSchema {
				var late createManagedLateSessionRecovery
				if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.Review != review || late.ExpiredRenewal == nil {
					return true, activeTransportError()
				}
				if _, _, err := managedLateSessionRuntimeProgress(store, e, &late); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}
	appFrame, app, appErr := managedLateAppRootCRLFrame(store, e, false)
	if appErr != nil {
		return true, appErr
	}
	if app != nil && managedLateAppTransportBeforeInstallation(appFrame) {
		_, post, err := loadManagedLatePostMintSourceContent(app.Plan.Configuration, store, appFrame, true)
		if err != nil || post.InstallationPolicy != managedLateInstallationPolicy {
			return true, activeTransportError()
		}
		return true, nil
	}
	var post *createManagedLatePostMint
	if review := managedLatePostMintReview(journal); review != "" {
		var late createManagedLateSessionRecovery
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.OpenBaoTLSRepair == nil {
			return true, activeTransportError()
		}
		_, loaded, err := loadManagedLatePostMintSource(late.OpenBaoTLSRepair.Plan.Configuration, store, e)
		if err != nil {
			return true, err
		}
		post = loaded
	} else {
		for i := len(journal.Entries) - 1; i >= 0; i-- {
			entry := journal.Entries[i]
			review := strings.TrimPrefix(entry.Key, "openbao-recovery-admission-")
			if entry.Key != "openbao-recovery-admission-"+review || !environmentPreflightDigest.MatchString(review) {
				continue
			}
			var artifact createFoundationRecoveryArtifact
			if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &artifact) != nil || artifact.Plan.Schema != managedLatePostMintSchema {
				continue
			}
			var selected createManagedLatePostMint
			var late createManagedLateSessionRecovery
			if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLatePostMintSourceFilename), &selected) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", selected.OriginalReview, "managed-late-session-source.json"), &late) != nil {
				return true, activeTransportError()
			}
			_, loaded, err := managedLatePostMintRuntimeFrame(store, e, &late)
			if err != nil || loaded == nil {
				return true, activeTransportError()
			}
			if _, _, err := managedLateSessionRuntimeProgress(store, e, &late); err != nil {
				return true, err
			}
			post = loaded
			break
		}
	}
	if post == nil {
		if app != nil {
			// Completed App history followed by an unknown/pending installation
			// outcome cannot fall back to an ordinary transport reader.
			return true, activeTransportError()
		}
		return false, nil
	}
	if post.InstallationPolicy != managedLateInstallationPolicy || !post.NativeStartedAt.IsZero() || !post.NativeDeadline.IsZero() || post.SessionExpires.IsZero() || !post.LeafNotAfter.Equal(post.SessionExpires) {
		return true, activeTransportError()
	}
	return true, nil
}

// This is routing only. The enclosing App frame has already authenticated
// its source, artifact and native receipts. Extra installation entries require
// the exact Post admission/clock/runtime frame above, never this source reader.
func managedLateAppTransportBeforeInstallation(frame *environmentCreateExecution) bool {
	return frame != nil && len(frame.Journal.Entries) == 370 && managedLatePostMintReview(frame.Journal) != ""
}

// Existing callers keep the ordinary material lifetime argument. The selected
// Post scope validates completed temporary bytes at their original genuine
// qualification; it does not substitute that lifetime for managed live checks.
func managedLatePostMintTransportMinimum(store secretStore, parent string, command ...*environmentCreateExecution) (time.Duration, bool, error) {
	selected, err := managedLatePostMintHistoricalTransportScope(store, parent, command...)
	return 2 * time.Hour, selected, err
}

// The scope always rereads the canonical owner and journal before this point.
// Borrow only the current command's immutable-history capability; native reads
// and current execution state remain the freshly observed values above.
func managedLateTransportSourceExecution(store secretStore, parent string, observed *environmentCreateExecution, command ...*environmentCreateExecution) (*environmentCreateExecution, error) {
	if len(command) == 0 || len(command) == 1 && command[0] == nil {
		return observed, nil
	}
	if len(command) != 1 || observed == nil {
		return nil, managedLateSourceReuseBlock()
	}
	selected := command[0]
	if selected.Store.Root != store.Root || selected.Store.ConfigRoot != store.ConfigRoot || selected.Store.Environment != store.Environment || observed.Store.Root != store.Root || observed.Store.ConfigRoot != store.ConfigRoot || observed.Store.Environment != store.Environment || selected.Journal.PlanSHA256 != parent || observed.Journal.PlanSHA256 != parent || deploymentplan.Digest(selected.owner) != deploymentplan.Digest(observed.owner) || deploymentplan.Digest(selected.Journal) != deploymentplan.Digest(observed.Journal) {
		return nil, managedLateSourceReuseBlock()
	}
	if !managedLateSourceReuseActive(selected) {
		return observed, nil
	}
	if err := checkManagedLateSourceReuse(selected); err != nil {
		return nil, err
	}
	view := *observed
	view.lateSourceReuse = selected.lateSourceReuse
	return &view, nil
}
