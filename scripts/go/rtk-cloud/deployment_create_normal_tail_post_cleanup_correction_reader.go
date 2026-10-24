package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func postCleanupCorrectionStagePlan(cfg deploymentConfig, e *environmentCreateExecution, source createNormalTailPostCleanupCorrectionSource, program *createNormalTailProgram, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, part string, defs []deploymentplan.Definition, p *deploymentplan.Plan) error {
	if p == nil {
		return postCleanupCorrectionBlock("correction lost its original compiled child")
	}
	stage := createNormalTailPostCleanupStage + "-" + part
	in := deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: source.HandoffSHA256}
	liveByID := map[string]deploymentplan.Object{}
	for _, d := range postCleanupCorrectionOriginalDefinitions(original, originalJournal) {
		liveByID[deploymentplan.ID(d.Object)] = d.Object
	}
	for _, r := range p.Resources {
		if r.Action == "create" {
			if r.UID != "" || r.Baseline != nil {
				return postCleanupCorrectionBlock("created correction resource has an adopted owner")
			}
			delete(liveByID, r.ID)
			continue
		}
		if r.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(r.UID) {
			return postCleanupCorrectionBlock("correction lacks its exact existing owner baseline")
		}
		if part != "retire" && originalJournal.Owners[r.ID] != r.UID {
			return postCleanupCorrectionBlock("correction changed an original workload owner")
		}
		o := deploymentplan.Clone(r.Baseline)
		m := deploymentplan.Map(o["metadata"])
		m["uid"] = r.UID
		m["resourceVersion"] = "1"
		liveByID[r.ID] = o
	}
	var live []deploymentplan.Object
	for _, o := range liveByID {
		live = append(live, o)
	}
	want, err := deploymentplan.Compile(in, defs, live)
	if err != nil || want.SHA256() != p.SHA256() {
		return postCleanupCorrectionBlock("correction child differs from its finite original recipe")
	}
	return nil
}

