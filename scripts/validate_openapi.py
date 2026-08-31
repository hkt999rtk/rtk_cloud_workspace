#!/usr/bin/env python3
"""Validate shared OpenAPI 3.0/3.1 contracts without remote reference fetches."""

import argparse
from pathlib import Path
from urllib.parse import unquote, urlsplit

import yaml
from openapi_spec_validator import validate


class UniqueKeyLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        self.flatten_mapping(node)
        keys = set()
        for key_node, _ in node.value:
            key = self.construct_object(key_node, deep=deep)
            if key in keys:
                raise ValueError(f"duplicate YAML key {key!r} at line {key_node.start_mark.line + 1}")
            keys.add(key)
        return super().construct_mapping(node, deep=deep)


def local_documents(path, workspace, documents):
    """Check each local reference before the spec validator resolves it."""
    visited = set()

    def load(source):
        source = source.resolve()
        if not source.is_relative_to(workspace.resolve()):
            raise ValueError("reference escapes the workspace")
        if source not in documents:
            documents[source] = yaml.load(source.read_text(), Loader=UniqueKeyLoader)
        return source, documents[source]

    def walk(value, source, context="openapi"):
        if not isinstance(value, (dict, list)):
            return
        key = (source, id(value), context)
        if key in visited:
            return
        visited.add(key)
        if isinstance(value, list):
            for child in value:
                walk(child, source, context)
            return
        if context.endswith("-map"):
            for child in value.values():
                walk(child, source, context.removesuffix("-map"))
            return
        if context == "schema":
            # The pinned resolver does not honor identifier/dynamic bases. Reject them
            # before validation so the guard and resolver cannot inspect different
            # documents (or resolve a supposedly local reference over the network).
            for keyword in ("$id", "$dynamicRef", "$dynamicAnchor"):
                if keyword in value:
                    raise ValueError(f"schema {keyword} bases are unsupported; use document-relative $ref paths")
        ref = value.get("$ref")
        if isinstance(ref, str):
            uri = urlsplit(ref)
            if uri.scheme or uri.netloc or uri.query:
                raise ValueError("OpenAPI references must be local files or fragments")
            target_path, target = load(source.parent / unquote(uri.path) if uri.path else source)
            fragment = unquote(uri.fragment)
            if fragment and not fragment.startswith("/"):
                raise ValueError("named reference anchors are unsupported; use JSON Pointer fragments")
            for token in fragment.split("/")[1:]:
                token = token.replace("~1", "/").replace("~0", "~")
                target = target[int(token)] if isinstance(target, list) else target[token]
            # A reference can make an otherwise arbitrary example/extension value
            # a schema: check that target in the reference site's context too.
            walk(target, target_path, context)
        for name, child in value.items():
            if context == "schema":
                if name in ("properties", "patternProperties", "$defs", "definitions", "dependentSchemas"):
                    walk(child, source, "schema-map")
                elif name in ("allOf", "anyOf", "oneOf", "prefixItems", "items", "additionalItems",
                              "additionalProperties", "unevaluatedItems", "unevaluatedProperties",
                              "contains", "contentSchema", "if", "then", "else", "not", "propertyNames"):
                    walk(child, source, "schema")
            elif name == "schema":
                walk(child, source, "schema")
            elif name == "schemas":
                walk(child, source, "schema-map")
            elif name in ("paths", "webhooks", "responses", "parameters", "headers", "requestBodies",
                          "callbacks", "links", "securitySchemes", "content", "encoding", "examples"):
                walk(child, source, "openapi-map" if isinstance(child, dict) else "openapi")
            elif not name.startswith("x-") and name not in ("example", "value", "default", "enum", "const", "$ref"):
                walk(child, source)

    path, document = load(path)
    walk(document, path)
    return document


def validate_file(path, workspace):
    document = local_documents(path, workspace, {})
    if not isinstance(document, dict) or document.get("openapi") not in ("3.0.3", "3.1.0"):
        raise ValueError("expected an OpenAPI 3.0.3 or 3.1.0 document")
    validate(document, base_uri=path.resolve().as_uri())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="*", type=Path)
    parser.add_argument("--workspace", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    files = args.files or [args.workspace / path for path in (
        "repos/rtk_cloud_contracts_doc/openapi.yaml",
        "repos/rtk_account_manager/openapi.yaml",
        "repos/rtk_billing/openapi.yaml",
        "repos/rtk_cloud_admin/docs/openapi.yaml",
    )]
    failed = False
    for path in files:
        try:
            validate_file(path, args.workspace)
        except Exception as exc:
            # Reference-resolution exceptions can embed the entire specification.
            detail = str(exc).split(" within ", 1)[0].splitlines()[0][:500]
            print(f"FAIL {path}: {type(exc).__name__}: {detail}")
            failed = True
        else:
            print(f"PASS {path}")
    return int(failed)


if __name__ == "__main__":
    raise SystemExit(main())
