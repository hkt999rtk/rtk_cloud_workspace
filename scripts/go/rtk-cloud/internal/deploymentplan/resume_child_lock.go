package deploymentplan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
)

// The original parent fence and a newly acquired native lease together prove
// that its old child cannot still be running. Archive only an empty interrupted
// lock after every write has a known owner and the private journal is unchanged.
func recoverFullyWrittenChildLock(ctx context.Context, p *Plan, parent ParentExecutionBackend, lease ExecutionLease, root, lock string, prior *retainedExecutionEvidence) error {
	bad := errors.New("interrupted child lock lacks exact fully written parent evidence")
	if p == nil || parent == nil || lease == nil || prior == nil || ctx.Err() != nil || prior.journal.InFlight != "" || prior.journal.Failed != "" || len(prior.journal.Completed) != len(p.Resources) || len(prior.journal.Owners) != len(p.Resources) || parent.CheckParentExecution(ctx, p, root) != nil || lease.Check(ctx) != nil {
		return bad
	}
	info, err := os.Lstat(lock)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return bad
	}
	entries, err := os.ReadDir(lock)
	if err != nil || len(entries) != 0 {
		return bad
	}
	fresh, err := p.resumableEvidence(root)
	if err != nil || fresh == nil || !bytes.Equal(prior.planRaw, fresh.planRaw) || !bytes.Equal(prior.journalRaw, fresh.journalRaw) || parent.CheckParentExecution(ctx, p, root) != nil || lease.Check(ctx) != nil {
		return bad
	}
	archive := filepath.Join(filepath.Dir(lock), ".active-recovered-"+Digest(lease.Owner())[:16])
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		return bad
	}
	return os.Rename(lock, archive)
}
