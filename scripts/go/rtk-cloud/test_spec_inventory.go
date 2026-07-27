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

const specInventorySchema = "rtk-cloud-spec-inventory/v1"

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
		ID     string `yaml:"id"`
		Status string `yaml:"status"`
		Owner  string `yaml:"owner"`
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
	AcceptanceLayer string   `yaml:"acceptance_layer"`
	Gate            string   `yaml:"gate"`
	Environments    []string `yaml:"environments"`
	Targets         []string `yaml:"targets"`
	Evidence        []string `yaml:"evidence"`
	FreshnessHours  int      `yaml:"freshness_hours"`
	Required        *bool    `yaml:"required"`
	Status          string   `yaml:"status"`
	DeprecatedBy    string   `yaml:"replacement,omitempty"`
	DeprecatedOwner string   `yaml:"deprecation_owner,omitempty"`
	DeprecatedWhy   string   `yaml:"deprecation_reason,omitempty"`
	ApprovedAt      string   `yaml:"approved_at,omitempty"`
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

type specInventoryFinding struct {
	Code       string `json:"code"`
	Source     string `json:"source"`
	Reference  string `json:"reference,omitempty"`
	Assessment string `json:"assessment"`
	Blocking   bool   `json:"blocking"`
}

type specInventory struct {
	SchemaVersion string                   `json:"schema_version"`
	Features      []testCatalogFeature     `json:"-"`
	Operations    []specOpenAPIOperation   `json:"operations"`
	Findings      []specInventoryFinding   `json:"findings"`
	Sources       []specSourceRegistryItem `json:"sources"`
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
	fmt.Fprintf(os.Stdout, "spec inventory: %d features, %d requirements, %d operations, %d blocking findings\n",
		len(inventory.Features), countSpecRequirements(inventory.Features), len(inventory.Operations), blocking)
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
		if source.Parser != "markdown" && source.Parser != "openapi" {
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

func loadSpecInventoryWithReader(registry specSourceRegistry, readFile func(string) ([]byte, error)) (specInventory, error) {
	inventory := specInventory{SchemaVersion: specInventorySchema, Sources: registry.Sources}
	featureIndex, requirementIndex := map[string]int{}, map[string]string{}
	for _, source := range registry.Sources {
		raw, readErr := readFile(source.Path)
		if readErr != nil {
			return specInventory{}, fmt.Errorf("read spec source %s: %w", source.Path, readErr)
		}
		if source.Parser == "markdown" {
			features, findings, parseErr := parseMarkdownSpec(source, raw)
			if parseErr != nil {
				return specInventory{}, parseErr
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
		operations, findings, parseErr := parseOpenAPISpec(source, raw)
		if parseErr != nil {
			return specInventory{}, parseErr
		}
		inventory.Operations = append(inventory.Operations, operations...)
		inventory.Findings = append(inventory.Findings, findings...)
	}
	featureSet := map[string]bool{}
	requirementSet := map[string]bool{}
	for _, feature := range inventory.Features {
		featureSet[feature.ID] = true
		for _, requirement := range feature.Requirements {
			requirementSet[requirement.ID] = true
		}
	}
	for _, operation := range inventory.Operations {
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
			}
		}
	}
	sort.Slice(inventory.Features, func(i, j int) bool { return inventory.Features[i].ID < inventory.Features[j].ID })
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
			end := next
			for end < len(lines) && specFeatureHeading.FindStringSubmatch(lines[end]) == nil && specRequirementHeading.FindStringSubmatch(lines[end]) == nil {
				end++
			}
			revision := specRequirementRevision(match[3], requirementMetadata, strings.Join(lines[next:end], "\n"))
			current.Requirements = append(current.Requirements, testCatalogRequirement{
				ID: match[2], Title: strings.TrimSpace(match[3]), AcceptanceLayer: requirementMetadata.AcceptanceLayer,
				Gate: requirementMetadata.Gate, Environments: requirementMetadata.Environments, Targets: requirementMetadata.Targets,
				Evidence: requirementMetadata.Evidence, FreshnessHours: requirementMetadata.FreshnessHours,
				Required: requirementMetadata.Required, Status: status, Revision: revision,
				SpecSource: specRequirementSource{
					DocumentID: source.ID, Path: source.Path, Section: match[2], Authority: source.Authority, Revision: revision,
				},
			})
			i = next - 1
		}
	}
	return features, findings, nil
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
	var findings []specInventoryFinding
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !regexp.MustCompile(`(?m)^x-rtk-spec:\s*\n(?:  .*\n)*?  id:\s*["']?` + regexp.QuoteMeta(source.ID) + `["']?\s*$`).MatchString(text) {
		findings = append(findings, specInventoryFinding{
			Code: "MISSING_DOCUMENT_METADATA", Source: source.Path, Blocking: sourceAuthorityRequired(source.Authority),
			Assessment: "registered OpenAPI source has no matching x-rtk-spec metadata",
		})
	}
	statusMatch := regexp.MustCompile(`(?m)^x-rtk-spec:\s*\n(?:  .*\n)*?  status:\s*["']?([^"'\s]+)["']?\s*$`).FindStringSubmatch(text)
	if statusMatch == nil {
		findings = append(findings, specInventoryFinding{
			Code: "REVIEW_REQUIRED", Source: source.Path, Blocking: false,
			Assessment: "OpenAPI document has no explicit status",
		})
	} else if !specDocumentStatusMatchesAuthority(statusMatch[1], source.Authority) {
		findings = append(findings, specInventoryFinding{
			Code: "DOCUMENT_STATUS_MISMATCH", Source: source.Path, Blocking: true,
			Assessment: fmt.Sprintf("OpenAPI status %q does not match registry authority %q", statusMatch[1], source.Authority),
		})
	}
	var operations []specOpenAPIOperation
	lines := strings.Split(text, "\n")
	inPaths := false
	route := ""
	for i := 0; i < len(lines); {
		line := lines[i]
		if line == "paths:" {
			inPaths = true
			i++
			continue
		}
		if inPaths && len(line) > 0 && line[0] != ' ' && line != "paths:" {
			break
		}
		if !inPaths {
			i++
			continue
		}
		if match := regexp.MustCompile(`^  (/[^:]+):\s*$`).FindStringSubmatch(line); match != nil {
			route = strings.Trim(match[1], `"'`)
			i++
			continue
		}
		methodMatch := regexp.MustCompile(`^    ([a-z]+):\s*$`).FindStringSubmatch(line)
		if methodMatch == nil || !openAPIMethods[methodMatch[1]] {
			i++
			continue
		}
		method := methodMatch[1]
		start := i
		i++
		for i < len(lines) && !regexp.MustCompile(`^    [a-z]+:\s*$`).MatchString(lines[i]) && !regexp.MustCompile(`^  /[^:]+:\s*$`).MatchString(lines[i]) && (strings.TrimSpace(lines[i]) == "" || strings.HasPrefix(lines[i], "      ")) {
			i++
		}
		block := lines[start:i]
		operationID := openAPIBlockScalar(block, "operationId")
		featureID := openAPIBlockScalar(block, "x-rtk-feature-id")
		requirementIDs := openAPIBlockList(block, "x-rtk-requirement-ids")
		revision, revisionErr := canonicalOpenAPIOperationRevision(block)
		if revisionErr != nil {
			return nil, findings, fmt.Errorf("%s operation %s: %w", source.Path, operationID, revisionErr)
		}
		item := specOpenAPIOperation{
			DocumentID: source.ID, SourcePath: source.Path, Path: route, Method: strings.ToUpper(method), OperationID: operationID,
			FeatureID: featureID, RequirementIDs: requirementIDs, Revision: revision,
		}
		operations = append(operations, item)
		if operationID == "" {
			findings = append(findings, specInventoryFinding{
				Code: "MISSING_OPERATION_ID", Source: source.Path, Reference: strings.ToUpper(method) + " " + route,
				Blocking: true, Assessment: "public OpenAPI operation has no operationId",
			})
		} else if featureID == "" || len(requirementIDs) == 0 {
			findings = append(findings, specInventoryFinding{
				Code: "UNMAPPED_OPERATION", Source: source.Path, Reference: operationID, Blocking: true,
				Assessment: "public OpenAPI operation lacks x-rtk-feature-id or x-rtk-requirement-ids",
			})
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

func openAPIBlockScalar(lines []string, key string) string {
	re := regexp.MustCompile(`^\s+` + regexp.QuoteMeta(key) + `:\s*["']?([^"'\s]+)["']?\s*$`)
	for _, line := range lines {
		if match := re.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

func openAPIBlockList(lines []string, key string) []string {
	keyRE := regexp.MustCompile(`^(\s+)` + regexp.QuoteMeta(key) + `:\s*$`)
	var out []string
	listIndent := -1
	for _, line := range lines {
		if match := keyRE.FindStringSubmatch(line); match != nil {
			listIndent = len(match[1])
			continue
		}
		if listIndent >= 0 {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				out = append(out, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), `"'`))
				continue
			}
			if trimmed != "" && len(line)-len(strings.TrimLeft(line, " ")) <= listIndent {
				break
			}
		}
	}
	return out
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

func canonicalOpenAPIOperationRevision(lines []string) (string, error) {
	if len(lines) == 0 {
		return "", errors.New("empty OpenAPI operation")
	}
	normalizedLines := make([]string, len(lines))
	for i, line := range lines {
		normalizedLines[i] = strings.TrimPrefix(line, "    ")
	}
	var operation any
	if err := yaml.Unmarshal([]byte(strings.Join(normalizedLines, "\n")), &operation); err != nil {
		return "", fmt.Errorf("parse operation for revision: %w", err)
	}
	canonical, err := json.Marshal(operation)
	if err != nil {
		return "", fmt.Errorf("canonicalize operation revision: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
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
	fmt.Fprintln(&b, "| Spec | Feature | Requirement | Revision | Authority | Status | Test IDs | Runners |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- |")
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
			fmt.Fprintf(&b, "| `%s#%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | %s |\n",
				requirement.SpecSource.Path, requirement.SpecSource.Section, feature.ID, requirement.ID,
				shortDigest(requirement.Revision), requirement.SpecSource.Authority, requirement.Status,
				joinCatalogValues(testIDs), joinCatalogValues(runners))
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Findings")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Code | Source | Reference | Blocking | Assessment |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- |")
	for _, finding := range inventory.Findings {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%t` | %s |\n", finding.Code, finding.Source, finding.Reference, finding.Blocking, escapeMarkdownCell(finding.Assessment))
	}
	return []byte(b.String())
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
