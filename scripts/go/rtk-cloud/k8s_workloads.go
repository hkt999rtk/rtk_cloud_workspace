package main

import (
	"os"
)

type k8sWorkload struct {
	Key            string
	Name           string
	EnvKey         string
	Image          string
	Namespace      string
	NamespaceKey   string
	Port           int
	Host           string
	MetricsEnabled bool
	MetricsPath    string
	MetricsJob     string
	MetricsService string
	MetricsPort    int
	ServiceEnabled bool
	RolloutTimeout string
}

type k8sAuxiliaryWorkload struct {
	Name        string
	Binary      string
	Port        int
	PortName    string
	MetricsPath string
	MetricsJob  string
}

type k8sPrometheusTarget struct {
	Job       string
	Namespace string
	Service   string
	Port      int
	Path      string
}

type k8sRolloutTarget struct {
	Namespace string
	Resource  string
	Timeout   string
}

type lkeWorkload = k8sWorkload
type lkeVideoCloudAuxiliaryService = k8sAuxiliaryWorkload
type lkePrometheusTarget = k8sPrometheusTarget
type lkeRolloutTarget = k8sRolloutTarget

func k8sWorkloads(env map[string]string) []k8sWorkload {
	return []k8sWorkload{
		{
			Key: "video-cloud", Name: "video-cloud-api", EnvKey: "LKE_VIDEO_CLOUD_IMAGE", Image: lkeEnvValue(env, "LKE_VIDEO_CLOUD_IMAGE"),
			Namespace: lkeNamespaceName(env, "video-cloud"), NamespaceKey: "video-cloud", Port: envIntDefault("LKE_VIDEO_CLOUD_PORT", 8080), Host: env["VIDEO_CLOUD_DOMAIN"],
			MetricsEnabled: true, MetricsPath: "/metrics/prometheus", MetricsPort: 80, ServiceEnabled: true, RolloutTimeout: "LKE_WORKLOAD_ROLLOUT_TIMEOUT",
		},
		{
			Key: "account-manager", Name: "account-manager", EnvKey: "LKE_ACCOUNT_MANAGER_IMAGE", Image: lkeEnvValue(env, "LKE_ACCOUNT_MANAGER_IMAGE"),
			Namespace: lkeNamespaceName(env, "account-manager"), NamespaceKey: "account-manager", Port: envIntDefault("LKE_ACCOUNT_MANAGER_PORT", 8080), Host: env["ACCOUNT_MANAGER_DOMAIN"],
			MetricsEnabled: true, MetricsPath: "/metrics/prometheus", MetricsPort: 80, ServiceEnabled: true, RolloutTimeout: "LKE_WORKLOAD_ROLLOUT_TIMEOUT",
		},
		{
			Key: "cloud-admin", Name: "cloud-admin", EnvKey: "LKE_CLOUD_ADMIN_IMAGE", Image: lkeEnvValue(env, "LKE_CLOUD_ADMIN_IMAGE"),
			Namespace: lkeNamespaceName(env, "admin"), NamespaceKey: "admin", Port: envIntDefault("LKE_CLOUD_ADMIN_PORT", 8080), Host: env["CLOUD_ADMIN_DOMAIN"],
			MetricsEnabled: true, MetricsPath: "/metrics/prometheus", MetricsPort: 80, ServiceEnabled: true, RolloutTimeout: "LKE_WORKLOAD_ROLLOUT_TIMEOUT",
		},
		{
			Key: "frontend", Name: "frontend", EnvKey: "LKE_FRONTEND_IMAGE", Image: lkeEnvValue(env, "LKE_FRONTEND_IMAGE"),
			Namespace: lkeNamespaceName(env, "frontend"), NamespaceKey: "frontend", Port: envIntDefault("LKE_FRONTEND_PORT", 8080), Host: firstNonEmpty(os.Getenv("LKE_FRONTEND_DOMAIN"), env["CLOUD_ADMIN_DOMAIN"]),
			MetricsEnabled: true, MetricsPath: "/metrics/prometheus", MetricsPort: 80, ServiceEnabled: true, RolloutTimeout: "LKE_WORKLOAD_ROLLOUT_TIMEOUT",
		},
		{
			Key: "cloud-logger", Name: "cloud-logger", EnvKey: "LKE_CLOUD_LOGGER_IMAGE", Image: lkeEnvValue(env, "LKE_CLOUD_LOGGER_IMAGE"),
			Namespace: lkeNamespaceName(env, "logger"), NamespaceKey: "logger", Port: envIntDefault("LKE_CLOUD_LOGGER_PORT", 18090), Host: env["CLOUD_LOGGER_DOMAIN"],
			ServiceEnabled: true, RolloutTimeout: "LKE_CLOUD_LOGGER_ROLLOUT_TIMEOUT",
		},
	}
}

func k8sAuxiliaryWorkloads() []k8sAuxiliaryWorkload {
	return []k8sAuxiliaryWorkload{
		{Name: "video-cloud-cleaner", Binary: "cleaner"},
		{Name: "video-cloud-clipverifier", Binary: "clipverifier", Port: 19500, PortName: "http", MetricsPath: "/metrics/prometheus", MetricsJob: "video-cloud-clip-verifier"},
		{Name: "video-cloud-statistics", Binary: "statistics"},
		{Name: "video-cloud-metricsexporter", Binary: "metricsexporter", Port: 19200, PortName: "http", MetricsPath: "/metrics/prometheus", MetricsJob: "video-cloud-metrics-exporter"},
		{Name: "video-cloud-turnregistry", Binary: "turnregistry", Port: 18190, PortName: "http", MetricsPath: "/metrics/prometheus"},
		{Name: "video-cloud-logingester", Binary: "logingester", Port: 19300, PortName: "http", MetricsPath: "/metrics/prometheus"},
		{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400, PortName: "http", MetricsPath: "/metrics/prometheus"},
	}
}

