package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type unifiedDeploymentOptions struct {
	Action, Environment, EnvironmentRoot, Workspace, Operation, Confirm, PlanSHA256, Report string
	ParentPlanSHA256, EmailKeyRepairProof                                                   string
	PostCleanupHelperImage, PostCleanupControllerImage                                      string
	Timeout, TotalTimeout                                                                   time.Duration
}

// Complete qualification includes every image, resource admission and phase fence.
const defaultCompleteDeploymentTimeout = 45 * time.Minute

type unifiedDeploymentDependencies struct {
	ContinueCreate          func(context.Context, deploymentConfig, unifiedDeploymentOptions, *unifiedDeploymentReport, io.Writer, io.Writer) (bool, error)
	RecoveryPreflight       func(context.Context, deploymentConfig, unifiedDeploymentOptions, *unifiedDeploymentReport, io.Writer, io.Writer) error
	FreshCreate             func(context.Context, deploymentConfig, unifiedDeploymentOptions, *unifiedDeploymentReport, io.Writer, io.Writer) error
	InvalidateProof         func(deploymentConfig) (string, error)
	PublishProof            func(context.Context, *compiledEnvironmentDeployment, deploymentplan.Qualification) error
	LoadQualified           func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error)
	LiveChecks              func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck
	PublishBindings         func(deploymentConfig, *compiledEnvironmentDeployment, string) error
	PublishObservations     func(deploymentConfig, *compiledEnvironmentDeployment, string, func(deploymentConfig, *compiledEnvironmentDeployment, string) error) error
	RequireBindings         func(*compiledEnvironmentDeployment) error
	CheckExecutionAvailable func(context.Context, deploymentConfig, string) error
	PrepareStorage          func(context.Context, deploymentConfig, io.Writer) error
	Resolve                 func(string, string, string) (deploymentConfig, error)
	Compile                 func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error)
	Checks                  func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck
	Execute                 func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error
	Accept                  func(context.Context, *compiledEnvironmentDeployment) error
	Guard                   func([]string) error
}

