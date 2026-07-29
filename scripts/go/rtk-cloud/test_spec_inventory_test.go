package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specFixtureFrontMatter = `---
rtk_spec:
  id: SPEC-TEST
  status: normative
  owner: cloud_platform
  requirement_inventory: complete
---
`

func specFixture(body string) []byte {
	return []byte(specFixtureFrontMatter + `
## [FEAT-TEST-FLOW-001] Product flow
<!-- rtk-feature
owner: cloud_platform
risk: critical
status: active
change_paths: [scripts/go/rtk-cloud/**]
commit_anchors: [workspace]
surfaces:
  - kind: operator-workflow
    source: scripts/go/rtk-cloud/main.go
    selector: test-spec-inventory
-->
### [REQ-E2E-TEST-FLOW-001] Product flow completes
<!-- rtk-requirement
acceptance_layer: e2e
gate: pr
environments: [ci]
evidence: [json]
status: active
-->
` + body + "\n")
}

func TestMarkdownSpecDigestIgnoresFormattingButTracksSemantics(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"}
	first, findings, err := parseMarkdownSpec(source, specFixture("The flow MUST complete."))
	if err != nil || len(findings) != 0 {
		t.Fatalf("parse first spec: %v, findings=%v", err, findings)
	}
	formatted := strings.Replace(string(specFixture("The flow MUST complete.")), "The flow MUST complete.", "  The flow   MUST complete.  ", 1)
	second, _, err := parseMarkdownSpec(source, []byte(formatted))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Requirements[0].Revision != second[0].Requirements[0].Revision {
		t.Fatal("format-only change altered requirement revision")
	}
	changed, _, err := parseMarkdownSpec(source, specFixture("The flow MUST complete and persist."))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Requirements[0].Revision == changed[0].Requirements[0].Revision {
		t.Fatal("acceptance semantic change did not alter requirement revision")
	}
}

func TestMarkdownSpecSurfacesUnspecifiedNormativeClauses(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"}
	raw := []byte(specFixtureFrontMatter + `
## Existing product behavior

The service MUST preserve the tenant boundary.

## [FEAT-TEST-FLOW-001] Product flow
<!-- rtk-feature
owner: cloud_platform
risk: critical
status: active
change_paths: [scripts/go/rtk-cloud/**]
commit_anchors: [workspace]
-->
### [REQ-E2E-TEST-FLOW-001] Product flow completes
<!-- rtk-requirement
acceptance_layer: e2e
gate: pr
environments: [ci]
evidence: [json]
status: active
-->
The flow MUST complete.
`)
	candidates := scanMarkdownRequirementCandidates(source, raw)
	if len(candidates) != 1 {
		t.Fatalf("candidates=%+v, want only the untagged normative clause", candidates)
	}
	if candidates[0].Status != "required" || candidates[0].Section != "Existing product behavior" ||
		!strings.Contains(candidates[0].Statement, "tenant boundary") {
		t.Fatalf("candidate=%+v", candidates[0])
	}

	draft := source
	draft.Authority = "draft"
	draftRaw := strings.Replace(string(raw), "status: normative", "status: draft", 1)
	candidates = scanMarkdownRequirementCandidates(draft, []byte(draftRaw))
	if len(candidates) != 1 || candidates[0].Status != "planned" {
		t.Fatalf("draft candidates=%+v", candidates)
	}
}

func TestMarkdownSourceRequiresExplicitInventoryReview(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"}
	withoutReview := strings.Replace(string(specFixture("The flow MUST complete.")), "  requirement_inventory: complete\n", "", 1)
	_, findings, err := parseMarkdownSpec(source, []byte(withoutReview))
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpecFinding(findings, "REQUIREMENT_INVENTORY_REVIEW_REQUIRED") {
		t.Fatalf("unreviewed normative source was accepted: %+v", findings)
	}
	_, findings, err = parseMarkdownSpec(source, specFixture("The flow MUST complete."))
	if err != nil {
		t.Fatal(err)
	}
	if hasSpecFinding(findings, "REQUIREMENT_INVENTORY_REVIEW_REQUIRED") {
		t.Fatalf("completed source review remained incomplete: %+v", findings)
	}
}

