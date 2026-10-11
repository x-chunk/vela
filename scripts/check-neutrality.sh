#!/usr/bin/env bash
# Check all tracked and nonignored source files using only Python's standard library.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
exec python3 "$root/scripts/check-neutrality.py" "$root"
