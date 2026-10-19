package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentKubernetesBackend struct {
	Store                   secretStore
	KubeconfigContent       []byte
	RolloutTimeout          time.Duration
	SchemaWorkspace         string
	OpenBaoReplacementReady func(context.Context, deploymentplan.Object, deploymentplan.Object) error
}

const environmentDeploymentCheckConcurrency = deploymentplan.MaxCheckConcurrency

// Independent, non-persisting checks share one bounded concurrency limit.
// Deployment mutations retain the plan's dependency and phase ordering.
func (b environmentKubernetesBackend) AdmissionConcurrency() int {
	return environmentDeploymentCheckConcurrency
}

func (b environmentKubernetesBackend) AdmissionProgress(ctx context.Context, checked, total int) {
	environmentDeploymentAdmissionProgress(ctx, checked, total)
}

// Kubernetes inventories can be larger than one resource, but subprocess output
// must not grow without a bound while private material is being captured.
type environmentAPIOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *environmentAPIOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 32<<20 {
		b.exceeded = true
		return 0, errors.New("deployment.access: Kubernetes response exceeds the inventory bound")
	}
	return b.buffer.Write(p)
}

// Retain only a bounded private prefix for error classification, draining the
// remainder without exposing it or letting diagnostics grow in memory.
type environmentPrivateAPIErrorOutput struct{ buffer bytes.Buffer }

func (b *environmentPrivateAPIErrorOutput) Write(p []byte) (int, error) {
	if remaining := (64 << 10) - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(remaining, len(p))])
	}
	return len(p), nil
}

// Freeze the single selected API/authentication identity. External credential
// files, token refresh commands and alternate contexts would introduce inputs
// that were not hashed with the approved plan.
func newEnvironmentKubernetesBackend(raw, environment string) (environmentKubernetesBackend, error) {
	return newEnvironmentKubernetesBackendAt(raw, environment, time.Now())
}

// Historical metadata recovery validates archived credentials at their original
// qualification time. It never uses them to contact the removed Kubernetes API.
func newEnvironmentKubernetesBackendAt(raw, environment string, checkedAt time.Time) (environmentKubernetesBackend, error) {
	if !secretEnvironmentPattern.MatchString(environment) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: explicit selected environment required")
	}
	var config struct {
		Version  string `yaml:"apiVersion"`
		Kind     string `yaml:"kind"`
		Current  string `yaml:"current-context"`
		Clusters []struct {
			Name    string         `yaml:"name"`
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string         `yaml:"name"`
			User map[string]any `yaml:"user"`
		} `yaml:"users"`
		Contexts []struct {
			Name    string         `yaml:"name"`
			Context map[string]any `yaml:"context"`
		} `yaml:"contexts"`
	}
	if len(raw) == 0 || len(raw) > 4<<20 || yaml.Unmarshal([]byte(raw), &config) != nil || config.Version != "v1" || config.Kind != "Config" || config.Current == "" {
		return environmentKubernetesBackend{}, errors.New("deployment.access: a bounded selected Kubernetes context is required")
	}
	var clusterName, userName string
	var selectedContext, cluster, user map[string]any
	for _, entry := range config.Contexts {
		if entry.Name == config.Current {
			if selectedContext != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes context")
			}
			selectedContext = entry.Context
			clusterName = deploymentplan.String(entry.Context["cluster"])
			userName = deploymentplan.String(entry.Context["user"])
		}
	}
	for _, entry := range config.Clusters {
		if entry.Name == clusterName {
			if cluster != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes cluster")
			}
			cluster = entry.Cluster
		}
	}
	for _, entry := range config.Users {
		if entry.Name == userName {
			if user != nil {
				return environmentKubernetesBackend{}, errors.New("deployment.access: ambiguous selected Kubernetes authentication identity")
			}
			user = entry.User
		}
	}
	endpoint, err := url.Parse(deploymentplan.String(cluster["server"]))
	if selectedContext == nil || clusterName == "" || userName == "" || cluster == nil || user == nil || err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || deploymentplan.String(cluster["certificate-authority-data"]) == "" {
		return environmentKubernetesBackend{}, errors.New("deployment.access: selected HTTPS cluster and embedded CA/authentication identity are required")
	}
	for field := range cluster {
		if !keySet("server", "certificate-authority-data", "tls-server-name")[field] {
			return environmentKubernetesBackend{}, errors.New("deployment.access: external or insecure Kubernetes transport inputs cannot be frozen")
		}
	}
	// Provider kubeconfigs can serialize an unused impersonation map. An empty
	// map adds no authentication input; nonempty impersonation stays unsupported.
	if extra, ok := user["as-user-extra"].(map[string]any); ok && len(extra) == 0 {
		delete(user, "as-user-extra")
	}
	for field := range user {
		if !keySet("token", "client-certificate-data", "client-key-data")[field] {
			return environmentKubernetesBackend{}, &deploymentplan.Blocker{ID: "deployment.access.kubeconfig", Cause: "selected kubeconfig contains unsupported authentication or impersonation fields", Next: "use embedded token or client certificate/key credentials; external credential files, refresh commands, authentication providers and nonempty impersonation settings cannot be frozen for this plan"}
		}
	}
	token := deploymentplan.String(user["token"])
	cert, key := deploymentplan.String(user["client-certificate-data"]), deploymentplan.String(user["client-key-data"])
	if (token == "") == (cert == "" || key == "") || ((cert == "") != (key == "")) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: one complete embedded Kubernetes authentication method is required")
	}
	if err := validateEnvironmentKubernetesCertificates(deploymentplan.String(cluster["certificate-authority-data"]), cert, key, checkedAt); err != nil {
		return environmentKubernetesBackend{}, err
	}
	if token != "" && (strings.TrimSpace(token) != token || strings.ContainsAny(token, "\x00\r\n\t ")) {
		return environmentKubernetesBackend{}, errors.New("deployment.access: embedded Kubernetes token is malformed")
	}
	frozen := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": config.Current, "contexts": []any{map[string]any{"name": config.Current, "context": selectedContext}}, "clusters": []any{map[string]any{"name": clusterName, "cluster": cluster}}, "users": []any{map[string]any{"name": userName, "user": user}}}
	encoded, err := yaml.Marshal(frozen)
	if err != nil {
		return environmentKubernetesBackend{}, errors.New("deployment.access: cannot freeze selected Kubernetes identity")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return environmentKubernetesBackend{}, errors.New("deployment.access: selected environment storage unavailable")
	}
	return environmentKubernetesBackend{Store: store, KubeconfigContent: encoded}, nil
}

