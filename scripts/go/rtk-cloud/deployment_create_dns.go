package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func environmentCreateDNSAdapter(cfg deploymentConfig, operator map[string]string) (dnsAdapter, dnsAdapterContext, error) {
	values := appendMap(appendMap(deploymentEndpointValues(cfg), cfg.DNSValues), operator)
	values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"] = cfg.Environment, cfg.Values["CLOUD_STACK_NAME"]
	adapter, err := newDNSAdapter(cfg.DNSAdapter)
	local := dnsContext(provisionPaths{EnvRoot: cfg.RuntimeRoot}, values)
	if err != nil {
		return nil, local, environmentCreateBlocker("deployment.create.dns", "maintained DNS adapter is unavailable")
	}
	if err := validateDNSLocalState(local, adapter.Name()); err != nil {
		return nil, local, err
	}
	return adapter, local, nil
}

// Fresh create never appropriates an old live address. Earlier ownership and
// ACME history are preserved, and every maintained name must genuinely be free.
func qualifyEnvironmentCreateDNS(ctx context.Context, cfg deploymentConfig, plan genericDNSPlan, operator map[string]string) error {
	adapter, local, err := environmentCreateDNSAdapter(cfg, operator)
	if err != nil {
		return err
	}
	if plan.RootDomain != local.RootDomain || len(plan.Records) == 0 || !sameCreateDNSIntent(plan, buildGenericDNSPlan(cfg)) {
		return environmentCreateBlocker("deployment.create.dns", "DNS stage is not the exact maintained selected-environment endpoint intent")
	}
	if err := adapter.Validate(ctx, local); err != nil {
		return environmentCreateBlocker("deployment.create.dns", "selected DNS credentials or permissions cannot be verified")
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil {
		return environmentCreateBlocker("deployment.create.dns", "selected authoritative DNS zone cannot be verified")
	}
	challenges, err := readDNSChallenges(local, adapter.Name())
	if err != nil || len(challenges) != 0 {
		return environmentCreateBlocker("deployment.create.dns", "original ACME cleanup evidence is unresolved")
	}
	owned, err := readDNSOwnership(local, adapter.Name())
	if err != nil {
		return err
	}
	// Previously owned names outside the new target remain historical evidence.
	// They are never silently republished or used as a naming fallback.
	seen := map[string]bool{}
	for _, record := range append(append([]dnsRecordSet(nil), plan.Records...), owned.Records...) {
		if err := validateManagedDNSHostname(record.Name); err != nil || !strings.HasSuffix(record.Name, "."+local.RootDomain) {
			return environmentCreateBlocker("deployment.create.dns", "DNS intent or retained ownership includes an invalid selected-zone hostname")
		}
		if seen[record.Name] {
			continue
		}
		seen[record.Name] = true
		for _, kind := range []string{"A", "AAAA", "CNAME"} {
			current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, kind)
			if err != nil || len(current.Values) != 0 {
				return environmentCreateBlocker("deployment.create.dns", "a maintained or previously owned endpoint is occupied or cannot be read: "+record.Name+"/"+kind)
			}
		}
	}
	return ctx.Err()
}

func sameCreateDNSIntent(a, b genericDNSPlan) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func bindEnvironmentCreateDNS(plan genericDNSPlan, cfg deploymentConfig, receipts map[string]environmentCreateProviderReceipt) ([]dnsRecordSet, error) {
	edge := receipts["edge"]
	if edge.Kind != "instance" || edge.NativeID == "" || edge.PublicIP == "" {
		return nil, environmentCreateBlocker("deployment.create.dns", "recorded native edge address is unavailable")
	}
	values := deploymentEndpointValues(cfg)
	turn := map[string]string{}
	for index := 1; index <= lkeCoturnVMCount(values); index++ {
		node := lkeCoturnVMEnvForIndex(values, index)
		receipt := receipts[fmt.Sprintf("turn-%02d", index)]
		if receipt.Kind != "instance" || receipt.NativeID == "" || receipt.PublicIP == "" {
			return nil, environmentCreateBlocker("deployment.create.dns", "recorded native TURN address is unavailable")
		}
		turn[lkeCoturnDomain(node)] = receipt.PublicIP
	}
	var records []dnsRecordSet
	for _, record := range plan.Records {
		if len(record.Values) != 1 {
			return nil, environmentCreateBlocker("deployment.create.dns", "DNS stage contains an unsupported allocation reference")
		}
		switch record.Values[0] {
		case "runtime:public-edge":
			record.Values = []string{edge.PublicIP}
		case "runtime:turn":
			ip := turn[record.Name]
			if ip == "" {
				return nil, environmentCreateBlocker("deployment.create.dns", "TURN name has no exact recorded node allocation")
			}
			record.Values = []string{ip}
		default:
			return nil, environmentCreateBlocker("deployment.create.dns", "DNS stage references an unqualified allocation")
		}
		if err := validateDNSRecord(plan.RootDomain, record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func executeEnvironmentCreateDNS(ctx context.Context, cfg deploymentConfig, plan genericDNSPlan, receipts map[string]environmentCreateProviderReceipt, operator map[string]string, execution *environmentCreateExecution) error {
	adapter, local, err := environmentCreateDNSAdapter(cfg, operator)
	if err != nil {
		return err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil {
		return environmentCreateBlocker("deployment.create.dns", "authoritative zone access is unavailable at execution")
	}
	records, err := bindEnvironmentCreateDNS(plan, cfg, receipts)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := execution.Check(ctx); err != nil {
			return err
		}
		key := "dns/" + record.Type + "/" + record.Name
		intent, _ := json.Marshal(record)
		normalTail, err := createOriginalRoutingContext(ctx, execution)
		if err != nil {
			return err
		}
		if normalTail {
			done, err := createNormalTailRecordedStage(ctx, execution, key, "routing", intent, intent, func(ctx context.Context) error {
				for _, kind := range []string{"A", "AAAA", "CNAME"} {
					current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, kind)
					if err != nil || kind == record.Type && !sameDNSValues(current.Values, record.Values) || kind != record.Type && len(current.Values) != 0 {
						return environmentCreateBlocker("deployment.create.dns", "recorded DNS target changed: "+record.Name)
					}
				}
				if entry := createOpenBaoACKCompletionEntry(execution.Journal, key); entry != nil && entry.State == "allocated" {
					return writeDNSOwnership(local, adapter.Name(), []dnsRecordSet{record})
				}
				return nil
			})
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		for _, kind := range []string{"A", "AAAA", "CNAME"} {
			current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, kind)
			if err != nil || len(current.Values) != 0 {
				return environmentCreateBlocker("deployment.create.dns", "endpoint changed before DNS publication: "+record.Name+"/"+kind)
			}
		}
		if err := execution.BeginEntry(key, "routing", intent, time.Now().UTC()); err != nil {
			return err
		}
		if err := adapter.UpsertRecordSet(ctx, local, zone, record); err != nil {
			return environmentCreateBlocker("deployment.create.dns", "DNS publication outcome requires original-journal reconciliation: "+record.Name)
		}
		// Save the allocation before readback; an uncertain read must not erase
		// the record's purpose or permit another unreviewed upsert.
		if err := execution.RecordReceipt(key, intent); err != nil {
			return err
		}
		current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, record.Type)
		if err != nil || !sameDNSValues(current.Values, record.Values) {
			return environmentCreateBlocker("deployment.create.dns", "exact DNS publication could not be verified: "+record.Name)
		}
		if err := writeDNSOwnership(local, adapter.Name(), []dnsRecordSet{record}); err != nil {
			return err
		}
		if err := execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func sameDNSValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}
