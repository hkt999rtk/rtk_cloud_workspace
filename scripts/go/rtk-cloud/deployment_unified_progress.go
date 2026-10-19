package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

type environmentProgressKey struct{}
type environmentProgressRoundKey struct{}

// Rounds are presentation context, never qualification evidence or input values.
func environmentDeploymentProgressRound(ctx context.Context, round string) context.Context {
	switch round {
	case "preflight", "before-execution":
		return context.WithValue(ctx, environmentProgressRoundKey{}, round)
	default:
		return ctx
	}
}

func environmentProgressRound(ctx context.Context) string {
	round, _ := ctx.Value(environmentProgressRoundKey{}).(string)
	return round
}

func environmentProgressStageKey(stage, round string) string {
	if round == "" {
		return stage
	}
	return round + "/" + stage
}

func environmentProgressRoundField(round string) string {
	if round == "" {
		return ""
	}
	return " round=" + round
}

type environmentProgress struct {
	mu                     sync.Mutex
	out                    io.Writer
	log                    io.Writer
	stage                  string
	round                  string
	status                 string
	started                time.Time
	stageStarted           map[string]time.Time
	announced              map[string]bool
	lastDisplay            string
	lastLogHeartbeat       time.Time
	checked                int
	total                  int
	resource, action, step string
	completed, resources   int
	operations             map[uint64]environmentProgressOperation
	nextOperation          uint64
	settled                int
	rollout                string
	terminal               bool
	columns                int
	color                  bool
	frame                  int
	lineVisible            bool
	durablePending         []byte
	terminalFD             uintptr
	hasTerminalFD          bool
}

type environmentProgressOperation struct {
	action, resource string
	started          time.Time
}

var environmentProgressActions = map[string]string{
	"read": "Read current resource", "inventory": "Inspect resource inventory",
	"storage": "Verify original PVC/PV bindings", "permissions": "Check Kubernetes permissions",
	"release-ci": "Verify CI release evidence", "release-image": "Verify selected image",
	"sql": "Verify database state", "pki": "Verify runtime PKI", "network": "Verify public mTLS endpoint",
	"dry-run": "Test exact resource body without saving", "apply": "Save approved resource body",
	"wait": "Wait for resource readiness", "retain": "Verify retained resource", "verify": "Verify deployed state",
}

func environmentProgressPermissionAction(verb string) string {
	action := "permissions-" + verb
	if environmentProgressActions[action] != "" {
		return action
	}
	return "permissions"
}

var environmentProgressKinds = map[string]string{}

func init() {
	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "bind", "escalate", "impersonate"} {
		environmentProgressActions["permissions-"+verb] = "Check Kubernetes " + verb + " permission"
	}
	for _, kind := range []string{"Namespace", "Node", "Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Secret", "ConfigMap", "Service", "ServiceAccount", "PersistentVolumeClaim", "PersistentVolume", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "NetworkPolicy", "Ingress", "IngressClass", "PodDisruptionBudget", "HorizontalPodAutoscaler", "ValidatingWebhookConfiguration", "Lease"} {
		environmentProgressKinds[strings.ToLower(kind)] = kind
	}
	for _, kind := range []string{"namespaces", "nodes", "pods", "deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "cronjobs", "secrets", "configmaps", "services", "serviceaccounts", "persistentvolumeclaims", "persistentvolumes", "roles", "rolebindings", "clusterroles", "clusterrolebindings", "networkpolicies", "ingresses", "ingressclasses", "poddisruptionbudgets", "horizontalpodautoscalers", "validatingwebhookconfigurations", "leases"} {
		singular := strings.TrimSuffix(kind, "s")
		switch kind {
		case "networkpolicies":
			singular = "networkpolicy"
		case "ingresses":
			singular = "ingress"
		case "ingressclasses":
			singular = "ingressclass"
		}
		environmentProgressKinds[kind] = environmentProgressKinds[singular]
	}
	environmentProgressKinds["pvc"], environmentProgressKinds["pv"] = "PersistentVolumeClaim", "PersistentVolume"
}