func defaultUnifiedDeploymentDependencies() unifiedDeploymentDependencies {
	return unifiedDeploymentDependencies{
		ContinueCreate:    tryUnifiedCreateNormalTail,
		RecoveryPreflight: runEnvironmentCreateRecoveryPreflight,
		FreshCreate: func(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) error {
			return runUnifiedFreshEnvironmentDeployment(ctx, cfg, o, report, out, diagnostics, defaultEnvironmentCreateDependencies())
		},
		InvalidateProof: invalidateEnvironmentPreflightProofWithToken,
		PublishProof: func(ctx context.Context, c *compiledEnvironmentDeployment, q deploymentplan.Qualification) error {
			proof, err := saveEnvironmentPreflightProofContext(ctx, c, q, time.Now().UTC())
			if err == nil {
				c.Proof = proof
			}
			return err
		},
		LoadQualified:           loadQualifiedEnvironmentDeployment,
		LiveChecks:              executionEnvironmentChecks,
		PublishBindings:         publishEnvironmentPreflightBindings,
		PublishObservations:     publishEnvironmentPreflightObservation,
		CheckExecutionAvailable: checkEnvironmentExecutionAvailable,
		RequireBindings: func(c *compiledEnvironmentDeployment) error {
			c.RequirePreflightBindings = true
			return verifyEnvironmentPreflightBindings(c)
		},
		PrepareStorage: prepareDeploymentMediaBucket,
		Resolve:        resolveDeploymentConfig, Compile: compileEnvironmentDeployment,
		Guard: recoveryMutationGuard,
		Checks: func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
			return map[string]deploymentplan.GateCheck{
				"deployment.target": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
				},
				"deployment.execution": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					nextStep, abortSteps := environmentDeploymentSteps(ctx)
					defer abortSteps()
					if p.Operation != "upgrade" {
						nextStep("deployment.execution.provider-quota")
						if err := c.CheckProviderQuota(ctx, p); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
					}
					nextStep("deployment.execution.node-capacity")
					if err := c.CheckNodeCapacity(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.execution.surge-capacity")
					if err := c.CheckSurgeCapacity(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.execution.storage")
					if err := c.Backend.CheckStorageBindings(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.execution.mqtt-transport")
					if err := checkEnvironmentAppTURNPlan(p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkEnvironmentDeviceMQTTCallback(ctx, p, c.Backend.Read); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkEnvironmentLogMQTTTransport(ctx, p, c.Backend.Read); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkEnvironmentCoreMQTTTransport(ctx, p, c.Release.Images["LKE_ALPINE_IMAGE"].Reference, c.Backend.Read); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkEnvironmentManagedMQTTConsumers(ctx, p, c.Backend.Read); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					otaActive := false
					videoStorageActive := false
					for _, resource := range p.Resources {
						otaActive = otaActive || resource.Action != "retire" && otaStartupContainer(resource.Desired) != nil
						videoStorageActive = videoStorageActive || resource.Action != "retire" && videoStorageStartupContainer(resource.Desired) != nil
					}
					if videoStorageActive {
						nextStep("deployment.execution.video-storage-startup")
						operator, err := c.Store.readOperator()
						if err != nil {
							return deploymentplan.GateEvidence{}, videoStorageStartupBlocker("", "original canonical credential inputs cannot be inspected")
						}
						if err := checkEnvironmentVideoStorageStartup(ctx, p, c.Configuration, c.Store, c.Backend.Read, operator, defaultDeploymentCredentialChecker()); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
					}
					if otaActive {
						nextStep("deployment.execution.ota-startup")
						operator, err := c.Store.readOperator()
						if err != nil {
							return deploymentplan.GateEvidence{}, otaStartupBlocker("", "canonical OTA credential inputs cannot be inspected")
						}
						if err := checkEnvironmentOTAStartup(ctx, p, c.Backend.Read, operator); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
						if c.Configuration.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
							nextStep("deployment.execution.ota-storage")
							if err := checkOTAStorageAccess(ctx, c.Configuration, c.Store, operator, defaultDeploymentCredentialChecker()); err != nil {
								return deploymentplan.GateEvidence{}, err
							}
						}
						nextStep("deployment.execution.ota-schema")
						if err := checkEnvironmentOTASchema(ctx, p, c.Store, c.Backend.Read, func(ctx context.Context, args ...string) ([]byte, error) { return c.Backend.command(ctx, nil, args...) }); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
						nextStep("deployment.execution.ota-registration")
						if err := checkEnvironmentOTARegistration(ctx, p, c.Release, c.Store, c.Backend.Read, func(ctx context.Context, args ...string) ([]byte, error) { return c.Backend.command(ctx, nil, args...) }); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
					}
					nextStep("deployment.execution.permissions")
					if err := c.Backend.CheckPlanAccess(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.execution.lease")
					if err := c.Backend.CheckExecutionPrerequisites(ctx, p, c.Store.Root); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.execution.admission")
					admit := func() error { return p.Admit(ctx, c.Backend) }
					if _, ok := createNormalTailHandoffFromContext(ctx); ok {
						admit = func() error { return p.AdmitResumable(ctx, c.Backend, c.Store.Root) }
					}
					if err := admit(); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("")
					return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
				},
				"deployment.pki": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					nextStep, abortSteps := environmentDeploymentSteps(ctx)
					defer abortSteps()
					nextStep("deployment.pki.before-fence")
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("deployment.pki.identity-permissions")
					if err := verifyQualifiedControllerIdentityPermissions(ctx, c); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					window, err := environmentPKIValidityWindow(c.Configuration.Values)
					if err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					store := c.Store
					store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
					nextStep("deployment.pki.consumer-policy")
					if err := checkEnvironmentCRLConsumerPolicy(p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					// Compilation and the ownership fence already qualify the exact
					// candidate and retained identity references. Workload readiness
					// belongs to rollout acceptance, not certificate verification.
					store.checkRuntime.preflightPlan = p
					nextStep("deployment.pki.bindings")
					if err := verifySecretStoreK8SBindings(store); err != nil {
						return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.pki.bindings", Cause: "original runtime Secret bindings cannot be verified", Next: "restore the selected environment's original synchronized credentials and bindings; never generate replacement identity to pass"}
					}
					now := time.Now().UTC()
					c.PKICheckedAt, c.PKIVerifiedUntil = now, now.Add(time.Duration(window)*time.Second)
					for index, at := range []time.Time{now, now.Add(time.Duration(window) * time.Second)} {
						nextStep([]string{"deployment.pki.mqtt-now", "deployment.pki.mqtt-window"}[index])
						if err := c.CheckMQTTFoundationIdentity(ctx, at); err != nil {
							return deploymentplan.GateEvidence{}, err
						}
						nextStep([]string{"deployment.pki.runtime-now", "deployment.pki.runtime-window"}[index])
						if err := verifySecretStoreK8SRuntime(store, at); err != nil {
							return deploymentplan.GateEvidence{}, environmentRuntimePKIQualificationError(err)
						}
					}
					nextStep("deployment.pki.after-fence")
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					nextStep("")
					evidence := deploymentplan.GateEvidence{SHA256: p.SHA256(), PendingRolloutHealth: store.checkRuntime.pendingRolloutHealth()}
					return evidence, nil
				},
				"deployment.compatibility": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					store := c.Store
					store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
					if err := verifyEnvironmentReleaseSchemasForPlan(ctx, c.Configuration, store, c.Release, p, c.Backend.Read); err != nil {
						return deploymentplan.GateEvidence{}, environmentSchemaQualificationError(err)
					}
					for _, resource := range p.Resources {
						target, err := environmentPaymentSimulatorTarget(resource)
						if err != nil {
							return deploymentplan.GateEvidence{}, err
						}
						if target == nil {
							continue
						}
						container, err := deploymentUpgradeSimulatorContainer(*target)
						if err != nil {
							return deploymentplan.GateEvidence{}, environmentSimulatorCompatibilityBlocker(resource.ID, "candidate simulator executable or Pod specification is invalid")
						}
						if err := verifyDeploymentSimulatorMigrationSetting(store, *target); err != nil {
							return deploymentplan.GateEvidence{}, environmentSimulatorCompatibilityBlocker(resource.ID, "candidate simulator does not prove startup schema migration is disabled")
						}
						if err := verifyDeploymentSimulatorStartup(ctx, deploymentplan.String(container["image"])); err != nil {
							return deploymentplan.GateEvidence{}, environmentSimulatorCompatibilityBlocker(resource.ID, "candidate simulator startup could not be verified with its restricted database identity")
						}
					}
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
				},
				"deployment.network": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					store := c.Store
					store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
					if err := verifyDeploymentPublicCertIssuer(ctx, c.Configuration, store); err != nil {
						return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.network", Cause: "required public CertIssuer route or authenticated mTLS path is unavailable", Next: "repair the selected environment's owned DNS/ingress/Service and required mTLS path; verify with check-deployment-health.sh, then repeat preflight"}
					}
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
				},
				"deployment.release": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					if err := c.Fence(ctx, p); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := verifyEnvironmentRelease(ctx, c.Configuration, c.Release); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest(c.Release)}, nil
				},
			}
		},
		Accept: func(ctx context.Context, c *compiledEnvironmentDeployment) error {
			return acceptEnvironmentRollout(ctx, c, defaultDeploymentCheckDependencies(), environmentProgressResultOutput(ctx, os.Stdout))
		},
		Execute: func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, qualify func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
			if accept == nil {
				return &deploymentplan.Blocker{ID: "deployment.post-deploy", Cause: "complete acceptance is unavailable", Next: "restore the required target, PKI and endpoint rollout health checks before execution"}
			}
			admission, err := admittedEnvironmentExecution(ctx, c)
			if err != nil {
				return err
			}
			if !admission.claimed.CompareAndSwap(false, true) {
				return preflightExecutionProofBlocker("the admitted execution attempt was already started")
			}
			if err := c.Plan.Execute(ctx, c.Backend, sha, c.Store.Root, qualify, c.Fence, func(ctx context.Context, p *deploymentplan.Plan) error {
				if err := c.Backend.CheckTargetWorkloads(ctx, p); err != nil {
					return err
				}
				return accept(ctx, c)
			}, c.FenceInventory); err != nil {
				return err
			}
			return nil
		},
	}
}

func environmentSimulatorCompatibilityBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.simulator", Resource: resource, Cause: cause, Next: "maintain the simulator Deployment and BILLING_DB_MIGRATE_ON_STARTUP=false; verify the exact candidate image startup with the restricted runtime database role, then repeat full preflight"}
}

// A Service shares this name but has no Pod template or executable to qualify.
func environmentPaymentSimulatorTarget(resource deploymentplan.Resource) (*deploymentUpgradeTarget, error) {
	if resource.Action == "retire" || resource.Desired["kind"] != "Deployment" || deploymentplan.Map(resource.Desired["metadata"])["name"] != "payment-simulator" {
		return nil, nil
	}
	var target deploymentUpgradeTarget
	raw, err := json.Marshal(resource.Desired)
	if err != nil || json.Unmarshal(raw, &target) != nil {
		return nil, environmentSimulatorCompatibilityBlocker(resource.ID, "candidate simulator Deployment cannot be read")
	}
	if _, err := deploymentUpgradeSimulatorContainer(target); err != nil {
		return nil, environmentSimulatorCompatibilityBlocker(resource.ID, "candidate simulator executable or Pod specification is invalid")
	}
	return &target, nil
}

// Rollout acceptance reuses the maintained health checker. Target image/owner
// verification is already performed by Execute; these are its required runtime
// Secret, PKI, workload and public-mTLS checks, without provider write canaries.
func acceptEnvironmentRollout(ctx context.Context, c *compiledEnvironmentDeployment, checks deploymentCheckDependencies, out io.Writer) error {
	if c == nil || c.Plan == nil || c.Configuration.Environment != c.Plan.Environment {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy", Cause: "selected rollout environment is unavailable", Next: "restore the exact checked plan and environment before retrying deployment"}
	}
	args := []string{"--environment", c.Plan.Environment, "--workspace", c.Configuration.Workspace, "--phase", deploymentCheckPostDeploy, "--read-only"}
	if c.Configuration.EnvironmentRoot != "" {
		args = append(args, "--environment-root", c.Configuration.EnvironmentRoot)
	}
	if err := runDeploymentCheckWithDependencies(ctx, args, out, out, checks); err != nil {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy", Cause: "required rollout health checks failed or were incomplete; deployment is not complete", Next: "resolve the failed Secret, PKI, workload or public mTLS check above; repeat check-deployment-health.sh for this environment and review the execution journal before retrying"}
	}
	return nil
}

