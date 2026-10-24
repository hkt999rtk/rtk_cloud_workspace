package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentSurgeRequest struct{ cpu, memory int64 }

func surgeCapacityBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.execution.surge-capacity", Resource: id, Cause: cause, Next: "reconcile the exact candidate rollout strategy and current selected-node request headroom, then repeat full preflight; preserve original identities and PVCs; this read-only snapshot creates no Pod and guarantees neither scheduling nor application readiness"}
}

// Only existing singleton Deployment template updates requiring an overlapping
// replacement are covered. Recreate and zero-surge updates do not need this
// extra reservation. The executor waits between resource writes, so candidates
// are checked independently rather than summing unrelated surge Pods.
func (c *compiledEnvironmentDeployment) CheckSurgeCapacity(ctx context.Context, plan *deploymentplan.Plan) error {
	if err := ctx.Err(); err != nil {
		return surgeCapacityBlocker("", secretCheckFailure(classifyDeploymentRuntimeError(ctx, nil, err, false), "surge-capacity snapshot interrupted").Error())
	}
	if c == nil || plan == nil || c.Plan == nil || c.Plan.SHA256() != plan.SHA256() || c.Store.Environment != plan.Environment {
		return surgeCapacityBlocker("", "exact selected plan and environment required")
	}
	candidates, err := environmentSurgeCandidates(plan)
	if err != nil || len(candidates) == 0 {
		return err
	}
	read := func(args ...string) ([]deploymentplan.Object, error) {
		raw, err := c.Backend.command(ctx, nil, args...)
		if err != nil {
			return nil, surgeCapacityBlocker(candidates[0].ID, secretCheckFailure(err, "native surge-capacity inventory is unavailable").Error())
		}
		var list struct {
			Items    []deploymentplan.Object `json:"items"`
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Items == nil || list.Metadata.Continue != "" {
			return nil, surgeCapacityBlocker(candidates[0].ID, "complete native surge-capacity inventory is unavailable")
		}
		return list.Items, nil
	}
	nodes, err := read("get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	// Include system and foreign workloads: allocatable minus only this stack's
	// requests would falsely count their reservations as available capacity.
	pods, err := read("get", "pods", "--all-namespaces", "-o", "json")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return surgeCapacityBlocker(candidates[0].ID, secretCheckFailure(classifyDeploymentRuntimeError(ctx, nil, err, false), "surge-capacity snapshot interrupted").Error())
	}
	return verifyEnvironmentSurgeCapacity(candidates, nodes, pods)
}

func environmentSurgeCandidates(plan *deploymentplan.Plan) ([]deploymentplan.Resource, error) {
	var result []deploymentplan.Resource
	for _, r := range plan.Resources {
		if r.Action != "update" || r.Desired["kind"] != "Deployment" {
			continue
		}
		spec, baseline := deploymentplan.Map(r.Desired["spec"]), deploymentplan.Map(r.Baseline["spec"])
		if deploymentplan.Digest(spec["template"]) == deploymentplan.Digest(baseline["template"]) {
			continue
		}
		replicas := 1
		if value, exists := spec["replicas"]; exists {
			var err error
			replicas, err = strconv.Atoi(fmt.Sprint(value))
			if err != nil || replicas < 0 {
				return nil, surgeCapacityBlocker(r.ID, "candidate replica count is invalid")
			}
		}
		if replicas != 1 {
			continue
		}
		strategy := deploymentplan.Map(spec["strategy"])
		if strategy["type"] == "Recreate" {
			continue
		}
		if kind := deploymentplan.String(strategy["type"]); kind != "" && kind != "RollingUpdate" {
			return nil, surgeCapacityBlocker(r.ID, "candidate singleton rollout strategy is unsupported")
		}
		rolling := deploymentplan.Map(strategy["rollingUpdate"])
		unavailable, unavailableErr := singletonRolloutCount(rolling["maxUnavailable"], false)
		surge, surgeErr := singletonRolloutCount(rolling["maxSurge"], true)
		if unavailableErr != nil || surgeErr != nil {
			return nil, surgeCapacityBlocker(r.ID, "candidate singleton rolling-update counts are invalid")
		}
		if unavailable == 0 && surge > 0 {
			result = append(result, r)
		}
	}
	return result, nil
}

func singletonRolloutCount(value any, roundUp bool) (int, error) {
	if value == nil {
		value = "25%"
	}
	raw := fmt.Sprint(value)
	if strings.HasSuffix(raw, "%") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "%"))
		if err != nil || n < 0 || n > 100 {
			return 0, fmt.Errorf("invalid rolling count")
		}
		if roundUp && n > 0 {
			return 1, nil
		}
		return n / 100, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid rolling count")
	}
	return n, nil
}