func TestNormativeCandidateDigestIgnoresFormattingAndTracksMeaning(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"}
	first := scanMarkdownRequirementCandidates(source, []byte(specFixtureFrontMatter+"## Contract\n\n- Clients MUST retain state.\n"))
	formatted := scanMarkdownRequirementCandidates(source, []byte(specFixtureFrontMatter+"## Contract\n\n  -   Clients   MUST retain state.  \n"))
	changed := scanMarkdownRequirementCandidates(source, []byte(specFixtureFrontMatter+"## Contract\n\n- Clients MUST retain durable state.\n"))
	if len(first) != 1 || len(formatted) != 1 || len(changed) != 1 {
		t.Fatalf("unexpected candidate scans: %v %v %v", first, formatted, changed)
	}
	if first[0].Revision != formatted[0].Revision {
		t.Fatal("format-only candidate change altered revision")
	}
	if first[0].Revision == changed[0].Revision {
		t.Fatal("candidate semantic change did not alter revision")
	}
}

func TestDraftSpecRequirementsArePlanned(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "draft", Owner: "cloud_platform"}
	features, _, err := parseMarkdownSpec(source, specFixture("The planned flow completes."))
	if err != nil {
		t.Fatal(err)
	}
	if got := features[0].Requirements[0].Status; got != "planned" {
		t.Fatalf("draft requirement status=%q, want planned", got)
	}
}

