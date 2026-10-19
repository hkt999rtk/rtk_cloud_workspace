package deploymentplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Backend interface {
	Read(context.Context, string) (Object, error)
	Write(context.Context, string, Object, bool) (Object, error)
	Wait(context.Context, Resource) error
}

// PhaseFence supplies fresh observations for one stage's dependency checks.
// Individual conditional writes still read their current owner independently.
type PhaseFence func(context.Context, *Plan) (map[string]Object, error)

// ExecutionProgressBackend reports stage transitions without implying success.
type ExecutionProgressBackend interface {
	ExecutionProgress(context.Context, string, string)
}

// Outcomes are emitted only after the phase's checks or writes complete.
type ExecutionProgressResultBackend interface {
	ExecutionProgressResult(context.Context, string, string, bool)
}

// ExecutionResourceProgressBackend observes work on one resource. A complete
// event means its journal entry was saved, not that deployment was accepted.
type ExecutionResourceProgressBackend interface {
	ExecutionResourceProgress(context.Context, int, int, Resource, string)
}

// Only backends with independent, concurrent-safe dry-run operations opt in.
// Progress counts settled checks (including retains), never passed checks.
type ParallelAdmissionBackend interface {
	AdmissionConcurrency() int
	AdmissionProgress(context.Context, int, int)
}

// MaxCheckConcurrency bounds independent read and admission groups.
const MaxCheckConcurrency = 12
const maxAdmissionConcurrency = MaxCheckConcurrency

// Controllers on separate hosts must serialize through the selected cluster,
// not merely through their own local SecretStore directory. Leases cannot be
// stolen by an elapsed clock. Execution releases its own lease on every return;
// abrupt termination or failed cleanup still requires reviewed recovery.
type ExecutionLease interface {
	Owner() LeaseOwner
	Check(context.Context) error
	Release(context.Context) error
}

type LeaseOwner struct {
	ResourceID string `json:"resource_id"`
	UID        string `json:"uid"`
	Holder     string `json:"holder"`
}

type ExecutionOwner struct {
	PlanSHA256  string     `json:"plan_sha256"`
	Environment string     `json:"environment"`
	Stack       string     `json:"stack"`
	Hostname    string     `json:"hostname"`
	Lease       LeaseOwner `json:"lease"`
}
type LeasingBackend interface {
	AcquireExecution(context.Context, *Plan) (ExecutionLease, error)
}

// A fresh-environment lifecycle owns the local fence before Kubernetes exists.
// Its child plans still acquire a real cluster Lease and keep separate journals.
// The adapter must verify the exact live parent owner, then durably bind this
// Lease to that owner before any child mutation. It cannot bypass qualification,
// admission, resource ownership, or the distributed execution Lease.
type ParentExecutionBackend interface {
	CheckParentExecution(context.Context, *Plan, string) error
	RecordParentExecutionLease(context.Context, *Plan, LeaseOwner) error
}

// kubectl refreshes its apply bookkeeping during replace when this annotation
// already exists. Ignore only that client metadata in admission results; the
// baseline/current drift projection still fences the original annotation.
func matchesAdmission(body, admitted Object) bool {
	if uid := String(Map(body["metadata"])["uid"]); uid != "" && uid != String(Map(admitted["metadata"])["uid"]) {
		return false
	}
	want, have, ok := admissionProjections(body, admitted)
	return ok && Matches(want, have)
}

func admissionProjections(body, admitted Object) (Object, Object, bool) {
	want, have := Projection(body), Projection(admitted)
	normalizeDeploymentRevision(want)
	normalizeDeploymentRevision(have)
	if want == nil || have == nil {
		return nil, nil, false
	}
	if !normalizeDaemonSetAdmissionGeneration(body, admitted, have) {
		return want, have, false
	}
	for _, object := range []Object{want, have} {
		metadata := Map(object["metadata"])
		annotations := Map(metadata["annotations"])
		if annotations == nil {
			continue
		}
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(annotations) == 0 {
			delete(metadata, "annotations")
		}
	}
	return want, have, true
}

// DaemonSet updates increment this native counter when the Pod template changes.
// Compare the original request on a copy; ownership and all other fields stay fenced.
func normalizeDaemonSetAdmissionGeneration(body, admitted, have Object) bool {
	const annotation = "deprecated.daemonset.template.generation"
	before, declared := Map(Map(body["metadata"])["annotations"])[annotation]
	after := Map(Map(admitted["metadata"])["annotations"])[annotation]
	if !declared || reflect.DeepEqual(before, after) {
		return true
	}
	if body["apiVersion"] != "apps/v1" || admitted["apiVersion"] != "apps/v1" || body["kind"] != "DaemonSet" || admitted["kind"] != "DaemonSet" {
		return false
	}
	uid := String(Map(body["metadata"])["uid"])
	if uid == "" || uid != String(Map(admitted["metadata"])["uid"]) {
		return false
	}
	oldText, oldString := before.(string)
	newText, newString := after.(string)
	oldCounter, oldErr := strconv.ParseInt(oldText, 10, 64)
	newCounter, newErr := strconv.ParseInt(newText, 10, 64)
	if !oldString || !newString || oldErr != nil || newErr != nil || oldCounter < 1 || newCounter <= oldCounter || newCounter-oldCounter != 1 || strconv.FormatInt(oldCounter, 10) != oldText || strconv.FormatInt(newCounter, 10) != newText {
		return false
	}
	// API JSON generations must remain exact integers, including the increment.
	const maxJSONInteger = float64(1<<53 - 1)
	oldGeneration, oldNumber := Map(body["metadata"])["generation"].(float64)
	newGeneration, newNumber := Map(admitted["metadata"])["generation"].(float64)
	if !oldNumber || !newNumber || !(oldGeneration >= 1 && oldGeneration < maxJSONInteger && newGeneration >= 1 && newGeneration <= maxJSONInteger) || math.Trunc(oldGeneration) != oldGeneration || math.Trunc(newGeneration) != newGeneration || newGeneration != oldGeneration+1 {
		return false
	}
	Map(Map(have["metadata"])["annotations"])[annotation] = before
	return true
}

