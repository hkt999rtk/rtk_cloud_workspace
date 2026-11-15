package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const custodyTokenAccessStage = "custody-token-runtime-access"

type custodyTokenRuntimeFact struct {
	Resource, PodUID, Container, Image, ImageID string
	UID, GID                                    int64
}
type custodyTokenAccessSource struct{ Stage, Plan, Journal string }
type custodyTokenAccessProof struct {
	Schema, Parent, Owner, Configuration string
	Sources                              []custodyTokenAccessSource
	Facts                                []custodyTokenRuntimeFact
	Plan                                 *deploymentplan.Plan
}

// A legacy 0440 projection without an explicit Pod owner cannot be read by an
// image-default non-root user. Add only that actual runtime's existing group;
// never guess a UID/GID, change the image, or rewrite the frozen session recipe.
func compileCustodyTokenAccess(sources []serviceSessionRecordedPlan, live []deploymentplan.Object) (*deploymentplan.Plan, []custodyTokenRuntimeFact, error) {
	bad := func() (*deploymentplan.Plan, []custodyTokenRuntimeFact, error) {
		return nil, nil, createProviderRecoveryBlock("custody token access requires the recorded workload UID and its exact native runtime identity")
	}
	inventory := map[string]deploymentplan.Object{}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if inventory[id] != nil {
			return bad()
		}
		inventory[id] = o
	}
	var defs []deploymentplan.Definition
	var facts []custodyTokenRuntimeFact
	var inputs deploymentplan.Inputs
	seen := map[string]bool{}
	for _, source := range sources {
		if source.Plan == nil || source.Journal.PlanSHA256 != source.Plan.SHA256() || source.Journal.InFlight != "" || source.SelectedIDs == nil {
			return bad()
		}
		for _, r := range source.Plan.Resources {
			if !source.SelectedIDs[r.ID] {
				continue
			}
			if seen[r.ID] {
				return bad()
			}
			seen[r.ID] = true
			actual := inventory[r.ID]
			if actual == nil || source.Journal.Owners[r.ID] == "" || deploymentplan.Map(actual["metadata"])["uid"] != source.Journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
				return bad()
			}
			if actual["kind"] != "Deployment" || deploymentplan.Map(actual["spec"])["replicas"] == float64(0) {
				continue
			}
			pod := environmentPKIPod(actual)
			if deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
				continue
			}
			auth := deploymentplan.Map(managedNamedField(pod["volumes"], "pki-custody-auth")["projected"])
			if auth["defaultMode"] != float64(0440) {
				continue
			}
			var selectedGroup int64 = -1
			for _, raw := range certIssuerObjectList(pod["containers"]) {
				c := deploymentplan.Map(raw)
				if mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" || managedNamedField(c["volumeMounts"], "pki-custody-auth") == nil {
					continue
				}
				// Explicit runAsUser lets kubelet choose native token ownership already.
				if deploymentplan.Map(pod["securityContext"])["runAsUser"] != nil || deploymentplan.Map(c["securityContext"])["runAsUser"] != nil {
					continue
				}
				found := false
				for _, candidate := range live {
					if candidate["kind"] != "Pod" || deploymentplan.Map(candidate["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(pod, deploymentplan.Map(candidate["spec"])) {
						continue
					}
					owner, err := environmentPodController(candidate, inventory)
					if err != nil || owner != r.ID {
						continue
					}
					actualContainer := managedNamedField(deploymentplan.Map(candidate["spec"])["containers"], deploymentplan.String(c["name"]))
					if actualContainer == nil || actualContainer["image"] != c["image"] {
						continue
					}
					status := managedNamedField(deploymentplan.Map(candidate["status"])["containerStatuses"], deploymentplan.String(c["name"]))
					user := deploymentplan.Map(deploymentplan.Map(status["user"])["linux"])
					uid, e1 := strconv.ParseInt(fmt.Sprint(user["uid"]), 10, 64)
					gid, e2 := strconv.ParseInt(fmt.Sprint(user["gid"]), 10, 64)
					if e1 != nil || e2 != nil || uid < 0 || gid < 0 || deploymentplan.String(status["imageID"]) == "" {
						return bad()
					}
					found = true
					if uid == 0 || gid == 0 {
						continue
					}
					rootGroup := false
					for _, g := range certIssuerObjectList(user["supplementalGroups"]) {
						rootGroup = rootGroup || fmt.Sprint(g) == "0"
					}
					if rootGroup {
						continue
					}
					if selectedGroup != -1 && selectedGroup != gid {
						return bad()
					}
					selectedGroup = gid
					facts = append(facts, custodyTokenRuntimeFact{Resource: r.ID, PodUID: deploymentplan.String(deploymentplan.Map(candidate["metadata"])["uid"]), Container: deploymentplan.String(c["name"]), Image: deploymentplan.String(c["image"]), ImageID: deploymentplan.String(status["imageID"]), UID: uid, GID: gid})
				}
				if !found {
					return bad()
				}
			}
			if selectedGroup < 0 {
				continue
			}
			desired := deploymentplan.Projection(actual)
			security := deploymentplan.Map(environmentPKIPod(desired)["securityContext"])
			if security == nil {
				security = deploymentplan.Object{}
				environmentPKIPod(desired)["securityContext"] = security
			}
			security["fsGroup"] = float64(selectedGroup)
			defs = append(defs, deploymentplan.Definition{Object: desired, Phase: "workload"})
			if inputs.Environment == "" {
				inputs = source.Plan.Inputs
			} else if inputs.Environment != source.Plan.Environment || inputs.Stack != source.Plan.Stack || inputs.ConfigurationSHA256 != source.Plan.ConfigurationSHA256 {
				return bad()
			}
		}
	}
	if len(defs) == 0 {
		return nil, nil, nil
	}
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Resource+facts[i].Container+facts[i].PodUID < facts[j].Resource+facts[j].Container+facts[j].PodUID
	})
	inputs.Operation = "scoped"
	inputs.BindingsSHA256 = deploymentplan.Digest(struct {
		Stage string
		Facts []custodyTokenRuntimeFact
		Body  string
	}{custodyTokenAccessStage, facts, environmentCreateStageBindingsSHA(custodyTokenAccessStage, defs)})
	p, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range p.Resources {
		if r.Action != "update" || r.UID == "" {
			return bad()
		}
	}
	return p, facts, nil
}