// Metadata names are public identifiers. Never accept arbitrary arguments,
// URLs, output, credential values or control characters as progress targets.
func environmentProgressResourceID(kind, namespace, name string) string {
	kind = environmentProgressKinds[strings.ToLower(strings.Split(kind, ".")[0])]
	if kind == "" {
		return ""
	}
	for _, value := range []string{namespace, name} {
		if len(value) > 253 {
			return ""
		}
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
				return ""
			}
		}
	}
	return kind + "/" + namespace + "/" + name
}

func validEnvironmentProgressResource(id string) bool {
	parts := strings.Split(id, "/")
	return len(parts) == 3 && environmentProgressResourceID(parts[0], parts[1], parts[2]) == id
}

func environmentProgressStageTask(stage string) string {
	if stage == "deployment.execution.ota-storage" {
		return "Inspect original OTA bucket privacy and signed read access"
	}
	if stage == "deployment.execution.ota-schema" {
		return "Read existing OTA schema prerequisites from original PostgreSQL"
	}
	if task := map[string]string{
		"deployment.preflight.evidence":              "Validate and load the qualified preflight plan",
		"deployment.execution.recheck.target":        "Check current execution inputs and original resource owners",
		"deployment.execution.recheck.execution":     "Check current capacity, permissions, storage and execution ownership",
		"deployment.execution.recheck.pki":           "Check current PKI registry, revocation and certificate validity",
		"deployment.execution.recheck.compatibility": "Check current schema prerequisites",
		"deployment.execution.recheck.network":       "Check current public routes and authenticated mTLS",
		"deployment.compile":                         "Compile the deployment plan", "deployment.qualification": "Check every required preflight gate",
		"deployment.target": "Verify target ownership and deployment inputs", "deployment.release": "Verify images and exact-source CI evidence",
		"deployment.execution": "Verify deployment can execute", "deployment.execution.node-capacity": "Check node capacity",
		"deployment.execution.surge-capacity":        "Check capacity for overlapping replacement Pods",
		"deployment.inputs.ota-storage":              "Inspect the existing dedicated OTA object-storage allocation",
		"deployment.inputs.media-storage":            "Inspect the original existing media storage allocation",
		"deployment.execution.video-storage-startup": "Verify independent VideoStorage inputs and original media allocation",
		"deployment.execution.ota-startup":           "Verify OTA database, private storage and original credentials",
		"deployment.execution.ota-registration":      "Compare exact-image OTA registration with original AM definitions",
		"deployment.execution.mqtt-transport":        "Verify MQTT trust, private callback, CRL persistence and App TURN policy",
		"deployment.execution.storage":               "Verify original PVC/PV bindings", "deployment.execution.permissions": "Check resource permissions",
		"deployment.execution.lease": "Check or acquire exclusive deployment ownership", "deployment.execution.admission": "Read baselines and test exact resource bodies",
		"deployment.create.target":            "Verify fresh target absence through the selected provider",
		"deployment.execution.recovery":       "Inspect preserved execution evidence and verify an accepted retained target",
		"deployment.pki.identity-permissions": "Verify private identity directory permissions and target volume policy",
		"deployment.pki":                      "Verify environment identity and trust", "deployment.pki.bindings": "Verify installed Secret bindings",
		"deployment.pki.runtime-now": "Verify runtime certificates and trust at current time", "deployment.pki.runtime-window": "Verify runtime certificates through the upgrade window",
		"deployment.pki.mqtt-now": "Verify MQTT identity at current time", "deployment.pki.mqtt-window": "Verify MQTT identity through the upgrade window",
		"deployment.pki.consumer-policy": "Verify required CRL readers and bundle consumers in the candidate policy",
		"deployment.compatibility":       "Verify release and database compatibility", "deployment.network": "Verify public CertIssuer and authenticated mTLS",
		"deployment.compatibility.transition": "Verify original allocations and candidate PKI/storage transitions",
		"deployment.inputs":                   "Read maintained settings and credentials", "deployment.inputs.fence": "Recheck deployment inputs and live owners",
		"deployment.inventory.read": "Inspect the selected environment inventory", "deployment.storage.observation": "Record original PVC/PV bindings",
		"deployment.pki.observation": "Inspect original runtime identity", "deployment.renderer.support": "Render support service resources",
		"deployment.renderer.target": "Render target service resources", "deployment.ownership": "Verify original resource ownership",
		"deployment.dependencies": "Resolve retained resource dependencies", "deployment.plan.compile": "Order the exact resource plan",
		"deployment.release.resolve": "Resolve pinned images and CI artifacts", "deployment.release.sources": "Check source revision pins",
		"deployment.release.sources.recheck": "Recheck source revisions after CI artifact selection",
	}[stage]; task != "" {
		return task
	}
	if strings.HasPrefix(stage, "deployment.release.") {
		parts := strings.Split(stage, ".")
		if len(parts) >= 4 {
			role := parts[len(parts)-1]
			if parts[2] == "verify" {
				role = parts[3]
			}
			role = strings.ToLower(strings.ReplaceAll(role, "_", "-"))
			if role != "" && environmentProgressResourceID("ConfigMap", "", role) != "" {
				if parts[2] == "verify" {
					if task := map[string]string{
						"workspace-pin": "Verify workspace source pin for ", "checkout-head": "Verify checkout HEAD for ",
						"pull": "Pull selected image for ", "oci-labels": "Verify OCI image labels for ",
						"ci-runs": "List exact-source CI runs for ", "release-runs": "List exact-source Release runs for ",
						"publication-jobs": "Verify publication jobs for ", "workspace-baseline-runs": "List workspace baseline runs for ",
						"backup-runs": "List PostgreSQL backup release runs for ", "release-run": "Verify Release run details for ",
						"artifact-list": "List formal CI artifacts for ", "artifact-download": "Download and verify formal CI artifact for ",
					}[parts[len(parts)-1]]; task != "" {
						return task + role
					}
				}
				if parts[2] == "provider" {
					return "Resolve official image for " + role
				}
				if parts[2] == "ci" {
					return "Resolve CI image artifact for " + role
				}
				return "Verify release evidence for " + role
			}
		}
	}
	if strings.HasPrefix(stage, "deployment.execution.") {
		parts := strings.Split(stage, ".")
		step := parts[len(parts)-1]
		tasks := map[string]string{"qualification": "Check current execution prerequisites using preflight evidence", "admission": "Check exact server admission", "fence": "Recheck every stage dependency", "apply": "Apply resources in dependency order", "rollout": "Wait for rollout readiness", "verification": "Verify updated resources; deployment is not complete yet", "acceptance": "Run post-deploy acceptance; deployment is not complete yet", "lease": "Check exclusive execution ownership", "recovery": "Inspect original state and prepare recovery evidence", "ownership": "Verify final resource owners", "journal": "Save verified deployment completion", "cleanup": "Release execution ownership and archive the local lock"}
		if task := tasks[step]; task != "" {
			return task
		}
	}
	if strings.HasSuffix(stage, "fence") {
		return "Recheck deployment inputs and live resource owners"
	}
	return "Check deployment prerequisites"
}

