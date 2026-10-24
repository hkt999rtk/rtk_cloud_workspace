package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRedisSourceCodecFixture(t *testing.T) (secretStore, managedLateRedisDurableSaved, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "config")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store := secretStore{ConfigRoot: root, Root: filepath.Join(root, "dev"), Environment: "dev"}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("a", 64)
	source := environmentCreateJournal{PlanSHA256: parent, Environment: "dev", Stack: "video-cloud-dev"}
	for i := range 434 {
		source.Entries = append(source.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("recorded-%03d", i), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"public_receipt": strings.Repeat("p", 1024)})})
	}
	d := createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, ParentSHA: parent, SourceSHA: deploymentplan.Digest(source), SourceLeases: len(source.KubernetesLeases)}
	return store, managedLateRedisDurableSaved{Descriptor: d, Source: source}, filepath.Join(managedLateRedisDurableBase(parent), "source.json")
}

func TestManagedLateRedisSourceSnapshotRoundTripsFull434BeyondCeremonyBound(t *testing.T) {
	store, saved, relative := managedLateRedisSourceCodecFixture(t)
	before := deploymentplan.Digest(saved)
	raw, err := json.Marshal(saved)
	if err != nil || len(raw) <= 256<<10 || len(raw) >= 4<<20 {
		t.Fatal("fixture must reproduce the full-journal size gap", len(raw), err)
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, saved); err == nil {
		t.Fatal("ordinary ceremony metadata bound was widened")
	}
	if _, err := os.Lstat(filepath.Join(store.Root, relative)); !os.IsNotExist(err) {
		t.Fatal("small-record refusal published source metadata", err)
	}
	ref, err := writeManagedLateRedisDurableSource(store, saved)
	if err != nil || ref != "/store/"+filepath.ToSlash(relative) {
		t.Fatal("full authenticated source snapshot refused", ref, err)
	}
	var decoded managedLateRedisDurableSaved
	if err := createOpenBaoACKCompletionRead(store, relative, &decoded); err != nil || deploymentplan.Digest(decoded) != before || len(decoded.Source.Entries) != 434 {
		t.Fatal("existing consumer did not recover identical source and descriptor", err)
	}
	written, err := os.ReadFile(filepath.Join(store.Root, relative))
	if err != nil || !bytes.Equal(raw, written) || deploymentplan.Digest(saved) != before {
		t.Fatal("source wire bytes or in-memory evidence changed", err)
	}
	info, err := os.Stat(filepath.Join(store.Root, relative))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source metadata was not private", err)
	}
	for p := filepath.Dir(filepath.Join(store.Root, relative)); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("source ancestor was not a real private directory", err)
		}
		if p == store.ConfigRoot {
			break
		}
	}
	if _, err := writeManagedLateRedisDurableSource(store, saved); err == nil {
		t.Fatal("retry overwrote an existing exclusive source snapshot")
	}
	saved.QualificationSHA = strings.Repeat("b", 64)
	if _, err := writeManagedLateRedisDurableSource(store, saved); err == nil {
		t.Fatal("changed source snapshot overwrote original evidence")
	}
	after, _ := os.ReadFile(filepath.Join(store.Root, relative))
	if !bytes.Equal(written, after) {
		t.Fatal("exclusive retry changed original source bytes")
	}
}

func TestManagedLateRedisSourceSnapshotRefusesOversizeEncodingAndUnsafeShape(t *testing.T) {
	for _, mode := range []string{"oversize", "encoding", "source-hash", "parent", "entries", "leases", "schema", "foreign-store", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store, saved, relative := managedLateRedisSourceCodecFixture(t)
			switch mode {
			case "oversize":
				saved.Source.Entries[0].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"public_receipt": strings.Repeat("p", 4<<20)})
				saved.Descriptor.SourceSHA = deploymentplan.Digest(saved.Source)
			case "encoding":
				saved.Descriptor.Claim = deploymentplan.Object{"unsupported": make(chan int)}
			case "source-hash":
				saved.Descriptor.SourceSHA = strings.Repeat("b", 64)
			case "parent":
				saved.Descriptor.ParentSHA = strings.Repeat("b", 64)
			case "entries":
				saved.Source.Entries = saved.Source.Entries[:433]
				saved.Descriptor.SourceSHA = deploymentplan.Digest(saved.Source)
			case "leases":
				saved.Descriptor.SourceLeases++
			case "schema":
				saved.Descriptor.Schema = "foreign"
			case "foreign-store":
				store.Root = filepath.Join(store.ConfigRoot, "foreign")
			case "symlink":
				ancestor := filepath.Dir(filepath.Join(store.Root, relative))
				if err := os.MkdirAll(filepath.Dir(ancestor), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), ancestor); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := writeManagedLateRedisDurableSource(store, saved); err == nil {
				t.Fatal("unsafe source snapshot accepted", mode)
			}
			if _, err := os.Lstat(filepath.Join(store.Root, relative)); !os.IsNotExist(err) {
				t.Fatal("refused source snapshot published evidence", err)
			}
		})
	}
}
