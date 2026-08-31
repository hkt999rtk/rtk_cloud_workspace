package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAvailableSpecInventoryResolvesNestedContractWithoutRootCheckout(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "tests/spec-sources.yaml"), `schema_version: 1
sources:
  - {id: SPEC-CONTRACT-OPENAPI, path: repos/rtk_cloud_contracts_doc/openapi.yaml, parser: openapi, authority: canonical, owner: cloud_platform}
  - {id: SPEC-VC-OPENAPI, path: repos/rtk_video_cloud/docs/openapi.yaml, parser: openapi, authority: service, owner: rtk_video_cloud}
`)
	consumerPath := "repos/rtk_video_cloud/docs/openapi.yaml"
	writeTestFile(t, filepath.Join(workspace, consumerPath), `x-rtk-spec: {id: SPEC-VC-OPENAPI, status: normative}
paths:
  /clouds:
    $ref: './rtk_cloud_contracts_doc/openapi.yaml#/paths/~1clouds'
`)
	aliasPath := filepath.Join(workspace, "repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc/openapi.yaml")
	contract := `x-rtk-spec: {id: SPEC-CONTRACT-OPENAPI, status: canonical}
paths:
  /clouds:
    get:
      operationId: listClouds
      x-rtk-feature-id: FEAT-TEST-CLOUD-001
      x-rtk-requirement-ids: [REQ-TEST-CLOUD-001]
      responses: {'200': {description: original}}
`
	writeTestFile(t, aliasPath, contract)
	inventory, err := loadAvailableSpecInventory(workspace)
	if err != nil || len(inventory.Sources) != 1 || len(inventory.Operations) != 1 {
		t.Fatalf("partial runner inventory: %+v, %v", inventory, err)
	}
	op := inventory.Operations[0]
	if op.DocumentID != "SPEC-VC-OPENAPI" || op.SourcePath != consumerPath || op.OperationID != "listClouds" || len(op.RequirementIDs) != 1 {
		t.Fatalf("reference lost consumer identity or requirements: %+v", op)
	}
	writeTestFile(t, aliasPath, strings.Replace(contract, "description: original", "description: changed", 1))
	changed, err := loadAvailableSpecInventory(workspace)
	if err != nil || changed.Operations[0].Revision == op.Revision {
		t.Fatalf("partial reference digest did not change: %v", err)
	}
	if _, err := loadSpecInventory(workspace); err == nil || !strings.Contains(err.Error(), canonicalOpenAPIPath) {
		t.Fatalf("central inventory must still require root contracts: %v", err)
	}
	// A populated canonical checkout wins; stale nested files cannot override it.
	writeTestFile(t, filepath.Join(workspace, canonicalOpenAPIPath), contract)
	complete, err := loadAvailableSpecInventory(workspace)
	if err != nil || len(complete.Operations) != 2 {
		t.Fatalf("complete runner inventory: %+v, %v", complete, err)
	}
	for _, operation := range complete.Operations {
		if operation.Revision != op.Revision {
			t.Fatalf("nested contract overrode canonical contract: %+v", operation)
		}
	}
}

func TestRunnerOpenAPIReaderKeepsAllowlistAndRejectsAmbiguousAliases(t *testing.T) {
	canonical := specSourceRegistryItem{ID: "SPEC-CONTRACT-OPENAPI", Path: canonicalOpenAPIPath, Parser: "openapi", Authority: "canonical"}
	available := specSourceRegistry{Sources: []specSourceRegistryItem{
		{Path: "repos/rtk_video_cloud/docs/openapi.yaml", Parser: "openapi"},
		{Path: "repos/rtk_account_manager/openapi.yaml", Parser: "openapi"},
		{Path: "repos/ignored/docs/spec.md", Parser: "markdown"},
		{Path: "unrelated.yaml", Parser: "openapi"},
		{Path: "repos/ignored/other.yaml", Parser: "openapi"},
		canonical,
	}}
	registry := specSourceRegistry{Sources: []specSourceRegistryItem{canonical}}
	vcAlias := "repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc/openapi.yaml"
	amAlias := "repos/rtk_account_manager/docs/rtk_cloud_contracts_doc/openapi.yaml"
	valid := "x-rtk-spec: {id: SPEC-CONTRACT-OPENAPI, status: canonical}\npaths: {}\n"
	for _, tc := range []struct {
		name, first, second, want string
		rootErr, aliasErr         error
		unregistered              bool
	}{
		{name: "one available alias", first: valid},
		{name: "agreeing aliases", first: valid, second: valid},
		{name: "later available alias", second: valid},
		{name: "missing aliases", want: "file does not exist"},
		{name: "conflicting aliases", first: valid, second: valid + "# different revision\n", want: "disagree"},
		{name: "invalid YAML", first: "[", want: "does not identify"},
		{name: "wrong ID", first: strings.ReplaceAll(valid, "SPEC-CONTRACT-OPENAPI", "SPEC-OTHER"), want: "does not identify"},
		{name: "wrong authority", first: strings.ReplaceAll(valid, "canonical", "draft"), want: "does not identify"},
		{name: "alias unreadable", aliasErr: os.ErrPermission, want: "permission denied"},
		{name: "root unreadable", rootErr: os.ErrPermission, first: valid, want: "permission denied"},
		{name: "unregistered canonical", unregistered: true, first: valid, want: "file does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := []string{}
			reader := func(target string) ([]byte, error) {
				reads = append(reads, target)
				switch target {
				case canonicalOpenAPIPath:
					if tc.rootErr != nil {
						return nil, tc.rootErr
					}
				case vcAlias:
					if tc.aliasErr != nil {
						return nil, tc.aliasErr
					}
					if tc.first != "" {
						return []byte(tc.first), nil
					}
				case amAlias:
					if tc.second != "" {
						return []byte(tc.second), nil
					}
				default:
					t.Fatalf("unregistered alias read: %s", target)
				}
				return nil, os.ErrNotExist
			}
			allowed := registry
			if tc.unregistered {
				allowed = specSourceRegistry{}
			}
			raw, err := newRunnerOpenAPIReader(allowed, available, reader)(canonicalOpenAPIPath)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			} else if err != nil || string(raw) != valid {
				t.Fatalf("unexpected contract: %q, %v", raw, err)
			}
			if (tc.rootErr != nil || tc.unregistered) && len(reads) != 1 {
				t.Fatalf("fallback bypassed registration/permission error: %v", reads)
			}
		})
	}
	reads := 0
	reader := newRunnerOpenAPIReader(registry, available, func(target string) ([]byte, error) {
		reads++
		return nil, os.ErrNotExist
	})
	if _, err := reader("another.yaml"); !os.IsNotExist(err) || reads != 1 {
		t.Fatalf("non-contract reads must not fall back: %d, %v", reads, err)
	}
}

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
