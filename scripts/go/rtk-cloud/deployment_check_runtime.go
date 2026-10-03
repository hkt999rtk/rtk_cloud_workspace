package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// deploymentCheckRuntime belongs to one checker invocation. Legacy callers leave
// secretStore.checkRuntime nil and retain their existing behavior.
type deploymentCheckRuntime struct {
	ctx            context.Context
	environment    string
	requestTimeout time.Duration
	processTimeout time.Duration
	sqlTimeout     time.Duration
	retryDelay     time.Duration
	mu             sync.Mutex
	cache          map[string]*deploymentCheckRead
}

type deploymentCheckRead struct {
	done   chan struct{}
	output []byte
	err    error
}

func newDeploymentCheckRuntime(ctx context.Context, environment string) *deploymentCheckRuntime {
	return &deploymentCheckRuntime{
		ctx: ctx, environment: environment, requestTimeout: 10 * time.Second,
		processTimeout: 20 * time.Second, sqlTimeout: 30 * time.Second,
		retryDelay: 200 * time.Millisecond, cache: map[string]*deploymentCheckRead{},
	}
}

type deploymentRuntimeError struct{ code, detail string }

func (e *deploymentRuntimeError) Error() string {
	if strings.Contains(e.detail, "["+e.code+"]") {
		return e.detail
	}
	return e.detail + " [" + e.code + "]"
}
func (e *deploymentRuntimeError) CheckCode() string { return e.code }

// The original diagnostic is retained only in memory for classification. Never
// include raw kubectl/psql output, arguments, or credential-bearing URLs in errors.
func classifyDeploymentRuntimeError(ctx context.Context, raw []byte, err error, sql bool) *deploymentRuntimeError {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return &deploymentRuntimeError{"CHECK_CANCELLED", "deployment check cancelled"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &deploymentRuntimeError{"KUBE_TIMEOUT", "Kubernetes command exceeded its deadline"}
	}
	diagnostic := strings.ToLower(string(raw))
	switch {
	case sql && strings.Contains(diagnostic, "statement timeout"):
		return &deploymentRuntimeError{"SQL_TIMEOUT", "database read exceeded its statement timeout"}
	case strings.Contains(diagnostic, "forbidden"), strings.Contains(diagnostic, "permission denied"):
		return &deploymentRuntimeError{"KUBE_FORBIDDEN", "selected identity lacks permission for the required read"}
	case strings.Contains(diagnostic, "unauthorized"), strings.Contains(diagnostic, "must be logged in"):
		return &deploymentRuntimeError{"KUBE_UNAUTHORIZED", "Kubernetes authentication failed"}
	case strings.Contains(diagnostic, "too many requests"), strings.Contains(diagnostic, "toomanyrequests"), strings.Contains(diagnostic, "429"):
		return &deploymentRuntimeError{"KUBE_RATE_LIMITED", "Kubernetes API rate limited the read"}
	case strings.Contains(diagnostic, "timeout"), strings.Contains(diagnostic, "timed out"), strings.Contains(diagnostic, "deadline exceeded"):
		return &deploymentRuntimeError{"KUBE_TIMEOUT", "Kubernetes read timed out"}
	case strings.Contains(diagnostic, "notfound"), strings.Contains(diagnostic, "not found"):
		return &deploymentRuntimeError{"KUBE_NOT_FOUND", "required Kubernetes resource was not found"}
	case strings.Contains(diagnostic, "service unavailable"), strings.Contains(diagnostic, "server error"), strings.Contains(diagnostic, "connection refused"), strings.Contains(diagnostic, "connection reset"), strings.Contains(diagnostic, "unexpected eof"), strings.Contains(diagnostic, "503"), strings.Contains(diagnostic, "502"), strings.Contains(diagnostic, "504"):
		return &deploymentRuntimeError{"KUBE_UNAVAILABLE", "Kubernetes API is temporarily unavailable"}
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return &deploymentRuntimeError{"KUBE_TOOL_MISSING", "kubectl is unavailable"}
	}
	return &deploymentRuntimeError{"KUBE_COMMAND_FAILED", "Kubernetes command failed; inspect the selected resource and controller connectivity"}
}

func secretCheckFailure(err error, message string) error {
	var classified interface{ CheckCode() string }
	if errors.As(err, &classified) {
		return &deploymentRuntimeError{classified.CheckCode(), message}
	}
	return errors.New(message)
}

