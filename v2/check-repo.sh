#!/usr/bin/env bash
# check-repo: what Git holds is what Flux can apply, offline (docs/iteration-2-plan.md#17-check-repo-in-go-the-decisions-not-the-subprocesses-approved-8-november-2026).
#
# swhurl-v2 check-repo decides every check over this checkout: the documentation links, the two tables the docs keep in
# step with Git, the names v2's compiled tree gives apps, every Secret's SOPS structure, the platform settings and which
# folders Flux renders with what each substitutes. This script runs the steps where a tool verifies itself:
#   kubectl kustomize   renders each active path, and swhurl-v2 check-repo --rendered applies that path's substitution;
#   flux-schema         with SCHEMA=1 only, validates each rendered path against the Kubernetes and Flux schemas;
#   bash -n             parses every shell file Git holds (tracked, or untracked and not ignored).
# Every step runs even when an earlier one failed, so one run reports every problem; any problem exits 1.
#
# SWHURL_V2 names the binary; without it the script builds one into its temporary folder. Needs Go to build, git, jq,
# kubectl and bash; SCHEMA=1 also needs flux-schema (docs/contributing.md).
set -Eeuo pipefail
if [ "$#" -ne 0 ]; then
  echo 'usage: check-repo.sh (no arguments; SCHEMA=1 adds schema validation)' >&2
  exit 2
fi
schema=${SCHEMA:-}
if [ -n "$schema" ] && [ "$schema" != 1 ]; then
  echo "usage: SCHEMA is 1 or unset, not '$schema'" >&2
  exit 2
fi
if [ "$schema" = 1 ] && ! command -v flux-schema >/dev/null; then
  echo '[BAD] SCHEMA=1 needs flux-schema on PATH (docs/contributing.md)' >&2
  exit 2
fi
cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

v2=${SWHURL_V2:-}
if [ -z "$v2" ]; then
  v2="$work/swhurl-v2"
  (cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$v2" ./cmd/swhurl-v2)
fi

# The decisions: exit 1 is findings, which the report holds; anything else is a checkout it could not read.
status=0
"$v2" check-repo --root . --json >"$work/report.json" || status=$?
if [ "$status" -gt 1 ]; then
  echo "[BAD] swhurl-v2 check-repo could not read the checkout (exit $status)" >&2
  exit "$status"
fi
jq -r '.checks[] | if (.problems | length) > 0 then .problems[] | "[BAD] " + . else "[OK] " + .okay end' "$work/report.json"
failures=$(jq '[.checks[].problems | length] | add // 0' "$work/report.json")
mapfile -t paths < <(jq -r '.paths[].path' "$work/report.json")

# Each active path, rendered, substituted as Flux does and, with SCHEMA=1, schema-validated.
done_label=rendered
[ "$schema" = 1 ] && done_label='rendered and schema-validated'
for path in "${paths[@]}"; do
  if ! kubectl kustomize "$path" >"$work/rendered.yaml" 2>"$work/error"; then
    echo "[BAD] $path: kubectl kustomize failed: $(head -c 2000 "$work/error")"
    failures=$((failures + 1))
    continue
  fi
  if ! "$v2" check-repo --root . --rendered "$path" <"$work/rendered.yaml" >"$work/applied.yaml" 2>"$work/error"; then
    cat "$work/error"
    failures=$((failures + 1))
    continue
  fi
  if [ "$schema" = 1 ] && ! flux-schema validate --schema-location default --schema-location ecosystem \
    --skip-json-path /sops <"$work/applied.yaml" >"$work/error" 2>&1; then
    echo "[BAD] $path: schema validation failed:"
    sed 's/^/  /' "$work/error"
    failures=$((failures + 1))
    continue
  fi
  echo "[OK] $done_label $path"
done

# Every shell file parses on its own.
mapfile -t scripts < <(git ls-files --cached --others --exclude-standard '*.sh' | sort -u)
unparsed=0
for script in "${scripts[@]}"; do
  if ! bash -n "$script" 2>"$work/error"; then
    echo "[BAD] $script: $(head -c 2000 "$work/error")"
    unparsed=$((unparsed + 1))
  fi
done
if [ "$unparsed" -eq 0 ]; then
  echo "[OK] ${#scripts[@]} shell files pass individual syntax checks"
fi
failures=$((failures + unparsed))

if [ "$failures" -ne 0 ]; then
  printf '\nValidation failed: %d problem(s).\n' "$failures"
  exit 1
fi
echo "Validation passed for ${#paths[@]} active render entrypoints."
