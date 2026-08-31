import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

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
