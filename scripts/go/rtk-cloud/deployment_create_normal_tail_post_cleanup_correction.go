package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailPostCleanupSchema = "rtk-cloud.create-post-cleanup-correction/v1"
const createNormalTailPostCleanupStage = "post-cleanup-runtime-correction"

// This source grants no new identity or allocation authority. It binds one
// finite correction to an already accepted workload and completed cleanup.
type createNormalTailPostCleanupCorrectionSource struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256, HandoffSHA256   string
	ConfigurationSHA256, PrefixJournalSHA256                          string
	ControllerImage                                                   string `json:",omitempty"`
	PrefixEntries, PrefixLeases                                       int
	WorkloadPlanSHA256, WorkloadJournalSHA256                         string
	SteadyPlanSHA256, SteadyJournalSHA256                             string
	RetirementPlanSHA256, RetirementJournalSHA256, FinalizationSHA256 string
	CertIssuerPublic                                                  *environmentCertIssuerPublicProfile     `json:",omitempty"`
	LogIngester                                                       *environmentLogIngesterPVCWriterProfile `json:",omitempty"`
	QualifiedAt                                                       time.Time
}

type createNormalTailPostCleanupPrepared struct {
	SourceSHA256 string
	CertIssuer   *environmentCertIssuerPublicProof `json:",omitempty"`
	ObservedAt   time.Time
}

type createNormalTailPostCleanupCompletion struct {
	Schema, SourceSHA256, PreparedSHA256 string
	Plans, Journals                      map[string]string
	CompletedAt                          time.Time
}

type createNormalTailPostCleanupCorrection struct {
	Source       createNormalTailPostCleanupCorrectionSource
	SourceSHA256 string
	Prepared     *createNormalTailPostCleanupPrepared
	Completion   *createNormalTailPostCleanupCompletion
	Plans        map[string]*deploymentplan.Plan
	Journals     map[string]deploymentplan.Journal

	// Read-local original proof authenticated by this reader invocation. These
	// private fields do not change any durable JSON or survive a fresh read.
	originalProgram *createNormalTailProgram
	originalPlan    *deploymentplan.Plan
	originalJournal deploymentplan.Journal
}

func postCleanupCorrectionBlock(cause string) error {
	return environmentCreateBlocker("deployment.create.post-cleanup-correction", cause)
}

func postCleanupCorrectionPath(e *environmentCreateExecution, part string) string {
	return filepath.Join(e.Directory, "normal-tail-post-cleanup-correction-"+part+".json")
}

func postCleanupCorrectionKey(part string) string {
	return "kubernetes/" + createNormalTailPostCleanupStage + "-" + part
}

func postCleanupCorrectionPrefix(e *environmentCreateExecution, s createNormalTailPostCleanupCorrectionSource) (environmentCreateJournal, error) {
	if e == nil || s.PrefixEntries < 1 || s.PrefixEntries > len(e.Journal.Entries) || s.PrefixLeases < 0 || s.PrefixLeases > len(e.Journal.KubernetesLeases) {
		return environmentCreateJournal{}, postCleanupCorrectionBlock("original completed prefix is unavailable")
	}
	j := e.Journal
	j.Entries = j.Entries[:s.PrefixEntries]
	j.KubernetesLeases = j.KubernetesLeases[:s.PrefixLeases]
	j.CompletedAt = time.Time{}
	for _, entry := range j.Entries {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(s.QualifiedAt) || len(entry.Receipt) == 0 {
			return j, postCleanupCorrectionBlock("original prefix contains an unfinished outcome")
		}
	}
	return j, nil
}

