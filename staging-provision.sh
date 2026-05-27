#!/bin/sh

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
exec /usr/bin/env bash "$SCRIPT_DIR/scripts/staging-provision.sh" "$@"
