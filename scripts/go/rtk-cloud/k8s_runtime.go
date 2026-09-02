package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runKubernetesProvision(provider cloudProvider, ctx provisionContext) error {
	if rtkCloudTestMode() {
		// Deterministic, disposable test fixtures do not use the user's canonical
		// secret store.
		lkeRuntimeSecretStateDir = filepath.Join(ctx.Paths.EnvRoot, "state", "secrets")
		if err := loadLKEImageManifestDefaults(ctx.Paths.EnvRoot, ctx.Env); err != nil {
			return err
		}
		if ctx.Opts.mode.apply || ctx.Opts.mode.dns || ctx.Opts.mode.deploy || ctx.Opts.mode.artifacts || ctx.Opts.mode.e2e {
			if err := writeLKECompatibilityArtifacts(ctx.Paths, ctx.Env); err != nil {
				return err
			}
		}
		return runProvisionSteps(ctx, kubernetesProvisionSteps(provider))
	}
	environment := ctx.Env["CLOUD_ENV_NAME"]
	if environment == "" {
		environment = "staging"
	}
	store, restore, err := configureProvisionSecretStore(environment)
	if err != nil {
		return err
	}
	defer restore()
	activeCanonicalSecretStore = true
	defer func() {
		activeCanonicalSecretStore = false
		activeSecretEnvironmentRoot = ""
	}()
	lkeRuntimeSecretStateDir = store.RuntimeDir()
	if err := loadLKEImageManifestDefaults(ctx.Paths.EnvRoot, ctx.Env); err != nil {
		return err
	}
	if ctx.Opts.mode.reset {
		return errors.New("Kubernetes provision reset is not implemented; use remove-k8s for current staging teardown")
	}
	if ctx.Opts.mode.apply || ctx.Opts.mode.dns || ctx.Opts.mode.deploy || ctx.Opts.mode.artifacts || ctx.Opts.mode.e2e {
		if err := writeLKECompatibilityArtifacts(ctx.Paths, ctx.Env); err != nil {
			return err
		}
	}
	return runProvisionSteps(ctx, kubernetesProvisionSteps(provider))
}

type provisionStep struct {
	Name       string
	Phase      string
	Enabled    func(provisionContext) bool
	Run        func(provisionContext) error
	SkipReason func(provisionContext) string
}

func runProvisionSteps(ctx provisionContext, steps []provisionStep) error {
	for _, step := range steps {
		if step.Enabled != nil && !step.Enabled(ctx) {
			continue
		}
		if err := step.Run(ctx); err != nil {
			return err
		}
	}
	return nil
}

