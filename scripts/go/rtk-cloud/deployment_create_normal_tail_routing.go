package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Routing receipts belong to the held original create execution in both fresh
// create and completed-bootstrap continuation. The runtime is not a handoff;
// it can only select readback while its exact original owner remains held.
func createOriginalRoutingContext(ctx context.Context, e *environmentCreateExecution) (bool, error) {
	if h, ok := createNormalTailHandoffFromContext(ctx); ok {
		if e == nil || h.Execution != e {
			return false, createProviderRecoveryBlock("routing handoff differs from the original execution")
		}
		return true, e.Check(ctx)
	}
	runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	if !ok {
		return false, nil
	}
	if e == nil || runtime.Execution != e || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 {
		return false, createProviderRecoveryBlock("routing runtime differs from the original execution")
	}
	return true, e.Check(ctx)
}

func createNormalTailDataPlaneReadback(ctx context.Context, store secretStore, plan *environmentCreatePlan, execution *environmentCreateExecution, resource environmentCreateProviderResource, receipt environmentCreateProviderReceipt) (string, error) {
	if err := execution.Check(ctx); err != nil {
		return "", err
	}
	script := "set -eu\nsystemctl is-active coturn >/dev/null\nsha256sum /etc/turnserver.conf /etc/video-cloud-coturn-cli.env /etc/video-cloud-turnregistrar.env /etc/systemd/system/video-cloud-turnregistrar.service /opt/video_cloud/bin/turnregistrar\n"
	if resource.Key == "edge" {
		script = "set -eu\nhaproxy -c -f /etc/haproxy/haproxy.cfg >/dev/null\nsystemctl is-active haproxy >/dev/null\nsha256sum /etc/haproxy/haproxy.cfg /etc/sysctl.d/99-rtk-cloud-edge.conf /etc/systemd/system/haproxy.service.d/10-rtk-cloud-edge.conf\n"
	}
	raw, err := environmentCreatePinnedSSH(ctx, store, plan.Provider, environmentCreateHostMaterialPlan(execution), resource, receipt, script)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", createProviderBlocker("original data-plane configuration readback is empty")
	}
	if err := execution.Check(ctx); err != nil {
		return "", err
	}
	return environmentPreflightBytesSHA256(raw), nil
}

func qualifyCreateNormalTailDNS(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, operator map[string]string) error {
	if h == nil || h.Execution == nil || h.OriginalPlan == nil || h.Execution.Check(ctx) != nil {
		return createProviderRecoveryBlock("original DNS continuation owner unavailable")
	}
	adapter, local, err := environmentCreateDNSAdapter(cfg, operator)
	if err != nil {
		return err
	}
	if err := adapter.Validate(ctx, local); err != nil {
		return err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil {
		return err
	}
	records, err := bindEnvironmentCreateDNS(h.OriginalPlan.DNS, cfg, h.Receipts)
	if err != nil {
		return err
	}
	for _, record := range records {
		key := "dns/" + record.Type + "/" + record.Name
		entry := createOpenBaoACKCompletionEntry(h.Execution.Journal, key)
		if entry != nil {
			intent, _ := json.Marshal(record)
			sha, err := environmentCreatePlanSHA256(intent)
			if err != nil || entry.Phase != "routing" || entry.IntentSHA256 != sha || entry.State != "allocated" && entry.State != "completed" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(intent)) {
				return createProviderRecoveryBlock("DNS request outcome is uncertain or differs from its original record: " + record.Name)
			}
		}
		for _, kind := range []string{"A", "AAAA", "CNAME"} {
			current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, kind)
			if err != nil {
				return err
			}
			if entry != nil && kind == record.Type {
				if !sameDNSValues(current.Values, record.Values) {
					return createProviderRecoveryBlock("recorded DNS target changed: " + record.Name)
				}
			} else if len(current.Values) != 0 {
				return createProviderRecoveryBlock("unrecorded DNS owner is occupied: " + record.Name + "/" + kind)
			}
		}
	}
	return h.Execution.Check(ctx)
}

func createNormalTailRoutingStageKeys(plan *environmentCreatePlan) []string {
	allowed := []string{}
	for _, resource := range plan.Provider.Resources {
		if resource.Kind == "instance" {
			allowed = append(allowed, "configure-"+resource.Key)
		}
	}
	for _, record := range plan.DNS.Records {
		allowed = append(allowed, "dns/"+record.Type+"/"+record.Name)
	}
	for _, resource := range plan.Provider.Resources {
		if resource.Kind == "instance" && resource.Key != "edge" {
			allowed = append(allowed, "activate-"+resource.Key)
		}
	}
	return allowed
}

