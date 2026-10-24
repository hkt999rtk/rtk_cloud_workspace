package main

import (
	"fmt"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productSessionRenewalStep struct {
	Key, Phase, Suffix string
	Intent             any
	Receipt            any
	Definitions        []deploymentplan.Definition
}

// Return exact typed operations; the normal reader checks their lifecycle,
// chronological interleaving, effective deadline and original lease evidence.
func productSessionRenewalProgressSpecs(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) ([]productSessionRenewalStep, error) {
	if err := validateProductSessionRenewalSource(store, e, l, s); err != nil {
		return nil, err
	}
	recipes, err := productSessionRenewalStageRecipes(store, e, l, s)
	if err != nil {
		return nil, err
	}
	env, stack := l.Bootstrap.Environment, l.Bootstrap.Stack
	policy := "rtk-" + env + "-service-bootstrap-" + s.SessionID
	makePolicy := func(name, hash string) productSessionRenewalStep {
		return productSessionRenewalStep{Key: "openbao-policy-" + name, Phase: "bootstrap", Intent: deploymentplan.Object{"environment": env, "policy": name, "policy_sha256": hash}}
	}
	makeRole := func(name string, accounts, policies []string) productSessionRenewalStep {
		intent := deploymentplan.Object{"role": name, "namespace": stack + "-video-cloud", "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
		return productSessionRenewalStep{Key: "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12], Phase: "bootstrap", Intent: intent}
	}
	makeChild := func(suffix string) productSessionRenewalStep {
		return productSessionRenewalStep{Key: "kubernetes/" + productSessionRenewalStage(l, s, suffix), Phase: productSessionRenewalStage(l, s, suffix), Suffix: suffix, Definitions: recipes[suffix]}
	}
	account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
	out := []productSessionRenewalStep{makePolicy(policy, s.SignerPolicySHA256), makeRole(account, []string{account}, []string{policy}), makeChild("job"), makeChild("custody"), makeChild("credentials")}
	cleanup := "create-" + s.SessionID + "-cleanup-controller"
	text := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+policy)
	out = append(out, makePolicy(cleanup, environmentPreflightBytesSHA256([]byte(text))))
	var policies []string
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-policy-"+cleanup {
			break
		}
		if entry.State == "completed" && strings.HasPrefix(entry.Key, "openbao-policy-create-") && strings.HasSuffix(entry.Key, "-controller") {
			policies = append(policies, strings.TrimPrefix(entry.Key, "openbao-policy-"))
		}
	}
	policies = append(policies, cleanup)
	slices.Sort(policies)
	out = append(out, makeRole("pki-controller-"+env, []string{"pki-controller"}, policies), makeChild("controls"))
	return out, nil
}

func productSessionRenewalFinalizerSpecs(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal) ([]productSessionRenewalStep, error) {
	defs, err := productSessionRenewalOriginalRetireDefinitions(store, e, l, r)
	if err != nil {
		return nil, err
	}
	out := make([]productSessionRenewalStep, 0, 3)
	for _, definition := range defs {
		out = append(out, productSessionRenewalCopyRetireSpec(r, definition))
	}
	cfg := deploymentConfig{Environment: l.Bootstrap.Environment}
	policies, err := productSessionRenewalPoliciesToRetire(store, e, l, r)
	if err != nil {
		return nil, err
	}
	for _, prior := range policies {
		intent := productSessionRenewalRetirePolicyIntent(cfg, prior)
		receipt := deploymentplan.Clone(intent)
		receipt["absent"] = true
		out = append(out, productSessionRenewalStep{Key: productSessionRenewalRetirePolicyKey(prior), Phase: "bootstrap", Intent: intent, Receipt: receipt})
	}
	return out, nil
}
