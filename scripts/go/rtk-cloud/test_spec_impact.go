package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type specImpactChange struct {
	Kind          string `json:"kind"`
	FeatureID     string `json:"feature_id"`
	RequirementID string `json:"requirement_id"`
	WorkflowID    string `json:"workflow_id,omitempty"`
	BaseRevision  string `json:"base_revision,omitempty"`
	HeadRevision  string `json:"head_revision,omitempty"`
}

type specImpactReport struct {
	SchemaVersion string             `json:"schema_version"`
	Base          string             `json:"base"`
	Head          string             `json:"head"`
	Changes       []specImpactChange `json:"changes"`
}

func runTestSpecImpact(args []string) error {
	fs := flag.NewFlagSet("test-spec-impact", flag.ContinueOnError)
	base, head, outputDir := "", "HEAD", ".artifacts/spec-impact"
	fs.StringVar(&base, "base", base, "base workspace commit")
	fs.StringVar(&head, "head", head, "head workspace commit")
	fs.StringVar(&outputDir, "output-dir", outputDir, "impact report directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(base) == "" {
		return errors.New("test-spec-impact requires --base")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	baseInventory, baseErr := loadSpecInventoryAt(workspace, base)
	if baseErr != nil && !isMissingSpecRegistry(baseErr) {
		return baseErr
	}
	headInventory, err := loadSpecInventoryAt(workspace, head)
	if err != nil {
		return err
	}
	report := compareSpecInventories(base, head, baseInventory, headInventory)
	if err := writeSpecImpactReport(outputDir, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "spec impact: %d requirement/workflow changes\n", len(report.Changes))
	for _, change := range report.Changes {
		if change.Kind == "REMOVED" || change.Kind == "WORKFLOW_REMOVED" {
			if change.WorkflowID != "" {
				return fmt.Errorf("workflow %s was removed without a deprecated lifecycle record", change.WorkflowID)
			}
			return fmt.Errorf("requirement %s was removed without a deprecated lifecycle record", change.RequirementID)
		}
	}
	return nil
}

func loadSpecInventoryAt(workspace, ref string) (specInventory, error) {
	if ref == "HEAD" {
		return loadSpecInventory(workspace)
	}
	registryRaw, err := gitFileAt(workspace, ref, "tests/spec-sources.yaml")
	if err != nil {
		return specInventory{}, fmt.Errorf("read spec registry at %s: %w", ref, err)
	}
	registry, err := parseSpecSourceRegistry(registryRaw)
	if err != nil {
		return specInventory{}, err
	}
	return loadSpecInventoryWithReader(registry, func(path string) ([]byte, error) {
		return gitWorkspaceFileAt(workspace, ref, path)
	})
}

func gitWorkspaceFileAt(workspace, ref, path string) ([]byte, error) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		submodulePath := strings.Join(parts[:2], "/")
		relative := strings.Join(parts[2:], "/")
		tree, err := exec.Command("git", "-C", workspace, "ls-tree", ref, submodulePath).Output()
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(tree))
		if len(fields) < 3 || fields[1] != "commit" {
			return nil, fmt.Errorf("%s is not a submodule at %s", submodulePath, ref)
		}
		return gitFileAt(filepath.Join(workspace, filepath.FromSlash(submodulePath)), fields[2], relative)
	}
	return gitFileAt(workspace, ref, path)
}

