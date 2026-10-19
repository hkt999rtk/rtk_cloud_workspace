package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailProgramWindowSchema = "rtk-cloud.create-remaining-program-window/v1"

// This supplements an operator-selected execution timeout, never its program,
// qualification or original admission. Its total is anchored to that admission.
type createNormalTailProgramWindow struct {
	Schema, Environment, ParentSHA256, OwnerSHA256              string
	ProgramSHA256, QualificationSHA256, OriginalAdmissionSHA256 string
	OriginalAdmittedAt, OriginalDeadline, RecordedAt, Deadline  time.Time
	TotalTimeout                                                time.Duration
	CheckerSHA256                                               string
	PreviousSHA256                                              string `json:",omitempty"`
}

func createNormalTailProgramWindowPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-program-window.json")
}

type createNormalTailProgramWindowRecord struct {
	Window    createNormalTailProgramWindow
	RawSHA256 string
}

func createNormalTailProgramWindowNumberPath(e *environmentCreateExecution, n int) string {
	if n == 1 {
		return createNormalTailProgramWindowPath(e)
	}
	return filepath.Join(e.Directory, fmt.Sprintf("normal-tail-program-window-%04d.json", n))
}
func readCreateNormalTailProgramWindows(store secretStore, e *environmentCreateExecution, original createNormalTailProgramAdmission) ([]createNormalTailProgramWindowRecord, error) {
	bad := func() ([]createNormalTailProgramWindowRecord, error) {
		return nil, environmentCreateBlocker("deployment.create.program", "supplemental windows changed their original program, owner, fixed anchor or append-only order")
	}
	if e == nil || e.Store != store || validateEnvironmentPreflightDirectories(store, e.Directory) != nil {
		return bad()
	}
	entries, err := os.ReadDir(e.Directory)
	if err != nil {
		return nil, err
	}
	var numbers []int
	for _, entry := range entries {
		name := entry.Name()
		if name == "normal-tail-program-window.json" {
			numbers = append(numbers, 1)
			continue
		}
		if !strings.HasPrefix(name, "normal-tail-program-window-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(name, "normal-tail-program-window-"), ".json")
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || name != filepath.Base(createNormalTailProgramWindowNumberPath(e, n)) {
			return bad()
		}
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	var records []createNormalTailProgramWindowRecord
	previous, total, recorded := "", original.Deadline.Sub(original.AdmittedAt), original.AdmittedAt
	for i, n := range numbers {
		if n != i+1 {
			return bad()
		}
		raw, err := readBoundedEnvironmentFile(createNormalTailProgramWindowNumberPath(e, n), true)
		var w createNormalTailProgramWindow
		if err != nil || decodeEnvironmentJSON(raw, &w) != nil || w.Schema != createNormalTailProgramWindowSchema || w.Environment != store.Environment || w.ParentSHA256 != e.Journal.PlanSHA256 || w.OwnerSHA256 != deploymentplan.Digest(e.owner) || w.ProgramSHA256 != original.ProgramSHA256 || w.QualificationSHA256 != original.QualificationSHA256 || w.OriginalAdmissionSHA256 != deploymentplan.Digest(original) || !w.OriginalAdmittedAt.Equal(original.AdmittedAt) || !w.OriginalDeadline.Equal(original.Deadline) || w.TotalTimeout <= total || !w.Deadline.Equal(original.AdmittedAt.Add(w.TotalTimeout)) || w.RecordedAt.Before(recorded) || !w.RecordedAt.Before(w.Deadline) || w.RecordedAt.After(time.Now().UTC()) || !environmentPreflightDigest.MatchString(w.CheckerSHA256) || w.PreviousSHA256 != previous {
			return bad()
		}
		previous, total, recorded = environmentPreflightBytesSHA256(raw), w.TotalTimeout, w.RecordedAt
		records = append(records, createNormalTailProgramWindowRecord{w, previous})
	}
	return records, nil
}
func readCreateNormalTailProgramWindow(store secretStore, e *environmentCreateExecution, original createNormalTailProgramAdmission) (*createNormalTailProgramWindow, error) {
	records, err := readCreateNormalTailProgramWindows(store, e, original)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return &records[len(records)-1].Window, nil
}

// All runtime and pure history consumers use the same effective bound. Reading
// historical proof never applies today's clock to completed native operations.
func readCreateNormalTailProgramAdmission(store secretStore, e *environmentCreateExecution) (createNormalTailProgramAdmission, error) {
	original, err := readCreateNormalTailProgramOriginalAdmission(store, e)
	if err != nil {
		return original, err
	}
	w, err := readCreateNormalTailProgramWindow(store, e, original)
	if err == nil && w != nil {
		original.Deadline = w.Deadline
	}
	return original, err
}

func admitCreateNormalTailProgramWindow(ctx context.Context, e *environmentCreateExecution, original createNormalTailProgramAdmission, now time.Time, totals ...time.Duration) (createNormalTailProgramAdmission, error) {
	bad := func() (createNormalTailProgramAdmission, error) {
		return original, environmentCreateBlocker("deployment.execution.deadline", "the explicit total window is expired or differs from the original admitted program")
	}
	if e == nil || e.Check(ctx) != nil || len(totals) > 1 || len(totals) == 1 && totals[0] < 0 {
		return bad()
	}
	var total time.Duration
	if len(totals) == 1 {
		total = totals[0]
	}
	records, err := readCreateNormalTailProgramWindows(e.Store, e, original)
	if err != nil {
		return original, err
	}
	previous := ""
	if len(records) > 0 {
		last := records[len(records)-1]
		if total == 0 || total == last.Window.TotalTimeout {
			if !now.Before(last.Window.Deadline) {
				return bad()
			}
			original.Deadline = last.Window.Deadline
			return original, e.Check(ctx)
		}
		if total < last.Window.TotalTimeout {
			return bad()
		}
		previous = last.RawSHA256
	}
	if total == 0 {
		if !now.Before(original.Deadline) {
			return bad()
		}
		return original, nil
	}
	deadline := original.AdmittedAt.Add(total)
	if total <= original.Deadline.Sub(original.AdmittedAt) || !now.Before(deadline) || now.Before(original.AdmittedAt) {
		return bad()
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return original, err
	}
	w := &createNormalTailProgramWindow{Schema: createNormalTailProgramWindowSchema, Environment: e.Store.Environment, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: original.ProgramSHA256, QualificationSHA256: original.QualificationSHA256, OriginalAdmissionSHA256: deploymentplan.Digest(original), OriginalAdmittedAt: original.AdmittedAt, OriginalDeadline: original.Deadline, RecordedAt: now, Deadline: deadline, TotalTimeout: total, CheckerSHA256: checker, PreviousSHA256: previous}
	raw, err := json.Marshal(w)
	if err != nil {
		return original, err
	}
	if err = e.Check(ctx); err != nil {
		return original, err
	}
	invalidateManagedLateSourceReuse(e)
	if err = writeExclusivePrivateDeploymentJSON(createNormalTailProgramWindowNumberPath(e, len(records)+1), raw); err != nil {
		// An exclusive writer may have won with the same explicit total.
		// Authenticate that complete chain; never overwrite its bytes.
		if errors.Is(err, os.ErrExist) {
			read, re := readCreateNormalTailProgramWindow(e.Store, e, original)
			if re == nil && read != nil && read.TotalTimeout == total && read.Deadline.Equal(deadline) {
				original.Deadline = deadline
				return original, e.Check(ctx)
			}
		}
		return original, err
	}
	if err = syncEnvironmentPreflightDirectory(e.Directory); err != nil {
		return original, err
	}
	read, err := readCreateNormalTailProgramWindow(e.Store, e, original)
	if err != nil || read == nil || deploymentplan.Digest(read) != deploymentplan.Digest(w) {
		return bad()
	}
	original.Deadline = deadline
	return original, e.Check(ctx)
}
