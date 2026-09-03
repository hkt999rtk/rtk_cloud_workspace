package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type capacityWorkloadSpec struct {
	Name           string
	Prefix         string
	Scale          string
	SpreadReplicas bool
}

type capacityWorkloadPlan struct {
	NodeClass         string `json:"node_class"`
	MinimumReplicas   int    `json:"minimum_replicas"`
	EffectiveReplicas int    `json:"effective_replicas"`
	RequestCPUMilli   int    `json:"request_cpu_milli"`
	RequestMemoryMiB  int    `json:"request_memory_mib"`
	SpreadFloor       int    `json:"spread_floor"`
}

type capacityNodeClassPlan struct {
	PlanningVCPU       int `json:"planning_vcpu"`
	PlanningMemoryGiB  int `json:"planning_memory_gib"`
	UsableCPUMilli     int `json:"usable_cpu_milli"`
	UsableMemoryMiB    int `json:"usable_memory_mib"`
	TotalRequestCPU    int `json:"total_request_cpu_milli"`
	TotalRequestMemory int `json:"total_request_memory_mib"`
	RequiredByCPU      int `json:"required_by_cpu"`
	RequiredByMemory   int `json:"required_by_memory"`
	RequiredBySpread   int `json:"required_by_spread"`
	MinimumCount       int `json:"minimum_count"`
	EffectiveCount     int `json:"effective_count"`
}

type sharedCapacityPlan struct {
	Workloads   map[string]capacityWorkloadPlan  `json:"workloads"`
	NodeClasses map[string]capacityNodeClassPlan `json:"node_classes"`
}

var capacityWorkloadRegistry = []capacityWorkloadSpec{
	{Name: "ingress-nginx-controller", Prefix: "INGRESS"},
	{Name: "postgresql", Prefix: "POSTGRES"},
	{Name: "redis", Prefix: "REDIS"},
	{Name: "redis-exporter", Prefix: "REDIS_EXPORTER"},
	{Name: "mqtt", Prefix: "MQTT", Scale: "connections", SpreadReplicas: true},
	{Name: "video-cloud-api", Prefix: "VIDEO_CLOUD_API", Scale: "active-devices", SpreadReplicas: true},
	{Name: "account-manager", Prefix: "ACCOUNT_MANAGER", SpreadReplicas: true},
	{Name: "billing", Prefix: "BILLING"},
	{Name: "cloud-admin", Prefix: "CLOUD_ADMIN"},
	{Name: "frontend", Prefix: "FRONTEND"},
	{Name: "cloud-logger", Prefix: "CLOUD_LOGGER"},
	{Name: "video-cloud-cleaner", Prefix: "VIDEO_CLOUD_CLEANER"},
	{Name: "video-cloud-statistics", Prefix: "VIDEO_CLOUD_STATISTICS"},
	{Name: "video-cloud-metricsexporter", Prefix: "VIDEO_CLOUD_METRICS_EXPORTER"},
	{Name: "video-cloud-turnregistry", Prefix: "VIDEO_CLOUD_TURN_REGISTRY"},
	{Name: "video-cloud-logingester", Prefix: "VIDEO_CLOUD_LOG_INGESTER"},
	{Name: "video-cloud-mqttusage", Prefix: "VIDEO_CLOUD_MQTT_USAGE"},
	{Name: "certissuer", Prefix: "CERTISSUER"},
	{Name: "factoryenroll", Prefix: "FACTORY_ENROLL"},
	{Name: "prometheus", Prefix: "PROMETHEUS"},
	{Name: "grafana", Prefix: "GRAFANA"},
	{Name: "loki", Prefix: "LOKI"},
	{Name: "openbao", Prefix: "OPENBAO"},
}