func (p *environmentProgress) write(heartbeat bool) {
	if p.status != "" {
		return
	}
	p.writeLog(heartbeat)
	line := p.plainLine(false, false)
	if !heartbeat && line == p.lastDisplay {
		if p.terminal {
			p.writeTerminal()
		}
		return
	}
	if !heartbeat {
		p.lastDisplay = line
	}
	if p.terminal {
		if !heartbeat {
			p.clearTerminalLine()
			p.writePlain(false)
		}
		p.writeTerminal()
		return
	}
	p.writePlain(heartbeat)
}

func (p *environmentProgress) writeLog(heartbeat bool) {
	if !heartbeat || time.Since(p.lastLogHeartbeat) >= 10*time.Second {
		if p.log != nil {
			fmt.Fprintln(p.log, p.plainLine(heartbeat, true))
		}
		p.lastLogHeartbeat = time.Now()
	}
}

// Revisiting a prerequisite updates the active task and private log, without
// announcing the same start again. Counts and resource changes use write.
func (p *environmentProgress) writeStart() {
	key := environmentProgressStageKey(p.stage, p.round)
	if p.announced == nil {
		p.announced = map[string]bool{}
	}
	if p.announced[key] {
		p.writeLog(false)
		if p.terminal {
			p.writeTerminal()
		}
		return
	}
	p.announced[key] = true
	p.write(false)
}