func tryUnifiedCustodyTokenAccess(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, parent *environmentCreatePlan, out, diagnostics io.Writer) (bool, error) {
	path := filepath.Join(e.Directory, "custody-token-access.json")
	var q custodyTokenAccessProof
	if _, err := os.Lstat(path); err == nil {
		if readExecutionJSON(path, &q) != nil || q.Schema != "rtk-cloud.custody-token-access/v1" || q.Parent != e.Journal.PlanSHA256 || q.Owner != deploymentplan.Digest(e.owner) || q.Configuration != deploymentplan.Digest(cfg) || q.Plan == nil {
			return true, createProviderRecoveryBlock("original custody token access qualification changed")
		}
		if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+custodyTokenAccessStage); entry != nil && entry.State == "completed" {
			if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, native, entry.Key); err != nil {
				return true, err
			}
			return tryUnifiedProductPublicCRLs(ctx, cfg, o, report, store, e, native, parent, out, diagnostics)
		}
	} else if !os.IsNotExist(err) {
		return true, err
	} else {
		live, err := native.Inventory(ctx, e.Journal.Stack)
		if err != nil {
			return true, err
		}
		history, _, err := serviceSessionOwnerHistory(ctx, store, e)
		if err != nil {
			return true, err
		}
		wanted := map[string]bool{}
		for _, source := range history {
			for _, r := range source.Plan.Resources {
				if r.Action == "retire" {
					delete(wanted, r.ID)
					continue
				}
				if source.Journal.Owners[r.ID] != "" && r.Desired["kind"] == "Deployment" && managedNamedField(environmentPKIPod(r.Desired)["volumes"], "pki-custody-auth") != nil {
					wanted[r.ID] = true
				}
			}
		}
		if len(wanted) == 0 {
			return false, nil
		}
		ids := make([]string, 0, len(wanted))
		for id := range wanted {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		sources, err := loadServiceSessionCoreSources(ctx, store, e, native, ids)
		if err != nil {
			return true, err
		}
		p, facts, err := compileCustodyTokenAccess(sources, live)
		if err != nil {
			return true, err
		}
		if p == nil {
			return false, nil
		}
		q = custodyTokenAccessProof{Schema: "rtk-cloud.custody-token-access/v1", Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Configuration: deploymentplan.Digest(cfg), Plan: p, Facts: facts}
		unique := map[string]custodyTokenAccessSource{}
		for _, s := range sources {
			unique[s.Stage] = custodyTokenAccessSource{s.Stage, s.Plan.SHA256(), deploymentplan.Digest(s.Journal)}
		}
		for _, s := range unique {
			q.Sources = append(q.Sources, s)
		}
		sort.Slice(q.Sources, func(i, j int) bool { return q.Sources[i].Stage < q.Sources[j].Stage })
		rel, err := filepath.Rel(store.Root, path)
		if err != nil {
			return true, err
		}
		if err := writeCreateWorkloadImageArtifact(store, rel, q); err != nil {
			return true, err
		}
	}
	if err := validateCustodyTokenAccessDelta(q); err != nil {
		return true, err
	}
	for _, s := range q.Sources {
		p, err := readEnvironmentCreateStageInput(store, e, s.Stage)
		if err != nil || p == nil || p.SHA256() != s.Plan {
			return true, createProviderRecoveryBlock("custody token original source input changed")
		}
		j, ok, err := readEnvironmentCreateStageProgress(store, e, s.Stage, p)
		if err != nil || !ok || deploymentplan.Digest(j) != s.Journal {
			return true, createProviderRecoveryBlock("custody token original source journal changed before access repair")
		}
	}
	if err := q.Plan.AdmitResumable(ctx, native, store.Root); err != nil {
		return true, err
	}
	deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
	if err != nil || !time.Now().Before(deadline) {
		return true, createProviderRecoveryBlock("original custody access execution deadline unavailable")
	}
	if o.Action == "create" {
		if o.Confirm != parent.Stack || o.PlanSHA256 != q.Plan.SHA256() {
			return true, createProviderRecoveryBlock("execute the exact preflight custody token access plan")
		}
		bounded, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if err := executeCompiledEnvironmentCreateStage(bounded, cfg, store, parent, e, custodyTokenAccessStage, q.Plan, native, out); err != nil {
			return true, err
		}
	} else if o.Action != "preflight" {
		return true, createProviderRecoveryBlock("custody token access requires original create")
	}
	report.Scope = "original-generation-custody-token-access"
	report.Decision = "GO"
	if o.Action == "create" {
		report.Decision, report.ReusedPreflight = "PASS", true
	}
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: q.Plan.SHA256()}
	for _, r := range q.Plan.Resources {
		report.Plan.Resources = append(report.Plan.Resources, deploymentplan.SummaryResource{ID: r.ID, Phase: r.Phase, Action: r.Action})
	}
	now := time.Now().UTC()
	report.QualifiedAt, report.EvidenceValidUntil, report.ExecutionDeadline = &now, &deadline, &deadline
	report.QualificationSHA256, report.Completed = deploymentplan.Digest(q), false
	report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.recovery.custody-token-access", Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest(q)})
	_, _ = io.WriteString(out, "Custody token access templates only; original session, deployment and full health remain incomplete.\n")
	return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

