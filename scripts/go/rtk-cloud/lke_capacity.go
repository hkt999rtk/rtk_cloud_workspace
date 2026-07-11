package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
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
		if plan.TargetConnects > 0 && plan.MQTTCapacity < plan.TargetConnects {
			return fmt.Errorf("LKE capacity check failed: target_connects=%d requires at least %d MQTT replicas, current capacity=%d; set LKE_MQTT_REPLICAS=auto or increase LKE_MQTT_REPLICAS", plan.TargetConnects, plan.RequiredMQTTPods, plan.MQTTCapacity)
		}
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
	nodeCount, err := lkeNodeCount(env)
	if err != nil {
		return lkeCapacityPlanResult{}, err
	}
	nodeType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	allocCPU, allocMem, err := lkeNodeAllocatable(nodeType, env)
	if err != nil {
		return lkeCapacityPlanResult{}, err
	}
	systemCPU, err := parseCPUQuantity(firstNonEmpty(os.Getenv("LKE_SYSTEM_RESERVED_CPU_PER_NODE"), env["LKE_SYSTEM_RESERVED_CPU_PER_NODE"], "500m"))
	if err != nil {
		return lkeCapacityPlanResult{}, fmt.Errorf("LKE_SYSTEM_RESERVED_CPU_PER_NODE: %w", err)
	}
	systemMem, err := parseMemoryMi(firstNonEmpty(os.Getenv("LKE_SYSTEM_RESERVED_MEMORY_PER_NODE"), env["LKE_SYSTEM_RESERVED_MEMORY_PER_NODE"], "384Mi"))
	if err != nil {
		return lkeCapacityPlanResult{}, fmt.Errorf("LKE_SYSTEM_RESERVED_MEMORY_PER_NODE: %w", err)
	}
	items, err := lkeCapacityWorkloads(env, opts)
	if err != nil {
		return lkeCapacityPlanResult{}, err
	}
	totalCPU := 0
	totalMem := 0
	spreadMin := 1
	for _, item := range items {
		totalCPU += item.CPUMilli * item.Replicas
		totalMem += item.MemoryMi * item.Replicas
		if item.SpreadMin > spreadMin {
			spreadMin = item.SpreadMin
		}
	}
	cpuPerNode := allocCPU - systemCPU
	memPerNode := allocMem - systemMem
	if cpuPerNode <= 0 {
		return lkeCapacityPlanResult{}, fmt.Errorf("node type %s has no CPU headroom after system reserve", nodeType)
	}
	if memPerNode <= 0 {
		return lkeCapacityPlanResult{}, fmt.Errorf("node type %s has no memory headroom after system reserve", nodeType)
	}
	requiredCPU := ceilDiv(totalCPU, cpuPerNode)
	requiredMem := ceilDiv(totalMem, memPerNode)
	required := maxInt(requiredCPU, requiredMem, spreadMin)
	targetConnects := lkeTargetConnects(env)
	requiredMQTT := 0
	mqttCapacity := 0
	if targetConnects > 0 {
		perPod := lkeMQTTConnectionsPerPod(env)
		requiredMQTT = ceilDiv(targetConnects, perPod)
		mqttCapacity = lkeMQTTReplicas(env) * perPod
		if requiredMQTT > required {
			required = requiredMQTT
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left := items[i].CPUMilli*items[i].Replicas + items[i].MemoryMi*items[i].Replicas
		right := items[j].CPUMilli*items[j].Replicas + items[j].MemoryMi*items[j].Replicas
		if left == right {
			return items[i].Name < items[j].Name
		}
		return left > right
	})
	providerServices := lkeProviderServices(env, nodeCount)
	return lkeCapacityPlanResult{
		NodeType: nodeType, NodeCount: nodeCount, TargetConnects: targetConnects, MQTTCapacity: mqttCapacity, RequiredMQTTPods: requiredMQTT, AllocatableCPU: allocCPU, AllocatableMemMi: allocMem,
		SystemCPUPerNode: systemCPU, SystemMemPerNode: systemMem, Workloads: items,
		WorkloadCPU: totalCPU, WorkloadMemMi: totalMem, RequiredCPUNode: requiredCPU, RequiredMemNode: requiredMem,
		RequiredSpread: spreadMin, RequiredNodes: required, ProviderServices: providerServices,
	}, nil
}