func kubernetesProvisionSteps(provider cloudProvider) []provisionStep {
	return []provisionStep{
		{
			Name:    "preflight",
			Phase:   "provider",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.preflight },
			Run: func(ctx provisionContext) error {
				if provider.Name() == "lke" {
					return lkePreflight(ctx.Paths, ctx.Env)
				}
				return provider.EnsureKubeAccess(ctx)
			},
		},
		{
			Name:    "plan",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.plan },
			Run: func(ctx provisionContext) error {
				printKubernetesProvisionPlan(provider, ctx)
				return nil
			},
		},
		{
			Name:    "dns-adapter-preflight",
			Phase:   "dns",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.dns },
			Run: func(ctx provisionContext) error {
				_, _, _, err := selectedDNSAdapter(ctx.Paths, ctx.Env)
				return err
			},
		},
		{
			Name:  "capacity-check",
			Phase: "runtime",
			Enabled: func(ctx provisionContext) bool {
				return provider.Name() == "lke" && (ctx.Opts.mode.preflight || ctx.Opts.mode.apply || ctx.Opts.mode.deploy || ctx.Opts.mode.e2e)
			},
			Run: func(ctx provisionContext) error {
				return lkeCheckCapacityWithPaths(ctx.Paths, ctx.Env, ctx.Opts)
			},
		},
		{
			Name:  "ensure-kube-access",
			Phase: "provider",
			Enabled: func(ctx provisionContext) bool {
				return ctx.Opts.mode.apply || ctx.Opts.mode.dns || ctx.Opts.mode.deploy || ctx.Opts.mode.e2e
			},
			Run: func(ctx provisionContext) error {
				if ctx.Opts.mode.deploy {
					if err := ensureLKEDeployImages(ctx.Env, ctx.Opts); err != nil {
						return err
					}
				}
				return provider.EnsureKubeAccess(ctx)
			},
		},
		{
			Name:  "ensure-lke-node-pool",
			Phase: "provider",
			Enabled: func(ctx provisionContext) bool {
				return provider.Name() == "lke" &&
					(ctx.Opts.mode.apply || ctx.Opts.mode.deploy) &&
					os.Getenv("RUNTIME_COVERAGE_SHARED_CLUSTER") != "1"
			},
			Run: func(ctx provisionContext) error {
				return ensureLKENodePool(ctx.Paths, ctx.Env)
			},
		},
		{
			Name:  "wait-kube-api-ready",
			Phase: "provider",
			Enabled: func(ctx provisionContext) bool {
				return ctx.Opts.mode.apply || ctx.Opts.mode.dns || ctx.Opts.mode.deploy || ctx.Opts.mode.e2e
			},
			Run: func(ctx provisionContext) error {
				return waitForKubernetesAPIReady()
			},
		},
		{
			Name:    "apply-base",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.apply },
			Run: func(ctx provisionContext) error {
				if err := lkeApplyBase(ctx.Env, ctx.Opts); err != nil {
					return err
				}
				if !ctx.Opts.mode.deploy {
					fmt.Fprintln(os.Stderr, "[lke-provision] apply complete without deploy; service images were not installed")
				}
				return nil
			},
		},
		{
			Name:    "deploy-workloads",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.deploy },
			Run: func(ctx provisionContext) error {
				if err := lkeDeployWorkloads(ctx.Paths, ctx.Env, ctx.Opts); err != nil {
					return err
				}
				if err := applySharedKubernetesNodeClassPlacement(ctx); err != nil {
					return err
				}
				return pruneLKEUnusedNodePools(ctx.Paths, ctx.Env)
			},
		},
		{
			Name:    "public-https",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.dns },
			Run: func(ctx provisionContext) error {
				return lkeApplyPublicHTTPS(ctx.Paths, ctx.Env, ctx.Opts)
			},
		},
		{
			Name:    "write-artifacts",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.artifacts },
			Run: func(ctx provisionContext) error {
				dir, err := writeLKEProvisionArtifacts(ctx.Paths, ctx.Env)
				if err != nil {
					return err
				}
				fmt.Fprintln(os.Stdout, dir)
				return nil
			},
		},
		{
			Name:    "e2e",
			Phase:   "runtime",
			Enabled: func(ctx provisionContext) bool { return ctx.Opts.mode.e2e },
			Run: func(ctx provisionContext) error {
				return lkeProvisionE2E(ctx.Env, ctx.Opts)
			},
		},
	}
}

