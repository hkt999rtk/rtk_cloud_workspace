package recovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func NewID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return time.Now().UTC().Format("20060102t150405z") + "-" + hex.EncodeToString(b[:])
}

// Snapshot only runs with a persisted maintenance lock and paused writers.
func (e *Engine) Snapshot(ctx context.Context, j Journal, id string) (string, error) {
	if err := e.AssertPaused(ctx, j); err != nil {
		return "", err
	}
	if err := PrivateDirectory(e.Config.Directory); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(e.Config.Directory, ".capture-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err = e.Capture(ctx, dir); err != nil {
		return "", err
	}
	// Check again after capture, detecting writers that restarted during it.
	if err = e.AssertPaused(ctx, j); err != nil {
		return "", err
	}
	m := Manifest{Version: Version, Scope: "core", ID: id, Environment: e.Config.Environment, Stack: e.Config.Stack, CreatedAt: time.Now().UTC(), ConfigurationSHA256: InventoryFingerprint(e.Config), Components: e.Config.Components, ExternalDependencies: e.Config.ExternalDependencies, Evidence: map[string]string{"consistency": "maintenance-window", "operation_id": j.ID, "verification": "archive validation only; live restore drill required"}}
	file := filepath.Join(e.Config.Directory, id+".age")
	if err = Pack(dir, file, m, e.Config.Recipients); err != nil {
		return "", err
	}
	i, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	if i.Size() > e.Config.MaxArchiveBytes {
		os.Remove(file)
		return "", errors.New("encrypted backup exceeds configured size limit")
	}
	return file, nil
}

func (e *Engine) Create(ctx context.Context) (string, error) {
	id := NewID()
	j, err := e.Begin(ctx, "backup", id)
	if err != nil {
		return id, err
	}
	file, err := e.Snapshot(ctx, j, id)
	if err != nil {
		return id, err
	}
	j.BackupID = id
	j.Phase = "captured"
	if err = e.save(ctx, &j, false); err != nil {
		return id, err
	}
	if err = e.publish(ctx, id, file); err != nil {
		return id, err
	}
	// Even a successful backup does not implicitly reopen external traffic.
	return id, nil
}

