#!/usr/bin/env bash
set -Eeuo pipefail

# Convenience wrapper to print the current phase plan (and delete plan).
# Keep logic centralized in run.sh so docs and automation don't drift.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

exec ./run.sh --dry-run "$@"

