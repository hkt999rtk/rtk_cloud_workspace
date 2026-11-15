package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Use the selected cluster and CA, but discard the operator authentication
// completely before producing a per-service Kubernetes identity.
func ciRolloutKubeconfig(original, environment, token string) ([]byte, error) {
	if environment != "dev" || token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\x00\r\n\t ") {
		return nil, errors.New("CI rollout: valid restricted dev identity required")
	}
	backend, err := newEnvironmentKubernetesBackend(original, environment)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if yaml.Unmarshal(backend.KubeconfigContent, &config) != nil {
		return nil, errors.New("CI rollout: selected Kubernetes configuration is invalid")
	}
	users, ok := config["users"].([]any)
	if !ok || len(users) != 1 {
		return nil, errors.New("CI rollout: selected Kubernetes identity is ambiguous")
	}
	user := deploymentplan.Map(users[0])
	if user == nil {
		return nil, errors.New("CI rollout: selected Kubernetes identity is invalid")
	}
	user["user"] = map[string]any{"token": token}
	result, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	if _, err = newEnvironmentKubernetesBackend(string(result), environment); err != nil {
		return nil, err
	}
	return result, nil
}

// Check exact named resource access and reject direct broad Secret/Pod access.
// Kubernetes RBAC cannot constrain individual fields of a Deployment patch.
func ciRolloutVerifyAccess(ctx context.Context, backend environmentKubernetesBackend, identityNamespace, repo string, targets []ciRolloutTarget) error {
	if !ciRepositoryName.MatchString(repo) || identityNamespace == "" || len(targets) == 0 {
		return errors.New("CI rollout: restricted access targets required")
	}
	path, cleanup, err := backend.privateKubeconfig()
	if err != nil {
		return err
	}
	defer cleanup()
	check := func(namespace, verb, resource string, allowed bool) error {
		cmd := exec.CommandContext(ctx, lkeKubectl(), "--kubeconfig", path, "--request-timeout=15s", "auth", "can-i", verb, resource, "--namespace", namespace)
		raw, err := cmd.Output()
		answer := strings.TrimSpace(string(raw))
		if allowed && err == nil && answer == "yes" {
			return nil
		}
		var exit *exec.ExitError
		if !allowed && errors.As(err, &exit) && exit.ExitCode() == 1 && answer == "no" {
			return nil
		}
		return errors.New("CI rollout: restricted identity permissions do not match selected targets")
	}
	type accessRequest struct {
		namespace, verb, resource string
		allowed                   bool
	}
	var requests []accessRequest
	seen := map[string]bool{}
	namespaces := map[string]bool{identityNamespace: true}
	for _, target := range targets {
		namespaces[target.Namespace] = true
		key := target.Namespace + "/" + target.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, verb := range []string{"get", "list", "watch", "patch"} {
			requests = append(requests, accessRequest{target.Namespace, verb, "deployments.apps/" + target.Name, true})
		}
	}
	stateName, err := ciRolloutStateName(repo)
	if err != nil {
		return err
	}
	for _, rule := range []struct{ verb, resource string }{{"get", "leases.coordination.k8s.io/rtk-deployment-execution"}, {"create", "leases.coordination.k8s.io"}, {"delete", "leases.coordination.k8s.io/rtk-deployment-execution"}, {"get", "configmaps/" + stateName}, {"patch", "configmaps/" + stateName}} {
		requests = append(requests, accessRequest{identityNamespace, rule.verb, rule.resource, true})
	}
	for namespace := range namespaces {
		for _, rule := range []struct{ verb, resource string }{{"get", "secrets"}, {"create", "pods"}, {"patch", "deployments.apps/rtk-ci-unrelated-probe"}} {
			requests = append(requests, accessRequest{namespace, rule.verb, rule.resource, false})
		}
	}
	jobs := make(chan accessRequest, len(requests))
	for _, request := range requests {
		jobs <- request
	}
	close(jobs)
	var wg sync.WaitGroup
	errorsFound := make(chan error, 1)
	for i := 0; i < min(8, len(requests)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for request := range jobs {
				if err := check(request.namespace, request.verb, request.resource, request.allowed); err != nil {
					select {
					case errorsFound <- err:
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errorsFound:
		return err
	default:
	}

	return nil
}
