package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// readIssuerResumeDeadline reads only the already admitted review's private
// execution window. A preflight command timeout is a separate, shorter budget.
func readIssuerResumeDeadline(store secretStore, e *environmentCreateExecution, review string, original environmentCreateJournalEntry) (time.Time, bool, error) {
	bad := func() (time.Time, bool, error) {
		return time.Time{}, false, createProviderRecoveryBlock("the original continuation execution deadline is unavailable or changed")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || !environmentPreflightDigest.MatchString(review) || original.Key == "" || original.StartedAt.IsZero() {
		return bad()
	}
	current := createOpenBaoACKCompletionEntry(e.Journal, original.Key)
	if current == nil || current.Phase != original.Phase || !current.StartedAt.Equal(original.StartedAt) || current.IntentSHA256 != original.IntentSHA256 {
		return bad()
	}
	relative := filepath.Join("deployment", "create", "issuer-enrollment-continuation", review, "resume-admission.json")
	path, err := store.safePath(relative)
	if err != nil {
		return bad()
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	} else if err != nil {
		return bad()
	}
	var admission issuerResumeAdmission
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readEnvironmentPreflightJSONWithCanonical(path, &admission, false) != nil || admission.Review != review || admission.Original != original.Key || admission.ResumedAt.IsZero() || admission.ResumedAt.After(time.Now().UTC()) || admission.ResumedAt.Before(original.StartedAt) || !admission.ResumedAt.Before(admission.Deadline) {
		return bad()
	}
	if e.Check(context.Background()) != nil {
		return bad()
	}
	return admission.Deadline, true, nil
}
