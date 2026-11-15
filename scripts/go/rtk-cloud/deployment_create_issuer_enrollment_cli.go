package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type issuerEnrollmentContinuationProof struct {
	Parent, ParentJournal, ParentOwner, Checker, Selection, Release, FirstStage string
	ServerStage                                                                 string                           `json:"ServerStage,omitempty"`
	ManagedStage                                                                string                           `json:"ManagedStage,omitempty"`
	PartialSession                                                              *partialSessionContinuationProof `json:"PartialSession,omitempty"`
	PartialTrustForward                                                         *partialSessionTrustForwardProof `json:"PartialTrustForward,omitempty"`
	QualifiedAt, ValidUntil                                                     time.Time
}

func boundIssuerContinuation(parent context.Context, started time.Time, timeout time.Duration, now time.Time) (context.Context, context.CancelFunc, error) {
	deadline := started.Add(timeout)
	if started.IsZero() || timeout <= 0 || !now.Before(deadline) {
		return nil, nil, createProviderRecoveryBlock("original continuation execution timeout is exhausted")
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, nil
}

type issuerResumeAdmission struct {
	Review    string    `json:"review_sha256"`
	Original  string    `json:"original_admission"`
	ResumedAt time.Time `json:"resumed_at"`
	Deadline  time.Time `json:"deadline"`
}

func recordIssuerResumeAdmission(store secretStore, relative, review, original string, now, deadline time.Time) error {
	if !environmentPreflightDigest.MatchString(review) || original == "" || now.IsZero() || !now.Before(deadline) {
		return createProviderRecoveryBlock("valid original continuation admission is required")
	}
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return store.write(relative, mustProviderRecoveryJSON(issuerResumeAdmission{Review: review, Original: original, ResumedAt: now, Deadline: deadline}), false)
	} else if err != nil {
		return err
	}
	var existing issuerResumeAdmission
	if readEnvironmentPreflightJSONWithCanonical(path, &existing, false) != nil || existing.Review != review || existing.Original != original || existing.ResumedAt.IsZero() || existing.ResumedAt.After(now) || !existing.ResumedAt.Before(existing.Deadline) {
		return createProviderRecoveryBlock("original continuation admission is unavailable or changed")
	}
	if existing.Deadline.Equal(deadline) {
		return nil
	}
	// Preserve the prior admission if the authorized total timeout changes.
	supplement := filepath.Join(filepath.Dir(relative), "resume-admission-"+deploymentplan.Digest(deadline)[:16]+".json")
	if supplement == relative {
		return createProviderRecoveryBlock("continuation timeout supplement differs from its recorded deadline")
	}
	return recordIssuerResumeAdmission(store, supplement, review, original, now, deadline)
}

