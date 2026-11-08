package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// One workload may own several credential purposes. Alias names come from the
// maintained identity recipes, not a guess based on live PVC names or Pod UIDs.
func createEnvironmentCustodyBinding(cfg deploymentConfig, p environmentCreateBootstrapPlan, generation, managedRoot string) (*environmentCustodyBinding, error) {
	if err := validateEnvironmentCustodySettings(cfg.Values); err != nil {
		return nil, err
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil, nil
	}
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Stack != "video-cloud-"+cfg.Environment || !environmentPreflightDigest.MatchString(generation) || managedRoot != "" && !environmentPreflightDigest.MatchString(managedRoot) {
		return nil, environmentCreateBootstrapBlocker("custody inventory requires its original qualified environment and create generation")
	}
	expected, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(expected) != len(p.ProductIdentities) {
		return nil, environmentCreateBootstrapBlocker("custody product owners differ from maintained environment intent")
	}
	for i, product := range p.ProductIdentities {
		if product != expected[i] {
			return nil, environmentCreateBootstrapBlocker("custody product owner differs from its exact maintained identity recipe")
		}
	}
	owners := map[string]environmentCustodyOwnerBinding{}
	add := func(id, namespace, account, alias string) error {
		owner, exists := owners[id]
		if exists && (owner.Namespace != namespace || owner.ServiceAccount != account) {
			return environmentCreateBootstrapBlocker("native custody workload has conflicting namespace or service-account owners")
		}
		owner.ID, owner.Namespace, owner.ServiceAccount = id, namespace, account
		if slices.Contains(owner.StorageAliases, alias) {
			return environmentCreateBootstrapBlocker("native custody storage alias is duplicated")
		}
		owner.StorageAliases = append(owner.StorageAliases, alias)
		owners[id] = owner
		return nil
	}
	for _, role := range []string{"pki-controller", "certissuer", "account-manager"} {
		namespace := p.Stack + "-video-cloud"
		if role == "account-manager" {
			namespace = p.Stack + "-account-manager"
		}
		if err = add(role, namespace, role, environmentCreateServiceOwnerClaim(role)); err != nil {
			return nil, err
		}
	}
	for _, owner := range environmentCreateApplicationOwners() {
		if err = add(owner.Name, p.Stack+"-video-cloud", owner.Name, owner.Claim); err != nil {
			return nil, err
		}
	}
	if err = add("mqtt-pki", p.Stack+"-video-cloud", "mqtt-pki", "mqtt-pki-host-identity"); err != nil {
		return nil, err
	}
	productWorkloads := map[string]string{"mqtt": "video-cloud-mqttfoundation", "shadow": "video-cloud-shadowworker", "ota": "video-cloud-otaservice", "video-storage": "video-cloud-videostorage", "webrtc": "video-cloud-webrtcservice", "logger": "video-cloud-logingester"}
	for _, product := range p.ProductIdentities {
		workload := productWorkloads[strings.TrimPrefix(product.Subject, "service:")]
		if workload == "" {
			return nil, environmentCreateBootstrapBlocker("maintained product identity has no native workload custody owner")
		}
		if err = add(workload, p.Stack+"-video-cloud", workload, product.Claim); err != nil {
			return nil, err
		}
	}
	binding := &environmentCustodyBinding{Generation: generation, OpenBaoRootSHA256: managedRoot}
	for _, owner := range owners {
		slices.Sort(owner.StorageAliases)
		binding.Owners = append(binding.Owners, owner)
	}
	slices.SortFunc(binding.Owners, func(a, b environmentCustodyOwnerBinding) int { return strings.Compare(a.ID, b.ID) })
	return binding, nil
}

