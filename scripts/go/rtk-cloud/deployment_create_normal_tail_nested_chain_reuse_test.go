package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type nestedRuntimeReadFixture struct {
	store     secretStore
	e         *environmentCreateExecution
	original  *deploymentplan.Plan
	journal   deploymentplan.Journal
	admission createNormalTailProgramAdmission
	outer     int
}

// Reuse the retained synthetic output of the genuine two-renewal ordinary
// producer. Copy it into this test's private root; never read canonical dev.
func loadNestedRuntimeReadFixture(t *testing.T) *nestedRuntimeReadFixture {
	t.Helper()
	path := os.Getenv("RTK_NESTED_RUNTIME_READER_EXPORT")
	if path == "" {
		t.Skip("explicit retained synthetic two-renewal export required")
	}
	fixtureRoot := filepath.Dir(path)
	if !productHistoricalCRLFixturePath(fixtureRoot, path) {
		t.Fatal("isolated synthetic export required")
	}
	var input struct {
		Root, Directory, Owner string
		Journal                environmentCreateJournal
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &input) != nil {
		t.Fatal("synthetic export encoding", err)
	}
	rel, err := filepath.Rel(input.Root, input.Directory)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) || !productHistoricalCRLFixturePath(fixtureRoot, input.Root) {
		t.Fatal("synthetic execution root", err)
	}
	base := t.TempDir()
	if err = os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "dev")
	err = filepath.WalkDir(input.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(input.Root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if !entry.Type().IsRegular() {
			t.Fatal("synthetic export must contain regular private files")
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, value, 0600)
	})
	if err != nil {
		t.Fatal("copy synthetic history", err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	store, err := newSecretStore(base, "dev")
	if err != nil {
		t.Fatal(err)
	}
	e := &environmentCreateExecution{Store: store, Directory: filepath.Join(root, rel), Journal: input.Journal}
	e.JournalPath = filepath.Join(e.Directory, "journal.json")
	ownerPath := filepath.Join(e.Directory, "owner/owner.json")
	if input.Journal.CompletedAt.IsZero() {
		ownerPath = filepath.Join(store.Root, "deployment/.execution-active/owner.json")
	}
	if readExecutionJSON(ownerPath, &e.owner) != nil || deploymentplan.Digest(e.owner) != input.Owner {
		t.Fatal("genuine original owner absent")
	}
	admission, err := readCreateNormalTailProgramAdmission(store, e)
	if err != nil {
		t.Fatal("genuine admission", err)
	}
	frozen, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		t.Fatal("genuine frozen target", err)
	}
	original, j, err := readCreateNormalTailWorkloadOriginal(store, e, frozen)
	if err != nil || original == nil {
		t.Fatal("genuine original journal", err)
	}
	at := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/workload" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("genuine workload absent")
	}
	return &nestedRuntimeReadFixture{store, e, original, j, admission, at}
}
func (f *nestedRuntimeReadFixture) read() (int, []*deploymentplan.Plan, error) {
	return validateCreateNormalTailNestedWorkloadStages(f.store, f.e, f.original, f.journal, f.e.Journal.Entries[f.outer], f.e.Journal.Entries[f.outer+1:], f.admission)
}

// The interrupted fixture is a retained genuine producer checkpoint: pending
// ancestors, allocated0004, and the actual accepted winner journal. The optional
// completed fixture is another untouched genuine producer export.
func TestNestedRuntimeReadEpochGenuineHistory(t *testing.T) {
	paths := []struct{ name, path string }{{"interrupted", os.Getenv("RTK_NESTED_RUNTIME_READER_EXPORT")}}
	if path := os.Getenv("RTK_NESTED_RUNTIME_READER_COMPLETE_EXPORT"); path != "" {
		paths = append(paths, struct{ name, path string }{"completed", path})
	}
	for _, item := range paths {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("RTK_NESTED_RUNTIME_READER_EXPORT", item.path)
			f := loadNestedRuntimeReadFixture(t)
			before := deploymentplan.Digest(f.e.Journal)
			count, children, err := f.read()
			if err != nil || count == 0 || len(children) == 0 {
				t.Fatal("genuine nested history refused", err)
			}
			if deploymentplan.Digest(f.e.Journal) != before {
				t.Fatal("pure read changed parent history")
			}
			found := false
			for _, p := range children {
				if p != nil {
					j, exists, err := p.ReadExecution(f.store.Root)
					if err != nil {
						t.Fatal(err)
					}
					found = found || exists && j.Accepted
				}
			}
			if !found {
				t.Fatal("accepted winner proof lost")
			}
			t.Logf("genuine %s history consumed %d entries and preserved %d child plans", item.name, count, len(children))
		})
	}
}
func TestNestedRuntimeReadEpochRechecksNextInvocation(t *testing.T) {
	f := loadNestedRuntimeReadFixture(t)
	if _, _, err := f.read(); err != nil {
		t.Fatal("initial genuine proof", err)
	}
	descriptor := createNormalTailRuntimeDescriptorPath(f.e, 4)
	original, err := os.ReadFile(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"changed-source", "changed-logical-receipt"} {
		t.Run(mode, func(t *testing.T) {
			entries := append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
			switch mode {
			case "changed-source":
				var d createNormalTailRuntimeForward
				if json.Unmarshal(original, &d) != nil {
					t.Fatal("descriptor")
				}
				d.SourceSHA256 = deploymentplan.Digest("changed after previous invocation")
				if err := os.WriteFile(descriptor, mustProviderRecoveryJSON(d), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-logical-receipt":
				for i := range f.e.Journal.Entries {
					if f.e.Journal.Entries[i].Key == "kubernetes/"+createNormalTailProductSessionForwardStage {
						f.e.Journal.Entries[i].Receipt = json.RawMessage(`{"foreign":true}`)
					}
				}
			}
			if _, _, err := f.read(); err == nil {
				t.Fatal("next invocation reused stale authenticated history")
			}
			f.e.Journal.Entries = entries
			if err := os.WriteFile(descriptor, original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, _, err := f.read(); err != nil {
		t.Fatal("restored genuine history", err)
	}
	after, err := os.ReadFile(descriptor)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("fixture descriptor not restored", err)
	}
}
