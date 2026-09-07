package recovery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func raftComponent() Component {
	return Component{ID: "openbao", Kind: "openbao-raft", Namespace: "platform", Pod: "openbao-0", Container: "openbao", RaftPeers: []string{"openbao-0", "openbao-1", "openbao-2"}, TokenFile: "/recovery/token", CAFile: "/openbao/transport/ca.crt", TLSServerName: "openbao-internal"}
}
func TestRaftConfig(t *testing.T) {
	c := fixture(t)
	c.Components[2] = raftComponent()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Component){
		"duplicate": func(c *Component) { c.RaftPeers[1] = c.RaftPeers[0] },
		"single":    func(c *Component) { c.RaftPeers = c.RaftPeers[:1] },
		"even":      func(c *Component) { c.RaftPeers = c.RaftPeers[:2] },
		"pod":       func(c *Component) { c.Pod = "other" },
		"token":     func(c *Component) { c.TokenFile = "relative" },
		"ca":        func(c *Component) { c.CAFile = "/a/../b" },
		"tls":       func(c *Component) { c.TLSServerName = "*.example.com" },
		"container": func(c *Component) { c.Container = "" },
		"pvc":       func(c *Component) { c.PVC = "physical" },
	} {
		t.Run(name, func(t *testing.T) {
			v := raftComponent()
			mutate(&v)
			if validateRaftComponent(v) == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
	original := fixture(t)
	c.Components = append(c.Components, original.Components[2])
	c.Components[len(c.Components)-1].ID = "other"
	if c.Validate() == nil {
		t.Fatal("accepted mixed backend")
	}
	if !logicalPod(raftComponent(), "openbao-2") || logicalPod(raftComponent(), "other") {
		t.Fatal("incorrect peer coverage")
	}
}
func TestRaftExecBoundary(t *testing.T) {
	for _, op := range []string{"save", "restore"} {
		t.Run(op, func(t *testing.T) {
			calls := 0
			failure := errors.New("uncertain transport failure")
			e := Engine{Config: Config{TimeoutSeconds: 5}, Exec: func(_ context.Context, args []string, in io.Reader, out io.Writer) error {
				calls++
				all := strings.Join(args, " ")
				for _, required := range []string{"env -i", "BAO_SKIP_VERIFY=false", "BAO_MAX_RETRIES=0", "BAO_ADDR=https://127.0.0.1:8200", "/recovery/token"} {
					if !strings.Contains(all, required) {
						t.Fatalf("missing %s", required)
					}
				}
				if strings.Contains(all, "-force") {
					t.Fatal("forced restore")
				}
				if args[len(args)-2] != op {
					t.Fatal("wrong operation")
				}
				want := "/dev/stdout"
				if op == "restore" {
					want = "/dev/stdin"
					b, _ := io.ReadAll(in)
					if string(b) != "snapshot" {
						t.Fatal("lost stream")
					}
				}
				if args[len(args)-1] != want {
					t.Fatal("wrong stream")
				}
				return failure
			}}
			if err := e.raftSnapshot(context.Background(), raftComponent(), op, strings.NewReader("snapshot"), io.Discard); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("retried ambiguous operation")
			}
		})
	}
}
func snapshotFixture(t *testing.T, change func(map[string][]byte), extra bool) []byte {
	t.Helper()
	m := map[string][]byte{"meta.json": []byte(`{"Size":5,"Index":10,"Term":2}`), "state.bin": []byte("state"), "SHA256SUMS.sealed": []byte("sealed-fixture")}
	m["SHA256SUMS"] = []byte(fmt.Sprintf("%x  meta.json\n%x  state.bin\n", sha256.Sum256(m["meta.json"]), sha256.Sum256(m["state.bin"])))
	if change != nil {
		change(m)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"meta.json", "state.bin", "SHA256SUMS", "SHA256SUMS.sealed"} {
		data, ok := m[name]
		if !ok {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if extra {
		tw.WriteHeader(&tar.Header{Name: "state.bin", Mode: 0600, Size: 1})
		tw.Write([]byte("x"))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestRaftSnapshotValidation(t *testing.T) {
	for name, change := range map[string]func(map[string][]byte){"valid": nil, "corrupt": func(m map[string][]byte) { m["state.bin"] = []byte("other") }, "unsealed": func(m map[string][]byte) { delete(m, "SHA256SUMS.sealed") }, "missing": func(m map[string][]byte) { delete(m, "meta.json") }, "checksums": func(m map[string][]byte) { m["SHA256SUMS"] = append(m["SHA256SUMS"], m["SHA256SUMS"]...) }} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "snapshot")
			os.WriteFile(p, snapshotFixture(t, change, false), 0600)
			err := validateRaftSnapshot(p, 1<<20)
			if (err == nil) != (name == "valid") {
				t.Fatal(err)
			}
		})
	}
	valid := snapshotFixture(t, nil, false)
	for name, b := range map[string][]byte{"truncated": valid[:len(valid)-4], "duplicate": snapshotFixture(t, nil, true), "trailing": append(append([]byte{}, valid...), []byte("bad")...)} {
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, b, 0600)
		if validateRaftSnapshot(p, 1<<20) == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	p := filepath.Join(t.TempDir(), "limited")
	os.WriteFile(p, valid, 0600)
	if validateRaftSnapshot(p, 128) == nil {
		t.Fatal("accepted decompression over limit")
	}
}

// Optional fixture generated by the pinned OpenBao binary, with real sealed sums.
func TestRaftNativeSnapshot(t *testing.T) {
	p := os.Getenv("RTK_TEST_RAFT_SNAPSHOT")
	if p == "" {
		t.Skip("native snapshot not supplied")
	}
	if err := validateRaftSnapshot(p, 64<<20); err != nil {
		t.Fatal(err)
	}
}

func TestRaftCaptureAndApply(t *testing.T) {
	snapshot := snapshotFixture(t, nil, false)
	e := Engine{Config: Config{Components: []Component{raftComponent()}, TimeoutSeconds: 5, MaxArchiveBytes: 1 << 20}}
	calls := 0
	e.Exec = func(_ context.Context, args []string, in io.Reader, out io.Writer) error {
		calls++
		switch args[len(args)-2] {
		case "save":
			_, err := out.Write(snapshot)
			return err
		case "restore":
			b, err := io.ReadAll(in)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, snapshot) {
				t.Fatal("restored different snapshot")
			}
			return nil
		default:
			t.Fatal("unexpected command")
			return nil
		}
	}
	dir := t.TempDir()
	if err := e.Capture(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("unexpected command count %d", calls)
	}
	// Capture must reject invalid native bytes, not package them as a backup.
	e.Exec = func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
		_, err := out.Write([]byte("not a snapshot"))
		return err
	}
	if e.Capture(context.Background(), t.TempDir()) == nil {
		t.Fatal("accepted invalid provider output")
	}
	e.Exec = func(_ context.Context, _ []string, _ io.Reader, _ io.Writer) error {
		return errors.New("uncertain restore")
	}
	if err := e.Apply(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "remains in maintenance") {
		t.Fatal("lost maintenance failure", err)
	}
}

func TestRaftPreflightPeerInventory(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "unlisted", "offline"} {
		t.Run(scenario, func(t *testing.T) {
			e, f := newFake(t)
			e.Config.Components[2] = raftComponent()
			e.Config.Workloads[2].Role = "data"
			if scenario == "offline" {
				e.Config.Workloads[2].Role = "offline"
			}
			f.config = e.Config
			e.Exec = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
				if strings.Contains(strings.Join(args, " "), " get pods ") {
					names := []string{"openbao-0", "openbao-1", "openbao-2"}
					if scenario == "missing" {
						names = names[:2]
					}
					if scenario == "unlisted" {
						names = append(names, "openbao-3")
					}
					items := []any{}
					for _, name := range names {
						items = append(items, map[string]any{"metadata": map[string]any{"name": name, "ownerReferences": []any{map[string]string{"kind": "StatefulSet", "name": "openbao"}}}, "status": map[string]string{"phase": "Running"}})
					}
					return json.NewEncoder(out).Encode(map[string]any{"items": items})
				}
				return f.exec(ctx, args, in, out)
			}
			_, err := e.Preflight(context.Background())
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("unexpected preflight result: %v", err)
			}
		})
	}
}
