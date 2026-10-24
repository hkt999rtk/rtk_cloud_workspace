package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const initialListenerPolicy = "path \"*\" { capabilities = [\"deny\"] }\n"

func initialListenerAuthNames(cfg deploymentConfig) (string, string) {
	return "create-initial-listener-" + cfg.Environment, "pki-controller-" + cfg.Environment
}

func verifyInitialListenerAuthenticationAbsent(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return err
	}
	_, role := initialListenerAuthNames(cfg)
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nset +e; bao list -format=json auth/kubernetes/role; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]\n")
	if err != nil || !initialListenerRoleAbsent(raw, role) {
		return createProviderRecoveryBlock("initial listener role is occupied or absence is unproven; no overwrite allowed")
	}
	policy, _ := initialListenerAuthNames(cfg)
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy list -format=json\n")
	var names []string
	if err != nil || json.Unmarshal(raw, &names) != nil || slices.Contains(names, policy) {
		return createProviderRecoveryBlock("initial deny-only policy is occupied or absence is unproven")
	}
	return nil
}

func initialListenerRoleAbsent(raw []byte, role string) bool {
	var names []string
	if json.Unmarshal(raw, &names) == nil {
		return !slices.Contains(names, role)
	}
	var empty map[string]json.RawMessage
	return json.Unmarshal(raw, &empty) == nil && len(empty) == 0
}

func configureInitialListenerAuthentication(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	if err := verifyNoInitialIssuers(ctx, cfg, runtime.Backend); err != nil {
		return err
	}
	if err := verifyInitialListenerAuthenticationAbsent(ctx, cfg, runtime); err != nil {
		return err
	}
	policy, role := initialListenerAuthNames(cfg)
	if err := installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, policy, initialListenerPolicy); err != nil {
		return err
	}
	return configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, role, []string{"pki-controller", "certissuer"}, []string{policy})
}

func verifyInitialListenerAuthentication(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, separated ...bool) error {
	_, role := initialListenerAuthNames(cfg)
	if len(separated) == 1 && separated[0] {
		if err := verifyInitialListenerAuthenticationRole(ctx, cfg, runtime, role, []string{"pki-controller"}); err != nil {
			return err
		}
		return verifyInitialListenerAuthenticationRole(ctx, cfg, runtime, "pki-initial-certissuer-"+cfg.Environment, []string{"certissuer"})
	}
	return verifyInitialListenerAuthenticationRole(ctx, cfg, runtime, role, []string{"certissuer", "pki-controller"})
}

