package main

import (
	"bytes"
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

func verifyProductSessionRenewalNextAuthorities(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, previous *productSessionRenewal) error {
	bp, err := readCreateNormalTailPreparationPlan(store, runtime.Execution, "product-identities-"+productIdentityScope(l)[:12]+"-service-session")
	if err != nil || bp == nil || len(bp.Resources) != 3 {
		return productSessionRenewalBlock("original claim account intent")
	}
	bj, exists, err := bp.ReadExecution(store.Root)
	if err != nil || !exists || bj.InFlight != "" {
		return productSessionRenewalBlock("original claim account custody")
	}
	for _, r := range bp.Resources[:2] {
		actual, er := runtime.Backend.Read(ctx, r.ID)
		if er != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != bj.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return productSessionRenewalBlock("original native claim or service account replaced")
		}
	}
	trustID, _, _, err := productIdentityOpenBaoTrustPins(l)
	if err != nil {
		return err
	}
	trust, err := runtime.Backend.Read(ctx, trustID)
	if err != nil || validateProductIdentityOpenBaoTrust(l, trust, time.Now().UTC()) != nil {
		return productSessionRenewalBlock("same managed signer trust")
	}
	policy, err := readProductSessionRenewalPolicy(ctx, cfg, runtime, previous.Installed.Ceremony.Intermediate.ID)
	if err != nil || environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)) != previous.Source.SignerPolicySHA256 {
		return productSessionRenewalBlock("same native signer policy")
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return productSessionRenewalBlock("original signer custody")
	}
	oldName := "rtk-" + cfg.Environment + "-service-bootstrap-" + previous.Installed.Ceremony.BootstrapSessionID
	current, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+oldName+"\n")
	if err != nil || strings.TrimSpace(string(current)) != strings.TrimSpace(policy.ServiceClientSignerPolicy) {
		return productSessionRenewalBlock("previous exact signer policy body")
	}
	account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
	policies, err := readProductRolePolicies(ctx, cfg, runtime, account, account)
	if err != nil || !slices.Equal(policies, []string{oldName}) {
		return productSessionRenewalBlock("previous exact bootstrap role")
	}
	controller, err := readProductRolePolicies(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, "pki-controller")
	if err != nil || !slices.Equal(controller, environmentCreateControllerPolicies(runtime)) {
		return productSessionRenewalBlock("previous exact cleanup controller role")
	}
	return runtime.Execution.Check(ctx)
}
