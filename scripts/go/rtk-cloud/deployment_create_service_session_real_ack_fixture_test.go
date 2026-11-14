package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// unifiedRealACKFixture drives the separately compiled product overlay through
// a private local IPC directory; no credential or query is printed to logs.
type unifiedRealACKFixture struct {
	dir        string
	command    *exec.Cmd
	cancel     context.CancelFunc
	done       chan struct{}
	processErr error // written before done closes
	output     *os.File
	mu         sync.Mutex
	sequence   uint64
}

type unifiedRealACKResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// startUnifiedRealACKFixture accepts the native harness input as a JSON-shaped
// map. The supplied issuer, key, CSR, certificates, sessions and clients are
// preserved exactly; only the private IPC directory is added by this helper.
func startUnifiedRealACKFixture(t *testing.T, workspace string, fixture map[string]any) *unifiedRealACKFixture {
	t.Helper()
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
		t.Skip("isolated PostgreSQL runtime not configured")
	}
	dir, err := os.MkdirTemp(t.TempDir(), "unified-native-ack-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := make(map[string]any, len(fixture)+1)
	for k, v := range fixture {
		data[k] = v
	}
	if _, exists := data["directory"]; exists {
		t.Fatal("caller must not choose the IPC directory")
	}
	data["directory"] = dir
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	const pkg = "cmd/serviceidentity-bootstrap"
	repo := filepath.Join(workspace, "repos", "rtk_video_cloud")
	fixtures := filepath.Join(workspace, "scripts", "go", "rtk-cloud", "testdata", "native-product", filepath.FromSlash(pkg))
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		virtual := filepath.Join(repo, filepath.FromSlash(pkg), entry.Name())
		if _, err := os.Lstat(virtual); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("native fixture would shadow product file %s", entry.Name())
		}
		replacements[virtual] = filepath.Join(fixtures, entry.Name())
	}
	if _, ok := replacements[filepath.Join(repo, filepath.FromSlash(pkg), "native_unified_session_ack_test.go")]; !ok {
		t.Fatal("unified native overlay unavailable")
	}
	overlayRaw, err := json.Marshal(map[string]any{"Replace": replacements})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err = os.WriteFile(overlay, overlayRaw, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(filepath.Join(dir, "native-test.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./"+pkg, "-run", "^TestNativeUnifiedSessionACKHarness$", "-count=1", "-v", "-timeout=4m")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOWORK=off", "RTK_UNIFIED_ACK_FIXTURE="+fixturePath)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		cancel()
		output.Close()
		t.Fatal(err)
	}
	f := &unifiedRealACKFixture{dir: dir, command: cmd, cancel: cancel, done: make(chan struct{}), output: output}
	go func() { f.processErr = cmd.Wait(); _ = output.Close(); close(f.done) }()
	t.Cleanup(func() {
		select {
		case <-f.done:
		default:
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = f.Stop(ctx)
			stop()
		}
		f.cancel()
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			t.Error("native ACK helper did not exit")
		}
	})
	readyCtx, readyCancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer readyCancel()
	response, err := f.wait(readyCtx, "ready")
	if err != nil || string(response.Result) != "true" {
		detail, _ := os.ReadFile(filepath.Join(dir, "native-test.log"))
		if len(detail) > 64<<10 {
			detail = detail[len(detail)-(64<<10):]
		}
		t.Fatalf("native ACK harness not ready: %v; isolated test log: %s", err, detail)
	}
	return f
}

func (f *unifiedRealACKFixture) wait(ctx context.Context, id string) (unifiedRealACKResponse, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(filepath.Join(f.dir, "response.json"))
		if err == nil {
			var response unifiedRealACKResponse
			if json.Unmarshal(raw, &response) != nil {
				return response, errors.New("invalid native ACK response")
			}
			if response.ID == id {
				if response.Error != "" {
					return response, fmt.Errorf("native ACK: %s", response.Error)
				}
				return response, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return unifiedRealACKResponse{}, err
		}
		select {
		case <-ctx.Done():
			return unifiedRealACKResponse{}, ctx.Err()
		case <-f.done:
			// The child atomically publishes its final response immediately before
			// exiting; observe it once more before reporting an early exit.
			if raw, readErr := os.ReadFile(filepath.Join(f.dir, "response.json")); readErr == nil {
				var response unifiedRealACKResponse
				if json.Unmarshal(raw, &response) == nil && response.ID == id {
					if response.Error != "" {
						return response, fmt.Errorf("native ACK: %s", response.Error)
					}
					return response, nil
				}
			}
			return unifiedRealACKResponse{}, fmt.Errorf("native ACK process exited before %s: %v", id, f.processErr)
		case <-ticker.C:
		}
	}
}

func (f *unifiedRealACKFixture) call(ctx context.Context, action, session, subject string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	id := strconv.FormatUint(f.sequence, 10)
	raw, err := json.Marshal(map[string]string{"id": id, "action": action, "session": session, "subject": subject})
	if err != nil {
		return nil, err
	}
	pending := filepath.Join(f.dir, "request.tmp")
	if err = os.WriteFile(pending, raw, 0600); err != nil {
		return nil, err
	}
	if err = os.Rename(pending, filepath.Join(f.dir, "request.json")); err != nil {
		return nil, err
	}
	response, err := f.wait(ctx, id)
	return response.Result, err
}

func (f *unifiedRealACKFixture) ACK(ctx context.Context, session, subject string) error {
	result, err := f.call(ctx, "ack", session, subject)
	if err != nil {
		return err
	}
	if string(result) != "true" {
		return errors.New("native ACK missing success")
	}
	return nil
}
func (f *unifiedRealACKFixture) Select(ctx context.Context, session string) (json.RawMessage, error) {
	return f.call(ctx, "select", session, "")
}
func (f *unifiedRealACKFixture) Stop(ctx context.Context) error {
	if _, err := f.call(ctx, "stop", "", ""); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
	}
	if f.processErr != nil {
		return fmt.Errorf("native ACK process failed: %w", f.processErr)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "native-test.log"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), "--- PASS: TestNativeUnifiedSessionACKHarness") {
		return errors.New("native ACK test did not report PASS")
	}
	return nil
}
