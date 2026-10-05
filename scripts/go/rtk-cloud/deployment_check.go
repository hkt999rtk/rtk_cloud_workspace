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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This facade is deliberately separate from credentials-check: its existing
// callers include local-only qualification and provision/bootstrap operations.
type deploymentCheckOptions struct {
	environment, environmentRoot, workspace, report string
	phase                                           string
	timeout                                         time.Duration
	requireMigration, requireProduct                bool
	qualification                                   deploymentCredentialCheckOptions
}

type deploymentCheckDependencies struct {
	guard                               func([]string) error
	resolve                             func(string, string, string) (deploymentConfig, error)
	store                               func(string) (secretStore, error)
	preflight                           func(context.Context, deploymentConfig, io.Writer) error
	local, bindings, migration, product func(secretStore) error
	runtime                             func(secretStore, time.Time) error
	plan                                func(context.Context, deploymentConfig, string, deploymentCredentialCheckOptions, func(deploymentCredentialCheck))
	collect                             func(context.Context, deploymentConfig, string, deploymentCredentialCheckOptions, bool, func(deploymentCredentialCheck)) []deploymentCredentialCheck
}

func defaultDeploymentCheckDependencies() deploymentCheckDependencies {
	return deploymentCheckDependencies{
		guard:     recoveryMutationGuard,
		resolve:   resolveDeploymentConfig,
		store:     func(environment string) (secretStore, error) { return newSecretStore("", environment) },
		preflight: defaultDeploymentCheckPreflight,
		local:     verifySecretStoreContents, bindings: verifySecretStoreK8SBindings,
		runtime: verifySecretStoreK8SRuntime, migration: verifyPKIMigrationDatabaseSecret,
		product: verifyProductPKIReadiness,
		plan: func(ctx context.Context, cfg deploymentConfig, path string, options deploymentCredentialCheckOptions, emit func(deploymentCredentialCheck)) {
			options.planOnly = true
			defaultDeploymentCredentialChecker().collectDeploymentChecks(ctx, cfg, path, options, false, emit)
		},
		collect: defaultDeploymentCredentialChecker().collectDeploymentChecks,
	}
}