func verifyEnvironmentSurgeCapacity(candidates []deploymentplan.Resource, nodes, pods []deploymentplan.Object) error {
	if len(candidates) == 0 {
		return nil
	}
	invalid := func() error {
		return surgeCapacityBlocker(candidates[0].ID, "native node or Pod resource-request evidence is incomplete or unsupported")
	}
	byName, uids := map[string]deploymentplan.Object{}, map[string]bool{}
	for _, node := range nodes {
		meta := deploymentplan.Map(node["metadata"])
		name, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["uid"])
		if node["kind"] != "Node" || name == "" || uid == "" || byName[name] != nil || uids[uid] {
			return invalid()
		}
		byName[name], uids[uid] = node, true
	}
	reserved, podNames, podUIDs := map[string]environmentSurgeRequest{}, map[string]bool{}, map[string]bool{}
	for _, pod := range pods {
		meta := deploymentplan.Map(pod["metadata"])
		name, namespace, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["uid"])
		id := namespace + "/" + name
		if pod["kind"] != "Pod" || name == "" || namespace == "" || uid == "" || podNames[id] || podUIDs[uid] {
			return invalid()
		}
		podNames[id], podUIDs[uid] = true, true
		spec, status := deploymentplan.Map(pod["spec"]), deploymentplan.Map(pod["status"])
		if status["phase"] == "Succeeded" || status["phase"] == "Failed" {
			continue
		}
		name = deploymentplan.String(spec["nodeName"])
		if value, exists := spec["nodeName"]; exists && value != name {
			return invalid()
		}
		if name == "" {
			continue // Unscheduled Pods have no reservation on a native node yet.
		}
		if byName[name] == nil {
			return invalid()
		}
		request, ok := environmentObservedPodRequest(pod)
		if !ok {
			return invalid()
		}
		current, ok := addSurgeRequest(reserved[name], request)
		if !ok {
			return invalid()
		}
		reserved[name] = current // Readiness and deletionTimestamp never free requests.
	}
	for _, candidate := range candidates {
		pod := environmentPKIPod(candidate.Desired)
		if !environmentSimpleSurgePlacement(pod) {
			return surgeCapacityBlocker(candidate.ID, "candidate surge placement needs unsupported hard-constraint or resource-profile qualification")
		}
		request, ok := environmentEffectivePodRequest(pod)
		if !ok {
			return surgeCapacityBlocker(candidate.ID, "candidate effective Pod resource requests are incomplete or unsupported")
		}
		fits := false
		for _, node := range nodes {
			eligible, valid := environmentSurgeNodeEligible(pod, node)
			if !valid {
				return invalid()
			}
			if !eligible {
				continue
			}
			allocatable, valid := environmentSurgeRequests(deploymentplan.Map(deploymentplan.Map(node["status"])["allocatable"]), nil)
			if !valid || allocatable.cpu <= 0 || allocatable.memory <= 0 {
				return invalid()
			}
			used := reserved[deploymentplan.String(deploymentplan.Map(node["metadata"])["name"])]
			if allocatable.cpu-used.cpu >= request.cpu && allocatable.memory-used.memory >= request.memory {
				fits = true
			}
		}
		if !fits {
			return surgeCapacityBlocker(candidate.ID, "no eligible native node has current CPU and memory request headroom for one required overlapping replacement Pod")
		}
	}
	return nil
}

