#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR"
go build -o "$SCRIPT_DIR/getsize" .
printf 'Built %s/getsize\n' "$SCRIPT_DIR"