// This is configuration reconciliation under admitted creation ownership, not
// identity generation. Exact owned policies/roles are idempotent; a completed
// scope is read-only. No KV data, certificate or key is created here.
func configureEnvironmentCreateCustody(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, binding *environmentCustodyBinding) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	e := runtime.Execution
	if e == nil || binding == nil || binding.Generation != runtime.ExecutionSHA256 || !environmentPreflightDigest.MatchString(binding.Generation) || e.Store.Environment != cfg.Environment || len(binding.Owners) == 0 {
		return environmentCreateBootstrapBlocker("custody initialization requires its original admitted create owner and workload inventory")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	if err := validateEnvironmentCustodySettings(cfg.Values); err != nil {
		return err
	}
	mount := cfg.Values["PKI_STATE_KV_MOUNT"]
	intent := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": cfg.Environment, "generation": binding.Generation, "mount": mount, "cas_required": true, "owners": binding.Owners}
	body, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	intentSHA, err := environmentCreatePlanSHA256(body)
	if err != nil {
		return err
	}
	key := "workload-custody-initialize"
	proof, _ := json.Marshal(deploymentplan.Object{"scope_sha256": intentSHA})
	found, recorded, completed := false, false, false
	for _, entry := range e.Journal.Entries {
		if entry.Key != key {
			continue
		}
		if found || entry.Phase != "bootstrap" || entry.IntentSHA256 != intentSHA || entry.StartedAt.IsZero() || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State != "completed" && !entry.CompletedAt.IsZero() || entry.State == "completed" && entry.CompletedAt.IsZero() {
			return environmentCreateBootstrapBlocker("recorded custody initialization differs from the original selected scope")
		}
		found, recorded, completed = true, entry.State != "pending", entry.State == "completed"
		if recorded && deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(jsonRawObject(proof)) {
			return environmentCreateBootstrapBlocker("original custody completion receipt is inconsistent")
		}
	}
	if !found {
		if err = e.BeginEntry(key, "bootstrap", body, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err = writeEnvironmentCreateDerivedPrivateFile(e.Store, filepath.Join("deployment", "create", binding.Generation, "custody", "owners.json"), body); err != nil {
		return err
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original OpenBao custody configuration credential is unavailable")
	}
	input := append(bytes.TrimSpace(token), '\n')
	header := "set -eu\nIFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, header+"bao secrets list -format=json\n")
	var mounts deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &mounts) != nil {
		return environmentCreateBootstrapBlocker("original OpenBao custody mount inventory cannot be read")
	}
	actual := deploymentplan.Map(mounts[mount+"/"])
	if actual != nil && (actual["type"] != "kv" || deploymentplan.Map(actual["options"])["version"] != "2") {
		return environmentCreateBootstrapBlocker("selected custody mount already exists with another storage type; replacement is prohibited")
	}
	if completed && actual == nil {
		return environmentCreateBootstrapBlocker("completed generation custody mount is missing; no identity is regenerated")
	}
	var writes, reads strings.Builder
	writes.WriteString(header)
	reads.WriteString(header)
	if actual == nil {
		fmt.Fprintf(&writes, "bao secrets enable -path=%s -version=2 kv >/dev/null\n", mount)
	}
	fmt.Fprintf(&writes, "bao write %s/config cas_required=true >/dev/null\n", mount)
	fmt.Fprintf(&reads, "bao read -format=json %s/config\n", mount)
	for _, owner := range binding.Owners {
		policy, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, owner)
		if err != nil {
			return err
		}
		role := environmentCustodyRoleName(cfg.Environment + "/" + binding.Generation + "/" + owner.ID)
		// Retain the existing native bounded Kubernetes-auth profile. A batch
		// executes inside the checked OpenBao Pod instead of one tunnel per role.
		roleBody, _ := json.Marshal(deploymentplan.Object{"bound_service_account_names": []string{owner.ServiceAccount}, "bound_service_account_namespaces": []string{owner.Namespace}, "bound_service_account_namespace_selector": "", "audience": "openbao", "token_policies": []string{role}, "token_no_default_policy": true, "token_ttl": 900, "token_max_ttl": 900, "token_period": 0})
		fmt.Fprintf(&writes, "bao policy write %s - >/dev/null <<'RTK_CUSTODY_POLICY'\n%sRTK_CUSTODY_POLICY\nbao write auth/kubernetes/role/%s - >/dev/null <<'RTK_CUSTODY_ROLE'\n%s\nRTK_CUSTODY_ROLE\n", role, policy, role, roleBody)
		fmt.Fprintf(&reads, "bao read -format=json sys/policies/acl/%s\nbao read -format=json auth/kubernetes/role/%s\n", role, role)
	}
	if !completed {
		if err = e.Check(ctx); err != nil {
			return err
		}
		// A lost write response is resolved by exact native readback below. It
		// never authorizes issuing a new key or replaying an issuance request.
		_, _ = environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, writes.String())
	}
	observed, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, reads.String())
	if err != nil || verifyEnvironmentCustodyConfiguration(observed, cfg, *binding) != nil {
		return environmentCreateBootstrapBlocker("native custody configuration readback differs from its exact original mount, policy or workload role; no completion is recorded")
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	if !recorded {
		if err = e.RecordReceipt(key, proof); err != nil {
			return err
		}
	}
	if !completed {
		return e.CompleteEntry(key, time.Now().UTC())
	}
	return nil
}

func verifyEnvironmentCustodyConfiguration(raw []byte, cfg deploymentConfig, binding environmentCustodyBinding) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var kv struct {
		Data struct {
			CAS bool `json:"cas_required"`
		} `json:"data"`
	}
	if decoder.Decode(&kv) != nil || !kv.Data.CAS {
		return fmt.Errorf("custody KV requires conditional writes")
	}
	for _, owner := range binding.Owners {
		policy, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, binding, owner)
		if err != nil {
			return err
		}
		var acl struct {
			Data struct {
				Policy string `json:"policy"`
			} `json:"data"`
		}
		if decoder.Decode(&acl) != nil || strings.TrimSpace(acl.Data.Policy) != strings.TrimSpace(policy) {
			return fmt.Errorf("custody ACL differs")
		}
		var role struct {
			Data struct {
				Accounts   []string `json:"bound_service_account_names"`
				Namespaces []string `json:"bound_service_account_namespaces"`
				Selector   string   `json:"bound_service_account_namespace_selector"`
				Policies   []string `json:"token_policies"`
				Audience   string   `json:"audience"`
				NoDefault  bool     `json:"token_no_default_policy"`
				TTL        int      `json:"token_ttl"`
				MaxTTL     int      `json:"token_max_ttl"`
				Period     int      `json:"token_period"`
			} `json:"data"`
		}
		name := environmentCustodyRoleName(cfg.Environment + "/" + binding.Generation + "/" + owner.ID)
		if decoder.Decode(&role) != nil || !slices.Equal(role.Data.Accounts, []string{owner.ServiceAccount}) || !slices.Equal(role.Data.Namespaces, []string{owner.Namespace}) || role.Data.Selector != "" || !slices.Equal(role.Data.Policies, []string{name}) || role.Data.Audience != "openbao" || !role.Data.NoDefault || role.Data.TTL != 900 || role.Data.MaxTTL != 900 || role.Data.Period != 0 {
			return fmt.Errorf("custody Kubernetes role differs")
		}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("unexpected custody configuration output")
	}
	return nil
}