func environmentSimpleSurgePlacement(pod deploymentplan.Object) bool {
	if pod["resources"] != nil || pod["runtimeClassName"] != nil || pod["hostNetwork"] == true || pod["schedulerName"] != nil && pod["schedulerName"] != "default-scheduler" {
		return false
	}
	affinity := deploymentplan.Map(pod["affinity"])
	for _, key := range []string{"nodeAffinity", "podAffinity", "podAntiAffinity"} {
		if deploymentplan.Map(affinity[key])["requiredDuringSchedulingIgnoredDuringExecution"] != nil {
			return false
		}
	}
	for _, raw := range certIssuerObjectList(pod["topologySpreadConstraints"]) {
		if deploymentplan.Map(raw)["whenUnsatisfiable"] != "ScheduleAnyway" {
			return false
		}
	}
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(raw)["persistentVolumeClaim"] != nil {
			return false
		}
	}
	for _, field := range []string{"containers", "initContainers"} {
		for _, raw := range certIssuerObjectList(pod[field]) {
			container := deploymentplan.Map(raw)
			for _, rawPort := range certIssuerObjectList(container["ports"]) {
				if value := deploymentplan.Map(rawPort)["hostPort"]; value != nil && fmt.Sprint(value) != "0" {
					return false
				}
			}
			for _, resourceField := range []string{"requests", "limits"} {
				for key := range deploymentplan.Map(deploymentplan.Map(container["resources"])[resourceField]) {
					if key != "cpu" && key != "memory" {
						return false
					}
				}
			}
		}
	}
	return true
}

func environmentSurgeNodeEligible(pod, node deploymentplan.Object) (bool, bool) {
	meta, spec, status := deploymentplan.Map(node["metadata"]), deploymentplan.Map(node["spec"]), deploymentplan.Map(node["status"])
	labels := deploymentplan.Map(meta["labels"])
	if meta["deletionTimestamp"] != nil || spec["unschedulable"] == true || labels["kubernetes.io/os"] != "linux" || labels["kubernetes.io/arch"] != "amd64" {
		return false, true
	}
	ready, seen := false, 0
	for _, raw := range certIssuerObjectList(status["conditions"]) {
		condition := deploymentplan.Map(raw)
		if condition["type"] == "Ready" {
			seen++
			ready = condition["status"] == "True"
		}
	}
	if seen != 1 {
		return false, false
	}
	if !ready || pod["nodeName"] != nil && pod["nodeName"] != meta["name"] {
		return false, true
	}
	for key, value := range deploymentplan.Map(pod["nodeSelector"]) {
		if _, ok := value.(string); !ok {
			return false, false
		}
		if labels[key] != value {
			return false, true
		}
	}
	for _, raw := range certIssuerObjectList(spec["taints"]) {
		taint := deploymentplan.Map(raw)
		if taint["effect"] != "NoSchedule" && taint["effect"] != "NoExecute" {
			continue
		}
		tolerated := false
		for _, rawToleration := range certIssuerObjectList(pod["tolerations"]) {
			t := deploymentplan.Map(rawToleration)
			if t["effect"] != nil && t["effect"] != "" && t["effect"] != taint["effect"] {
				continue
			}
			operator := deploymentplan.String(t["operator"])
			if operator == "Exists" && (t["key"] == nil || t["key"] == "" || t["key"] == taint["key"]) || (operator == "" || operator == "Equal") && t["key"] == taint["key"] && deploymentplan.String(t["value"]) == deploymentplan.String(taint["value"]) {
				tolerated = true
			}
		}
		if !tolerated {
			return false, true
		}
	}
	return true, true
}

func environmentEffectivePodRequest(pod deploymentplan.Object) (environmentSurgeRequest, bool) {
	if pod == nil || pod["resources"] != nil {
		return environmentSurgeRequest{}, false
	}
	if pod["initContainers"] != nil && certIssuerObjectList(pod["initContainers"]) == nil || pod["overhead"] != nil && deploymentplan.Map(pod["overhead"]) == nil {
		return environmentSurgeRequest{}, false
	}
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) == 0 {
		return environmentSurgeRequest{}, false
	}
	var regular, sidecars, initMax environmentSurgeRequest
	for _, raw := range containers {
		container := deploymentplan.Map(raw)
		if container == nil || deploymentplan.String(container["name"]) == "" {
			return regular, false
		}
		resources := deploymentplan.Map(container["resources"])
		if !validSurgeResourceFields(container, resources) {
			return regular, false
		}
		request, ok := environmentSurgeRequests(deploymentplan.Map(resources["requests"]), deploymentplan.Map(resources["limits"]))
		if !ok {
			return regular, false
		}
		regular, ok = addSurgeRequest(regular, request)
		if !ok {
			return regular, false
		}
	}
	for _, raw := range certIssuerObjectList(pod["initContainers"]) {
		container := deploymentplan.Map(raw)
		if container == nil || deploymentplan.String(container["name"]) == "" {
			return regular, false
		}
		resources := deploymentplan.Map(container["resources"])
		if !validSurgeResourceFields(container, resources) {
			return regular, false
		}
		request, ok := environmentSurgeRequests(deploymentplan.Map(resources["requests"]), deploymentplan.Map(resources["limits"]))
		if !ok {
			return regular, false
		}
		if container["restartPolicy"] == "Always" {
			sidecars, ok = addSurgeRequest(sidecars, request)
			request = sidecars
		} else {
			if container["restartPolicy"] != nil {
				return regular, false
			}
			request, ok = addSurgeRequest(sidecars, request)
		}
		if !ok {
			return regular, false
		}
		initMax.cpu, initMax.memory = max(initMax.cpu, request.cpu), max(initMax.memory, request.memory)
	}
	steady, ok := addSurgeRequest(regular, sidecars)
	if !ok {
		return regular, false
	}
	steady.cpu, steady.memory = max(steady.cpu, initMax.cpu), max(steady.memory, initMax.memory)
	overhead, valid := environmentSurgeRequests(deploymentplan.Map(pod["overhead"]), nil)
	if !valid {
		return steady, false
	}
	return addSurgeRequest(steady, overhead)
}