func TestSpecInventoryReportsDuplicateIDsAsDrift(t *testing.T) {
	registry := specSourceRegistry{SchemaVersion: 1, Sources: []specSourceRegistryItem{
		{ID: "SPEC-TEST", Path: "canonical.md", Parser: "markdown", Authority: "canonical", Owner: "cloud_platform"},
		{ID: "SPEC-LOWER", Path: "service.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"},
	}}
	lower := strings.Replace(string(specFixture("Different acceptance.")), "id: SPEC-TEST", "id: SPEC-LOWER", 1)
	inventory, err := loadSpecInventoryWithReader(registry, func(path string) ([]byte, error) {
		if path == "canonical.md" {
			return specFixture("Canonical acceptance."), nil
		}
		return []byte(lower), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Features) != 1 || !hasSpecFinding(inventory.Findings, "SPEC_DRIFT") {
		t.Fatalf("duplicate authority definition was not surfaced as SPEC_DRIFT: %+v", inventory)
	}
}

func TestOpenAPIOperationsRequireCrossReferences(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-API", Path: "openapi.yaml", Parser: "openapi", Authority: "canonical", Owner: "cloud_platform"}
	raw := []byte(`openapi: 3.1.0
x-rtk-spec:
  id: SPEC-API
paths:
  /clips/{id}:
    get:
      operationId: getClip
      responses:
        "200":
          description: ok
`)
	operations, findings, err := parseOpenAPISpec(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || !hasSpecFinding(findings, "UNMAPPED_OPERATION") {
		t.Fatalf("unmapped operation was not reported: operations=%+v findings=%+v", operations, findings)
	}
	mapped := strings.Replace(string(raw), "      responses:", "      x-rtk-feature-id: FEAT-VC-CLIP-001\n      x-rtk-requirement-ids:\n        - REQ-VC-CLIP-RANGE-001\n      responses:", 1)
	_, findings, err = parseOpenAPISpec(source, []byte(mapped))
	if err != nil {
		t.Fatal(err)
	}
	if hasSpecFinding(findings, "UNMAPPED_OPERATION") {
		t.Fatalf("mapped operation remains unmapped: %+v", findings)
	}
	first, _, err := parseOpenAPISpec(source, []byte(mapped))
	if err != nil {
		t.Fatal(err)
	}
	formatted := strings.Replace(mapped, `operationId: getClip`, `operationId:    getClip`, 1)
	second, _, err := parseOpenAPISpec(source, []byte(formatted))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Revision != second[0].Revision {
		t.Fatal("OpenAPI formatting change altered operation revision")
	}
	changed := strings.Replace(mapped, `description: ok`, `description: changed contract`, 1)
	third, _, err := parseOpenAPISpec(source, []byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Revision == third[0].Revision {
		t.Fatal("OpenAPI contract change did not alter operation revision")
	}
}

func TestMultiOperationRequirementRequiresDependencyClassification(t *testing.T) {
	registry := specSourceRegistry{SchemaVersion: 1, Sources: []specSourceRegistryItem{
		{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"},
		{ID: "SPEC-API", Path: "openapi.yaml", Parser: "openapi", Authority: "service", Owner: "cloud_platform"},
	}}
	openAPI := []byte(`openapi: 3.1.0
x-rtk-spec:
  id: SPEC-API
  status: normative
paths:
  /resources:
    post:
      operationId: createResource
      x-rtk-feature-id: FEAT-TEST-FLOW-001
      x-rtk-requirement-ids:
        - REQ-E2E-TEST-FLOW-001
      responses: { "200": { description: ok } }
    get:
      operationId: listResources
      x-rtk-feature-id: FEAT-TEST-FLOW-001
      x-rtk-requirement-ids:
        - REQ-E2E-TEST-FLOW-001
      responses: { "200": { description: ok } }
`)
	load := func(markdown []byte) specInventory {
		t.Helper()
		inventory, err := loadSpecInventoryWithReader(registry, func(path string) ([]byte, error) {
			if path == "spec.md" {
				return markdown, nil
			}
			return openAPI, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return inventory
	}
	unclassified := load(specFixture("Acceptance."))
	if !hasSpecFinding(unclassified.Findings, "OPERATION_DEPENDENCY_REVIEW_REQUIRED") {
		t.Fatalf("multi-operation requirement escaped dependency review: %+v", unclassified.Findings)
	}
	independent := strings.Replace(
		string(specFixture("Acceptance.")),
		"acceptance_layer: e2e",
		"acceptance_layer: e2e\noperation_model: independent",
		1,
	)
	classified := load([]byte(independent))
	if hasSpecFinding(classified.Findings, "OPERATION_DEPENDENCY_REVIEW_REQUIRED") {
		t.Fatalf("explicit independent operation model was ignored: %+v", classified.Findings)
	}
	workflow := strings.Replace(independent, "operation_model: independent", "operation_model: workflow", 1)
	missingWorkflow := load([]byte(workflow))
	if !hasSpecFinding(missingWorkflow.Findings, "MISSING_OPERATION_WORKFLOW") {
		t.Fatalf("workflow model without DAG was accepted: %+v", missingWorkflow.Findings)
	}
}

func TestWorkflowDependenciesValidateDAGArtifactsAndOperations(t *testing.T) {
	source := specSourceRegistryItem{
		ID: "SPEC-WORKFLOWS", Path: "workflows.yaml", Parser: "workflow",
		Authority: "canonical", Owner: "cloud_platform",
	}
	feature := testCatalogFeature{
		ID:           "FEAT-TEST-FLOW-001",
		Requirements: []testCatalogRequirement{{ID: "REQ-E2E-TEST-FLOW-001"}},
	}
	operations := []specOpenAPIOperation{
		{DocumentID: "SPEC-API", OperationID: "create", FeatureID: feature.ID, RequirementIDs: []string{"REQ-E2E-TEST-FLOW-001"}},
		{DocumentID: "SPEC-API", OperationID: "read", FeatureID: feature.ID, RequirementIDs: []string{"REQ-E2E-TEST-FLOW-001"}},
	}
	valid := []byte(`schema_version: 1
rtk_spec: { id: SPEC-WORKFLOWS, status: canonical, owner: cloud_platform }
workflows:
  - id: WF-TEST-FLOW-001
    title: Create then read
    feature_id: FEAT-TEST-FLOW-001
    requirement_ids: [REQ-E2E-TEST-FLOW-001]
    inputs: [request]
    steps:
      - id: create
        operation_ref: SPEC-API#create
        consumes: [request]
        produces: [resource_id]
        expected: success
      - id: read
        operation_ref: SPEC-API#read
        depends_on: [{ step: create, type: data }]
        consumes: [resource_id]
        expected: success
`)
	workflows, findings, err := parseWorkflowSpec(source, valid, []testCatalogFeature{feature}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 || workflows[0].Revision == "" || len(findings) != 0 {
		t.Fatalf("valid workflow parse: workflows=%+v findings=%+v", workflows, findings)
	}
	cyclic := strings.Replace(string(valid),
		"        consumes: [request]\n        produces: [resource_id]",
		"        depends_on: [{ step: read, type: state }]\n        consumes: [request]\n        produces: [resource_id]", 1)
	_, findings, err = parseWorkflowSpec(source, []byte(cyclic), []testCatalogFeature{feature}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpecFinding(findings, "WORKFLOW_DEPENDENCY_CYCLE") {
		t.Fatalf("workflow cycle was accepted: %+v", findings)
	}
	unsatisfied := strings.Replace(string(valid), "consumes: [resource_id]", "consumes: [missing_artifact]", 1)
	_, findings, err = parseWorkflowSpec(source, []byte(unsatisfied), []testCatalogFeature{feature}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpecFinding(findings, "UNSATISFIED_WORKFLOW_ARTIFACT") {
		t.Fatalf("unsatisfied workflow artifact was accepted: %+v", findings)
	}
	unknown := strings.Replace(string(valid), "SPEC-API#read", "SPEC-API#missing", 1)
	_, findings, err = parseWorkflowSpec(source, []byte(unknown), []testCatalogFeature{feature}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpecFinding(findings, "UNKNOWN_WORKFLOW_OPERATION") {
		t.Fatalf("unknown workflow operation was accepted: %+v", findings)
	}
}

func TestSpecImpactIncludesWorkflowRevisionChanges(t *testing.T) {
	workflow := specWorkflow{
		ID: "WF-TEST-FLOW-001", FeatureID: "FEAT-TEST-FLOW-001",
		RequirementIDs: []string{"REQ-E2E-TEST-FLOW-001"}, Revision: "old",
	}
	before := specInventory{Workflows: []specWorkflow{workflow}}
	workflow.Revision = "new"
	after := specInventory{Workflows: []specWorkflow{workflow}}
	report := compareSpecInventories("base", "head", before, after)
	if len(report.Changes) != 1 || report.Changes[0].Kind != "WORKFLOW_MODIFIED" ||
		report.Changes[0].WorkflowID != workflow.ID {
		t.Fatalf("workflow impact=%+v", report.Changes)
	}
}

func TestSpecImpactIncludesUnspecifiedNormativeCandidateChanges(t *testing.T) {
	before := specInventory{Candidates: []specRequirementCandidate{{
		DocumentID: "SPEC-TEST", SourcePath: "spec.md", Section: "Contract", Line: 10, Revision: "old",
	}}}
	after := specInventory{Candidates: []specRequirementCandidate{{
		DocumentID: "SPEC-TEST", SourcePath: "spec.md", Section: "Contract", Line: 10, Revision: "new",
	}}}
	report := compareSpecInventories("base", "head", before, after)
	if len(report.Changes) != 2 ||
		report.Changes[0].Kind != "UNSPECIFIED_ADDED" ||
		report.Changes[1].Kind != "UNSPECIFIED_REMOVED" {
		t.Fatalf("candidate impact=%+v", report.Changes)
	}
	if report.Changes[0].FeatureID != "" || report.Changes[1].FeatureID != "" {
		t.Fatalf("unclassified candidate invented a feature mapping: %+v", report.Changes)
	}
}

func TestSpecImpactMarksRevisionChangesAndIllegalRemoval(t *testing.T) {
	requirement := testCatalogRequirement{ID: "REQ-E2E-TEST-FLOW-001", Revision: "old", Status: "active"}
	before := specInventory{Features: []testCatalogFeature{{ID: "FEAT-TEST-FLOW-001", Requirements: []testCatalogRequirement{requirement}}}}
	requirement.Revision = "new"
	after := specInventory{Features: []testCatalogFeature{{ID: "FEAT-TEST-FLOW-001", Requirements: []testCatalogRequirement{requirement}}}}
	report := compareSpecInventories("base", "head", before, after)
	if len(report.Changes) != 1 || report.Changes[0].Kind != "MODIFIED" {
		t.Fatalf("modified requirement impact=%+v", report.Changes)
	}
	report = compareSpecInventories("base", "head", before, specInventory{})
	if len(report.Changes) != 1 || report.Changes[0].Kind != "REMOVED" {
		t.Fatalf("removed requirement impact=%+v", report.Changes)
	}
}

func TestSpecInventoryCommandsWriteReportsAndEnforceMode(t *testing.T) {
	output := t.TempDir()
	if err := runTestSpecInventory([]string{"check", "--mode", "observe", "--output-dir", output}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spec-inventory.json", "SPEC_TRACEABILITY.md"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := runTestSpecInventory([]string{"render", "--mode", "observe", "--output-dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(mustWorkspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	requiredErr := runTestSpecInventory([]string{"check", "--mode", "required", "--output-dir", t.TempDir()})
	hasBlocking := false
	for _, finding := range inventory.Findings {
		hasBlocking = hasBlocking || finding.Blocking
	}
	if hasBlocking != (requiredErr != nil) {
		t.Fatalf("required mode result does not match current blocking findings: blockers=%t err=%v", hasBlocking, requiredErr)
	}
	for _, args := range [][]string{{"unknown"}, {"check", "--mode", "invalid"}, {"check", "--unknown"}} {
		if err := runTestSpecInventory(args); err == nil {
			t.Fatalf("invalid spec inventory command accepted: %v", args)
		}
	}
}

func mustWorkspaceRoot(t *testing.T) string {
	t.Helper()
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestSpecImpactCommandWritesEmptyHeadComparison(t *testing.T) {
	output := t.TempDir()
	if err := runTestSpecImpact([]string{"--base", "HEAD", "--head", "HEAD", "--output-dir", output}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spec-impact.json", "SPEC_IMPACT.md"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := runTestSpecImpact(nil); err == nil {
		t.Fatal("spec impact accepted missing base")
	}
	if err := runTestSpecImpact([]string{"--base", "HEAD", "--unknown"}); err == nil {
		t.Fatal("spec impact accepted an unknown flag")
	}
}

func TestMissingSpecRegistryDetectionDoesNotHideSubmoduleFetchErrors(t *testing.T) {
	if !isMissingSpecRegistry(fmt.Errorf("comparison: %w", errSpecRegistryMissing)) {
		t.Fatal("typed missing registry error was not recognized")
	}
	if isMissingSpecRegistry(errors.New("fatal: object does not exist in shallow submodule")) {
		t.Fatal("generic shallow-submodule failure was treated as an absent root registry")
	}
}

func TestSpecImpactWorkflowsFetchBaseSubmoduleCommits(t *testing.T) {
	workspace := mustWorkspaceRoot(t)
	script, err := os.ReadFile(filepath.Join(workspace, "scripts", "fetch-spec-impact-base.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`git -C "$workspace" ls-tree -r "$base_ref"`,
		`"$mode" != "160000"`,
		`git -C "$repository" fetch --no-tags --depth=1 origin "$object"`,
		`git -C "$repository" cat-file -e "${object}^{commit}"`,
	} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("base submodule fetch helper lacks %q", required)
		}
	}
	for _, workflow := range []string{
		".github/workflows/workspace-test-baseline.yml",
		".github/workflows/feature-qualification.yml",
	} {
		raw, readErr := os.ReadFile(filepath.Join(workspace, workflow))
		if readErr != nil {
			t.Fatal(readErr)
		}
		fetchAt := strings.Index(string(raw), `fetch-spec-impact-base.sh`)
		impactAt := strings.Index(string(raw), `test-spec-impact`)
		if fetchAt < 0 || impactAt < 0 || fetchAt > impactAt {
			t.Fatalf("%s does not fetch base submodule commits before spec impact", workflow)
		}
	}
}

func TestSpecRegistryRejectsInvalidSources(t *testing.T) {
	valid := `schema_version: 1
sources:
  - id: SPEC-TEST
    path: specs/test.md
    parser: markdown
    authority: service
    owner: cloud_platform
`
	for name, body := range map[string]string{
		"schema":    strings.Replace(valid, "schema_version: 1", "schema_version: 2", 1),
		"owner":     strings.Replace(valid, "cloud_platform", "unknown", 1),
		"parser":    strings.Replace(valid, "parser: markdown", "parser: pdf", 1),
		"authority": strings.Replace(valid, "authority: service", "authority: rumor", 1),
		"path":      strings.Replace(valid, "specs/test.md", "../test.md", 1),
		"duplicate": valid + `  - id: SPEC-TEST
    path: specs/other.md
    parser: markdown
    authority: service
    owner: cloud_platform
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSpecSourceRegistry([]byte(body)); err == nil {
				t.Fatal("invalid registry accepted")
			}
		})
	}
	if _, err := parseSpecSourceRegistry([]byte(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestSpecParserRejectsMalformedMetadataAndTracksIDs(t *testing.T) {
	source := specSourceRegistryItem{ID: "SPEC-TEST", Path: "spec.md", Parser: "markdown", Authority: "service", Owner: "cloud_platform"}
	missingFeatureMetadata := strings.Replace(string(specFixture("Acceptance.")), "<!-- rtk-feature", "<!-- missing-feature", 1)
	if _, _, err := parseMarkdownSpec(source, []byte(missingFeatureMetadata)); err == nil {
		t.Fatal("feature without metadata accepted")
	}
	missingRequirementMetadata := strings.Replace(string(specFixture("Acceptance.")), "<!-- rtk-requirement", "<!-- missing-requirement", 1)
	if _, _, err := parseMarkdownSpec(source, []byte(missingRequirementMetadata)); err == nil {
		t.Fatal("requirement without metadata accepted")
	}
	orphan := specFixtureFrontMatter + `
### [REQ-E2E-TEST-FLOW-001] Orphan
<!-- rtk-requirement
acceptance_layer: e2e
gate: pr
environments: [ci]
status: active
-->
Acceptance.
`
	if _, _, err := parseMarkdownSpec(source, []byte(orphan)); err == nil {
		t.Fatal("orphan requirement accepted")
	}
	ids := scanSpecIDs(specFixture("Acceptance."))
	if strings.Join(ids, ",") != "FEAT-TEST-FLOW-001,REQ-E2E-TEST-FLOW-001" {
		t.Fatalf("scan IDs=%v", ids)
	}
}

func hasSpecFinding(findings []specInventoryFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
