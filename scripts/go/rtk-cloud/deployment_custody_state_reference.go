package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A durable identity is a logical record under its original workload owner.
// Pod/PVC UIDs and mutable KV versions are deliberately not storage authority.
type environmentCustodyStateReference struct {
	Backend        string `json:"backend"`
	Generation     string `json:"generation"`
	Owner          string `json:"owner"`
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"service_account"`
	Mount          string `json:"mount"`
	RecordKey      string `json:"record_key"`
	StatePath      string `json:"state_path"`
	RootSHA256     string `json:"root_sha256"`
}

func environmentCustodyRecordKey(prefix, path string) string {
	sum := sha256.Sum256([]byte(path))
	return prefix + "/" + hex.EncodeToString(sum[:])
}

func newEnvironmentCustodyStateReference(cfg deploymentConfig, binding *environmentCustodyBinding, namespace, alias, path, root string) (*environmentCustodyStateReference, error) {
	bad := func() (*environmentCustodyStateReference, error) {
		return nil, environmentCreateBootstrapBlocker("identity record requires its original environment, generation, workload scope and credential authority")
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" || binding == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || !environmentPreflightDigest.MatchString(root) {
		return bad()
	}
	var selected *environmentCustodyOwnerBinding
	for _, owner := range binding.Owners {
		aliases := owner.StorageAliases
		if len(aliases) == 0 {
			aliases = []string{owner.ID}
		}
		if owner.Namespace != namespace || !slices.Contains(aliases, alias) {
			continue
		}
		if selected != nil {
			return bad()
		}
		copy := owner
		selected = &copy
	}
	if selected == nil {
		return bad()
	}
	if _, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, *selected); err != nil {
		return nil, err
	}
	return &environmentCustodyStateReference{
		Backend: "openbao-kv-v2", Generation: binding.Generation, Owner: selected.ID,
		Namespace: namespace, ServiceAccount: selected.ServiceAccount,
		Mount: cfg.Values["PKI_STATE_KV_MOUNT"], StatePath: path, RootSHA256: root,
		RecordKey: environmentCustodyRecordKey(cfg.Environment+"/"+binding.Generation+"/"+selected.ID, path),
	}, nil
}

func validateEnvironmentCustodyStateReference(actual *environmentCustodyStateReference, cfg deploymentConfig, binding *environmentCustodyBinding, namespace, alias, path, root string) error {
	expected, err := newEnvironmentCustodyStateReference(cfg, binding, namespace, alias, path, root)
	if err != nil {
		return err
	}
	if actual == nil || deploymentplan.Digest(actual) != deploymentplan.Digest(expected) {
		return environmentCreateBootstrapBlocker("identity record differs from its original workload custody reference")
	}
	return nil
}

func validateEnvironmentProductIdentityStorage(cfg deploymentConfig, l *createNormalTailProductLifecycle, output environmentCreateProductIdentity, receipt environmentProductIdentityReceipt) error {
	if err := validateEnvironmentCustodySettings(cfg.Values); err != nil {
		return err
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		if receipt.Custody != nil || !environmentBootstrapOwnerUUID.MatchString(receipt.ClaimUID) {
			return environmentCreateBootstrapBlocker("product storage receipt differs from its selected original PVC backend")
		}
		return nil
	}
	if l == nil || receipt.ClaimUID != "" {
		return environmentCreateBootstrapBlocker("KV product receipt must name its original logical record without a physical claim UID")
	}
	binding, err := createEnvironmentCustodyBinding(cfg, l.Bootstrap, l.ParentSHA256, "")
	if err != nil {
		return err
	}
	return validateEnvironmentCustodyStateReference(receipt.Custody, cfg, binding, cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", output.Claim, environmentCreateServiceOwnerIdentityPath(strings.TrimPrefix(output.Subject, "service:"), "client.json"), l.Root.RootFingerprint)
}
