package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This capability lives only on one execution object and its bounded historical
// copies. It is never a deployment artifact. Only successful pure constructors
// populate it; live observations and current child journals are not memoized.
type managedLateSourceReuse struct {
	mu       sync.Mutex
	store    secretStore
	source   environmentCreateJournal
	ownerSHA string
	manifest map[string]string
	values   map[string]json.RawMessage
	outputs  map[string]bool
	invalid  bool
	// Set only after strict admitted runtime progress has validated this exact
	// current snapshot. Historical source views remain the original374/370.
	currentSHA, admissionReview, descriptorSHA string
	boundedSHA                                 string
}

const managedLateSourceReuseFileLimit = 32768
const managedLateSourceReuseBytesLimit int64 = 512 << 20

func managedLateSourceReuseBlock() error {
	return environmentCreateBootstrapBlocker("command-local late historical source changed; reauthenticate unchanged canonical ancestry before qualification or mutation")
}

// These are the maintained producers consumed by the old source constructors.
// Reports, scratch, live Kubernetes state, and the current standalone Device
// repair are deliberately outside this historical manifest. The latter gets its
// own fresh source/evidence validation at every Device observation boundary.
func managedLateSourceReuseRoots(parent string) []string {
	roots := []string{"deployment/rollback", "deployment/resources", "deployment/repairs", "pki", "openbao/init.json", "openbao/root-token", "openbao/unseal-key", "deployment/.execution-active/owner.json"}
	for _, producer := range []string{parent, "material/" + parent, "executions/" + parent, "preflight", "app-root-crl-recovery", "certissuer-provider-prepare", "mqtt-root-crl-recovery", "openbao-host-ack", "openbao-recovery", "openbao-tls-root-crl-recovery", "recovery", "root-crl-recovery", "transport-renewal"} {
		roots = append(roots, "deployment/create/"+producer)
	}
	return roots
}

func managedLateSourceReuseExcluded(relative string, outputs map[string]bool) bool {
	for output := range outputs {
		if relative == output || strings.HasPrefix(relative, output+"/") {
			return true
		}
	}
	return false
}

func newManagedLateSourceReuse(store secretStore, e *environmentCreateExecution) (*managedLateSourceReuse, error) {
	if e == nil || store.Environment != "dev" || e.Store.Root != store.Root || store.Root != filepath.Join(store.ConfigRoot, store.Environment) || !filepath.IsAbs(store.Root) || !environmentPreflightDigest.MatchString(e.Journal.PlanSHA256) || len(e.Journal.Entries) != 374 || managedLatePostMintReview(e.Journal) == "" || !e.Journal.CompletedAt.IsZero() || e.owner.PlanSHA256 != e.Journal.PlanSHA256 || e.owner.OwnerToken != e.Journal.OwnerToken {
		return nil, managedLateSourceReuseBlock()
	}
	for _, root := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(root) != nil {
			return nil, managedLateSourceReuseBlock()
		}
	}
	manifest, err := readManagedLateSourceReuseManifest(store, e.Journal.PlanSHA256, nil)
	if err != nil {
		return nil, err
	}
	// Copy the selected source too: later changes to a caller's slices cannot
	// change the authority against which a historical view is compared.
	raw, err := json.Marshal(e.Journal)
	var source environmentCreateJournal
	if err != nil || json.Unmarshal(raw, &source) != nil {
		return nil, managedLateSourceReuseBlock()
	}
	return &managedLateSourceReuse{store: store, source: source, ownerSHA: deploymentplan.Digest(e.owner), manifest: manifest, values: map[string]json.RawMessage{}, outputs: map[string]bool{}}, nil
}