func validateEnvironmentKubernetesCertificates(ca, clientCertificate, clientKey string, now time.Time) error {
	raw, err := base64.StdEncoding.Strict().DecodeString(ca)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("deployment.access: embedded Kubernetes CA bundle is malformed")
	}
	count := 0
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("deployment.access: embedded Kubernetes CA bundle has unexpected content")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return errors.New("deployment.access: embedded Kubernetes CA is invalid or outside its validity period")
		}
		count++
		raw = rest
	}
	if count == 0 {
		return errors.New("deployment.access: embedded Kubernetes CA bundle is empty")
	}
	if clientCertificate == "" && clientKey == "" {
		return nil
	}
	certBytes, certErr := base64.StdEncoding.Strict().DecodeString(clientCertificate)
	keyBytes, keyErr := base64.StdEncoding.Strict().DecodeString(clientKey)
	if certErr != nil || keyErr != nil || len(certBytes) > 1<<20 || len(keyBytes) > 1<<20 {
		return errors.New("deployment.access: embedded Kubernetes client identity is malformed")
	}
	pair, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("deployment.access: embedded Kubernetes client certificate and key do not match")
	}
	for _, raw := range pair.Certificate {
		certificate, err := x509.ParseCertificate(raw)
		if err != nil || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return errors.New("deployment.access: embedded Kubernetes client chain is outside its validity period")
		}
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if leaf.IsCA || leaf.Subject.CommonName == "" {
		return errors.New("deployment.access: embedded Kubernetes client leaf lacks an authenticated subject")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		allowed := false
		for _, usage := range leaf.ExtKeyUsage {
			allowed = allowed || usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny
		}
		if !allowed {
			return errors.New("deployment.access: embedded Kubernetes client leaf is not permitted for client authentication")
		}
	}
	return nil
}

