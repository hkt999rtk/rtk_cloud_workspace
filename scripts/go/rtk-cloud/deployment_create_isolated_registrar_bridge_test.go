package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolatedRegistrarBridge exposes only the three bounded local-image actions
// required for TURN artifact extraction, never a Docker daemon socket.
type isolatedRegistrarBridge struct {
	server                                  *httptest.Server
	mu                                      sync.Mutex
	token, image, revision, root, container string
	capabilityBinaries                      map[string]map[string]bool
	run                                     func(context.Context, ...string) ([]byte, error)
}
type isolatedRegistrarRequest struct {
	Config string   `json:"config"`
	Args   []string `json:"args"`
}

func newIsolatedRegistrarBridge(t *testing.T, root, image, revision string, run func(context.Context, ...string) ([]byte, error)) *isolatedRegistrarBridge {
	t.Helper()
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || !isolatedScriptTempPath(actual) || !immutableEnvironmentImage.MatchString(image) || !deploymentUpgradeSHA.MatchString(revision) {
		t.Fatal("isolated registrar bridge requires temporary store, immutable image and exact source")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	b := &isolatedRegistrarBridge{token: hex.EncodeToString(random), image: image, revision: revision, root: actual, run: run}
	b.server = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(func() {
		b.server.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.container != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = b.run(ctx, "rm", "-f", b.container)
			b.container = ""
		}
	})
	return b
}
func (b *isolatedRegistrarBridge) inside(path string, isDir bool) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(b.root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && ((isDir && info.IsDir()) || (!isDir && info.Mode().IsRegular()))
}
func (b *isolatedRegistrarBridge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/docker" || r.Header.Get("Authorization") != "Bearer "+b.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req isolatedRegistrarRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 16384))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || (len(req.Args) > 0 && req.Args[0] != "run" && !b.inside(req.Config, true)) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	fail := func() { http.Error(w, "restricted docker operation failed", http.StatusBadRequest) }
	if len(req.Args) >= 15 && req.Args[0] == "run" {
		expected := []string{"run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint"}
		if req.Config != "" || !equalIsolatedRegistrarStrings(req.Args[:12], expected) || !b.capabilityBinaries[req.Args[13]][req.Args[12]] {
			fail()
			return
		}
		if req.Args[12] == "/bin/sh" {
			const script = "test -x /app/pkicontroller; test -x /app/pkiceremony; test -x /app/serviceidentity-bootstrap; test -x /app/openbaopkihost; test -x /app/emqxpkihost; openssl version >/dev/null"
			if len(req.Args) != 16 || req.Args[14] != "-ec" || req.Args[15] != script {
				fail()
				return
			}
		} else if len(req.Args) != 15 || req.Args[14] != "--deployment-capabilities" {
			fail()
			return
		}
		cid, err := os.CreateTemp(b.root, ".isolated-capability-cid-*")
		if err != nil {
			fail()
			return
		}
		cidPath := cid.Name()
		_ = cid.Close()
		_ = os.Remove(cidPath)
		defer func() {
			defer os.Remove(cidPath)
			raw, err := os.ReadFile(cidPath)
			id := strings.TrimSpace(string(raw))
			if err == nil && len(id) == 64 && strings.Trim(id, "0123456789abcdef") == "" {
				cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
				defer done()
				_, _ = b.run(cleanup, "rm", "-f", id)
			}
		}()
		args := append([]string{"run", "--pull=never", "--cidfile", cidPath}, req.Args[1:]...)
		out, err := b.run(ctx, args...)
		if err != nil || len(out) > 16<<10 {
			fail()
			return
		}
		_, _ = w.Write(out)
		return
	}
	if len(req.Args) == 6 && req.Args[0] == "create" && req.Args[1] == "--platform" && req.Args[2] == "linux/amd64" && req.Args[3] == "--entrypoint" && req.Args[4] == "/app/turnregistrar" && req.Args[5] == b.image && b.container == "" {
		out, err := b.run(ctx, "--config", req.Config, "create", "--pull=never", "--platform", "linux/amd64", "--entrypoint", "/app/turnregistrar", b.image)
		id := strings.TrimSpace(string(out))
		if err != nil || len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			fail()
			return
		}
		b.container = id
		_, _ = fmt.Fprintln(w, id)
		return
	}
	if len(req.Args) == 3 && req.Args[0] == "cp" && b.container != "" && req.Args[1] == b.container+":/app/turnregistrar" && b.inside(req.Args[2], false) {
		before, err := os.Lstat(req.Args[2])
		if err != nil {
			fail()
			return
		}
		stage, err := os.CreateTemp("", "rtk-registrar-stage-*")
		if err != nil {
			fail()
			return
		}
		path := stage.Name()
		_ = stage.Close()
		defer os.Remove(path)
		if _, err := b.run(ctx, "--config", req.Config, "cp", req.Args[1], path); err != nil {
			fail()
			return
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128<<20 {
			fail()
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fail()
			return
		}
		parent := filepath.Dir(req.Args[2])
		if !b.inside(parent, true) {
			fail()
			return
		}
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			fail()
			return
		}
		directory, err := os.Open(resolvedParent)
		if err != nil {
			fail()
			return
		}
		defer directory.Close()
		fd, err := unix.Openat(int(directory.Fd()), filepath.Base(req.Args[2]), unix.O_WRONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			fail()
			return
		}
		f := os.NewFile(uintptr(fd), "isolated-turnregistrar")
		current, err := f.Stat()
		if err != nil || !os.SameFile(before, current) {
			_ = f.Close()
			fail()
			return
		}
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			fail()
			return
		}
		_, err = io.Copy(f, bytes.NewReader(data))
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			fail()
			return
		}
		return
	}
	if len(req.Args) == 2 && req.Args[0] == "rm" && b.container != "" && req.Args[1] == b.container {
		if _, err := b.run(ctx, "--config", req.Config, "rm", b.container); err != nil {
			fail()
			return
		}
		b.container = ""
		return
	}
	fail()
}
func equalIsolatedRegistrarStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (b *isolatedRegistrarBridge) port(t *testing.T) string {
	t.Helper()
	_, p, err := net.SplitHostPort(b.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func installIsolatedRegistrarDockerShim(t *testing.T, dir string) {
	t.Helper()
	original := filepath.Join(dir, "docker")
	if err := os.Rename(original, filepath.Join(dir, "docker-publication")); err != nil {
		t.Fatal(err)
	}
	const shim = `#!/usr/bin/python3
import json, os, sys, urllib.request
args = sys.argv[1:]
if args and args[0] == 'run':
    config, selected = '', args
elif len(args) >= 3 and args[0] == '--config' and args[2] in ('create','cp','rm'):
    config, selected = args[1], args[2:]
else:
    os.execv(os.path.join(os.path.dirname(__file__), 'docker-publication'), ['docker'] + args)
req = urllib.request.Request(os.environ['RTK_TEST_REGISTRAR_BRIDGE'] + '/docker', data=json.dumps({'config':config, 'args':selected}).encode(), headers={'Authorization':'Bearer ' + os.environ['RTK_TEST_REGISTRAR_TOKEN'], 'Content-Type':'application/json'}, method='POST')
try:
    with urllib.request.urlopen(req, timeout=35) as result:
        sys.stdout.buffer.write(result.read())
except Exception:
    sys.exit(1)
`
	if err := os.WriteFile(original, []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedRegistrarBridgeLimitsOperationsAndCleansUp(t *testing.T) {
	var calls []string
	id := strings.Repeat("a", 64)
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		for i, arg := range args {
			if arg == "create" {
				return []byte(id + "\n"), nil
			}
			if arg == "cp" {
				if i+2 >= len(args) {
					return nil, fmt.Errorf("bad cp")
				}
				return nil, os.WriteFile(args[i+2], []byte("isolated artifact"), 0600)
			}
		}
		return nil, nil
	}
	t.Run("scoped and cleanup", func(t *testing.T) {
		root := t.TempDir()
		config := filepath.Join(root, "config")
		if err := os.Mkdir(config, 0700); err != nil {
			t.Fatal(err)
		}
		artifactDir := filepath.Join(root, "artifacts")
		if err := os.Mkdir(artifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(artifactDir, "artifact")
		if err := os.WriteFile(dest, nil, 0600); err != nil {
			t.Fatal(err)
		}
		image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64)
		b := newIsolatedRegistrarBridge(t, root, image, strings.Repeat("a", 40), run)
		call := func(token string, args ...string) int {
			t.Helper()
			payload, _ := json.Marshal(isolatedRegistrarRequest{Config: config, Args: args})
			r, err := http.NewRequest(http.MethodPost, b.server.URL+"/docker", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+token)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			return response.StatusCode
		}
		create := []string{"create", "--platform", "linux/amd64", "--entrypoint", "/app/turnregistrar", image}
		if got := call("wrong", create...); got != http.StatusForbidden {
			t.Fatal("unauthorized", got)
		}
		bad := append([]string(nil), create...)
		bad[5] = image + "bad"
		if got := call(b.token, bad...); got != http.StatusBadRequest {
			t.Fatal("wrong image", got)
		}
		if got := call(b.token, create...); got != http.StatusOK {
			t.Fatal("create", got)
		}
		if got := call(b.token, "cp", strings.Repeat("b", 64)+":/app/turnregistrar", dest); got != http.StatusBadRequest {
			t.Fatal("foreign owner", got)
		}
		if got := call(b.token, "cp", id+":/etc/passwd", dest); got != http.StatusBadRequest {
			t.Fatal("foreign source", got)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(dest, link); err != nil {
			t.Fatal(err)
		}
		if got := call(b.token, "cp", id+":/app/turnregistrar", link); got != http.StatusBadRequest {
			t.Fatal("symlink destination", got)
		}
		if got := call(b.token, "cp", id+":/app/turnregistrar", dest); got != http.StatusOK {
			t.Fatal("valid copy", got)
		}
		if data, err := os.ReadFile(dest); err != nil || string(data) != "isolated artifact" {
			t.Fatal("artifact mismatch", err)
		}
		if got := call(b.token, "rm", strings.Repeat("b", 64)); got != http.StatusBadRequest {
			t.Fatal("foreign container removal", got)
		}
	})
	if len(calls) != 3 || !strings.Contains(calls[0], "--pull=never") || !strings.Contains(calls[2], "rm -f "+id) {
		t.Fatalf("unexpected calls/cleanup: %v", calls)
	}
}

func TestIsolatedRegistrarBridgeRestrictsCapabilityRuns(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64)
	var calls [][]string
	id := strings.Repeat("c", 64)
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "run" {
			for i, a := range args {
				if a == "--cidfile" && i+1 < len(args) {
					if err := os.WriteFile(args[i+1], []byte(id), 0600); err != nil {
						return nil, err
					}
				}
			}
			return []byte(`{"schema":"synthetic-test"}`), nil
		}
		return nil, nil
	}
	b := newIsolatedRegistrarBridge(t, root, image, strings.Repeat("a", 40), run)
	b.capabilityBinaries = map[string]map[string]bool{image: {"/app/serviceidentity-bootstrap": true}}
	call := func(args []string) int {
		payload, _ := json.Marshal(isolatedRegistrarRequest{Args: args})
		r, err := http.NewRequest(http.MethodPost, b.server.URL+"/docker", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+b.token)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	good := []string{"run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", image, "--deployment-capabilities"}
	for _, bad := range [][]string{
		append(append([]string(nil), good...), "extra"),
		append(append([]string(nil), good[:5]...), append([]string{"host"}, good[6:]...)...),
		append(append([]string(nil), good[:12]...), append([]string{"/bin/bash"}, good[13:]...)...),
		append(append([]string(nil), good[:13]...), append([]string{image + "bad"}, good[14:]...)...),
	} {
		if got := call(bad); got != http.StatusBadRequest {
			t.Fatal("unexpected acceptance", bad, got)
		}
	}
	if got := call(good); got != http.StatusOK {
		t.Fatal("valid run rejected", got)
	}
	if len(calls) != 2 || calls[0][0] != "run" || calls[0][1] != "--pull=never" || !equalIsolatedRegistrarStrings(calls[1], []string{"rm", "-f", id}) {
		t.Fatalf("unexpected invocation or cleanup: %v", calls)
	}
}