func lkeProviderServices(env map[string]string, nodeCount int) lkeProviderServicePlan {
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
	required := nodeCount + postgresVolumes + edgeVMs + coturnVMs
	return lkeProviderServicePlan{
		NodeServices:     nodeCount,
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
	nodeType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	allocCPU, allocMem, err := lkeNodeAllocatable(nodeType, env)
	if err != nil {
		return 0, err
	}
	systemCPU, err := parseCPUQuantity(firstNonEmpty(os.Getenv("LKE_SYSTEM_RESERVED_CPU_PER_NODE"), env["LKE_SYSTEM_RESERVED_CPU_PER_NODE"], "500m"))
	if err != nil {
		return 0, err
	}
	systemMem, err := parseMemoryMi(firstNonEmpty(os.Getenv("LKE_SYSTEM_RESERVED_MEMORY_PER_NODE"), env["LKE_SYSTEM_RESERVED_MEMORY_PER_NODE"], "384Mi"))
	if err != nil {
		return 0, err
	}
	items, err := lkeCapacityWorkloads(env, provisionOptions{})
	if err != nil {
		return 0, err
	}
	totalCPU := 0
	totalMem := 0
	spreadMin := 1
	for _, item := range items {
		totalCPU += item.CPUMilli * item.Replicas
		totalMem += item.MemoryMi * item.Replicas
		if item.SpreadMin > spreadMin {
			spreadMin = item.SpreadMin
		}
	}
	cpuNodes := ceilDiv(totalCPU, allocCPU-systemCPU)
	memNodes := ceilDiv(totalMem, allocMem-systemMem)
	return maxInt(cpuNodes, memNodes, spreadMin), nil
}

func lkeCapacityWorkloads(env map[string]string, opts provisionOptions) ([]lkeResourceRequest, error) {
	items := []lkeResourceRequest{}
	add := func(name string, replicas int, cpu, mem string, spread int) error {
		if replicas <= 0 {
			return nil
		}
		cpuMilli, err := parseCPUQuantity(cpu)
		if err != nil {
			return fmt.Errorf("%s cpu %q: %w", name, cpu, err)
		}
		memMi, err := parseMemoryMi(mem)
		if err != nil {
			return fmt.Errorf("%s memory %q: %w", name, mem, err)
		}
		items = append(items, lkeResourceRequest{Name: name, Replicas: replicas, CPUMilli: cpuMilli, MemoryMi: memMi, SpreadMin: spread})
		return nil
	}
	ingressReplicas, _ := strconv.Atoi(lkeIngressReplicas(env))
	if err := add("ingress-nginx-controller", ingressReplicas, firstNonEmpty(os.Getenv("LKE_INGRESS_REQUEST_CPU"), env["LKE_INGRESS_REQUEST_CPU"], "500m"), firstNonEmpty(os.Getenv("LKE_INGRESS_REQUEST_MEMORY"), env["LKE_INGRESS_REQUEST_MEMORY"], "512Mi"), 1); err != nil {
		return nil, err
	}
	if err := add("postgresql", 1, firstNonEmpty(os.Getenv("LKE_POSTGRES_REQUEST_CPU"), env["LKE_POSTGRES_REQUEST_CPU"], "1"), firstNonEmpty(os.Getenv("LKE_POSTGRES_REQUEST_MEMORY"), env["LKE_POSTGRES_REQUEST_MEMORY"], "2Gi"), 1); err != nil {
		return nil, err
	}
	if err := add("redis", 1, firstNonEmpty(os.Getenv("LKE_REDIS_REQUEST_CPU"), env["LKE_REDIS_REQUEST_CPU"], "100m"), firstNonEmpty(os.Getenv("LKE_REDIS_REQUEST_MEMORY"), env["LKE_REDIS_REQUEST_MEMORY"], "128Mi"), 1); err != nil {
		return nil, err
	}
	if err := add("redis-exporter", 1, firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_REQUEST_CPU"), env["LKE_REDIS_EXPORTER_REQUEST_CPU"], "50m"), firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_REQUEST_MEMORY"), env["LKE_REDIS_EXPORTER_REQUEST_MEMORY"], "64Mi"), 1); err != nil {
		return nil, err
	}
	mqttReplicas := lkeMQTTReplicas(env)
	if profile, ok := lkeContainerResourceProfile(env, "mqtt"); ok {
		if err := add("mqtt", mqttReplicas, profile.requestCPU, profile.requestMemory, mqttReplicas); err != nil {
			return nil, err
		}
	}
	for _, workload := range k8sSelectedWorkloads(env, opts) {
		profile, ok := lkeContainerResourceProfile(env, workload.Name)
		if !ok {
			continue
		}
		replicas := 1
		if raw, err := strconv.Atoi(lkeWorkloadReplicas(env, workload)); err == nil && raw > 0 {
			replicas = raw
		}
		spread := 1
		if workload.Name == "account-manager" || workload.Name == "video-cloud-api" {
			spread = replicas
		}
		if err := add(workload.Name, replicas, profile.requestCPU, profile.requestMemory, spread); err != nil {
			return nil, err
		}
	}
	if k8sWorkloadSelected(env, opts, "video-cloud") {
		for _, service := range k8sAuxiliaryWorkloads() {
			profile, ok := lkeContainerResourceProfile(env, service.Name)
			if !ok {
				continue
			}
			if err := add(service.Name, 1, profile.requestCPU, profile.requestMemory, 1); err != nil {
				return nil, err
			}
		}
	}
	return items, nil
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

func parseCPUQuantity(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	if strings.HasSuffix(value, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "m"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid CPU quantity %q", raw)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid CPU quantity %q", raw)
	}
	return int(math.Ceil(f * 1000)), nil
}

func parseMemoryMi(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"Gi", 1024}, {"G", 1000}, {"Mi", 1}, {"M", 1000.0 / 1024.0}, {"Ki", 1.0 / 1024.0}, {"K", 1000.0 / 1024.0 / 1024.0},
	}
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(value, unit.suffix), 64)
			if err != nil || f < 0 {
				return 0, fmt.Errorf("invalid memory quantity %q", raw)
			}
			return int(math.Ceil(f * unit.mult)), nil
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid memory quantity %q", raw)
	}
	return int(math.Ceil(float64(n) / 1024.0 / 1024.0)), nil
}

func ceilDiv(n, d int) int {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