// Normal current clusters report status resources even without an active
// resize. Take the per-container maximum of desired, actual and allocated
// requests before applying the ordered init/sidecar and overhead calculation.
// Never use readiness as a prerequisite or release a terminating reservation.
func environmentObservedPodRequest(pod deploymentplan.Object) (environmentSurgeRequest, bool) {
	spec := deploymentplan.Clone(deploymentplan.Map(pod["spec"]))
	status := deploymentplan.Map(pod["status"])
	if spec == nil {
		return environmentSurgeRequest{}, false
	}
	// Existing Pod-level allocation is native evidence, not a new candidate
	// profile. Aggregate reports can be present for ordinary container-level
	// Pods too. Preserve their greater CPU/memory reservation without adding
	// the same allocation twice. Candidate Pod-level profiles remain scoped out.
	podResources := deploymentplan.Map(spec["resources"])
	if !validSurgeResourceFields(spec, podResources) {
		return environmentSurgeRequest{}, false
	}
	delete(spec, "resources")
	for _, fields := range [][2]string{{"containers", "containerStatuses"}, {"initContainers", "initContainerStatuses"}} {
		containers := map[string]deploymentplan.Object{}
		for _, raw := range certIssuerObjectList(spec[fields[0]]) {
			container := deploymentplan.Map(raw)
			name := deploymentplan.String(container["name"])
			if name == "" || containers[name] != nil {
				return environmentSurgeRequest{}, false
			}
			containers[name] = container
		}
		statuses := certIssuerObjectList(status[fields[1]])
		if status[fields[1]] != nil && statuses == nil {
			return environmentSurgeRequest{}, false
		}
		seen := map[string]bool{}
		for _, raw := range statuses {
			actual := deploymentplan.Map(raw)
			name := deploymentplan.String(actual["name"])
			container := containers[name]
			if container == nil || seen[name] {
				return environmentSurgeRequest{}, false
			}
			seen[name] = true
			resources := deploymentplan.Map(container["resources"])
			if !validSurgeResourceFields(container, resources) {
				return environmentSurgeRequest{}, false
			}
			request, ok := environmentSurgeRequests(deploymentplan.Map(resources["requests"]), deploymentplan.Map(resources["limits"]))
			if !ok {
				return request, false
			}
			actualResources := deploymentplan.Map(actual["resources"])
			if !validSurgeResourceFields(actual, actualResources) || actual["allocatedResources"] != nil && deploymentplan.Map(actual["allocatedResources"]) == nil {
				return request, false
			}
			for _, values := range []deploymentplan.Object{deploymentplan.Map(actualResources["requests"]), deploymentplan.Map(actual["allocatedResources"])} {
				observed, ok := environmentSurgeRequests(values, nil)
				if !ok {
					return request, false
				}
				request.cpu, request.memory = max(request.cpu, observed.cpu), max(request.memory, observed.memory)
			}
			if resources == nil {
				resources = deploymentplan.Object{}
				container["resources"] = resources
			}
			resources["requests"] = deploymentplan.Object{"cpu": fmt.Sprintf("%dm", request.cpu), "memory": strconv.FormatInt(request.memory, 10)}
		}
	}
	request, ok := environmentEffectivePodRequest(spec)
	if !ok {
		return request, false
	}
	actualPodResources := deploymentplan.Map(status["resources"])
	if !validSurgeResourceFields(status, actualPodResources) || status["allocatedResources"] != nil && deploymentplan.Map(status["allocatedResources"]) == nil {
		return request, false
	}
	// Spec Pod-level requests exclude overhead; native aggregate status reports
	// already represent actual/allocated Pod totals. Keep the maximum of totals.
	specPodRequest, ok := environmentSurgeRequests(deploymentplan.Map(podResources["requests"]), deploymentplan.Map(podResources["limits"]))
	if !ok {
		return request, false
	}
	if len(deploymentplan.Map(podResources["requests"])) > 0 || len(deploymentplan.Map(podResources["limits"])) > 0 {
		overhead, valid := environmentSurgeRequests(deploymentplan.Map(spec["overhead"]), nil)
		if !valid {
			return request, false
		}
		specPodRequest, ok = addSurgeRequest(specPodRequest, overhead)
		if !ok {
			return request, false
		}
		request.cpu, request.memory = max(request.cpu, specPodRequest.cpu), max(request.memory, specPodRequest.memory)
	}
	for _, values := range []deploymentplan.Object{deploymentplan.Map(actualPodResources["requests"]), deploymentplan.Map(status["allocatedResources"])} {
		observed, valid := environmentSurgeRequests(values, nil)
		if !valid {
			return request, false
		}
		request.cpu, request.memory = max(request.cpu, observed.cpu), max(request.memory, observed.memory)
	}
	return request, true
}

