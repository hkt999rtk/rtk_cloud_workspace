package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Explicit-file mode compiles the real Video Cloud Event codec against the
// workspace's reviewed Logger gitlink, without upgrading Video Cloud's released
// Logger dependency or making its standalone go mod tidy depend on this workspace.
func TestBillingArchiveVideoCloudCanonicalCompatibility(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workspace compatibility fixture")
	}
	module := filepath.Dir(filepath.Dir(source))
	usage := filepath.Join(module, "..", "..", "repos", "rtk_video_cloud", "internal", "usage")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", "^TestRawLifecycleArchiveCanonicalBindingCompatibility$",
		filepath.Join(usage, "event.go"), filepath.Join(usage, "raw_lifecycle_archive_compat_test.go"))
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Logger/Video Cloud financial canonicalization compatibility: %v\n%s", err, output)
	}
}