func verifyInitialListenerAuthenticationRole(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, role string, accounts []string) error {
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return err
	}
	policy, _ := initialListenerAuthNames(cfg)
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+role+"\n")
	var response struct {
		Data struct {
			Accounts   []string `json:"bound_service_account_names"`
			Namespaces []string `json:"bound_service_account_namespaces"`
			Policies   []string `json:"token_policies"`
			Audience   string   `json:"audience"`
			NoDefault  bool     `json:"token_no_default_policy"`
			TTL        float64  `json:"token_ttl"`
			MaxTTL     float64  `json:"token_max_ttl"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &response) != nil {
		return createProviderRecoveryBlock("recorded initial listener role is unreadable")
	}
	slices.Sort(response.Data.Accounts)
	if !slices.Equal(response.Data.Accounts, accounts) || !slices.Equal(response.Data.Namespaces, []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}) || !slices.Equal(response.Data.Policies, []string{policy}) || response.Data.Audience != "openbao" || !response.Data.NoDefault || response.Data.TTL != 900 || response.Data.MaxTTL != 900 {
		return createProviderRecoveryBlock("initial listener role exceeds its exact namespace, account, policy or lifetime scope")
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+policy+"\n")
	if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(initialListenerPolicy) {
		return createProviderRecoveryBlock("initial listener policy is not the original deny-only policy")
	}
	return nil
}

func separateInitialCertIssuerAuthentication(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (environmentCreateBootstrapRuntime, error) {
	if runtime.Execution == nil || apply == nil {
		return runtime, createProviderRecoveryBlock("original fenced listener execution required for initial auth separation")
	}
	if err := verifyNoInitialIssuers(ctx, cfg, runtime.Backend); err != nil {
		return runtime, err
	}
	if err := verifyInitialListenerAuthentication(ctx, cfg, runtime); err != nil {
		return runtime, err
	}
	policy, _ := initialListenerAuthNames(cfg)
	role := "pki-initial-certissuer-" + cfg.Environment
	updated, selected, err := initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil {
		return runtime, err
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return runtime, err
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao list -format=json auth/kubernetes/role\n")
	if err != nil || !initialListenerRoleAbsent(raw, role) {
		return runtime, createProviderRecoveryBlock("separate initial CertIssuer role is already occupied or unproven")
	}
	if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, role, []string{"certissuer"}, []string{policy}); err != nil {
		return runtime, err
	}
	if err := apply(ctx, "certissuer-initial-auth-separation", selected); err != nil {
		return runtime, err
	}
	if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, []string{policy}); err != nil {
		return runtime, err
	}
	runtime.InitialListeners = updated
	return runtime, nil
}

func initialCertIssuerAuthDefinitions(cfg deploymentConfig, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, []deploymentplan.Definition, error) {
	var updated, selected []deploymentplan.Definition
	role := "pki-initial-certissuer-" + cfg.Environment
	for _, d := range definitions {
		copy := deploymentplan.Definition{Object: deploymentplan.Clone(d.Object), Phase: d.Phase}
		if deploymentplan.ID(copy.Object) == "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/certissuer" {
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(copy.Object)["containers"])[0])
			if mqttFoundationEnv(container, "OPENBAO_KUBERNETES_ROLE") != "pki-controller-"+cfg.Environment {
				return nil, nil, createProviderRecoveryBlock("original CertIssuer bootstrap auth role changed")
			}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"OPENBAO_KUBERNETES_ROLE": role}))
			selected = append(selected, copy)
		}
		updated = append(updated, copy)
	}
	if len(selected) != 1 {
		return nil, nil, createProviderRecoveryBlock("exact original initial CertIssuer owner required")
	}
	return updated, selected, nil
}

func loadInitialListenerCheckpoint(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, accepted ...bool) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, journal, createProviderRecoveryBlock("initial listener failure lacks six exact completed-write owners and original intent")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	var verifiedOwners map[string]string
	if len(accepted) == 1 && accepted[0] {
		var originalSHA, stage string
		for _, entry := range e.Journal.Entries {
			if entry.Key == "kubernetes/pki-initial-listeners" && entry.State == "completed" {
				var receipt struct {
					Original string `json:"original_plan_sha256"`
					Stage    string `json:"actual_retained_verification_stage"`
				}
				if json.Unmarshal(entry.Receipt, &receipt) == nil {
					originalSHA, stage = receipt.Original, receipt.Stage
				}
			}
		}
		var retainedSHA string
		for _, entry := range e.Journal.Entries {
			if entry.Key == stage && entry.State == "completed" {
				var receipt struct {
					Plan string `json:"plan_sha256"`
				}
				if json.Unmarshal(entry.Receipt, &receipt) == nil {
					retainedSHA = receipt.Plan
				}
			}
		}
		var retained deploymentplan.Plan
		var retainedJournal deploymentplan.Journal
		if !environmentPreflightDigest.MatchString(retainedSHA) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", retainedSHA, "plan.json"), &retained) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", retainedSHA, "journal.json"), &retainedJournal) != nil || retained.SHA256() != retainedSHA || retainedJournal.PlanSHA256 != retainedSHA || !retainedJournal.Accepted || retainedJournal.InFlight != "" || retainedJournal.Failed != "" || len(retained.Resources) != 6 || len(retainedJournal.Owners) != 6 || len(retainedJournal.Completed) != 6 {
			return bad()
		}
		sha = originalSHA
		verifiedOwners = retainedJournal.Owners
		if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || plan.SHA256() != sha {
			return bad()
		}
		for _, r := range plan.Resources {
			found := false
			for _, v := range retained.Resources {
				if v.ID == r.ID && v.Action == "retain" && deploymentplan.Digest(v.Desired) == deploymentplan.Digest(r.Desired) {
					found = true
				}
			}
			if !found {
				return bad()
			}
		}
	}
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || journal.Accepted || journal.InFlight != "" || len(plan.Resources) != 6 || len(journal.Owners) != 6 || journal.Failed != "Deployment/"+p.Plan.Stack+"-video-cloud/pki-controller" && journal.Failed != "deployment.post-deploy.workloads" || len(journal.Completed) < 5 {
		return bad()
	}
	wanted := map[string]bool{}
	for _, name := range []string{"pki-controller", "certissuer"} {
		for _, kind := range []string{"Deployment", "Service", "ServiceAccount"} {
			wanted[kind+"/"+p.Plan.Stack+"-video-cloud/"+name] = true
		}
	}
	for _, r := range plan.Resources {
		if !wanted[r.ID] || r.Action != "create" || journal.Owners[r.ID] == "" || verifiedOwners != nil && verifiedOwners[r.ID] != journal.Owners[r.ID] {
			return bad()
		}
		delete(wanted, r.ID)
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil {
		return bad()
	}
	matched := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/pki-initial-listeners" && (entry.State == "pending" || len(accepted) == 1 && accepted[0] && entry.State == "completed") && entry.IntentSHA256 == intentSHA {
			matched = true
		}
	}
	if !matched {
		return bad()
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = sha, deploymentplan.Digest(journal)
	return &plan, journal, nil
}