func gitFileAt(repository, ref, path string) ([]byte, error) {
	cmd := exec.Command("git", "-C", repository, "show", ref+":"+filepath.ToSlash(path))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func isMissingSpecRegistry(err error) bool {
	text := err.Error()
	return strings.Contains(text, "does not exist") || strings.Contains(text, "exists on disk, but not in")
}

func compareSpecInventories(base, head string, before, after specInventory) specImpactReport {
	type indexedRequirement struct {
		FeatureID string
		Item      testCatalogRequirement
	}
	beforeIndex, afterIndex := map[string]indexedRequirement{}, map[string]indexedRequirement{}
	for _, feature := range before.Features {
		for _, requirement := range feature.Requirements {
			beforeIndex[requirement.ID] = indexedRequirement{FeatureID: feature.ID, Item: requirement}
		}
	}
	for _, feature := range after.Features {
		for _, requirement := range feature.Requirements {
			afterIndex[requirement.ID] = indexedRequirement{FeatureID: feature.ID, Item: requirement}
		}
	}
	report := specImpactReport{SchemaVersion: "rtk-cloud-spec-impact/v2", Base: base, Head: head}
	for id, current := range afterIndex {
		previous, exists := beforeIndex[id]
		kind := ""
		switch {
		case !exists:
			kind = "ADDED"
		case current.Item.Status == "deprecated" && previous.Item.Status != "deprecated":
			kind = "DEPRECATED"
		case current.Item.Revision != previous.Item.Revision:
			kind = "MODIFIED"
		}
		if kind != "" {
			report.Changes = append(report.Changes, specImpactChange{
				Kind: kind, FeatureID: current.FeatureID, RequirementID: id,
				BaseRevision: previous.Item.Revision, HeadRevision: current.Item.Revision,
			})
		}
	}
	for id, previous := range beforeIndex {
		if _, exists := afterIndex[id]; !exists {
			report.Changes = append(report.Changes, specImpactChange{
				Kind: "REMOVED", FeatureID: previous.FeatureID, RequirementID: id, BaseRevision: previous.Item.Revision,
			})
		}
	}
	beforeWorkflows, afterWorkflows := map[string]specWorkflow{}, map[string]specWorkflow{}
	for _, workflow := range before.Workflows {
		beforeWorkflows[workflow.ID] = workflow
	}
	for _, workflow := range after.Workflows {
		afterWorkflows[workflow.ID] = workflow
	}
	for id, current := range afterWorkflows {
		previous, exists := beforeWorkflows[id]
		kind := ""
		switch {
		case !exists:
			kind = "WORKFLOW_ADDED"
		case current.Revision != previous.Revision:
			kind = "WORKFLOW_MODIFIED"
		}
		if kind == "" {
			continue
		}
		for _, requirementID := range current.RequirementIDs {
			report.Changes = append(report.Changes, specImpactChange{
				Kind: kind, FeatureID: current.FeatureID, RequirementID: requirementID, WorkflowID: id,
				BaseRevision: previous.Revision, HeadRevision: current.Revision,
			})
		}
	}
	for id, previous := range beforeWorkflows {
		if _, exists := afterWorkflows[id]; exists {
			continue
		}
		for _, requirementID := range previous.RequirementIDs {
			report.Changes = append(report.Changes, specImpactChange{
				Kind: "WORKFLOW_REMOVED", FeatureID: previous.FeatureID, RequirementID: requirementID, WorkflowID: id,
				BaseRevision: previous.Revision,
			})
		}
	}
	sort.Slice(report.Changes, func(i, j int) bool {
		if report.Changes[i].FeatureID == report.Changes[j].FeatureID {
			if report.Changes[i].RequirementID == report.Changes[j].RequirementID {
				return report.Changes[i].WorkflowID < report.Changes[j].WorkflowID
			}
			return report.Changes[i].RequirementID < report.Changes[j].RequirementID
		}
		return report.Changes[i].FeatureID < report.Changes[j].FeatureID
	})
	return report
}

func writeSpecImpactReport(outputDir string, report specImpactReport) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "spec-impact.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "# Spec Impact")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Base: `%s`\n- Head: `%s`\n- Changes: **%d**\n\n", report.Base, report.Head, len(report.Changes))
	fmt.Fprintln(&b, "| Change | Feature | Requirement | Workflow | Base revision | Head revision |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- |")
	for _, change := range report.Changes {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` |\n", change.Kind, change.FeatureID, change.RequirementID, change.WorkflowID, shortDigest(change.BaseRevision), shortDigest(change.HeadRevision))
	}
	return os.WriteFile(filepath.Join(outputDir, "SPEC_IMPACT.md"), []byte(b.String()), 0o644)
}
