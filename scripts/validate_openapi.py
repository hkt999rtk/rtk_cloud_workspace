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
    path = path.resolve()
    if not path.is_relative_to(workspace.resolve()):
        raise ValueError("reference escapes the workspace")
    if path in documents:
        return documents[path]
    document = yaml.load(path.read_text(), Loader=UniqueKeyLoader)
    documents[path] = document

    def walk(value):
        if isinstance(value, dict):
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
                if uri.path:
                    local_documents(path.parent / unquote(uri.path), workspace, documents)
            for child in value.values():
                walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)

    walk(document)
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