func (e *Engine) Inspect(ctx context.Context, file, identity string) (Manifest, error) {
	var m Manifest
	if err := PrivateDirectory(e.Config.Directory); err != nil {
		return m, err
	}
	dir, err := os.MkdirTemp(e.Config.Directory, ".inspect-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(dir)
	m, err = Unpack(file, identity, dir, e.Config.MaxArchiveBytes)
	if err != nil {
		return m, err
	}
	if err = e.matchManifest(m); err != nil {
		return m, err
	}
	// Inspection remains local/read-only: PostgreSQL's restore-list check belongs
	// to apply preflight, not an offline manifest inspection.
	return m, nil
}

func (e *Engine) matchManifest(m Manifest) error {
	if m.Environment != e.Config.Environment || m.Stack != e.Config.Stack || m.ConfigurationSHA256 != InventoryFingerprint(e.Config) {
		return errors.New("backup logical environment, stack or dataset inventory differs from target")
	}
	if InventoryFingerprint(Config{Environment: m.Environment, Stack: m.Stack, Components: m.Components}) != m.ConfigurationSHA256 {
		return errors.New("manifest inventory fingerprint is inconsistent")
	}
	return nil
}

func (e *Engine) Restore(ctx context.Context, file, identity string) (string, error) {
	if err := PrivateDirectory(e.Config.Directory); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(e.Config.Directory, ".restore-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	m, err := Unpack(file, identity, dir, e.Config.MaxArchiveBytes)
	if err != nil {
		return "", err
	}
	if err = e.matchManifest(m); err != nil {
		return "", err
	}
	if err = e.ValidateArtifacts(ctx, dir); err != nil {
		return "", err
	}
	id := NewID()
	j, err := e.Begin(ctx, "restore", id)
	if err != nil {
		return id, err
	}
	j.BackupID = m.ID
	j.SafetyBackupID = id + "-before"
	safety, err := e.Snapshot(ctx, j, j.SafetyBackupID)
	if err != nil {
		return id, err
	}
	if err = e.publish(ctx, j.SafetyBackupID, safety); err != nil {
		return id, err
	}
	j.Phase = "restoring"
	if err = e.save(ctx, &j, false); err != nil {
		return id, err
	}
	if err = e.AssertPaused(ctx, j); err != nil {
		return id, err
	}
	if err = e.Apply(ctx, dir); err != nil {
		return id, err
	}
	j.Phase = "restored"
	return id, e.save(ctx, &j, false)
}

// Reapply explicitly retries the original backup or rolls back to the captured
// safety backup. The existing maintenance journal is never bypassed/deleted.
func (e *Engine) Reapply(ctx context.Context, file, identity string) error {
	j, err := e.Load(ctx)
	if err != nil {
		return err
	}
	if j.Operation != "restore" || j.SafetyBackupID == "" || (j.Phase != "restoring" && j.Phase != "restored" && j.Phase != "verifying" && j.Phase != "verified") {
		return errors.New("no restorable failed operation")
	}
	dir, err := os.MkdirTemp(e.Config.Directory, ".retry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	m, err := Unpack(file, identity, dir, e.Config.MaxArchiveBytes)
	if err != nil {
		return err
	}
	if m.ID != j.BackupID && m.ID != j.SafetyBackupID {
		return errors.New("retry accepts only this operation's original or safety backup")
	}
	if err = e.matchManifest(m); err != nil {
		return err
	}
	if err = e.ValidateArtifacts(ctx, dir); err != nil {
		return err
	}
	j.Phase = "restoring"
	j.VerifiedAt = nil
	j.BackupID = m.ID
	if err = e.save(ctx, &j, false); err != nil {
		return err
	}
	for _, role := range []string{"application", "offline"} {
		for _, w := range j.Workloads {
			if w.Role == role {
				if err = e.scale(ctx, w, 0); err != nil {
					return err
				}
				if err = e.waitStopped(ctx, w); err != nil {
					return err
				}
			}
		}
		if role == "application" {
			if err = e.checks(ctx, e.Config.QuiescenceChecks); err != nil {
				return err
			}
		}
	}
	if err = e.AssertPaused(ctx, j); err != nil {
		return err
	}
	if err = e.Apply(ctx, dir); err != nil {
		return err
	}
	j.Phase = "restored"
	return e.save(ctx, &j, false)
}

func (e *Engine) startRole(ctx context.Context, j Journal, role string) error {
	for _, w := range j.Workloads {
		if w.Role == role {
			if err := e.scale(ctx, w, w.Replicas); err != nil {
				return err
			}
		}
	}
	return nil
}

// Verify starts private services behind the operator's still-closed external
// traffic fence. Recovery checks unseal/reconcile before application startup;
// health checks must not replay charges or emit production email.
func (e *Engine) Verify(ctx context.Context) error {
	j, err := e.Load(ctx)
	if err != nil {
		return err
	}
	switch j.Phase {
	case "captured", "restored", "verifying", "verified":
	default:
		return errors.New("operation is incomplete; verify is not allowed")
	}
	j.Phase = "verifying"
	j.VerifiedAt = nil
	if err = e.save(ctx, &j, false); err != nil {
		return err
	}
	if err = e.checks(ctx, e.Config.StartupChecks); err != nil {
		return err
	}
	if err = e.startRole(ctx, j, "offline"); err != nil {
		return err
	}
	if err = e.checks(ctx, e.Config.RecoveryChecks); err != nil {
		return err
	}
	if err = e.startRole(ctx, j, "application"); err != nil {
		return err
	}
	if err = e.checks(ctx, e.Config.HealthChecks); err != nil {
		// Best-effort re-pause applications; the persistent lock and external fence
		// remain even if the cluster becomes unavailable during this cleanup.
		for _, w := range j.Workloads {
			if w.Role == "application" {
				_ = e.scale(context.WithoutCancel(ctx), w, 0)
			}
		}
		return err
	}
	now := time.Now().UTC()
	j.VerifiedAt = &now
	j.Phase = "verified"
	return e.save(ctx, &j, false)
}

type Reconciliation struct {
	Environment      string `json:"environment"`
	Stack            string `json:"stack"`
	OperationID      string `json:"operation_id"`
	BackupID         string `json:"backup_id"`
	ApprovedBy       string `json:"approved_by"`
	PaymentAndEmail  bool   `json:"payment_and_email_reconciled"`
	PKIAndRevocation bool   `json:"pki_and_revocation_reconciled"`
	ObjectsAndAudit  bool   `json:"objects_and_audit_checked"`
}

func (e *Engine) Resume(ctx context.Context, evidence string) error {
	j, err := e.Load(ctx)
	if err != nil {
		return err
	}
	if j.Phase != "verified" || j.VerifiedAt == nil {
		return errors.New("verify must pass before resume")
	}
	if time.Since(*j.VerifiedAt) > 30*time.Minute {
		return errors.New("verification expired; verify again before resume")
	}
	if j.Operation == "restore" {
		f, err := os.Open(evidence)
		if err != nil {
			return errors.New("restore requires reconciliation JSON evidence")
		}
		var r Reconciliation
		err = Decode(io.LimitReader(f, 65536), &r)
		f.Close()
		if err != nil {
			return err
		}
		if r.Environment != j.Environment || r.Stack != j.Stack || r.OperationID != j.ID || r.BackupID != j.BackupID || r.ApprovedBy == "" || !r.PaymentAndEmail || !r.PKIAndRevocation || !r.ObjectsAndAudit {
			return errors.New("incomplete or mismatched reconciliation evidence")
		}
		if err = WriteJSON(filepath.Join(e.Config.Directory, j.ID+".reconciliation.json"), r); err != nil {
			return err
		}
	}
	if err = e.checks(ctx, e.Config.HealthChecks); err != nil {
		return err
	}
	// Save evidence before releasing locks. Never delete workload/PVC data here.
	j.Phase = "complete"
	if err = WriteJSON(filepath.Join(e.Config.Directory, j.ID+".operation.json"), j); err != nil {
		return err
	}
	if err = e.kube(ctx, nil, io.Discard, "-n", e.Config.LockNamespace, "delete", "configmap", lockName, "--wait=true"); err != nil {
		return err
	}
	if err = os.Remove(e.journalPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("Maintenance lock released. Re-enable external traffic/controllers only after operator sign-off.")
	return nil
}

// Abort is limited to a source or target whose data has not been overwritten.
// Once restore enters 'restoring', a failed operation requires recovery using
// its safety backup; it can never be made healthy by simply clearing a lock.
func (e *Engine) Abort(ctx context.Context) error {
	j, err := e.Load(ctx)
	if err != nil {
		return err
	}
	if j.Phase != "quiescing" && j.Phase != "paused" && j.Phase != "captured" {
		return errors.New("cannot abort after restore mutations or verification; repair under maintenance")
	}
	j.Operation = "backup"
	j.Phase = "captured"
	if err = e.save(ctx, &j, false); err != nil {
		return err
	}
	return e.Verify(ctx)
}
