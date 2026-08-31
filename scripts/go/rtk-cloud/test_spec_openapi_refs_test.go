package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestOpenAPIRegisteredPathReferencesPreserveConsumerOperations(t *testing.T) {
	canonical := specSourceRegistryItem{ID: "SPEC-CONTRACT-OPENAPI", Path: "repos/rtk_cloud_contracts_doc/openapi.yaml", Parser: "openapi", Authority: "canonical"}
	consumer := specSourceRegistryItem{ID: "SPEC-VC-OPENAPI", Path: "repos/rtk_video_cloud/docs/openapi.yaml", Parser: "openapi", Authority: "service"}
	registry := specSourceRegistry{Sources: []specSourceRegistryItem{canonical, consumer}}
	contract := `x-rtk-spec: {id: SPEC-CONTRACT-OPENAPI, status: canonical}
paths:
  '/releases/{id}:publish':
    post:
      operationId: publishRelease
      x-rtk-feature-id: FEAT-TEST-RELEASE-001
      x-rtk-requirement-ids: [REQ-TEST-RELEASE-001]
      responses: {'200': {description: original}}
`
	wrapper := `x-rtk-spec: {id: SPEC-VC-OPENAPI, status: normative}
paths:
  '/releases/{id}:publish':
    $ref: './rtk_cloud_contracts_doc/openapi.yaml#/paths/~1releases~1{id}:publish'
`
	reads := 0
	reader := func(path string) ([]byte, error) {
		reads++
		if path != canonical.Path {
			return nil, fmt.Errorf("unexpected path %s", path)
		}
		return []byte(contract), nil
	}
	resolver := newOpenAPIPathResolver(registry, reader)
	first, findings, err := parseOpenAPISpecWithResolver(consumer, []byte(wrapper), resolver)
	if err != nil || len(findings) != 0 || len(first) != 1 {
		t.Fatalf("parse: %v %+v %+v", err, findings, first)
	}
	if first[0].DocumentID != consumer.ID || first[0].SourcePath != consumer.Path || first[0].Path != "/releases/{id}:publish" || first[0].OperationID != "publishRelease" || first[0].RequirementIDs[0] != "REQ-TEST-RELEASE-001" {
		t.Fatalf("lost consumer scope: %+v", first[0])
	}
	if _, _, err = parseOpenAPISpecWithResolver(consumer, []byte(wrapper), resolver); err != nil || reads != 1 {
		t.Fatalf("cache read count %d: %v", reads, err)
	}
	contract = strings.Replace(contract, "description: original", "description: changed", 1)
	changed, _, err := parseOpenAPISpecWithResolver(consumer, []byte(wrapper), newOpenAPIPathResolver(registry, reader))
	if err != nil || changed[0].Revision == first[0].Revision {
		t.Fatalf("referenced contract change did not invalidate digest: %v", err)
	}
	conflict := wrapper + "    post: {operationId: shadow}\n"
	if _, _, err = parseOpenAPISpecWithResolver(consumer, []byte(conflict), resolver); err == nil {
		t.Fatal("ambiguous local override accepted")
	}
}

func TestOpenAPIPathReferencesCannotFetchUnregisteredOrRemoteData(t *testing.T) {
	registry := specSourceRegistry{Sources: []specSourceRegistryItem{{ID: "SPEC-API", Path: "api.yaml", Parser: "openapi"}}}
	reads := 0
	resolver := newOpenAPIPathResolver(registry, func(string) ([]byte, error) { reads++; return []byte("paths: {'/ok': {get: {operationId: ok}}}"), nil })
	for _, ref := range []string{"https://example.invalid/spec.yaml#/paths/~1ok", "file:///api.yaml#/paths/~1ok", "../credentials.yaml#/paths/~1ok", "api.yaml?key=secret#/paths/~1ok", "api.yaml?", "#/components/pathItems/item", "#/paths/~2bad", "#/paths/~1ok/get"} {
		if _, err := resolver("api.yaml", ref); err == nil {
			t.Errorf("unsafe reference accepted: %s", ref)
		}
	}
	if reads != 0 {
		t.Fatalf("unsafe references caused %d file reads", reads)
	}
	if _, err := resolver("api.yaml", "#/paths/~1missing"); err == nil {
		t.Fatal("missing path accepted")
	}
	cycle := newOpenAPIPathResolver(registry, func(string) ([]byte, error) {
		return []byte("paths: {'/a': {$ref: '#/paths/~1b'}, '/b': {$ref: '#/paths/~1a'}}"), nil
	})
	if _, err := cycle("api.yaml", "#/paths/~1a"); err == nil {
		t.Fatal("reference cycle accepted")
	}
}