func k8sImageWorkloads(env map[string]string, opts provisionOptions) []k8sWorkload {
	workloads := []k8sWorkload{
		{Key: "postgres", Name: "postgresql", EnvKey: "LKE_POSTGRES_IMAGE", Image: lkeEnvValue(env, "LKE_POSTGRES_IMAGE"), Namespace: lkeNamespaceName(env, "platform"), NamespaceKey: "platform", Port: 5432},
	}
	workloads = append(workloads, k8sSelectedWorkloads(env, opts)...)
	return workloads
}

func k8sSelectedWorkloads(env map[string]string, opts provisionOptions) []k8sWorkload {
	workloads := k8sWorkloads(env)
	if opts.loggerOnly {
		selected := []k8sWorkload{}
		for _, workload := range workloads {
			if workload.Key == "cloud-logger" {
				selected = append(selected, workload)
			}
		}
		return selected
	}
	if !opts.videoOnly {
		return workloads
	}
	selected := []k8sWorkload{}
	for _, workload := range workloads {
		if workload.Key == "video-cloud" || workload.Key == "cloud-logger" {
			selected = append(selected, workload)
		}
	}
	return selected
}

func k8sMissingDeployImageWorkloads(env map[string]string, opts provisionOptions) []k8sWorkload {
	missing := []k8sWorkload{}
	for _, workload := range k8sSelectedWorkloads(env, opts) {
		if firstNonEmpty(os.Getenv(workload.EnvKey), workload.Image) == "" {
			missing = append(missing, workload)
		}
	}
	return missing
}

func k8sMissingBuildImageWorkloads(env map[string]string, opts provisionOptions) []k8sWorkload {
	missing := []k8sWorkload{}
	for _, workload := range k8sImageWorkloads(env, opts) {
		if firstNonEmpty(os.Getenv(workload.EnvKey), workload.Image) == "" {
			missing = append(missing, workload)
		}
	}
	return missing
}

func k8sWorkloadSelected(env map[string]string, opts provisionOptions, key string) bool {
	for _, workload := range k8sSelectedWorkloads(env, opts) {
		if workload.Key == key {
			return true
		}
	}
	return false
}

func k8sRolloutTargets(workloads []k8sWorkload, defaultTimeout string) []k8sRolloutTarget {
	targets := []k8sRolloutTarget{}
	for _, workload := range workloads {
		if workload.Key == "cloud-logger" {
			continue
		}
		targets = append(targets, k8sRolloutTarget{
			Namespace: workload.Namespace,
			Resource:  "deployment/" + workload.Name,
			Timeout:   defaultTimeout,
		})
	}
	return targets
}

func k8sRolloutTargetsFromEnv(workloads []k8sWorkload) []k8sRolloutTarget {
	targets := []k8sRolloutTarget{}
	for _, workload := range workloads {
		timeout := firstNonEmpty(os.Getenv(workload.RolloutTimeout), "5m")
		targets = append(targets, k8sRolloutTargets([]k8sWorkload{workload}, timeout)...)
	}
	return targets
}

func k8sPrometheusTargets(env map[string]string, opts provisionOptions) []k8sPrometheusTarget {
	targets := []k8sPrometheusTarget{}
	for _, workload := range k8sSelectedWorkloads(env, opts) {
		if !workload.MetricsEnabled {
			continue
		}
		port := workload.MetricsPort
		if port == 0 {
			port = 80
		}
		targets = append(targets, k8sPrometheusTarget{
			Job:       firstNonEmpty(workload.MetricsJob, workload.Name),
			Namespace: workload.Namespace,
			Service:   firstNonEmpty(workload.MetricsService, workload.Name),
			Port:      port,
			Path:      firstNonEmpty(workload.MetricsPath, "/metrics/prometheus"),
		})
	}
	if k8sWorkloadSelected(env, opts, "video-cloud") {
		videoNS := lkeNamespaceName(env, "video-cloud")
		for _, service := range k8sAuxiliaryWorkloads() {
			if service.Port == 0 || service.MetricsPath == "" {
				continue
			}
			targets = append(targets, k8sPrometheusTarget{
				Job:       firstNonEmpty(service.MetricsJob, service.Name),
				Namespace: videoNS,
				Service:   service.Name,
				Port:      service.Port,
				Path:      service.MetricsPath,
			})
		}
		targets = append(targets, k8sPrometheusTarget{
			Job:       "video-cloud-factoryenroll",
			Namespace: videoNS,
			Service:   "factoryenroll",
			Port:      80,
			Path:      "/metrics/prometheus",
		})
	}
	targets = append(targets, k8sPrometheusTarget{
		Job:       "redis-exporter",
		Namespace: lkeNamespaceName(env, "platform"),
		Service:   "redis-exporter",
		Port:      9121,
		Path:      "/metrics",
	})
	observabilityNS := lkeNamespaceName(env, "observability")
	targets = append(targets,
		k8sPrometheusTarget{
			Job:       "video-cloud-prometheus",
			Namespace: observabilityNS,
			Service:   "video-cloud-prometheus",
			Port:      9090,
			Path:      "/metrics",
		},
		k8sPrometheusTarget{
			Job:       "video-cloud-grafana",
			Namespace: observabilityNS,
			Service:   "video-cloud-grafana",
			Port:      3000,
			Path:      "/metrics",
		},
	)
	return targets
}