func parseDeploymentCheckOptions(args []string, out io.Writer) (deploymentCheckOptions, error) {
	var o deploymentCheckOptions
	var selected, retiredEnv, retiredShared string
	fs := flag.NewFlagSet("deployment check", flag.ContinueOnError)
	fs.SetOutput(out)
	o.phase = deploymentCheckPostDeploy
	var selectedPhase string
	fs.Func("phase", "pre-deploy (can deploy) or post-deploy (existing runtime health; default)", func(v string) error {
		if v != deploymentCheckPreDeploy && v != deploymentCheckPostDeploy {
			return errors.New("--phase must be pre-deploy or post-deploy")
		}
		if selectedPhase != "" && selectedPhase != v {
			return errors.New("conflicting --phase values are not allowed")
		}
		selectedPhase, o.phase = v, v
		return nil
	})
	fs.StringVar(&o.environment, "environment", "", "selected tracked environment name")
	fs.StringVar(&o.environmentRoot, "environment-root", "", "explicit environment root")
	fs.StringVar(&o.workspace, "workspace", "", "workspace root")
	fs.StringVar(&o.report, "report", "", "write a sanitized JSON report (0600)")
	fs.DurationVar(&o.timeout, "timeout", 0, "overall deadline (default: fast 2m, standard 10m)")
	fs.BoolVar(&o.requireMigration, "require-pki-migration", false, "require PKI migration-owner binding")
	fs.BoolVar(&o.requireProduct, "require-product-pki", false, "require active pinned Product Device Root")
	q := &o.qualification
	fs.BoolVar(&q.fast, "fast", false, "image metadata only; retains the selected phase checks (does not select a phase)")
	fs.BoolVar(&q.readOnly, "read-only", false, "suppress DNS/storage writes and receipts; --image still pulls")
	fs.StringVar(&selected, "checks", "", "linode,ghcr,dns,storage,tls,mounts; selected phase prerequisites always apply")
	fs.Func("image", "repeatable GHCR digest-pinned image for linux/amd64", func(v string) error { q.images = append(q.images, v); return nil })
	fs.Func("manifest", "repeatable complete rendered workload JSON", func(v string) error { q.manifests = append(q.manifests, v); return nil })
	fs.StringVar(&q.tls.cert, "tls-cert", "", "PEM certificate chain")
	fs.StringVar(&q.tls.key, "tls-key", "", "private key file")
	fs.StringVar(&q.tls.ca, "tls-ca", "", "trusted CA bundle")
	fs.StringVar(&q.tls.hostname, "tls-name", "", "actual server DNS name")
	fs.StringVar(&q.tls.purpose, "tls-purpose", "server", "server or client")
	fs.IntVar(&q.tls.minDays, "min-valid-days", 7, "required certificate lifetime")
	fs.BoolVar(&q.createMissingObjectStorageBucket, "create-missing-object-storage-bucket", false, "legacy synchronous bucket repair")
	fs.BoolVar(&q.grantObjectStorageBucketAccess, "grant-object-storage-bucket-access", false, "legacy synchronous credential repair")
	fs.StringVar(&retiredEnv, "env-file", "", "retired; use the selected environment SecretStore")
	fs.StringVar(&retiredShared, "shared-env-file", "", "retired; shared credentials are unsupported")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("unexpected positional arguments; use --flag=value for boolean values")
	}
	if o.environment == "" || !secretEnvironmentPattern.MatchString(o.environment) {
		return o, errors.New("--environment must identify an existing environment")
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["env-file"] || visited["shared-env-file"] {
		return o, errors.New("--env-file and --shared-env-file are retired; use the selected environment SecretStore")
	}
	if visited["timeout"] && o.timeout <= 0 {
		return o, errors.New("--timeout must be positive")
	}
	if q.fast && visited["read-only"] && !q.readOnly {
		return o, errors.New("--fast conflicts with --read-only=false")
	}
	if o.phase == deploymentCheckPreDeploy {
		if visited["read-only"] && !q.readOnly {
			return o, errors.New("pre-deploy checks are read-only; --read-only=false is not allowed")
		}
		if q.createMissingObjectStorageBucket || q.grantObjectStorageBucketAccess {
			return o, errors.New("credential repair flags are not allowed during pre-deploy checks")
		}
		if o.requireMigration || o.requireProduct {
			return o, errors.New("--require-pki-migration and --require-product-pki are post-deploy health checks")
		}
	}
	if q.fast || o.phase == deploymentCheckPreDeploy {
		q.readOnly = true
	}
	if o.timeout == 0 {
		o.timeout = 10 * time.Minute
		if q.fast {
			o.timeout = 2 * time.Minute
		}
	}
	if q.createMissingObjectStorageBucket && q.grantObjectStorageBucketAccess {
		return o, errors.New("bucket creation and credential replacement must run separately")
	}
	if q.createMissingObjectStorageBucket || q.grantObjectStorageBucketAccess {
		for _, name := range []string{"fast", "read-only", "checks", "image", "manifest", "tls-cert", "tls-key", "tls-ca", "tls-name", "tls-purpose", "min-valid-days"} {
			if visited[name] {
				return o, errors.New("run credential repairs separately from scoped/read-only qualification")
			}
		}
	}
	if q.tls.cert == "" && (visited["tls-purpose"] || visited["min-valid-days"]) {
		return o, errors.New("TLS purpose/lifetime flags require --tls-cert, --tls-key and --tls-ca")
	}
	if err := q.configureChecks(selected); err != nil {
		return o, err
	}
	// Bad local inputs must not spend time contacting external systems.
	for _, path := range append(append([]string{}, q.manifests...), q.tls.cert, q.tls.key, q.tls.ca) {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return o, errors.New("qualification input must be an existing regular file")
		}
	}
	if q.tls.cert != "" {
		if check := checkRolloutTLS(q.tls); !check.Passed {
			return o, fmt.Errorf("TLS input qualification failed: %s", check.Detail)
		}
	}
	for _, path := range q.manifests {
		if check := checkRolloutMounts(path); !check.Passed {
			return o, fmt.Errorf("manifest input qualification failed: %s", check.Detail)
		}
	}
	if o.report != "" {
		info, err := os.Stat(filepath.Dir(o.report))
		if err != nil || !info.IsDir() {
			return o, errors.New("--report parent directory must already exist")
		}
		if info, err := os.Lstat(o.report); err == nil && !info.Mode().IsRegular() {
			return o, errors.New("--report must name a regular file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return o, errors.New("--report destination cannot be inspected")
		}
	}
	return o, nil
}

