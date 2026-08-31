package main

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

type openAPIPathResolver func(sourcePath, ref string) (map[string]yaml.Node, error)

// Resolve Path Item references only from registered local OpenAPI documents.
// No network, arbitrary workspace file, schema reference traversal or file URI.
func newOpenAPIPathResolver(registry specSourceRegistry, readFile func(string) ([]byte, error)) openAPIPathResolver {
	registered := map[string]bool{}
	for _, source := range registry.Sources {
		if source.Parser == "openapi" {
			registered[source.Path] = true
		}
	}
	cache := map[string]map[string]yaml.Node{}
	var resolve func(string, string, map[string]bool) (map[string]yaml.Node, error)
	resolve = func(source, ref string, seen map[string]bool) (map[string]yaml.Node, error) {
		u, err := url.Parse(ref)
		if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.ForceQuery || strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") {
			return nil, fmt.Errorf("Path Item reference must be local")
		}
		target := source
		if u.Path != "" {
			target = path.Clean(path.Join(path.Dir(source), u.Path))
		}
		// Consumer contracts symlinks are the documented canonical workspace
		// layout. Load the registered canonical document, never an arbitrary alias.
		parts := strings.Split(target, "/")
		if len(parts) == 5 && parts[0] == "repos" && strings.HasSuffix(target, "/docs/rtk_cloud_contracts_doc/openapi.yaml") {
			target = "repos/rtk_cloud_contracts_doc/openapi.yaml"
		}
		if !registered[target] {
			return nil, fmt.Errorf("Path Item target is not a registered OpenAPI document")
		}
		if !strings.HasPrefix(u.Fragment, "/paths/") {
			return nil, fmt.Errorf("Path Item reference must select a registered path")
		}
		token := strings.TrimPrefix(u.Fragment, "/paths/")
		if strings.Contains(token, "/") {
			return nil, fmt.Errorf("Path Item reference must select one path")
		}
		for i := 0; i < len(token); i++ {
			if token[i] == '~' {
				if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
					return nil, fmt.Errorf("invalid JSON Pointer escape")
				}
				i++
			}
		}
		route := strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		key := target + "#" + u.Fragment
		if seen[key] || len(seen) >= 32 {
			return nil, fmt.Errorf("cyclic or excessive Path Item reference chain")
		}
		seen[key] = true
		defer delete(seen, key)
		paths, ok := cache[target]
		if !ok {
			raw, err := readFile(target)
			if err != nil {
				return nil, err
			}
			var doc struct {
				Paths map[string]yaml.Node `yaml:"paths"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return nil, err
			}
			paths = doc.Paths
			cache[target] = paths
		}
		node, ok := paths[route]
		if !ok {
			return nil, fmt.Errorf("referenced Path Item does not exist")
		}
		var item map[string]yaml.Node
		if err := node.Decode(&item); err != nil {
			return nil, err
		}
		if nested, ok := item["$ref"]; ok {
			inherited, err := resolve(target, nested.Value, seen)
			if err != nil {
				return nil, err
			}
			delete(item, "$ref")
			for k, v := range inherited {
				if _, exists := item[k]; exists {
					return nil, fmt.Errorf("conflicting referenced Path Item field")
				}
				item[k] = v
			}
		}
		return item, nil
	}
	return func(source, ref string) (map[string]yaml.Node, error) { return resolve(source, ref, map[string]bool{}) }
}