func validSurgeResourceFields(container, resources deploymentplan.Object) bool {
	if container["resources"] != nil && resources == nil {
		return false
	}
	for _, field := range []string{"requests", "limits"} {
		if resources[field] != nil && deploymentplan.Map(resources[field]) == nil {
			return false
		}
	}
	return true
}

func addSurgeRequest(a, b environmentSurgeRequest) (environmentSurgeRequest, bool) {
	if a.cpu > math.MaxInt64-b.cpu || a.memory > math.MaxInt64-b.memory {
		return a, false
	}
	return environmentSurgeRequest{a.cpu + b.cpu, a.memory + b.memory}, true
}

func environmentSurgeRequests(requests, limits deploymentplan.Object) (environmentSurgeRequest, bool) {
	result := environmentSurgeRequest{}
	for _, key := range []string{"cpu", "memory"} {
		value, exists := requests[key]
		if !exists {
			value, exists = limits[key] // Native request default when only a limit is set.
		}
		if !exists {
			continue
		}
		raw, ok := value.(string)
		if !ok || raw == "" {
			return result, false
		}
		if key == "cpu" {
			if !strings.HasSuffix(raw, "m") {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > float64(math.MaxInt32)/1000 {
					return result, false
				}
			}
			cpu, err := parseCPUQuantity(raw)
			if err != nil || cpu < 0 || cpu > math.MaxInt32 {
				return result, false
			}
			result.cpu = int64(cpu)
		} else {
			// Compare memory in bytes. MiB rounding per Pod would either invent
			// allocatable bytes or lose usable headroom through repeated rounding.
			units := []struct {
				suffix     string
				multiplier int64
			}{{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}, {"E", 1e18}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3}, {"K", 1e3}}
			multiplier := int64(1)
			for _, unit := range units {
				if strings.HasSuffix(raw, unit.suffix) {
					raw, multiplier = strings.TrimSuffix(raw, unit.suffix), unit.multiplier
					break
				}
			}
			if _, err := strconv.ParseFloat(raw, 64); err != nil {
				return result, false
			}
			number, valid := new(big.Rat).SetString(raw)
			if !valid || number.Sign() < 0 {
				return result, false
			}
			number.Mul(number, new(big.Rat).SetInt64(multiplier))
			integer, remainder := new(big.Int), new(big.Int)
			integer.QuoRem(number.Num(), number.Denom(), remainder)
			if remainder.Sign() > 0 {
				integer.Add(integer, big.NewInt(1))
			}
			if !integer.IsInt64() {
				return result, false
			}
			result.memory = integer.Int64()
		}
	}
	return result, true
}