// Admit performs no persisted Kubernetes writes. The executor repeats it.
func (p *Plan) Admit(ctx context.Context, backend Backend) error {
	if parallel, ok := backend.(ParallelAdmissionBackend); ok {
		if concurrency := parallel.AdmissionConcurrency(); concurrency > 1 {
			return p.admitParallel(ctx, backend, parallel, concurrency)
		}
	}
	for _, resource := range p.Resources {
		if err := admitResource(ctx, backend, resource); err != nil {
			return err
		}
	}
	return nil
}

func admitResource(ctx context.Context, backend Backend, resource Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := backend.Read(ctx, resource.ID)
	if err != nil {
		return preserveReadBlocker(err, blocked("deployment.access", resource.ID, "cannot read the selected resource", "restore environment-local read access"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := resource.Body(current)
	if err != nil {
		return err
	}
	if body == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	admitted, err := backend.Write(ctx, resource.Action, body, true)
	if err != nil {
		return blocked("deployment.admission", resource.ID, "server dry-run rejected the exact desired body", "resolve write permission, immutable fields, admission, quota and namespace prerequisites")
	}
	if admitted == nil || ID(admitted) != resource.ID || !matchesAdmission(body, admitted) {
		return blocked("deployment.admission", resource.ID, admissionMismatchCause("server admission changed the approved resource or consumer configuration", body, admitted), "declare and qualify the admission mutation or repair the policy; never deploy a different image, identity or workload body")
	}
	return nil
}

func (p *Plan) admitParallel(ctx context.Context, backend Backend, parallel ParallelAdmissionBackend, concurrency int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	workers := min(concurrency, maxAdmissionConcurrency, len(p.Resources))
	parallel.AdmissionProgress(ctx, 0, len(p.Resources))
	if workers == 0 {
		return nil
	}
	type result struct {
		index int
		err   error
	}
	jobs := make(chan int)
	results := make(chan result, workers)
	var joined sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		joined.Add(1)
		go func() {
			defer joined.Done()
			for index := range jobs {
				results <- result{index, admitResource(ctx, backend, p.Resources[index])}
			}
		}()
	}
	errorsByIndex := make([]error, len(p.Resources))
	next, active, completed := 0, 0, 0
	open, failed := true, false
	cancelled := ctx.Done()
	for open || active > 0 {
		if open && (next == len(p.Resources) || failed || ctx.Err() != nil) {
			close(jobs)
			open = false
		}
		if ctx.Err() != nil {
			cancelled = nil
		}
		if !open && active == 0 {
			break
		}
		var dispatch chan<- int
		if open {
			dispatch = jobs
		}
		select {
		case dispatch <- next:
			next++
			active++
		case finished := <-results:
			active--
			completed++
			errorsByIndex[finished.index] = finished.err
			failed = failed || finished.err != nil
			// Callbacks run only in the caller, after one check settles.
			parallel.AdmissionProgress(ctx, completed, len(p.Resources))
		case <-cancelled:
			cancelled = nil
		}
	}
	joined.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Dispatch is a prefix of plan order. Join that entire prefix before
	// selecting its first failure, independent of completion order.
	for _, err := range errorsByIndex {
		if err != nil {
			return err
		}
	}
	return nil
}

// Native readers classify private API diagnostics before returning an access
// blocker. Preserve only its exact-resource safe fields, never an outer error
// wrapper or an unrelated owner/operation's diagnostic.
func preserveReadBlocker(err, fallback error) error {
	var failure, defaultFailure *Blocker
	if errors.As(err, &failure) && errors.As(fallback, &defaultFailure) && failure.Resource == defaultFailure.Resource && (failure.ID == "deployment.access.resource" || failure.ID == "deployment.access.storage") {
		return &Blocker{ID: failure.ID, Resource: failure.Resource, Cause: failure.Cause, Next: failure.Next}
	}
	return fallback
}

type Journal struct {
	PlanSHA256        string            `json:"plan_sha256"`
	Completed         []string          `json:"completed"`
	Failed            string            `json:"failed,omitempty"`
	InFlight          string            `json:"in_flight,omitempty"`
	Owners            map[string]string `json:"owners"`
	Accepted          bool              `json:"accepted"`
	AcceptanceStarted bool              `json:"acceptance_started,omitempty"`
}

// Execute requires the current plan, real qualification and a private rollback
// root. It never automatically reverts schema, PKI state or a concurrent update.
func (p *Plan) Execute(ctx context.Context, backend Backend, expectedDigest, privateRoot string, qualify, fence, accept func(context.Context, *Plan) error, phaseFences ...PhaseFence) (resultErr error) {
	return p.execute(ctx, backend, expectedDigest, privateRoot, qualify, fence, accept, false, phaseFences...)
}

// ExecuteResumable continues only recorded, successful resource writes of the
// exact original plan. accept must be a repeatable observation, not a ceremony
// or business transaction. An uncertain in-flight write always needs separate
// reconciliation; an absent owner never authorizes adoption or recreation.
func (p *Plan) ExecuteResumable(ctx context.Context, backend Backend, expectedDigest, privateRoot string, qualify, fence, accept func(context.Context, *Plan) error, phaseFences ...PhaseFence) error {
	return p.execute(ctx, backend, expectedDigest, privateRoot, qualify, fence, accept, true, phaseFences...)
}

func (p *Plan) execute(ctx context.Context, backend Backend, expectedDigest, privateRoot string, qualify, fence, accept func(context.Context, *Plan) error, resumable bool, phaseFences ...PhaseFence) (resultErr error) {
	if expectedDigest == "" || expectedDigest != p.SHA256() {
		return blocked("deployment.plan", "", "reviewed plan digest does not match", "repeat preflight and use its exact plan SHA-256")
	}
	if qualify == nil || fence == nil || accept == nil {
		return errors.New("full release qualification, input fences and post-deploy acceptance are required before execution")
	}
	if len(phaseFences) > 1 || len(phaseFences) == 1 && phaseFences[0] == nil {
		return errors.New("at most one non-nil phase input fence is allowed")
	}
	leasing, ok := backend.(LeasingBackend)
	if !ok {
		return blocked("deployment.execution.lease", "", "cluster-wide execution fencing is unavailable", "install the environment-bound execution lease adapter before repeating full qualification")
	}
	parent, hasParent := backend.(ParentExecutionBackend)
	if hasParent && parent.CheckParentExecution(ctx, p, privateRoot) != nil {
		return blocked("deployment.execution.lease", "", "the fresh-environment parent execution owner cannot be verified", "inspect the original create journal and local execution fence before continuing")
	}
	progress, _ := backend.(ExecutionProgressBackend)
	progressResult, _ := backend.(ExecutionProgressResultBackend)
	activePhase, activeStep := "", ""
	finishProgress := func(passed bool) error {
		if activeStep != "" && progressResult != nil {
			progressResult.ExecutionProgressResult(ctx, activePhase, activeStep, passed && ctx.Err() == nil && p.SHA256() == expectedDigest)
		}
		activePhase, activeStep = "", ""
		if passed {
			if p.SHA256() != expectedDigest {
				return errors.New("plan changed during execution progress reporting")
			}
			return ctx.Err()
		}
		return nil
	}
	defer func() { finishProgress(false) }()
	emitProgress := func(phase, step string) {
		activePhase, activeStep = phase, step
		if progress != nil {
			progress.ExecutionProgress(ctx, phase, step)
		}
	}
	emitProgress("", "qualification")
	if err := qualify(ctx, p); err != nil {
		return err
	}
	if p.SHA256() != expectedDigest {
		return errors.New("plan changed during qualification")
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	emitProgress("", "admission")
	var prior *retainedExecutionEvidence
	admission := p
	if resumable {
		var err error
		prior, err = p.resumableEvidence(privateRoot)
		if err != nil {
			return err
		}
		if prior != nil {
			if err := p.verifyRecorded(ctx, backend, prior.journal); err != nil {
				return err
			}
			admission = p.recordedTarget(prior.journal)
		}
	}
	if err := admission.Admit(ctx, backend); err != nil {
		return err
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	emitProgress("", "lease")
	lease, err := leasing.AcquireExecution(ctx, p)
	if err != nil || lease == nil {
		var failure *Blocker
		if errors.As(err, &failure) && failure.ID == "deployment.execution.cleanup" {
			return &Blocker{ID: failure.ID, Resource: failure.Resource, Cause: failure.Cause, Next: failure.Next}
		}
		return blocked("deployment.execution.lease", "", "another controller or interrupted operation owns cluster execution, or lease access is unavailable", "inspect the exact environment execution lease and its private journal; complete recovery qualification before releasing an interrupted owner")
	}
	var environmentLock string
	var environmentLockInfo os.FileInfo
	var running *os.File
	defer func() {
		_ = finishProgress(false)
		cleanupProgressCtx := context.WithoutCancel(ctx)
		if progress != nil {
			progress.ExecutionProgress(cleanupProgressCtx, "", "cleanup")
		}
		cleanupPassed := false
		defer func() {
			if progressResult != nil {
				progressResult.ExecutionProgressResult(cleanupProgressCtx, "", "cleanup", cleanupPassed)
			}
		}()
		if running != nil {
			defer running.Close()
		}
		// The execution deadline or signal must not cancel release of our own
		// UID-bound lease. Retain the original journal on every outcome.
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil {
			resultErr = errors.Join(blocked("deployment.execution.cleanup", lease.Owner().ResourceID, "the original execution lease could not be released", "inspect the exact lease owner and private journal, then reconcile cleanup before repeating preflight; never delete a concurrent owner's lease"), resultErr)
			return
		}
		if environmentLockInfo != nil {
			// Archive only the directory created by this execution. The process
			// lock stays held until release and archival have both finished.
			current, err := os.Lstat(environmentLock)
			if err != nil || !os.SameFile(environmentLockInfo, current) {
				resultErr = errors.Join(blocked("deployment.execution.cleanup", "", "the original local execution owner changed or cannot be inspected", "preserve the private journal and inspect local execution ownership before repeating preflight"), resultErr)
				return
			}
			archive, err := os.MkdirTemp(filepath.Join(privateRoot, "deployment", "rollback", expectedDigest), "execution-owner-")
			if err != nil {
				resultErr = errors.Join(blocked("deployment.execution.cleanup", "", "a private execution owner archive could not be prepared", "restore private environment-store access and reconcile the retained local owner before repeating preflight"), resultErr)
				return
			}
			if err := os.Rename(environmentLock, filepath.Join(archive, "owner")); err != nil {
				_ = os.Remove(archive)
				resultErr = errors.Join(blocked("deployment.execution.cleanup", "", "the original local execution owner could not be archived", "restore private environment-store access and reconcile the retained local owner before repeating preflight"), resultErr)
				return
			}
		}
		cleanupPassed = true
	}()
	directory := filepath.Join(privateRoot, "deployment", "rollback", expectedDigest)
	if !filepath.IsAbs(privateRoot) {
		return errors.New("absolute environment SecretStore root required")
	}
	for _, d := range []string{privateRoot, filepath.Join(privateRoot, "deployment"), filepath.Join(privateRoot, "deployment", "rollback"), directory} {
		if err := privateDirectory(d); err != nil {
			return err
		}
	}
	// Different reviewed plans still target the same environment. A per-plan
	// journal lock alone would allow them to execute concurrently.
	localLock := filepath.Join(privateRoot, "deployment", ".execution-active")
	owner := lease.Owner()
	if owner.ResourceID != "Lease/"+p.Stack+"-video-cloud/rtk-deployment-execution" || owner.UID == "" || owner.Holder == "" {
		return errors.New("exact execution lease owner receipt unavailable")
	}
	if hasParent {
		if parent.CheckParentExecution(ctx, p, privateRoot) != nil || parent.RecordParentExecutionLease(ctx, p, owner) != nil {
			return blocked("deployment.execution.lease", "", "the child Lease could not be bound to the original fresh-environment owner", "preserve both journals and inspect the parent execution fence")
		}
	} else {
		if err := os.Mkdir(localLock, 0700); err != nil {
			return blocked("deployment.execution", "", "another or an interrupted plan owns environment execution", "inspect the environment-local active execution and journal; requalify recovery before releasing its lock")
		}
		environmentLock = localLock
		environmentLockInfo, err = os.Lstat(environmentLock)
		if err != nil {
			return errors.New("original local execution directory cannot be inspected")
		}
		running, err = LockExecutionOwner(environmentLock, true)
		if err != nil {
			return err
		}
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			return errors.New("execution controller hostname unavailable")
		}
		if err := writeExclusiveJSON(filepath.Join(environmentLock, "owner.json"), ExecutionOwner{expectedDigest, p.Inputs.Environment, p.Inputs.Stack, hostname, owner}); err != nil {
			return err
		}
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	lock := filepath.Join(directory, ".active")
	if err := os.Mkdir(lock, 0700); err != nil {
		return blocked("deployment.execution", "", "another execution or an interrupted execution owns the private journal", "inspect the recorded outcome and requalify recovery before releasing the environment-local execution lock")
	}
	defer os.Remove(lock)
	_, journalErr := os.Lstat(filepath.Join(directory, "journal.json"))
	_, planErr := os.Lstat(filepath.Join(directory, "plan.json"))
	journal := Journal{PlanSHA256: expectedDigest, Owners: map[string]string{}}
	if resumable && prior != nil {
		fresh, err := p.resumableEvidence(privateRoot)
		if err != nil || fresh == nil || !bytes.Equal(prior.planRaw, fresh.planRaw) || !bytes.Equal(prior.journalRaw, fresh.journalRaw) {
			return existingExecutionEvidenceBlocker()
		}
		journal = fresh.journal
		if err := p.verifyRecorded(ctx, backend, journal); err != nil {
			return err
		}
		if journal.Accepted {
			if err := fence(ctx, p); err != nil {
				return err
			}
			if err := accept(ctx, p); err != nil {
				return err
			}
			if ctx.Err() != nil || p.SHA256() != expectedDigest || fence(ctx, p) != nil || lease.Check(ctx) != nil {
				return existingExecutionEvidenceBlocker()
			}
			if err := p.verifyRecorded(ctx, backend, journal); err != nil {
				return err
			}
			current, err := p.resumableEvidence(privateRoot)
			if err != nil || current == nil || !bytes.Equal(current.planRaw, fresh.planRaw) || !bytes.Equal(current.journalRaw, fresh.journalRaw) || ctx.Err() != nil || fence(ctx, p) != nil || lease.Check(ctx) != nil {
				return existingExecutionEvidenceBlocker()
			}
			return p.saveAcceptedTargetVerification(privateRoot, fresh.journalRaw, owner, false)
		}
	} else if !errors.Is(journalErr, os.ErrNotExist) || !errors.Is(planErr, os.ErrNotExist) {
		emitProgress("", "recovery")
		evidence, err := p.acceptedRetainEvidence(directory)
		if err != nil {
			return err
		}
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if p.SHA256() != expectedDigest || fence(ctx, p) != nil || p.SHA256() != expectedDigest {
				return blocked("deployment.inputs", "", "reviewed inputs changed during retained-target revalidation", "repeat qualification and preserve the original accepted execution evidence")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := lease.Check(ctx); err != nil {
				return blocked("deployment.execution.lease", "", "execution ownership changed during retained-target revalidation", "review actual state and preserve the original accepted execution evidence")
			}
			if outcome, err := p.ReconcileOutcome(ctx, backend, evidence.journal); err != nil {
				return err
			} else if outcome != "accepted-target" {
				return existingExecutionEvidenceBlocker()
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if p.SHA256() != expectedDigest {
				return blocked("deployment.inputs", "", "reviewed plan changed during retained-target revalidation", "repeat qualification and preserve the original accepted execution evidence")
			}
			if err := lease.Check(ctx); err != nil {
				return blocked("deployment.execution.lease", "", "execution ownership changed during retained-target revalidation", "review actual state and preserve the original accepted execution evidence")
			}
			return nil
		}
		if err := check(); err != nil {
			return err
		}
		if err := finishProgress(true); err != nil {
			return err
		}
		emitProgress("", "acceptance")
		if err := accept(ctx, p); err != nil {
			var failure *Blocker
			if errors.As(err, &failure) {
				return &Blocker{ID: failure.ID, Resource: failure.Resource, Cause: failure.Cause, Next: failure.Next}
			}
			return blocked("deployment.post-deploy", "", "complete PKI, endpoint and business-flow acceptance failed during retained-target revalidation", "inspect private evidence and repair the actual target; preserve the original accepted execution evidence")
		}
		if err := finishProgress(true); err != nil {
			return err
		}
		emitProgress("", "recovery")
		if err := check(); err != nil {
			return err
		}
		fresh, err := p.acceptedRetainEvidence(directory)
		if err != nil || !bytes.Equal(evidence.planRaw, fresh.planRaw) || !bytes.Equal(evidence.journalRaw, fresh.journalRaw) {
			return existingExecutionEvidenceBlocker()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.SHA256() != expectedDigest {
			return blocked("deployment.inputs", "", "reviewed plan changed before retained-target verification was recorded", "repeat qualification and preserve the original accepted execution evidence")
		}
		if err := lease.Check(ctx); err != nil {
			return blocked("deployment.execution.lease", "", "execution ownership changed before retained-target verification was recorded", "review actual state and preserve the original accepted execution evidence")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.SHA256() != expectedDigest {
			return blocked("deployment.inputs", "", "reviewed plan changed before retained-target verification was recorded", "repeat qualification and preserve the original accepted execution evidence")
		}
		if err := p.saveAcceptedTargetVerification(privateRoot, evidence.journalRaw, owner, true); err != nil {
			return err
		}
		return finishProgress(true)
	}
	if prior == nil {
		if err := writeExclusiveJSON(filepath.Join(directory, "plan.json"), p); err != nil {
			return err
		}
		if err := saveJournal(directory, journal); err != nil {
			return err
		}
	}
	resourceProgress, _ := backend.(ExecutionResourceProgressBackend)
	emitResourceProgress := func(index int, resource Resource, step string) error {
		if resourceProgress != nil {
			resourceProgress.ExecutionResourceProgress(ctx, index, len(p.Resources), resource, step)
		}
		var progressErr error
		if p.SHA256() != expectedDigest {
			progressErr = errors.New("plan changed during execution")
		} else {
			progressErr = ctx.Err()
		}
		if progressErr != nil {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
		}
		return progressErr
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	phase := ""
	readinessFence := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.SHA256() != expectedDigest {
			return errors.New("plan changed during workload readiness")
		}
		if err := lease.Check(ctx); err != nil {
			return blocked("deployment.execution.lease", "", "the exact cluster execution owner changed during workload readiness", "retain the native owners and execution journal; stop dependent writes")
		}
		return nil
	}
	for index, resource := range p.Resources {
		if index < len(journal.Completed) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.SHA256() != expectedDigest {
			return errors.New("plan changed during execution")
		}
		if err := lease.Check(ctx); err != nil {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
			return blocked("deployment.execution.lease", resource.ID, "the exact cluster execution owner changed or cannot be verified", "stop all dependent writes and review the interrupted owner's recovery journal; never acquire a replacement lease implicitly")
		}
		if resource.Phase != phase {
			emitProgress(resource.Phase, "fence")
			var observations map[string]Object
			var fenceErr error
			if len(phaseFences) == 1 {
				observations, fenceErr = phaseFences[0](ctx, p)
			} else {
				fenceErr = fence(ctx, p)
			}
			if fenceErr == nil {
				fenceErr = ctx.Err()
			}
			if fenceErr == nil && p.SHA256() != expectedDigest {
				journal.Failed = resource.ID
				_ = saveJournal(directory, journal)
				return errors.New("plan changed during execution")
			}
			if fenceErr != nil {
				journal.Failed = resource.ID
				_ = saveJournal(directory, journal)
				var failure *Blocker
				if errors.As(fenceErr, &failure) && failure != nil {
					selected := failure.Resource
					if selected == "" {
						selected = resource.ID
					}
					return &Blocker{ID: failure.ID, Resource: selected, Cause: failure.Cause, Next: failure.Next}
				}
				return blocked("deployment.inputs", resource.ID, "environment configuration, release or PKI bindings changed", "repeat qualification; completed-resource journal retained")
			}
			readDependency := func(id string) (Object, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if current := observations[id]; current != nil {
					return current, nil
				}
				// Inventory omissions do not prove absence, including planned
				// creates and retired owners. Resolve every missing ID directly.
				return backend.Read(ctx, id)
			}
			// Check every not-yet-applied owner before advancing the stage. A
			// retained dependency remains fenced throughout the complete rollout.
			for i, candidate := range p.Resources {
				if err := ctx.Err(); err != nil {
					journal.Failed = candidate.ID
					_ = saveJournal(directory, journal)
					return err
				}
				if i < index && candidate.Action != "retain" {
					current, err := readDependency(candidate.ID)
					matches := err == nil
					if candidate.Action == "retire" {
						matches = matches && current == nil
					} else {
						matches = matches && current != nil && Matches(candidate.Desired, current) && journal.Owners[candidate.ID] != "" && String(Map(current["metadata"])["uid"]) == journal.Owners[candidate.ID]
					}
					if !matches {
						journal.Failed = candidate.ID
						_ = saveJournal(directory, journal)
						return preserveReadBlocker(err, blocked("deployment.drift", candidate.ID, "a completed stage owner or desired dependency changed", "stop dependent updates and requalify actual state; never overwrite the concurrent owner"))
					}
					continue
				}
				current, err := readDependency(candidate.ID)
				if err == nil {
					if journal.Owners[candidate.ID] != "" {
						err = recordedResourceMatches(candidate, journal.Owners[candidate.ID], current)
					} else {
						_, err = candidate.Body(current)
					}
				}
				if err != nil {
					journal.Failed = candidate.ID
					_ = saveJournal(directory, journal)
					return preserveReadBlocker(err, blocked("deployment.drift", candidate.ID, "a stage dependency changed", "repeat qualification; do not overwrite concurrent changes"))
				}
			}
			if err := finishProgress(true); err != nil {
				return err
			}
			emitProgress(resource.Phase, "apply")
			phase = resource.Phase
		}
		if p.SHA256() != expectedDigest {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
			return errors.New("plan changed during execution")
		}
		if err := ctx.Err(); err != nil {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
			return err
		}
		if err := p.waitWorkloadReadinessDependencies(ctx, backend, journal, resource, readinessFence); err != nil {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
			return err
		}
		if err := emitResourceProgress(index, resource, "read"); err != nil {
			return err
		}
		current, err := backend.Read(ctx, resource.ID)
		if err != nil {
			err = preserveReadBlocker(err, blocked("deployment.access", resource.ID, "cannot read the selected resource", "restore environment-local read access; preserve the execution journal and ownership fences"))
		}
		owner := resource.UID
		if err == nil && journal.Owners[resource.ID] != "" {
			owner = journal.Owners[resource.ID]
			err = recordedResourceMatches(resource, owner, current)
		} else if err == nil {
			var body Object
			body, err = resource.Body(current)
			if err == nil && body != nil {
				bodyDigest := Digest(body)
				var admitted Object
				if err = emitResourceProgress(index, resource, "dry-run"); err == nil {
					admitted, err = backend.Write(ctx, resource.Action, body, true)
				}
				if err == nil && Digest(body) != bodyDigest {
					err = errors.New("admission mutated the selected request body")
				}
				if err == nil && (admitted == nil || ID(admitted) != resource.ID || !matchesAdmission(body, admitted)) {
					err = blocked("deployment.admission", resource.ID, admissionMismatchCause("admission result differs from the approved resource body", body, admitted), "repair or qualify the exact admission mutation before execution")
				}
				if err == nil {
					err = emitResourceProgress(index, resource, "apply")
				}
				if err == nil {
					journal.InFlight = resource.ID
					if err = saveJournal(directory, journal); err != nil {
						return err
					}
					var actual Object
					// A failed response may follow a persisted API write. Preserve its
					// in-flight journal evidence even though execution locks are released.
					actual, err = backend.Write(ctx, resource.Action, body, false)
					owner = String(Map(actual["metadata"])["uid"])
					if err == nil && (owner == "" || (resource.UID != "" && owner != resource.UID)) {
						err = errors.New("Kubernetes update returned an unbound owner")
					}
					if err == nil {
						journal.Owners[resource.ID] = owner
						journal.InFlight = ""
						if err = saveJournal(directory, journal); err != nil {
							return err
						}
					}
				}
			}
		}
		// Delayed-binding storage needs its consuming Pod to be scheduled first.
		// All PVCs are still required to bind during final verification.
		if err == nil && String(resource.Desired["kind"]) != "PersistentVolumeClaim" && !deferredWorkloadReadiness(resource) {
			if err = emitResourceProgress(index, resource, "wait"); err == nil {
				err = backend.Wait(ctx, resource)
			}
		}
		if err != nil {
			journal.Failed = resource.ID
			_ = saveJournal(directory, journal)
			return fmt.Errorf("deployment stopped at %s; private rollback and completed-resource journal retained; requalify recovery: %w", resource.ID, err)
		}
		journal.Completed = append(journal.Completed, resource.ID)
		journal.Owners[resource.ID] = owner
		if err = saveJournal(directory, journal); err != nil {
			return err
		}
		if err := emitResourceProgress(index, resource, "complete"); err != nil {
			return err
		}
		if index+1 == len(p.Resources) || p.Resources[index+1].Phase != phase {
			if phase == "workload" {
				if failedID, err := p.waitWorkloadReadinessBlock(ctx, backend, journal, index, readinessFence, func(index int, resource Resource) error {
					return emitResourceProgress(index, resource, "wait")
				}); err != nil {
					journal.Failed = failedID
					_ = saveJournal(directory, journal)
					return err
				}
			}
			if err := finishProgress(true); err != nil {
				return err
			}
		}
	}
	verify := func() error {
		emitProgress("", "verification")
		if err := p.Verify(ctx, backend); err != nil {
			journal.Failed = "deployment.post-deploy"
			var blocker *Blocker
			if errors.As(err, &blocker) && blocker.Resource != "" {
				journal.Failed = blocker.Resource
			}
			return errors.Join(err, saveJournal(directory, journal))
		}
		if err := finishProgress(true); err != nil {
			return err
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	// Keep distributed ownership through PKI, public endpoint and business
	// acceptance. Healthy Kubernetes objects alone do not complete deployment.
	if err := lease.Check(ctx); err != nil {
		return blocked("deployment.execution.lease", "", "execution ownership changed before acceptance", "review actual state and the private execution journal before recovery")
	}
	journal.AcceptanceStarted = true
	if err := saveJournal(directory, journal); err != nil {
		return err
	}
	// Acceptance may seal PKI or commit representative business transactions,
	// even when every Kubernetes resource was retained. Keep its outcome in the
	// journal after releasing execution locks; a failed attempt is not unstarted.
	emitProgress("", "acceptance")
	if err := accept(ctx, p); err != nil {
		journal.Failed = "deployment.post-deploy"
		var failure *Blocker
		if errors.As(err, &failure) {
			journal.Failed = failure.ID
			_ = saveJournal(directory, journal)
			return &Blocker{ID: failure.ID, Resource: failure.Resource, Cause: failure.Cause, Next: failure.Next}
		}
		_ = saveJournal(directory, journal)
		return blocked("deployment.post-deploy", "", "complete PKI, endpoint and business-flow acceptance failed", "inspect private evidence and repair the actual target; qualify a new plan or recovery before another deployment")
	}
	if p.SHA256() != expectedDigest || fence(ctx, p) != nil {
		return blocked("deployment.inputs", "", "reviewed inputs changed during acceptance", "requalify the actual target and retained recovery evidence before completing deployment")
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	emitProgress("", "ownership")
	for _, resource := range p.Resources {
		if resource.Action == "retire" {
			continue
		}
		id, owner := resource.ID, journal.Owners[resource.ID]
		actual, err := backend.Read(ctx, id)
		if err != nil || owner == "" || String(Map(actual["metadata"])["uid"]) != owner {
			return preserveReadBlocker(err, blocked("deployment.ownership", id, "recorded owner changed after rollout", "inspect the private journal; do not adopt a replacement implicitly"))
		}
	}
	if err := lease.Check(ctx); err != nil {
		return blocked("deployment.execution.lease", "", "cluster execution ownership changed before completion", "reconcile actual state and the execution journal before completing deployment")
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	emitProgress("", "journal")
	if resumable && (ctx.Err() != nil || p.SHA256() != expectedDigest || fence(ctx, p) != nil || lease.Check(ctx) != nil) {
		return existingExecutionEvidenceBlocker()
	}
	journal.Accepted = true
	journal.Failed = ""
	if err := saveJournal(directory, journal); err != nil {
		return err
	}
	if err := finishProgress(true); err != nil {
		return err
	}
	return nil
}

type retainedExecutionEvidence struct {
	planRaw, journalRaw []byte
	journal             Journal
}

func existingExecutionEvidenceBlocker() error {
	return blocked("deployment.recovery", "", "this plan already has execution evidence that cannot be revalidated as a complete accepted retained target", "inspect the private journal and produce a newly qualified plan; never overwrite or continue an earlier partial execution")
}

// Only an exact, already accepted all-retain plan may be checked again. This
// proves a current outcome without changing the original execution's evidence.
func (p *Plan) acceptedRetainEvidence(directory string) (retainedExecutionEvidence, error) {
	var evidence retainedExecutionEvidence
	if len(p.Resources) == 0 {
		return evidence, existingExecutionEvidenceBlocker()
	}
	for _, resource := range p.Resources {
		if resource.Action != "retain" {
			return evidence, existingExecutionEvidenceBlocker()
		}
	}
	planRaw, err := readPrivateExecutionEvidence(filepath.Join(directory, "plan.json"))
	expected, encodeErr := json.Marshal(p)
	if err != nil || encodeErr != nil || !bytes.Equal(planRaw, expected) {
		return evidence, existingExecutionEvidenceBlocker()
	}
	journalRaw, err := readPrivateExecutionEvidence(filepath.Join(directory, "journal.json"))
	if err != nil {
		return evidence, existingExecutionEvidenceBlocker()
	}
	decoder := json.NewDecoder(bytes.NewReader(journalRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence.journal); err != nil {
		return evidence, existingExecutionEvidenceBlocker()
	}
	var extra any
	canonicalJournal, encodeErr := json.Marshal(evidence.journal)
	if decoder.Decode(&extra) != io.EOF || encodeErr != nil || !bytes.Equal(journalRaw, canonicalJournal) || !evidence.journal.Accepted || !evidence.journal.AcceptanceStarted || evidence.journal.PlanSHA256 != p.SHA256() || evidence.journal.Failed != "" || evidence.journal.InFlight != "" || len(evidence.journal.Completed) != len(p.Resources) || len(evidence.journal.Owners) != len(p.Resources) {
		return evidence, existingExecutionEvidenceBlocker()
	}
	evidence.planRaw, evidence.journalRaw = planRaw, journalRaw
	return evidence, nil
}

func readPrivateExecutionEvidence(path string) ([]byte, error) {
	const limit = 32 << 20
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("original private execution evidence unavailable")
	}
	f := os.NewFile(uintptr(fd), "execution-evidence")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit {
		return nil, errors.New("bounded private regular execution evidence required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("bounded private execution evidence required")
	}
	return raw, nil
}

func (p *Plan) saveAcceptedTargetVerification(privateRoot string, journalRaw []byte, owner LeaseOwner, retained bool) error {
	directory := filepath.Join(privateRoot, "deployment", "recovery")
	if err := privateDirectory(directory); err != nil {
		return err
	}
	attempt, err := os.MkdirTemp(directory, "revalidated-")
	if err != nil {
		return errors.New("private retained-target verification directory unavailable")
	}
	outcome := "accepted-target-revalidated"
	if retained {
		outcome = "accepted-retained-target-revalidated"
	}
	receipt := struct {
		PlanSHA256            string     `json:"plan_sha256"`
		OriginalJournalSHA256 string     `json:"original_journal_sha256"`
		Outcome               string     `json:"outcome"`
		ResourceCount         int        `json:"resource_count"`
		Lease                 LeaseOwner `json:"lease"`
		VerifiedAt            time.Time  `json:"verified_at"`
	}{p.SHA256(), fmt.Sprintf("%x", sha256.Sum256(journalRaw)), outcome, len(p.Resources), owner, time.Now().UTC()}
	return writeExclusiveJSON(filepath.Join(attempt, "verification.json"), receipt)
}

// The OS releases this lock when the controller exits. Reconciliation must run
// on the recorded controller with its original private directory, never a copy.
func LockExecutionOwner(directory string, create bool) (*os.File, error) {
	flags := syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if create {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Open(filepath.Join(directory, "running.lock"), flags, 0600)
	if err != nil {
		return nil, errors.New("original execution process lock unavailable")
	}
	f := os.NewFile(uintptr(fd), "execution-owner")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("original execution lock must be a private regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("the original execution controller or another reconciler is still running")
	}
	return f, nil
}

// ReconcileOutcome never writes resources or performs rollback. Only a provably
// unstarted mutation or an already accepted, unchanged target can release fences.
func (p *Plan) ReconcileOutcome(ctx context.Context, backend Backend, journal Journal) (string, error) {
	if p == nil || p.Version != Version || len(p.Resources) == 0 || journal.PlanSHA256 != p.SHA256() || journal.InFlight != "" {
		return "", blocked("deployment.recovery", "", "execution evidence is invalid or a write outcome remains uncertain", "retain both fences; resolve the recorded request and independently verify security/data state before recovery")
	}
	resources := map[string]Resource{}
	for _, r := range p.Resources {
		resources[r.ID] = r
	}
	seen := map[string]bool{}
	for _, id := range journal.Completed {
		r, ok := resources[id]
		if !ok || seen[id] || (!journal.Accepted && r.Action != "retain") {
			return "", blocked("deployment.recovery", id, "execution has partial changes or invalid completed-resource evidence", "retain the fences and qualify a separate recovery; automatic rollback or continuation is forbidden")
		}
		seen[id] = true
	}
	for id := range journal.Owners {
		r, ok := resources[id]
		if !ok || (!journal.Accepted && r.Action != "retain") {
			return "", blocked("deployment.recovery", id, "one or more resource writes have completed", "retain the fences and qualify the actual partial target before recovery")
		}
	}
	if journal.Accepted {
		if !journal.AcceptanceStarted || journal.Failed != "" || len(seen) != len(p.Resources) {
			return "", errors.New("complete accepted execution evidence required")
		}
		for _, r := range p.Resources {
			if err := backend.Wait(ctx, r); err != nil {
				return "", blocked("deployment.recovery", r.ID, "accepted target is no longer ready", "repair and reverify actual target health while retaining ownership fences")
			}
			current, err := backend.Read(ctx, r.ID)
			if r.Action == "retire" {
				if err != nil || current != nil {
					return "", preserveReadBlocker(err, blocked("deployment.drift", r.ID, "retired resource is present or unreadable", "retain fences; never remove a concurrent replacement owner"))
				}
			} else if err != nil || current == nil || journal.Owners[r.ID] == "" || (r.UID != "" && journal.Owners[r.ID] != r.UID) || String(Map(current["metadata"])["uid"]) != journal.Owners[r.ID] || !Matches(r.Desired, current) {
				return "", preserveReadBlocker(err, blocked("deployment.drift", r.ID, "accepted target or ownership changed", "retain fences and requalify the actual target; never overwrite a concurrent owner"))
			}
			if r.Action == "retain" {
				if _, err := r.Body(current); err != nil {
					return "", err
				}
			}
		}
		return "accepted-target", nil
	}
	if journal.AcceptanceStarted {
		return "", blocked("deployment.recovery", "", "acceptance began but completion is unverified", "retain the fences; verify PKI and business-flow outcomes before a separately qualified recovery")
	}
	for _, r := range p.Resources {
		current, err := backend.Read(ctx, r.ID)
		if err != nil {
			return "", preserveReadBlocker(err, blocked("deployment.access", r.ID, "original resource cannot be inspected", "restore read access; preserve the execution fences"))
		}
		if _, err := r.Body(current); err != nil {
			return "", err
		}
	}
	return "no-resource-writes", nil
}

func (p *Plan) Verify(ctx context.Context, backend Backend) error {
	for _, resource := range p.Resources {
		if resource.Action != "retire" && String(resource.Desired["kind"]) == "PersistentVolumeClaim" {
			if err := backend.Wait(ctx, resource); err != nil {
				return blocked("deployment.storage.binding", resource.ID, "persistent storage has not bound after consumer rollout", "repair storage capacity, topology and consumer scheduling; keep the execution journal and ownership fence for recovery")
			}
		}
		current, err := backend.Read(ctx, resource.ID)
		if resource.Action == "retire" {
			if err != nil || current != nil {
				return preserveReadBlocker(err, blocked("deployment.retirement", resource.ID, "retired owner still exists", "inspect termination blockers without deleting data or concurrent owners"))
			}
			continue
		}
		if err != nil || current == nil || !Matches(resource.Desired, current) {
			return preserveReadBlocker(err, blocked("deployment.post-deploy", resource.ID, "actual resource does not match the qualified desired state", "inspect workload health and the private rollout journal before recovery"))
		}
		if resource.UID != "" && String(Map(current["metadata"])["uid"]) != resource.UID {
			return blocked("deployment.ownership", resource.ID, "owner changed after rollout", "inspect ownership; do not adopt a replacement implicitly")
		}
		if resource.Action == "retain" {
			if _, err := resource.Body(current); err != nil {
				return err
			}
		}
	}
	return nil
}

func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot create private deployment directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("real private 0700 deployment directory required")
	}
	return nil
}
func writeExclusiveJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode private rollback")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("existing rollback must be a private regular file")
		}
		old, e := os.ReadFile(path)
		if e == nil && string(old) == string(raw) {
			return nil
		}
		return errors.New("existing rollback differs; do not overwrite recovery evidence")
	}
	if err != nil {
		return errors.New("cannot create private rollback file")
	}
	_, writeErr := f.Write(raw)
	return errors.Join(writeErr, f.Sync(), f.Close())
}
func saveJournal(directory string, journal Journal) error {
	raw, _ := json.Marshal(journal)
	f, err := os.CreateTemp(directory, ".journal-")
	if err != nil {
		return errors.New("cannot persist rollout journal")
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(raw)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return errors.New("cannot persist rollout journal")
	}
	return os.Rename(path, filepath.Join(directory, "journal.json"))
}
