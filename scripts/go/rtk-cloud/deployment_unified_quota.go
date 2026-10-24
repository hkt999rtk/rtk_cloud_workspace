package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Complete native account inventories are necessary quota evidence. They are
// not proof of scheduling, provisioning permission or a provider migration.
func completeProviderResourceCount(ctx context.Context, token, path string) (int, error) {
	seen := map[int]bool{}
	pages, results := 0, -1
	for page := 1; ; page++ {
		raw, err := deploymentPreflightLKERead(ctx, token, fmt.Sprintf("%s?page_size=500&page=%d", path, page))
		if err != nil {
			return 0, errors.New("complete native provider inventory cannot be read")
		}
		var list struct {
			Data    []struct{ ID int } `json:"data"`
			Page    int                `json:"page"`
			Pages   *int               `json:"pages"`
			Results *int               `json:"results"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Pages == nil || list.Results == nil || list.Data == nil || list.Page != page || *list.Pages < 0 || *list.Pages > 1000 || *list.Results < 0 {
			return 0, errors.New("native provider inventory pagination or result count is incomplete")
		}
		if page == 1 {
			pages, results = *list.Pages, *list.Results
		} else if pages != *list.Pages || results != *list.Results {
			return 0, errors.New("native provider inventory changed during pagination")
		}
		if pages == 0 && (page != 1 || results != 0 || len(list.Data) != 0) || pages > 0 && page > pages {
			return 0, errors.New("native provider inventory pagination is inconsistent")
		}
		for _, resource := range list.Data {
			if resource.ID <= 0 || seen[resource.ID] {
				return 0, errors.New("native provider inventory has absent or repeated resource IDs")
			}
			seen[resource.ID] = true
		}
		if pages == 0 || page == pages {
			if len(seen) != results {
				return 0, errors.New("native provider inventory does not contain all reported resources")
			}
			return len(seen), nil
		}
	}
}

func (c *compiledEnvironmentDeployment) CheckProviderQuota(ctx context.Context, plan *deploymentplan.Plan) error {
	if c == nil || plan == nil || c.Plan == nil || c.Plan.SHA256() != plan.SHA256() || c.Store.Environment != plan.Environment {
		return errors.New("exact selected plan and environment required for quota qualification")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Existing Kubernetes updates use API ownership, node capacity, storage
	// and admission checks. They do not provision Linode clusters or pools.
	if plan.Operation == "upgrade" {
		return nil
	}
	account, err := readLKEAccountState(plan.Environment, true)
	if err != nil {
		return quotaQualificationBlocker("operator-confirmed account quota cannot be verified")
	}
	operator, err := c.Store.readOperator()
	if err != nil || operator["LINODE_TOKEN"] == "" {
		return quotaQualificationBlocker("selected environment provider access is unavailable")
	}
	current := 0
	for _, endpoint := range []string{"/linode/instances", "/volumes", "/nodebalancers"} {
		count, err := completeProviderResourceCount(ctx, operator["LINODE_TOKEN"], endpoint)
		if err != nil {
			return quotaQualificationBlocker(err.Error())
		}
		current += count
	}
	additional := 0
	claims := map[string]bool{}
	for _, resource := range plan.Resources {
		claims[resource.ID] = true
		if resource.Action == "create" && resource.Desired["kind"] == "PersistentVolumeClaim" && deploymentplan.String(deploymentplan.Map(resource.Desired["spec"])["volumeName"]) == "" {
			additional++
		}
		if resource.Action == "create" && resource.Desired["kind"] == "Service" && deploymentplan.Map(resource.Desired["spec"])["type"] == "LoadBalancer" {
			additional++
		}
	}
	// StatefulSet templates are implicit native claim creates. Count only claims
	// absent from the exact complete inventory; retain all existing allocations.
	for _, resource := range plan.Resources {
		if resource.Action == "retire" || resource.Desired["kind"] != "StatefulSet" {
			continue
		}
		spec := deploymentplan.Map(resource.Desired["spec"])
		replicas, ok := spec["replicas"].(float64)
		if !ok || replicas < 0 || replicas > 10000 || replicas != math.Trunc(replicas) || spec["ordinals"] != nil {
			return quotaQualificationBlocker("candidate StatefulSet claim allocations require an explicit supported replica range")
		}
		metadata := deploymentplan.Map(resource.Desired["metadata"])
		for _, template := range certIssuerObjectList(spec["volumeClaimTemplates"]) {
			name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(template)["metadata"])["name"])
			if name == "" {
				return quotaQualificationBlocker("candidate claim template identity is unavailable")
			}
			for ordinal := 0; ordinal < int(replicas); ordinal++ {
				id := "PersistentVolumeClaim/" + deploymentplan.String(metadata["namespace"]) + "/" + name + "-" + deploymentplan.String(metadata["name"]) + "-" + strconv.Itoa(ordinal)
				if !claims[id] {
					additional++
					claims[id] = true
				}
			}
		}
	}
	allocated, err := c.recordedNormalTailClaimCount(ctx, plan)
	if err != nil {
		return err
	}
	if allocated > additional {
		return quotaQualificationBlocker("recorded claim allocation scope differs from the frozen target")
	}
	additional -= allocated
	if account["LKE_ACTIVE_SERVICE_LIMIT"] != "unlimited" {
		limit, err := strconv.Atoi(account["LKE_ACTIVE_SERVICE_LIMIT"])
		if err != nil || limit <= 0 || current+additional > limit {
			return quotaQualificationBlocker(fmt.Sprintf("native account inventory plus candidate allocations exceeds the confirmed cap (current=%d additional=%d)", current, additional))
		}
	}
	return nil
}

// Native account inventory already includes allocated claims. Only this
// original execution's frozen plan, durable child write and current native
// binding can remove them from the remaining allocation count.
func (c *compiledEnvironmentDeployment) recordedNormalTailClaimCount(ctx context.Context, plan *deploymentplan.Plan) (int, error) {
	h, ok := createNormalTailHandoffFromContext(ctx)
	if !ok {
		return 0, nil
	}
	e := h.Execution
	if e.Store != c.Store {
		return 0, quotaQualificationBlocker("normal workload owner differs from the selected environment")
	}
	if err := e.Check(ctx); err != nil {
		return 0, err
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload") == nil {
		return 0, nil
	}
	frozen, err := readCreateNormalTailFrozenTarget(c.Store, e)
	if err != nil {
		return 0, err
	}
	original, j, err := readCreateNormalTailWorkloadOriginal(c.Store, e, frozen)
	if err != nil || original == nil {
		return 0, err
	}
	forward, err := readCreateNormalTailWorkloadForwardDescriptor(c.Store, e, original, j)
	if err != nil {
		return 0, err
	}
	known := plan.SHA256() == frozen.SHA256() || forward != nil && plan.SHA256() == forward.Target.Plan.SHA256()
	if !known && forward != nil {
		successor, err := readCreateNormalTailProductSessionForward(c.Store, e, original, j, forward)
		if err != nil {
			return 0, err
		}
		known = successor != nil && plan.SHA256() == successor.Target.Plan.SHA256()
		if !known {
			runtime, err := readCreateNormalTailRuntimeForward(c.Store, e)
			if err != nil {
				return 0, err
			}
			known = runtime != nil && plan.SHA256() == runtime.Target.Plan.SHA256()
		}
	}
	if !known {
		return 0, quotaQualificationBlocker("normal workload differs from its original or exact linked forward target")
	}
	stages, err := readCreateNormalTailVolumeStages(c.Store, e)
	if err != nil {
		return 0, err
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return 0, err
	}
	if _, err := observeManagedLatePlannedVolumeStages(ctx, c.Backend, ledger, h.OriginalVolumes, &h.RedisVolume, stages); err != nil {
		return 0, err
	}
	if err := e.Check(ctx); err != nil {
		return 0, err
	}
	claims, err := managedLatePlannedVolumeClaims(plan)
	if err != nil {
		return 0, err
	}
	allocated := 0
	for _, volume := range ledger.Volumes {
		if _, planned := claims["PersistentVolumeClaim/"+volume.Namespace+"/"+volume.PVCName]; planned {
			allocated++
		}
	}
	return allocated, nil
}

func quotaQualificationBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.execution.provider-quota", Cause: cause, Next: "restore complete read access to instances, volumes and NodeBalancers; confirm the real account cap or obtain more capacity, then repeat full preflight; no credit is taken for deleting data volumes or unexecuted resource shrinkage"}
}
