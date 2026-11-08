package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Source custody is a generation fact. Re-observing public TLS, an upgrade or
// health must not lose it, replace current resource UIDs or initialize keys.
func restoreEnvironmentCustodyBindings(ctx context.Context, cfg deploymentConfig, store secretStore, operation string, observed *environmentIdentityBindings) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("observed workload custody differs from its original environment or generation scope")
	}
	if observed == nil || observed.Environment != cfg.Environment || store.Environment != cfg.Environment {
		return bad()
	}
	generation := ""
	for _, pod := range observed.Pods {
		for _, c := range pod.Containers {
			if mqttFoundationEnv(c.Fields, "PKI_STATE_BACKEND") != "openbao" {
				continue
			}
			parts := strings.Split(mqttFoundationEnv(c.Fields, "PKI_STATE_RECORD_PREFIX"), "/")
			if cfg.Values["PKI_STATE_BACKEND"] != "openbao" || len(parts) != 3 || parts[0] != cfg.Environment || !environmentPreflightDigest.MatchString(parts[1]) || generation != "" && generation != parts[1] {
				return bad()
			}
			generation = parts[1]
		}
	}
	if generation == "" {
		if cfg.Values["PKI_STATE_BACKEND"] == "openbao" {
			return bad()
		}
		return nil
	}
	var binding *environmentCustodyBinding
	// Before bootstrap completion is published, only this admitted create
	// context can supply its initialized original scope and registered Root.
	if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); operation == "create" && ok && runtime.Custody != nil && runtime.ServiceCeremony != nil {
		if runtime.ExecutionSHA256 != generation || runtime.Custody.Generation != generation {
			return bad()
		}
		snapshot, _, err := readEnvironmentCustodyTransportState(cfg, store, generation)
		if err != nil || snapshot.ManagedRootSHA256 == "" {
			return bad()
		}
		copy := *runtime.Custody
		if copy.OpenBaoRootSHA256 != "" && copy.OpenBaoRootSHA256 != snapshot.ManagedRootSHA256 {
			return bad()
		}
		copy.OpenBaoRootSHA256 = snapshot.ManagedRootSHA256
		copy.ServiceRootSHA256 = runtime.ServiceCeremony.Root.RootFingerprint
		binding = &copy
	} else {
		var err error
		binding, err = readEnvironmentCustodyBootstrapBinding(cfg, store, generation)
		if err != nil {
			return err
		}
	}
	if !environmentPreflightDigest.MatchString(binding.ServiceRootSHA256) {
		return bad()
	}
	if observed.Custody != nil && deploymentplan.Digest(observed.Custody) != deploymentplan.Digest(binding) {
		return bad()
	}
	for _, pod := range observed.Pods {
		for _, c := range pod.Containers {
			if mqttFoundationEnv(c.Fields, "PKI_STATE_BACKEND") != "openbao" {
				continue
			}
			prefix := mqttFoundationEnv(c.Fields, "PKI_STATE_RECORD_PREFIX")
			matched := false
			for _, owner := range binding.Owners {
				if owner.Namespace != observed.Stack+"-"+pod.NamespaceSuffix || owner.ID != pod.Name || prefix != cfg.Environment+"/"+generation+"/"+owner.ID || pod.PodFields["serviceAccountName"] != owner.ServiceAccount {
					continue
				}
				if _, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, owner); err != nil {
					return err
				}
				if mqttFoundationEnv(c.Fields, "PKI_STATE_KV_MOUNT") != cfg.Values["PKI_STATE_KV_MOUNT"] || mqttFoundationEnv(c.Fields, "PKI_STATE_OPENBAO_ROLE") != environmentCustodyRoleName(prefix) || mqttFoundationEnv(c.Fields, "PKI_ENVIRONMENT") != cfg.Environment {
					return bad()
				}
				matched = true
			}
			if !matched {
				return bad()
			}
		}
	}
	observed.Custody = binding
	return nil
}
