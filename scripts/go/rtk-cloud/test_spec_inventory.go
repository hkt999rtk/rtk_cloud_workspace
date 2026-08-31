package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const specInventorySchema = "rtk-cloud-spec-inventory/v3"

type specSourceRegistry struct {
	SchemaVersion int                      `yaml:"schema_version"`
	Sources       []specSourceRegistryItem `yaml:"sources"`
}

type specSourceRegistryItem struct {
	ID        string `yaml:"id" json:"id"`
	Path      string `yaml:"path" json:"path"`
	Parser    string `yaml:"parser" json:"parser"`
	Authority string `yaml:"authority" json:"authority"`
	Owner     string `yaml:"owner" json:"owner"`
}

type specDocumentMetadata struct {
	RTKSpec struct {
		ID                   string `yaml:"id"`
		Status               string `yaml:"status"`
		Owner                string `yaml:"owner"`
		RequirementInventory string `yaml:"requirement_inventory"`
	} `yaml:"rtk_spec"`
}

type specFeatureMetadata struct {
	Owner         string               `yaml:"owner"`
	Risk          string               `yaml:"risk"`
	Status        string               `yaml:"status"`
	ChangePaths   []string             `yaml:"change_paths"`
	CommitAnchors []string             `yaml:"commit_anchors"`
	Surfaces      []testCatalogSurface `yaml:"surfaces"`
}

type specRequirementMetadata struct {
	AcceptanceLayer     string   `yaml:"acceptance_layer"`
	OperationModel      string   `yaml:"operation_model" json:"operation_model,omitempty"`
	Gate                string   `yaml:"gate"`
	Environments        []string `yaml:"environments"`
	Targets             []string `yaml:"targets"`
	Evidence            []string `yaml:"evidence"`
	FreshnessHours      int      `yaml:"freshness_hours"`
	Required            *bool    `yaml:"required"`
	Status              string   `yaml:"status"`
	RenamedFromRevision string   `yaml:"renamed_from_revision,omitempty" json:"renamed_from_revision,omitempty"`
	DeprecatedBy        string   `yaml:"replacement,omitempty"`
	DeprecatedOwner     string   `yaml:"deprecation_owner,omitempty"`
	DeprecatedWhy       string   `yaml:"deprecation_reason,omitempty"`
	ApprovedAt          string   `yaml:"approved_at,omitempty"`
}

type specRequirementSource struct {
	DocumentID  string `json:"document_id,omitempty"`
	Path        string `json:"path,omitempty"`
	Section     string `json:"section,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Authority   string `json:"authority,omitempty"`
	Revision    string `json:"revision,omitempty"`
}

type specOpenAPIOperation struct {
	DocumentID     string   `json:"document_id"`
	SourcePath     string   `json:"source_path"`
	Path           string   `json:"path"`
	Method         string   `json:"method"`
	OperationID    string   `json:"operation_id"`
	FeatureID      string   `json:"feature_id,omitempty"`
	RequirementIDs []string `json:"requirement_ids,omitempty"`
	Revision       string   `json:"revision"`
}

type specLogicalOperation struct {
	ID             string   `yaml:"id"`
	FeatureID      string   `yaml:"feature_id"`
	RequirementIDs []string `yaml:"requirement_ids"`
}

type specLogicalOperationDocument struct {
	SchemaVersion int `yaml:"schema_version"`
	RTKSpec       struct {
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
		Owner  string `yaml:"owner"`
	} `yaml:"rtk_spec"`
	Operations []specLogicalOperation `yaml:"operations"`
}

type specWorkflowDependency struct {
	Step string `yaml:"step" json:"step"`
	Type string `yaml:"type" json:"type"`
}

type specWorkflowStateTransition struct {
	From string `yaml:"from" json:"from,omitempty"`
	To   string `yaml:"to" json:"to,omitempty"`
}

type specWorkflowStep struct {
	ID                      string                      `yaml:"id" json:"id"`
	OperationRef            string                      `yaml:"operation_ref" json:"operation_ref"`
	OperationFeatureID      string                      `yaml:"operation_feature_id" json:"operation_feature_id,omitempty"`
	OperationRequirementIDs []string                    `yaml:"operation_requirement_ids" json:"operation_requirement_ids,omitempty"`
	Dependencies            []specWorkflowDependency    `yaml:"depends_on" json:"depends_on,omitempty"`
	Consumes                []string                    `yaml:"consumes" json:"consumes,omitempty"`
	Produces                []string                    `yaml:"produces" json:"produces,omitempty"`
	StateTransition         specWorkflowStateTransition `yaml:"state_transition" json:"state_transition,omitempty"`
	Expected                string                      `yaml:"expected" json:"expected"`
	Always                  bool                        `yaml:"always" json:"always,omitempty"`
}

type specWorkflow struct {
	ID             string             `yaml:"id" json:"id"`
	Title          string             `yaml:"title" json:"title"`
	FeatureID      string             `yaml:"feature_id" json:"feature_id"`
	RequirementIDs []string           `yaml:"requirement_ids" json:"requirement_ids"`
	Inputs         []string           `yaml:"inputs" json:"inputs,omitempty"`
	Steps          []specWorkflowStep `yaml:"steps" json:"steps"`
	DocumentID     string             `json:"document_id"`
	SourcePath     string             `json:"source_path"`
	Authority      string             `json:"authority"`
	Status         string             `json:"status"`
	Revision       string             `json:"revision"`
}

type specWorkflowDocument struct {
	SchemaVersion int `yaml:"schema_version"`
	RTKSpec       struct {
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
		Owner  string `yaml:"owner"`
	} `yaml:"rtk_spec"`
	Workflows []specWorkflow `yaml:"workflows"`
}

type specInventoryFinding struct {
	Code       string `json:"code"`
	Source     string `json:"source"`
	Reference  string `json:"reference,omitempty"`
	Assessment string `json:"assessment"`
	Blocking   bool   `json:"blocking"`
}

// specRequirementCandidate is a normative statement that is present in a
// registered source document but has not yet been promoted to a stable REQ-*
// record. Candidates are migration gaps, not requirements invented by tests,
// and therefore never enter the feature-coverage denominator.
type specRequirementCandidate struct {
	DocumentID string `json:"document_id"`
	SourcePath string `json:"source_path"`
	Authority  string `json:"authority"`
	Section    string `json:"section,omitempty"`
	Line       int    `json:"line"`
	Statement  string `json:"statement"`
	Revision   string `json:"revision"`
	Status     string `json:"status"`
}

type specInventory struct {
	SchemaVersion string                     `json:"schema_version"`
	Features      []testCatalogFeature       `json:"-"`
	Operations    []specOpenAPIOperation     `json:"operations"`
	Workflows     []specWorkflow             `json:"workflows"`
	Candidates    []specRequirementCandidate `json:"candidates"`
	Findings      []specInventoryFinding     `json:"findings"`
	Sources       []specSourceRegistryItem   `json:"sources"`
}

var (
	specFeatureHeading     = regexp.MustCompile(`^(#{2,6}) \[(FEAT(?:-[A-Z0-9]+){2,3}-[0-9]{3})\] (.+?)\s*$`)
	specRequirementHeading = regexp.MustCompile(`^(#{2,6}) \[(REQ(?:-[A-Z0-9]+){2,4}-[0-9]{3})\] (.+?)\s*$`)
	openAPIMethods         = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "options": true, "head": true, "patch": true, "trace": true}
)