func secretCheckSummary(message string) error {
	// Existing aggregate checks collect safe strings. Preserve their classification
	// for the facade without changing legacy error text or exposing tool output.
	for _, code := range []string{"CHECK_CANCELLED", "KUBE_TIMEOUT", "SQL_TIMEOUT", "KUBE_FORBIDDEN", "KUBE_UNAUTHORIZED", "KUBE_RATE_LIMITED", "KUBE_UNAVAILABLE", "KUBE_NOT_FOUND", "KUBE_TOOL_MISSING", "KUBE_INVALID_RESPONSE", "KUBE_INVENTORY_EMPTY", "KUBE_COMMAND_FAILED"} {
		if strings.Contains(message, "["+code+"]") {
			return &deploymentRuntimeError{code, message}
		}
	}
	return errors.New(message)
}

func secretCheckKubectl(runtimes []*deploymentCheckRuntime, combined bool, args ...string) ([]byte, error) {
	if len(runtimes) == 0 || runtimes[0] == nil {
		cmd := exec.Command(lkeKubectl(), args...)
		if combined {
			return cmd.CombinedOutput()
		}
		return cmd.Output()
	}
	return runtimes[0].kubectl(combined, args...)
}

func (r *deploymentCheckRuntime) cached(key string, load func() ([]byte, error)) ([]byte, error) {
	r.mu.Lock()
	entry := r.cache[key]
	if entry == nil {
		entry = &deploymentCheckRead{done: make(chan struct{})}
		r.cache[key] = entry
		r.mu.Unlock()
		entry.output, entry.err = load()
		r.mu.Lock()
		if entry.err != nil {
			// This immutable per-run snapshot also remembers an unavailable read.
			// Dependents must not repeat the same failed inventory request N times.
			entry.output = nil
		}
		close(entry.done)
		r.mu.Unlock()
		return entry.output, entry.err
	}
	r.mu.Unlock()
	select {
	case <-entry.done:
		return entry.output, entry.err
	case <-r.ctx.Done():
		return nil, classifyDeploymentRuntimeError(r.ctx, nil, r.ctx.Err(), false)
	}
}

func (r *deploymentCheckRuntime) kubectl(combined bool, args ...string) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, classifyDeploymentRuntimeError(r.ctx, nil, err, false)
	}
	get, namespace, kubeconfig := -1, "", ""
	for i, arg := range args {
		if arg == "get" && get < 0 {
			get = i
		}
		if i+1 < len(args) {
			if arg == "-n" || arg == "--namespace" {
				namespace = args[i+1]
			}
			if arg == "--kubeconfig" {
				kubeconfig = args[i+1]
			}
		}
	}
	if get >= 0 && get+1 < len(args) && (args[get+1] == "secrets" || args[get+1] == "secret") {
		inventory, err := r.secretInventory(kubeconfig)
		if err != nil {
			return nil, err
		}
		if args[get+1] == "secrets" {
			return inventory, nil
		}
		if get+2 >= len(args) {
			return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "Secret lookup has no resource name"}
		}
		var secrets liveSecretList
		_ = json.Unmarshal(inventory, &secrets)
		for _, secret := range secrets.Items {
			if secret.Metadata.Namespace == namespace && secret.Metadata.Name == args[get+2] {
				return json.Marshal(secret)
			}
		}
		return nil, &deploymentRuntimeError{"KUBE_NOT_FOUND", "required Secret is absent from the selected stack inventory"}
	}
	if get >= 0 && get+1 < len(args) && (args[get+1] == "deployments" || args[get+1] == "deployment") {
		inventory, err := r.cached("deployment-inventory\x00"+kubeconfig+"\x00"+namespace, func() ([]byte, error) {
			raw, err := r.run(false, "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployments", "-o", "json")
			if err != nil {
				return nil, err
			}
			if _, err := decodeDeploymentCheckInventory(raw, "Deployment"); err != nil {
				return nil, err
			}
			return raw, nil
		})
		if err != nil {
			return nil, err
		}
		var deployments struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(inventory, &deployments) // The cached snapshot was validated above.
		if get+2 >= len(args) || strings.HasPrefix(args[get+2], "-") {
			stack := "video-cloud-" + r.environment
			if len(deployments.Items) == 0 && (namespace == stack+"-video-cloud" || namespace == stack+"-account-manager") {
				return nil, &deploymentRuntimeError{"KUBE_INVENTORY_EMPTY", "required selected namespace has no observed Deployments; verify the environment and kubeconfig"}
			}
			return inventory, nil
		}
		for _, raw := range deployments.Items {
			var metadata struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if json.Unmarshal(raw, &metadata) == nil && metadata.Metadata.Name == args[get+2] {
				for i, arg := range args {
					if arg == "-o" && i+1 < len(args) && args[i+1] == "name" {
						return []byte("deployment.apps/" + metadata.Metadata.Name + "\n"), nil
					}
				}
				return raw, nil
			}
		}
		for _, arg := range args {
			if arg == "--ignore-not-found=true" {
				return nil, nil
			}
		}
		return nil, &deploymentRuntimeError{"KUBE_NOT_FOUND", "required Deployment is absent from the selected namespace inventory"}
	}
	load := func() ([]byte, error) { return r.run(combined, args...) }
	if get >= 0 {
		return r.cached(strings.Join(args, "\x00"), load)
	}
	return load()
}