func (b environmentKubernetesBackend) privateKubeconfig() (string, func(), error) {
	if len(b.KubeconfigContent) == 0 {
		return "", nil, errors.New("deployment.access: frozen Kubernetes identity is absent")
	}
	store := b.Store
	if !filepath.IsAbs(store.Root) || !secretEnvironmentPattern.MatchString(store.Environment) {
		return "", nil, errors.New("deployment.access: frozen environment store unavailable")
	}
	temporaryRoot := filepath.Join(store.Root, "deployment", "tmp")
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), temporaryRoot} {
		if err := ensurePrivateDirectory(path); err != nil {
			return "", nil, errors.New("deployment.access: private environment temporary directory unavailable")
		}
	}
	directory, err := os.MkdirTemp(temporaryRoot, "kubernetes-api-*")
	if err != nil {
		return "", nil, errors.New("deployment.access: cannot prepare private API identity")
	}
	cleanup := func() { os.RemoveAll(directory) }
	path := filepath.Join(directory, "kubeconfig.yaml")
	if err := os.WriteFile(path, b.KubeconfigContent, 0600); err != nil {
		cleanup()
		return "", nil, errors.New("deployment.access: cannot prepare private API identity")
	}
	return path, cleanup, nil
}

// Admission errors can contain Secret values. Only sanitized operation and
// resource identities cross this boundary; private payloads stay in memory.
func (b environmentKubernetesBackend) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	commandIndex := 0
	for commandIndex < len(args) {
		if args[commandIndex] == "--namespace" && commandIndex+1 < len(args) {
			commandIndex += 2
			continue
		}
		if strings.HasPrefix(args[commandIndex], "--namespace=") {
			commandIndex++
			continue
		}
		break
	}
	waitCommand := commandIndex < len(args) && (args[commandIndex] == "wait" || args[commandIndex] == "rollout" && commandIndex+1 < len(args) && args[commandIndex+1] == "status")
	readOnlyGet := len(input) == 0 && deploymentKubectlGetIndex(args) >= 0
	requestTimeout, processTimeout := "90s", 2*time.Minute
	readRuntime := newDeploymentCheckRuntime(ctx, b.Store.Environment)
	if readOnlyGet {
		requestTimeout, processTimeout = readRuntime.requestTimeout.String(), readRuntime.processTimeout
	}
	if waitCommand {
		if b.RolloutTimeout <= 0 || b.RolloutTimeout > time.Duration(1<<63-1)-30*time.Second {
			return nil, errors.New("deployment.access: positive configured rollout timeout required")
		}
		requestTimeout, processTimeout = b.RolloutTimeout.String(), b.RolloutTimeout+30*time.Second
	}
	if !readOnlyGet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, processTimeout)
		defer cancel()
	}
	// Derive only Kubernetes metadata/collection identities for the reporter.
	// Never expose raw command arguments, request bodies or provider output.
	namespace := ""
	dry := false
	for i, arg := range args {
		if arg == "--namespace" && i+1 < len(args) {
			namespace = args[i+1]
		}
		if strings.HasPrefix(arg, "--namespace=") {
			namespace = strings.TrimPrefix(arg, "--namespace=")
		}
		if arg == "--dry-run=server" {
			dry = true
		}
	}
	action, resourceID := "", ""
	if len(args) >= 2 {
		switch args[0] {
		case "get":
			action = "read"
			if args[1] == environmentNamespacedInventoryKinds {
				action, resourceID = "inventory", environmentProgressResourceID("Namespace", "", namespace)
			} else if args[1] == environmentClusterInventoryKinds {
				action, resourceID = "inventory", environmentProgressResourceID("ClusterRole", "", "")
			} else {
				name := ""
				if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
					name = args[2]
				} else {
					action = "inventory"
				}
				resourceID = environmentProgressResourceID(args[1], namespace, name)
			}
		case "create", "replace", "delete":
			action = "apply"
			if dry {
				action = "dry-run"
			}
			var body deploymentplan.Object
			if json.Unmarshal(input, &body) == nil {
				if body["kind"] == "SelfSubjectAccessReview" {
					attributes := deploymentplan.Map(deploymentplan.Map(body["spec"])["resourceAttributes"])
					action = environmentProgressPermissionAction(deploymentplan.String(attributes["verb"]))
					resourceID = environmentProgressResourceID(deploymentplan.String(attributes["resource"]), deploymentplan.String(attributes["namespace"]), deploymentplan.String(attributes["name"]))
				} else {
					metadata := deploymentplan.Map(body["metadata"])
					resourceID = environmentProgressResourceID(deploymentplan.String(body["kind"]), deploymentplan.String(metadata["namespace"]), deploymentplan.String(metadata["name"]))
				}
			}
			if args[0] == "delete" {
				for _, arg := range args {
					if !strings.HasPrefix(arg, "--raw=") {
						continue
					}
					path, err := url.Parse(strings.TrimPrefix(arg, "--raw="))
					if err != nil || path.Host != "" || path.User != nil {
						continue
					}
					parts := strings.Split(strings.Trim(path.Path, "/"), "/")
					if len(parts) < 2 {
						continue
					}
					if path.Query().Get("dryRun") != "" {
						action = "dry-run"
					}
					namespace := ""
					for index := 0; index+1 < len(parts); index++ {
						if parts[index] == "namespaces" {
							namespace = parts[index+1]
						}
					}
					resourceID = environmentProgressResourceID(parts[len(parts)-2], namespace, parts[len(parts)-1])
				}
			}
		case "auth":
			if len(args) > 3 && args[1] == "can-i" {
				parts := strings.SplitN(args[3], "/", 2)
				name := ""
				if len(parts) == 2 {
					name = parts[1]
				}
				action, resourceID = environmentProgressPermissionAction(args[2]), environmentProgressResourceID(parts[0], namespace, name)
			}
		}
	}
	if waitCommand {
		action = "wait"
		for _, arg := range args[commandIndex+1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			parts := strings.SplitN(arg, "/", 2)
			if len(parts) == 2 {
				resourceID = environmentProgressResourceID(parts[0], namespace, parts[1])
				break
			}
		}
	}
	finished := environmentDeploymentOperation(ctx, action, resourceID)
	defer finished()
	path, cleanup, err := b.privateKubeconfig()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	argv := append([]string{"--kubeconfig", path, "--request-timeout=" + requestTimeout}, args...)
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		commandContext, cancel := context.WithTimeout(ctx, processTimeout)
		cmd := exec.CommandContext(commandContext, "kubectl", argv...)
		cmd.WaitDelay = time.Second
		cmd.Stdin = bytes.NewReader(input)
		var stdout environmentAPIOutput
		var stderr environmentPrivateAPIErrorOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		measured := beginDeploymentQuery(ctx, "kubectl")
		err := cmd.Run()
		measured(err)
		var failure *deploymentRuntimeError
		if readOnlyGet && stdout.exceeded {
			failure = &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "inventory response exceeds the native output bound"}
		} else if err != nil {
			failure = classifyDeploymentRuntimeError(commandContext, stderr.buffer.Bytes(), err, false)
		}
		cancel()
		if failure == nil {
			capturePreflightGateResponse(ctx, stdout.buffer.Bytes())
			return stdout.buffer.Bytes(), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !readOnlyGet || !transientDeploymentReadError(failure) || attempt+1 >= deploymentReadMaxAttempts {
			if failure.code != "KUBE_COMMAND_FAILED" {
				return nil, failure
			}
			return nil, errors.New("selected Kubernetes API operation failed; inspect sanitized admission events with environment-local access")
		}
		timer := time.NewTimer(readRuntime.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func environmentResourceArguments(id string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, errors.New("invalid Kubernetes resource identity")
	}
	args := []string{parts[0], parts[2]}
	if parts[1] != "" {
		args = append(args, "--namespace", parts[1])
	}
	return args, nil
}

func (b environmentKubernetesBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	object, failure := b.readOwner(ctx, id)
	if failure != nil {
		return nil, resourceReadBlocker(id, failure)
	}
	return object, nil
}

func resourceReadBlocker(resource string, failure *deploymentRuntimeError) error {
	next := "verify the selected frozen Kubernetes identity, get permission for this resource and API connectivity, then repeat qualification without replacing the original owner"
	switch failure.code {
	case "KUBE_FORBIDDEN":
		next = "grant the selected Kubernetes identity get permission for this resource, then repeat qualification without replacing the original owner"
	case "KUBE_UNAUTHORIZED":
		next = "restore the selected environment's Kubernetes authentication, then regenerate and qualify the frozen plan"
	case "KUBE_CONFIG_UNAVAILABLE":
		next = "restore the selected environment's private frozen Kubernetes identity and directory permissions, then regenerate and qualify the plan"
	case "KUBE_TOOL_MISSING":
		next = "restore the configured kubectl executable, then repeat qualification with the selected frozen Kubernetes identity"
	case "KUBE_INVALID_RESPONSE":
		next = "verify the Kubernetes API response and kubectl compatibility for this resource, then repeat qualification without replacing the original owner"
	case "KUBE_TIMEOUT", "KUBE_RATE_LIMITED", "KUBE_UNAVAILABLE":
		next = "verify cluster connectivity and API availability, then repeat qualification without replacing the original owner"
	case "CHECK_CANCELLED":
		next = "repeat the cancelled qualification to obtain fresh resource evidence"
	}
	return &deploymentplan.Blocker{ID: "deployment.access.resource", Resource: resource, Cause: "cannot read the selected resource [" + failure.code + "]", Next: next}
}

type environmentOwnerReadOutput struct {
	environmentAPIOutput
	cancel   context.CancelFunc
	exceeded bool
}

func (o *environmentOwnerReadOutput) Write(p []byte) (int, error) {
	n, err := o.environmentAPIOutput.Write(p)
	if err != nil {
		o.exceeded = true
		o.cancel()
	}
	return n, err
}

// Resource and storage fences require a fresh GET with the frozen identity.
// Bound native output and classify stderr in memory; only transient read
// failures may retry at most three attempts. Empty or incompatible observations
// are not retried, and every success still proves the selected original owner.
func (b environmentKubernetesBackend) readOwner(ctx context.Context, id string) (deploymentplan.Object, *deploymentRuntimeError) {
	if err := ctx.Err(); err != nil {
		return nil, classifyDeploymentRuntimeError(ctx, nil, err, false)
	}
	args, err := environmentResourceArguments(id)
	if err != nil {
		return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "invalid resource identity"}
	}
	finished := environmentDeploymentOperation(ctx, "read", id)
	defer finished()
	path, cleanup, err := b.privateKubeconfig()
	if err != nil {
		return nil, &deploymentRuntimeError{"KUBE_CONFIG_UNAVAILABLE", "frozen selected Kubernetes identity is unavailable"}
	}
	defer cleanup()
	runtime := newDeploymentCheckRuntime(ctx, b.Store.Environment)
	args = append(append([]string{"--kubeconfig", path, "--request-timeout=" + runtime.requestTimeout.String(), "get"}, args...), "--ignore-not-found=true", "-o", "json")
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, classifyDeploymentRuntimeError(ctx, nil, err, false)
		}
		commandContext, cancel := context.WithTimeout(ctx, runtime.processTimeout)
		cmd := exec.CommandContext(commandContext, lkeKubectl(), args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return os.ErrProcessDone
		}
		cmd.WaitDelay = time.Second
		stdout, stderr := environmentOwnerReadOutput{cancel: cancel}, environmentOwnerReadOutput{cancel: cancel}
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		var failure *deploymentRuntimeError
		if stdout.exceeded || stderr.exceeded {
			failure = &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "resource response exceeds the native output bound"}
		} else if err != nil {
			failure = classifyDeploymentRuntimeError(commandContext, stderr.buffer.Bytes(), err, false)
		}
		cancel()
		if failure == nil {
			raw := stdout.buffer.Bytes()
			if len(bytes.TrimSpace(raw)) == 0 {
				return nil, nil
			}
			var object deploymentplan.Object
			if json.Unmarshal(raw, &object) != nil || deploymentplan.ID(object) != id {
				return nil, &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "response does not match the selected resource"}
			}
			return object, nil
		}
		if !transientDeploymentReadError(failure) || attempt+1 >= deploymentReadMaxAttempts || ctx.Err() != nil {
			return nil, failure
		}
		timer := time.NewTimer(runtime.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, classifyDeploymentRuntimeError(ctx, nil, ctx.Err(), false)
		case <-timer.C:
		}
	}
}

