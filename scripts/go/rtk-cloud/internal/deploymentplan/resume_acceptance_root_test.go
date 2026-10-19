package deploymentplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func appliedPodAcceptanceRootFixture(t *testing.T) (*Plan, *parentExecutionFixture, string, Journal) {
	t.Helper()
	in := Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: Digest("cfg"), ReleaseSHA256: Digest("release"), BindingsSHA256: Digest("helper")}
	pod := Object{"apiVersion": "v1", "kind": "Pod", "metadata": Object{"name": "redis-durable-aaaaaaaaaaaa", "namespace": "video-cloud-dev-platform"}, "spec": Object{"restartPolicy": "Never", "containers": []any{Object{"name": "redis-migration", "image": "registry.example/helper@sha256:" + strings.Repeat("a", 64)}}}}
	p, err := Compile(in, []Definition{{Object: pod, Phase: "workload"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "deployment", "rollback", p.SHA256())
	parent := filepath.Join(root, "deployment", ".execution-active")
	for _, path := range []string{directory, parent} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "parent"), []byte("original-parent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusiveJSON(filepath.Join(directory, "plan.json"), p); err != nil {
		t.Fatal(err)
	}
	id := p.Resources[0].ID
	uid := "11111111-1111-4111-8111-111111111111"
	j := Journal{PlanSHA256: p.SHA256(), Completed: []string{id}, Owners: map[string]string{id: uid}, AcceptanceStarted: true, Failed: "deployment.post-deploy.workloads"}
	if err := saveJournal(directory, j); err != nil {
		t.Fatal(err)
	}
	live := Clone(p.Resources[0].Desired)
	Map(live["metadata"])["uid"] = uid
	live["status"] = Object{"phase": "Running"}
	b := &parentExecutionFixture{root: root, memoryBackend: memoryBackend{objects: map[string]Object{id: live}}}
	return p, b, root, j
}

func appliedPodJournalRootBytes(t *testing.T, root, digest string) ([]byte, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "deployment", "rollback", digest, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	return raw, hex.EncodeToString(h[:])
}

func TestAppliedPodAcceptanceOnlyRootPreservesWriteEvidence(t *testing.T) {
	p, b, root, original := appliedPodAcceptanceRootFixture(t)
	digest := p.SHA256()
	originalRaw, originalSHA := appliedPodJournalRootBytes(t, root, digest)
	planPath := filepath.Join(root, "deployment", "rollback", digest, "plan.json")
	planRaw, _ := os.ReadFile(planPath)
	accepts := 0
	fence := func(ctx context.Context, p *Plan) error { return b.CheckParentExecution(ctx, p, root) }
	accept := func(_ context.Context, _ *Plan, owners map[string]string) error {
		accepts++
		if !b.leaseHeld || !reflect.DeepEqual(owners, original.Owners) {
			t.Fatal("acceptance lost its lease/original owner")
		}
		return nil
	}
	j, err := p.ResumeAppliedPodAcceptance(t.Context(), b, digest, originalSHA, root, fence, accept)
	if err != nil {
		t.Fatal(err)
	}
	if accepts != 1 || !j.Accepted || j.Failed != "" || !j.AcceptanceStarted || !reflect.DeepEqual(j.Completed, original.Completed) || !reflect.DeepEqual(j.Owners, original.Owners) {
		t.Fatal("original write evidence was replaced or acceptance skipped")
	}
	if err := VerifyAppliedPodAcceptanceReceipt(root, digest, j); err != nil {
		t.Fatal("genuine acceptance receipt cannot be consumed", err)
	}
	if len(b.written) != 0 || len(b.dry) != 0 || b.leaseHeld || b.records != 0 {
		t.Fatal("acceptance-only path admitted/wrote resources or left its lease held")
	}
	if after, _ := os.ReadFile(planPath); string(after) != string(planRaw) {
		t.Fatal("original plan changed")
	}
	if parent, _ := os.ReadFile(filepath.Join(root, "deployment", ".execution-active", "parent")); string(parent) != "original-parent" {
		t.Fatal("original parent changed")
	}
	if _, err := os.Stat(filepath.Join(root, "deployment", "rollback", digest, ".active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("child local lock retained")
	}
	paths, err := filepath.Glob(filepath.Join(root, "deployment", "rollback", digest, "acceptance-recovery", "attempt-*", "original.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("original failed evidence not preserved", err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var preserved struct {
		JournalSHA string
		Journal    json.RawMessage
		Lease      LeaseOwner
	}
	if json.Unmarshal(raw, &preserved) != nil || preserved.JournalSHA != originalSHA || Digest(json.RawMessage(originalRaw)) != Digest(preserved.Journal) || preserved.Lease.UID == "" {
		t.Fatal("failed evidence/actual revalidation lease differs")
	}
	completed := filepath.Join(filepath.Dir(paths[0]), "completed.json")
	if info, err := os.Stat(completed); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("successful acceptance receipt missing/private mode changed", err)
	}
	stored, _ := appliedPodJournalRootBytes(t, root, digest)
	var actual Journal
	if json.Unmarshal(stored, &actual) != nil || !actual.Accepted || actual.Failed != "" {
		t.Fatal("actual accepted journal was not published")
	}
	if err := os.Remove(completed); err != nil {
		t.Fatal(err)
	}
	if VerifyAppliedPodAcceptanceReceipt(root, digest, actual) == nil {
		t.Fatal("accepted journal without genuine completed receipt was adopted")
	}
}

func TestAppliedPodAcceptanceOnlyRootRejectsUnknownOrDriftingState(t *testing.T) {
	for _, mode := range []string{"wrong-hash", "in-flight", "unapplied", "missing-owner", "other-failure", "already-accepted", "wrong-native-uid", "accept-failed", "uid-replaced-during-accept", "parent-lost-during-accept"} {
		t.Run(mode, func(t *testing.T) {
			p, b, root, original := appliedPodAcceptanceRootFixture(t)
			digest := p.SHA256()
			switch mode {
			case "in-flight":
				original.InFlight = p.Resources[0].ID
			case "unapplied":
				original.Completed = nil
			case "missing-owner":
				original.Owners = map[string]string{}
			case "other-failure":
				original.Failed = "deployment.write"
			case "already-accepted":
				original.Accepted, original.Failed = true, ""
			case "wrong-native-uid":
				Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
			}
			if err := saveJournal(filepath.Join(root, "deployment", "rollback", digest), original); err != nil {
				t.Fatal(err)
			}
			before, pin := appliedPodJournalRootBytes(t, root, digest)
			if mode == "wrong-hash" {
				pin = strings.Repeat("0", 64)
			}
			fence := func(ctx context.Context, p *Plan) error { return b.CheckParentExecution(ctx, p, root) }
			accept := func(context.Context, *Plan, map[string]string) error {
				switch mode {
				case "accept-failed":
					return errors.New("required acceptance failed")
				case "uid-replaced-during-accept":
					Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
				case "parent-lost-during-accept":
					return os.Remove(filepath.Join(root, "deployment", ".execution-active", "parent"))
				}
				return nil
			}
			if _, err := p.ResumeAppliedPodAcceptance(t.Context(), b, digest, pin, root, fence, accept); err == nil {
				t.Fatal("unsafe continuation accepted")
			}
			after, _ := appliedPodJournalRootBytes(t, root, digest)
			if string(before) != string(after) || len(b.written) != 0 || len(b.dry) != 0 || b.leaseHeld {
				t.Fatal("failed continuation changed original evidence/resources or retained its lease")
			}
		})
	}
}
