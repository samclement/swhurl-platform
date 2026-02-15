#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Helmfile validation is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helmfile
need_cmd helm

# helmfile diff relies on the helm-diff plugin. Fail fast with clear guidance.
if ! helm diff --help >/dev/null 2>&1; then
  die "helm diff plugin not found. Install via: helm plugin install https://github.com/databus23/helm-diff"
fi

log_info "Verifying Helmfile environment '${HELMFILE_ENV:-default}'"
log_info "Assuming Helm repos are already configured (run scripts/25_helm_repos.sh first)"

log_info "Running helmfile lint"
helmfile_cmd lint

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

log_info "Rendering desired manifests (helmfile template)"
helmfile_cmd template > "$rendered"

log_info "Validating rendered manifests against live API (kubectl dry-run=server)"
srv_err="$(mktemp)"
cli_err="$(mktemp)"
if ! kubectl apply --dry-run=server -f "$rendered" >/dev/null 2>"$srv_err"; then
  log_warn "Server dry-run failed; trying client dry-run fallback"
  # Print a short excerpt so failures are actionable without dumping huge output.
  lines="${HELMFILE_SERVER_DRY_RUN_DEBUG_LINES:-60}"
  if [[ -s "$srv_err" ]]; then
    log_warn "Server dry-run error excerpt (matching lines):"
    rg -n '^(Error from server|error:|Error:)' "$srv_err" | head -n 50 >&2 || true
    log_warn "Server dry-run stderr tail (non-warning, last ${lines} lines):"
    rg -v '^Warning:' "$srv_err" | tail -n "$lines" >&2 || true
  fi
  if ! kubectl apply --dry-run=client -f "$rendered" >/dev/null 2>"$cli_err"; then
    if [[ -s "$cli_err" ]]; then
      log_error "Client dry-run also failed (matching lines):"
      rg -n '^(Error from server|error:|Error:)' "$cli_err" | head -n 50 >&2 || true
      log_error "Client dry-run stderr tail (non-warning, last ${lines} lines):"
      rg -v '^Warning:' "$cli_err" | tail -n "$lines" >&2 || true
    fi
    exit 1
  fi
else
  # Server dry-run may emit warnings on stderr; keep quiet by default.
  if [[ "${HELMFILE_SERVER_DRY_RUN_PRINT_WARNINGS:-false}" == "true" && -s "$srv_err" ]]; then
    lines="${HELMFILE_SERVER_DRY_RUN_DEBUG_LINES:-60}"
    log_warn "Server dry-run warnings excerpt (first ${lines} lines):"
    sed -n "1,${lines}p" "$srv_err" >&2 || true
  fi
fi
rm -f "$srv_err" "$cli_err" || true

log_info "Template/dry-run validation passed"

# Drift check: helmfile diff should be the final gate for "declarative == live".
# We keep suppression knobs to avoid known non-actionable noise (e.g. some Secret churn).
log_info "Running helmfile diff (drift check)"
diff_out="$(mktemp)"
diff_clean="$(mktemp)"
trap 'rm -f "$rendered" "$diff_out" "$diff_clean"' EXIT

DIFF_CONTEXT="${HELMFILE_DIFF_CONTEXT:-3}"
DIFF_SUPPRESS_SECRETS="${HELMFILE_DIFF_SUPPRESS_SECRETS:-true}"
DIFF_SUPPRESS_OBJECTS="${HELMFILE_DIFF_SUPPRESS_OBJECTS:-}" # comma-separated kinds, e.g. Secret
DIFF_SUPPRESS_LINE_REGEX="${HELMFILE_DIFF_SUPPRESS_OUTPUT_LINE_REGEX:-}" # comma-separated regexes

diff_args=(diff --detailed-exitcode --context "$DIFF_CONTEXT" --skip-deps)
if [[ "$DIFF_SUPPRESS_SECRETS" == "true" ]]; then
  diff_args+=(--suppress-secrets)
fi

IFS=',' read -r -a suppress_objs <<< "$DIFF_SUPPRESS_OBJECTS"
for o in "${suppress_objs[@]}"; do
  [[ -n "$o" ]] || continue
  diff_args+=(--suppress "$o")
done

IFS=',' read -r -a suppress_lines <<< "$DIFF_SUPPRESS_LINE_REGEX"
for r in "${suppress_lines[@]}"; do
  [[ -n "$r" ]] || continue
  diff_args+=(--suppress-output-line-regex "$r")
done

set +e
helmfile_cmd "${diff_args[@]}" >"$diff_out" 2>&1
rc=$?
set -e

# Strip ANSI color sequences so parsing/printing is stable.
sed -r 's/\x1B\[[0-9;]*[[:alpha:]]//g' "$diff_out" >"$diff_clean" || cp "$diff_out" "$diff_clean"

if [[ "$rc" -eq 0 ]]; then
  log_info "No Helmfile drift detected"
  exit 0
fi
if [[ "$rc" -ne 2 ]]; then
  log_error "helmfile diff failed (exit=$rc)"
  sed -n '1,200p' "$diff_clean" >&2 || true
  exit "$rc"
fi

# Exit 2 means "diff exists". Treat known non-actionable churn as ignorable, but fail on anything else.
#
# Known non-actionable churn:
# - Cilium rotates these TLS/CA secrets and helm-diff will always show changes (with secret content suppressed).
IGNORED_RESOURCE_HEADERS=(
  "kube-system, cilium-ca, Secret (v1) has changed:"
  "kube-system, hubble-relay-client-certs, Secret (v1) has changed:"
  "kube-system, hubble-server-certs, Secret (v1) has changed:"
)

# Resource headers look like:
#   <ns>, <name>, <Kind> (<apiVersion>) has changed:
resource_headers="$(rg -n '^[^,]+, [^,]+, .* has (changed|been added|been removed):$' "$diff_clean" | sed -E 's/^[0-9]+://g' || true)"

# Filter out ignored resource headers.
actionable_headers="$resource_headers"
for h in "${IGNORED_RESOURCE_HEADERS[@]}"; do
  actionable_headers="$(printf "%s\n" "$actionable_headers" | rg -v -F "$h" || true)"
done
actionable_headers="$(printf "%s\n" "$actionable_headers" | rg -v '^$' || true)"

# Also treat "diff exists but all output suppressed/boilerplate" as non-actionable.
boilerplate_stripped="$(rg -v '^(Comparing release=|Affected releases are:|Identified at least one change|$)' "$diff_clean" || true)"

if [[ -z "$boilerplate_stripped" ]]; then
  log_info "Helmfile reported changes but output was fully suppressed/boilerplate-only"
  exit 0
fi

if [[ -z "$resource_headers" ]]; then
  log_error "Helmfile reported drift but resource header parsing failed; refusing to auto-ignore"
  sed -n '1,200p' "$diff_clean" >&2 || true
  exit 1
fi

if [[ -z "$actionable_headers" ]]; then
  log_info "Helmfile reported changes but they are fully ignorable/noise-only"
  log_info "Ignored drift: Cilium CA/Hubble cert secret churn"
  exit 0
fi

log_error "Helmfile drift detected"
sed -n '1,200p' "$diff_clean" >&2 || true
exit 1
