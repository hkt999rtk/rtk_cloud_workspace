#!/usr/bin/env python3
"""Compare source extraction with a catalog captured from an initialized database.

This command never generates or rewrites HTML. PostgreSQL catalog input is the
JSON output of tests/fixtures/database-simplification/catalog.sql.
"""
import argparse
import json
import re
import sys

import generate_database_er_atlas as atlas


def normalized_type(value):
    value = value.lower().strip()
    aliases = {'timestamptz': 'timestamp with time zone', 'timestamp': 'timestamp without time zone',
               'bigserial': 'bigint', 'serial': 'integer', 'int': 'integer', 'bool': 'boolean',
               'float8': 'double precision', 'varchar': 'character varying'}
    return aliases.get(value, re.sub(r'^varchar\(', 'character varying(', value))


def signature(table):
    return {
        'columns': {name: {'type': normalized_type(col['type']), 'nn': col['nn']}
                    for name, col in table['columns'].items()},
        'pk': tuple(table['pk']),
        'unique': sorted({tuple(cols) for cols in table['unique'].values()}),
        'fks': sorted((fk['parent'], tuple(fk['cols']), tuple(fk['target'])) for fk in table['fks'].values()),
    }


def compare(source, catalog):
    differences = []
    for name in sorted(set(source) | set(catalog)):
        if name not in source:
            differences.append(f'{name}: live table missing from source extraction')
        elif name not in catalog:
            differences.append(f'{name}: extracted table missing from live catalog')
        else:
            expected, actual = signature(source[name]), signature(catalog[name])
            for field in expected:
                if expected[field] != actual[field]:
                    differences.append(f'{name}.{field}: source={expected[field]!r} catalog={actual[field]!r}')
    return differences


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', required=True, choices=atlas.SOURCES)
    parser.add_argument('--catalog', required=True)
    args = parser.parse_args()
    with open(args.catalog) as stream:
        catalog = json.load(stream)
    differences = compare(atlas.parse_database(atlas.SOURCES[args.database]), catalog)
    if differences:
        print('\n'.join(differences), file=sys.stderr)
        return 1
    print(f'{args.database}: {len(catalog)} tables and their columns/keys match the live catalog')
    return 0


if __name__ == '__main__':
    sys.exit(main())