func validateCustodyTokenAccessDelta(q custodyTokenAccessProof) error {
	bad := func() error {
		return createProviderRecoveryBlock("custody token access may add only the recorded runtime group to original Deployment templates")
	}
	if q.Plan == nil || len(q.Plan.Resources) == 0 || len(q.Facts) == 0 {
		return bad()
	}
	for _, r := range q.Plan.Resources {
		if r.Action != "update" || r.UID == "" || r.Baseline == nil || r.Desired["kind"] != "Deployment" || deploymentplan.Map(environmentPKIPod(r.Baseline)["securityContext"])["fsGroup"] != nil {
			return bad()
		}
		group := deploymentplan.Map(environmentPKIPod(r.Desired)["securityContext"])["fsGroup"]
		matched := false
		for _, f := range q.Facts {
			if f.Resource == r.ID {
				if f.UID <= 0 || f.GID <= 0 || group != float64(f.GID) || !immutableEnvironmentImage.MatchString(f.Image) || f.PodUID == "" || f.Container == "" || f.ImageID == "" {
					return bad()
				}
				c := managedNamedField(environmentPKIPod(r.Baseline)["containers"], f.Container)
				if c == nil || c["image"] != f.Image {
					return bad()
				}
				matched = true
			}
		}
		if !matched {
			return bad()
		}
		unchanged := deploymentplan.Clone(r.Desired)
		security := deploymentplan.Map(environmentPKIPod(unchanged)["securityContext"])
		delete(security, "fsGroup")
		if len(security) == 0 && environmentPKIPod(r.Baseline)["securityContext"] == nil {
			delete(environmentPKIPod(unchanged), "securityContext")
		}
		if !deploymentplan.Matches(unchanged, r.Baseline) || !deploymentplan.Matches(r.Baseline, unchanged) {
			return bad()
		}
	}
	return nil
}