func applySharedKubernetesNodeClassPlacement(ctx provisionContext) error {
	if ctx.Env["DEPLOYMENT_ARCHITECTURE"] == "" {
		return nil
	}
	labelKey := firstNonEmpty(ctx.Env["NODE_CLASS_LABEL_KEY"], "rtk.io/node-class")
	type placementTarget struct {
		namespace, kind, name, prefix string
	}
	targets := []placementTarget{}
	for _, workload := range lkeSelectedWorkloads(ctx.Env, ctx.Opts) {
		prefix := map[string]string{
			"video-cloud": "VIDEO_CLOUD_API", "account-manager": "ACCOUNT_MANAGER",
			"billing": "BILLING", "cloud-admin": "CLOUD_ADMIN", "frontend": "FRONTEND", "cloud-logger": "CLOUD_LOGGER",
		}[workload.Key]
		targets = append(targets, placementTarget{workload.Namespace, "deployment", workload.Name, prefix})
	}
	videoNS := lkeNamespaceName(ctx.Env, "video-cloud")
	for name, prefix := range map[string]string{
		"video-cloud-cleaner": "VIDEO_CLOUD_CLEANER", "video-cloud-clipverifier": "VIDEO_CLOUD_CLIP_VERIFIER", "video-cloud-statistics": "VIDEO_CLOUD_STATISTICS",
		"video-cloud-metricsexporter": "VIDEO_CLOUD_METRICS_EXPORTER", "video-cloud-turnregistry": "VIDEO_CLOUD_TURN_REGISTRY",
		"video-cloud-logingester": "VIDEO_CLOUD_LOG_INGESTER", "video-cloud-mqttusage": "VIDEO_CLOUD_MQTT_USAGE",
		"certissuer": "CERTISSUER", "factoryenroll": "FACTORY_ENROLL",
	} {
		targets = append(targets, placementTarget{videoNS, "deployment", name, prefix})
	}
	for _, target := range []placementTarget{
		{lkeNamespaceName(ctx.Env, "account-manager"), "deployment", "account-manager-email-worker", "ACCOUNT_MANAGER_EMAIL_WORKER"},
		{lkeNamespaceName(ctx.Env, "account-manager"), "deployment", "account-manager-outbox-worker", "ACCOUNT_MANAGER_OUTBOX_WORKER"},
		{lkeNamespaceName(ctx.Env, "billing"), "deployment", "billing-payment-worker", "BILLING_PAYMENT_WORKER"},
		{lkeNamespaceName(ctx.Env, "billing"), "deployment", "payment-simulator", "PAYMENT_SIMULATOR"},
	} {
		targets = append(targets, target)
	}
	targets = append(targets,
		placementTarget{lkeNamespaceName(ctx.Env, "platform"), "deployment", "redis", "REDIS"},
		placementTarget{lkeNamespaceName(ctx.Env, "platform"), "deployment", "redis-exporter", "REDIS_EXPORTER"},
		placementTarget{lkeNamespaceName(ctx.Env, "observability"), "deployment", "video-cloud-prometheus", "PROMETHEUS"},
		placementTarget{lkeNamespaceName(ctx.Env, "observability"), "deployment", "video-cloud-loki", "LOKI"},
		placementTarget{lkeNamespaceName(ctx.Env, "observability"), "deployment", "video-cloud-grafana", "GRAFANA"},
		placementTarget{lkeNamespaceName(ctx.Env, "platform"), "statefulset", "postgresql", "POSTGRES"},
		placementTarget{videoNS, "statefulset", "mqtt", "MQTT"},
		placementTarget{lkeNamespaceName(ctx.Env, "secrets"), "statefulset", "openbao", "OPENBAO"},
	)
	activeTargets := make([]placementTarget, 0, len(targets))
	for _, target := range targets {
		found, err := kubectlCombinedOutput(nil, "-n", target.namespace, "get", target.kind, target.name, "--ignore-not-found=true", "-o", "name")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(found)) == "" {
			continue
		}
		class := firstNonEmpty(ctx.Env[target.prefix+"_NODE_CLASS"], ctx.Env["DEFAULT_WORKLOAD_NODE_CLASS"], "general")
		patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"nodeSelector":{%q:%q}}}}}`, labelKey, class)
		if err := runKubectl("-n", target.namespace, "patch", target.kind, target.name, "--type=merge", "-p", patch); err != nil {
			return err
		}
		activeTargets = append(activeTargets, target)
	}
	for _, target := range activeTargets {
		if target.kind != "deployment" {
			continue
		}
		if err := runKubectl("-n", target.namespace, "rollout", "status", "deployment/"+target.name, "--timeout", "5m"); err != nil {
			return err
		}
	}
	return nil
}

func printKubernetesProvisionPlan(provider cloudProvider, ctx provisionContext) {
	fmt.Fprintf(os.Stdout, "provider: %s\n", provider.Name())
	fmt.Fprintf(os.Stdout, "runtime: %s\n", provider.Runtime())
	fmt.Fprintln(os.Stdout, "steps:")
	for _, step := range kubernetesProvisionSteps(provider) {
		if step.Enabled != nil && !step.Enabled(ctx) {
			continue
		}
		fmt.Fprintf(os.Stdout, "- %s (%s)\n", step.Name, step.Phase)
	}
	if provider.Name() == "lke" {
		lkePlan(ctx.Env, ctx.Opts)
	}
}