type deploymentCheckResult struct {
	ID            string   `json:"check_id"`
	Status        string   `json:"status"`
	Code          string   `json:"code"`
	Resource      string   `json:"resource"`
	Required      bool     `json:"required"`
	DurationMS    int64    `json:"duration_ms"`
	Attempts      int      `json:"attempts"`
	DependsOn     []string `json:"depends_on"`
	Message       string   `json:"message"`
	NextAction    string   `json:"next_action"`
	EvidenceTime  string   `json:"evidence_time"`
	EvidenceLevel string   `json:"evidence_level,omitempty"`
	Reused        bool     `json:"reused,omitempty"`
}

type deploymentCheckReport struct {
	SchemaVersion int                     `json:"schema_version"`
	Environment   string                  `json:"environment"`
	Phase         string                  `json:"phase"`
	Mode          string                  `json:"mode"`
	ReadOnly      bool                    `json:"read_only"`
	Scope         string                  `json:"scope"`
	Overall       string                  `json:"overall"`
	StartedAt     string                  `json:"started_at"`
	DurationMS    int64                   `json:"duration_ms"`
	Coverage      map[string]int          `json:"coverage"`
	Checks        []deploymentCheckResult `json:"checks"`
}

type deploymentCheckReporter struct {
	mu      sync.Mutex
	out     io.Writer
	results map[string]deploymentCheckResult
	running map[string]time.Time
	started time.Time
}

func newDeploymentCheckReporter(out io.Writer) *deploymentCheckReporter {
	return &deploymentCheckReporter{out: out, results: map[string]deploymentCheckResult{}, running: map[string]time.Time{}, started: time.Now()}
}

func (r *deploymentCheckReporter) emit(c deploymentCredentialCheck) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := c.ID
	if id == "" {
		id = c.Name
	}
	status := c.Status
	if status == "" {
		status = "FAIL"
		if c.Passed {
			status = "PASS"
		}
	}
	prior := r.results[id]
	if status == "PENDING" {
		if prior.ID != "" {
			return
		}
		r.results[id] = deploymentCheckResult{ID: id, Status: status, Resource: c.Resource, Required: c.Required, DependsOn: c.DependsOn}
		fmt.Fprintf(r.out, "[PLAN] %s required=%t\n", id, c.Required)
		return
	}
	if status == "RUNNING" {
		r.running[id] = time.Now()
		prior.ID, prior.Status = id, status
		r.results[id] = prior
		fmt.Fprintf(r.out, "[START] %s\n", id)
		return
	}
	if started, ok := r.running[id]; ok && c.DurationMS == 0 {
		c.DurationMS = time.Since(started).Milliseconds()
	}
	delete(r.running, id)
	if c.EvidenceTime == "" {
		c.EvidenceTime = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if c.DependsOn == nil {
		c.DependsOn = prior.DependsOn
	}
	if c.DependsOn == nil {
		c.DependsOn = []string{}
	}
	if c.Resource == "" {
		c.Resource = prior.Resource
	}
	if c.Code == "" {
		c.Code = map[string]string{"PASS": "VERIFIED", "FAIL": "CHECK_FAILED", "ERROR": "CHECK_ERROR", "BLOCKED": "DEPENDENCY_FAILED", "SKIPPED": "NOT_SELECTED"}[status]
	}
	c.Required = c.Required || prior.Required
	r.results[id] = deploymentCheckResult{ID: id, Status: status, Code: c.Code, Resource: c.Resource, Required: c.Required, DurationMS: c.DurationMS, Attempts: c.Attempts, DependsOn: c.DependsOn, Message: c.Detail, NextAction: c.NextAction, EvidenceTime: c.EvidenceTime, EvidenceLevel: c.EvidenceLevel, Reused: c.Reused}
	fmt.Fprintf(r.out, "[%s] %s duration=%dms attempts=%d code=%s: %s\n", status, id, c.DurationMS, c.Attempts, c.Code, c.Detail)
	if c.NextAction != "" {
		fmt.Fprintf(r.out, "  next: %s\n", c.NextAction)
	}
}

