#!/usr/bin/env bash
# The public whole-tree gate has no selected-instance or flag mode.
set -Eeuo pipefail
if [ "$#" -ne 0 ]; then
  echo 'usage: check-apps (no arguments; validates the whole tree)' >&2
  exit 2
fi
cd "$(git rev-parse --show-toplevel)"
chart=$(bash v2/chart.sh)
if bash v2/check-rendered.sh --chart "$chart"; then
  echo "[OK] v2's records render to Git and every compiled instance passes Go Kubernetes policy"
else
  status=$?
  echo "[BAD] v2/check-rendered.sh exited $status: render comparison or Go Kubernetes policy failed" >&2
  exit "$status"
fi
