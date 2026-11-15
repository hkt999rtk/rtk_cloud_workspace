package main

import (
	"context"
	"errors"
	"os"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type openBaoFirstStageQualification struct {
	PlanSHA256    string `json:"plan_sha256,omitempty"`
	JournalSHA256 string `json:"journal_sha256,omitempty"`
	ParentState   string `json:"parent_state,omitempty"`
	Accepted      bool   `json:"accepted"`
}
type openBaoFirstRecoveryQualification struct {
	Schema              string                         `json:"schema"`
	Environment         string                         `json:"environment"`
	ParentSHA256        string                         `json:"parent_sha256"`
	ParentJournalSHA256 string                         `json:"parent_journal_sha256"`
	ParentOwnerSHA256   string                         `json:"parent_owner_sha256"`
	OperatorSHA256      string                         `json:"operator_sha256"`
	Foundation          openBaoFirstStageQualification `json:"foundation"`
	OpenBao             string                         `json:"openbao"`
	Transport           string                         `json:"transport"`
	Management          string                         `json:"management"`
	Signer              string                         `json:"signer"`
	Services            openBaoFirstStageQualification `json:"services"`
}

// This qualifies only the original OpenBao-first bootstrap checkpoint. The
// caller must still run the complete recovery preflight, including the native
// provider, storage and namespace owner gates, before reporting a GO.
func qualifyOpenBaoFirstRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string) (openBaoFirstRecoveryQualification, error) {
	var q openBaoFirstRecoveryQualification
	bad := func(reason string) (openBaoFirstRecoveryQualification, error) {
		return openBaoFirstRecoveryQualification{}, createProviderRecoveryBlock("original OpenBao-first checkpoint: " + reason)
	}
	if e == nil || e.Store != store || b.Store != store || cfg.Environment != store.Environment || !e.Journal.CompletedAt.IsZero() || e.Check(ctx) != nil {
		return bad("parent owner unavailable")
	}
	plan, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !plan.OpenBaoFirst || plan.Environment != cfg.Environment || plan.Stack != e.Journal.Stack || configSHA != plan.ConfigurationSHA256 || configSHA != deploymentplan.Digest(cfg) {
		return bad("original admitted generation differs")
	}
	if _, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &plan); err != nil {
		return bad("original operator settings differ")
	}
	started := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-transport-material" {
			started = true
		}
		if !started && (entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || (entry.Phase != "provider" && entry.Phase != "storage" && entry.Phase != "namespace")) {
			return bad("earlier provider, storage or namespace operation is incomplete")
		}
	}
	if started && !isOpenBaoFirstEarlyCheckpoint(e.Journal) {
		return bad("journal contains an unsupported or ambiguous later operation")
	}
	stageSummary := func(s *openBaoFirstRecordedStage) openBaoFirstStageQualification {
		var r openBaoFirstStageQualification
		if s != nil {
			r.PlanSHA256 = s.Plan.SHA256()
			r.ParentState = s.ParentState
			r.Accepted = s.HasJournal && s.Journal.Accepted
			if s.HasJournal {
				r.JournalSHA256 = deploymentplan.Digest(s.Journal)
			}
		}
		return r
	}
	foundation, err := inspectOpenBaoFirstRecordedStage(ctx, cfg, store, e, "foundation", b)
	if err != nil {
		return q, err
	}
	_, hasTransport, missingCA, err := readOpenBaoFirstRetainedTransport(ctx, cfg, store, e, b)
	if err != nil {
		return q, err
	}
	transport := "absent"
	if hasTransport {
		entry := createOpenBaoACKCompletionEntry(e.Journal, "openbao-transport-material")
		if entry == nil {
			return bad("transport receipt missing")
		}
		transport = entry.State
		if missingCA {
			transport += "-derived-missing"
		}
	}
	if foundation != nil && (!hasTransport || createOpenBaoACKCompletionEntry(e.Journal, "openbao-transport-material").State != "completed") {
		return bad("foundation lacks completed original transport")
	}
	openbao := "not-started"
	hasInit := false
	for _, entry := range e.Journal.Entries {
		switch entry.Key {
		case "openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config":
			hasInit = true
		}
	}
	if hasInit {
		if _, err := readOpenBaoFirstInitialized(ctx, cfg, store, e, b); err != nil {
			return q, err
		}
		openbao = "initialized"
	} else {
		for _, rel := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
			path, err := store.safePath(rel)
			if err != nil {
				return bad("original identity path unavailable")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return bad("unrecorded OpenBao initialization material")
			}
		}
		if foundation != nil && foundation.HasJournal && foundation.Journal.Accepted && foundation.ParentState == "completed" {
			ns := plan.Stack + "-secrets"
			id := "StatefulSet/" + ns + "/openbao"
			set, err := b.Read(ctx, id)
			if err != nil || set == nil || deploymentplan.Map(set["metadata"])["uid"] != foundation.Journal.Owners[id] {
				return bad("original OpenBao StatefulSet owner differs")
			}
			pod, err := b.Read(ctx, "Pod/"+ns+"/openbao-0")
			if err != nil || validateOpenBaoPodOwner(set, pod) != nil {
				return bad("original OpenBao Pod owner differs")
			}
			status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, b)
			if err != nil || status.Initialized || !status.Sealed {
				return bad("actual OpenBao is not verified uninitialized and sealed")
			}
			openbao = "ready-to-initialize"
		}
	}
	_, mgmtMissing, management, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, "management-transport-material", b)
	if err != nil {
		return q, err
	}
	_, signerMissing, signer, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, "account-signer-material", b)
	if err != nil {
		return q, err
	}
	if openbao != "initialized" && (management != "absent" || signer != "absent") {
		return bad("downstream material precedes verified initialized OpenBao")
	}
	if management == "absent" && signer != "absent" {
		return bad("Account signer precedes management transport")
	}
	services, err := inspectOpenBaoFirstRecordedStage(ctx, cfg, store, e, "foundation-services", b)
	if err != nil {
		return q, err
	}
	if services != nil && (openbao != "initialized" || management != "completed" || signer != "completed" || len(mgmtMissing) != 0 || len(signerMissing) != 0) {
		return bad("services stage lacks completed original dependencies")
	}
	if len(mgmtMissing) > 0 {
		management += "-derived-missing"
	}
	if len(signerMissing) > 0 {
		signer += "-derived-missing"
	}
	if err := e.Check(ctx); err != nil {
		return q, err
	}
	q = openBaoFirstRecoveryQualification{Schema: "rtk-cloud.openbao-first-source/v1", Environment: cfg.Environment, ParentSHA256: e.Journal.PlanSHA256, ParentJournalSHA256: deploymentplan.Digest(e.Journal), ParentOwnerSHA256: deploymentplan.Digest(e.owner), OperatorSHA256: deploymentplan.Digest(operator), Foundation: stageSummary(foundation), OpenBao: openbao, Transport: transport, Management: management, Signer: signer, Services: stageSummary(services)}
	return q, nil
}