func (p *environmentProgress) writePlain(heartbeat bool) {
	line := p.plainLine(heartbeat, false)
	if p.terminal && p.color {
		header, task, _ := strings.Cut(line, "\n  ")
		line = header + "\n  \x1b[36m" + task + "\x1b[0m"
	}
	fmt.Fprintln(p.out, line)
}

func (p *environmentProgress) plainLine(heartbeat, details bool) string {
	var line strings.Builder
	fmt.Fprintf(&line, "[RUNNING] %s", p.stage)
	line.WriteString(environmentProgressRoundField(p.round))
	if details {
		fmt.Fprintf(&line, " elapsed=%ds", int64(time.Since(p.started)/time.Second))
	}
	if p.total >= 0 {
		fmt.Fprintf(&line, " checked=%d/%d", p.checked, p.total)
	}
	if p.resources > 0 {
		fmt.Fprintf(&line, " resources=%d/%d", p.completed, p.resources)
	}
	if p.resource != "" {
		fmt.Fprintf(&line, " action=%s step=%s resource=%s", p.action, p.step, p.resource)
	}
	if p.rollout != "" {
		fmt.Fprintf(&line, " %s", p.rollout)
	}
	if p.settled > 0 {
		fmt.Fprintf(&line, " queries-finished=%d", p.settled)
	}
	if len(p.operations) > 0 {
		ids := make([]uint64, 0, len(p.operations))
		for id := range p.operations {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		fmt.Fprintf(&line, " active=%d", len(ids))
		for _, id := range ids[:min(3, len(ids))] {
			op := p.operations[id]
			fmt.Fprintf(&line, " | %s", environmentProgressActions[op.action])
			if op.resource != "" {
				resource := op.resource
				parts := strings.Split(resource, "/")
				if parts[2] == "" {
					resource = parts[0] + "/*"
					if parts[1] != "" {
						resource = parts[0] + "/" + parts[1] + "/*"
					}
				}
				fmt.Fprintf(&line, " %s", resource)
			}
			if details {
				fmt.Fprintf(&line, " (%ds)", int64(time.Since(op.started)/time.Second))
			}
		}
		if len(ids) > 3 {
			fmt.Fprintf(&line, " | +%d more", len(ids)-3)
		}
	}
	if heartbeat {
		fmt.Fprint(&line, " still running")
	}
	fmt.Fprintf(&line, "\n  %s", environmentProgressStageTask(p.stage))
	return line.String()
}

func environmentDeploymentOperation(ctx context.Context, action, resource string) func() {
	progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress)
	if !ok || environmentProgressActions[action] == "" || resource != "" && !validEnvironmentProgressResource(resource) {
		return func() {}
	}
	progress.mu.Lock()
	if progress.operations == nil {
		progress.operations = map[uint64]environmentProgressOperation{}
	}
	progress.nextOperation++
	id := progress.nextOperation
	progress.operations[id] = environmentProgressOperation{action, resource, time.Now()}
	progress.mu.Unlock()
	return func() {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		if _, exists := progress.operations[id]; exists {
			delete(progress.operations, id)
			progress.settled++
		}
	}
}

