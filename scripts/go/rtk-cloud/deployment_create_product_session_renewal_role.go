package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A role has no native UID. Reconcile only an exact pending original intent:
// either its desired role is present, or the latest authenticated earlier role
// is still present. A successful list can establish absence for a new role.
func reconcileProductSessionRenewalRole(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, key string, intent deploymentplan.Object) (bool, error) {
	bad := func() (bool, error) {
		return false, productSessionRenewalBlock("pending auth role has no exact known native outcome")
	}
	name := deploymentplan.String(intent["role"])
	var previous deploymentplan.Object
	seen := false
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key == key {
			if seen {
				return bad()
			}
			seen = true
			continue
		}
		if seen || !strings.HasPrefix(entry.Key, "openbao-role-"+name+"-") {
			continue
		}
		var receipt deploymentplan.Object
		if entry.Phase != "bootstrap" || entry.State != "completed" && entry.State != "allocated" || entry.StartedAt.IsZero() || (entry.State == "completed") == entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &receipt) != nil || !productSessionRenewalRoleIntent(receipt, name, cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud") || entry.IntentSHA256 != deploymentplan.Digest(receipt) || entry.Key != "openbao-role-"+name+"-"+deploymentplan.Digest(receipt)[:12] {
			return bad()
		}
		previous = receipt
	}
	if !seen {
		return bad()
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return bad()
	}
	input := append(bytes.TrimSpace(token), '\n')
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao list -format=json auth/kubernetes/role\n")
	var names []string
	if err != nil || json.Unmarshal(raw, &names) != nil || names == nil {
		return bad()
	}
	if !slices.Contains(names, name) {
		if previous != nil {
			return bad()
		}
		return true, runtime.Execution.Check(ctx)
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+name+"\n")
	var observed struct {
		Data struct {
			Accounts   []string `json:"bound_service_account_names"`
			Namespaces []string `json:"bound_service_account_namespaces"`
			Policies   []string `json:"token_policies"`
			Audience   string   `json:"audience"`
			NoDefault  bool     `json:"token_no_default_policy"`
			TTL        int      `json:"token_ttl"`
			MaxTTL     int      `json:"token_max_ttl"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &observed) != nil || !slices.Equal(observed.Data.Namespaces, []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}) || observed.Data.Audience != "openbao" || !observed.Data.NoDefault || observed.Data.TTL != 900 || observed.Data.MaxTTL != 900 {
		return bad()
	}
	actual := deploymentplan.Object{"role": name, "namespace": observed.Data.Namespaces[0], "service_accounts": observed.Data.Accounts, "policies": observed.Data.Policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	if !productSessionRenewalRoleIntent(actual, name, cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud") {
		return bad()
	}
	if productSessionRenewalRolesEqual(actual, intent) {
		return false, runtime.Execution.Check(ctx)
	}
	if previous != nil && productSessionRenewalRolesEqual(actual, previous) {
		return true, runtime.Execution.Check(ctx)
	}
	return bad()
}

func productSessionRenewalRoleIntent(o deploymentplan.Object, name, namespace string) bool {
	if len(o) != 7 || o["role"] != name || o["namespace"] != namespace || o["audience"] != "openbao" || o["ttl"] != "15m" || o["no_default_policy"] != true {
		return false
	}
	for _, key := range []string{"service_accounts", "policies"} {
		v, err := productSessionRenewalRoleStrings(o[key])
		if err != nil || len(v) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, s := range v {
			if !secretEnvironmentPattern.MatchString(s) || seen[s] {
				return false
			}
			seen[s] = true
		}
	}
	return true
}

func productSessionRenewalRolesEqual(a, b deploymentplan.Object) bool {
	a, b = deploymentplan.Clone(a), deploymentplan.Clone(b)
	for _, key := range []string{"service_accounts", "policies"} {
		x, _ := productSessionRenewalRoleStrings(a[key])
		y, _ := productSessionRenewalRoleStrings(b[key])
		slices.Sort(x)
		slices.Sort(y)
		a[key], b[key] = x, y
	}
	return deploymentplan.Digest(a) == deploymentplan.Digest(b)
}

func productSessionRenewalRoleStrings(v any) ([]string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var values []string
	err = json.Unmarshal(raw, &values)
	return values, err
}