func (r *deploymentCheckReporter) heartbeat(ctx context.Context, done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			r.mu.Lock()
			ids := make([]string, 0, len(r.running))
			for id := range r.running {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			remaining := time.Duration(0)
			if deadline, ok := ctx.Deadline(); ok {
				remaining = time.Until(deadline)
				if remaining < 0 {
					remaining = 0
				}
			}
			fmt.Fprintf(r.out, "[PROGRESS] elapsed=%s remaining=%s running=%s\n", time.Since(r.started).Round(time.Second), remaining.Round(time.Second), strings.Join(ids, ","))
			r.mu.Unlock()
		}
	}
}

func (r *deploymentCheckReporter) snapshot(environment, phase string, fast, readOnly bool) deploymentCheckReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	report := deploymentCheckReport{SchemaVersion: 1, Environment: environment, Phase: phase, Mode: "standard", ReadOnly: readOnly, Scope: deploymentCheckScope(phase), Overall: "PASS", StartedAt: r.started.UTC().Format(time.RFC3339Nano), DurationMS: time.Since(r.started).Milliseconds(), Coverage: map[string]int{"required": 0, "PASS": 0, "FAIL": 0, "ERROR": 0, "BLOCKED": 0, "SKIPPED": 0}}
	if fast {
		report.Mode = "fast"
		report.Scope += "; fast: writes and full image pulls unverified"
	} else if readOnly && phase == deploymentCheckPostDeploy {
		report.Scope += "; read-only: write permissions unverified"
	}
	for _, v := range r.results {
		if v.Status == "PENDING" || v.Status == "RUNNING" {
			v.Status, v.Code, v.Message = "BLOCKED", "CHECK_INCOMPLETE", "check did not complete"
			v.NextAction = "Resolve interrupted prerequisites and rerun the check."
			v.EvidenceTime = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if v.DependsOn == nil {
			v.DependsOn = []string{}
		}
		if v.Status == "FAIL" || v.Status == "ERROR" {
			report.Overall = "FAIL"
		}
		if v.Required {
			report.Coverage["required"]++
			if v.Status != "PASS" {
				report.Overall = "FAIL"
			}
		}
		report.Coverage[v.Status]++
		report.Checks = append(report.Checks, v)
	}
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].ID < report.Checks[j].ID })
	return report
}

func deploymentCheckFailure(id string, err error) deploymentCredentialCheck {
	c := deploymentCredentialCheck{ID: id, Name: id, Required: true, Status: "FAIL", Code: "CHECK_FAILED", Detail: err.Error(), NextAction: "Resolve the reported check before repeating qualification.", Attempts: 1}
	var coded interface{ CheckCode() string }
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		c.Status, c.Code, c.Detail = "ERROR", "CHECK_TIMEOUT", "check exceeded its time budget"
	case errors.Is(err, context.Canceled):
		c.Status, c.Code, c.Detail = "ERROR", "CHECK_CANCELED", "check was canceled"
	case errors.As(err, &coded):
		c.Status, c.Code = "ERROR", coded.CheckCode()
	}
	switch c.Code {
	case "KUBE_FORBIDDEN":
		c.NextAction = "Grant the selected Kubernetes identity permission to read the required resources."
	case "KUBE_UNAUTHORIZED":
		c.NextAction = "Refresh the selected environment's Kubernetes authentication."
	case "KUBE_TOOL_MISSING":
		c.NextAction = "Install kubectl or correct the configured executable path."
	case "KUBE_INVENTORY_EMPTY", "KUBE_NOT_FOUND":
		c.NextAction = "Confirm the selected cluster and namespaces, then restore the required existing resources."
	case "KUBE_INVALID_RESPONSE":
		c.NextAction = "Check the Kubernetes API response and kubectl compatibility."
	case "KUBE_TIMEOUT", "KUBE_UNAVAILABLE", "KUBE_RATE_LIMITED":
		c.NextAction = "Check cluster connectivity and API availability before rerunning."
	case "SQL_TIMEOUT":
		c.NextAction = "Inspect database availability and the slow read before rerunning."
	case "CHECK_TIMEOUT":
		c.NextAction = "Inspect the slowest checks; use --timeout to adjust the overall budget when needed."
	case "CHECK_CANCELED", "CHECK_CANCELLED":
		c.NextAction = "Rerun the canceled check to obtain complete evidence."
	}
	return c
}

