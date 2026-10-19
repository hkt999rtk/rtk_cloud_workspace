package main

import (
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

// Independent CRL proof readers reconstruct their historical executor without
// a live command. Bind only the exact canonical owner/journal they already
// authenticate, solely for typed new-volume projection. This snapshot cannot
// pass Execution.Check or authorize native IO.
func bindManagedLateRedisRecordedVolumeExecution(store secretStore, e *environmentCreateExecution) error {
	bad := func() error { return createOpenBaoACKBlock("redis-durable-canonical-volume-provenance") }
	if e == nil || e.Store != store {
		return bad()
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return err
	}
	if len(ledger.Volumes) == 18 {
		return nil
	}
	var active managedTransportVolumeLedgerActive
	if createOpenBaoACKCompletionRead(store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return bad()
	}
	owner := e.owner
	if owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != e.Journal.PlanSHA256 || filepath.IsAbs(owner.Journal) || filepath.Clean(owner.Journal) != owner.Journal || !strings.HasPrefix(owner.Journal, "deployment/create/executions/"+owner.PlanSHA256+"/execution-") || filepath.Base(owner.Journal) != "journal.json" {
		return bad()
	}
	var current environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil || !createOpenBaoACKCompletionMembership(e.Journal, current, owner) {
		return bad()
	}
	ownerPath := "deployment/.execution-active/owner.json"
	if !current.CompletedAt.IsZero() {
		ownerPath = filepath.Join(filepath.Dir(owner.Journal), "owner/owner.json")
	}
	var actualOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, ownerPath, &actualOwner) != nil || deploymentplan.Digest(actualOwner) != deploymentplan.Digest(owner) {
		return bad()
	}
	actual := &environmentCreateExecution{Store: store, owner: actualOwner, Journal: current, JournalPath: filepath.Join(store.Root, owner.Journal), Directory: filepath.Join(store.Root, filepath.Dir(owner.Journal))}
	view := *e
	view.volumeExecution = actual
	if _, err := managedLateRedisRecordedVolumeProjection(store, &view, ledger); err != nil {
		return err
	}
	var closing environmentCreateJournal
	var closingOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, owner.Journal, &closing) != nil || deploymentplan.Digest(closing) != deploymentplan.Digest(current) || createOpenBaoACKCompletionRead(store, ownerPath, &closingOwner) != nil || deploymentplan.Digest(closingOwner) != deploymentplan.Digest(actualOwner) {
		return bad()
	}
	e.volumeExecution = actual
	return nil
}

// Only the held actual parent may supply new allocation evidence. A private
// historical parsing view borrows that parent without becoming native authority.
func managedLateRedisRecordedVolumeProjection(store secretStore, e *environmentCreateExecution, ledger environmentCreateResourceLedger) (environmentCreateResourceLedger, error) {
	bad := func() (environmentCreateResourceLedger, error) {
		return environmentCreateResourceLedger{}, createOpenBaoACKBlock("redis-durable-recorded-volumes")
	}
	actual, sourceErr := managedLateRedisVolumeActualExecution(store, e)
	if sourceErr != nil {
		return bad()
	}
	original, redis, err := readManagedLateRedisDurableVolumeAuthority(store, actual)
	if err != nil {
		return bad()
	}
	if len(ledger.Volumes) > len(original)+1 {
		// Independent completed-proof readers hold only the authenticated owner
		// and canonical journal. Their frozen target lives beside that journal.
		if actual.Directory == "" {
			view := *actual
			view.Directory = filepath.Join(store.Root, filepath.Dir(actual.owner.Journal))
			actual = &view
		}
		stages, err := readCreateNormalTailVolumeStages(store, actual)
		if err != nil {
			return bad()
		}
		return managedLatePlannedVolumeStagesProjection(ledger, original, redis, stages)
	}
	return managedLateRedisDurableVolumeProjection(ledger, original, redis)
}

// A full ledger and active pointer are checked before this function is used.
// The qualified historical eighteen remain literal after typed allocations.
func managedLateRedisHistoricalVolumes(store secretStore, e *environmentCreateExecution, ledger environmentCreateResourceLedger) (environmentCreateResourceLedger, error) {
	if len(ledger.Volumes) == 18 {
		return ledger, nil
	}
	return managedLateRedisRecordedVolumeProjection(store, e, ledger)
}

// Nested admitted readers may rewind the authenticated392 parsing view again.
// Reconstruct that SAME token-bound view before checking its literal subprefix.
func managedLateRedisVolumeActualExecution(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, error) {
	bad := func() (*environmentCreateExecution, error) {
		return nil, createOpenBaoACKBlock("redis-durable-volume-source")
	}
	if e == nil || e.Store != store {
		return bad()
	}
	if e.latePureSource == nil {
		if e.volumeExecution == nil {
			return e, nil
		}
		actual := e.volumeExecution
		if actual == e || actual.volumeExecution != nil || actual.latePureSource != nil || actual.Store != store || deploymentplan.Digest(e.owner) != deploymentplan.Digest(actual.owner) || len(e.Journal.Entries) > len(actual.Journal.Entries) || len(e.Journal.KubernetesLeases) > len(actual.Journal.KubernetesLeases) {
			return bad()
		}
		if deploymentplan.Digest(e.Journal) == deploymentplan.Digest(actual.Journal) {
			return actual, nil
		}
		if !e.Journal.CompletedAt.IsZero() || len(e.Journal.Entries) > 392 && len(e.Journal.Entries) != 434 {
			return bad()
		}
		prefix := actual.Journal
		prefix.CompletedAt = time.Time{}
		prefix.Entries = prefix.Entries[:len(e.Journal.Entries)]
		prefix.KubernetesLeases = prefix.KubernetesLeases[:len(e.Journal.KubernetesLeases)]
		if deploymentplan.Digest(prefix) != deploymentplan.Digest(e.Journal) {
			return bad()
		}
		return actual, nil
	}
	actual := e.latePureSource.parent
	if actual == nil || len(actual.Journal.Entries) < 392 || e.latePureSource.completedProofSHA == "" || e.latePureSource.sourceLeases < 0 || len(actual.Journal.KubernetesLeases) < e.latePureSource.sourceLeases {
		return bad()
	}
	view := *e
	view.Journal = actual.Journal
	view.Journal.CompletedAt = time.Time{}
	view.Journal.Entries = slices.Clone(actual.Journal.Entries[:392])
	view.Journal.KubernetesLeases = slices.Clone(actual.Journal.KubernetesLeases[:e.latePureSource.sourceLeases])
	if !managedLatePureSourceViewMatches(actual, &view) || len(e.Journal.Entries) > 392 || len(e.Journal.KubernetesLeases) > len(view.Journal.KubernetesLeases) || !e.Journal.CompletedAt.IsZero() {
		return bad()
	}
	prefix := view.Journal
	prefix.Entries = prefix.Entries[:len(e.Journal.Entries)]
	prefix.KubernetesLeases = prefix.KubernetesLeases[:len(e.Journal.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(e.Journal) {
		return bad()
	}
	return actual, nil
}