func postCleanupCorrectionOriginal(store secretStore, e *environmentCreateExecution, s *createNormalTailPostCleanupCorrectionSource) (*createNormalTailProgram, *deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*createNormalTailProgram, *deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, nil, deploymentplan.Journal{}, postCleanupCorrectionBlock("accepted workload, steady transition or original finalization changed")
	}
	p, err := readCreateNormalTailProgram(store, e)
	if err != nil || p.Products == nil {
		return bad()
	}
	frozen, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return bad()
	}
	w, j, err := readCreateNormalTailWorkload(store, e, frozen)
	if err != nil || w == nil || !j.Accepted || !j.AcceptanceStarted || j.InFlight != "" || j.Failed != "" || len(j.Completed) != len(w.Resources) || len(j.Owners) != len(w.Resources) {
		return bad()
	}
	steady, sj, err := acceptedCreateStageForProduct(store, e, p.Products, "steady")
	if err != nil || steady == nil {
		return bad()
	}
	retire, rj, err := acceptedCreateStageForProduct(store, e, p.Products, "producer-retire")
	if err != nil || retire == nil {
		return bad()
	}
	var final struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone json.RawMessage                        `json:"custody_tombstone"`
		UIDs      map[string]string                      `json:"credential_uids"`
	}
	installed, err := readEffectiveProductInstalled(store, e, p.Products)
	if err != nil || installed == nil || readProductStageJSON(store, p.Products, "finalized.json", &final) != nil || final.Native.SessionID != installed.Ceremony.BootstrapSessionID || final.Native.Environment != store.Environment || final.Native.FinalizedAt.IsZero() {
		return bad()
	}
	if s != nil {
		if s.Schema != createNormalTailPostCleanupSchema || s.ParentSHA256 != e.Journal.PlanSHA256 || s.OwnerSHA256 != deploymentplan.Digest(e.owner) || s.ProgramSHA256 != deploymentplan.Digest(p) || s.HandoffSHA256 != p.HandoffSHA256 || s.ConfigurationSHA256 != p.MaintainedConfigSHA256 || s.WorkloadPlanSHA256 != w.SHA256() || s.WorkloadJournalSHA256 != deploymentplan.Digest(j) || s.SteadyPlanSHA256 != steady.SHA256() || s.SteadyJournalSHA256 != deploymentplan.Digest(sj) || s.RetirementPlanSHA256 != retire.SHA256() || s.RetirementJournalSHA256 != deploymentplan.Digest(rj) || s.FinalizationSHA256 != deploymentplan.Digest(final) || s.QualifiedAt.Before(final.Native.FinalizedAt) || s.QualifiedAt.After(time.Now().UTC()) || s.CertIssuerPublic == nil && s.LogIngester == nil && s.ControllerImage == "" {
			return bad()
		}
		prefix, err := postCleanupCorrectionPrefix(e, *s)
		if err != nil || deploymentplan.Digest(prefix) != s.PrefixJournalSHA256 {
			return bad()
		}
	}
	return p, w, j, nil
}

func postCleanupCorrectionOriginalDefinitions(p *deploymentplan.Plan, j deploymentplan.Journal) []deploymentplan.Definition {
	var defs []deploymentplan.Definition
	for _, r := range p.Resources {
		if r.Action == "retire" {
			continue
		}
		o := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(o["metadata"])["uid"] = j.Owners[r.ID]
		defs = append(defs, deploymentplan.Definition{Object: o, Phase: r.Phase})
	}
	return defs
}