func verifyDeploymentCheckLegacyPaths(store secretStore, workspace string) error {
	root := filepath.Join(workspace, "cloud_env", store.Environment, "runtime")
	for _, relative := range []string{"state/secrets", "state/kubeconfig.yaml", "state/openbao", "services/video-cloud/video-cloud.env"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); err == nil {
			return fmt.Errorf("legacy sensitive runtime path still exists: %s", relative)
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("legacy sensitive runtime paths cannot be inspected")
		}
	}
	return nil
}

func executeDeploymentCheck(ctx context.Context, o deploymentCheckOptions, cfg deploymentConfig, deps deploymentCheckDependencies, reporter *deploymentCheckReporter) {
	if o.phase == deploymentCheckPreDeploy {
		executeDeploymentPreDeployCheck(ctx, o, cfg, deps, reporter)
		return
	}
	store, storeErr := deps.store(cfg.Environment)
	store.checkRuntime = newDeploymentCheckRuntime(ctx, cfg.Environment)
	type task struct {
		id           string
		required     bool
		dependencies []string
		run          func() error
	}
	tasks := []task{
		{"secrets.local", true, nil, func() error {
			if storeErr != nil {
				return errors.New("selected environment SecretStore cannot be resolved")
			}
			return deps.local(store)
		}},
		{"secrets.legacy-paths", true, nil, func() error { return verifyDeploymentCheckLegacyPaths(store, cfg.Workspace) }},
		{"k8s.access-input", true, nil, func() error {
			info, err := os.Stat(store.KubeconfigPath())
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("existing-environment checks require a readable nonempty environment-local kubeconfig")
			}
			f, err := os.Open(store.KubeconfigPath())
			if err != nil {
				return errors.New("environment-local kubeconfig is not readable")
			}
			return f.Close()
		}},
		{"secrets.bindings", true, []string{"secrets.local", "k8s.access-input"}, func() error { return deps.bindings(store) }},
		{"pki.runtime", true, []string{"secrets.local", "k8s.access-input"}, func() error { return deps.runtime(store, time.Now()) }},
		{"pki.migration", o.requireMigration, []string{"secrets.local", "k8s.access-input"}, func() error { return deps.migration(store) }},
		{"pki.product-root", o.requireProduct, []string{"secrets.local", "k8s.access-input"}, func() error { return deps.product(store) }},
	}
	for _, t := range tasks {
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PENDING", Required: t.required, DependsOn: t.dependencies, Resource: cfg.Environment})
	}
	q := o.qualification
	q.envFile = defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
	if deps.plan != nil {
		deps.plan(ctx, cfg, q.envFile, q, reporter.emit)
	}
	passed := map[string]bool{}
	var passedMu sync.Mutex
	runTask := func(t task) {
		if !t.required {
			reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "SKIPPED", Code: "NOT_SELECTED", Detail: "optional qualification was not requested"})
			return
		}
		blocked := false
		passedMu.Lock()
		for _, dep := range t.dependencies {
			if !passed[dep] {
				blocked = true
			}
		}
		passedMu.Unlock()
		if blocked {
			reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "BLOCKED", Code: "DEPENDENCY_FAILED", Required: true, Detail: "required SecretStore or kubeconfig prerequisite failed", DependsOn: t.dependencies})
			return
		}
		if err := ctx.Err(); err != nil {
			reporter.emit(deploymentCheckFailure(t.id, err))
			return
		}
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "RUNNING"})
		if err := t.run(); err != nil {
			c := deploymentCheckFailure(t.id, err)
			if t.id == "k8s.access-input" {
				c.Status, c.Code, c.NextAction = "BLOCKED", "KUBECONFIG_UNAVAILABLE", "Restore access to the selected existing environment; missing local files do not prove the cloud is empty."
			}
			reporter.emit(c)
		} else {
			passedMu.Lock()
			passed[t.id] = true
			passedMu.Unlock()
			reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PASS", Passed: true, Required: true, Detail: "verified", Attempts: 1})
		}
	}
	// The live groups depend only on the three local prerequisites and share
	// the same snapshot runtime; at most four are active in this phase.
	for _, t := range tasks[:3] {
		runTask(t)
	}
	var live sync.WaitGroup
	for _, t := range tasks[3:] {
		live.Add(1)
		go func(t task) { defer live.Done(); runTask(t) }(t)
	}
	live.Wait()
	allowWrites := true
	for _, t := range tasks {
		if t.required && !passed[t.id] {
			allowWrites = false
		}
	}
	deps.collect(ctx, cfg, q.envFile, q, allowWrites, reporter.emit)
}

