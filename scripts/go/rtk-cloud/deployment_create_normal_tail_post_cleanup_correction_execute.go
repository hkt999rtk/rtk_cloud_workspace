package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type postCleanupCorrectionOperations struct {
	Qualify  func(context.Context, *createNormalTailProgram, *deploymentplan.Plan, deploymentplan.Journal) (*environmentCertIssuerPublicProfile, *environmentLogIngesterPVCWriterProfile, error)
	Prepared func(context.Context, environmentCertIssuerPublicProfile) (environmentCertIssuerPublicProof, error)
	Verify   func(context.Context, *createNormalTailPostCleanupCorrection) error
	Apply    environmentCreateBootstrapApply
}

func ensureCreateNormalTailPostCleanupCorrection(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, helperImage, controllerImage string, out io.Writer) error {
	e := h.Execution
	ops := postCleanupCorrectionOperations{
		Qualify: func(ctx context.Context, p *createNormalTailProgram, w *deploymentplan.Plan, j deploymentplan.Journal) (*environmentCertIssuerPublicProfile, *environmentLogIngesterPVCWriterProfile, error) {
			original := postCleanupCorrectionOriginalDefinitions(w, j)
			var public *environmentCertIssuerPublicProfile
			if helperImage != "" {
				v, err := qualifyEnvironmentPostCleanupCertIssuerProfile(ctx, cfg, c.Backend, original, helperImage)
				if err != nil {
					return nil, nil, err
				}
				public = &v
			}
			ledger, err := readEnvironmentCreateLedger(e)
			if err != nil {
				return nil, nil, err
			}
			log, err := qualifyEnvironmentPostCleanupLogIngesterProfile(ctx, cfg, c.Backend, original, ledger)
			if err != nil {
				return nil, nil, err
			}
			return public, &log, nil
		},
		Prepared: func(ctx context.Context, p environmentCertIssuerPublicProfile) (environmentCertIssuerPublicProof, error) {
			return verifyEnvironmentCertIssuerPublicProfile(ctx, cfg, c.Backend, p)
		},
		Verify: func(ctx context.Context, r *createNormalTailPostCleanupCorrection) error {
			if r.Source.CertIssuerPublic != nil {
				if r.Prepared == nil || r.Prepared.CertIssuer == nil {
					return postCleanupCorrectionBlock("public serving proof is unavailable")
				}
				if err := verifyEnvironmentPostCleanupCertIssuerServing(ctx, cfg, c.Backend, *r.Source.CertIssuerPublic, *r.Prepared.CertIssuer); err != nil {
					return err
				}
			}
			// Native consumers poll asynchronously after their ordinary rollout.
			// Wait for their actual selected mounted CRLs and registry ACKs before
			// the unchanged whole-environment acceptance observes them.
			if r.Source.ControllerImage != "" || r.Source.LogIngester != nil {
				if err := verifyEnvironmentPostCleanupCRLConsumers(ctx, cfg, c.Store, c.Backend); err != nil {
					return err
				}
			}
			return e.Check(ctx)
		},
		Apply: func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
			parent := *h.OriginalPlan
			parent.Release = c.Release
			return applyCreateNormalTailStageWithCapture(ctx, cfg, c.Store, &parent, e, stage, defs, out, false)
		},
	}
	return ensureCreateNormalTailPostCleanupCorrectionWithOperations(ctx, cfg, h, c, helperImage, controllerImage, ops)
}