func postCleanupCorrectionDefinitions(cfg deploymentConfig, s createNormalTailPostCleanupCorrectionSource, p *deploymentplan.Plan, j deploymentplan.Journal, part string, prepared *deploymentplan.Plan, preparedJournal deploymentplan.Journal) ([]deploymentplan.Definition, error) {
	original := postCleanupCorrectionOriginalDefinitions(p, j)
	var defs []deploymentplan.Definition
	if s.CertIssuerPublic != nil {
		var next []deploymentplan.Definition
		var err error
		switch part {
		case "prepare":
			next, err = environmentPostCleanupCertIssuerPreparationDefinitions(cfg, original, *s.CertIssuerPublic)
		case "apply":
			next, err = environmentPostCleanupCertIssuerPublicDefinitions(cfg, original, *s.CertIssuerPublic)
		case "retire":
			if prepared == nil || !preparedJournal.Accepted {
				return nil, postCleanupCorrectionBlock("helper retirement lacks its accepted preparation")
			}
			for _, r := range prepared.Resources {
				if r.Desired["kind"] != "Job" && r.Desired["kind"] != "NetworkPolicy" {
					continue
				}
				o := deploymentplan.Clone(r.Desired)
				deploymentplan.Map(o["metadata"])["uid"] = preparedJournal.Owners[r.ID]
				next = append(next, deploymentplan.Definition{Object: o, Phase: "retirement", Retire: true})
			}
		default:
			return nil, postCleanupCorrectionBlock("unknown finite correction step")
		}
		if err != nil {
			return nil, err
		}
		defs = append(defs, next...)
	}
	if part == "apply" && s.LogIngester != nil {
		next, err := environmentPostCleanupLogIngesterDefinitions(cfg, original, *s.LogIngester)
		if err != nil {
			return nil, err
		}
		defs = append(defs, next...)
	}
	if part == "apply" && s.ControllerImage != "" {
		d, err := postCleanupControllerDefinition(cfg, original, s.ControllerImage)
		if err != nil {
			return nil, err
		}
		defs = append(defs, d)
	}
	seen := map[string]bool{}
	for i := range defs {
		id := deploymentplan.ID(defs[i].Object)
		kind := deploymentplan.String(defs[i].Object["kind"])
		if seen[id] || !keySet("Deployment", "Ingress", "NetworkPolicy", "ConfigMap", "Job")[kind] {
			return nil, postCleanupCorrectionBlock("correction changed an undeclared resource kind")
		}
		seen[id] = true
		if !defs[i].Retire {
			defs[i].Retain = false
		}
	}
	return defs, nil
}

func readPostCleanupJSON(e *environmentCreateExecution, part string, target any) (bool, error) {
	p := postCleanupCorrectionPath(e, part)
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	raw, err := readBoundedEnvironmentFile(p, true)
	if err != nil || decodeEnvironmentJSON(raw, target) != nil {
		return false, postCleanupCorrectionBlock("private correction artifact is invalid")
	}
	return true, nil
}

func writePostCleanupJSON(e *environmentCreateExecution, part string, value any) error {
	rel, err := filepath.Rel(e.Store.Root, postCleanupCorrectionPath(e, part))
	if err != nil {
		return err
	}
	_, err = writeProductArtifact(e.Store, rel, value)
	return err
}

func postCleanupCorrectionHasHistory(e *environmentCreateExecution) bool {
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/"+createNormalTailPostCleanupStage+"-") || entry.Key == createNormalTailPostCleanupStage+"-complete" {
			return true
		}
	}
	return false
}

func postCleanupControllerDefinition(cfg deploymentConfig, original []deploymentplan.Definition, image string) (deploymentplan.Definition, error) {
	parts := strings.Split(image, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || strings.ContainsAny(parts[0], " \t\r\n") || !environmentPreflightDigest.MatchString(parts[1]) {
		return deploymentplan.Definition{}, postCleanupCorrectionBlock("controller image must be the explicit immutable qualified image")
	}
	id := "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/pki-controller"
	for _, d := range original {
		if deploymentplan.ID(d.Object) != id {
			continue
		}
		o := deploymentplan.Clone(d.Object)
		pod := environmentPKIPod(o)
		found := false
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] == "pki-controller" {
				if found || c["image"] == image {
					return deploymentplan.Definition{}, postCleanupCorrectionBlock("controller image correction is ambiguous or unchanged")
				}
				c["image"] = image
				found = true
			}
		}
		if !found {
			return deploymentplan.Definition{}, postCleanupCorrectionBlock("original controller executable is unavailable")
		}
		return deploymentplan.Definition{Object: o, Phase: "workload"}, nil
	}
	return deploymentplan.Definition{}, postCleanupCorrectionBlock("original controller owner is unavailable")
}
