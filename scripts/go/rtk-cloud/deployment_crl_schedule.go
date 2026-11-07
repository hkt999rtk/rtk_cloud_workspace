package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type crlScheduleOptions struct{ environment, workspace, confirm, image, helperRevision string }
type crlScheduleResult struct {
	FinishedAt time.Time `json:"finished_at"`
	ExitCode   int       `json:"exit_code"`
}

func runEnvironmentCRLSchedule(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: deployment crl-schedule install|status|remove --environment dev|staging|prod; install requires --workspace ABSOLUTE_PATH --confirm STACK --image CI_IMAGE@sha256:DIGEST")
	}
	action := args[0]
	if action != "install" && action != "status" && action != "remove" {
		return fmt.Errorf("unknown CRL schedule action %q", action)
	}
	var o crlScheduleOptions
	fs := flag.NewFlagSet("crl-schedule", flag.ContinueOnError)
	fs.StringVar(&o.environment, "environment", "", "explicit dev, staging or prod target")
	fs.StringVar(&o.workspace, "workspace", "", "absolute stable checkout used by every scheduled run")
	fs.StringVar(&o.confirm, "confirm", "", "exact environment stack confirmation")
	fs.StringVar(&o.image, "image", "", "canonical CI native helper image pinned by digest")
	fs.StringVar(&o.helperRevision, "helper-revision", "", "immutable helper source revision; defaults to the checkout video repository HEAD at install")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected CRL scheduler arguments")
	}
	if o.environment != "dev" && o.environment != "staging" && o.environment != "prod" {
		return errors.New("--environment must explicitly select dev, staging or prod")
	}
	store, err := newSecretStore("", o.environment)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	files, err := renderCRLScheduleNative(runtime.GOOS, home, store)
	if err != nil {
		return err
	}
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
		manager = "launchctl"
	}
	if _, err := exec.LookPath(manager); err != nil {
		return fmt.Errorf("CRL scheduler requires %s: %w", manager, err)
	}
	unit := "rtk-cloud-crl-" + o.environment + ".timer"
	label := "com.rtk-cloud.crl." + o.environment
	domain := "gui/" + strconv.Itoa(os.Getuid())
	plist := filepath.Join(home, "Library/LaunchAgents", label+".plist")
	command := func(args ...string) error {
		out, err := exec.Command(manager, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %w: %s", manager, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	installed := false
	for path := range files {
		if _, err := os.Stat(path); err == nil {
			installed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if action == "status" {
		if err := printCRLScheduleStatus(os.Stdout, store, installed, time.Now()); err != nil {
			return err
		}
		if !installed {
			return nil
		}
		var args []string
		if runtime.GOOS == "darwin" {
			args = []string{"print", domain + "/" + label}
		} else {
			args = []string{"--user", "status", unit, "--no-pager"}
		}
		cmd := exec.Command(manager, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("CRL scheduler status is unhealthy or unavailable: %w", err)
		}
		if runtime.GOOS == "linux" {
			// A successful oneshot service is normally inactive; status(service)
			// would incorrectly report failure. Show its recorded result instead.
			service := exec.Command(manager, "--user", "show", "rtk-cloud-crl-"+o.environment+".service", "--property=ActiveState,Result,ExecMainStatus,ExecMainExitTimestamp")
			service.Stdout, service.Stderr = os.Stdout, os.Stderr
			if err := service.Run(); err != nil {
				return fmt.Errorf("CRL renewal service status unavailable: %w", err)
			}
		}
		return nil
	}
	if action == "remove" {
		if !installed {
			return nil
		}
		if runtime.GOOS == "darwin" {
			err = command("bootout", domain, plist)
		} else {
			err = command("--user", "disable", "--now", unit)
		}
		if err != nil {
			return err
		}
		for path := range files {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if runtime.GOOS == "linux" {
			if err := command("--user", "daemon-reload"); err != nil {
				return err
			}
		}
		fmt.Println("CRL schedule removed; private renewal evidence and custody retained")
		return nil
	}
	if installed {
		return errors.New("CRL schedule files already exist; inspect status and remove the existing schedule before installing a replacement")
	}
	if !filepath.IsAbs(o.workspace) {
		return errors.New("install requires --workspace with an absolute stable checkout")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}$`).MatchString(o.image) {
		return errors.New("install requires --image pinned by a SHA-256 digest")
	}
	if o.helperRevision == "" {
		raw, err := exec.Command("git", "-C", filepath.Join(o.workspace, "repos/rtk_video_cloud"), "rev-parse", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("resolve helper source revision; provide --helper-revision explicitly: %w", err)
		}
		o.helperRevision = strings.TrimSpace(string(raw))
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(o.helperRevision) {
		return errors.New("--helper-revision must be a full 40-character Git revision")
	}
	cfg, err := resolveDeploymentConfig(o.workspace, o.environment, "")
	if err != nil {
		return err
	}
	if o.confirm == "" || o.confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return errors.New("install requires --confirm matching the selected environment's CLOUD_STACK_NAME")
	}
	facade := filepath.Join(o.workspace, "scripts/renew-environment-crls.sh")
	if info, err := os.Stat(facade); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("renewal facade is unavailable: %s", facade)
	}
	path, err := crlScheduleExecutablePath()
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(store.ConfigRoot); err != nil {
		return err
	}
	for _, relative := range []string{"", "deployment", "deployment/crl", "deployment/crl/schedule"} {
		p, err := store.safePath(relative)
		if relative == "" {
			p = store.Root
			err = nil
		}
		if err != nil {
			return err
		}
		if err := ensurePrivateDirectory(p); err != nil {
			return err
		}
	}
	if err := store.write("deployment/crl/schedule/run.sh", []byte(renderCRLScheduleRunner(o, store, path)), true); err != nil {
		return err
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		path, err := store.safePath("deployment/crl/schedule/" + name)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := store.write("deployment/crl/schedule/"+name, nil, false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("scheduler log must be a regular private file: %s", path)
		} else if err := os.Chmod(path, 0600); err != nil {
			return err
		}

	}
	record := map[string]string{"environment": o.environment, "workspace": o.workspace, "image": o.image, "helper_revision": o.helperRevision, "stack": o.confirm, "installed_at": time.Now().UTC().Format(time.RFC3339)}
	raw, _ := json.MarshalIndent(record, "", "  ")
	if err := store.write("deployment/crl/schedule/installation.json", raw, true); err != nil {
		return err
	}
	for path, data := range files {
		// OS scheduler definitions contain only paths and labels, never secret values.
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if runtime.GOOS == "darwin" {
		err = command("bootstrap", domain, plist)
	} else {
		err = command("--user", "daemon-reload")
		if err == nil {
			err = command("--user", "enable", "--now", unit)
		}
	}
	if err != nil {
		return fmt.Errorf("schedule files retained for diagnosis; activation failed: %w", err)
	}
	fmt.Printf("CRL schedule installed for %s; hourly renewal requires this host, checkout, Docker and cluster access to remain available\n", o.environment)
	return nil
}

func crlScheduleExecutablePath() (string, error) {
	paths := []string{}
	for _, tool := range []string{"go", "kubectl", "docker", "git", "gh"} {
		binary, err := exec.LookPath(tool)
		if err != nil {
			return "", fmt.Errorf("CRL scheduler requires %s on the installer PATH: %w", tool, err)
		}
		absolute, err := filepath.Abs(binary)
		if err != nil {
			return "", err
		}
		paths = append(paths, filepath.Dir(absolute))
	}
	paths = append(paths, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	return strings.Join(paths, ":"), nil
}

func renderCRLScheduleRunner(o crlScheduleOptions, store secretStore, path string) string {
	dir := filepath.Join(store.Root, "deployment/crl/schedule")
	// The engine owns the per-environment lock. Both schedulers serialize this
	// one service, and manual runs enter the same engine lock.
	return "#!/bin/bash\nset -u\numask 077\nexport PATH=" + shellSingleQuote(path) + "\nexport RTK_CLOUD_CONFIG_ROOT=" + shellSingleQuote(store.ConfigRoot) + "\nstate=" + shellSingleQuote(dir) + "\n" + `exec >>"$state/stdout.log" 2>>"$state/stderr.log"
finish() {
  code=$?
  trap - EXIT
  finished_at=$(/bin/date -u +%Y-%m-%dT%H:%M:%SZ)
  printf '{"finished_at":"%s","exit_code":%d}\n' "$finished_at" "$code" > "$state/last-result.json.tmp.$$"
  /bin/mv "$state/last-result.json.tmp.$$" "$state/last-result.json"
  if [ "$code" -eq 0 ]; then
    /bin/cp "$state/last-result.json" "$state/last-success.json.tmp.$$"
    /bin/mv "$state/last-success.json.tmp.$$" "$state/last-success.json"
  fi
  printf '%s CRL renewal exit_code=%s\n' "$finished_at" "$code"
  exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
/bin/date -u +%Y-%m-%dT%H:%M:%SZ > "$state/last-started.txt"
` + "/bin/bash " + shellSingleQuote(filepath.Join(o.workspace, "scripts/renew-environment-crls.sh")) + " --environment " + shellSingleQuote(o.environment) + " --execute --recover --confirm " + shellSingleQuote(o.confirm) + " --image " + shellSingleQuote(o.image) + " --helper-revision " + shellSingleQuote(o.helperRevision) + "\n"
}

func renderCRLScheduleNative(platform, home string, store secretStore) (map[string]string, error) {
	dir := filepath.Join(store.Root, "deployment/crl/schedule")
	runner := filepath.Join(dir, "run.sh")
	files := map[string]string{}
	if platform == "darwin" {
		escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
		label := "com.rtk-cloud.crl." + store.Environment
		files[filepath.Join(home, "Library/LaunchAgents", label+".plist")] = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + escape(label) + `</string>
<key>ProgramArguments</key><array><string>/bin/bash</string><string>` + escape(runner) + `</string></array>
<key>StartInterval</key><integer>3600</integer><key>RunAtLoad</key><true/>
<key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>` + escape(filepath.Join(dir, "stdout.log")) + `</string>
<key>StandardErrorPath</key><string>` + escape(filepath.Join(dir, "stderr.log")) + `</string>
</dict></plist>
`
	} else if platform == "linux" {
		// systemd expands % specifiers and $ variables even inside double quotes.
		quote := func(s string) string {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$", "\n", `\n`, "\r", `\r`).Replace(s) + `"`
		}
		name := "rtk-cloud-crl-" + store.Environment
		base := filepath.Join(home, ".config/systemd/user")
		files[filepath.Join(base, name+".service")] = "[Unit]\nDescription=RTK Cloud " + store.Environment + " CRL renewal\n[Service]\nType=oneshot\nUMask=0077\nTimeoutStartSec=infinity\nExecStart=/bin/bash " + quote(runner) + "\n"
		files[filepath.Join(base, name+".timer")] = "[Unit]\nDescription=Hourly RTK Cloud " + store.Environment + " CRL renewal\n[Timer]\nOnCalendar=hourly\nOnBootSec=1min\nPersistent=true\nUnit=" + name + ".service\n[Install]\nWantedBy=timers.target\n"
	} else {
		return nil, fmt.Errorf("CRL scheduling supports Darwin launchd or Linux systemd --user, not %s", platform)
	}
	return files, nil
}

func printCRLScheduleStatus(out io.Writer, store secretStore, installed bool, now time.Time) error {
	dir := filepath.Join(store.Root, "deployment/crl/schedule")
	resultPath := filepath.Join(dir, "last-result.json")
	fmt.Fprintf(out, "environment=%s installed=%t\nlast_result=%s\nlast_success=%s\nlast_started=%s\nlogs=%s\nmaintenance_status=%s\n", store.Environment, installed, resultPath, filepath.Join(dir, "last-success.json"), filepath.Join(dir, "last-started.txt"), dir, filepath.Join(store.Root, "deployment/crl-maintenance/status.json"))
	enginePath, err := store.safePath("deployment/crl-maintenance/status.json")
	if err != nil {
		return err
	}
	engineRaw, err := os.ReadFile(enginePath)
	if err == nil {
		var status crlMaintenanceStatus
		if err := json.Unmarshal(engineRaw, &status); err != nil {
			return fmt.Errorf("invalid maintenance status: %w", err)
		}
		if status.Environment != store.Environment {
			return errors.New("maintenance status environment mismatch")
		}
		fmt.Fprintf(out, "next_expiry=%s last_maintenance_success=%s maintenance_failure=%q\n", status.NextExpiry.Format(time.RFC3339), status.LastSuccess.Format(time.RFC3339), status.Failure)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(resultPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "last_run=never stale=true")
		return nil
	}
	if err != nil {
		return err
	}
	var result crlScheduleResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("invalid scheduler last result: %w", err)
	}
	if result.FinishedAt.IsZero() {
		return errors.New("scheduler last result has no completion timestamp")
	}
	fmt.Fprintf(out, "last_finished=%s last_exit_code=%d stale=%t\n", result.FinishedAt.Format(time.RFC3339), result.ExitCode, now.Sub(result.FinishedAt) > 2*time.Hour || result.FinishedAt.After(now))
	return nil
}