func validateCreateNormalTailStages(cfg deploymentConfig, h *createNormalTailHandoff) error {
	e := h.Execution
	allowed := append([]string{"kubernetes/workload"}, createNormalTailRoutingStageKeys(h.OriginalPlan)...)
	// Locate the authenticated migration boundary by its semantic final key;
	// later normal stages do not gain new checkpoint-count variants.
	boundary := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "normal-redis-durable-verified" {
			if boundary >= 0 {
				return createProviderRecoveryBlock("duplicate migration boundary")
			}
			boundary = i
		}
	}
	if boundary < 0 {
		return createProviderRecoveryBlock("unknown normal continuation stage")
	}
	suffix := e.Journal.Entries[boundary+1:]
	var children []*deploymentplan.Plan
	if len(suffix) > 0 {
		program, err := readCreateNormalTailProgram(e.Store, e)
		if err != nil {
			return err
		}
		if program.HandoffSHA256 != deploymentplan.Digest(h.Pins) || suffix[0].Key != "normal-tail/program" || suffix[0].StartedAt.Before(e.Journal.Entries[boundary].CompletedAt) || suffix[0].CompletedAt.After(time.Now().UTC()) {
			return createProviderRecoveryBlock("normal program differs from the completed bootstrap handoff")
		}
		if err := verifyCreateNormalTailProgramAdmission(e.Store, e); err != nil {
			return err
		}
		suffix = suffix[1:]
		publicConsumed, publicChildren, err := validateCreateNormalTailPublicTLSStages(e.Store, e, program.PublicTLS, suffix)
		if err != nil {
			return err
		}
		children = append(children, publicChildren...)
		if publicConsumed > 0 && publicConsumed < len(suffix) && suffix[publicConsumed].StartedAt.Before(suffix[publicConsumed-1].CompletedAt) {
			return createProviderRecoveryBlock("product lifecycle precedes its public TLS publication")
		}
		suffix = suffix[publicConsumed:]
		consumed, plans, err := validateCreateNormalTailProductStages(e.Store, e, program.Products, suffix)
		if err != nil {
			return err
		}
		if consumed < 0 || consumed > len(suffix) {
			return createProviderRecoveryBlock("invalid product lifecycle boundary")
		}
		children = append(children, plans...)
		for _, entry := range suffix[:consumed] {
			if entry.Key == "kubernetes/workload" {
				allowed = allowed[1:]
				break
			}
		}
		suffix = suffix[consumed:]
	}
	routingCount := len(suffix)
	if routingCount > len(allowed) {
		routingCount = len(allowed)
	}
	for i, entry := range suffix[:routingCount] {
		if entry.Key != allowed[i] || entry.StartedAt.IsZero() || entry.StartedAt.Before(e.Journal.Entries[boundary].CompletedAt) || entry.StartedAt.After(time.Now().UTC()) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || i < len(suffix)-1 && entry.State != "completed" {
			return createProviderRecoveryBlock(fmt.Sprintf("invalid normal continuation stage: %s", entry.Key))
		}
		if entry.State == "completed" && (entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || len(entry.Receipt) == 0) || entry.State != "completed" && !entry.CompletedAt.IsZero() || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State == "allocated" && len(entry.Receipt) == 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return createProviderRecoveryBlock("normal continuation receipt is invalid: " + entry.Key)
		}
		if entry.Key == "kubernetes/workload" && entry.State == "completed" {
			if _, _, err := managedTransportPostAcceptedStage(e.Store, e, entry.Key); err != nil {
				return err
			}
		}
	}
	correction, err := readCreateNormalTailPostCleanupCorrection(cfg, e.Store, e)
	if err != nil {
		return err
	}
	if correction != nil && (routingCount != len(allowed) || correction.Source.PrefixEntries != len(e.Journal.Entries)-len(suffix)+routingCount) {
		return createProviderRecoveryBlock("correction precedes completed original routing")
	}
	if len(suffix) > routingCount {
		if correction == nil {
			return createProviderRecoveryBlock("unknown normal continuation stage")
		}
	}
	if correction != nil {
		for _, child := range correction.Plans {
			children = append(children, child)
		}
	}
	var workload *deploymentplan.Plan
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload") != nil {
		frozen, err := readCreateNormalTailFrozenTarget(e.Store, e)
		if err != nil {
			return err
		}
		if frozen.CreateParentSHA256 != e.Journal.PlanSHA256 || frozen.CreateHandoffSHA256 != deploymentplan.Digest(h.Pins) {
			return createProviderRecoveryBlock("normal workload frozen handoff changed")
		}
		workload, _, err = readCreateNormalTailWorkload(e.Store, e, frozen)
		if err != nil {
			return err
		}
		children = append(children, workload)
	}
	baseLeases := len(h.Source.KubernetesLeases) + 5
	if len(e.Journal.KubernetesLeases) < baseLeases {
		return createProviderRecoveryBlock("completed migration lease history is missing")
	}
	for _, lease := range e.Journal.KubernetesLeases[baseLeases:] {
		matched := false
		for _, child := range children {
			if child != nil && managedLateLeaseMatches(lease, e.Journal.Stack, child.SHA256()) {
				matched = true
				break
			}
		}
		if !matched {
			return createProviderRecoveryBlock("normal child lease has no recorded original workload")
		}
	}
	return nil
}