// Missing runtime adapters remain required BLOCKED gates. Neither a private
// schema-1 file nor the former partial provider checks can turn them into GO.
func runUnifiedEnvironmentDeployment(action string, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runUnifiedEnvironmentDeploymentWithDependencies(ctx, action, args, os.Stdout, os.Stderr, defaultUnifiedDeploymentDependencies())
}

func parseUnifiedDeploymentOptions(action string, args []string, out io.Writer) (unifiedDeploymentOptions, error) {
	o := unifiedDeploymentOptions{Action: action, Operation: "upgrade", Timeout: defaultCompleteDeploymentTimeout}
	if action == "create" {
		o.Operation = "create"
	}
	fs := flag.NewFlagSet("deployment "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.Environment, "environment", "", "selected maintained environment")
	fs.StringVar(&o.EnvironmentRoot, "environment-root", "", "explicit maintained environment root")
	fs.StringVar(&o.Workspace, "workspace", "", "workspace root")
	fs.StringVar(&o.Operation, "operation", o.Operation, "create (automatically qualifies fresh or original-checkpoint prerequisites), or upgrade")
	fs.StringVar(&o.ParentPlanSHA256, "parent-plan-sha256", "", "recovery only: bind the original canonical create owner")
	fs.StringVar(&o.EmailKeyRepairProof, "email-key-repair-proof", "", "recovery only: original canonical verified initial-email repair proof")
	fs.StringVar(&o.PostCleanupHelperImage, "post-cleanup-helper-image", "", "recovery only: immutable qualified same-key public serving profile helper")
	fs.StringVar(&o.PostCleanupControllerImage, "post-cleanup-controller-image", "", "recovery only: immutable qualified fixed-Root CRL refresh controller")
	fs.StringVar(&o.Confirm, "confirm", "", "exact environment stack confirmation for execution")
	fs.StringVar(&o.PlanSHA256, "plan-sha256", "", "exact reviewed plan SHA-256; required for execution")
	fs.StringVar(&o.Report, "report", "", "sanitized JSON report (0600)")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "complete operation timeout; execution is independent of preflight evidence expiry after admission")
	fs.DurationVar(&o.TotalTimeout, "total-timeout", 0, "explicit admitted-program total window anchored to its original admission")
	var readOnly bool
	fs.BoolVar(&readOnly, "read-only", false, "other preflight probes are read-only; a missing configured media bucket may be created")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	readOnlyVisited := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "read-only" {
			readOnlyVisited = true
		}
	})
	if readOnlyVisited && (action != "preflight" || !readOnly) {
		return o, errors.New("--read-only applies only to preflight and cannot be false")
	}
	if fs.NArg() != 0 || o.Environment == "" || !secretEnvironmentPattern.MatchString(o.Environment) || o.Timeout <= 0 {
		return o, errors.New("a valid --environment, positive --timeout and named flags are required")
	}
	if o.TotalTimeout < 0 || o.TotalTimeout > 0 && action != "create" {
		return o, errors.New("--total-timeout requires an already admitted create program and a positive duration")
	}
	if o.Operation == "full-deployment" {
		o.Operation = "upgrade"
	}
	if o.Operation != "create" && o.Operation != "upgrade" {
		return o, errors.New("--operation must be create or upgrade")
	}
	if o.ParentPlanSHA256 != "" || o.EmailKeyRepairProof != "" {
		if action != "preflight" || o.Operation != "create" || o.ParentPlanSHA256 != "" && !environmentPreflightDigest.MatchString(o.ParentPlanSHA256) {
			return o, errors.New("checkpoint overrides require create preflight and a valid original parent")
		}
	}
	if action == "create" && o.Operation != "create" || action == "upgrade" && o.Operation != "upgrade" {
		return o, errors.New("operation conflicts with the selected deployment action")
	}
	if (action == "create" || action == "upgrade") && (o.PlanSHA256 == "" || len(o.PlanSHA256) != 64 || strings.Trim(o.PlanSHA256, "0123456789abcdef") != "") {
		return o, errors.New("execution requires the exact reviewed --plan-sha256 from full preflight")
	}
	return o, nil
}

