package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Backend interface {
	Read(context.Context, string) (Object, error)
	Write(context.Context, string, Object, bool) (Object, error)
	Wait(context.Context, Resource) error
}

// Controllers on separate hosts must serialize through the selected cluster,
// not merely through their own local SecretStore directory. Leases cannot be
// stolen by an elapsed clock; interrupted mutations require reviewed recovery.
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

// kubectl refreshes its apply bookkeeping during replace when this annotation
// already exists. Ignore only that client metadata in admission results; the
// baseline/current drift projection still fences the original annotation.
func matchesAdmission(body, admitted Object) bool {
	if uid := String(Map(body["metadata"])["uid"]); uid != "" && uid != String(Map(admitted["metadata"])["uid"]) {
		return false
	}
	want, have := Projection(body), Projection(admitted)
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
	return Matches(want, have)
}

// Admit performs no persisted Kubernetes writes. The executor repeats it.
func (p *Plan) Admit(ctx context.Context, backend Backend) error {
	for _, resource := range p.Resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := backend.Read(ctx, resource.ID)
		if err != nil {
			return blocked("deployment.access", resource.ID, "cannot read the selected resource", "restore environment-local read access")
		}
		body, err := resource.Body(current)
		if err != nil {
			return err
		}
		if body != nil {
			var admitted Object
			if admitted, err = backend.Write(ctx, resource.Action, body, true); err != nil {
				return blocked("deployment.admission", resource.ID, "server dry-run rejected the exact desired body", "resolve write permission, immutable fields, admission, quota and namespace prerequisites")
			}
			if admitted == nil || ID(admitted) != resource.ID || !matchesAdmission(body, admitted) {
				return blocked("deployment.admission", resource.ID, "server admission changed the approved resource or consumer configuration", "declare and qualify the admission mutation or repair the policy; never deploy a different image, identity or workload body")
			}
		}
	}
	return nil
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
func (p *Plan) Execute(ctx context.Context, backend Backend, expectedDigest, privateRoot string, qualify, fence, accept func(context.Context, *Plan) error) error {
	if expectedDigest == "" || expectedDigest != p.SHA256() {
		return blocked("deployment.plan", "", "reviewed plan digest does not match", "repeat preflight and use its exact plan SHA-256")
	}
	if qualify == nil || fence == nil || accept == nil {
		return errors.New("full release qualification, input fences and post-deploy acceptance are required before execution")
	}
	leasing, ok := backend.(LeasingBackend)
	if !ok {
		return blocked("deployment.execution.lease", "", "cluster-wide execution fencing is unavailable", "install the environment-bound execution lease adapter before repeating full qualification")
	}
	if err := qualify(ctx, p); err != nil {
		return err
	}
	if p.SHA256() != expectedDigest {
		return errors.New("plan changed during qualification")
	}
	if err := p.Admit(ctx, backend); err != nil {
		return err
	}
	lease, err := leasing.AcquireExecution(ctx, p)
	if err != nil || lease == nil {
		return blocked("deployment.execution.lease", "", "another controller or interrupted operation owns cluster execution, or lease access is unavailable", "inspect the exact environment execution lease and its private journal; complete recovery qualification before releasing an interrupted owner")
	}
	possibleMutation := false
	leaseReleased := false
	defer func() {
		if !possibleMutation && !leaseReleased {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = lease.Release(cleanup)
		}
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
	environmentLock := filepath.Join(privateRoot, "deployment", ".execution-active")
	if err := os.Mkdir(environmentLock, 0700); err != nil {
		return blocked("deployment.execution", "", "another or an interrupted plan owns environment execution", "inspect the environment-local active execution and journal; requalify recovery before releasing its lock")
	}
	defer func() {
		if !possibleMutation || leaseReleased {
			_ = os.RemoveAll(environmentLock)
		}
	}()
	running, err := LockExecutionOwner(environmentLock, true)
	if err != nil {
		return err
	}
	defer running.Close()
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return errors.New("execution controller hostname unavailable")
	}
	owner := lease.Owner()
	if owner.ResourceID != "Lease/"+p.Stack+"-video-cloud/rtk-deployment-execution" || owner.UID == "" || owner.Holder == "" {
		return errors.New("exact execution lease owner receipt unavailable")
	}
	if err := writeExclusiveJSON(filepath.Join(environmentLock, "owner.json"), ExecutionOwner{expectedDigest, p.Inputs.Environment, p.Inputs.Stack, hostname, owner}); err != nil {
		return err
	}
	lock := filepath.Join(directory, ".active")
	if err := os.Mkdir(lock, 0700); err != nil {
		return blocked("deployment.execution", "", "another execution or an interrupted execution owns the private journal", "inspect the recorded outcome and requalify recovery before releasing the environment-local execution lock")
	}
	defer os.Remove(lock)
	if _, err := os.Lstat(filepath.Join(directory, "journal.json")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return blocked("deployment.recovery", "", "this plan already has execution evidence", "inspect the private journal and produce a newly qualified plan; never overwrite an earlier partial execution")
	}
	if err := writeExclusiveJSON(filepath.Join(directory, "plan.json"), p); err != nil {
		return err
	}
	journal := Journal{PlanSHA256: expectedDigest, Owners: map[string]string{}}
	if err := saveJournal(directory, journal); err != nil {
		return err
	}
	phase := ""
	for index, resource := range p.Resources {
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
			if err := fence(ctx, p); err != nil {
				journal.Failed = resource.ID
				_ = saveJournal(directory, journal)
				return blocked("deployment.inputs", resource.ID, "environment configuration, release or PKI bindings changed", "repeat qualification; completed-resource journal retained")
			}
			// Check every not-yet-applied owner before advancing the stage. A
			// retained dependency remains fenced throughout the complete rollout.
			for i, candidate := range p.Resources {
				if i < index && candidate.Action != "retain" {
					current, err := backend.Read(ctx, candidate.ID)
					matches := err == nil
					if candidate.Action == "retire" {
						matches = matches && current == nil
					} else {
						matches = matches && current != nil && Matches(candidate.Desired, current) && journal.Owners[candidate.ID] != "" && String(Map(current["metadata"])["uid"]) == journal.Owners[candidate.ID]
					}
					if !matches {
						journal.Failed = candidate.ID
						_ = saveJournal(directory, journal)
						return blocked("deployment.drift", candidate.ID, "a completed stage owner or desired dependency changed", "stop dependent updates and requalify actual state; never overwrite the concurrent owner")
					}
					continue
				}
				current, err := backend.Read(ctx, candidate.ID)
				if err == nil {
					_, err = candidate.Body(current)
				}
				if err != nil {
					journal.Failed = candidate.ID
					_ = saveJournal(directory, journal)
					return blocked("deployment.drift", candidate.ID, "a stage dependency changed", "repeat qualification; do not overwrite concurrent changes")
				}
			}
			phase = resource.Phase
		}
		current, err := backend.Read(ctx, resource.ID)
		owner := resource.UID
		if err == nil {
			var body Object
			body, err = resource.Body(current)
			if err == nil && body != nil {
				bodyDigest := Digest(body)
				var admitted Object
				admitted, err = backend.Write(ctx, resource.Action, body, true)
				if err == nil && Digest(body) != bodyDigest {
					err = errors.New("admission mutated the selected request body")
				}
				if err == nil && (admitted == nil || ID(admitted) != resource.ID || !matchesAdmission(body, admitted)) {
					err = blocked("deployment.admission", resource.ID, "admission result differs from the approved resource body", "repair or qualify the exact admission mutation before execution")
				}
				if err == nil {
					journal.InFlight = resource.ID
					if err = saveJournal(directory, journal); err != nil {
						return err
					}
					var actual Object
					// A failed response may follow a persisted API write. Preserve the
					// shared lease and local fence until its actual outcome is reviewed.
					possibleMutation = true
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
		if err == nil && String(resource.Desired["kind"]) != "PersistentVolumeClaim" {
			err = backend.Wait(ctx, resource)
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
	}
	verify := func() error {
		if err := p.Verify(ctx, backend); err != nil {
			journal.Failed = "deployment.post-deploy"
			var blocker *Blocker
			if errors.As(err, &blocker) && blocker.Resource != "" {
				journal.Failed = blocker.Resource
			}
			return errors.Join(err, saveJournal(directory, journal))
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
	// even when every Kubernetes resource was retained.
	possibleMutation = true
	if err := accept(ctx, p); err != nil {
		journal.Failed = "deployment.post-deploy"
		var failure *Blocker
		if errors.As(err, &failure) {
			journal.Failed = failure.ID
			_ = saveJournal(directory, journal)
			return &Blocker{ID: failure.ID, Resource: failure.Resource, Cause: failure.Cause, Next: failure.Next}
		}
		_ = saveJournal(directory, journal)
		return blocked("deployment.post-deploy", "", "complete PKI, endpoint and business-flow acceptance failed", "inspect private evidence and repair the actual target; retain the execution lease until reviewed recovery completes")
	}
	if p.SHA256() != expectedDigest || fence(ctx, p) != nil {
		return blocked("deployment.inputs", "", "reviewed inputs changed during acceptance", "requalify the actual target and retained recovery evidence before completing deployment")
	}
	if err := verify(); err != nil {
		return err
	}
	for _, resource := range p.Resources {
		if resource.Action == "retire" {
			continue
		}
		id, owner := resource.ID, journal.Owners[resource.ID]
		actual, err := backend.Read(ctx, id)
		if err != nil || owner == "" || String(Map(actual["metadata"])["uid"]) != owner {
			return blocked("deployment.ownership", id, "recorded owner changed after rollout", "inspect the private journal; do not adopt a replacement implicitly")
		}
	}
	if err := lease.Check(ctx); err != nil {
		return blocked("deployment.execution.lease", "", "cluster execution ownership changed before completion", "reconcile actual state and the execution journal before completing deployment")
	}
	journal.Accepted = true
	if err := saveJournal(directory, journal); err != nil {
		return err
	}
	if err := lease.Release(ctx); err != nil {
		return blocked("deployment.execution.lease", "", "completed resource verification could not release its exact cluster execution owner", "inspect the exact UID-bound lease and journal; do not report deployment completion until release is confirmed")
	}
	leaseReleased = true
	return nil
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
					return "", blocked("deployment.drift", r.ID, "retired resource is present or unreadable", "retain fences; never remove a concurrent replacement owner")
				}
			} else if err != nil || current == nil || journal.Owners[r.ID] == "" || (r.UID != "" && journal.Owners[r.ID] != r.UID) || String(Map(current["metadata"])["uid"]) != journal.Owners[r.ID] || !Matches(r.Desired, current) {
				return "", blocked("deployment.drift", r.ID, "accepted target or ownership changed", "retain fences and requalify the actual target; never overwrite a concurrent owner")
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
			return "", blocked("deployment.access", r.ID, "original resource cannot be inspected", "restore read access; preserve the execution fences")
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
				return blocked("deployment.retirement", resource.ID, "retired owner still exists", "inspect termination blockers without deleting data or concurrent owners")
			}
			continue
		}
		if err != nil || current == nil || !Matches(resource.Desired, current) {
			return blocked("deployment.post-deploy", resource.ID, "actual resource does not match the qualified desired state", "inspect workload health and the private rollout journal before recovery")
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