// The transport boundaries remain explicit for the maintained isolated native
// fixture. All provenance, recipes, ordinary execution and completion are shared.
func ensureCreateNormalTailPostCleanupCorrectionWithOperations(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, helperImage, controllerImage string, ops postCleanupCorrectionOperations) error {
	if h == nil || h.Execution == nil || c == nil || c.Store != h.Execution.Store || ops.Qualify == nil || ops.Prepared == nil || ops.Verify == nil || ops.Apply == nil {
		return postCleanupCorrectionBlock("complete original correction adapters are required")
	}
	e := h.Execution
	if err := e.Check(ctx); err != nil {
		return err
	}
	r, err := readCreateNormalTailPostCleanupCorrection(cfg, c.Store, e)
	if err != nil {
		return err
	}
	if r == nil {
		if helperImage == "" && controllerImage == "" {
			return nil
		}
		program, w, j, err := postCleanupCorrectionOriginal(c.Store, e, nil)
		if err != nil {
			return err
		}
		if program.HandoffSHA256 != deploymentplan.Digest(h.Pins) || program.MaintainedConfigSHA256 != deploymentplan.Digest(cfg) {
			return postCleanupCorrectionBlock("original handoff or maintained configuration changed")
		}
		for _, entry := range e.Journal.Entries {
			if entry.State != "completed" {
				return postCleanupCorrectionBlock("correction requires completed known effects")
			}
		}
		for _, key := range createNormalTailRoutingStageKeys(h.OriginalPlan) {
			entry := createOpenBaoACKCompletionEntry(e.Journal, key)
			if entry == nil || entry.State != "completed" {
				return postCleanupCorrectionBlock("correction precedes original external routing")
			}
		}
		public, log, err := ops.Qualify(ctx, program, w, j)
		if err != nil {
			return err
		}
		steady, sj, err := acceptedCreateStageForProduct(c.Store, e, program.Products, "steady")
		if err != nil {
			return err
		}
		retire, rj, err := acceptedCreateStageForProduct(c.Store, e, program.Products, "producer-retire")
		if err != nil {
			return err
		}
		var final struct {
			Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
			Tombstone json.RawMessage                        `json:"custody_tombstone"`
			UIDs      map[string]string                      `json:"credential_uids"`
		}
		if err := readProductStageJSON(c.Store, program.Products, "finalized.json", &final); err != nil {
			return err
		}
		s := createNormalTailPostCleanupCorrectionSource{Schema: createNormalTailPostCleanupSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: deploymentplan.Digest(program), HandoffSHA256: program.HandoffSHA256, ConfigurationSHA256: deploymentplan.Digest(cfg), PrefixEntries: len(e.Journal.Entries), PrefixLeases: len(e.Journal.KubernetesLeases), WorkloadPlanSHA256: w.SHA256(), WorkloadJournalSHA256: deploymentplan.Digest(j), SteadyPlanSHA256: steady.SHA256(), SteadyJournalSHA256: deploymentplan.Digest(sj), RetirementPlanSHA256: retire.SHA256(), RetirementJournalSHA256: deploymentplan.Digest(rj), FinalizationSHA256: deploymentplan.Digest(final), CertIssuerPublic: public, LogIngester: log, ControllerImage: controllerImage, QualifiedAt: time.Now().UTC()}
		prefix, err := postCleanupCorrectionPrefix(e, s)
		if err != nil {
			return err
		}
		s.PrefixJournalSHA256 = deploymentplan.Digest(prefix)
		if _, err := postCleanupCorrectionDefinitions(cfg, s, w, j, "apply", nil, deploymentplan.Journal{}); err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := writePostCleanupJSON(e, "source", s); err != nil {
			return err
		}
		r, err = readCreateNormalTailPostCleanupCorrection(cfg, c.Store, e)
		if err != nil {
			return err
		}
	}
	if controllerImage != "" && controllerImage != r.Source.ControllerImage || helperImage != "" && (r.Source.CertIssuerPublic == nil || helperImage != r.Source.CertIssuerPublic.HelperImage) {
		return postCleanupCorrectionBlock("retry selected a different qualified helper or controller image")
	}
	original, originalJournal := r.originalPlan, r.originalJournal
	for _, part := range []string{"prepare", "apply", "retire"} {
		defs, err := postCleanupCorrectionDefinitions(cfg, r.Source, original, originalJournal, part, r.Plans["prepare"], r.Journals["prepare"])
		if err != nil {
			return err
		}
		if len(defs) == 0 {
			continue
		}
		entry := createOpenBaoACKCompletionEntry(e.Journal, postCleanupCorrectionKey(part))
		if entry == nil || entry.State != "completed" {
			if part == "apply" && r.Source.CertIssuerPublic != nil && r.Prepared == nil {
				proof, err := ops.Prepared(ctx, *r.Source.CertIssuerPublic)
				if err != nil {
					return err
				}
				prepared := createNormalTailPostCleanupPrepared{SourceSHA256: r.SourceSHA256, CertIssuer: &proof, ObservedAt: time.Now().UTC()}
				if err := writePostCleanupJSON(e, "prepared", prepared); err != nil {
					return err
				}
				r, err = readCreateNormalTailPostCleanupCorrection(cfg, c.Store, e)
				if err != nil {
					return err
				}
			}
			if part == "retire" {
				if err := ops.Verify(ctx, r); err != nil {
					return err
				}
			}
			if err := ops.Apply(ctx, createNormalTailPostCleanupStage+"-"+part, defs); err != nil {
				return err
			}
			r, err = readCreateNormalTailPostCleanupCorrection(cfg, c.Store, e)
			if err != nil {
				return err
			}
		}
	}
	if err := ops.Verify(ctx, r); err != nil {
		return err
	}
	if r.Completion == nil {
		done := createNormalTailPostCleanupCompletion{Schema: createNormalTailPostCleanupSchema, SourceSHA256: r.SourceSHA256, PreparedSHA256: deploymentplan.Digest(r.Prepared), Plans: map[string]string{}, Journals: map[string]string{}, CompletedAt: time.Now().UTC()}
		for part, p := range r.Plans {
			j := r.Journals[part]
			if !j.Accepted {
				return postCleanupCorrectionBlock("correction child is not accepted")
			}
			done.Plans[part] = p.SHA256()
			done.Journals[part] = deploymentplan.Digest(j)
		}
		if err := writePostCleanupJSON(e, "completed", done); err != nil {
			return err
		}
		r.Completion = &done
	}
	key := createNormalTailPostCleanupStage + "-complete"
	if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry == nil {
		if err := e.Check(ctx); err != nil {
			return err
		}
		receipt := postCleanupCorrectionCompletionReceipt(r)
		before := e.Journal.Entries
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, Phase: "verification", State: "completed", IntentSHA256: activeTransportJournalDigest(jsonRawObject(receipt)), Receipt: receipt, StartedAt: r.Completion.CompletedAt, CompletedAt: r.Completion.CompletedAt})
		if err := e.save(); err != nil {
			e.Journal.Entries = before
			return err
		}
	}
	_, err = readCreateNormalTailPostCleanupCorrection(cfg, c.Store, e)
	return err
}