func capacitySourceKeys() []string {
	keys := []string{"CAPACITY_ACTIVE_DEVICES", "CAPACITY_ACTIVE_DEVICES_PER_API_POD"}
	for _, spec := range capacityWorkloadRegistry {
		keys = append(keys, spec.Prefix+"_MIN_REPLICAS", spec.Prefix+"_NODE_CLASS", spec.Prefix+"_REQUEST_CPU", spec.Prefix+"_REQUEST_MEMORY")
	}
	return keys
}

func capacityWorkloadSpecForName(name string) (capacityWorkloadSpec, bool) {
	for _, spec := range capacityWorkloadRegistry {
		if spec.Name == name {
			return spec, true
		}
	}
	return capacityWorkloadSpec{}, false
}

func buildSharedCapacityPlan(values map[string]string) (sharedCapacityPlan, map[string]string, error) {
	plan := sharedCapacityPlan{Workloads: map[string]capacityWorkloadPlan{}, NodeClasses: map[string]capacityNodeClassPlan{}}
	effective := map[string]string{}
	for _, spec := range capacityWorkloadRegistry {
		minimum, err := positiveIntValue(spec.Prefix+"_MIN_REPLICAS", values[spec.Prefix+"_MIN_REPLICAS"])
		if err != nil {
			return plan, nil, err
		}
		replicas := minimum
		switch spec.Scale {
		case "connections":
			target, err := positiveIntValue("CAPACITY_TARGET_CONNECTIONS", values["CAPACITY_TARGET_CONNECTIONS"])
			if err != nil {
				return plan, nil, err
			}
			perPod, err := positiveIntValue("CAPACITY_CONNECTIONS_PER_MQTT_POD", values["CAPACITY_CONNECTIONS_PER_MQTT_POD"])
			if err != nil {
				return plan, nil, err
			}
			replicas = maxInt(minimum, ceilDiv(target, perPod))
		case "active-devices":
			target, err := positiveIntValue("CAPACITY_ACTIVE_DEVICES", values["CAPACITY_ACTIVE_DEVICES"])
			if err != nil {
				return plan, nil, err
			}
			perPod, err := positiveIntValue("CAPACITY_ACTIVE_DEVICES_PER_API_POD", values["CAPACITY_ACTIVE_DEVICES_PER_API_POD"])
			if err != nil {
				return plan, nil, err
			}
			replicas = maxInt(minimum, ceilDiv(target, perPod))
		}
		cpu, err := parseCPUQuantity(values[spec.Prefix+"_REQUEST_CPU"])
		if err != nil || cpu <= 0 {
			return plan, nil, fmt.Errorf("%s_REQUEST_CPU must be positive: %v", spec.Prefix, err)
		}
		memory, err := parseMemoryMi(values[spec.Prefix+"_REQUEST_MEMORY"])
		if err != nil || memory <= 0 {
			return plan, nil, fmt.Errorf("%s_REQUEST_MEMORY must be positive: %v", spec.Prefix, err)
		}
		spread := 1
		if spec.SpreadReplicas {
			spread = replicas
		}
		item := capacityWorkloadPlan{NodeClass: values[spec.Prefix+"_NODE_CLASS"], MinimumReplicas: minimum, EffectiveReplicas: replicas, RequestCPUMilli: cpu, RequestMemoryMiB: memory, SpreadFloor: spread}
		if item.NodeClass != "general" && item.NodeClass != "broker" && item.NodeClass != "database" {
			return plan, nil, fmt.Errorf("%s_NODE_CLASS must be general, broker, or database", spec.Prefix)
		}
		plan.Workloads[spec.Name] = item
		effective[spec.Prefix+"_EFFECTIVE_REPLICAS"] = strconv.Itoa(replicas)
	}

	for _, class := range []string{"general", "broker", "database"} {
		prefix := "NODE_CLASS_" + classKey(class)
		minimum, err := nonNegativeIntValue(prefix+"_MIN_COUNT", values[prefix+"_MIN_COUNT"])
		if err != nil {
			return plan, nil, err
		}
		vcpu, err := positiveIntValue(prefix+"_MIN_VCPU", values[prefix+"_MIN_VCPU"])
		if err != nil {
			return plan, nil, err
		}
		memoryGiB, err := positiveIntValue(prefix+"_MIN_MEMORY_GIB", values[prefix+"_MIN_MEMORY_GIB"])
		if err != nil {
			return plan, nil, err
		}
		reservedCPU, _ := strconv.Atoi(values["CAPACITY_SYSTEM_RESERVED_CPU_MILLI"])
		reservedMemory, _ := strconv.Atoi(values["CAPACITY_SYSTEM_RESERVED_MEMORY_MIB"])
		usableCPU := vcpu*1000 - reservedCPU
		usableMemory := memoryGiB*1024 - reservedMemory
		if usableCPU <= 0 || usableMemory <= 0 {
			return plan, nil, fmt.Errorf("node class %s has no resources after system reserve", class)
		}
		totalCPU, totalMemory, spread := 0, 0, 0
		for name, workload := range plan.Workloads {
			if workload.NodeClass != class {
				continue
			}
			if workload.RequestCPUMilli > usableCPU || workload.RequestMemoryMiB > usableMemory {
				return plan, nil, fmt.Errorf("workload %s cannot fit one %s planning node", name, class)
			}
			totalCPU += workload.RequestCPUMilli * workload.EffectiveReplicas
			totalMemory += workload.RequestMemoryMiB * workload.EffectiveReplicas
			spread = maxInt(spread, workload.SpreadFloor)
		}
		classPlan := capacityNodeClassPlan{
			PlanningVCPU: vcpu, PlanningMemoryGiB: memoryGiB, UsableCPUMilli: usableCPU, UsableMemoryMiB: usableMemory,
			TotalRequestCPU: totalCPU, TotalRequestMemory: totalMemory,
			RequiredByCPU: ceilDiv(totalCPU, usableCPU), RequiredByMemory: ceilDiv(totalMemory, usableMemory), RequiredBySpread: spread, MinimumCount: minimum,
		}
		classPlan.EffectiveCount = maxInt(minimum, classPlan.RequiredByCPU, classPlan.RequiredByMemory, classPlan.RequiredBySpread)
		plan.NodeClasses[class] = classPlan
		effective[prefix+"_EFFECTIVE_COUNT"] = strconv.Itoa(classPlan.EffectiveCount)
		effective[prefix+"_TOTAL_REQUEST_CPU_MILLI"] = strconv.Itoa(totalCPU)
		effective[prefix+"_TOTAL_REQUEST_MEMORY_MIB"] = strconv.Itoa(totalMemory)
		effective[prefix+"_USABLE_CPU_MILLI"] = strconv.Itoa(usableCPU)
		effective[prefix+"_USABLE_MEMORY_MIB"] = strconv.Itoa(usableMemory)
		effective[prefix+"_REQUIRED_BY_CPU"] = strconv.Itoa(classPlan.RequiredByCPU)
		effective[prefix+"_REQUIRED_BY_MEMORY"] = strconv.Itoa(classPlan.RequiredByMemory)
		effective[prefix+"_REQUIRED_BY_SPREAD"] = strconv.Itoa(classPlan.RequiredBySpread)
	}
	return plan, effective, nil
}

func classKey(class string) string {
	if class == "general" {
		return "GENERAL"
	}
	if class == "broker" {
		return "BROKER"
	}
	return "DATABASE"
}

func sortedCapacityWorkloadNames() []string {
	names := make([]string, 0, len(capacityWorkloadRegistry))
	for _, spec := range capacityWorkloadRegistry {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
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
	}{{"Gi", 1024}, {"G", 1000}, {"Mi", 1}, {"M", 1000.0 / 1024.0}, {"Ki", 1.0 / 1024.0}, {"K", 1000.0 / 1024.0 / 1024.0}}
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
