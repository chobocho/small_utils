#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR"
go build -o "$SCRIPT_DIR/get_size" .
printf 'Built %s/get_size\n' "$SCRIPT_DIR"
