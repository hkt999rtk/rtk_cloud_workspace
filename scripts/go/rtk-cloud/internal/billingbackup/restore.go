package billingbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type StagedRecovery struct {
	Directory      string               `json:"directory"`
	Snapshot       string               `json:"snapshot"`
	Source         Verified             `json:"source"`
	State          logger.RecoveryState `json:"state"`
	PreparedSHA256 string               `json:"prepared_sha256"`
}

// StageRestore prepares a new private recovery copy, not a live restore. It
// requires a previously independently verified immutable set, creates a durable
// admission fence, and never chooses/resets a PostgreSQL recovery point or cursor.
func (e Engine) StageRestore(ctx context.Context, prefix, destination string) (StagedRecovery, error) {
	var out StagedRecovery
	if err := e.Config.Validate(); err != nil {
		return out, err
	}
	if e.Objects == nil || len(e.Identities) == 0 {
		return out, errors.New("isolated recovery reader dependencies required")
	}
	if !filepath.IsAbs(destination) {
		return out, errors.New("explicit absolute private recovery staging root required")
	}
	if err := privateDirectory(destination); err != nil {
		return out, err
	}
	m, raw, err := e.readManifest(ctx, prefix)
	if err != nil {
		return out, err
	}
	r, err := e.Objects.Read(ctx, prefix+"/complete.json")
	if err != nil {
		return out, err
	}
	var completion billingarchive.SignedCompletion
	err = billingarchive.StrictDecode(r, 32768, &completion)
	r.Close()
	if err != nil {
		return out, err
	}
	keys, _ := e.Config.PublicKeys()
	if _, err = verifyCompletionNow(completion, m, raw, keys); err != nil {
		return out, err
	}
	if err = checkScratch(destination, m, false); err != nil {
		return out, err
	}
	work, err := os.MkdirTemp(destination, "billing-restore-")
	if err != nil {
		return out, err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(work)
		}
	}()
	out = StagedRecovery{Directory: work, Snapshot: filepath.Join(work, "inbox.db"), Source: Verified{m, completion, prefix}}
	if err = atomicJSON(filepath.Join(work, "source.json"), out.Source); err != nil {
		return out, err
	}
	snapshot, err := os.OpenFile(out.Snapshot, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer snapshot.Close()
	events, err := os.OpenFile(filepath.Join(work, "events.ndjson"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer events.Close()
	for _, part := range m.Parts {
		r, err := e.Objects.Read(ctx, Prefix(m, part.Kind)+"/"+part.Name)
		if err != nil {
			return out, err
		}
		w := snapshot
		if part.Kind == "events" {
			w = events
		}
		err = billingarchive.DecodePart(ctx, w, r, e.Identities, part)
		r.Close()
		if err != nil {
			return out, err
		}
	}
	if err = snapshot.Sync(); err != nil {
		return out, err
	}
	if err = events.Sync(); err != nil {
		return out, err
	}
	snapshot.Close()
	if err = logger.ValidateBillingSnapshot(ctx, out.Snapshot, m, keys); err != nil {
		return out, err
	}
	if _, err = events.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	if err = logger.ValidateBillingSnapshotExports(ctx, out.Snapshot, m, events); err != nil {
		return out, err
	}
	if out.State, err = logger.PrepareBillingSnapshotRecovery(ctx, out.Snapshot, m, keys); err != nil {
		return out, err
	}
	f, err := os.Open(out.Snapshot)
	if err != nil {
		return out, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return out, err
	}
	out.PreparedSHA256 = hex.EncodeToString(h.Sum(nil))
	if err = atomicJSON(filepath.Join(work, "recovery-stage.json"), out); err != nil {
		return out, err
	}
	success = true
	return out, nil
}