func runTestSpecInventory(args []string) error {
	action := "check"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	if action != "check" && action != "render" {
		return errors.New("usage: test-spec-inventory [check|render] [--mode observe|required] [--output-dir PATH]")
	}
	fs := flag.NewFlagSet("test-spec-inventory "+action, flag.ContinueOnError)
	mode, outputDir := "observe", ".artifacts/spec-inventory"
	fs.StringVar(&mode, "mode", mode, "qualification mode: observe or required")
	fs.StringVar(&outputDir, "output-dir", outputDir, "inventory report directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if mode != "observe" && mode != "required" {
		return fmt.Errorf("unsupported spec inventory mode %q", mode)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	cases, err := loadSpecTraceabilityCases(workspace)
	if err != nil {
		return err
	}
	if err := writeSpecInventoryReport(outputDir, inventory, cases); err != nil {
		return err
	}
	traceability := renderSpecInventoryReport(inventory, cases)
	docPath := filepath.Join(workspace, "docs", "spec-test-traceability.md")
	if action == "render" {
		if err := os.WriteFile(docPath, traceability, 0o644); err != nil {
			return err
		}
	} else if committed, readErr := os.ReadFile(docPath); readErr != nil || !bytes.Equal(committed, traceability) {
		return errors.New("docs/spec-test-traceability.md is stale; run rtk-cloud test-spec-inventory render")
	}
	blocking := 0
	for _, finding := range inventory.Findings {
		if finding.Blocking {
			blocking++
		}
	}
	fmt.Fprintf(os.Stdout, "spec inventory: %d features, %d requirements, %d operations, %d workflows, %d unspecified normative candidates, %d blocking findings\n",
		len(inventory.Features), countSpecRequirements(inventory.Features), len(inventory.Operations), len(inventory.Workflows), len(inventory.Candidates), blocking)
	if action == "check" && mode == "required" && blocking > 0 {
		return fmt.Errorf("spec inventory has %d blocking findings", blocking)
	}
	return nil
}

func loadSpecSourceRegistry(workspace string) (specSourceRegistry, error) {
	path := filepath.Join(workspace, "tests", "spec-sources.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return specSourceRegistry{}, fmt.Errorf("read spec source registry: %w", err)
	}
	return parseSpecSourceRegistry(raw)
}

func parseSpecSourceRegistry(raw []byte) (specSourceRegistry, error) {
	var registry specSourceRegistry
	if err := yaml.Unmarshal(raw, &registry); err != nil {
		return specSourceRegistry{}, fmt.Errorf("parse spec source registry: %w", err)
	}
	if registry.SchemaVersion != 1 || len(registry.Sources) == 0 {
		return specSourceRegistry{}, errors.New("spec source registry requires schema_version 1 and at least one source")
	}
	seenID, seenPath := map[string]bool{}, map[string]bool{}
	authorities := map[string]bool{"canonical": true, "service": true, "approved": true, "draft": true, "proposed": true, "review_required": true}
	for i, source := range registry.Sources {
		if source.ID == "" || source.Path == "" || source.Owner == "" || !catalogOwners[source.Owner] {
			return specSourceRegistry{}, fmt.Errorf("spec sources[%d] requires id, path, and known owner", i)
		}
		if seenID[source.ID] || seenPath[source.Path] {
			return specSourceRegistry{}, fmt.Errorf("duplicate spec source id or path at sources[%d]", i)
		}
		seenID[source.ID], seenPath[source.Path] = true, true
		if source.Parser != "markdown" && source.Parser != "openapi" && source.Parser != "operations" && source.Parser != "workflow" {
			return specSourceRegistry{}, fmt.Errorf("spec source %s has unsupported parser %q", source.ID, source.Parser)
		}
		if !authorities[source.Authority] {
			return specSourceRegistry{}, fmt.Errorf("spec source %s has unsupported authority %q", source.ID, source.Authority)
		}
		if filepath.IsAbs(source.Path) || strings.Contains(source.Path, "..") || strings.Contains(source.Path, `\`) {
			return specSourceRegistry{}, fmt.Errorf("spec source %s path must be workspace-relative", source.ID)
		}
	}
	return registry, nil
}

func loadSpecInventory(workspace string) (specInventory, error) {
	registry, err := loadSpecSourceRegistry(workspace)
	if err != nil {
		return specInventory{}, err
	}
	return loadSpecInventoryWithReader(registry, func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
	})
}

// loadAvailableSpecInventory supports runner-specific partial checkouts. The
// registry remains the allow-list, but a test-ui/test-e2e runner only needs the
// spec repositories present in that job. Central inventory commands always use
// loadSpecInventory and therefore still require every registered source.
func loadAvailableSpecInventory(workspace string) (specInventory, error) {
	registry, err := loadSpecSourceRegistry(workspace)
	if err != nil {
		return specInventory{}, err
	}
	available := registry
	available.Sources = nil
	for _, source := range registry.Sources {
		if _, statErr := os.Stat(filepath.Join(workspace, filepath.FromSlash(source.Path))); statErr == nil {
			available.Sources = append(available.Sources, source)
		} else if !os.IsNotExist(statErr) {
			return specInventory{}, fmt.Errorf("inspect spec source %s: %w", source.Path, statErr)
		}
	}
	if len(available.Sources) == 0 {
		return specInventory{}, errors.New("runner checkout contains no registered spec source")
	}
	readFile := func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
	}
	// Availability controls which documents contribute inventory, not which
	// registered documents their references may resolve. Partial runners can
	// use a validated consumer checkout of the canonical contract for references.
	resolvePath := newOpenAPIPathResolver(registry, newRunnerOpenAPIReader(registry, available, readFile))
	return loadSpecInventoryWithResolver(available, readFile, resolvePath)
}

func loadSpecInventoryWithReader(registry specSourceRegistry, readFile func(string) ([]byte, error)) (specInventory, error) {
	return loadSpecInventoryWithResolver(registry, readFile, newOpenAPIPathResolver(registry, readFile))
}

func loadSpecInventoryWithResolver(registry specSourceRegistry, readFile func(string) ([]byte, error), resolvePath openAPIPathResolver) (specInventory, error) {
	inventory := specInventory{SchemaVersion: specInventorySchema, Sources: registry.Sources}
	featureIndex, requirementIndex := map[string]int{}, map[string]string{}
	type pendingWorkflowSource struct {
		source specSourceRegistryItem
		raw    []byte
	}
	var workflowSources []pendingWorkflowSource
	for _, source := range registry.Sources {
		raw, readErr := readFile(source.Path)
		if readErr != nil {
			return specInventory{}, fmt.Errorf("read spec source %s: %w", source.Path, readErr)
		}
		if source.Parser == "workflow" {
			workflowSources = append(workflowSources, pendingWorkflowSource{source: source, raw: raw})
			continue
		}
		if source.Parser == "markdown" {
			features, findings, parseErr := parseMarkdownSpec(source, raw)
			if parseErr != nil {
				return specInventory{}, parseErr
			}
			candidates := scanMarkdownRequirementCandidates(source, raw)
			inventory.Candidates = append(inventory.Candidates, candidates...)
			for _, candidate := range candidates {
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code:       "UNSPECIFIED_NORMATIVE_CLAUSE",
					Source:     candidate.SourcePath,
					Reference:  fmt.Sprintf("%s@L%d", candidate.Section, candidate.Line),
					Assessment: fmt.Sprintf("normative clause %s requires a stable FEAT-*/REQ-* source mapping: %s", shortDigest(candidate.Revision), candidate.Statement),
					Blocking:   candidate.Status == "required",
				})
			}
			inventory.Findings = append(inventory.Findings, findings...)
			for _, feature := range features {
				if existing, ok := featureIndex[feature.ID]; ok {
					inventory.Findings = append(inventory.Findings, specInventoryFinding{
						Code: "SPEC_DRIFT", Source: source.Path, Reference: feature.ID, Blocking: true,
						Assessment: fmt.Sprintf("feature is defined by both %s and %s", inventory.Features[existing].SpecSource.Path, source.Path),
					})
					continue
				}
				featureIndex[feature.ID] = len(inventory.Features)
				for _, requirement := range feature.Requirements {
					if prior := requirementIndex[requirement.ID]; prior != "" {
						inventory.Findings = append(inventory.Findings, specInventoryFinding{
							Code: "DUPLICATE_REQUIREMENT", Source: source.Path, Reference: requirement.ID, Blocking: true,
							Assessment: fmt.Sprintf("requirement is also defined by %s", prior),
						})
					}
					requirementIndex[requirement.ID] = source.Path
				}
				inventory.Features = append(inventory.Features, feature)
			}
			continue
		}
		if source.Parser == "operations" {
			operations, findings, parseErr := parseLogicalOperationSpec(source, raw)
			if parseErr != nil {
				return specInventory{}, parseErr
			}
			inventory.Operations = append(inventory.Operations, operations...)
			inventory.Findings = append(inventory.Findings, findings...)
			continue
		}
		operations, findings, parseErr := parseOpenAPISpecWithResolver(source, raw, resolvePath)
		if parseErr != nil {
			return specInventory{}, parseErr
		}
		inventory.Operations = append(inventory.Operations, operations...)
		inventory.Findings = append(inventory.Findings, findings...)
	}
	featureSet := map[string]bool{}
	requirementSet := map[string]bool{}
	requirementFeature := map[string]string{}
	for _, feature := range inventory.Features {
		featureSet[feature.ID] = true
		for _, requirement := range feature.Requirements {
			requirementSet[requirement.ID] = true
			requirementFeature[requirement.ID] = feature.ID
		}
	}
	operationRefs := map[string]string{}
	for _, operation := range inventory.Operations {
		operationRef := operation.DocumentID + "#" + operation.OperationID
		if operationRef != operation.DocumentID+"#" {
			if prior, exists := operationRefs[operationRef]; exists {
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "DUPLICATE_OPERATION_REF", Source: operation.SourcePath, Reference: operationRef, Blocking: true,
					Assessment: "operation reference is also defined by " + prior,
				})
			} else {
				operationRefs[operationRef] = operation.SourcePath
			}
		}
		if operation.FeatureID != "" && !featureSet[operation.FeatureID] {
			inventory.Findings = append(inventory.Findings, specInventoryFinding{
				Code: "UNKNOWN_OPERATION_FEATURE", Source: operation.SourcePath, Reference: operation.OperationID, Blocking: true,
				Assessment: "OpenAPI operation references an unknown spec feature",
			})
		}
		for _, requirementID := range operation.RequirementIDs {
			if !requirementSet[requirementID] {
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "UNKNOWN_OPERATION_REQUIREMENT", Source: operation.SourcePath, Reference: operation.OperationID, Blocking: true,
					Assessment: "OpenAPI operation references an unknown spec requirement " + requirementID,
				})
			} else if operation.FeatureID != "" && requirementFeature[requirementID] != operation.FeatureID {
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "OPERATION_REQUIREMENT_FEATURE_MISMATCH", Source: operation.SourcePath, Reference: operation.OperationID, Blocking: true,
					Assessment: fmt.Sprintf("requirement %s belongs to %s, not operation feature %s", requirementID, requirementFeature[requirementID], operation.FeatureID),
				})
			}
		}
	}
	workflowRefs := map[string]string{}
	for _, pending := range workflowSources {
		workflows, findings, parseErr := parseWorkflowSpec(pending.source, pending.raw, inventory.Features, inventory.Operations)
		if parseErr != nil {
			return specInventory{}, parseErr
		}
		for _, workflow := range workflows {
			if prior := workflowRefs[workflow.ID]; prior != "" {
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "DUPLICATE_WORKFLOW", Source: pending.source.Path, Reference: workflow.ID, Blocking: true,
					Assessment: "workflow is also defined by " + prior,
				})
				continue
			}
			workflowRefs[workflow.ID] = pending.source.Path
			inventory.Workflows = append(inventory.Workflows, workflow)
		}
		inventory.Findings = append(inventory.Findings, findings...)
	}
	operationCounts := map[string]int{}
	for _, operation := range inventory.Operations {
		for _, requirementID := range operation.RequirementIDs {
			operationCounts[requirementID]++
		}
	}
	workflowCounts := map[string]int{}
	for _, workflow := range inventory.Workflows {
		for _, requirementID := range workflow.RequirementIDs {
			workflowCounts[requirementID]++
		}
	}
	for _, feature := range inventory.Features {
		for _, requirement := range feature.Requirements {
			switch {
			case requirement.OperationModel == "" && operationCounts[requirement.ID] > 1:
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "OPERATION_DEPENDENCY_REVIEW_REQUIRED", Source: requirement.SpecSource.Path, Reference: requirement.ID, Blocking: true,
					Assessment: fmt.Sprintf("%d operations map to this requirement; the source spec must declare operation_model: independent or provide operation_model: workflow with a canonical DAG", operationCounts[requirement.ID]),
				})
			case requirement.OperationModel == "workflow" && workflowCounts[requirement.ID] == 0:
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "MISSING_OPERATION_WORKFLOW", Source: requirement.SpecSource.Path, Reference: requirement.ID, Blocking: true,
					Assessment: "requirement declares operation_model: workflow but no canonical workflow binds it",
				})
			case requirement.OperationModel == "independent" && workflowCounts[requirement.ID] > 0:
				inventory.Findings = append(inventory.Findings, specInventoryFinding{
					Code: "OPERATION_MODEL_WORKFLOW_CONFLICT", Source: requirement.SpecSource.Path, Reference: requirement.ID, Blocking: true,
					Assessment: "requirement declares independent operations but is bound to a canonical workflow",
				})
			}
		}
	}
	sort.Slice(inventory.Features, func(i, j int) bool { return inventory.Features[i].ID < inventory.Features[j].ID })
	sort.Slice(inventory.Workflows, func(i, j int) bool { return inventory.Workflows[i].ID < inventory.Workflows[j].ID })
	sort.Slice(inventory.Candidates, func(i, j int) bool {
		if inventory.Candidates[i].SourcePath == inventory.Candidates[j].SourcePath {
			if inventory.Candidates[i].Line == inventory.Candidates[j].Line {
				return inventory.Candidates[i].Revision < inventory.Candidates[j].Revision
			}
			return inventory.Candidates[i].Line < inventory.Candidates[j].Line
		}
		return inventory.Candidates[i].SourcePath < inventory.Candidates[j].SourcePath
	})
	sort.Slice(inventory.Findings, func(i, j int) bool {
		if inventory.Findings[i].Source == inventory.Findings[j].Source {
			return inventory.Findings[i].Reference < inventory.Findings[j].Reference
		}
		return inventory.Findings[i].Source < inventory.Findings[j].Source
	})
	return inventory, nil
}

func parseMarkdownSpec(source specSourceRegistryItem, raw []byte) ([]testCatalogFeature, []specInventoryFinding, error) {
	var findings []specInventoryFinding
	metadata, hasMetadata := parseSpecFrontMatter(raw)
	documentRequired := sourceAuthorityRequired(source.Authority)
	if !hasMetadata {
		findings = append(findings, specInventoryFinding{
			Code: "MISSING_DOCUMENT_METADATA", Source: source.Path, Blocking: sourceAuthorityRequired(source.Authority),
			Assessment: "registered Markdown source has no rtk_spec front matter",
		})
	} else if metadata.RTKSpec.ID != source.ID || (metadata.RTKSpec.Owner != "" && metadata.RTKSpec.Owner != source.Owner) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_METADATA_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: "rtk_spec id/owner does not match spec-sources.yaml",
		})
	} else if strings.TrimSpace(metadata.RTKSpec.Status) == "" {
		documentRequired = false
		findings = append(findings, specInventoryFinding{
			Code: "REVIEW_REQUIRED", Source: source.Path, Blocking: false,
			Assessment: "document has no explicit status and is excluded from the required denominator",
		})
	} else if !specDocumentStatusMatchesAuthority(metadata.RTKSpec.Status, source.Authority) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_STATUS_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: fmt.Sprintf("document status %q does not match registry authority %q", metadata.RTKSpec.Status, source.Authority),
		})
	}
	if hasMetadata {
		switch metadata.RTKSpec.RequirementInventory {
		case "complete":
		case "", "review_required":
			findings = append(findings, specInventoryFinding{
				Code: "REQUIREMENT_INVENTORY_REVIEW_REQUIRED", Source: source.Path, Blocking: documentRequired,
				Assessment: "source owner has not confirmed that every normative clause has a stable FEAT-*/REQ-* mapping",
			})
		default:
			findings = append(findings, specInventoryFinding{
				Code: "INVALID_REQUIREMENT_INVENTORY_STATUS", Source: source.Path, Blocking: true,
				Assessment: "rtk_spec.requirement_inventory must be complete or review_required",
			})
		}
	}
	if hasMetadata && !specDocumentStatusRequired(metadata.RTKSpec.Status) {
		documentRequired = false
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var features []testCatalogFeature
	var current *testCatalogFeature
	for i := 0; i < len(lines); i++ {
		if match := specFeatureHeading.FindStringSubmatch(lines[i]); match != nil {
			comment, next, ok := readSpecMetadataComment(lines, i+1, "rtk-feature")
			if !ok {
				return nil, findings, fmt.Errorf("%s:%d feature %s is missing rtk-feature metadata", source.Path, i+1, match[2])
			}
			var featureMetadata specFeatureMetadata
			if err := yaml.Unmarshal([]byte(comment), &featureMetadata); err != nil {
				return nil, findings, fmt.Errorf("%s:%d parse feature metadata: %w", source.Path, i+1, err)
			}
			status := featureMetadata.Status
			if status == "" {
				status = "active"
			}
			feature := testCatalogFeature{
				ID: match[2], Title: strings.TrimSpace(match[3]), Owner: featureMetadata.Owner, Risk: featureMetadata.Risk,
				Status: status, ChangePaths: featureMetadata.ChangePaths, CommitAnchors: featureMetadata.CommitAnchors,
				Surfaces:   featureMetadata.Surfaces,
				SpecSource: specRequirementSource{DocumentID: source.ID, Path: source.Path, Section: match[2], Authority: source.Authority},
			}
			features = append(features, feature)
			current = &features[len(features)-1]
			i = next - 1
			continue
		}
		if match := specRequirementHeading.FindStringSubmatch(lines[i]); match != nil {
			if current == nil {
				return nil, findings, fmt.Errorf("%s:%d requirement %s has no preceding feature heading", source.Path, i+1, match[2])
			}
			comment, next, ok := readSpecMetadataComment(lines, i+1, "rtk-requirement")
			if !ok {
				return nil, findings, fmt.Errorf("%s:%d requirement %s is missing rtk-requirement metadata", source.Path, i+1, match[2])
			}
			var requirementMetadata specRequirementMetadata
			if err := yaml.Unmarshal([]byte(comment), &requirementMetadata); err != nil {
				return nil, findings, fmt.Errorf("%s:%d parse requirement metadata: %w", source.Path, i+1, err)
			}
			status := requirementMetadata.Status
			if status == "" {
				status = "active"
			}
			if !documentRequired && status == "active" {
				if hasMetadata && strings.TrimSpace(metadata.RTKSpec.Status) == "" {
					status = "review_required"
				} else {
					status = "planned"
				}
			}
			if status == "deprecated" &&
				(strings.TrimSpace(requirementMetadata.DeprecatedOwner) == "" ||
					strings.TrimSpace(requirementMetadata.DeprecatedWhy) == "" ||
					strings.TrimSpace(requirementMetadata.ApprovedAt) == "") {
				return nil, findings, fmt.Errorf("%s:%d deprecated requirement %s requires deprecation_owner, deprecation_reason, and approved_at", source.Path, i+1, match[2])
			}
			if requirementMetadata.RenamedFromRevision != "" &&
				!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(requirementMetadata.RenamedFromRevision) {
				return nil, findings, fmt.Errorf("%s:%d requirement %s renamed_from_revision must be a SHA-256 digest", source.Path, i+1, match[2])
			}
			if requirementMetadata.OperationModel != "" &&
				requirementMetadata.OperationModel != "independent" &&
				requirementMetadata.OperationModel != "workflow" {
				return nil, findings, fmt.Errorf("%s:%d requirement %s operation_model must be independent or workflow", source.Path, i+1, match[2])
			}
			end := next
			requirementLevel := len(match[1])
			headingPattern := regexp.MustCompile(`^(#{1,6})\s+`)
			for end < len(lines) {
				heading := headingPattern.FindStringSubmatch(lines[end])
				if heading != nil && len(heading[1]) <= requirementLevel {
					break
				}
				end++
			}
			revision := specRequirementRevision(match[3], requirementMetadata, strings.Join(lines[next:end], "\n"))
			current.Requirements = append(current.Requirements, testCatalogRequirement{
				ID: match[2], Title: strings.TrimSpace(match[3]), AcceptanceLayer: requirementMetadata.AcceptanceLayer,
				OperationModel: requirementMetadata.OperationModel,
				Gate:           requirementMetadata.Gate, Environments: requirementMetadata.Environments, Targets: requirementMetadata.Targets,
				Evidence: requirementMetadata.Evidence, FreshnessHours: requirementMetadata.FreshnessHours,
				Required: requirementMetadata.Required, Status: status,
				RenamedFromRevision: requirementMetadata.RenamedFromRevision, Revision: revision,
				SpecSource: specRequirementSource{
					DocumentID: source.ID, Path: source.Path, Section: match[2], Authority: source.Authority, Revision: revision,
				},
			})
			i = next - 1
		}
	}
	return features, findings, nil
}

var normativeClausePattern = regexp.MustCompile(`(?i)\b(must|shall|should|required)\b|必須|不得|應當|應該`)

func scanMarkdownRequirementCandidates(source specSourceRegistryItem, raw []byte) []specRequirementCandidate {
	metadata, hasMetadata := parseSpecFrontMatter(raw)
	required := sourceAuthorityRequired(source.Authority)
	if hasMetadata {
		required = specDocumentStatusRequired(metadata.RTKSpec.Status)
	}
	status := "planned"
	if required {
		status = "required"
	}

	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	headingPattern := regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	inFence, inComment := false, false
	requirementLevel := 0
	section := ""
	seen := map[string]bool{}
	var candidates []specRequirementCandidate
	inFrontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFrontMatter {
			if i > 0 && trimmed == "---" {
				inFrontMatter = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			if !strings.Contains(trimmed, "-->") {
				inComment = true
			}
			continue
		}
		if heading := headingPattern.FindStringSubmatch(line); heading != nil {
			level := len(heading[1])
			if requirementLevel > 0 && level <= requirementLevel {
				requirementLevel = 0
			}
			section = strings.TrimSpace(heading[2])
			if specRequirementHeading.FindStringSubmatch(line) != nil {
				requirementLevel = level
			}
			continue
		}
		if requirementLevel > 0 || !normativeClausePattern.MatchString(trimmed) {
			continue
		}
		if markdownTableHeader(lines, i) {
			continue
		}
		statement := normalizeNormativeStatement(trimmed)
		if statement == "" {
			continue
		}
		sum := sha256.Sum256([]byte(source.ID + "\n" + section + "\n" + statement))
		revision := hex.EncodeToString(sum[:])
		if seen[revision] {
			continue
		}
		seen[revision] = true
		candidates = append(candidates, specRequirementCandidate{
			DocumentID: source.ID,
			SourcePath: source.Path,
			Authority:  source.Authority,
			Section:    section,
			Line:       i + 1,
			Statement:  statement,
			Revision:   revision,
			Status:     status,
		})
	}
	return candidates
}

func markdownTableHeader(lines []string, index int) bool {
	if index < 0 || index+1 >= len(lines) {
		return false
	}
	header := strings.TrimSpace(lines[index])
	separator := strings.TrimSpace(lines[index+1])
	if !strings.HasPrefix(header, "|") || !strings.HasSuffix(header, "|") ||
		!strings.HasPrefix(separator, "|") || !strings.HasSuffix(separator, "|") {
		return false
	}
	cells := strings.Split(strings.Trim(separator, "|"), "|")
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.TrimPrefix(cell, ":")
		cell = strings.TrimSuffix(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func normalizeNormativeStatement(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`).ReplaceAllString(value, "")
	value = regexp.MustCompile(`\s+`).ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func parseSpecFrontMatter(raw []byte) (specDocumentMetadata, bool) {
	var metadata specDocumentMetadata
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return metadata, false
	}
	rest := raw[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return metadata, false
	}
	if err := yaml.Unmarshal(rest[:end], &metadata); err != nil || metadata.RTKSpec.ID == "" {
		return specDocumentMetadata{}, false
	}
	return metadata, true
}

func readSpecMetadataComment(lines []string, start int, kind string) (string, int, bool) {
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	open := "<!-- " + kind
	if start >= len(lines) || strings.TrimSpace(lines[start]) != open {
		return "", start, false
	}
	var body []string
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "-->" {
			return strings.Join(body, "\n"), i + 1, true
		}
		body = append(body, lines[i])
	}
	return "", start, false
}

func specRequirementRevision(title string, metadata specRequirementMetadata, body string) string {
	body = regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(body), " ")
	payload := struct {
		Title    string                  `json:"title"`
		Metadata specRequirementMetadata `json:"metadata"`
		Body     string                  `json:"body"`
	}{Title: strings.TrimSpace(title), Metadata: metadata, Body: body}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func parseOpenAPISpec(source specSourceRegistryItem, raw []byte) ([]specOpenAPIOperation, []specInventoryFinding, error) {
	return parseOpenAPISpecWithResolver(source, raw, nil)
}

func parseOpenAPISpecWithResolver(source specSourceRegistryItem, raw []byte, resolve openAPIPathResolver) ([]specOpenAPIOperation, []specInventoryFinding, error) {
	// Parse YAML structure rather than indentation/line syntax. Inline lists,
	// quoted paths and extension examples must not change the denominator.
	var document struct {
		Metadata struct {
			ID     string `yaml:"id"`
			Status string `yaml:"status"`
		} `yaml:"x-rtk-spec"`
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, nil, fmt.Errorf("parse OpenAPI inventory %s: %w", source.Path, err)
	}
	var findings []specInventoryFinding
	if document.Metadata.ID != source.ID {
		findings = append(findings, specInventoryFinding{
			Code: "MISSING_DOCUMENT_METADATA", Source: source.Path, Blocking: sourceAuthorityRequired(source.Authority),
			Assessment: "registered OpenAPI source has no matching x-rtk-spec metadata",
		})
	}
	if document.Metadata.Status == "" {
		findings = append(findings, specInventoryFinding{
			Code: "REVIEW_REQUIRED", Source: source.Path,
			Assessment: "OpenAPI document has no explicit status",
		})
	} else if !specDocumentStatusMatchesAuthority(document.Metadata.Status, source.Authority) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_STATUS_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: fmt.Sprintf("OpenAPI status %q does not match registry authority %q", document.Metadata.Status, source.Authority),
		})
	}
	var operations []specOpenAPIOperation
	for route, pathNode := range document.Paths {
		if !strings.HasPrefix(route, "/") {
			continue
		}
		var pathItem map[string]yaml.Node
		if err := pathNode.Decode(&pathItem); err != nil {
			return nil, findings, fmt.Errorf("%s path %s: %w", source.Path, route, err)
		}
		if ref, referenced := pathItem["$ref"]; referenced {
			if resolve == nil {
				findings = append(findings, specInventoryFinding{
					Code: "UNRESOLVED_PATH_ITEM", Source: source.Path, Reference: route, Blocking: true,
					Assessment: "referenced path items must be resolved before inventory can establish their operations",
				})
			} else {
				inherited, err := resolve(source.Path, ref.Value)
				if err != nil {
					return nil, findings, fmt.Errorf("%s path %s reference: %w", source.Path, route, err)
				}
				// OpenAPI does not define conflicting local/inherited fields. Refuse
				// to guess which operation or parameters should control the inventory.
				for key, node := range inherited {
					if _, duplicate := pathItem[key]; duplicate {
						return nil, findings, fmt.Errorf("%s path %s duplicates referenced field %s", source.Path, route, key)
					}
					pathItem[key] = node
				}
			}
		}
		for method, node := range pathItem {
			if !openAPIMethods[method] {
				continue
			}
			var metadata struct {
				OperationID    string   `yaml:"operationId"`
				FeatureID      string   `yaml:"x-rtk-feature-id"`
				RequirementIDs []string `yaml:"x-rtk-requirement-ids"`
			}
			if err := node.Decode(&metadata); err != nil {
				return nil, findings, fmt.Errorf("%s %s %s operation metadata: %w", source.Path, method, route, err)
			}
			// Preserve the established digest representation {method: operation}.
			var operation any
			if err := node.Decode(&operation); err != nil {
				return nil, findings, fmt.Errorf("%s %s %s operation: %w", source.Path, method, route, err)
			}
			canonical, err := json.Marshal(map[string]any{method: operation})
			if err != nil {
				return nil, findings, fmt.Errorf("canonicalize operation revision: %w", err)
			}
			sum := sha256.Sum256(canonical)
			item := specOpenAPIOperation{
				DocumentID: source.ID, SourcePath: source.Path, Path: route, Method: strings.ToUpper(method),
				OperationID: metadata.OperationID, FeatureID: metadata.FeatureID,
				RequirementIDs: metadata.RequirementIDs, Revision: hex.EncodeToString(sum[:]),
			}
			operations = append(operations, item)
			if item.OperationID == "" {
				findings = append(findings, specInventoryFinding{
					Code: "MISSING_OPERATION_ID", Source: source.Path, Reference: strings.ToUpper(method) + " " + route,
					Blocking: true, Assessment: "public OpenAPI operation has no operationId",
				})
			} else if item.FeatureID == "" || len(item.RequirementIDs) == 0 {
				findings = append(findings, specInventoryFinding{
					Code: "UNMAPPED_OPERATION", Source: source.Path, Reference: item.OperationID, Blocking: true,
					Assessment: "public OpenAPI operation lacks x-rtk-feature-id or x-rtk-requirement-ids",
				})
			}
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].SourcePath == operations[j].SourcePath && operations[i].Path == operations[j].Path {
			return operations[i].OperationID < operations[j].OperationID
		}
		if operations[i].SourcePath == operations[j].SourcePath {
			return operations[i].Path < operations[j].Path
		}
		return operations[i].SourcePath < operations[j].SourcePath
	})
	return operations, findings, nil
}

func parseLogicalOperationSpec(source specSourceRegistryItem, raw []byte) ([]specOpenAPIOperation, []specInventoryFinding, error) {
	var document specLogicalOperationDocument
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, nil, fmt.Errorf("parse logical operation spec %s: %w", source.Path, err)
	}
	var findings []specInventoryFinding
	if document.SchemaVersion != 1 {
		findings = append(findings, specInventoryFinding{
			Code: "INVALID_OPERATION_SCHEMA", Source: source.Path, Blocking: true,
			Assessment: "logical operation spec requires schema_version 1",
		})
	}
	if document.RTKSpec.ID != source.ID || (document.RTKSpec.Owner != "" && document.RTKSpec.Owner != source.Owner) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_METADATA_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: "rtk_spec id/owner does not match spec-sources.yaml",
		})
	}
	if !specDocumentStatusMatchesAuthority(document.RTKSpec.Status, source.Authority) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_STATUS_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: fmt.Sprintf("operation status %q does not match registry authority %q", document.RTKSpec.Status, source.Authority),
		})
	}
	idPattern := regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	seen := map[string]bool{}
	operations := make([]specOpenAPIOperation, 0, len(document.Operations))
	for _, logical := range document.Operations {
		if !idPattern.MatchString(logical.ID) {
			findings = append(findings, specInventoryFinding{
				Code: "INVALID_OPERATION_ID", Source: source.Path, Reference: logical.ID, Blocking: true,
				Assessment: "logical operation IDs must be stable lower_snake_case identifiers",
			})
		}
		if seen[logical.ID] {
			findings = append(findings, specInventoryFinding{
				Code: "DUPLICATE_OPERATION_REF", Source: source.Path, Reference: source.ID + "#" + logical.ID, Blocking: true,
				Assessment: "logical operation ID is repeated",
			})
		}
		seen[logical.ID] = true
		if logical.FeatureID == "" || len(logical.RequirementIDs) == 0 {
			findings = append(findings, specInventoryFinding{
				Code: "UNMAPPED_OPERATION", Source: source.Path, Reference: logical.ID, Blocking: true,
				Assessment: "public SDK operation lacks feature_id or requirement_ids",
			})
		}
		revisionRaw, _ := json.Marshal(logical)
		revisionSum := sha256.Sum256(revisionRaw)
		operations = append(operations, specOpenAPIOperation{
			DocumentID: source.ID, SourcePath: source.Path, OperationID: logical.ID,
			FeatureID: logical.FeatureID, RequirementIDs: logical.RequirementIDs,
			Revision: hex.EncodeToString(revisionSum[:]),
		})
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].OperationID < operations[j].OperationID })
	return operations, findings, nil
}

func parseWorkflowSpec(
	source specSourceRegistryItem,
	raw []byte,
	features []testCatalogFeature,
	operations []specOpenAPIOperation,
) ([]specWorkflow, []specInventoryFinding, error) {
	var document specWorkflowDocument
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, nil, fmt.Errorf("parse workflow spec %s: %w", source.Path, err)
	}
	var findings []specInventoryFinding
	if document.SchemaVersion != 1 {
		findings = append(findings, workflowFinding(source, "", "INVALID_WORKFLOW_SCHEMA", "workflow spec requires schema_version 1"))
	}
	if document.RTKSpec.ID != source.ID ||
		(document.RTKSpec.Owner != "" && document.RTKSpec.Owner != source.Owner) {
		findings = append(findings, workflowFinding(source, "", "DOCUMENT_METADATA_MISMATCH",
			"rtk_spec id/owner does not match spec-sources.yaml"))
	}
	if !specDocumentStatusMatchesAuthority(document.RTKSpec.Status, source.Authority) {
		findings = append(findings, workflowFinding(source, "", "DOCUMENT_STATUS_MISMATCH",
			fmt.Sprintf("workflow status %q does not match registry authority %q", document.RTKSpec.Status, source.Authority)))
	}
	featureRequirements := map[string]map[string]bool{}
	for _, feature := range features {
		featureRequirements[feature.ID] = map[string]bool{}
		for _, requirement := range feature.Requirements {
			featureRequirements[feature.ID][requirement.ID] = true
		}
	}
	operationIndex := map[string]specOpenAPIOperation{}
	for _, operation := range operations {
		ref := operation.DocumentID + "#" + operation.OperationID
		if prior, exists := operationIndex[ref]; exists {
			findings = append(findings, workflowFinding(source, ref, "DUPLICATE_OPERATION_REF",
				fmt.Sprintf("operation reference is ambiguous between %s and %s", prior.SourcePath, operation.SourcePath)))
			continue
		}
		operationIndex[ref] = operation
	}
	workflowIDs := map[string]bool{}
	workflowIDPattern := regexp.MustCompile(`^WF(?:-[A-Z0-9]+){2,5}-[0-9]{3}$`)
	stepIDPattern := regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	artifactPattern := regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	dependencyTypes := map[string]bool{
		"auth": true, "session": true, "data": true, "state": true,
		"readiness": true, "cleanup": true,
	}
	for workflowIndex := range document.Workflows {
		workflow := &document.Workflows[workflowIndex]
		workflow.DocumentID = source.ID
		workflow.SourcePath = source.Path
		workflow.Authority = source.Authority
		workflow.Status = document.RTKSpec.Status
		workflow.Revision = specWorkflowRevision(*workflow)
		if !workflowIDPattern.MatchString(workflow.ID) {
			findings = append(findings, workflowFinding(source, workflow.ID, "INVALID_WORKFLOW_ID", "workflow requires a stable WF-* ID"))
		}
		if workflowIDs[workflow.ID] {
			findings = append(findings, workflowFinding(source, workflow.ID, "DUPLICATE_WORKFLOW", "workflow ID is repeated"))
		}
		workflowIDs[workflow.ID] = true
		if strings.TrimSpace(workflow.Title) == "" {
			findings = append(findings, workflowFinding(source, workflow.ID, "MISSING_WORKFLOW_TITLE", "workflow title is required"))
		}
		requirements, featureExists := featureRequirements[workflow.FeatureID]
		if !featureExists {
			findings = append(findings, workflowFinding(source, workflow.ID, "UNKNOWN_WORKFLOW_FEATURE", "workflow references an unknown feature"))
		}
		if len(workflow.RequirementIDs) == 0 {
			findings = append(findings, workflowFinding(source, workflow.ID, "MISSING_WORKFLOW_REQUIREMENT", "workflow must bind at least one requirement"))
		}
		requirementSet := map[string]bool{}
		for _, requirementID := range workflow.RequirementIDs {
			if requirementSet[requirementID] {
				findings = append(findings, workflowFinding(source, workflow.ID, "DUPLICATE_WORKFLOW_REQUIREMENT", "workflow repeats requirement "+requirementID))
			}
			requirementSet[requirementID] = true
			if !requirements[requirementID] {
				findings = append(findings, workflowFinding(source, workflow.ID, "WORKFLOW_REQUIREMENT_FEATURE_MISMATCH",
					"requirement "+requirementID+" is not owned by workflow feature "+workflow.FeatureID))
			}
		}
		if len(workflow.Steps) < 2 {
			findings = append(findings, workflowFinding(source, workflow.ID, "WORKFLOW_TOO_SHORT", "workflow must contain at least two ordered steps"))
		}
		inputs := map[string]bool{}
		for _, input := range workflow.Inputs {
			if !artifactPattern.MatchString(input) || inputs[input] {
				findings = append(findings, workflowFinding(source, workflow.ID, "INVALID_WORKFLOW_INPUT", "workflow inputs must be unique stable artifact names"))
			}
			inputs[input] = true
		}
		stepIndex := map[string]specWorkflowStep{}
		for _, step := range workflow.Steps {
			if !stepIDPattern.MatchString(step.ID) || stepIndex[step.ID].ID != "" {
				findings = append(findings, workflowFinding(source, workflow.ID+"#"+step.ID, "INVALID_WORKFLOW_STEP_ID", "step IDs must be unique lower_snake_case identifiers"))
			}
			stepIndex[step.ID] = step
		}
		for _, step := range workflow.Steps {
			ref := workflow.ID + "#" + step.ID
			operation, exists := operationIndex[step.OperationRef]
			if !exists {
				findings = append(findings, workflowFinding(source, ref, "UNKNOWN_WORKFLOW_OPERATION", "unknown operation reference "+step.OperationRef))
			} else {
				crossFeature := operation.FeatureID != workflow.FeatureID
				if crossFeature && step.OperationFeatureID == "" {
					findings = append(findings, workflowFinding(source, ref, "WORKFLOW_OPERATION_FEATURE_MISMATCH",
						fmt.Sprintf("cross-feature operation %s must explicitly declare operation_feature_id %s", step.OperationRef, operation.FeatureID)))
				} else if step.OperationFeatureID != "" && step.OperationFeatureID != operation.FeatureID {
					findings = append(findings, workflowFinding(source, ref, "WORKFLOW_OPERATION_FEATURE_MISMATCH",
						fmt.Sprintf("declared operation feature %s does not match source feature %s", step.OperationFeatureID, operation.FeatureID)))
				}
				if crossFeature {
					if len(step.OperationRequirementIDs) == 0 {
						findings = append(findings, workflowFinding(source, ref, "WORKFLOW_OPERATION_REQUIREMENT_MISMATCH",
							"cross-feature operation must explicitly declare operation_requirement_ids"))
					}
					sourceRequirements := map[string]bool{}
					for _, requirementID := range operation.RequirementIDs {
						sourceRequirements[requirementID] = true
					}
					for _, requirementID := range step.OperationRequirementIDs {
						if !sourceRequirements[requirementID] {
							findings = append(findings, workflowFinding(source, ref, "WORKFLOW_OPERATION_REQUIREMENT_MISMATCH",
								"declared operation requirement "+requirementID+" is not mapped by "+step.OperationRef))
						}
					}
				} else if !operationRequirementOverlap(operation.RequirementIDs, requirementSet) {
					findings = append(findings, workflowFinding(source, ref, "WORKFLOW_OPERATION_REQUIREMENT_MISMATCH",
						"operation does not reference a requirement bound to this workflow"))
				}
			}
			if step.Expected != "success" && step.Expected != "rejected" {
				findings = append(findings, workflowFinding(source, ref, "INVALID_WORKFLOW_EXPECTATION", "expected must be success or rejected"))
			}
			if (step.StateTransition.From == "") != (step.StateTransition.To == "") {
				findings = append(findings, workflowFinding(source, ref, "INVALID_STATE_TRANSITION", "state_transition requires both from and to"))
			}
			if len(step.Dependencies) == 0 && step.ID != workflow.Steps[0].ID {
				findings = append(findings, workflowFinding(source, ref, "MISSING_WORKFLOW_DEPENDENCY", "every step after the first must declare a dependency"))
			}
			for _, dependency := range step.Dependencies {
				if dependency.Step == step.ID || stepIndex[dependency.Step].ID == "" {
					findings = append(findings, workflowFinding(source, ref, "UNKNOWN_WORKFLOW_DEPENDENCY", "dependency step is missing or self-referential: "+dependency.Step))
				}
				if !dependencyTypes[dependency.Type] {
					findings = append(findings, workflowFinding(source, ref, "INVALID_WORKFLOW_DEPENDENCY_TYPE", "unsupported dependency type "+dependency.Type))
				}
			}
			for _, artifact := range append(append([]string{}, step.Consumes...), step.Produces...) {
				if !artifactPattern.MatchString(artifact) {
					findings = append(findings, workflowFinding(source, ref, "INVALID_WORKFLOW_ARTIFACT", "invalid artifact name "+artifact))
				}
			}
			if step.Always {
				hasCleanupDependency := false
				for _, dependency := range step.Dependencies {
					hasCleanupDependency = hasCleanupDependency || dependency.Type == "cleanup"
				}
				if !hasCleanupDependency {
					findings = append(findings, workflowFinding(source, ref, "INVALID_ALWAYS_STEP", "always step requires a cleanup dependency"))
				}
			}
		}
		if cycle := workflowDependencyCycle(workflow.Steps); len(cycle) > 0 {
			findings = append(findings, workflowFinding(source, workflow.ID, "WORKFLOW_DEPENDENCY_CYCLE", "workflow dependency cycle: "+strings.Join(cycle, " -> ")))
		} else {
			for _, step := range workflow.Steps {
				available := map[string]bool{}
				for input := range inputs {
					available[input] = true
				}
				for ancestor := range workflowAncestors(step.ID, stepIndex) {
					for _, artifact := range stepIndex[ancestor].Produces {
						available[artifact] = true
					}
				}
				for _, artifact := range step.Consumes {
					if !available[artifact] {
						findings = append(findings, workflowFinding(source, workflow.ID+"#"+step.ID, "UNSATISFIED_WORKFLOW_ARTIFACT",
							"consumed artifact is not an input or produced by a transitive dependency: "+artifact))
					}
				}
			}
		}
	}
	sort.Slice(document.Workflows, func(i, j int) bool { return document.Workflows[i].ID < document.Workflows[j].ID })
	return document.Workflows, findings, nil
}

func workflowFinding(source specSourceRegistryItem, reference, code, assessment string) specInventoryFinding {
	return specInventoryFinding{Code: code, Source: source.Path, Reference: reference, Assessment: assessment, Blocking: true}
}

func specWorkflowRevision(workflow specWorkflow) string {
	workflow.DocumentID = ""
	workflow.SourcePath = ""
	workflow.Authority = ""
	workflow.Status = ""
	workflow.Revision = ""
	raw, _ := json.Marshal(workflow)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func operationRequirementOverlap(operationRequirements []string, workflowRequirements map[string]bool) bool {
	for _, requirementID := range operationRequirements {
		if workflowRequirements[requirementID] {
			return true
		}
	}
	return false
}

func workflowDependencyCycle(steps []specWorkflowStep) []string {
	index := map[string]specWorkflowStep{}
	for _, step := range steps {
		index[step.ID] = step
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var stack, cycle []string
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			start := 0
			for stack[start] != id {
				start++
			}
			cycle = append(append([]string{}, stack[start:]...), id)
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		stack = append(stack, id)
		for _, dependency := range index[id].Dependencies {
			if index[dependency.Step].ID != "" && visit(dependency.Step) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, step := range steps {
		if visit(step.ID) {
			return cycle
		}
	}
	return nil
}

func workflowAncestors(stepID string, index map[string]specWorkflowStep) map[string]bool {
	ancestors := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		for _, dependency := range index[id].Dependencies {
			if !ancestors[dependency.Step] {
				ancestors[dependency.Step] = true
				visit(dependency.Step)
			}
		}
	}
	visit(stepID)
	return ancestors
}

func sourceAuthorityRequired(authority string) bool {
	return authority == "canonical" || authority == "service" || authority == "approved"
}

func specDocumentStatusRequired(status string) bool {
	return status == "canonical" || status == "normative" || status == "approved"
}

func specDocumentStatusMatchesAuthority(status, authority string) bool {
	switch authority {
	case "canonical":
		return status == "canonical"
	case "service":
		return status == "normative"
	case "approved":
		return status == "approved"
	case "draft", "proposed", "review_required":
		return status == authority
	default:
		return false
	}
}

func countSpecRequirements(features []testCatalogFeature) int {
	total := 0
	for _, feature := range features {
		total += len(feature.Requirements)
	}
	return total
}

func loadSpecTraceabilityCases(workspace string) ([]testCatalogCase, error) {
	raw, err := os.ReadFile(filepath.Join(workspace, "tests", "catalog.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read test catalog for traceability: %w", err)
	}
	var catalog testCatalog
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse test catalog for traceability: %w", err)
	}
	return catalog.Cases, nil
}

func writeSpecInventoryReport(outputDir string, inventory specInventory, cases []testCatalogCase) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	jsonPath := filepath.Join(outputDir, "spec-inventory.json")
	raw, err := json.MarshalIndent(inventoryReportJSON(inventory), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "SPEC_TRACEABILITY.md"), renderSpecInventoryReport(inventory, cases), 0o644)
}

func renderSpecInventoryReport(inventory specInventory, cases []testCatalogCase) []byte {
	var b strings.Builder
	fmt.Fprintln(&b, "# Spec Inventory And Test Traceability")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "> Generated from registered canonical/normative/approved specs; tests do not define the requirement denominator.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Spec | Feature | Requirement | Operation model | Revision | Authority | Status | Test IDs | Runners |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, feature := range inventory.Features {
		for _, requirement := range feature.Requirements {
			var testIDs, runners []string
			for _, tc := range cases {
				if tc.Status == "active" && catalogContainsString(tc.Verifies, requirement.ID) {
					testIDs = append(testIDs, tc.ID)
					if !catalogContainsString(runners, tc.Runner) {
						runners = append(runners, tc.Runner)
					}
				}
			}
			sort.Strings(testIDs)
			sort.Strings(runners)
			fmt.Fprintf(&b, "| `%s#%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | %s |\n",
				requirement.SpecSource.Path, requirement.SpecSource.Section, feature.ID, requirement.ID,
				requirement.OperationModel, shortDigest(requirement.Revision), requirement.SpecSource.Authority, requirement.Status,
				joinCatalogValues(testIDs), joinCatalogValues(runners))
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Operation Cross-Reference")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Operation | Method and path | Feature | Requirements | Revision |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- |")
	for _, operation := range inventory.Operations {
		fmt.Fprintf(&b, "| `%s#%s` | `%s %s` | `%s` | %s | `%s` |\n",
			operation.DocumentID, operation.OperationID, operation.Method, operation.Path, operation.FeatureID,
			joinCatalogValues(operation.RequirementIDs), shortDigest(operation.Revision))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Required Workflow Dependencies")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Workflow | Feature | Requirements | Step | Operation | Depends on | Consumes → produces | Transition | Expected |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, workflow := range inventory.Workflows {
		for _, step := range workflow.Steps {
			var dependencies []string
			for _, dependency := range step.Dependencies {
				dependencies = append(dependencies, dependency.Step+" ("+dependency.Type+")")
			}
			transition := ""
			if step.StateTransition.From != "" {
				transition = step.StateTransition.From + " → " + step.StateTransition.To
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | `%s` | `%s` | %s | %s → %s | %s | `%s` |\n",
				workflow.ID, workflow.FeatureID, joinCatalogValues(workflow.RequirementIDs), step.ID, step.OperationRef,
				joinCatalogValues(dependencies), joinCatalogValues(step.Consumes), joinCatalogValues(step.Produces),
				escapeMarkdownCell(transition), step.Expected)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Unspecified Normative Candidates")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "> These statements come from the registered specs but do not yet have stable Feature/Requirement IDs. They are reported as migration gaps and do not receive feature-coverage credit.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Spec | Section and line | Authority | Status | Candidate revision | Statement |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- |")
	for _, candidate := range inventory.Candidates {
		fmt.Fprintf(&b, "| `%s` | `%s@L%d` | `%s` | `%s` | `%s` | %s |\n",
			candidate.SourcePath, candidate.Section, candidate.Line, candidate.Authority, candidate.Status,
			shortDigest(candidate.Revision), escapeSpecInventoryCell(candidate.Statement))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Findings")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Code | Source | Reference | Blocking | Assessment |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- |")
	for _, finding := range inventory.Findings {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%t` | %s |\n", finding.Code, finding.Source, finding.Reference, finding.Blocking, escapeSpecInventoryCell(finding.Assessment))
	}
	return []byte(b.String())
}

func escapeSpecInventoryCell(value string) string {
	// Candidate statements are excerpts from their source documents. Relative
	// links in those excerpts lose their source-directory context when rendered
	// into the workspace report, so present them as text instead of emitting
	// misleading or broken links.
	return strings.ReplaceAll(escapeMarkdownCell(value), "[", `\[`)
}

func inventoryReportJSON(inventory specInventory) map[string]any {
	type featureRecord struct {
		ID           string                   `json:"id"`
		Title        string                   `json:"title"`
		Owner        string                   `json:"owner"`
		Risk         string                   `json:"risk"`
		Source       specRequirementSource    `json:"source"`
		Requirements []testCatalogRequirement `json:"requirements"`
	}
	features := make([]featureRecord, 0, len(inventory.Features))
	for _, feature := range inventory.Features {
		features = append(features, featureRecord{
			ID: feature.ID, Title: feature.Title, Owner: feature.Owner, Risk: feature.Risk,
			Source: feature.SpecSource, Requirements: feature.Requirements,
		})
	}
	return map[string]any{
		"schema_version": inventory.SchemaVersion, "features": features, "operations": inventory.Operations,
		"workflows": inventory.Workflows, "candidates": inventory.Candidates,
		"findings": inventory.Findings, "sources": inventory.Sources,
	}
}

func shortDigest(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

// scanSpecIDs is used by impact comparison and intentionally ignores comments
// outside registered RTK feature/requirement headings.
func scanSpecIDs(raw []byte) []string {
	var ids []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if match := specFeatureHeading.FindStringSubmatch(line); match != nil {
			ids = append(ids, match[2])
		} else if match := specRequirementHeading.FindStringSubmatch(line); match != nil {
			ids = append(ids, match[2])
		}
	}
	return ids
}