func tryUnifiedIssuerEnrollment(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) (bool, error) {
	if cfg.Environment != "dev" || o.Operation != "create" || (o.Action != "preflight" && o.Action != "create") {
		return false, nil
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return false, nil
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil || checkpoint.Profile != acceptedIssuerEnrollmentProfile && checkpoint.Profile != acceptedIssuerCoreCRLsProfile && checkpoint.Profile != issuerMQTTCustodyCAProfile && checkpoint.Profile != issuerMQTTLogTrustProfile && checkpoint.Profile != issuerDeviceCustodyProfile && checkpoint.Profile != "initial-transport-successor-continuation" {
		return false, nil
	}
	report.Scope = "original-generation-recovery"
	fail := func(err error) (bool, error) {
		return true, emitEnvironmentCreateFailure(out, diagnostics, o, report, err)
	}
	if o.ParentPlanSHA256 != "" && o.ParentPlanSHA256 != checkpoint.Owner.PlanSHA256 || o.EmailKeyRepairProof != "" || o.TotalTimeout != 0 {
		return fail(createProviderRecoveryBlock("continuation requires the original CertIssuer enrollment parent and ordinary execution timeout"))
	}
	e, source, err := loadActiveTransportExecution(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return fail(err)
	}
	defer e.Close()
	if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(checkpoint.Journal) {
		return fail(createProviderRecoveryBlock("the original accepted enrollment checkpoint changed"))
	}
	resumeSuccessor := checkpoint.Profile == "initial-transport-successor-continuation"
	resumeDevice := checkpoint.Profile == issuerDeviceCustodyProfile || resumeSuccessor
	resumeLog := checkpoint.Profile == issuerMQTTLogTrustProfile || resumeDevice
	resumeMQTT := checkpoint.Profile == issuerMQTTCustodyCAProfile || resumeLog
	resumeCore := checkpoint.Profile == acceptedIssuerCoreCRLsProfile || resumeMQTT
	var priorEntry environmentCreateJournalEntry
	var priorProof issuerEnrollmentContinuationProof
	var interruptedIssuerStage *deploymentplan.Plan
	firstStageName := "service-managed-enrollment-issuer"
	if resumeCore {
		var ok bool
		if resumeSuccessor {
			priorEntry, ok, err = initialSuccessorContinuationEntry(store, e)
			if err != nil {
				return fail(err)
			}
			firstStageName = "initial-transport-successor-continuation"
		} else if resumeDevice {
			priorEntry, ok = deviceCustodyContinuationEntry(e.Journal)
			firstStageName = "device-custody-forward"
		} else if resumeLog {
			priorEntry, ok = issuerMQTTLogTrustCheckpoint(e.Journal)
			firstStageName = mqttLogTrustStage
		} else if resumeMQTT {
			priorEntry, ok = issuerMQTTCustodyCheckpoint(e.Journal)
			firstStageName = mqttCustodyCAStage
		} else {
			priorEntry, ok = issuerCoreContinuationEntry(e.Journal)
			firstStageName = "service-managed-core"
		}
		if !ok {
			return fail(createProviderRecoveryBlock("original accepted Service core checkpoint changed"))
		}
		priorProof, err = verifyIssuerCoreContinuationAdmission(store, e, priorEntry)
		if err != nil {
			return fail(err)
		}
		if o.Action == "create" && !resumeSuccessor {
			bounded, cancel, err := boundIssuerContinuation(ctx, priorEntry.StartedAt, o.Timeout, time.Now())
			if err != nil {
				return fail(err)
			}
			defer cancel()
			ctx = bounded
		}
	}
	if resumeSuccessor {
		interruptedIssuerStage, err = interruptedCustodyControllerStage(store, e)
		if err != nil {
			return fail(err)
		}
	}
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return fail(createProviderRecoveryBlock("original deployment operator credentials changed"))
	}
	originalOperator, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &source.Plan.Plan)
	if err != nil {
		return fail(err)
	}
	if deploymentplan.Digest(originalOperator) != source.Plan.Plan.OperatorSHA256 {
		return fail(createProviderRecoveryBlock("original deployment operator credentials changed"))
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != source.Plan.KubeconfigSHA256 {
		return fail(createProviderRecoveryBlock("original deployment Kubernetes identity changed"))
	}
	b, err := newEnvironmentKubernetesBackendForConfig(string(raw), cfg)
	if err != nil {
		return fail(err)
	}
	b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return fail(err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return fail(err)
	}
	var admitted issuerEnrollmentContinuationProof
	var admittedReview string
	var hasAdmission, partialRenewal, partialMinted bool
	var forwardAdmission issuerEnrollmentContinuationProof
	var forwardReview string
	var hasForwardAdmission bool
	var completedSession *secondSessionAdmission
	var completedSessionReview string
	var partialTrustForward *createManagedLateSeedForward
	var trustForwardNeeded bool
	if resumeSuccessor {
		admitted, admittedReview, hasAdmission, err = loadPartialSessionAdmission(store, e)
		if err != nil {
			return fail(err)
		}
		if hasAdmission {
			forwardAdmission, forwardReview, hasForwardAdmission, err = loadPartialSessionTrustForwardAdmission(store, e, admitted, admittedReview)
			if err != nil {
				return fail(err)
			}
		}
	}
	if resumeSuccessor && hasAdmission {
		if handled, hostErr := tryUnifiedRuntimeHostCompatibility(ctx, cfg, o, report, store, e, b, &source.Plan.Plan, out, diagnostics); handled {
			if hostErr != nil {
				return fail(hostErr)
			}
			return true, nil
		}
		handled, sessionErr := tryUnifiedServiceSessionContinuation(ctx, cfg, o, report, store, e, b, source.Plan.Plan, operator, priorEntry, admitted, out, diagnostics)
		if handled {
			if sessionErr != nil {
				return fail(sessionErr)
			}
			return true, nil
		}
		head, review, err := sessionLineageHead(store, e)
		if err != nil {
			return fail(err)
		}
		if head != nil && head.Batches != nil {
			done, err := completedServiceSessionBatch(store, e, *head, review)
			if err != nil {
				return fail(err)
			}
			if done {
				completedSession, completedSessionReview = head, review
			}
		}
	}
	if resumeSuccessor && o.Action == "create" {
		if completedSession != nil {
			deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
			if err != nil || deadline.Before(completedSession.ExecutionDeadline) || !time.Now().Before(deadline) {
				return fail(createProviderRecoveryBlock("original Service session execution deadline is exhausted or unavailable"))
			}
			bounded, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			ctx = bounded
		} else {
			bounded, cancel, err := boundIssuerContinuation(ctx, priorEntry.StartedAt, o.Timeout, time.Now())
			if err != nil {
				return fail(err)
			}
			defer cancel()
			ctx = bounded
		}
	}
	if shouldTryExpiredSessionPrivateRepair(resumeSuccessor, hasAdmission, completedSession) {
		parent := source.Plan.Plan
		handled, scopedErr := tryExpiredSessionPrivateRepair(ctx, cfg, store, e, b, &parent, priorEntry, admitted, admittedReview, hasAdmission, forwardAdmission, forwardReview, hasForwardAdmission, o, report, out, diagnostics)
		if handled {
			if scopedErr != nil {
				return fail(scopedErr)
			}
			return true, nil
		}
	}
	var forward environmentCreatePlan
	var runtime environmentCreateBootstrapRuntime
	var firstStage *deploymentplan.Plan
	var interruptedServerStage, serverForward *deploymentplan.Plan
	var interruptedManagedStage, managedForward *deploymentplan.Plan
	var managedWrites []activeTransportRenewalResource
	var mqttSourcePlan, mqttCAPlan, priorRelease, logRepairImage string
	var bootstrapPublications map[string]string
	proof := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal),
		ParentOwner: deploymentplan.Digest(e.owner), Checker: checker}
	workloadImageReview := ""
	steps := []struct {
		id  string
		run func(context.Context) error
	}{
		{"deployment.recovery.target", func(ctx context.Context) error {
			for _, r := range source.Plan.Plan.Provider.Resources {
				_, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, r, source.Plan.Receipts[r.Key], operator)
				if err != nil || !ready {
					return createProviderRecoveryBlock("recorded original provider resource is unavailable: " + r.Key)
				}
			}
			allowance := issuerEnrollmentSessionAllowance{Expired: resumeSuccessor, NeedsRenewal: &partialRenewal, Minted: &partialMinted, Completed: completedSession}
			if hasAdmission {
				allowance.Successor = admitted.PartialSession
			}
			if err := verifyAcceptedIssuerEnrollmentContinuation(ctx, cfg, store, e, b, resumeMQTT, allowance); err != nil {
				return err
			}
			if hasAdmission && !partialRenewal {
				return createProviderRecoveryBlock("admitted partial renewal no longer matches its original session")
			}
			if resumeMQTT {
				for _, stage := range issuerMQTTCheckpointStages {
					if _, _, err := acceptedCreateStage(store, e, stage); err != nil {
						return err
					}
				}
			} else if resumeCore {
				for _, stage := range issuerCoreCheckpointStages {
					plan, _, err := acceptedCreateStage(store, e, stage)
					if err != nil {
						return err
					}
					if err := plan.AdmitResumable(ctx, b, store.Root); err != nil {
						return err
					}
				}
			}
			return nil
		}},
		{"deployment.recovery.release", func(ctx context.Context) error {
			historical, err := createHistoricalReleaseSelection(ctx, cfg, store, e, source.Plan.Plan)
			if err != nil {
				return err
			}
			if o.Action == "preflight" {
				if err := verifyEnvironmentReleaseWithCommand(ctx, cfg, source.Plan.Plan.Release, environmentCIImageCommand(cfg), historical); err != nil {
					return err
				}
				if err := qualifyEnvironmentCreateFrozenDataPlaneArtifact(ctx, cfg, &source.Plan.Plan, store); err != nil {
					return err
				}
			} else if err := verifyEnvironmentImageSources(ctx, cfg, source.Plan.Plan.Release, environmentCIImageCommand(cfg), historical); err != nil {
				return err
			}
			images, selection, err := loadCreateVideoRepairImages(ctx, cfg, store, e.Journal.PlanSHA256, issuerLeaseMetadataRepair, issuerLeaseMetadataRepairSchema,
				"video-cloud-api", "video-cloud-emqx-pki", "video-cloud-openbao-pki")
			if err != nil {
				return err
			}
			var replacements map[string]string
			forward, replacements, err = issuerEnrollmentForwardPlan(source.Plan.Plan, images)
			if err != nil {
				return err
			}
			proof.Selection, proof.Release = selection, deploymentplan.Digest(forward.Release)
			if resumeCore && (priorProof.Selection != proof.Selection || priorProof.Release != proof.Release) {
				return createProviderRecoveryBlock("formal repair image selection differs from the original admitted continuation")
			}
			priorRelease = proof.Release
			if resumeLog {
				logImages, logSelection, err := loadCreateVideoRepairImages(ctx, cfg, store, e.Journal.PlanSHA256, mqttLogTrustRepair, mqttLogTrustRepairSchema, "video-cloud-api")
				if err != nil {
					return err
				}
				next, logReplacements, err := issuerEnrollmentForwardPlan(forward, logImages)
				if err != nil {
					return err
				}
				for original, current := range replacements {
					if updated := logReplacements[current]; updated != "" {
						replacements[original] = updated
					}
				}
				forward, logRepairImage = next, logImages["video-cloud-api"].Reference
				proof.Selection = deploymentplan.Digest([]string{proof.Selection, logSelection})
				proof.Release = deploymentplan.Digest(forward.Release)
			}
			runtime, err = prepareIssuerEnrollmentRuntime(ctx, cfg, store, e, b, operator, source.Plan.Plan, replacements, resumeMQTT, resumeDevice, partialRenewal)
			runtime.AcceptedServiceCoreCRLs = resumeCore
			if err == nil && resumeMQTT {
				material := createFoundationRecoveryPlan{Plan: forward, Materials: map[string]string{}}
				if resumeDevice {
					material.Materials["device-custody-continuation"] = e.Journal.PlanSHA256
				}
				root, operation, rootErr := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, &material, "mqtt", resumeDevice, resumeDevice)
				if rootErr != nil {
					return rootErr
				}
				runtime.AcceptedMQTTRoot, runtime.AcceptedMQTTRootOperation = &root, &operation
				if resumeDevice {
					mqttIntermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, root, findEnvironmentCreateDomainPolicy(forward.Bootstrap, "mqtt"))
					if err != nil {
						return err
					}
					bao, baoOperation, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, &material, "openbao_tls", true)
					if err != nil {
						return err
					}
					baoIntermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, bao, findEnvironmentCreateDomainPolicy(forward.Bootstrap, "openbao_tls"))
					if err != nil {
						return err
					}
					var device environmentCreateRegisteredIssuer
					if !resumeSuccessor {
						device, err = readDeviceCustodyReadyRoot(cfg, store, e)
					}
					if err != nil {
						return err
					}
					runtime.ServiceSessionRecovery = &createServiceSessionRecovery{PostServiceResume: true, PostTransportResume: true, MQTTRoot: &root, MQTTIntermediate: &mqttIntermediate, OpenBaoRoot: &bao, OpenBaoOperation: &baoOperation, OpenBaoIntermediate: &baoIntermediate, DeviceTrustRecovery: &createDeviceTrustRecovery{Root: device}}
					if resumeSuccessor {
						r := runtime.ServiceSessionRecovery
						r.Ceremony = *runtime.AcceptedIssuerEnrollment
						r.AppTrustRecovery = &createAppTrustRecovery{}
						r.ManagedTransportRecovery = &createManagedTransportRecovery{SourceReview: priorEntry.IntentSHA256}
						if hasAdmission {
							if err := restorePartialSessionProof(admitted, admittedReview, e.Journal, r); err != nil {
								return err
							}
							if hasForwardAdmission {
								partialTrustForward = forwardAdmission.PartialTrustForward.Forward
								r.ManagedTransportRecovery.PartialTrustForward = partialTrustForward
								r.ManagedTransportRecovery.PartialTrustForwardReview = forwardReview
								r.RenewalCandidate = partialTrustForward.Candidate
							}
							if completedSession != nil {
								latest, err := restoreSecondSessionRuntime(ctx, store, e, *completedSession, completedSessionReview, r.Ceremony, bao, forward)
								if err != nil {
									return err
								}
								if err := adoptCompletedServiceSession(r, latest); err != nil {
									return err
								}
								if _, completed, err := observeCompletedManagedSessionRecovery(ctx, cfg, forward.Bootstrap, store, runtime); err != nil {
									return err
								} else if !completed {
									if err := observeSecondSessionSuccessor(ctx, cfg, store, e, b, forward, r.Ceremony, bao, completedSessionReview, time.Now().UTC()); err != nil {
										return err
									}
								}
							} else if partialMinted {
								if err := observePartialSessionSuccessor(ctx, cfg, store, e, b, admitted, admittedReview, r); err != nil {
									return err
								}
							} else {
								check := *r
								check.ManagedTransportRecovery = &createManagedTransportRecovery{}
								if err := preparePartialIssuerSessionRenewal(ctx, cfg, store, e, b, &check); err != nil || check.OriginalKeySHA != r.OriginalKeySHA || check.OriginalCSRSHA != r.OriginalCSRSHA {
									return createProviderRecoveryBlock("admitted original partial session custody changed")
								}
								seedRuntime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
								state, _, stateErr := readManagedSessionState(ctx, cfg, seedRuntime, r.Intent.SessionID)
								clients, clientErr := readManagedSessionInventory(ctx, cfg, seedRuntime, r.Ceremony, r.Intent.Subjects, state, true)
								saved := r.ManagedTransportRecovery.SessionRenewal.OriginalState
								if stateErr != nil || clientErr != nil || saved == nil || !partialSessionOriginalStatePreserved(*saved, state, time.Now().UTC()) || clients != r.ManagedTransportRecovery.SessionRenewal.ClientsSHA256 {
									return createProviderRecoveryBlock("admitted original partial session or workload identity inventory changed")
								}
							}
						} else if partialRenewal {
							if err := preparePartialIssuerSessionRenewal(ctx, cfg, store, e, b, r); err != nil {
								return err
							}
						}
						if err := prepareManagedTransportRecovery(ctx, cfg, store, e, b, operator, &material, r, runtime); err != nil {
							return err
						}
						bootstrapPublications = map[string]string{}
						for _, domain := range managedPKIDomains {
							key := "accepted-service-core-managed-published-" + domain
							if !environmentPreflightDigest.MatchString(material.Materials[key]) {
								return createProviderRecoveryBlock("original published issuer evidence is unavailable: " + domain)
							}
							bootstrapPublications["pki/"+domain+"/issuer.json"] = material.Materials[key]
						}
					}
				}
			}
			return err
		}},
		{"deployment.recovery.storage", func(ctx context.Context) error {
			if err := validateEnvironmentCreateStorageAllocation(cfg); err != nil {
				return err
			}
			return qualifyProviderRecoveryStorage(ctx, cfg, operator)
		}},
		{"deployment.recovery.bootstrap", func(ctx context.Context) error {
			if o.Action == "preflight" {
				preservedRoots := []string{runtime.AcceptedIssuerEnrollment.Root.ID}
				if runtime.AcceptedMQTTRoot != nil {
					preservedRoots = append(preservedRoots, runtime.AcceptedMQTTRoot.ID)
				}
				if resumeDevice {
					preservedRoots = append(preservedRoots, runtime.ServiceSessionRecovery.OpenBaoRoot.ID)
				}
				if resumeSuccessor {
					preservedRoots = append(preservedRoots, runtime.ServiceSessionRecovery.ManagedTransportRecovery.AppRoot.ID)
				}
				routingSHA, err := readCompletedOriginalAppRouting(ctx, store, e, b)
				if err != nil {
					return err
				}
				if err := qualifyEnvironmentCreateBootstrapCapabilitiesWithRouting(ctx, cfg, forward.Bootstrap, store, operator, bootstrapPublications, routingSHA, preservedRoots...); err != nil {
					return err
				}
			}
			if resumeSuccessor {
				managedWrites, err = completedManagedAllocationWrites(store, e)
				if err != nil {
					return err
				}
				r := runtime.ServiceSessionRecovery
				d := r.ManagedTransportRecovery
				roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot, "app": d.AppRoot}
				intermediates := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Intermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate, "app": d.AppIntermediate}
				policies, err := environmentCreateCompleteSignerPolicies(roots, intermediates)
				if err != nil {
					return err
				}
				name := "certissuer-" + cfg.Environment
				intent := deploymentplan.Object{"role": name, "namespace": forward.Stack + "-video-cloud", "service_accounts": []string{"certissuer"}, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
				key := "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12]
				if !initialSuccessorAllowsPendingRole(store, e, key, intent) {
					return createProviderRecoveryBlock("original pending issuer role does not match the verified active signing mounts")
				}
				if _, err := reconcileProductSessionRenewalRole(ctx, cfg, runtime, key, intent); err != nil {
					return err
				}
				candidate := r.Candidate
				if candidate == nil {
					return createProviderRecoveryBlock("original managed transport continuation was not compiled")
				}
				if interruptedIssuerStage != nil {
					firstStageName = custodyControllerForwardStage(interruptedIssuerStage)
					previous, err := readEnvironmentCreateStageInput(store, e, firstStageName)
					if err != nil {
						return err
					}
					if previous != nil {
						candidate = previous
						entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+firstStageName)
						if entry != nil && entry.State == "completed" {
							if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, "kubernetes/"+firstStageName); err != nil {
								return err
							}
						} else if err := candidate.AdmitResumable(ctx, b, store.Root); err != nil {
							return err
						}
					} else if err := candidate.Admit(ctx, b); err != nil {
						return err
					}
				} else if err := candidate.Admit(ctx, b); err != nil {
					return err
				}
				firstStage, proof.FirstStage = candidate, candidate.SHA256()
				interruptedServerStage, serverForward, err = qualifyOpenBaoStartupForward(ctx, store, e, b)
				if err != nil {
					return err
				}
				if serverForward != nil {
					proof.ServerStage = serverForward.SHA256()
				}
				interruptedManagedStage, managedForward, err = qualifyManagedAllocationForward(ctx, store, e, b)
				if err != nil {
					return err
				}
				if managedForward != nil {
					proof.ManagedStage = managedForward.SHA256()
				}
				return nil
			}
			if resumeDevice {
				original, candidate, _, err := qualifyDeviceCustodyForward(ctx, cfg, store, e, b, operator)
				if err != nil {
					return err
				}
				if original.ReleaseSHA256 != proof.Release {
					return createProviderRecoveryBlock("original Device trust release differs from the formal repair selection")
				}
				runtime.ServiceSessionRecovery.DeviceTrustRecovery.Original = original
				firstStage, proof.FirstStage = candidate, candidate.SHA256()
				return nil
			}
			if resumeMQTT {
				var err error
				firstStage, mqttSourcePlan, err = qualifyMQTTCustodyStage(ctx, store, e, b, priorRelease, proof.Release, logRepairImage)
				if err != nil {
					return err
				}
				if resumeLog {
					original, err := readEnvironmentCreateStageInput(store, e, "mqtt-root-trust")
					if err != nil || original == nil || original.ReleaseSHA256 != priorRelease {
						return createProviderRecoveryBlock("original MQTT Root stage input is unavailable")
					}
					mqttCAPlan, mqttSourcePlan = mqttSourcePlan, original.SHA256()
				}
				proof.FirstStage = firstStage.SHA256()
				return nil
			}
			ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
			render := renderEnvironmentCreateManagedServiceIssuerDefinitions
			if resumeCore {
				render = renderEnvironmentCreateManagedServiceDefinitions
			}
			defs, err := render(ctx, cfg, forward.Bootstrap, runtime, *runtime.AcceptedIssuerEnrollment)
			if err != nil {
				return err
			}
			defs, err = projectEnvironmentCreateCustodyStage(ctx, cfg, store, &forward, e, defs, nil)
			if err != nil {
				return err
			}
			live, err := b.Inventory(ctx, forward.Stack)
			if err != nil {
				return err
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: forward.Stack, Operation: "scoped",
				ConfigurationSHA256: forward.ConfigurationSHA256, ReleaseSHA256: proof.Release, BindingsSHA256: environmentCreateStageBindingsSHA(firstStageName, defs)}, defs, live)
			if err != nil {
				return err
			}
			proof.FirstStage = plan.SHA256()
			firstStage = plan
			return plan.Admit(ctx, b)
		}},
		{"deployment.recovery.network", func(ctx context.Context) error {
			return qualifyOriginalEnvironmentCreateDNS(ctx, cfg, forward.DNS, source.Plan.Receipts, e, operator)
		}},
		{"deployment.recovery.inputs", func(ctx context.Context) error {
			if err := validateEnvironmentCreatePlan(cfg, &forward, originalOperator); err != nil {
				return err
			}
			workload, workloadStage, err := selectedCreateWorkloadImageForward(ctx, store, e, b)
			if err != nil {
				return err
			}
			if workload != nil {
				if e.versionState == nil || e.versionState.cache == nil || deploymentplan.Digest(e.versionState.cache.RuntimeImages) != deploymentplan.Digest(workload.Images) {
					return createProviderRecoveryBlock("workload successor differs from selected environment image settings")
				}
				entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+workloadStage)
				if entry != nil && entry.State == "completed" {
					if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, b, entry.Key); err != nil {
						return err
					}
				} else {
					// Qualification and execution classify original completed enrollment
					// producers through the same adapter; preflight never produces allocation
					// receipts while waiting on already written dependency phases.
					observed, err := b.Inventory(ctx, workload.Plan.Stack)
					if err != nil {
						return err
					}
					readiness, err := newEnvironmentCreateStageReadinessBackend(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, store, e, workloadStage, workload.Plan, b, observed)
					if err != nil {
						return err
					}
					readiness.observationOnly = true
					if err := workload.Plan.AdmitResumable(ctx, readiness, store.Root); err != nil {
						return err
					}
				}
				workloadImageReview = deploymentplan.Digest(workload)
			}
			return e.Check(ctx)
		}},
	}
	for _, step := range steps {
		if err := runEnvironmentProgress(ctx, out, step.id, step.run); err != nil {
			return fail(err)
		}
		report.Gates = append(report.Gates, deploymentplan.GateResult{ID: step.id, Status: "PASS", Required: true})
	}
	if partialRenewal {
		r := runtime.ServiceSessionRecovery
		if r == nil || r.RenewalCandidate == nil {
			return fail(createProviderRecoveryBlock("partial session renewal candidate is unavailable"))
		}
		if hasAdmission {
			if proof.Parent != admitted.Parent || proof.ParentOwner != admitted.ParentOwner || proof.Selection != admitted.Selection || proof.Release != admitted.Release || proof.FirstStage != admitted.FirstStage || proof.ServerStage != admitted.ServerStage || proof.ManagedStage != admitted.ManagedStage {
				return fail(createProviderRecoveryBlock("admitted continuation settings, release or stages changed"))
			}
			trustForwardNeeded = hasForwardAdmission
			if !hasForwardAdmission {
				journal, exists, progressErr := readEnvironmentCreateStageProgress(store, e, "service-session-original-key-renewal-"+admittedReview, admitted.PartialSession.Candidate)
				if progressErr != nil {
					return fail(progressErr)
				}
				trustForwardNeeded = exists && journal.Failed != ""
			}
			if trustForwardNeeded && hasForwardAdmission {
				if proof.Selection != forwardAdmission.Selection || proof.Release != forwardAdmission.Release || proof.FirstStage != forwardAdmission.FirstStage || proof.ServerStage != forwardAdmission.ServerStage || proof.ManagedStage != forwardAdmission.ManagedStage {
					return fail(createProviderRecoveryBlock("admitted trust forward inputs changed"))
				}
				if err := partialTrustForward.Candidate.AdmitResumable(ctx, b, store.Root); err != nil {
					return fail(err)
				}
				proof = forwardAdmission
			} else if trustForwardNeeded {
				partialTrustForward, err = preparePartialSessionTrustForward(ctx, cfg, store, e, b, admitted, admittedReview, r)
				if err != nil {
					return fail(err)
				}
				if err := partialTrustForward.Candidate.Admit(ctx, b); err != nil {
					return fail(err)
				}
				var snapshot environmentCreateJournal
				if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
					return fail(err)
				}
				proof.PartialTrustForward = &partialSessionTrustForwardProof{SourceJournal: snapshot, OriginalReview: admittedReview, Forward: partialTrustForward}
				if !partialSessionTrustForwardProofMatches(proof, admitted, admittedReview, e.Journal) {
					return fail(createProviderRecoveryBlock("partial trust forward qualification is inconsistent"))
				}
			} else {
				if proof.Checker != admitted.Checker {
					return fail(createProviderRecoveryBlock("admitted continuation checker changed"))
				}
				if err := r.RenewalCandidate.AdmitResumable(ctx, b, store.Root); err != nil {
					return fail(err)
				}
				proof = admitted
			}
		} else {
			if err := r.RenewalCandidate.Admit(ctx, b); err != nil {
				return fail(err)
			}
			proof.PartialSession, err = makePartialSessionProof(proof, e, r)
			if err != nil {
				return fail(err)
			}
		}
	}
	sha := deploymentplan.Digest(proof)
	if o.Action == "preflight" && completedSession != nil && len(forward.Bootstrap.ProductIdentities) > 0 {
		// Qualify the original declared producers with the installed core ACKs;
		// their future identities/ACKs belong to execution, before finalization.
		if runtime.ServiceSessionRecovery == nil {
			return fail(createProviderRecoveryBlock("completed Service session recovery authority is unavailable"))
		}
		productRuntime := runtime
		if runtime.PKIClient != nil {
			client := *runtime.PKIClient
			client.Execution = e
			productRuntime.PKIClient = &client
		}
		if runtime.Custody != nil && runtime.ServiceSessionRecovery != nil {
			binding := *runtime.Custody
			binding.ServiceRootSHA256 = runtime.ServiceSessionRecovery.Ceremony.Root.RootFingerprint
			productRuntime.Custody = &binding
		}
		if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
			if _, _, err := qualifyFreshProductResume(ctx, cfg, forward.Bootstrap, forward.Release, store, productRuntime, runtime.ServiceSessionRecovery.Ceremony); err != nil {
				return fail(err)
			}
		} else {
			program, err := readFreshProductProgram(store, e)
			if err != nil {
				return fail(err)
			}
			path, pathErr := store.safePath(productIdentitySessionPath(&program.Lifecycle))
			if pathErr != nil {
				return fail(pathErr)
			}
			if _, statErr := os.Lstat(path); statErr == nil {
				installed, err := readProductInstalled(store, &program.Lifecycle)
				if err != nil {
					return fail(err)
				}
				if err := verifyProductIdentityOutputs(ctx, cfg, productRuntime, &program.Lifecycle, installed); err != nil {
					return fail(err)
				}
			} else if !os.IsNotExist(statErr) {
				return fail(statErr)
			}
		}
		if err := qualifyRemainingFreshProductIssuer(ctx, cfg, store, &forward, productRuntime); err != nil {
			return fail(err)
		}
	}
	// The remaining current-core successor has its own exact full-scope review.
	// All original continuation keys and deadlines continue to use sha below.
	reportedSHA := sha
	applicationPlanSHA := o.PlanSHA256
	var currentCoreEnvelope *currentCoreForwardEnvelope
	if o.Action == "create" && workloadImageReview != "" && o.PlanSHA256 == sha {
		return fail(createProviderRecoveryBlock("runtime image continuation requires its new complete GO plan, including exact workload successor"))
	}
	if o.Action == "create" && completedSession != nil && o.PlanSHA256 != sha {
		currentCoreEnvelope, err = loadCurrentCoreForwardEnvelope(ctx, store, e, o.PlanSHA256, sha, &forward, runtime)
		if err != nil {
			return fail(err)
		}
		if currentCoreEnvelope != nil {
			if currentCoreEnvelope.WorkloadImageForward != workloadImageReview {
				return fail(currentCoreForwardBlock("exact-workload-image-review"))
			}
			applicationPlanSHA, err = currentCoreApplicationDispatchReview(sha, o.PlanSHA256, currentCoreEnvelope)
			if err != nil {
				return fail(err)
			}
			reportedSHA, o.PlanSHA256 = o.PlanSHA256, sha
		}
	}
	var applicationForward *createFailedForwardProof
	if resumeSuccessor && hasAdmission && (!trustForwardNeeded || hasForwardAdmission) {
		if o.Action == "create" && o.Confirm != forward.Stack {
			return fail(createProviderRecoveryBlock("the selected original environment confirmation is required"))
		}
		executionDeadline := priorEntry.StartedAt.Add(o.Timeout)
		if completedSession != nil {
			executionDeadline, err = currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
			if err != nil || executionDeadline.Before(completedSession.ExecutionDeadline) {
				return fail(createProviderRecoveryBlock("original Service session execution deadline is unavailable"))
			}
		} else if o.Action == "create" {
			if effective, found, readErr := effectiveIssuerDeadline(ctx, store, e, sha, priorEntry); readErr != nil {
				return fail(readErr)
			} else if found {
				executionDeadline = effective
			}
		}
		if o.Action == "preflight" && completedSession == nil {
			executionDeadline = time.Time{}
			// Only an already-started failed-stage repair needs the original
			// execution window; ordinary preflight does not depend on it.
			for _, entry := range e.Journal.Entries {
				if entry.Phase != "failed-stage-forward" {
					continue
				}
				var found bool
				executionDeadline, found, err = effectiveIssuerDeadline(ctx, store, e, sha, priorEntry)
				if err != nil {
					return fail(err)
				}
				if !found {
					executionDeadline = time.Time{}
				}
				break
			}
		}
		applicationForward, err = dispatchFailedApplicationForward(ctx, cfg, store, e, b, &forward, runtime, proof, sha, o.Action, applicationPlanSHA, out, time.Now().UTC(), executionDeadline)
		if err != nil {
			return fail(err)
		}
		if applicationForward != nil {
			review := deploymentplan.Digest(*applicationForward)
			qualifiedAt, validUntil := applicationForward.QualifiedAt, applicationForward.ValidUntil
			if applicationForward.initializer != nil {
				review = deploymentplan.Digest(*applicationForward.initializer)
				qualifiedAt, validUntil = applicationForward.initializer.QualifiedAt, applicationForward.initializer.ExecutionDeadline
				if applicationForward.initializer.mount != nil {
					review = deploymentplan.Digest(*applicationForward.initializer.mount)
					qualifiedAt, validUntil = applicationForward.initializer.mount.QualifiedAt, applicationForward.initializer.mount.ExecutionDeadline
				}
				if !executionDeadline.IsZero() && executionDeadline.After(validUntil) {
					validUntil = executionDeadline
				}
			}
			if currentCoreEnvelope != nil && currentCoreEnvelope.ApplicationReview != review {
				return fail(currentCoreForwardBlock("reviewed-application-scope"))
			}
			if o.Action == "preflight" {
				var core *currentCoreForwardQualification
				if r := runtime.ServiceSessionRecovery; r != nil {
					core = r.currentCore
				}
				if _, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, sha, review, core, report, cfg.Environment, qualifiedAt, validUntil, workloadImageReview); err != nil {
					return fail(err)
				}
				return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
			}
			// The repair has its own admission. Subsequent original stages keep
			// using the previously admitted continuation and its original SHA.
			o.PlanSHA256 = sha
		}
	}
	if currentCoreEnvelope != nil && applicationForward == nil && currentCoreEnvelope.ApplicationReview != "" {
		return fail(currentCoreForwardBlock("missing-reviewed-application-scope"))
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.currentCore != nil && o.Action == "preflight" {
		reportedSHA, err = writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, sha, deploymentplan.Digest(r.currentCore), "", workloadImageReview)
		if err != nil {
			return fail(err)
		}
	}
	if workloadImageReview != "" && o.Action == "preflight" && reportedSHA == sha {
		reportedSHA, err = writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, sha, "", "", workloadImageReview)
		if err != nil {
			return fail(err)
		}
	}
	for i := range report.Gates {
		report.Gates[i].EvidenceSHA256 = reportedSHA
	}
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: reportedSHA}
	for _, stage := range []string{"remaining-bootstrap", "workload", "routing", "acceptance"} {
		report.Plan.Resources = append(report.Plan.Resources, deploymentplan.SummaryResource{ID: "Stage/" + stage, Phase: stage, Action: "create"})
	}
	rel := filepath.Join("deployment", "create", "issuer-enrollment-continuation", sha, "qualification.json")
	now := time.Now().UTC()
	if o.Action == "preflight" {
		if hasAdmission && (!trustForwardNeeded || hasForwardAdmission) {
			expected := admittedReview
			if hasForwardAdmission {
				expected = forwardReview
			}
			if sha != expected {
				return fail(createProviderRecoveryBlock("admitted partial session qualification changed"))
			}
			report.Decision = "GO"
			report.QualificationSHA256 = reportedSHA
			return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
		}
		if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(rel)); err != nil {
			return fail(err)
		}
		proof.QualifiedAt, proof.ValidUntil = now, now.Add(time.Hour)
		if err := store.write(rel, mustProviderRecoveryJSON(proof), true); err != nil {
			return fail(err)
		}
		report.Decision, report.QualifiedAt, report.EvidenceValidUntil = "GO", &proof.QualifiedAt, &proof.ValidUntil
		report.QualificationSHA256 = deploymentplan.Digest(proof)
		return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
	}
	var reviewed issuerEnrollmentContinuationProof
	existingPartialAdmission := hasAdmission && (!trustForwardNeeded || hasForwardAdmission)
	if o.Confirm != forward.Stack || o.PlanSHA256 != sha || readEnvironmentPreflightJSON(filepath.Join(store.Root, rel), &reviewed) != nil ||
		reviewed.QualifiedAt.After(now) || !reviewed.ValidUntil.Equal(reviewed.QualifiedAt.Add(time.Hour)) || !existingPartialAdmission && !now.Before(reviewed.ValidUntil) {
		return fail(createProviderRecoveryBlock("fresh exact GO for this accepted enrollment continuation is required"))
	}
	qualifiedAt, validUntil := reviewed.QualifiedAt, reviewed.ValidUntil
	reviewed.QualifiedAt, reviewed.ValidUntil = time.Time{}, time.Time{}
	if deploymentplan.Digest(reviewed) != sha {
		return fail(createProviderRecoveryBlock("original enrollment continuation no longer matches its reviewed inputs"))
	}
	key := issuerEnrollmentContinuationPrefix + sha
	admittedAt := now
	if resumeCore {
		key, admittedAt = priorEntry.Key, priorEntry.StartedAt
		deadline := admittedAt.Add(o.Timeout)
		if effective, found, readErr := effectiveIssuerDeadline(ctx, store, e, sha, priorEntry); readErr != nil {
			return fail(readErr)
		} else if found {
			deadline = effective
		}
		if completedSession != nil {
			deadline, err = currentInitializerEffectiveDeadline(ctx, store, e, now)
			if err != nil || deadline.Before(completedSession.ExecutionDeadline) {
				return fail(createProviderRecoveryBlock("original Service session execution deadline is unavailable"))
			}
		}
		if !now.Before(deadline) {
			return fail(createProviderRecoveryBlock("original continuation execution timeout is exhausted"))
		}
		relative := filepath.Join(filepath.Dir(rel), "resume-admission.json")
		if err := recordIssuerResumeAdmission(store, relative, sha, key, now, deadline); err != nil {
			return fail(err)
		}
	} else if err := e.BeginEntry(key, "continuation", mustProviderRecoveryJSON(reviewed), now); err != nil {
		return fail(err)
	}
	if resumeDevice && !resumeSuccessor {
		firstStageName = "device-root-trust-policy-forward-" + sha
		runtime.ServiceSessionRecovery.Review = sha
	}
	if partialRenewal {
		qualified := reviewed
		qualified.QualifiedAt, qualified.ValidUntil = qualifiedAt, validUntil
		r := runtime.ServiceSessionRecovery
		if completedSession != nil {
			stage := "service-session-original-key-renewal-" + completedSessionReview
			candidate, err := readEnvironmentCreateStageInput(store, e, stage)
			if err != nil || candidate == nil || !r.SecondSession || r.Review != completedSessionReview || candidate.SHA256() != completedSession.Renewal.Candidate.SHA256() {
				return fail(createProviderRecoveryBlock("completed Service session original Job evidence changed"))
			}
		} else if trustForwardNeeded {
			if err := beginPartialSessionTrustForward(store, e, qualified, sha, admitted, admittedReview, now); err != nil {
				return fail(err)
			}
			r.Review = admittedReview
			r.ManagedTransportRecovery.PartialTrustForward, r.ManagedTransportRecovery.PartialTrustForwardReview = partialTrustForward, sha
			r.RenewalCandidate = partialTrustForward.Candidate
			if err := saveEnvironmentCreateStageInput(ctx, store, e, "partial-session-trust-forward-"+sha, partialTrustForward.Candidate); err != nil {
				return fail(err)
			}
		} else {
			if err := beginPartialSessionAdmission(store, e, qualified, sha, now); err != nil {
				return fail(err)
			}
			r.Review = sha
			if err := saveEnvironmentCreateStageInput(ctx, store, e, "service-session-original-key-renewal-"+sha, r.RenewalCandidate); err != nil {
				return fail(err)
			}
		}
	}
	if resumeSuccessor && interruptedIssuerStage == nil {
		firstStageName = "transport-enrollment-issuer"
	}
	if err := saveEnvironmentCreateStageInput(ctx, store, e, firstStageName, firstStage); err != nil {
		return fail(err)
	}
	if serverForward != nil {
		if err := saveEnvironmentCreateStageInput(ctx, store, e, openBaoStartupForwardStage(interruptedServerStage), serverForward); err != nil {
			return fail(err)
		}
	}
	if managedForward != nil {
		if err := saveEnvironmentCreateStageInput(ctx, store, e, managedAllocationForwardStage(interruptedManagedStage), managedForward); err != nil {
			return fail(err)
		}
	}
	report.QualifiedAt, report.EvidenceValidUntil, report.ExecutionAdmittedAt, report.ReusedPreflight = &qualifiedAt, &validUntil, &admittedAt, true
	if deadline, ok := ctx.Deadline(); ok {
		report.ExecutionDeadline = &deadline
	}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		if applicationForward != nil && stage == applicationForward.FailedStage.Stage {
			return verifyCompletedApplicationForwardStage(ctx, cfg, store, &forward, e, b, applicationForward, definitions)
		}
		if partialRenewal && stage == "service-session-original-key-renewal-"+runtime.ServiceSessionRecovery.Review {
			r := runtime.ServiceSessionRecovery
			candidate := r.RenewalCandidate
			if candidate == nil || len(candidate.Resources) != 1 || len(definitions) != 1 || deploymentplan.ID(definitions[0].Object) != candidate.Resources[0].ID || deploymentplan.Digest(deploymentplan.Projection(definitions[0].Object)) != deploymentplan.Digest(deploymentplan.Projection(candidate.Resources[0].Desired)) {
				return createProviderRecoveryBlock("partial session renewal differs from its qualified original Job")
			}
			if r.SecondSession || !trustForwardNeeded {
				return executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, stage, candidate, b, out)
			}
			forwardStage := "partial-session-trust-forward-" + sha
			if err := executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, forwardStage, candidate, b, out); err != nil {
				return err
			}
			key := "kubernetes/" + stage
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			alias := mustProviderRecoveryJSON(partialSessionTrustForwardAlias(partialTrustForward, sha))
			if entry == nil {
				return createProviderRecoveryBlock("original failed seed stage is unavailable")
			}
			if entry.State == "completed" {
				if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(alias)) {
					return createProviderRecoveryBlock("original seed forward receipt changed")
				}
				return nil
			}
			if entry.State == "pending" {
				if err := e.RecordReceipt(key, alias); err != nil {
					return err
				}
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
		if resumeSuccessor && interruptedManagedStage != nil && stage == "transport-managed-hosts" {
			forwardStage := managedAllocationForwardStage(interruptedManagedStage)
			if err := executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, forwardStage, managedForward, b, out); err != nil {
				return err
			}
			key := "kubernetes/" + stage
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil {
				return createProviderRecoveryBlock("original interrupted managed host stage disappeared")
			}
			if entry.State == "completed" {
				return nil
			}
			if entry.State == "pending" {
				if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"actual_qualified_forward_stage": "kubernetes/" + forwardStage, "original_failed_plan_sha256": interruptedManagedStage.SHA256()})); err != nil {
					return err
				}
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
		if resumeSuccessor && interruptedServerStage != nil && stage == "transport-server-enrollment" {
			forwardStage := openBaoStartupForwardStage(interruptedServerStage)
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if len(managedWrites) == 0 || entry == nil || entry.State != "completed" {
				if err := executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, forwardStage, serverForward, b, out); err != nil {
					return err
				}
			}
			key := "kubernetes/" + stage
			entry = createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil {
				return createProviderRecoveryBlock("original interrupted server enrollment disappeared")
			}
			if entry.State == "completed" {
				return nil
			}
			if entry.State == "pending" {
				if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"actual_qualified_forward_stage": "kubernetes/" + forwardStage, "original_failed_plan_sha256": interruptedServerStage.SHA256()})); err != nil {
					return err
				}
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
		if resumeSuccessor && interruptedIssuerStage != nil && stage == "transport-enrollment-issuer" {
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if len(managedWrites) == 0 || entry == nil || entry.State != "completed" {
				if err := executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, firstStageName, firstStage, b, out); err != nil {
					return err
				}
			}
			key := "kubernetes/" + stage
			entry = createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil {
				return createProviderRecoveryBlock("original interrupted issuer stage disappeared")
			}
			if entry.State == "completed" {
				return nil
			}
			if entry.State == "pending" {
				if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"actual_qualified_forward_stage": "kubernetes/" + firstStageName, "original_failed_plan_sha256": interruptedIssuerStage.SHA256()})); err != nil {
					return err
				}
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
		if resumeDevice && stage == firstStageName {
			return executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, stage, firstStage, b, out)
		}
		if resumeMQTT && stage == "mqtt-root-trust" {
			if err := executeCompiledEnvironmentCreateStage(ctx, cfg, store, &forward, e, firstStageName, firstStage, b, out); err != nil {
				return err
			}
			if resumeLog {
				if err := completeMQTTCustodyForwardEntry(e, mqttCustodyCAStage, firstStageName, mqttCAPlan, runtime.AcceptedMQTTRoot.ID); err != nil {
					return err
				}
			}
			return completeMQTTCustodyForwardEntry(e, stage, firstStageName, mqttSourcePlan, runtime.AcceptedMQTTRoot.ID)
		}
		return applyEnvironmentCreateStage(ctx, cfg, store, &forward, e, stage, definitions, out)
	}
	if err := executeEnvironmentCreateWithRuntime(ctx, cfg, &forward, source.Plan.Receipts, store, operator, e, out, runtime, apply); err != nil {
		return fail(err)
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"accepted": true, "original_parent": proof.Parent, "release_sha256": proof.Release})); err != nil {
		return fail(err)
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		return fail(err)
	}
	if err := e.CompleteAndRelease(time.Now().UTC()); err != nil {
		return fail(err)
	}
	report.Decision, report.Completed = "GO", true
	return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

// The expired predecessor is no longer the active recovery path once its
// authenticated successor batch and real ACKs have completed.
func shouldTryExpiredSessionPrivateRepair(resumeSuccessor, hasAdmission bool, completed *secondSessionAdmission) bool {
	return resumeSuccessor && hasAdmission && completed == nil
}
