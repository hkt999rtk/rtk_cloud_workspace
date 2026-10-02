package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	m "rtk-cloud-workspace/scripts/go/internal/cloudmonitor"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cloud-monitor validate|check|watch|report --environment dev --config monitor.json")
		return 2
	}
	command := args[0]
	if command != "validate" && command != "check" && command != "watch" && command != "report" {
		fmt.Fprintln(os.Stderr, "unknown command")
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment (required)")
	config := fs.String("config", "", "monitor JSON (required)")
	root := fs.String("config-root", "", "SecretStore base root")
	workspace := fs.String("workspace", ".", "workspace root")
	out := fs.String("out-dir", "output/cloud-monitor", "private history/report output")
	synthetic := fs.Bool("enable-synthetic", false, "enable dedicated authentication/MQTT/TURN probes")
	duration := fs.Duration("duration", 0, "optional watch duration")
	skipReport := fs.Bool("skip-report", false, "check: store JSON without rendering PDF")
	fromArg := fs.String("from", "", "report start RFC3339")
	toArg := fs.String("to", "", "report end RFC3339")
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *env == "" || *config == "" {
		fmt.Fprintln(os.Stderr, "explicit --environment and --config are required")
		return 2
	}
	if *duration < 0 {
		fmt.Fprintln(os.Stderr, "--duration must be positive")
		return 2
	}
	cfg, inv, e := m.LoadConfig(*config, *env)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	rt, e := m.ResolveRuntime(*env, *root, *workspace, *out)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	if command == "validate" {
		fmt.Printf("valid environment=%s expected_targets=%d source=%s\n", cfg.Environment, len(inv.Targets), inv.SourceFingerprint)
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *duration > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, *duration)
		defer stop()
	}
	runner := m.OSRunner{}
	summary := func(s m.Snapshot) {
		fmt.Printf("%s environment=%s overall=%s coverage=%.1f%% required=%d unknown=%d not_run=%d\n", s.StartedAt.Format(time.RFC3339), s.Environment, s.Overall, s.Coverage.Percent, s.Coverage.Required, s.Coverage.Unknown, s.Coverage.NotRun)
	}
	switch command {
	case "check":
		s, e := m.Check(ctx, cfg, inv, rt, runner, *synthetic)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 2
		}
		summary(s)
		if !*skipReport {
			html, pdf, e := m.GenerateReport(ctx, cfg, rt, runner, s.StartedAt.Add(-time.Second), time.Now().Add(time.Second), "check-"+s.RunID)
			if e != nil {
				fmt.Fprintln(os.Stderr, "report failed:", e)
				return 3
			}
			fmt.Printf("HTML=%s\nPDF=%s\n", html, pdf)
		}
		if s.Overall == m.Pass {
			return 0
		}
		return 1
	case "watch":
		e = m.Watch(ctx, cfg, inv, rt, runner, *synthetic, summary, func(e error) {
			if e != nil {
				fmt.Fprintln(os.Stderr, "daily report failed; sampling continues")
			}
		})
	case "report":
		to := time.Now().UTC()
		from := to.Add(-24 * time.Hour)
		if *toArg != "" {
			to, e = time.Parse(time.RFC3339, *toArg)
			if e != nil {
				fmt.Fprintln(os.Stderr, "invalid --to")
				return 2
			}
		}
		if *fromArg == "" {
			from = to.Add(-24 * time.Hour)
		}
		if *fromArg != "" {
			from, e = time.Parse(time.RFC3339, *fromArg)
			if e != nil {
				fmt.Fprintln(os.Stderr, "invalid --from")
				return 2
			}
		}
		if !to.After(from) {
			fmt.Fprintln(os.Stderr, "--to must follow --from")
			return 2
		}
		var html, pdf string
		html, pdf, e = m.GenerateReport(ctx, cfg, rt, runner, from, to, "")
		if e == nil {
			fmt.Printf("HTML=%s\nPDF=%s\n", html, pdf)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	return 0
}