func environmentDeploymentResourceProgress(ctx context.Context, index, total int, resource, action, step string) {
	if index < 0 || index >= total || !validEnvironmentProgressResource(resource) || strings.HasSuffix(resource, "/") {
		return
	}
	switch action {
	case "create", "update", "retain", "retire":
	default:
		return
	}
	switch step {
	case "read", "dry-run", "apply", "wait", "complete":
	default:
		return
	}
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		completed := index
		if step == "complete" {
			completed++
		}
		if completed < progress.completed || progress.resources > 0 && progress.resources != total {
			return
		}
		progress.resource, progress.action, progress.step = resource, action, step
		progress.resources = total
		progress.completed = max(progress.completed, completed)
		if step == "complete" {
			progress.clearTerminalLine()
			if progress.log != nil {
				fmt.Fprintf(progress.log, "[OK] %s action=%s resources=%d/%d\n", resource, action, completed, total)
			}
			if progress.terminal {
				progress.writeTerminal()
			}
		} else if step == "read" {
			progress.write(false)
		}
	}
}

func environmentDeploymentRolloutProgress(ctx context.Context, resource string, ready, updated, desired, waitSeconds int) {
	if !validEnvironmentProgressResource(resource) || strings.HasSuffix(resource, "/") || waitSeconds <= 0 || ready < 0 || updated < 0 || desired < 0 || ready > 1000000 || updated > 1000000 || desired > 1000000 {
		return
	}
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		value := fmt.Sprintf("waiting-for=%s ready=%d/%d updated=%d/%d rollout-limit=%ds", resource, ready, desired, updated, desired, waitSeconds)
		if value != progress.rollout {
			progress.rollout = value
			progress.write(false)
		}
	}
}

// Only maintained stage IDs cross this boundary; never pass command output,
// credentials, input values or provider errors to the progress reporter.
func environmentDeploymentProgress(ctx context.Context, stage string) {
	environmentDeploymentProgressAt(ctx, stage, time.Now())
}

func environmentDeploymentProgressAt(ctx context.Context, stage string, started time.Time) {
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		round := environmentProgressRound(ctx)
		if progress.stage == stage && progress.round == round && progress.status == "" {
			progress.writeStart()
			return
		}
		progress.stage, progress.round, progress.started = stage, round, started
		if progress.stageStarted == nil {
			progress.stageStarted = map[string]time.Time{}
		}
		key := environmentProgressStageKey(stage, round)
		if first, ok := progress.stageStarted[key]; ok {
			progress.started = first
		} else {
			progress.stageStarted[key] = progress.started
		}
		progress.status = ""
		progress.checked, progress.total = 0, -1
		progress.resource, progress.action, progress.step, progress.rollout = "", "", "", ""
		progress.settled = 0
		progress.writeStart()
	}
}

// A successful wait returns to the active apply task, not a new apply batch.
func environmentDeploymentProgressResume(ctx context.Context, stage string, started time.Time) {
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.stage, progress.round, progress.started = stage, environmentProgressRound(ctx), started
		progress.status, progress.rollout = "", ""
		progress.writeLog(false)
		if progress.terminal {
			progress.writeTerminal()
		}
	}
}

// This is called only with a verified gate outcome or completed synchronous work.
// Query counts and readiness observations never call it to infer success.
func environmentDeploymentProgressResult(ctx context.Context, stage, status string) {
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.status = status
		progress.clearTerminalLine()
		if !deploymentStatusSuccessful(status) {
			fmt.Fprintf(progress.out, "%s %s%s\n", deploymentStatusLabelColor(status, progress.color), stage, environmentProgressRoundField(environmentProgressRound(ctx)))
		}
		if progress.log != nil {
			started := progress.started
			if at, ok := progress.stageStarted[environmentProgressStageKey(stage, environmentProgressRound(ctx))]; ok {
				started = at
			}
			fmt.Fprintf(progress.log, "%s %s elapsed=%ds%s\n", deploymentStatusLabelColor(status, false), stage, int64(time.Since(started)/time.Second), environmentProgressRoundField(environmentProgressRound(ctx)))
		}
	}
}

