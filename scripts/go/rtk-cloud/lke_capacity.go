package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type lkeResourceRequest struct {
	Name      string
	Replicas  int
	CPUMilli  int
	MemoryMi  int
	SpreadMin int
}

type lkeCapacityPlanResult struct {
	NodeType         string
	NodeCount        int
	TargetConnects   int
	MQTTCapacity     int
	RequiredMQTTPods int
	AllocatableCPU   int
	AllocatableMemMi int
	SystemCPUPerNode int
	SystemMemPerNode int
	Workloads        []lkeResourceRequest
	WorkloadCPU      int
	WorkloadMemMi    int
	RequiredCPUNode  int
	RequiredMemNode  int
	RequiredSpread   int
	RequiredNodes    int
	ProviderServices lkeProviderServicePlan
}

type lkeProviderServicePlan struct {
	NodeServices     int
	PostgresVolumes  int
	EdgeVMs          int
	CoturnVMs        int
	RequiredServices int
	Limit            int
}

func lkePrintCapacityPlan(env map[string]string, opts provisionOptions) {
	plan, err := lkeCapacityPlan(env, opts)
	if err != nil {
		fmt.Fprintf(os.Stdout, "- capacity: error: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stdout, "- capacity:")
	fmt.Fprintf(os.Stdout, "  - node_type: %s\n", plan.NodeType)
	fmt.Fprintf(os.Stdout, "  - node_count: %d\n", plan.NodeCount)
	if plan.TargetConnects > 0 {
		fmt.Fprintf(os.Stdout, "  - target_connects: %d\n", plan.TargetConnects)
		fmt.Fprintf(os.Stdout, "  - mqtt_capacity: %d required_mqtt_replicas=%d\n", plan.MQTTCapacity, plan.RequiredMQTTPods)
	}
	fmt.Fprintf(os.Stdout, "  - per_node_allocatable: cpu=%dm memory=%dMi\n", plan.AllocatableCPU, plan.AllocatableMemMi)
	fmt.Fprintf(os.Stdout, "  - per_node_system_reserved: cpu=%dm memory=%dMi\n", plan.SystemCPUPerNode, plan.SystemMemPerNode)
	fmt.Fprintf(os.Stdout, "  - workload_requests: cpu=%dm memory=%dMi\n", plan.WorkloadCPU, plan.WorkloadMemMi)
	fmt.Fprintf(os.Stdout, "  - required_nodes: cpu=%d memory=%d spread=%d effective=%d\n", plan.RequiredCPUNode, plan.RequiredMemNode, plan.RequiredSpread, plan.RequiredNodes)
	if plan.ProviderServices.RequiredServices > 0 {
		limit := "unset"
		if plan.ProviderServices.Limit > 0 {
			limit = strconv.Itoa(plan.ProviderServices.Limit)
		}
		fmt.Fprintf(os.Stdout, "  - provider_active_services: required=%d limit=%s nodes=%d postgres_volumes=%d edge_vms=%d coturn_vms=%d\n", plan.ProviderServices.RequiredServices, limit, plan.ProviderServices.NodeServices, plan.ProviderServices.PostgresVolumes, plan.ProviderServices.EdgeVMs, plan.ProviderServices.CoturnVMs)
	}
}

func lkeCheckCapacity(env map[string]string, opts provisionOptions) error {
	return lkeCheckCapacityWithPaths(provisionPaths{}, env, opts)
}

func lkeCheckCapacityWithPaths(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if strings.TrimSpace(os.Getenv("LKE_CAPACITY_CHECK")) == "0" {
		return nil
	}
	plan, err := lkeCapacityPlan(env, opts)
	if err != nil {
		return err
	}
	if plan.NodeCount >= plan.RequiredNodes {
		if plan.ProviderServices.Limit > 0 && plan.ProviderServices.RequiredServices > plan.ProviderServices.Limit {
			return fmt.Errorf("LKE provider capacity check failed: required active services=%d exceeds LKE_LINODE_ACTIVE_SERVICE_LIMIT=%d (nodes=%d postgres_volumes=%d edge_vms=%d coturn_vms=%d); reduce LKE_NODE_COUNT, use LKE_POSTGRES_STORAGE_MODE=emptydir for ephemeral validation, reduce LKE_EDGE_HAPROXY_COUNT, reduce LKE_COTURN_VM_COUNT, or request a Linode quota increase", plan.ProviderServices.RequiredServices, plan.ProviderServices.Limit, plan.ProviderServices.NodeServices, plan.ProviderServices.PostgresVolumes, plan.ProviderServices.EdgeVMs, plan.ProviderServices.CoturnVMs)
		}
		if err := lkeCheckLiveProviderActiveServices(paths, env, plan.ProviderServices); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] capacity ok: node_count=%d required=%d workload_cpu=%dm workload_memory=%dMi\n", plan.NodeCount, plan.RequiredNodes, plan.WorkloadCPU, plan.WorkloadMemMi)
		return nil
	}
	lines := []string{
		fmt.Sprintf("LKE capacity check failed: LKE_NODE_COUNT=%d is smaller than required_nodes=%d for %s", plan.NodeCount, plan.RequiredNodes, plan.NodeType),
		fmt.Sprintf("per_node_allocatable=cpu:%dm,memory:%dMi per_node_system_reserved=cpu:%dm,memory:%dMi", plan.AllocatableCPU, plan.AllocatableMemMi, plan.SystemCPUPerNode, plan.SystemMemPerNode),
		fmt.Sprintf("workload_requests=cpu:%dm,memory:%dMi required_nodes_cpu=%d required_nodes_memory=%d required_nodes_spread=%d", plan.WorkloadCPU, plan.WorkloadMemMi, plan.RequiredCPUNode, plan.RequiredMemNode, plan.RequiredSpread),
	}
	for _, item := range plan.Workloads {
		if item.CPUMilli == 0 && item.MemoryMi == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s replicas=%d request=cpu:%dm,memory:%dMi", item.Name, item.Replicas, item.CPUMilli, item.MemoryMi))
	}
	lines = append(lines, "Increase LKE_NODE_COUNT/LKE_NODE_TYPE or lower explicit staging resource requests in env/stack.env before provisioning.")
	return errors.New(strings.Join(lines, "\n"))
}

func lkeCheckLiveProviderActiveServices(paths provisionPaths, env map[string]string, plan lkeProviderServicePlan) error {
	if plan.Limit <= 0 || strings.TrimSpace(paths.EnvRoot) == "" {
		return nil
	}
	token := resolveLinodeToken(paths.EnvRoot)
	if token == "" {
		return nil
	}
	out, err := linodeRequestRaw(token, "GET", "/linode/instances?page_size=500", "")
	if err != nil {
		return err
	}
	var listed struct {
		Data []linodeInstance `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return err
	}
	activeLabels := map[string]bool{}
	for _, item := range listed.Data {
		activeLabels[item.Label] = true
	}
	current := len(listed.Data)
	additional := plan.RequiredServices
	reducible := 0
	if cluster, err := discoverLKECluster(token, paths, env, false); err == nil && cluster.ID > 0 {
		additional = 0
		if plan.EdgeVMs > 0 && !activeLabels[lkeEdgeHAProxyLabel(env)] {
			additional++
		}
		for i := 1; i <= plan.CoturnVMs; i++ {
			if !activeLabels[lkeCoturnVMLabel(lkeCoturnVMEnvForIndex(env, i))] {
				additional++
			}
		}
		reducible = lkeReducibleMainNodeServices(token, cluster, env, plan.NodeServices)
	}
	projected := current - reducible + additional
	if projected > plan.Limit {
		return fmt.Errorf("LKE live provider capacity check failed: projected active services=%d exceeds LKE_LINODE_ACTIVE_SERVICE_LIMIT=%d (current_active=%d reducible_lke_nodes=%d additional_required=%d edge_vms=%d coturn_vms=%d); delete unused Linode services or request a Linode quota increase before rerunning staging provision", projected, plan.Limit, current, reducible, additional, plan.EdgeVMs, plan.CoturnVMs)
	}
	fmt.Fprintf(os.Stderr, "[lke] provider active services ok: current=%d reducible_lke_nodes=%d additional_required=%d projected=%d limit=%d\n", current, reducible, additional, projected, plan.Limit)
	return nil
}

func lkeReducibleMainNodeServices(token string, cluster lkeCluster, env map[string]string, desiredCount int) int {
	if desiredCount <= 0 {
		return 0
	}
	pools, err := listLKENodePools(token, strconv.Itoa(cluster.ID))
	if err != nil {
		return 0
	}
	desiredType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	for _, pool := range pools {
		if pool.Type != desiredType || lkeNodePoolHasPostgresPlacement(pool) {
			continue
		}
		if pool.Count > desiredCount {
			return pool.Count - desiredCount
		}
		return 0
	}
	return 0
}

func lkeCapacityPlan(env map[string]string, opts provisionOptions) (lkeCapacityPlanResult, error) {
	_ = opts
	brokerCount, err := lkeNodeCount(env)
	if err != nil {
		return lkeCapacityPlanResult{}, err
	}
	class := strings.ToUpper(firstNonEmpty(env["MQTT_NODE_CLASS"], "broker"))
	nodeCount := brokerCount
	nodeType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	if class == "GENERAL" {
		nodeCount = envIntFrom(env, "LKE_GENERAL_NODE_COUNT", 0)
		nodeType = firstNonEmpty(env["LKE_GENERAL_NODE_TYPE"], nodeType)
	} else if class == "DATABASE" {
		nodeCount = envIntFrom(env, "LKE_POSTGRES_NODE_COUNT", 0)
		nodeType = firstNonEmpty(env["LKE_POSTGRES_NODE_TYPE"], nodeType)
	}
	allocCPU := envIntFrom(env, "NODE_CLASS_"+class+"_USABLE_CPU_MILLI", 0)
	allocMem := envIntFrom(env, "NODE_CLASS_"+class+"_USABLE_MEMORY_MIB", 0)
	totalCPU := envIntFrom(env, "NODE_CLASS_"+class+"_TOTAL_REQUEST_CPU_MILLI", 0)
	totalMem := envIntFrom(env, "NODE_CLASS_"+class+"_TOTAL_REQUEST_MEMORY_MIB", 0)
	required := envIntFrom(env, "NODE_CLASS_"+class+"_EFFECTIVE_COUNT", nodeCount)
	targetConnects := lkeTargetConnects(env)
	requiredMQTT := lkeMQTTReplicas(env)
	mqttCapacity := requiredMQTT * lkeMQTTConnectionsPerPod(env)
	providerServices := lkeProviderServices(env, nodeCount)
	requiredCPU := envIntFrom(env, "NODE_CLASS_"+class+"_REQUIRED_BY_CPU", 0)
	requiredMemory := envIntFrom(env, "NODE_CLASS_"+class+"_REQUIRED_BY_MEMORY", 0)
	requiredSpread := envIntFrom(env, "NODE_CLASS_"+class+"_REQUIRED_BY_SPREAD", requiredMQTT)
	return lkeCapacityPlanResult{
		NodeType: nodeType, NodeCount: nodeCount, TargetConnects: targetConnects, MQTTCapacity: mqttCapacity, RequiredMQTTPods: requiredMQTT, AllocatableCPU: allocCPU, AllocatableMemMi: allocMem,
		SystemCPUPerNode: envIntFrom(env, "CAPACITY_SYSTEM_RESERVED_CPU_MILLI", 0), SystemMemPerNode: envIntFrom(env, "CAPACITY_SYSTEM_RESERVED_MEMORY_MIB", 0),
		WorkloadCPU: totalCPU, WorkloadMemMi: totalMem, RequiredCPUNode: requiredCPU, RequiredMemNode: requiredMemory, RequiredSpread: requiredSpread, RequiredNodes: required, ProviderServices: providerServices,
	}, nil
}

func lkeProviderServices(env map[string]string, nodeCount int) lkeProviderServicePlan {
	workerNodes := nodeCount
	if _, hasBroker := env["LKE_NODE_COUNT"]; hasBroker {
		workerNodes = maxInt(envIntFrom(env, "LKE_NODE_COUNT", 0), 0) + maxInt(envIntFrom(env, "LKE_GENERAL_NODE_COUNT", 0), 0)
	}
	if lkePostgresDedicatedNodePoolEnabled(env) {
		workerNodes += maxInt(envIntFrom(env, "LKE_POSTGRES_NODE_COUNT", 1), 0)
	}
	postgresVolumes := 0
	if lkePostgresUsesPVC(env) {
		postgresVolumes = 1
	}
	edgeVMs := envIntFrom(env, "LKE_EDGE_HAPROXY_COUNT", 1)
	if edgeVMs < 0 {
		edgeVMs = 0
	}
	coturnVMs := lkeCoturnVMCount(env)
	limit := envIntFrom(env, "LKE_LINODE_ACTIVE_SERVICE_LIMIT", 0)
	required := workerNodes + postgresVolumes + edgeVMs + coturnVMs
	return lkeProviderServicePlan{
		NodeServices:     workerNodes,
		PostgresVolumes:  postgresVolumes,
		EdgeVMs:          edgeVMs,
		CoturnVMs:        coturnVMs,
		RequiredServices: required,
		Limit:            limit,
	}
}

func envIntFrom(env map[string]string, key string, fallback int) int {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv(key), env[key]))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func lkeRecommendedNodeCount(env map[string]string) (int, error) {
	count := envIntFrom(env, "NODE_CLASS_BROKER_EFFECTIVE_COUNT", 0)
	if count <= 0 {
		return 0, errors.New("shared capacity plan missing NODE_CLASS_BROKER_EFFECTIVE_COUNT")
	}
	return count, nil
}

func lkeNodeAllocatable(nodeType string, env map[string]string) (int, int, error) {
	if cpuRaw := firstNonEmpty(os.Getenv("LKE_NODE_ALLOCATABLE_CPU"), env["LKE_NODE_ALLOCATABLE_CPU"]); cpuRaw != "" {
		memRaw := firstNonEmpty(os.Getenv("LKE_NODE_ALLOCATABLE_MEMORY"), env["LKE_NODE_ALLOCATABLE_MEMORY"])
		if memRaw == "" {
			return 0, 0, errors.New("LKE_NODE_ALLOCATABLE_MEMORY is required when LKE_NODE_ALLOCATABLE_CPU is set")
		}
		cpu, err := parseCPUQuantity(cpuRaw)
		if err != nil {
			return 0, 0, fmt.Errorf("LKE_NODE_ALLOCATABLE_CPU: %w", err)
		}
		mem, err := parseMemoryMi(memRaw)
		if err != nil {
			return 0, 0, fmt.Errorf("LKE_NODE_ALLOCATABLE_MEMORY: %w", err)
		}
		return cpu, mem, nil
	}
	shape, ok := lkeNodeTypeShape(nodeType)
	if !ok {
		return 0, 0, fmt.Errorf("unknown LKE_NODE_TYPE=%s; set LKE_NODE_ALLOCATABLE_CPU and LKE_NODE_ALLOCATABLE_MEMORY", nodeType)
	}
	cpu := shape.cpu*1000 - 70
	mem := shape.memoryGi*1024 - 1292
	if cpu < 100 {
		cpu = shape.cpu * 1000
	}
	if mem < 256 {
		mem = shape.memoryGi * 1024
	}
	return cpu, mem, nil
}

type lkeNodeType struct {
	cpu      int
	memoryGi int
}

func lkeNodeTypeShape(nodeType string) (lkeNodeType, bool) {
	shape, ok := lkeNodeTypeCatalog()[nodeType]
	return shape, ok
}

func lkeNodeTypeCatalog() map[string]lkeNodeType {
	return map[string]lkeNodeType{
		"g6-standard-1":  {cpu: 1, memoryGi: 2},
		"g6-standard-2":  {cpu: 2, memoryGi: 4},
		"g6-standard-4":  {cpu: 4, memoryGi: 8},
		"g6-standard-6":  {cpu: 6, memoryGi: 16},
		"g6-standard-8":  {cpu: 8, memoryGi: 32},
		"g6-standard-16": {cpu: 16, memoryGi: 64},
		"g6-standard-20": {cpu: 20, memoryGi: 96},
		"g6-standard-24": {cpu: 24, memoryGi: 128},
		"g6-standard-32": {cpu: 32, memoryGi: 192},
	}
}
