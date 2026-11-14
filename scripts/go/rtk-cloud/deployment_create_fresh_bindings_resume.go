package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// An original bootstrap snapshot is immutable history. Later TLS resources and
// authenticated short-lived sessions may advance without replacing that file.
func validateFreshBootstrapBindingsContinuity(environment, parent, scope string, outputs []environmentCreateProductIdentity, original, current environmentIdentityBindings) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("original fresh bootstrap bindings differ from current permanent identities")
	}
	if validateEnvironmentIdentityBindings(original, environment) != nil || validateEnvironmentIdentityBindings(current, environment) != nil || original.ProductIdentities == nil || current.ProductIdentities == nil || original.Custody == nil || current.Custody == nil {
		return bad()
	}
	a, b := original.ProductIdentities, current.ProductIdentities
	enrollment := func(binding *environmentFreshProductBinding) string {
		if binding.EnrollmentSessionID != "" {
			return binding.EnrollmentSessionID
		}
		return binding.SessionID
	}
	if a.ParentSHA256 != parent || b.ParentSHA256 != parent || a.ScopeSHA256 != scope || b.ScopeSHA256 != scope || a.IssuerID != b.IssuerID || enrollment(a) != enrollment(b) || original.Custody.Generation != parent || deploymentplan.Digest(original.Custody) != deploymentplan.Digest(current.Custody) {
		return bad()
	}
	preserved := func(id string) bool { return original.Owners[id] != "" && original.Owners[id] == current.Owners[id] }
	if !preserved("StatefulSet/"+original.Stack+"-secrets/openbao") || len(outputs) == 0 {
		return bad()
	}
	for _, output := range outputs {
		if !preserved(output.ID) {
			return bad()
		}
	}
	for _, owner := range original.Custody.Owners {
		for _, kind := range []string{"Deployment", "StatefulSet"} {
			id := kind + "/" + owner.Namespace + "/" + owner.ID
			if original.Owners[id] != "" && !preserved(id) {
				return bad()
			}
		}
	}
	return nil
}

func writeOrReuseFreshBootstrapBindings(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, current environmentIdentityBindings) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("original fresh bootstrap bindings cannot be safely reused")
	}
	if e == nil || e.Store != store || e.Journal.Environment != cfg.Environment || e.Check(ctx) != nil {
		return bad()
	}
	relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "bootstrap", "bindings.json")
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) {
		return bad()
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		_, err = writeEnvironmentCreateBootstrapJSON(store, relative, current)
		return err
	} else if err != nil {
		return bad()
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return bad()
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return err
	}
	var original environmentIdentityBindings
	if createOpenBaoACKCompletionRead(store, relative, &original) != nil {
		return bad()
	}
	if err := validateFreshBootstrapBindingsContinuity(cfg.Environment, e.Journal.PlanSHA256, productIdentityScope(&program.Lifecycle), program.Lifecycle.Outputs, original, current); err != nil {
		return err
	}
	return e.Check(ctx)
}
