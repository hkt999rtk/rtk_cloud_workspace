import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

from validate_openapi import validate_file


class OpenAPIValidationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)

    def spec(self, version="3.1.0", schema=None):
        return {
            "openapi": version,
            "info": {"title": "Fixture", "version": "1"},
            "paths": {},
            "components": {"schemas": {"Value": schema or {"type": "string"}}},
        }

    def write(self, name, value):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value if isinstance(value, str) else json.dumps(value))
        return path

    def test_both_supported_schema_dialects(self):
        for version, schema in (
            ("3.0.3", {"type": "number", "minimum": 0, "exclusiveMinimum": True}),
            ("3.1.0", {"type": ["number", "null"], "exclusiveMinimum": 0}),
        ):
            with self.subTest(version=version):
                validate_file(self.write("api.json", self.spec(version, schema)), self.root)

    def test_invalid_schema_is_rejected(self):
        with self.assertRaises(Exception):
            validate_file(self.write("api.json", self.spec("3.0.3", {"type": "not-a-type"})), self.root)

    def test_duplicate_yaml_key_is_rejected(self):
        path = self.write("api.yaml", "openapi: 3.1.0\ninfo: {}\ninfo: {}\n")
        with self.assertRaisesRegex(ValueError, "duplicate YAML key 'info' at line 3"):
            validate_file(path, self.root)

    def test_local_reference_and_recursive_schema(self):
        self.write("schema.yaml", "type: object\nproperties:\n  child: {$ref: '#'}\n")
        path = self.write("api.json", self.spec(schema={"$ref": "schema.yaml"}))
        validate_file(path, self.root)

    def test_duplicate_referenced_document_is_rejected(self):
        self.write("schema.yaml", "type: object\ntype: string\n")
        path = self.write("api.json", self.spec(schema={"$ref": "schema.yaml"}))
        with self.assertRaisesRegex(ValueError, "duplicate YAML key 'type'"):
            validate_file(path, self.root)

    def test_schema_id_cannot_hide_duplicate_keys_in_another_base(self):
        self.write("child.yaml", "type: string\n")
        self.write("schemas/child.yaml", "type: object\ntype: string\n")
        path = self.write("api.json", self.spec(schema={"$id": "schemas/root.yaml", "$ref": "child.yaml"}))
        with self.assertRaisesRegex(ValueError, r"schema \$id bases are unsupported"):
            validate_file(path, self.root)

    def test_schema_ids_are_rejected_before_the_resolver_runs(self):
        for identifier in ("schemas/root.yaml", "https://example.invalid/schema.yaml", "//example.invalid/schema.yaml", "../outside/schema.yaml"):
            with self.subTest(identifier=identifier):
                path = self.write("api.json", self.spec(schema={"type": "object", "properties": {"child": {"$id": identifier, "$ref": "child.yaml"}}}))
                with patch("validate_openapi.validate") as resolver:
                    with self.assertRaisesRegex(ValueError, r"schema \$id bases are unsupported"):
                        validate_file(path, self.root)
                    resolver.assert_not_called()

    def test_referenced_documents_cannot_introduce_schema_ids(self):
        self.write("schemas/child.yaml", "$id: https://example.invalid/root.yaml\ntype: string\n")
        path = self.write("api.json", self.spec(schema={"$ref": "schemas/child.yaml"}))
        with self.assertRaisesRegex(ValueError, r"schema \$id bases are unsupported"):
            validate_file(path, self.root)

    def test_dynamic_references_are_rejected_before_resolution(self):
        for ref in ("https://example.invalid/schema.yaml", "//example.invalid/schema.yaml", "missing.yaml", "../outside.yaml", "#node"):
            with self.subTest(ref=ref):
                path = self.write("api.json", self.spec(schema={"$dynamicRef": ref}))
                with patch("validate_openapi.validate") as resolver:
                    with self.assertRaisesRegex(ValueError, r"schema \$dynamicRef bases are unsupported"):
                        validate_file(path, self.root)
                    resolver.assert_not_called()

    def test_referenced_documents_cannot_hide_dynamic_references(self):
        self.write("schemas/child.yaml", "$dynamicRef: https://example.invalid/root.yaml\n")
        path = self.write("api.json", self.spec(schema={"$ref": "schemas/child.yaml"}))
        with self.assertRaisesRegex(ValueError, r"schema \$dynamicRef bases are unsupported"):
            validate_file(path, self.root)

    def test_dynamic_anchors_are_explicitly_unsupported(self):
        path = self.write("api.json", self.spec(schema={"type": "object", "properties": {"node": {"$dynamicAnchor": "node", "type": "string"}}}))
        with self.assertRaisesRegex(ValueError, r"schema \$dynamicAnchor bases are unsupported"):
            validate_file(path, self.root)

    def test_schema_like_keys_in_data_and_property_names_are_allowed(self):
        data = {"$id": "customer-1", "$dynamicRef": "https://example.invalid/data", "$dynamicAnchor": "data", "$ref": "missing-data.json"}
        schema = {"type": "object", "properties": {name: {"type": "string"} for name in data},
                  "example": data, "default": data, "enum": [data], "x-payload": data}
        for version in ("3.0.3", "3.1.0"):
            with self.subTest(version=version):
                spec = self.spec(version, schema)
                spec["components"]["examples"] = {"Payload": {"value": data}}
                spec["x-payload"] = data
                validate_file(self.write("api.json", spec), self.root)

    def test_schema_property_names_do_not_hide_real_schema_keywords(self):
        for name in ("example", "default", "value", "$id", "x-data"):
            with self.subTest(name=name):
                schema = {"type": "object", "properties": {name: {"$dynamicRef": "https://example.invalid/schema"}}}
                with patch("validate_openapi.validate") as resolver:
                    with self.assertRaisesRegex(ValueError, r"schema \$dynamicRef bases are unsupported"):
                        validate_file(self.write("api.json", self.spec(schema=schema)), self.root)
                    resolver.assert_not_called()

    def test_reference_to_data_subtree_is_checked_as_a_schema(self):
        spec = self.spec(schema={"$ref": "#/x-payload"})
        spec["x-payload"] = {"$id": "https://example.invalid/root", "$ref": "child.json"}
        with patch("validate_openapi.validate") as resolver:
            with self.assertRaisesRegex(ValueError, r"schema \$id bases are unsupported"):
                validate_file(self.write("api.json", spec), self.root)
            resolver.assert_not_called()

    def test_external_fragment_is_checked_with_schema_context(self):
        self.write("container.json", {"payload": {"$dynamicRef": "https://example.invalid/schema"}})
        spec = self.spec(schema={"$ref": "container.json#/payload"})
        with patch("validate_openapi.validate") as resolver:
            with self.assertRaisesRegex(ValueError, r"schema \$dynamicRef bases are unsupported"):
                validate_file(self.write("api.json", spec), self.root)
            resolver.assert_not_called()

    def test_schema_examples_and_const_are_arbitrary_data(self):
        data = {"$id": "record", "$ref": "https://example.invalid/data"}
        spec = self.spec(schema={"const": data, "examples": [data]})
        spec["paths"] = {"/items": {"get": {"responses": {"200": {
            "description": "ok", "content": {"application/json": {
                "schema": {"type": "object"}, "example": data,
                "examples": {"Named": {"value": data}},
            }},
        }}}}}
        validate_file(self.write("api.json", spec), self.root)

    def test_named_anchors_are_explicitly_unsupported(self):
        path = self.write("api.json", self.spec(schema={"$ref": "#node"}))
        with patch("validate_openapi.validate") as resolver:
            with self.assertRaisesRegex(ValueError, "named reference anchors are unsupported"):
                validate_file(path, self.root)
            resolver.assert_not_called()

    def test_example_reference_is_not_ignored(self):
        spec = self.spec()
        spec["components"]["examples"] = {"Payload": {"$ref": "https://example.invalid/example.json"}}
        with patch("validate_openapi.validate") as resolver:
            with self.assertRaisesRegex(ValueError, "local files or fragments"):
                validate_file(self.write("api.json", spec), self.root)
            resolver.assert_not_called()

    def test_pointer_escaping_and_referenced_schema_examples(self):
        self.write("container.json", {"schemas": {"a/b~c": {"type": "object", "examples": [{"$id": "data"}]}}})
        spec = self.spec(schema={"$ref": "container.json#/schemas/a~1b~0c"})
        validate_file(self.write("api.json", spec), self.root)

    def test_ci_triggers_cover_inventory_implementation_and_module_inputs(self):
        root = Path(__file__).resolve().parent.parent
        workflow = yaml.load((root / ".github/workflows/contracts-openapi.yml").read_text(), Loader=yaml.BaseLoader)
        for event in ("pull_request", "push"):
            with self.subTest(event=event):
                paths = workflow["on"][event]["paths"]
                for source in ("scripts/go/rtk-cloud/**", "scripts/go/go.mod", "scripts/go/go.sum", "go.work", "go.work.sum"):
                    self.assertIn(source, paths)

    def test_missing_reference_is_rejected(self):
        path = self.write("api.json", self.spec(schema={"$ref": "missing.yaml"}))
        with self.assertRaises(FileNotFoundError):
            validate_file(path, self.root)

    def test_missing_fragment_is_rejected(self):
        path = self.write("api.json", self.spec(schema={"$ref": "#/components/schemas/Missing"}))
        with self.assertRaises(Exception):
            validate_file(path, self.root)

    def test_remote_and_escaping_references_are_rejected(self):
        for ref in ("https://example.invalid/schema.yaml", "//example.invalid/schema.yaml", "../outside.yaml", "schema.yaml?query=yes"):
            with self.subTest(ref=ref):
                path = self.write("api.json", self.spec(schema={"$ref": ref}))
                with self.assertRaisesRegex(ValueError, "local files or fragments|escapes the workspace"):
                    validate_file(path, self.root)

    def test_version_is_required(self):
        for spec in ({}, [], self.spec("2.0")):
            with self.subTest(spec=spec):
                with self.assertRaisesRegex(ValueError, "expected an OpenAPI"):
                    validate_file(self.write("api.json", spec), self.root)

    def test_cli_reports_all_files_and_fails_on_any_invalid_file(self):
        valid = self.write("valid.json", self.spec())
        invalid = self.write("invalid.json", self.spec(schema={"$ref": "#/components/schemas/Missing"}))
        result = subprocess.run(
            [sys.executable, str(Path(__file__).with_name("validate_openapi.py")), "--workspace", str(self.root), str(valid), str(invalid)],
            capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn(f"PASS {valid}", result.stdout)
        self.assertIn(f"FAIL {invalid}", result.stdout)
        self.assertLess(len(result.stdout), 1000)


if __name__ == "__main__":
    unittest.main()
