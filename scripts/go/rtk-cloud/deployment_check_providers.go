package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type deploymentProviderTask struct {
	check   deploymentCredentialCheck
	run     func(deploymentCredentialChecker) deploymentCredentialCheck
	write   bool
	profile bool
	skip    string
}

// collectDeploymentChecks is the checker facade's bounded provider engine.
// Legacy credentials-check and provision callers keep their existing entry points.
func (c deploymentCredentialChecker) collectDeploymentChecks(ctx context.Context, cfg deploymentConfig, envFile string, options deploymentCredentialCheckOptions, allowWrites bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
	if ctx == nil {
		ctx = context.Background()
	}
	c.session = &deploymentProviderSession{inventory: make(map[string]*deploymentInventoryEntry)}
	c.pullSemaphore = make(chan struct{}, 2)
	readOnly := options.readOnly || options.fast
	c.readOnly = readOnly
	var tasks []deploymentProviderTask
	values := map[string]string{}
	wanted := func(name string) bool { return options.selected == nil || options.selected[name] }
	add := func(id, name, resource, level string, profile, write bool, depends []string, run func(deploymentCredentialChecker) deploymentCredentialCheck) {
		if profile {
			depends = append([]string{"credentials.profile"}, depends...)
		}
		tasks = append(tasks, deploymentProviderTask{check: deploymentCredentialCheck{ID: id, Name: name, Resource: resource, EvidenceLevel: level, Required: !write || !readOnly, DependsOn: depends}, profile: profile, write: write, run: run})
	}
	needsProfile := wanted("linode") || wanted("ghcr") || wanted("dns") || wanted("storage")
	if needsProfile {
		add("credentials.profile", "credential env file", envFile, "local", false, false, nil, func(_ deploymentCredentialChecker) deploymentCredentialCheck {
			loaded, check := deploymentCredentialProfileValues(cfg.Environment, envFile, defaultDeploymentSharedCredentialFile())
			if check.Passed {
				values = loaded
				// Preserve verbatim GHCR credentials before launching concurrent readers.
				if info, err := os.Stat(envFile); err == nil && info.IsDir() {
					for _, name := range []string{"GHCR_PULL_USERNAME", "GHCR_PULL_TOKEN"} {
						if raw, err := os.ReadFile(filepath.Join(envFile, name)); err == nil {
							values[name] = string(raw)
						}
					}
				}
			}
			return check
		})
	}
	repair := options.createMissingObjectStorageBucket || options.grantObjectStorageBucketAccess
	// Preserve the legacy operations: resolved runtime-media bootstrap returns
	// directly; the env-only repair path also qualifies the other providers.
	if repair && cfg.Storage.RuntimeMedia.Bucket != "" {
		add("storage.repair", "Object Storage credential repair", cfg.Environment, "mutation", true, true, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
			// A repair changes key inventory; do not reuse pre-mutation snapshots.
			checker.session = nil
			if cfg.Storage.RuntimeMedia.Bucket != "" {
				err := checker.bootstrapRuntimeStorage(cfg, values, envFile)
				if err != nil {
					return deploymentCredentialCheck{Detail: err.Error()}
				}
				return deploymentCredentialCheck{Passed: true, Detail: "storage credential repair and validation completed"}
			}
			return checker.checkObjectStorageWithOptions(cfg, values, options)
		})
	} else {
		if wanted("linode") && cfg.Adapter == "lke" {
			add("provider.linode", "Linode API", cfg.Environment, "authenticated-read", true, false, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
				return checker.checkLinode(values)
			})
		} else if options.selected["linode"] {
			add("provider.linode", "Linode API", cfg.Environment, "configured", false, false, nil, unconfiguredProvider)
		}
		if wanted("ghcr") && cfg.Adapter == "lke" {
			if len(options.images) == 0 {
				for _, source := range lkeServiceImageSources() {
					repository := "hkt999rtk/" + source.RepoName + "/" + source.Name
					add("provider.ghcr."+source.Key, "GHCR pull "+repository, repository, "repository-read", true, false, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
						return checker.checkGHCRRepository(values, repository)
					})
				}
			} else {
				for _, image := range uniqueNonEmpty(options.images...) {
					level := "full-pull"
					if options.fast {
						level = "manifest-platform"
					}
					add("provider.ghcr.image."+image, "GHCR release image "+image, image, level, true, false, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
						if options.fast {
							return checker.checkRolloutImageMetadata(values, image)
						}
						return checker.checkRolloutImage(values, image)
					})
					if options.fast {
						add("provider.ghcr.pull."+image, "Full image pull "+image, image, "full-pull", false, false, nil, nil)
						tasks[len(tasks)-1].skip = "fast mode verifies image metadata; full layer pull was not requested"
						tasks[len(tasks)-1].check.Required = false
					}
				}
			}
		} else if options.selected["ghcr"] {
			add("provider.ghcr", "GHCR pull", cfg.Environment, "configured", false, false, nil, unconfiguredProvider)
		}
		if wanted("dns") {
			switch cfg.DNSAdapter {
			case "godaddy":
				add("provider.dns.read", "GoDaddy DNS read", cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], "authenticated-read", true, false, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					checker.readOnly = true
					return checker.checkGoDaddy(cfg, values)
				})
				add("provider.dns.write", "GoDaddy DNS write/delete", cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], "write-canary", true, true, []string{"provider.dns.read"}, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					checker.readOnly = false
					return checker.checkGoDaddy(cfg, values)
				})
			case "route53":
				add("provider.dns.route53", "Route53 DNS", cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], "unsupported", false, false, nil, func(_ deploymentCredentialChecker) deploymentCredentialCheck {
					return deploymentCredentialCheck{Code: "UNSUPPORTED", Status: "ERROR", Detail: "Route53 credential qualification is not implemented; DNS access remains unverified", NextAction: "Use a supported qualification path before deployment; this result is not GO."}
				})
			default:
				if options.selected["dns"] {
					add("provider.dns", "DNS", cfg.Environment, "configured", false, false, nil, unconfiguredProvider)
				}
			}
		}
		if repair {
			add("storage.repair", "Object Storage credential repair", cfg.Environment, "mutation", true, true, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
				checker.session = nil
				return checker.checkObjectStorageWithOptions(cfg, values, options)
			})
		} else if wanted("storage") {
			media := strings.EqualFold(strings.TrimSpace(cfg.Values["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"]), "true")
			count := 0
			storage := func(id, name, resource string, run func(deploymentCredentialChecker) deploymentCredentialCheck) {
				count++
				add(id+".read", name+" read", resource, "authenticated-read", true, false, nil, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					checker.readOnly = true
					return run(checker)
				})
				add(id+".write", name+" write/delete", resource, "write-canary", true, true, []string{id + ".read"}, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					checker.readOnly = false
					return run(checker)
				})
			}
			if media {
				storage("provider.storage.media", "Runtime media storage", cfg.Storage.RuntimeMedia.Bucket, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					return checker.checkObjectStorageWithOptions(cfg, values, options)
				})
				// The legacy env-only bucket path proves signed list access only.
				if cfg.Storage.RuntimeMedia.Bucket == "" {
					tasks = tasks[:len(tasks)-1]
				}
			}
			if cfg.Storage.OTAMode == "dedicated" && lkeOTAServiceRegistrationEnabled(cfg.Values) {
				storage("provider.storage.ota", "OTA firmware storage", cfg.Storage.OTAFirmware.Bucket, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					return checker.checkResolvedOTAStorage(cfg, values)
				})
			}
			if cfg.Storage.ReleaseArtifacts.Bucket != "" {
				storage("provider.storage.artifacts", "Release artifact storage", cfg.Storage.ReleaseArtifacts.Bucket, func(checker deploymentCredentialChecker) deploymentCredentialCheck {
					return checker.checkResolvedArtifactStorage(cfg, values)
				})
			}
			if count == 0 && options.selected["storage"] {
				add("provider.storage", "Object Storage", cfg.Environment, "configured", false, false, nil, unconfiguredProvider)
			}
		}
		if options.tls.cert != "" {
			add("local.tls", "rollout TLS", options.tls.hostname, "local", false, false, nil, func(_ deploymentCredentialChecker) deploymentCredentialCheck { return checkRolloutTLS(options.tls) })
		}
		for _, path := range uniqueNonEmpty(options.manifests...) {
			add("local.mounts."+path, "rollout Secret mounts "+filepath.Base(path), path, "local", false, false, nil, func(_ deploymentCredentialChecker) deploymentCredentialCheck {
				return checkRolloutMountsWithRequirement(path, !options.imageUpgrade)
			})
		}
	}
	// Coverage explicitly records optional checks the caller did not request.
	optional := func(id, name, detail string) {
		add(id, name, "", "not-requested", false, false, nil, nil)
		tasks[len(tasks)-1].check.Required = false
		tasks[len(tasks)-1].skip = detail
	}
	if !repair {
		for _, name := range []string{"linode", "ghcr", "dns", "storage"} {
			if !wanted(name) {
				optional("provider."+name, name+" qualification", "provider is outside the explicitly selected checks")
				continue
			}
			planned := false
			for _, task := range tasks {
				if task.check.ID == "provider."+name || strings.HasPrefix(task.check.ID, "provider."+name+".") {
					planned = true
					break
				}
			}
			if !planned {
				optional("provider."+name, name+" qualification", "provider is not configured for this environment")
				tasks[len(tasks)-1].check.Code = "NOT_APPLICABLE"
			}
		}
		if options.tls.cert == "" {
			optional("local.tls", "rollout TLS", "TLS inputs were not supplied")
		}
		if len(options.manifests) == 0 {
			optional("local.mounts", "rollout Secret mounts", "rendered workload inputs were not supplied")
		}
	}
	results := make([]deploymentCredentialCheck, len(tasks))
	var emitMu sync.Mutex
	publish := func(check deploymentCredentialCheck) {
		if emit != nil {
			emitMu.Lock()
			defer emitMu.Unlock()
			emit(check)
		}
	}
	for i, task := range tasks {
		results[i] = task.check
		results[i].Status = "PENDING"
		publish(results[i])
	}
	if options.planOnly {
		return results
	}
	finishBlocked := func(i int, code, detail string) {
		result := tasks[i].check
		result.Status, result.Code, result.Detail = "BLOCKED", code, detail
		result.EvidenceTime = time.Now().UTC().Format(time.RFC3339Nano)
		result.NextAction = "Resolve the failed prerequisite and rerun this check."
		results[i] = result
		publish(result)
	}
	execute := func(i int) {
		task := tasks[i]
		running := task.check
		running.Status = "RUNNING"
		publish(running)
		start := time.Now()
		trace := &deploymentProviderTrace{}
		checker := c
		checker.trace = trace
		checker = checker.withCheckContext(ctx)
		result := task.run(checker)
		result.ID, result.Name, result.Resource = task.check.ID, task.check.Name, task.check.Resource
		result.Required, result.DependsOn = task.check.Required, task.check.DependsOn
		if result.EvidenceLevel == "" {
			result.EvidenceLevel = task.check.EvidenceLevel
		}
		result.DurationMS = time.Since(start).Milliseconds()
		result.EvidenceTime = time.Now().UTC().Format(time.RFC3339Nano)
		trace.mu.Lock()
		result.Attempts, result.Reused = trace.attempts, trace.reused
		transportCode := trace.code
		trace.mu.Unlock()
		if result.Attempts == 0 {
			result.Attempts = 1
		}
		if ctx.Err() != nil {
			if result.Passed || result.Detail == "" {
				result.Detail = "overall check deadline or cancellation prevented complete evidence"
			}
			result.Passed = false
			result.Status, result.Code = "ERROR", deploymentRequestCode(ctx.Err())
		}
		if result.Passed {
			result.Status = "PASS"
		} else {
			if result.Code == "" {
				result.Code = transportCode
			}
			if ctx.Err() != nil {
				result.Code = deploymentRequestCode(ctx.Err())
			}
			if result.Code == "" && (strings.HasPrefix(task.check.ID, "provider.") || task.check.ID == "storage.repair") && deploymentMalformedProviderResponse(result.Detail) {
				result.Code = "INVALID_RESPONSE"
			}
			if result.Code == "" {
				result.Code = "CHECK_FAILED"
			}
			if result.Status == "" {
				result.Status = "FAIL"
				if keySet("TIMEOUT", "CANCELED", "CONNECTION_FAILED", "DNS_LOOKUP_FAILED", "TLS_FAILED", "PROVIDER_UNAVAILABLE", "RATE_LIMITED", "INVALID_RESPONSE", "INVALID_IMAGE_METADATA")[result.Code] {
					result.Status = "ERROR"
				}
			}
			if result.NextAction == "" {
				result.NextAction = deploymentProviderNextAction(result.Code)
			}
		}
		results[i] = result
		publish(result)
		// Mutation tasks run serially after readers finish. Surface a failed
		// cleanup independently even when the original request was canceled.
		if task.write && strings.Contains(result.Detail, "cleanup failed") {
			cleanup := deploymentCredentialCheck{ID: task.check.ID + ".cleanup", Name: task.check.Name + " cleanup", Resource: task.check.Resource, Status: "ERROR", Code: "CANARY_CLEANUP_FAILED", Required: true, Attempts: 1, DependsOn: []string{task.check.ID}, Detail: result.Detail, NextAction: "Inspect the reserved canary in the selected resource and complete its cleanup before rerunning.", EvidenceTime: result.EvidenceTime, EvidenceLevel: "cleanup"}
			results = append(results, cleanup)
			publish(cleanup)
		}
	}
	profilePassed := true
	for i, task := range tasks {
		if task.check.ID == "credentials.profile" {
			execute(i)
			profilePassed = results[i].Passed
		}
	}
	// Run local checks before network work, preserving quick feedback on inputs.
	for i, task := range tasks {
		if strings.HasPrefix(task.check.ID, "local.") && task.skip == "" {
			execute(i)
		}
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					finishBlocked(i, "CANCELED", "overall check canceled before this task started")
				} else {
					execute(i)
				}
			}
		}()
	}
	for i, task := range tasks {
		if task.check.ID == "credentials.profile" || (strings.HasPrefix(task.check.ID, "local.") && task.skip == "") {
			continue
		}
		if task.skip != "" || (task.write && readOnly) {
			result := task.check
			result.Status = "SKIPPED"
			if result.Code == "" {
				result.Code = "NOT_REQUESTED"
			}
			result.Detail = task.skip
			if result.Detail == "" {
				result.Detail = "read-only qualification excludes writes and validation receipts"
			}
			results[i] = result
			publish(result)
			continue
		}
		if task.profile && !profilePassed {
			finishBlocked(i, "PREREQUISITE_FAILED", "canonical credential profile could not be loaded")
			continue
		}
		if task.write {
			continue
		}
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	passed := map[string]bool{}
	for _, result := range results {
		passed[result.ID] = result.Passed
	}
	// Mutations retain ordered, synchronous execution and are never part of the
	// readonly pool. Secret failure blocks writes without hiding read diagnostics.
	for i, task := range tasks {
		if !task.write || results[i].Status != "PENDING" {
			continue
		}
		if !allowWrites {
			finishBlocked(i, "PREREQUISITE_FAILED", "Secret verification did not authorize mutation checks")
			continue
		}
		dependencyOK := true
		for _, id := range task.check.DependsOn {
			if !passed[id] {
				dependencyOK = false
			}
		}
		if !dependencyOK {
			finishBlocked(i, "PREREQUISITE_FAILED", "provider read prerequisites did not pass")
			continue
		}
		if ctx.Err() != nil {
			finishBlocked(i, "CANCELED", "overall check canceled before mutation started")
			continue
		}
		execute(i)
		passed[results[i].ID] = results[i].Passed
	}
	return results
}

func unconfiguredProvider(_ deploymentCredentialChecker) deploymentCredentialCheck {
	return deploymentCredentialCheck{Code: "NOT_CONFIGURED", Detail: "requested check is not configured for this environment"}
}
func deploymentProviderNextAction(code string) string {
	switch code {
	case "AUTH_REJECTED", "ACCESS_DENIED":
		return "Check the selected environment credential and its required provider permissions."
	case "TIMEOUT", "CONNECTION_FAILED", "DNS_LOOKUP_FAILED", "TLS_FAILED":
		return "Check network reachability and trust, then rerun the affected check."
	case "RATE_LIMITED", "PROVIDER_UNAVAILABLE":
		return "Retry after provider recovery; do not treat this result as successful qualification."
	default:
		return fmt.Sprintf("Resolve %s using the check detail, then rerun qualification.", code)
	}
}

func deploymentMalformedProviderResponse(detail string) bool {
	lower := strings.ToLower(detail)
	for _, pattern := range []string{"invalid json", "invalid xml", "invalid godaddy record response", "invalid image metadata", "invalid image config", "invalid data", "omitted a valid sha256", "inventory omitted", "unexpected page"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
