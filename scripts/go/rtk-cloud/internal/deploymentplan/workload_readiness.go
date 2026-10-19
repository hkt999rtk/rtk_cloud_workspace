package deploymentplan

import "context"

// Workload readiness can depend on a peer whose native owner is later in the
// same contiguous phase block. Successful native effects keep their ordinary
// prefix/owner journal; readiness is mandatory before crossing that block.
func deferredWorkloadReadiness(r Resource) bool {
	if r.Phase != "workload" || r.Action == "retire" {
		return false
	}
	switch String(r.Desired["kind"]) {
	case "Deployment", "StatefulSet", "DaemonSet", "Pod":
		return true
	}
	return false
}

func (p *Plan) workloadReadinessBlock(index int) (int, int) {
	start, end := index, index+1
	for start > 0 && p.Resources[start-1].Phase == p.Resources[index].Phase {
		start--
	}
	for end < len(p.Resources) && p.Resources[end].Phase == p.Resources[index].Phase {
		end++
	}
	return start, end
}

func workloadReadinessOwnerMatches(r Resource, owner string, current Object) error {
	if err := recordedResourceMatches(r, owner, current); err != nil {
		return err
	}
	if r.Action == "retain" {
		_, err := r.Body(current)
		return err
	}
	return nil
}

func waitRecordedWorkloadReadiness(ctx context.Context, backend Backend, r Resource, owner string, current Object, check func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	if err := workloadReadinessOwnerMatches(r, owner, current); err != nil {
		return err
	}
	if err := backend.Wait(ctx, r); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	after, err := backend.Read(ctx, r.ID)
	if err != nil {
		return preserveReadBlocker(err, blocked("deployment.access", r.ID, "recorded workload owner cannot be read after readiness", "restore read access and retain the execution journal"))
	}
	return workloadReadinessOwnerMatches(r, owner, after)
}

func (p *Plan) waitWorkloadReadinessBlock(ctx context.Context, backend Backend, j Journal, index int, check func(context.Context) error, progress func(int, Resource) error) (string, error) {
	start, end := p.workloadReadinessBlock(index)
	if len(j.Completed) < end {
		return p.Resources[index].ID, blocked("deployment.execution", p.Resources[index].ID, "workload phase native effects are incomplete", "resume the exact recorded phase before checking readiness")
	}
	for offset, r := range p.Resources[start:end] {
		if !deferredWorkloadReadiness(r) {
			continue
		}
		if progress != nil {
			if err := progress(start+offset, r); err != nil {
				return r.ID, err
			}
		}
		current, err := backend.Read(ctx, r.ID)
		if err != nil {
			return r.ID, preserveReadBlocker(err, blocked("deployment.access", r.ID, "recorded workload owner cannot be read before readiness", "restore read access and retain the execution journal"))
		}
		if err := waitRecordedWorkloadReadiness(ctx, backend, r, j.Owners[r.ID], current, check); err != nil {
			return r.ID, err
		}
	}
	return "", nil
}

// An explicit workload dependency retains its ready-before-dependent contract.
// Only undeclared peer startup coupling is handled by the phase barrier.
func (p *Plan) waitWorkloadReadinessDependencies(ctx context.Context, backend Backend, j Journal, r Resource, check func(context.Context) error) error {
	for _, id := range r.Dependencies {
		for _, dependency := range p.Resources {
			if dependency.ID != id || !deferredWorkloadReadiness(dependency) {
				continue
			}
			current, err := backend.Read(ctx, id)
			if err != nil {
				return preserveReadBlocker(err, blocked("deployment.access", id, "explicit workload dependency cannot be read", "restore read access and retain the execution journal"))
			}
			if err := waitRecordedWorkloadReadiness(ctx, backend, dependency, j.Owners[id], current, check); err != nil {
				return err
			}
		}
	}
	return nil
}
