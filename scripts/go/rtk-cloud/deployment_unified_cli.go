package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type unifiedDeploymentOptions struct {
	Action, Environment, EnvironmentRoot, Workspace, Release, Operation, Confirm, PlanSHA256, Report string
	Fast                                                                                             bool
	Timeout                                                                                          time.Duration
}
type unifiedDeploymentDependencies struct {
	Resolve func(string, string, string) (deploymentConfig, error)
	Compile func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error)
	Checks  func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck
	Execute func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error
	Accept  func(context.Context, *compiledEnvironmentDeployment) error
	Guard   func([]string) error
}

func defaultUnifiedDeploymentDependencies() unifiedDeploymentDependencies {
	return unifiedDeploymentDependencies{
		Resolve: resolveDeploymentConfig, Compile: compileEnvironmentDeployment,
		Guard: recoveryMutationGuard,
		Checks: func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
			return map[string]deploymentplan.GateCheck{
				"deployment.target": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
				},
				"deployment.execution": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Backend.CheckExecutionPrerequisites(ctx, p, c.Store.Root); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := p.Admit(ctx, c.Backend); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.execution", Cause: "execution lease prerequisites and exact server admission passed; complete provider capacity, scheduling and storage qualification is not implemented", Next: "complete actual execution capability checks for the selected plan; server dry-run alone cannot authorize a full update"}
				},
				"deployment.release": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := verifyEnvironmentRelease(ctx, c.Configuration, c.Release); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest(c.Release)}, nil
				},
			}
		},
		Execute: func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, qualify func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
			return c.Plan.Execute(ctx, c.Backend, sha, c.Store.Root, qualify, c.Fence, func(ctx context.Context, _ *deploymentplan.Plan) error { return accept(ctx, c) })
		},
	}
}

// Missing runtime adapters remain required BLOCKED gates. Neither a private
// schema-1 file nor the former partial provider checks can turn them into GO.
func runUnifiedEnvironmentDeployment(action string, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runUnifiedEnvironmentDeploymentWithDependencies(ctx, action, args, os.Stdout, os.Stderr, defaultUnifiedDeploymentDependencies())
}

func parseUnifiedDeploymentOptions(action string, args []string, out io.Writer) (unifiedDeploymentOptions, error) {
	o := unifiedDeploymentOptions{Action: action, Operation: "upgrade", Timeout: 10 * time.Minute}
	if action == "create" {
		o.Operation = "create"
	}
	fs := flag.NewFlagSet("deployment "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.Environment, "environment", "", "selected maintained environment")
	fs.StringVar(&o.EnvironmentRoot, "environment-root", "", "explicit maintained environment root")
	fs.StringVar(&o.Workspace, "workspace", "", "workspace root")
	fs.StringVar(&o.Release, "release", "", "immutable formal release manifest; defaults to the environment SecretStore deployment/release.json")
	fs.StringVar(&o.Operation, "operation", o.Operation, "create or upgrade")
	fs.StringVar(&o.Confirm, "confirm", "", "exact environment stack confirmation for execution")
	fs.StringVar(&o.PlanSHA256, "plan-sha256", "", "exact reviewed plan SHA-256; required for execution")
	fs.StringVar(&o.Report, "report", "", "sanitized JSON report (0600)")
	fs.BoolVar(&o.Fast, "fast", false, "reduced evidence; always NO-GO for complete updates")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "complete planning and qualification deadline")
	var readOnly bool
	fs.BoolVar(&readOnly, "read-only", false, "preflight is always read-only")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	readOnlyVisited := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "read-only" {
			readOnlyVisited = true
		}
	})
	if readOnlyVisited && (action != "preflight" || !readOnly) {
		return o, errors.New("--read-only applies only to preflight and cannot be false")
	}
	if fs.NArg() != 0 || o.Environment == "" || !secretEnvironmentPattern.MatchString(o.Environment) || o.Timeout <= 0 {
		return o, errors.New("a valid --environment, positive --timeout and named flags are required")
	}
	if o.Operation == "full-deployment" {
		o.Operation = "upgrade"
	}
	if o.Operation != "create" && o.Operation != "upgrade" {
		return o, errors.New("--operation must be create or upgrade")
	}
	if action == "create" && o.Operation != "create" || action == "upgrade" && o.Operation != "upgrade" {
		return o, errors.New("operation conflicts with the selected deployment action")
	}
	if o.Fast && action != "preflight" {
		return o, errors.New("--fast is only accepted by preflight and cannot authorize execution")
	}
	if (action == "create" || action == "upgrade") && (o.PlanSHA256 == "" || len(o.PlanSHA256) != 64 || strings.Trim(o.PlanSHA256, "0123456789abcdef") != "") {
		return o, errors.New("execution requires the exact reviewed --plan-sha256 from full preflight")
	}
	return o, nil
}