type unifiedDeploymentReport struct {
	Schema                string                      `json:"schema"`
	Scope                 string                      `json:"scope"`
	Environment           string                      `json:"environment"`
	Operation             string                      `json:"operation"`
	Decision              string                      `json:"decision"`
	Plan                  *deploymentplan.Summary     `json:"plan,omitempty"`
	Release               *environmentRelease         `json:"release,omitempty"`
	Gates                 []deploymentplan.GateResult `json:"gates,omitempty"`
	Completed             bool                        `json:"completed"`
	ValidityWindowSeconds int64                       `json:"validity_window_seconds,omitempty"`
	QualificationSHA256   string                      `json:"qualification_sha256,omitempty"`
	QualifiedAt           *time.Time                  `json:"qualified_at,omitempty"`
	EvidenceValidUntil    *time.Time                  `json:"evidence_valid_until,omitempty"`
	ReusedPreflight       bool                        `json:"reused_preflight,omitempty"`
	ExecutionAdmittedAt   *time.Time                  `json:"execution_admitted_at,omitempty"`
	ExecutionDeadline     *time.Time                  `json:"execution_deadline,omitempty"`
	ResolvedTargetSHA256  string                      `json:"resolved_target_sha256,omitempty"`
}

func runUnifiedEnvironmentDeploymentWithDependencies(parent context.Context, action string, args []string, out, diagnostics io.Writer, deps unifiedDeploymentDependencies) (resultErr error) {
	o, err := parseUnifiedDeploymentOptions(action, args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "error:", err)
		return exitCode(2)
	}
	cfg, err := deps.Resolve(o.Workspace, o.Environment, o.EnvironmentRoot)
	if err != nil {
		fmt.Fprintln(diagnostics, "error: maintained deployment configuration is unavailable")
		return exitCode(2)
	}
	if o.Report != "" {
		if err := validateEnvironmentDeploymentReportPath(cfg.Environment, o.Report); err != nil {
			fmt.Fprintln(diagnostics, "error: --report must select a private JSON file under ~/.config/rtk_cloud/<environment>/deployment/reports")
			return exitCode(2)
		}
	}
	if action == "create" || action == "upgrade" {
		if o.Confirm != cfg.Values["CLOUD_STACK_NAME"] {
			fmt.Fprintf(diagnostics, "error: --confirm %s is required\n", cfg.Values["CLOUD_STACK_NAME"])
			return exitCode(2)
		}
	}
	if deps.Guard != nil {
		if err := deps.Guard(append([]string{"deployment", action}, args...)); err != nil {
			fmt.Fprintln(diagnostics, "error: environment recovery/maintenance fence blocks this operation")
			return exitCode(1)
		}
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	ctx, closeMeasurements, err := openDeploymentMeasurements(ctx, cfg.Environment, action)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeMeasurements(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	progressLog, err := openEnvironmentProgressLog(cfg.Environment, action)
	if err != nil {
		fmt.Fprintln(diagnostics, "error: cannot create the private environment deployment log")
		return exitCode(1)
	}
	defer func() {
		if err := progressLog.Close(); err != nil {
			fmt.Fprintln(diagnostics, "error: deployment log could not be saved completely")
			if resultErr == nil {
				resultErr = exitCode(1)
			}
		}
	}()
	ctx = context.WithValue(ctx, environmentProgressLogKey{}, io.Writer(progressLog))
	fmt.Fprintf(progressLog, "started_at=%s environment=%s action=%s\n", time.Now().UTC().Format(time.RFC3339), cfg.Environment, action)
	report := unifiedDeploymentReport{Schema: "rtk-cloud.deployment-report/v2", Environment: cfg.Environment, Operation: o.Operation, Decision: "NO-GO"}
	defer func() {
		if raw, err := json.Marshal(report); err == nil {
			fmt.Fprintf(progressLog, "report=%s\n", raw)
		}
	}()
	if o.Operation == "create" && deps.ContinueCreate != nil {
		if handled, err := deps.ContinueCreate(ctx, cfg, o, &report, out, diagnostics); handled {
			return err
		}
	}
	if o.TotalTimeout > 0 {
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, errors.New("--total-timeout requires the original admitted complete program"))
	}
	if action == "preflight" && o.Operation == "create" {
		checkpoint, inspectErr := createPreflightHasCheckpoint(cfg)
		if inspectErr != nil {
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, errors.New("selected create checkpoint occupancy cannot be inspected"))
		}
		if checkpoint {
			if deps.RecoveryPreflight == nil {
				return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, errors.New("original create checkpoint qualification adapter unavailable"))
			}
			return deps.RecoveryPreflight(ctx, cfg, o, &report, out, diagnostics)
		}
		if o.ParentPlanSHA256 != "" || o.EmailKeyRepairProof != "" {
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, report, errors.New("checkpoint overrides have no original execution fence; fresh create cannot consume recovery evidence"))
		}
	}
	if o.Operation == "create" && deps.FreshCreate != nil {
		return deps.FreshCreate(ctx, cfg, o, &report, out, diagnostics)
	}
	return runUnifiedCompiledEnvironmentDeployment(ctx, cfg, o, &report, out, diagnostics, deps)
}