func (r *deploymentCheckRuntime) secretInventory(kubeconfig string) ([]byte, error) {
	return r.cached("secret-inventory\x00"+kubeconfig, func() ([]byte, error) {
		raw, err := r.run(false, "--kubeconfig", kubeconfig, "get", "secrets", "--all-namespaces", "-o", "json")
		if err != nil {
			return nil, err
		}
		if _, err := decodeDeploymentCheckInventory(raw, "Secret"); err != nil {
			return nil, err
		}
		var inventory liveSecretList
		if json.Unmarshal(raw, &inventory) != nil {
			return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "live Kubernetes Secret metadata is invalid"}
		}
		selected := liveSecretList{}
		stack := "video-cloud-" + r.environment
		for _, secret := range inventory.Items {
			if secret.Metadata.Namespace == "" {
				return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "live Kubernetes Secret inventory contains an item without a namespace"}
			}
			if secret.Metadata.Namespace == stack || strings.HasPrefix(secret.Metadata.Namespace, stack+"-") {
				selected.Items = append(selected.Items, secret)
			}
		}
		if len(selected.Items) == 0 {
			return nil, &deploymentRuntimeError{"KUBE_INVENTORY_EMPTY", "selected stack has no observed Secrets; verify the environment and kubeconfig"}
		}
		return json.Marshal(selected)
	})
}

// A successful kubectl process is not evidence of a valid Kubernetes list.
// Keep empty arrays distinct from absent/null items so optional named lookups
// may be absent without accepting malformed or unobserved required inventories.
func decodeDeploymentCheckInventory(raw []byte, resource string) ([]json.RawMessage, error) {
	invalid := &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "live Kubernetes " + resource + " inventory must contain an items array of named objects"}
	var inventory struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &inventory) != nil || inventory.Items == nil {
		return nil, invalid
	}
	for _, item := range inventory.Items {
		var object struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if json.Unmarshal(item, &object) != nil || strings.TrimSpace(object.Metadata.Name) == "" {
			return nil, invalid
		}
	}
	return inventory.Items, nil
}

func (r *deploymentCheckRuntime) run(combined bool, args ...string) ([]byte, error) {
	read, sql := false, false
	commandArgs := append([]string(nil), args...)
	for i, arg := range commandArgs {
		if arg == "get" {
			read = true
		}
		if arg == "--" {
			if i+1 < len(commandArgs) && commandArgs[i+1] == "psql" {
				sql = true
				commandArgs = append(append(append([]string(nil), commandArgs[:i+1]...), "env", "PGOPTIONS=-c statement_timeout=10000"), commandArgs[i+1:]...)
			}
			break
		}
	}
	timeout := r.processTimeout
	if sql {
		timeout = r.sqlTimeout
	}
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(r.ctx, timeout)
		cmd := exec.CommandContext(ctx, lkeKubectl(), append([]string{"--request-timeout=" + r.requestTimeout.String()}, commandArgs...)...)
		// Kill the local process group, including credential helpers, on cancellation.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = time.Second
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var failure *deploymentRuntimeError
		if err != nil {
			failure = classifyDeploymentRuntimeError(ctx, stderr.Bytes(), err, sql)
		}
		cancel()
		output := stdout.Bytes()
		if combined {
			output = append(output, stderr.Bytes()...)
		}
		if err == nil {
			return output, nil
		}
		transient := failure.code == "KUBE_TIMEOUT" || failure.code == "KUBE_RATE_LIMITED" || failure.code == "KUBE_UNAVAILABLE"
		if !read || !transient || attempt != 0 || r.ctx.Err() != nil {
			return output, failure
		}
		timer := time.NewTimer(r.retryDelay)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return nil, classifyDeploymentRuntimeError(r.ctx, nil, r.ctx.Err(), sql)
		case <-timer.C:
		}
	}
}
