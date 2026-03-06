#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

D2_BIN="${D2_BIN:-d2}"
if ! command -v "$D2_BIN" >/dev/null 2>&1; then
  go_d2="$(go env GOPATH 2>/dev/null)/bin/d2"
  if [[ -x "$go_d2" ]]; then
    D2_BIN="$go_d2"
  fi
fi

if ! command -v "$D2_BIN" >/dev/null 2>&1; then
  cat <<'EOF' >&2
[ERROR] d2 is required to generate charts.
Install d2 (https://d2lang.com) or set D2_BIN to a d2-compatible binary.
EOF
  exit 1
fi

declare -a CHARTS=(
  "docs/charts/c4/context.d2:docs/charts/c4/rendered/context.svg"
  "docs/charts/c4/container.d2:docs/charts/c4/rendered/container.svg"
  "docs/charts/c4/component-app-example.d2:docs/charts/c4/rendered/component-app-example.svg"
)

echo "[INFO] Generating architecture charts with $D2_BIN"
for entry in "${CHARTS[@]}"; do
  src_rel="${entry%%:*}"
  dst_rel="${entry##*:}"
  src="$REPO_ROOT/$src_rel"
  dst="$REPO_ROOT/$dst_rel"
  mkdir -p "$(dirname "$dst")"
  "$D2_BIN" "$src" "$dst" >/dev/null 2>&1
  chmod 644 "$dst"
  echo "[OK] $dst_rel"
done

echo "[INFO] Chart generation complete"
