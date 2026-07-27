package main

import (
	"strings"
	"testing"
)

const specFixtureFrontMatter = `---
rtk_spec:
  id: SPEC-TEST
  status: normative
  owner: cloud_platform
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

func hasSpecFinding(findings []specInventoryFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