type unifiedDeploymentReport struct {
	Schema      string                      `json:"schema"`
	Environment string                      `json:"environment"`
	Operation   string                      `json:"operation"`
	Decision    string                      `json:"decision"`
	Plan        *deploymentplan.Summary     `json:"plan,omitempty"`
	Gates       []deploymentplan.GateResult `json:"gates,omitempty"`
	Completed   bool                        `json:"completed"`
}

func runUnifiedEnvironmentDeploymentWithDependencies(parent context.Context, action string, args []string, out, diagnostics io.Writer, deps unifiedDeploymentDependencies) error {
	o, err := parseUnifiedDeploymentOptions(action, args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "error:", err)
		return exitCode(2)
	}
	cfg, err := deps.Resolve(o.Workspace, o.Environment, o.EnvironmentRoot)
	if err != nil {
		fmt.Fprintln(diagnostics, "error: maintained deployment configuration is unavailable")
		return exitCode(2)
	}
	if o.Report != "" {
		if err := validateEnvironmentDeploymentReportPath(cfg.Environment, o.Report); err != nil {
			fmt.Fprintln(diagnostics, "error: --report must select a private JSON file under ~/.config/rtk_cloud/<environment>/deployment/reports")
			return exitCode(2)
		}
	}
	if action == "create" || action == "upgrade" {
		if o.Confirm != cfg.Values["CLOUD_STACK_NAME"] {
			fmt.Fprintf(diagnostics, "error: --confirm %s is required\n", cfg.Values["CLOUD_STACK_NAME"])
			return exitCode(2)
		}
	}
	if deps.Guard != nil {
		if err := deps.Guard(append([]string{"deployment", action}, args...)); err != nil {
			fmt.Fprintln(diagnostics, "error: environment recovery/maintenance fence blocks this operation")
			return exitCode(1)
		}
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	report := unifiedDeploymentReport{Schema: "rtk-cloud.deployment-report/v2", Environment: cfg.Environment, Operation: o.Operation, Decision: "NO-GO"}
	c, err := deps.Compile(ctx, cfg, o.Operation, o.Release)
	if err != nil {
		// Never print compiler/provider output: it can contain private payloads.
		row := deploymentplan.GateResult{ID: "deployment.target", Status: "BLOCKED", Required: true, Cause: "complete target plan could not be compiled", Next: "select the exact formal release and repair maintained configuration, managed identity bindings, ownership and migration prerequisites", Impact: "the complete environment update has no executable reviewed plan", Recheck: "repeat full preflight for the same release"}
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
		}
		report.Gates = []deploymentplan.GateResult{row}
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, exitCode(1))
	}
	summary := c.Plan.Summary()
	report.Plan = &summary
	if action == "plan" {
		report.Decision = "PLANNED"
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, nil)
	}
	checks := deps.Checks(c)
	if deps.Accept == nil {
		// Completion verification is an execution prerequisite, even if every
		// other adapter is installed by a future capability implementation.
		check := checks["deployment.execution"]
		checks["deployment.execution"] = func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if check != nil {
				if _, err := check(ctx, p); err != nil {
					return deploymentplan.GateEvidence{}, err
				}
			}
			return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.execution", Cause: "complete post-deploy acceptance is unavailable", Next: "implement and qualify actual workload, PKI, endpoint and business-flow acceptance before execution"}
		}
	}
	options := deploymentplan.QualificationOptions{Fast: o.Fast, Production: cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] == "production"}
	qualification, err := c.Plan.Qualify(ctx, options, checks)
	report.Decision, report.Gates = qualification.Decision, qualification.Gates
	if err != nil {
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, exitCode(1))
	}
	if action == "create" || action == "upgrade" {
		if deps.Accept == nil {
			report.Decision = "NO-GO"
			report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.post-deploy", Status: "BLOCKED", Required: true, Cause: "complete post-deploy acceptance adapter is unavailable", Next: "implement and qualify actual workload, PKI, endpoint and business-flow acceptance before execution", Impact: "deployment completion cannot be verified", Recheck: "repeat complete qualification for the selected release"})
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, exitCode(1))
		}
		if o.PlanSHA256 != c.Plan.SHA256() {
			report.Decision = "NO-GO"
			report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.plan", Status: "FAIL", Required: true, Cause: "reviewed plan SHA-256 differs from the freshly compiled plan", Next: "repeat preflight and review the new plan", Impact: "the previous approval does not apply", Recheck: "use the newly approved --plan-sha256"})
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, exitCode(1))
		}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			_, err := p.Qualify(ctx, options, checks)
			return err
		}
		if err := deps.Execute(ctx, c, o.PlanSHA256, qualify, deps.Accept); err != nil {
			report.Decision = "NO-GO"
			row := deploymentplan.GateResult{ID: "deployment.execution", Status: "FAIL", Required: true, Cause: "execution stopped; private completed-resource and recovery evidence retained", Next: "inspect the environment-local journal and requalify the remaining plan", Impact: "deployment has not completed", Recheck: "verify actual state, PKI, public endpoints and representative business flows"}
			var blocker *deploymentplan.Blocker
			if errors.As(err, &blocker) {
				row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
			}
			report.Gates = append(report.Gates, row)
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, exitCode(1))
		}

		report.Completed = true
	}
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, nil)
}