// Fresh qualification and consumption share this path, including an original
// create execution that has already completed its bootstrap lifecycle.
func runUnifiedCompiledEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer, deps unifiedDeploymentDependencies) error {
	action := o.Action
	var err error
	if report.Scope == "" {
		report.Scope = "existing-environment-update"
		if o.Operation != "upgrade" {
			report.Scope = "environment-bootstrap"
		}
	}
	preflightGeneration := ""
	if action == "preflight" {
		var invalidateErr error
		if deps.InvalidateProof != nil {
			preflightGeneration, invalidateErr = deps.InvalidateProof(cfg)
		}
		if deps.InvalidateProof == nil || invalidateErr != nil || preflightGeneration == "" {
			report.Gates = []deploymentplan.GateResult{preflightExecutionProofRow("previous qualified execution evidence could not be invalidated")}
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
		}
	}
	if action == "preflight" && deps.PublishBindings != nil {
		// Invalidate older GO before any new checks, including compiler failures.
		if deps.PublishObservations == nil || deps.PublishObservations(cfg, nil, preflightGeneration, deps.PublishBindings) != nil {
			fmt.Fprintln(diagnostics, "error: cannot initialize private preflight bindings output")
			return exitCode(1)
		}
	}
	if action == "preflight" && deps.CheckExecutionAvailable != nil || o.Operation == "create" && (action == "preflight" || action == "plan" || action == "create") {
		stage := "deployment.execution.lease"
		if o.Operation == "create" {
			stage = "deployment.create.target"
		}
		err := runEnvironmentProgress(ctx, out, stage, func(progressCtx context.Context) error {
			if deps.CheckExecutionAvailable == nil {
				return &deploymentplan.Blocker{ID: "deployment.create.target", Cause: "operation-aware fresh target discovery is unavailable", Next: "restore the selected provider discovery adapter before create qualification; missing capability remains BLOCKED/NO-GO"}
			}
			return deps.CheckExecutionAvailable(progressCtx, cfg, o.Operation)
		})
		if err != nil {
			row := deploymentplan.GateResult{ID: "deployment.execution", Status: "FAIL", Required: true, Cause: "execution lock availability could not be verified", Next: "inspect the selected environment execution owner and restore lock inspection before repeating preflight", Impact: "preflight stopped before storage preparation, compilation and qualification", Recheck: "repeat full preflight after execution ownership is resolved"}
			var blocker *deploymentplan.Blocker
			if errors.As(err, &blocker) {
				row.Cause, row.Next = blocker.Cause, blocker.Next
				if blocker.Resource != "" {
					row.Cause += " (" + blocker.Resource + ")"
				}
			} else if errors.Is(err, context.DeadlineExceeded) {
				row.Cause = "execution lock inspection timed out"
			} else if errors.Is(err, context.Canceled) {
				row.Cause = "execution lock inspection was cancelled"
			}
			if o.Operation == "create" {
				row.Status, row.ID = "BLOCKED", "deployment.create.target"
				if blocker != nil {
					row.ID = blocker.ID
				}
				row.Impact = "fresh environment qualification stopped before storage preparation, release resolution and Kubernetes compilation"
				row.Recheck = "repeat explicit create qualification after the target and complete provider/bootstrap stages can be qualified"
			}
			report.Gates = []deploymentplan.GateResult{row}
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
		}
	}
	// An existing enabled VideoStorage role must retain its original allocation.
	// Create keeps the separate existing bucket-preparation/bootstrap contract.
	readOnlyMediaUpgrade := o.Operation == "upgrade" && cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true"
	if action == "preflight" && deps.PrepareStorage != nil && !readOnlyMediaUpgrade {
		if err := deps.PrepareStorage(ctx, cfg, out); err != nil {
			report.Gates = []deploymentplan.GateResult{{ID: "provider.storage.media.bucket", Status: "FAIL", Required: true, Cause: "configured media bucket could not be verified or created", Next: "check the environment Linode credentials, bucket creation permission, configured name/region and provider availability; repeat preflight"}}
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
		}
	}
	var c *compiledEnvironmentDeployment
	compileStage := "deployment.compile"
	if action == "create" || action == "upgrade" {
		compileStage = "deployment.preflight.evidence"
	}
	err = runEnvironmentProgress(ctx, out, compileStage, func(progressCtx context.Context) error {
		var compileErr error
		if action == "create" || action == "upgrade" {
			if deps.LoadQualified == nil {
				return preflightExecutionProofBlocker("qualified plan consumption is unavailable")
			}
			c, compileErr = deps.LoadQualified(progressCtx, cfg, o.Operation, o.PlanSHA256)
		} else {
			c, compileErr = deps.Compile(progressCtx, cfg, o.Operation)
		}
		return compileErr
	})
	if err != nil {
		// Never print compiler/provider output: it can contain private payloads.
		row := deploymentplan.GateResult{ID: "deployment.compilation.internal", Status: "BLOCKED", Required: true, Cause: "deployment compiler returned an unclassified internal error", Next: "report the selected environment and workspace revision so the failing compiler boundary can provide a safe stage/resource diagnosis and be repaired", Impact: "the complete environment update has no executable reviewed plan", Recheck: "repeat full preflight for the same release"}
		if action == "create" || action == "upgrade" {
			row = preflightExecutionProofRow("qualified execution evidence could not be loaded")
		}
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
			if blocker.Resource != "" {
				row.Cause += " (" + blocker.Resource + ")"
			}
		}
		report.Gates = []deploymentplan.GateResult{row}
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if action == "preflight" {
		c.PreflightGeneration = preflightGeneration
	}
	if action == "preflight" && deps.PublishBindings != nil {
		if deps.PublishObservations == nil || deps.PublishObservations(cfg, c, preflightGeneration, deps.PublishBindings) != nil {
			fmt.Fprintln(diagnostics, "error: cannot publish observed preflight bindings")
			return exitCode(1)
		}
	}
	if (action == "create" || action == "upgrade") && deps.RequireBindings != nil {
		if err := deps.RequireBindings(c); err != nil {
			report.Gates = []deploymentplan.GateResult{{ID: "deployment.preflight.bindings", Status: "BLOCKED", Required: true, Cause: "successful preflight evidence does not match current observed bindings and target plan", Next: "repeat full preflight and use its reviewed plan SHA-256", Impact: "deployment cannot consume stale or incomplete preflight evidence", Recheck: "repeat full preflight for this environment"}}
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
		}
	}
	if window, err := environmentPKIValidityWindow(c.Configuration.Values); err == nil {
		report.ValidityWindowSeconds = window
	}
	summary := c.Plan.Summary()
	report.Plan = &summary
	if action == "plan" {
		report.Decision = "PLANNED"
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	if action == "create" || action == "upgrade" {
		return executeQualifiedEnvironmentDeployment(ctx, c, o, out, diagnostics, deps, report)
	}
	checks := deps.Checks(c)
	if deps.Accept == nil {
		// Completion verification is an execution prerequisite, even if every
		// other adapter is installed by a future capability implementation.
		check := checks["deployment.execution"]
		checks["deployment.execution"] = func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if check != nil {
				if _, err := check(ctx, p); err != nil {
					return deploymentplan.GateEvidence{}, err
				}
			}
			return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.execution", Cause: "complete post-deploy acceptance is unavailable", Next: "restore the required workload, PKI and endpoint rollout health checks before execution"}
		}
	}
	options := deploymentplan.QualificationOptions{Production: cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] == "production", ExistingUpdate: o.Operation == "upgrade"}
	measuredGates := map[string]func(error){}
	for id, check := range checks {
		if check == nil {
			continue
		}
		checks[id] = func(checkCtx context.Context, plan *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			measuredGates[id] = beginDeploymentMeasurement(checkCtx, id)
			environmentDeploymentProgress(checkCtx, id)
			return checkedPreflightGate(checkCtx, id, plan, check)
		}
	}
	var qualification deploymentplan.Qualification
	err = runEnvironmentProgress(environmentDeploymentProgressRound(ctx, "preflight"), out, "deployment.qualification", func(progressCtx context.Context) error {
		var qualifyErr error
		qualification, qualifyErr = c.Plan.Qualify(progressCtx, options, checks, func(row deploymentplan.GateResult) {
			if finish := measuredGates[row.ID]; finish != nil {
				var measuredErr error
				if row.Status != "PASS" {
					measuredErr = errors.New("gate did not pass")
				}
				finish(measuredErr)
			}
			environmentDeploymentProgressResult(progressCtx, row.ID, row.Status)
		})
		return qualifyErr
	})
	report.Decision, report.Gates = qualification.Decision, qualification.Gates
	if err != nil {
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if deps.PublishProof == nil || ctx.Err() != nil {
		report.Decision = "NO-GO"
		report.Gates = append(report.Gates, preflightExecutionProofRow("complete qualified execution evidence could not be published"))
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if err := deps.PublishProof(ctx, c, qualification); err != nil || c.Proof == nil || ctx.Err() != nil {
		_ = revokeEnvironmentPreflightProof(cfg, preflightGeneration)
		report.Decision = "NO-GO"
		report.Gates = append(report.Gates, preflightExecutionProofRow("complete qualified execution evidence could not be saved privately"))
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	setUnifiedPreflightEvidenceReport(report, c.Proof, false)
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

func emitUnifiedDeploymentReport(out, diagnostics io.Writer, path string, report unifiedDeploymentReport, result error) error {
	if report.Plan != nil {
		fmt.Fprintf(out, "plan-sha256: %s\n", report.Plan.PlanSHA256)
	}
	for _, row := range report.Gates {
		if !deploymentStatusSuccessful(row.Status) {
			fmt.Fprintf(out, "%s %s\n", deploymentStatusLabel(out, row.Status), row.ID)
		}
		if len(row.PendingRolloutHealth) > 0 {
			fmt.Fprintf(out, "  %s rollout health pending: %s\n", row.ID, strings.Join(row.PendingRolloutHealth, ", "))
		}
		if row.Status != "PASS" {
			fmt.Fprintf(out, "  cause: %s\n  next: %s\n  impact: %s\n  recheck: %s\n", row.Cause, row.Next, row.Impact, row.Recheck)
		}
	}
	fmt.Fprintf(out, "%s environment=%s operation=%s scope=%s completed=%t\n", report.Decision, report.Environment, report.Operation, report.Scope, report.Completed)
	if path != "" {
		if err := writeUnifiedDeploymentReport(path, report); err != nil {
			fmt.Fprintln(diagnostics, "error: cannot publish sanitized deployment report")
			return exitCode(1)
		}
	}
	return result
}

func writeUnifiedDeploymentReport(path string, report unifiedDeploymentReport) error {
	if err := validateEnvironmentDeploymentReportPath(report.Environment, path); err != nil {
		return err
	}
	store, err := newSecretStore("", report.Environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	// Reuse the existing atomic, private report publisher rather than exposing
	// desired Secrets or baseline state through an additional file writer.
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return writePrivateDeploymentReportJSON(path, raw)
}

func validateEnvironmentDeploymentReportPath(environment, path string) error {
	return validateEnvironmentDeploymentJSONPath(environment, path, "reports")
}

func validateEnvironmentDeploymentJSONPath(environment, path, category string) error {
	if category != "reports" && category != "releases" {
		return errors.New("unknown environment deployment output category")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".json" || filepath.Dir(path) != filepath.Join(store.Root, "deployment", category) {
		return errors.New("deployment JSON output destination is outside its canonical environment directory")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Dir(path)} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return errors.New("deployment output directories must be real private 0700 directories")
		}
	}
	return nil
}