func readManagedLateSourceReuseManifest(store secretStore, parent string, outputs map[string]bool) (map[string]string, error) {
	for _, directory := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(directory) != nil {
			return nil, managedLateSourceReuseBlock()
		}
	}
	manifest := map[string]string{}
	var bytes int64
	for _, relative := range managedLateSourceReuseRoots(parent) {
		base := filepath.Join(store.Root, filepath.FromSlash(relative))
		// A final-component O_NOFOLLOW does not protect intermediate paths.
		// Pin every existing private ancestor, including the parents of the
		// standalone OpenBao inputs and the original owner file.
		for directory := filepath.Dir(base); directory != store.Root; directory = filepath.Dir(directory) {
			rel, err := filepath.Rel(store.Root, directory)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, managedLateSourceReuseBlock()
			}
			rel = filepath.ToSlash(rel)
			info, err := os.Lstat(directory)
			if errors.Is(err, os.ErrNotExist) {
				manifest[rel] = "absent"
				continue
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
				return nil, managedLateSourceReuseBlock()
			}
			manifest[rel] = "directory:0700"
		}
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			rel, err := filepath.Rel(store.Root, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return managedLateSourceReuseBlock()
			}
			rel = filepath.ToSlash(rel)
			if managedLateSourceReuseExcluded(rel, outputs) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if walkErr != nil {
				if path == base && errors.Is(walkErr, os.ErrNotExist) {
					manifest[rel] = "absent"
					return nil
				}
				return managedLateSourceReuseBlock()
			}
			if len(manifest) >= managedLateSourceReuseFileLimit {
				return managedLateSourceReuseBlock()
			}
			info, err := entry.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return managedLateSourceReuseBlock()
			}
			if info.IsDir() {
				if info.Mode().Perm() != 0700 {
					return managedLateSourceReuseBlock()
				}
				manifest[rel] = "directory:0700"
				return nil
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 0 || info.Size() > environmentPreflightArtifactLimit {
				return managedLateSourceReuseBlock()
			}
			bytes += info.Size()
			if bytes > managedLateSourceReuseBytesLimit {
				return managedLateSourceReuseBlock()
			}
			sha, err := hashManagedLateSourceReuseFile(path, info)
			if err != nil {
				return managedLateSourceReuseBlock()
			}
			manifest[rel] = sha
			return nil
		})
		if err != nil {
			return nil, managedLateSourceReuseBlock()
		}
	}
	return manifest, nil
}