func emitUnifiedDeploymentReport(out, diagnostics io.Writer, path string, report unifiedDeploymentReport, result error) error {
	if report.Plan != nil {
		fmt.Fprintf(out, "plan-sha256: %s\n", report.Plan.PlanSHA256)
	}
	for _, row := range report.Gates {
		fmt.Fprintf(out, "[%s] %s\n", row.Status, row.ID)
		if row.Status != "PASS" {
			fmt.Fprintf(out, "  cause: %s\n  next: %s\n  impact: %s\n  recheck: %s\n", row.Cause, row.Next, row.Impact, row.Recheck)
		}
	}
	fmt.Fprintf(out, "%s environment=%s operation=%s completed=%t\n", report.Decision, report.Environment, report.Operation, report.Completed)
	if path != "" {
		if err := writeUnifiedDeploymentReport(path, report); err != nil {
			fmt.Fprintln(diagnostics, "error: cannot publish sanitized deployment report")
			return exitCode(1)
		}
	}
	return result
}

func writeUnifiedDeploymentReport(path string, report unifiedDeploymentReport) error {
	if err := validateEnvironmentDeploymentReportPath(report.Environment, path); err != nil {
		return err
	}
	store, err := newSecretStore("", report.Environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	// Reuse the existing atomic, private report publisher rather than exposing
	// desired Secrets or baseline state through an additional file writer.
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return writePrivateDeploymentReportJSON(path, raw)
}

func validateEnvironmentDeploymentReportPath(environment, path string) error {
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".json" || filepath.Dir(path) != filepath.Join(store.Root, "deployment", "reports") {
		return errors.New("deployment report destination is outside canonical environment reports")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return errors.New("deployment report directories must be real private 0700 directories")
		}
	}
	return nil
}