func writeDeploymentCheckReport(path string, report deploymentCheckReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".deployment-check-*.json")
	if err != nil {
		return errors.New("cannot create deployment check report")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return errors.New("cannot write deployment check report")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close deployment check report")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot publish deployment check report")
	}
	return nil
}

func runDeploymentCheck(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDeploymentCheckWithDependencies(ctx, args, os.Stdout, os.Stderr, defaultDeploymentCheckDependencies())
}

func runDeploymentCheckWithDependencies(parent context.Context, args []string, out, diagnostics io.Writer, deps deploymentCheckDependencies) error {
	phase := deploymentCheckBannerPhase(args)
	if os.Getenv("RTK_CLOUD_CHECK_BANNER_PHASE") != phase || phase == "" {
		printDeploymentCheckPurpose(out, phase)
	}
	o, err := parseDeploymentCheckOptions(args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "error:", err)
		return exitCode(2)
	}
	// Preserve the existing maintenance fence, but reject bad flags and local
	// inputs before consulting operator state or starting any check.
	if deps.guard != nil {
		if err := deps.guard(append([]string{"deployment", "check"}, args...)); err != nil {
			fmt.Fprintln(diagnostics, "error:", err)
			return exitCode(1)
		}
	}
	cfg, err := deps.resolve(o.workspace, o.environment, o.environmentRoot)
	if err != nil {
		fmt.Fprintln(diagnostics, "error:", err)
		return exitCode(2)
	}
	if (o.qualification.createMissingObjectStorageBucket || o.qualification.grantObjectStorageBucketAccess) && cfg.Storage.RuntimeMediaCutoverRequired {
		if err := validateDeploymentStorageActivation(cfg); err != nil {
			fmt.Fprintln(diagnostics, "error: use storage-bootstrap with an isolated candidate profile before cutover:", err)
			return exitCode(2)
		}
	}
	ctx, cancel := context.WithTimeout(parent, o.timeout)
	defer cancel()
	reporter := newDeploymentCheckReporter(out)
	fmt.Fprintf(out, "Deployment check: phase=%s environment=%s fast=%t read-only=%t timeout=%s; selected checks only, not release approval\n", o.phase, cfg.Environment, o.qualification.fast, o.qualification.readOnly, o.timeout)
	done, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(heartbeatDone); reporter.heartbeat(ctx, done) }()
	executeDeploymentCheck(ctx, o, cfg, deps, reporter)
	if err := ctx.Err(); err != nil {
		reporter.emit(deploymentCheckFailure("execution.deadline", err))
	}
	close(done)
	<-heartbeatDone
	report := reporter.snapshot(cfg.Environment, o.phase, o.qualification.fast, o.qualification.readOnly)
	if o.report != "" {
		if err := writeDeploymentCheckReport(o.report, report); err != nil {
			fmt.Fprintln(diagnostics, "error:", err)
			return exitCode(1)
		}
	}
	fmt.Fprintln(out, "Check summary (stable check ID order):")
	for _, check := range report.Checks {
		fmt.Fprintf(out, "  [%s] %s %s\n", check.Status, check.ID, check.Code)
		if check.Status == "FAIL" || check.Status == "ERROR" || (check.Status == "BLOCKED" && check.Code != "DEPENDENCY_FAILED" && check.Code != "PREREQUISITE_FAILED") {
			fmt.Fprintf(out, "    cause: %s\n    next: %s\n", check.Message, check.NextAction)
		}
	}
	fmt.Fprintf(out, "overall: %s required=%d pass=%d fail=%d error=%d blocked=%d skipped=%d duration=%dms\n", report.Overall, report.Coverage["required"], report.Coverage["PASS"], report.Coverage["FAIL"], report.Coverage["ERROR"], report.Coverage["BLOCKED"], report.Coverage["SKIPPED"], report.DurationMS)
	slowest := append([]deploymentCheckResult{}, report.Checks...)
	sort.SliceStable(slowest, func(i, j int) bool { return slowest[i].DurationMS > slowest[j].DurationMS })
	for i := 0; i < len(slowest) && i < 3; i++ {
		if slowest[i].DurationMS > 0 {
			fmt.Fprintf(out, "slowest: %s %dms\n", slowest[i].ID, slowest[i].DurationMS)
		}
	}
	if report.Overall != "PASS" {
		return exitCode(1)
	}
	return nil
}