// Historical rollback plans use the maintained 32 MiB execution bound. Hash
// those private bytes as a stream without weakening their typed parser's limit
// or allocating a second full copy of every archived plan.
func hashManagedLateSourceReuseFile(path string, expected os.FileInfo) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", managedLateSourceReuseBlock()
	}
	file := os.NewFile(uintptr(fd), "late-historical-source")
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(expected, before) || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() != expected.Size() || before.Size() < 0 || before.Size() > environmentPreflightArtifactLimit {
		return "", managedLateSourceReuseBlock()
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, environmentPreflightArtifactLimit+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || n != before.Size() || n > environmentPreflightArtifactLimit || !os.SameFile(before, after) || after.Mode() != before.Mode() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", managedLateSourceReuseBlock()
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func managedLateSourceReuseActive(e *environmentCreateExecution) bool {
	if e == nil || e.lateSourceReuse == nil {
		return false
	}
	e.lateSourceReuse.mu.Lock()
	defer e.lateSourceReuse.mu.Unlock()
	return !e.lateSourceReuse.invalid
}

func invalidateManagedLateSourceReuse(e *environmentCreateExecution) {
	if e == nil || e.lateSourceReuse == nil {
		return
	}
	r := e.lateSourceReuse
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalid = true
	r.values = nil
}

func (r *managedLateSourceReuse) matches(store secretStore, e *environmentCreateExecution) bool {
	if e == nil || store.Root != r.store.Root || store.ConfigRoot != r.store.ConfigRoot || store.Environment != r.store.Environment || deploymentplan.Digest(e.owner) != r.ownerSHA {
		return false
	}
	if r.currentSHA != "" && deploymentplan.Digest(e.Journal) == r.currentSHA || r.boundedSHA != "" && deploymentplan.Digest(e.Journal) == r.boundedSHA {
		return true
	}
	if len(e.Journal.Entries) != 370 && len(e.Journal.Entries) != 374 || len(e.Journal.KubernetesLeases) > len(r.source.KubernetesLeases) {
		return false
	}
	expected := r.source
	expected.Entries = expected.Entries[:len(e.Journal.Entries)]
	expected.KubernetesLeases = expected.KubernetesLeases[:len(e.Journal.KubernetesLeases)]
	return deploymentplan.Digest(expected) == deploymentplan.Digest(e.Journal)
}

func (r *managedLateSourceReuse) check(store secretStore, e *environmentCreateExecution) error {
	if r.invalid || !r.matches(store, e) {
		return managedLateSourceReuseBlock()
	}
	manifest, err := readManagedLateSourceReuseManifest(store, r.source.PlanSHA256, r.outputs)
	if err != nil || !sameManagedLateSourceReuseManifest(r.manifest, manifest) {
		r.invalid = true
		r.values = nil
		return managedLateSourceReuseBlock()
	}
	return nil
}

// Qualification archives its own prospective previews before the six gates.
// Only a not-yet-existing, unreferenced review directory may be registered;
// recorded history and any input already in the manifest cannot be hidden.
func allowManagedLateSourceReuseOutput(e *environmentCreateExecution, relative string) error {
	if e == nil || e.lateSourceReuse == nil {
		return nil
	}
	r := e.lateSourceReuse
	r.mu.Lock()
	defer r.mu.Unlock()
	const base = "deployment/create/openbao-recovery/"
	review := strings.TrimPrefix(relative, base)
	if r.invalid || relative != base+review || !environmentPreflightDigest.MatchString(review) || filepath.ToSlash(filepath.Clean(relative)) != relative {
		return managedLateSourceReuseBlock()
	}
	if r.outputs[relative] {
		return nil
	}
	for _, entry := range r.source.Entries {
		if strings.Contains(entry.Key, review) {
			return managedLateSourceReuseBlock()
		}
	}
	for input := range r.manifest {
		if input == relative || strings.HasPrefix(input, relative+"/") {
			return managedLateSourceReuseBlock()
		}
	}
	if _, err := os.Lstat(filepath.Join(r.store.Root, relative)); !errors.Is(err, os.ErrNotExist) || r.manifest[strings.TrimSuffix(base, "/")] != "directory:0700" {
		return managedLateSourceReuseBlock()
	}
	if err := r.check(e.Store, e); err != nil {
		return err
	}
	r.outputs[relative] = true
	return nil
}

func sameManagedLateSourceReuseManifest(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, sha := range a {
		if b[path] != sha {
			return false
		}
	}
	return true
}

// Call this immediately before GO publication and before any native/local
// mutation, then invalidate before crossing that boundary. Absent capability
// means ordinary fresh derivation; an invalid/changed one is never authority.
func checkManagedLateSourceReuse(e *environmentCreateExecution) error {
	if e == nil || e.lateSourceReuse == nil {
		return nil
	}
	r := e.lateSourceReuse
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.check(e.Store, e)
}

// Only the immutable historical constructors share results inside this phase.
// Current progress, child journals, leases and native observations are still
// independently read by their existing validators on every call.
func beginManagedLateAdmittedSourcePhase(ctx context.Context, store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) error {
	if late == nil || late.ExpiredRenewal == nil || e == nil || len(e.Journal.Entries) <= 374 {
		return nil
	}
	for _, entry := range e.Journal.Entries[375:] {
		if entry.Key == "openbao-host-ack-verified-"+late.OriginalRepairReview {
			// Finalization has its own authenticated bounded prefix. Keep its
			// original fresh readers rather than extending this runtime cache.
			invalidateManagedLateSourceReuse(e)
			return nil
		}
	}
	if err := checkManagedLatePureSourceExecution(ctx, e); err != nil {
		return err
	}
	if managedLateSourceReuseActive(e) {
		r := e.lateSourceReuse
		r.mu.Lock()
		same := r.admissionReview == late.Review && r.descriptorSHA == managedLateSessionSourceDigest(late) && (r.currentSHA == deploymentplan.Digest(e.Journal) || r.boundedSHA != "" && r.boundedSHA == deploymentplan.Digest(e.Journal))
		r.mu.Unlock()
		if !same {
			return managedLateSourceReuseBlock()
		}
		return checkManagedLateSourceReuse(e)
	}
	currentSHA := deploymentplan.Digest(e.Journal)
	fresh, err := captureManagedLateAdmittedSourceReuse(ctx, store, e, late)
	if err != nil {
		return err
	}
	// During validation the provisional scope matches only the authenticated
	// original374/370 views. It cannot represent the actual admitted snapshot.
	e.lateSourceReuse = fresh
	accepted := false
	defer func() {
		if !accepted {
			invalidateManagedLateSourceReuse(e)
		}
	}()
	source, artifact, err := managedLateSessionRuntimeProgress(store, e, late)
	if err != nil {
		return err
	}
	if source == nil || artifact == nil || deploymentplan.Digest(source.Journal) != late.SourceSHA || deploymentplan.Digest(e.Journal) != currentSHA || artifact.Plan.Schema != managedLateExpiredRenewalSchema {
		return managedLateSourceReuseBlock()
	}
	if err := checkManagedLatePureSourceExecution(ctx, e); err != nil {
		return err
	}
	fresh.mu.Lock()
	fresh.currentSHA = currentSHA
	fresh.admissionReview = late.Review
	fresh.descriptorSHA = managedLateSessionSourceDigest(late)
	fresh.mu.Unlock()
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	accepted = true
	return nil
}

// Capture alone grants no admitted-current authority. The caller must run the
// full existing runtime-progress validator before setting its currentSHA.
func captureManagedLateAdmittedSourceReuse(ctx context.Context, store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*managedLateSourceReuse, error) {
	if e == nil || late == nil || late.Mode != managedLateSessionACKReady || late.ExpiredRenewal == nil || late.ExpiredRenewal.Schema != managedLateExpiredRenewalSchema || late.ExpiredRenewal.PriorLate == nil || late.ExpiredRenewal.PriorLate.OpenBaoTLSRepair == nil || store.Environment != "dev" || e.Store.Root != store.Root || e.Store.ConfigRoot != store.ConfigRoot || e.Store.Environment != store.Environment || e.owner.Schema != environmentCreateOwnerSchema || e.Journal.Schema != environmentCreateJournalSchema || !e.Journal.CompletedAt.IsZero() || len(e.Journal.Entries) <= 374 || late.ParentSHA != e.Journal.PlanSHA256 || late.SourceOwnerSHA != deploymentplan.Digest(e.owner) || !environmentPreflightDigest.MatchString(late.Review) {
		return nil, managedLateSourceReuseBlock()
	}
	if err := checkManagedLatePureSourceExecution(ctx, e); err != nil {
		return nil, err
	}
	artifact, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil || artifact.Plan.Schema != managedLateExpiredRenewalSchema || len(source.Entries) != 374 || managedLatePostMintReview(source) == "" || deploymentplan.Digest(source) != late.SourceSHA || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) || !managedLateAdmissionMatches(artifact, source, e.Journal.Entries[374], late) {
		return nil, managedLateSourceReuseBlock()
	}
	var archived createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-session-source.json"), &archived) != nil || archived.Review != late.Review || managedLateSessionSourceDigest(&archived) != managedLateSessionSourceDigest(late) {
		return nil, managedLateSourceReuseBlock()
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:374]
	prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != late.SourceSHA {
		return nil, managedLateSourceReuseBlock()
	}
	if len(e.Journal.Entries) > 375 {
		steps, stepErr := managedLateProgressSteps(late)
		if stepErr != nil {
			return nil, stepErr
		}
		step := 0
		for _, entry := range e.Journal.Entries[375:] {
			if strings.HasPrefix(entry.Key, "openbao-managed-unseal-") && step > 0 && steps[step-1].Kind == "owners" {
				continue // Full runtime progress authenticates the sole real proof.
			}
			if step >= len(steps) || entry.Key != steps[step].Key || entry.Phase != steps[step].Phase {
				return nil, managedLateSourceReuseBlock()
			}
			step++
		}
	}
	pure := *e
	pure.Journal = source
	return newManagedLateSourceReuse(store, &pure)
}