// Pure chronological reader; no API observation can substitute for a saved
// ordinary child, its original owner or its exact durable response.
func readCreateNormalTailPostCleanupCorrection(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createNormalTailPostCleanupCorrection, error) {
	if e == nil || e.Store != store {
		return nil, postCleanupCorrectionBlock("original correction owner is unavailable")
	}
	var s createNormalTailPostCleanupCorrectionSource
	found, err := readPostCleanupJSON(e, "source", &s)
	if err != nil {
		return nil, err
	}
	if !found {
		if postCleanupCorrectionHasHistory(e) {
			return nil, postCleanupCorrectionBlock("recorded correction lost its immutable source")
		}
		for _, part := range []string{"prepared", "completed"} {
			if _, err := os.Lstat(postCleanupCorrectionPath(e, part)); err == nil {
				return nil, postCleanupCorrectionBlock("orphan correction artifact")
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		return nil, nil
	}
	program, original, originalJournal, err := postCleanupCorrectionOriginal(store, e, &s)
	if err != nil {
		return nil, err
	}
	if deploymentplan.Digest(cfg) != s.ConfigurationSHA256 {
		return nil, postCleanupCorrectionBlock("selected maintained configuration changed")
	}
	r := &createNormalTailPostCleanupCorrection{Source: s, SourceSHA256: deploymentplan.Digest(s), Plans: map[string]*deploymentplan.Plan{}, Journals: map[string]deploymentplan.Journal{}, originalProgram: program, originalPlan: original, originalJournal: originalJournal}
	var prepared createNormalTailPostCleanupPrepared
	preparedFound, err := readPostCleanupJSON(e, "prepared", &prepared)
	if err != nil {
		return nil, err
	}
	if preparedFound {
		if prepared.SourceSHA256 != r.SourceSHA256 || prepared.ObservedAt.Before(s.QualifiedAt) || prepared.ObservedAt.After(time.Now().UTC()) || (s.CertIssuerPublic == nil) != (prepared.CertIssuer == nil) {
			return nil, postCleanupCorrectionBlock("prepared public state differs from its original source")
		}
		if s.CertIssuerPublic != nil {
			p := prepared.CertIssuer
			want := s.CertIssuerPublic
			if p.Schema != "rtk-cloud.certissuer-public-profile-proof/v1" || p.IntentSHA256 != deploymentplan.Digest(want) || p.RequestID != want.RequestID || p.StatePath != want.NewStatePath || p.OriginalCertificateSHA256 != want.OriginalCertificateSHA256 || p.PublicKeySHA256 != want.OriginalPublicKeySHA256 || p.RootSHA256 != want.RootSHA256 || deploymentplan.Digest(p.DNSNames) != deploymentplan.Digest(want.DNSNames) || !environmentPreflightDigest.MatchString(p.CertificateSHA256) || !environmentPreflightDigest.MatchString(p.StateSHA256) {
				return nil, postCleanupCorrectionBlock("prepared public identity is not the exact same-key profile")
			}
		}
		r.Prepared = &prepared
	}
	cursor := s.PrefixEntries
	previous := s.QualifiedAt
	admission, err := readCreateNormalTailProgramAdmission(store, e)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"prepare", "apply", "retire"} {
		defs, err := postCleanupCorrectionDefinitions(cfg, s, original, originalJournal, part, r.Plans["prepare"], r.Journals["prepare"])
		if err != nil {
			return nil, err
		}
		if len(defs) == 0 {
			continue
		}
		if cursor == len(e.Journal.Entries) {
			break
		}
		entry := e.Journal.Entries[cursor]
		stage := createNormalTailPostCleanupStage + "-" + part
		if entry.Key != postCleanupCorrectionKey(part) || entry.Phase != stage || entry.StartedAt.Before(previous) || entry.StartedAt.Before(admission.AdmittedAt) || !entry.StartedAt.Before(admission.Deadline) || entry.StartedAt.After(time.Now().UTC()) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State != "completed" && !entry.CompletedAt.IsZero() || entry.State == "completed" && (entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || entry.CompletedAt.After(admission.Deadline)) {
			return nil, postCleanupCorrectionBlock("unknown or out-of-order correction stage")
		}
		if part == "apply" && s.CertIssuerPublic != nil && (r.Prepared == nil || r.Prepared.ObservedAt.After(entry.StartedAt)) {
			return nil, postCleanupCorrectionBlock("runtime correction precedes prepared same-key public identity")
		}
		p, err := readCreateNormalTailPreparationPlan(store, e, stage)
		if err != nil {
			return nil, err
		}
		if err := postCleanupCorrectionStagePlan(cfg, e, s, program, original, originalJournal, part, defs, p); err != nil {
			return nil, err
		}
		child, j, err := readPostCleanupCorrectionStage(store, e, stage, p)
		if err != nil {
			return nil, err
		}
		r.Plans[part], r.Journals[part] = child, j
		cursor++
		if entry.State != "completed" {
			if preparedFound && part == "prepare" {
				return nil, postCleanupCorrectionBlock("prepared public identity precedes accepted helper")
			}
			if _, err := os.Lstat(postCleanupCorrectionPath(e, "completed")); err == nil {
				return nil, postCleanupCorrectionBlock("completion precedes an unfinished finite child")
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			if cursor != len(e.Journal.Entries) {
				return nil, postCleanupCorrectionBlock("unfinished correction was followed by another effect")
			}
			return r, nil
		}
		if !j.Accepted {
			return nil, postCleanupCorrectionBlock("correction completion has no accepted native child")
		}
		previous = entry.CompletedAt
	}
	if preparedFound && s.CertIssuerPublic != nil && !r.Journals["prepare"].Accepted {
		return nil, postCleanupCorrectionBlock("prepared public identity has no accepted helper")
	}
	var completed createNormalTailPostCleanupCompletion
	completedFound, err := readPostCleanupJSON(e, "completed", &completed)
	if err != nil {
		return nil, err
	}
	if completedFound {
		if !r.Journals["apply"].Accepted || s.CertIssuerPublic != nil && (!r.Journals["prepare"].Accepted || !r.Journals["retire"].Accepted || r.Prepared == nil) {
			return nil, postCleanupCorrectionBlock("completion precedes its actual finite children")
		}
		if completed.Schema != createNormalTailPostCleanupSchema || completed.SourceSHA256 != r.SourceSHA256 || completed.PreparedSHA256 != deploymentplan.Digest(r.Prepared) || completed.CompletedAt.Before(previous) || completed.CompletedAt.After(time.Now().UTC()) || len(completed.Plans) != len(r.Plans) || len(completed.Journals) != len(r.Plans) {
			return nil, postCleanupCorrectionBlock("correction completion proof changed")
		}
		for part, p := range r.Plans {
			j := r.Journals[part]
			if !j.Accepted || completed.Plans[part] != p.SHA256() || completed.Journals[part] != deploymentplan.Digest(j) {
				return nil, postCleanupCorrectionBlock("correction completion replaced a native child")
			}
		}
		r.Completion = &completed
	}
	if cursor < len(e.Journal.Entries) {
		entry := e.Journal.Entries[cursor]
		want := deploymentplan.Object{"source_sha256": r.SourceSHA256, "completion_sha256": deploymentplan.Digest(completed)}
		if !completedFound || cursor+1 != len(e.Journal.Entries) || entry.Key != createNormalTailPostCleanupStage+"-complete" || entry.Phase != "verification" || entry.State != "completed" || !entry.StartedAt.Equal(completed.CompletedAt) || !entry.CompletedAt.Equal(completed.CompletedAt) || entry.IntentSHA256 != deploymentplan.Digest(want) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(want) {
			return nil, postCleanupCorrectionBlock("unknown appended correction authority")
		}
		cursor++
	}
	if cursor != len(e.Journal.Entries) || !e.Journal.CompletedAt.IsZero() && (r.Completion == nil || cursor == s.PrefixEntries || e.Journal.Entries[cursor-1].Key != createNormalTailPostCleanupStage+"-complete") {
		return nil, postCleanupCorrectionBlock("original closure lacks completed finite correction")
	}
	return r, nil
}

func applyCreateNormalTailPostCleanupDefinitions(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	r, err := readCreateNormalTailPostCleanupCorrection(cfg, store, e)
	if err != nil || r == nil {
		return definitions, err
	}
	if !r.Journals["apply"].Accepted {
		return definitions, nil
	}
	byID := map[string]deploymentplan.Definition{}
	for _, part := range []string{"prepare", "apply"} {
		if !r.Journals[part].Accepted {
			continue
		}
		for _, resource := range r.Plans[part].Resources {
			if part == "prepare" && resource.Desired["kind"] != "ConfigMap" {
				continue
			}
			object := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(object["metadata"])["uid"] = r.Journals[part].Owners[resource.ID]
			byID[resource.ID] = deploymentplan.Definition{Object: object, Phase: resource.Phase, Retain: true}
		}
	}
	var out []deploymentplan.Definition
	for _, d := range definitions {
		if next, ok := byID[deploymentplan.ID(d.Object)]; ok {
			out = append(out, next)
			delete(byID, deploymentplan.ID(d.Object))
		} else {
			out = append(out, d)
		}
	}
	keys := make([]string, 0, len(byID))
	for id := range byID {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		out = append(out, byID[id])
	}
	return out, nil
}

func postCleanupCorrectionCompletionReceipt(r *createNormalTailPostCleanupCorrection) json.RawMessage {
	return mustProviderRecoveryJSON(deploymentplan.Object{"source_sha256": r.SourceSHA256, "completion_sha256": deploymentplan.Digest(r.Completion)})
}

func validatePostCleanupStageBeforeExecution(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan) error {
	r, err := readCreateNormalTailPostCleanupCorrection(cfg, store, e)
	if err != nil {
		return err
	}
	if r == nil {
		return postCleanupCorrectionBlock("finite stage has no original correction source")
	}
	part := strings.TrimPrefix(stage, createNormalTailPostCleanupStage+"-")
	if part == stage || part != "prepare" && part != "apply" && part != "retire" {
		return postCleanupCorrectionBlock("unknown finite correction stage")
	}
	program, original, j := r.originalProgram, r.originalPlan, r.originalJournal
	defs, err := postCleanupCorrectionDefinitions(cfg, r.Source, original, j, part, r.Plans["prepare"], r.Journals["prepare"])
	if err != nil {
		return err
	}
	return postCleanupCorrectionStagePlan(cfg, e, r.Source, program, original, j, part, defs, p)
}

// The common reader authenticates the exact pending intent and proves absence
// of both a child journal and its lease before returning nil. Retain that known
// zero-effect prefix so the same ordinary admission can resume; an absent
// intent, allocated response, malformed child or unknown write stays blocked.
func readPostCleanupCorrectionStage(store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	child, j, err := readCreateNormalTailStage(store, e, stage, p)
	if err != nil || j.InFlight != "" {
		return nil, j, postCleanupCorrectionBlock("correction has an unknown or invalid ordinary execution")
	}
	if child == nil {
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
		if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || deploymentplan.Digest(j) != deploymentplan.Digest(deploymentplan.Journal{}) {
			return nil, j, postCleanupCorrectionBlock("correction has no authenticated zero-effect admission prefix")
		}
		child = p
	}
	return child, j, nil
}
