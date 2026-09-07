package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRehearsalProcessHelper(t *testing.T) {
	if os.Getenv("RTK_REHEARSAL_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	os.Stdout.Write([]byte("R"))
	<-signals
	os.Exit(0)
}
func TestRehearsalControlledShutdown(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRehearsalProcessHelper$")
	cmd.Env = append(os.Environ(), "RTK_REHEARSAL_HELPER=1")
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	b := make([]byte, 1)
	if _, err := io.ReadFull(ready, b); err != nil || string(b) != "R" {
		t.Fatal("helper not ready", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := stopRehearsal(cmd, done); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("shutdown did not confirm process exit")
	}
}
func TestRehearsalUnexpectedExitFails(t *testing.T) {
	done := make(chan error, 1)
	done <- nil
	if err := stopRehearsal(&exec.Cmd{}, done); err == nil {
		t.Fatal("unexpected exit accepted as controlled shutdown")
	}
}
func TestRehearsalEnvironmentIsolation(t *testing.T) {
	t.Setenv("PGHOST", "production")
	t.Setenv("PGOPTIONS", "-c listen_addresses=*")
	t.Setenv("PGSERVICE", "source")
	t.Setenv("LD_PRELOAD", "/unexpected")
	t.Setenv("RTK_BACKUP_ACCESS_KEY_ID", "fixture-reader")
	t.Setenv("RTK_BACKUP_SECRET_ACCESS_KEY", "fixture-secret")
	t.Setenv("SSL_CERT_FILE", "/private/fixture-ca.pem")
	got := rehearsalEnvironment("/opt/postgres/bin")
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{"PGHOST=", "PGOPTIONS=", "PGSERVICE=", "LD_PRELOAD="} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("ambient setting inherited", forbidden)
		}
	}
	for _, required := range []string{"LC_ALL=C", "PATH=/opt/postgres/bin:/usr/bin:/bin", "RTK_BACKUP_ACCESS_KEY_ID=fixture-reader", "SSL_CERT_FILE=/private/fixture-ca.pem"} {
		if !strings.Contains(joined, required) {
			t.Fatal("required runtime setting missing", required)
		}
	}
}

func TestRehearsalCancellationStopsOwnedProcess(t *testing.T) {
	e, dest, _, _ := observeFixture(t)
	binaryDir := privateTemp(t)
	e.Config.BinaryDirectory = binaryDir
	// Refresh the configuration binding after selecting the fixture executable.
	var manifest Manifest
	if err := readPrivatePITRJSON(filepath.Join(dest, "verified.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ConfigurationSHA256 = Digest(e.Config)
	if err := WriteJSON(filepath.Join(dest, "verified.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dest, "socket"), 0700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\ntrap 'exit 0' INT\nprintf ready\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(filepath.Join(binaryDir, "postgres"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.Exec = func(_ context.Context, _ []string, _ io.Reader, _ io.Writer) error {
		for {
			log, _ := os.ReadFile(filepath.Join(dest, "rehearsal-server.log"))
			if strings.Contains(string(log), "ready") {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		return context.Canceled
	}
	_, err := e.rehearsePrepared(ctx, "fixture", dest, "postgres", "postgres")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not propagated", err)
	}
	if strings.Contains(err.Error(), "shutdown") || strings.Contains(err.Error(), "termination") {
		t.Fatal("cancelled rehearsal did not stop cleanly", err)
	}
}