// Execution publishes these admission snapshots only after all immediate
// checks pass. An existing review directory is an input of the execute phase,
// so adding its snapshots must end that phase before either local write.
func writeCreateRecoveryAdmissionSource(ctx context.Context, store secretStore, e *environmentCreateExecution, review string) error {
	if e == nil || e.Store.Root != store.Root || e.Store.ConfigRoot != store.ConfigRoot || e.Store.Environment != store.Environment || !environmentPreflightDigest.MatchString(review) {
		return managedLateSourceReuseBlock()
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	reuse := managedLateSourceReuseActive(e)
	invalidateManagedLateSourceReuse(e)
	for _, source := range []struct{ name, path string }{{"source-owner.json", filepath.Join(store.Root, "deployment/.execution-active/owner.json")}, {"source-journal.json", e.JournalPath}} {
		raw, err := readBoundedEnvironmentFile(source.path, true)
		if err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", review, source.name), raw); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	if reuse {
		// This captures the new immutable file set, with no retained result.
		// The final native/authentication boundary must derive history afresh.
		fresh, err := newManagedLateSourceReuse(store, e)
		if err != nil {
			return err
		}
		e.lateSourceReuse = fresh
	}
	return nil
}

func managedLateSourceReuseKey(cfg deploymentConfig, e *environmentCreateExecution, kind string) string {
	return deploymentplan.Digest([]string{deploymentplan.Digest(cfg), deploymentplan.Digest(e.Journal), kind})
}

func readManagedLateSourceReuse(store secretStore, e *environmentCreateExecution, cfg deploymentConfig, kind string, target any) (bool, error) {
	if e == nil || e.lateSourceReuse == nil {
		return false, nil
	}
	r := e.lateSourceReuse
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invalid {
		return false, nil
	}
	if err := r.check(store, e); err != nil {
		return false, err
	}
	raw, ok := r.values[managedLateSourceReuseKey(cfg, e, kind)]
	if !ok {
		return false, nil
	}
	destination := reflect.ValueOf(target)
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return false, managedLateSourceReuseBlock()
	}
	fresh := reflect.New(destination.Elem().Type())
	if json.Unmarshal(raw, fresh.Interface()) != nil {
		return false, managedLateSourceReuseBlock()
	}
	destination.Elem().Set(fresh.Elem())
	return true, nil
}

func rememberManagedLateSourceReuse(store secretStore, e *environmentCreateExecution, cfg deploymentConfig, kind string, value any) error {
	if e == nil || e.lateSourceReuse == nil {
		return nil
	}
	r := e.lateSourceReuse
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invalid {
		return nil
	}
	if err := r.check(store, e); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return managedLateSourceReuseBlock()
	}
	key := managedLateSourceReuseKey(cfg, e, kind)
	if old, ok := r.values[key]; ok && !slices.Equal(old, raw) {
		return managedLateSourceReuseBlock()
	}
	r.values[key] = raw
	return nil
}