// Call after the step's validation, rather than merely after a subprocess exits.
func environmentDeploymentProgressDone(ctx context.Context, stage string, err error) {
	status := "PASS"
	if err != nil || ctx.Err() != nil {
		status = "FAIL"
	}
	environmentDeploymentProgressResult(ctx, stage, status)
}

// For sequential steps whose callers return immediately on errors. Explicitly
// advancing proves the prior step succeeded; abort handles every early exit.
func environmentDeploymentSteps(ctx context.Context) (next func(string), abort func()) {
	stage := ""
	next = func(following string) {
		if following == stage {
			if stage != "" {
				environmentDeploymentProgress(ctx, stage)
			}
			return
		}
		if stage != "" {
			environmentDeploymentProgressDone(ctx, stage, nil)
		}
		stage = following
		if stage != "" {
			environmentDeploymentProgress(ctx, stage)
		}
	}
	abort = func() {
		if stage != "" {
			environmentDeploymentProgressResult(ctx, stage, "FAIL")
		}
	}
	return next, abort
}

// Admission counts finished resource checks, including failures. Counts never
// imply PASS and never restart the current stage's elapsed-time clock.
func environmentDeploymentAdmissionProgress(ctx context.Context, checked, total int) {
	if checked < 0 || total < 0 || checked > total {
		return
	}
	if progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress); ok {
		progress.mu.Lock()
		defer progress.mu.Unlock()
		if progress.total >= 0 && (total != progress.total || checked <= progress.checked) {
			return
		}
		first := progress.total < 0
		progress.checked, progress.total = checked, total
		if (first && checked == 0) || checked == total {
			progress.write(false)
		}
	}
}

func runEnvironmentProgress(ctx context.Context, out io.Writer, stage string, work func(context.Context) error) error {
	return runEnvironmentProgressInterval(ctx, out, stage, 10*time.Second, work)
}
func runEnvironmentProgressInterval(ctx context.Context, out io.Writer, stage string, interval time.Duration, work func(context.Context) error) (result error) {
	measured := beginDeploymentMeasurement(ctx, stage)
	defer func() { measured(result) }()
	progress := &environmentProgress{out: out, resources: -1}
	progress.log, _ = ctx.Value(environmentProgressLogKey{}).(io.Writer)
	started := time.Now()
	progress.terminal, progress.columns, progress.color = environmentProgressTerminal(out)
	if progress.terminal {
		if file, ok := out.(interface{ Fd() uintptr }); ok {
			progress.terminalFD, progress.hasTerminalFD = file.Fd(), true
		}
		interval = min(interval, 100*time.Millisecond)
	}
	ctx = context.WithValue(ctx, environmentProgressKey{}, progress)
	environmentDeploymentProgress(ctx, stage)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				progress.mu.Lock()
				progress.write(true)
				progress.mu.Unlock()
			}
		}
	}()
	// Always join the reporter before normal results use the same writer,
	// including errors/cancellation. Work stays synchronous; no task is abandoned.
	finished := false
	defer func() {
		close(done)
		<-stopped
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.clearTerminalLine()
		if len(progress.durablePending) > 0 {
			fmt.Fprintln(progress.out, string(progress.durablePending))
			progress.durablePending = nil
		}
		if finished {
			status := "PASS"
			if result != nil || ctx.Err() != nil {
				status = "FAIL"
			}
			if !deploymentStatusSuccessful(status) {
				fmt.Fprintf(progress.out, "%s %s%s\n", deploymentStatusLabelColor(status, progress.color), stage, environmentProgressRoundField(environmentProgressRound(ctx)))
			}
			if progress.log != nil {
				fmt.Fprintf(progress.log, "%s %s elapsed=%ds%s\n", deploymentStatusLabelColor(status, false), stage, int64(time.Since(started)/time.Second), environmentProgressRoundField(environmentProgressRound(ctx)))
			}
		}
	}()
	result = work(ctx)
	finished = true
	return result
}
