package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Legacy temporary OpenBao TLS included a bare search-path name. Managed PKI
// uses exact DNS policy; preserve every qualified dotted alias, omitting only
// the bare name that the native reservation contract refuses.
func correctedOpenBaoDNSPolicy(names []string) ([]string, error) {
	if slices.Index(names, "openbao") < 0 {
		return nil, createProviderRecoveryBlock("original refused OpenBao DNS policy differs")
	}
	result := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == "openbao" })
	if len(result) != 7 {
		return nil, createProviderRecoveryBlock("original OpenBao DNS aliases differ")
	}
	return result, nil
}

func prepareOpenBaoIntermediateDNSRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	a, _, err := loadAdmittedCreateRecovery(store, e, r.CompletedTransportReview)
	if err != nil {
		return err
	}
	root, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "openbao_tls", true)
	if err != nil {
		return err
	}
	r.OpenBaoRoot, r.OpenBaoOperation = &root, &op
	mqtt, _, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "mqtt", true)
	if err != nil {
		return err
	}
	r.MQTTRoot = &mqtt
	i, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, mqtt, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "mqtt"))
	if err != nil {
		return err
	}
	r.MQTTIntermediate = &i
	oldPolicy := findEnvironmentCreateDomainPolicy(a.Plan.Plan.Bootstrap, "openbao_tls")
	old := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: "openbao_tls", Kind: "intermediate", ParentID: root.ID, ServerDNSNames: oldPolicy.ServerDNSNames, ServiceClientIDs: oldPolicy.ServiceClientIDs}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	originalIntent, err := environmentCreateIssuerIntentSHA(old)
	if err != nil || entry.Key != "pki-reserve-openbao_tls-intermediate" || entry.State != "pending" || len(entry.Receipt) != 0 || entry.IntentSHA256 != originalIntent {
		return createProviderRecoveryBlock("only the original refused intermediate reservation may be corrected")
	}
	names, err := correctedOpenBaoDNSPolicy(old.ServerDNSNames)
	if err != nil {
		return err
	}
	for index := range p.Plan.Bootstrap.Domains {
		if p.Plan.Bootstrap.Domains[index].Domain == "openbao_tls" {
			p.Plan.Bootstrap.Domains[index].ServerDNSNames = names
		}
	}
	p.Materials["accepted-service-core-openbao-dns-source"] = deploymentplan.Digest(a)
	p.Materials["accepted-service-core-openbao-dns-original-request"] = deploymentplan.Digest(old)
	corrected := old
	corrected.ServerDNSNames = names
	p.Materials["accepted-service-core-openbao-dns-corrected-request"] = deploymentplan.Digest(corrected)
	plan, j, err := acceptedCreateStage(store, e, "kubernetes/openbao-root-trust-name-recovery-"+r.CompletedTransportReview)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	var defs []deploymentplan.Definition
	for _, v := range plan.Resources {
		actual, err := b.Read(ctx, v.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
			return createProviderRecoveryBlock("accepted OpenBao trust owner changed")
		}
		defs = append(defs, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired), Retain: true})
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("openbao-original-active-root", defs)}, defs, live)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-openbao-trust-forward-plan"] = r.Candidate.SHA256()
	bindAcceptedTransportRootMaterials(p.Materials)
	if r.PostServiceRenewal {
		image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
		if err != nil {
			return err
		}
		r.GrantImage = image
		p.Materials["accepted-service-core-trust-repair-selection"] = selection
		p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
		r.RenewalBindingSHA = deploymentplan.Digest(old)
		return prepareUsedMQTTBootstrapRenewal(ctx, cfg, store, e, b, p, r)
	}
	return nil
}

func bindAcceptedTransportRootMaterials(materials map[string]string) {
	for key, value := range materials {
		if strings.HasPrefix(key, "original-mqtt-root-") || strings.HasPrefix(key, "original-openbao_tls-root-") {
			materials["accepted-service-core-"+key] = value
			delete(materials, key)
		}
	}
}

func environmentCreateIssuerIntentSHA(request environmentCreateIssuerRequest) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return environmentCreatePlanSHA256(raw)
}

func originalOpenBaoRootCRL(store secretStore, e *environmentCreateExecution, root environmentCreateRegisteredIssuer) (string, error) {
	for _, v := range e.Journal.Entries {
		if v.Key == "pki-initial-root-crl-openbao_tls" && v.State == "completed" {
			var receipt struct {
				Issuer string `json:"issuer_id"`
				File   string `json:"crl_file"`
				SHA    string `json:"crl_pem_sha256"`
			}
			if json.Unmarshal(v.Receipt, &receipt) != nil || receipt.Issuer != root.ID || !environmentPreflightDigest.MatchString(receipt.SHA) {
				break
			}
			path, err := store.safePath(receipt.File)
			if err != nil {
				break
			}
			raw, err := readBoundedEnvironmentFile(path, true)
			if err != nil || environmentPreflightBytesSHA256(raw) != receipt.SHA {
				break
			}
			return filepath.ToSlash(receipt.File), nil
		}
	}
	return "", createProviderRecoveryBlock("original active OpenBao Root CRL receipt differs")
}