func (b environmentKubernetesBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action == "retire" {
		return b.retire(ctx, body, dry)
	}
	verb := "replace"
	if action == "create" {
		verb = "create"
	} else if action != "update" {
		return nil, errors.New("unsupported Kubernetes mutation")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("cannot encode selected Kubernetes resource")
	}
	args := []string{verb, "-f", "-", "-o", "json"}
	if dry {
		args = append(args, "--dry-run=server")
	}
	raw, err = b.command(ctx, raw, args...)
	if err != nil {
		return nil, err
	}
	var object deploymentplan.Object
	if json.Unmarshal(raw, &object) != nil || deploymentplan.ID(object) != deploymentplan.ID(body) {
		return nil, errors.New("Kubernetes mutation returned an unexpected resource")
	}
	return object, nil
}

func (b environmentKubernetesBackend) Wait(ctx context.Context, resource deploymentplan.Resource) (resultErr error) {
	parts := strings.Split(resource.ID, "/")
	onDeleteOpenBao := environmentOnDeleteOpenBao(resource) && (resource.Action == "update" || resource.Action == "retain")
	waits := resource.Action == "retire" || onDeleteOpenBao
	switch parts[0] {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod", "PersistentVolumeClaim":
		waits = true
	}
	if waits {
		if b.RolloutTimeout <= 0 {
			return &deploymentplan.Blocker{ID: "deployment.configuration.rollout-timeout", Resource: resource.ID, Cause: "a positive shared rollout timeout is required", Next: "set DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS in the common architecture configuration and repeat full preflight"}
		}
		restore := environmentExecutionWaitProgress(ctx, resource.Phase)
		// Explicit returns replace this value; a panic cannot report readiness OK.
		resultErr = errors.New("resource readiness wait did not complete")
		defer func() { restore(resultErr) }()
		defer func() { resultErr = environmentRolloutFailure(ctx, resource, resultErr, b.RolloutTimeout) }()
	}
	waitTimeout := fmt.Sprintf("--timeout=%ds", int64(b.RolloutTimeout/time.Second))
	args := []string{"--namespace", parts[1]}
	if resource.Action == "retire" {
		args = append(args, "wait", "--for=delete", parts[0]+"/"+parts[2], waitTimeout)
		_, err := b.command(ctx, nil, args...)
		return err
	}
	if onDeleteOpenBao {
		return b.waitWithRolloutProgress(ctx, resource, func() error { return b.waitOnDeleteOpenBao(ctx, resource) })
	}
	switch parts[0] {
	case "Deployment", "StatefulSet", "DaemonSet":
		args = append(args, "rollout", "status", parts[0]+"/"+parts[2], waitTimeout)
	case "Job":
		args = append(args, "wait", "--for=condition=complete", "Job/"+parts[2], waitTimeout)
		if err := b.waitJobTerminal(ctx, resource, args); err != nil {
			return err
		}
		return b.verifySchemaOwnerJob(ctx, resource)
	case "Pod":
		args = append(args, "wait", "--for=condition=Ready", "Pod/"+parts[2], waitTimeout)
	case "PersistentVolumeClaim":
		args = append(args, "wait", "--for=jsonpath={.status.phase}=Bound", "PersistentVolumeClaim/"+parts[2], waitTimeout)
	default:
		return nil
	}
	return b.waitWithRolloutProgress(ctx, resource, func() error {
		_, err := b.command(ctx, nil, args...)
		return err
	})
}

