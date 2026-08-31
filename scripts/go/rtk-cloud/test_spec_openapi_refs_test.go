package main

import (
	"errors"
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

func TestOpenAPIPathReferenceErrorsAndNestedInheritance(t *testing.T) {
	registry := specSourceRegistry{Sources: []specSourceRegistryItem{{Path: "api.yaml", Parser: "openapi"}}}
	for _, tc := range []struct {
		name, body, ref, want string
	}{
		{"invalid document", "paths: [", "#/paths/~1a", "yaml"},
		{"invalid item", "paths: {'/a': []}", "#/paths/~1a", "unmarshal"},
		{"nested conflict", "paths: {'/a': {$ref: '#/paths/~1b', get: {}}, '/b': {get: {}}}", "#/paths/~1a", "conflicting"},
		{"nested missing", "paths: {'/a': {$ref: '#/paths/~1b'}}", "#/paths/~1a", "does not exist"},
		{"trailing pointer escape", "", "#/paths/~", "invalid JSON Pointer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newOpenAPIPathResolver(registry, func(string) ([]byte, error) { return []byte(tc.body), nil })
			if _, err := resolver("api.yaml", tc.ref); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q: %v", tc.want, err)
			}
		})
	}
	sentinel := errors.New("registered file unreadable")
	resolver := newOpenAPIPathResolver(registry, func(string) ([]byte, error) { return nil, sentinel })
	if _, err := resolver("api.yaml", "#/paths/~1a"); !errors.Is(err, sentinel) {
		t.Fatalf("reader error lost: %v", err)
	}
	resolver = newOpenAPIPathResolver(registry, func(string) ([]byte, error) {
		return []byte("paths: {'/a': {$ref: '#/paths/~1b', summary: alias}, '/b': {$ref: '#/paths/~1~01'}, '/~1': {get: {operationId: escaped}}}"), nil
	})
	item, err := resolver("api.yaml", "#/paths/~1a")
	if err != nil || len(item) != 2 || item["summary"].Value != "alias" {
		t.Fatalf("nested inherited fields: %+v, %v", item, err)
	}
	var operation map[string]string
	node := item["get"]
	if err := node.Decode(&operation); err != nil || operation["operationId"] != "escaped" {
		t.Fatalf("escaped JSON Pointer did not resolve: %+v, %v", operation, err)
	}
	// Excessive chains fail deterministically even without a cycle.
	var body strings.Builder
	body.WriteString("paths:\n")
	for i := 0; i < 33; i++ {
		fmt.Fprintf(&body, "  '/%d': {$ref: '#/paths/~1%d'}\n", i, i+1)
	}
	resolver = newOpenAPIPathResolver(registry, func(string) ([]byte, error) { return []byte(body.String()), nil })
	if _, err := resolver("api.yaml", "#/paths/~10"); err == nil || !strings.Contains(err.Error(), "excessive") {
		t.Fatalf("reference depth limit missing: %v", err)
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