func (b environmentKubernetesBackend) Inventory(ctx context.Context, stack string) ([]deploymentplan.Object, error) {
	raw, err := b.command(ctx, nil, "get", "namespace", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("cannot decode namespace inventory")
	}
	var namespaces []deploymentplan.Object
	for _, namespace := range list.Items {
		name := deploymentplan.String(deploymentplan.Map(namespace["metadata"])["name"])
		if !strings.HasPrefix(name, stack+"-") {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	// Namespace collections are independent reads. Preserve their API order,
	// stop new dispatch on failure/cancellation and join every started process.
	type namespaceResult struct {
		index int
		items []deploymentplan.Object
		err   error
	}
	workers := min(environmentDeploymentCheckConcurrency, len(namespaces))
	completed := make(chan namespaceResult, workers)
	itemsByIndex := make([][]deploymentplan.Object, len(namespaces))
	errorsByIndex := make([]error, len(namespaces))
	var joined sync.WaitGroup
	next, active := 0, 0
	dispatch := func() {
		index := next
		next++
		active++
		joined.Add(1)
		go func() {
			defer joined.Done()
			name := deploymentplan.String(deploymentplan.Map(namespaces[index]["metadata"])["name"])
			raw, err := b.command(ctx, nil, "get", environmentNamespacedInventoryKinds, "--namespace", name, "-o", "json")
			var collection struct {
				Items []deploymentplan.Object `json:"items"`
			}
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				} else {
					err = fmt.Errorf("deployment.inventory: cannot enumerate resources in %s", name)
				}
			} else if json.Unmarshal(raw, &collection) != nil {
				err = errors.New("cannot decode complete environment inventory")
			}
			completed <- namespaceResult{index, collection.Items, err}
		}()
	}
	for next < len(namespaces) && active < workers && ctx.Err() == nil {
		dispatch()
	}
	failed := false
	for active > 0 {
		result := <-completed
		active--
		itemsByIndex[result.index], errorsByIndex[result.index] = result.items, result.err
		failed = failed || result.err != nil
		if !failed && next < len(namespaces) && ctx.Err() == nil {
			dispatch()
		}
	}
	joined.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var resources []deploymentplan.Object
	for index, namespace := range namespaces {
		resources = append(resources, namespace)
		resources = append(resources, itemsByIndex[index]...)
	}
	raw, err = b.command(ctx, nil, "get", environmentClusterInventoryKinds, "-l", "rtk.realtek.com/stack="+stack, "-o", "json")
	if err != nil {
		return nil, err
	}
	list.Items = nil
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("cannot decode cluster ownership inventory")
	}
	resources = append(resources, list.Items...)
	// Helm admission owners may lack our stack label. Discover them by exact
	// Service endpoint, deduplicating resources already selected above.
	raw, err = b.command(ctx, nil, "get", "validatingwebhookconfiguration", "-o", "json")
	if err != nil {
		return nil, ingressAdmissionBlocker(stack, "admission webhook ownership inventory cannot be read")
	}
	list.Items = nil
	if json.Unmarshal(raw, &list) != nil {
		return nil, ingressAdmissionBlocker(stack, "admission webhook inventory is invalid")
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		seen[deploymentplan.ID(resource)] = true
	}
	for _, object := range list.Items {
		if environmentIngressAdmissionOwner(object, stack) && !seen[deploymentplan.ID(object)] {
			resources = append(resources, object)
		}
	}
	return resources, nil
}

func (b environmentKubernetesBackend) retire(ctx context.Context, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	meta := deploymentplan.Map(body["metadata"])
	uid, rv := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	kind := deploymentplan.String(body["kind"])
	api, plural := "", ""
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		api = "apis/apps/v1"
		plural = strings.ToLower(kind) + "s"
	case "Job", "CronJob":
		api = "apis/batch/v1"
		plural = strings.ToLower(kind) + "s"
	case "Pod", "Service", "ServiceAccount":
		api = "api/v1"
		plural = strings.ToLower(kind) + "s"
	case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		api = "apis/rbac.authorization.k8s.io/v1"
		plural = strings.ToLower(kind) + "s"
	case "NetworkPolicy":
		api = "apis/networking.k8s.io/v1"
		plural = "networkpolicies"
	case "Ingress":
		api = "apis/networking.k8s.io/v1"
		plural = "ingresses"
	case "IngressClass":
		api = "apis/networking.k8s.io/v1"
		plural = "ingressclasses"
	case "ValidatingWebhookConfiguration":
		api = "apis/admissionregistration.k8s.io/v1"
		plural = "validatingwebhookconfigurations"
	case "PodDisruptionBudget":
		api = "apis/policy/v1"
		plural = "poddisruptionbudgets"
	case "HorizontalPodAutoscaler":
		api = "apis/autoscaling/v2"
		plural = "horizontalpodautoscalers"
	default:
		return nil, errors.New("resource needs its separate durable-data or PKI retirement lifecycle")
	}
	if uid == "" || rv == "" {
		return nil, errors.New("retirement requires exact UID and current resource version")
	}
	path := "/" + api + "/"
	if namespace := deploymentplan.String(meta["namespace"]); namespace != "" {
		path += "namespaces/" + url.PathEscape(namespace) + "/"
	}
	path += plural + "/" + url.PathEscape(deploymentplan.String(meta["name"]))
	if dry {
		path += "?dryRun=All"
	}
	options := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "preconditions": map[string]string{"uid": uid, "resourceVersion": rv}}
	if dry {
		// With a request body, the API server decodes DeleteOptions from that
		// body rather than the URL query. Query-only dryRun can delete the Pod.
		options["dryRun"] = []string{"All"}
	}
	raw, _ := json.Marshal(options)
	if _, err := b.command(ctx, raw, "delete", "--raw="+path, "-f", "-"); err != nil {
		return nil, err
	}
	return body, nil
}
