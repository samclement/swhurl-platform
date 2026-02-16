#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Release inventory check is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helm

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
warn() { printf "[WARN] %s\n" "$1"; }

fail=0
say "Required Releases"

mapfile -t expected < <(verify_expected_releases)

mapfile -t actual < <(helm list -A --no-headers 2>/dev/null | awk '{print $2"/"$1}' | sort -u)

for item in "${expected[@]}"; do
  if printf '%s\n' "${actual[@]}" | grep -qx "$item"; then
    ok "$item"
  else
    bad "$item"
  fi
done

STRICT_EXTRAS="${VERIFY_RELEASE_STRICT_EXTRAS:-false}"
if [[ "$STRICT_EXTRAS" != "true" && "$STRICT_EXTRAS" != "false" ]]; then
  bad "VERIFY_RELEASE_STRICT_EXTRAS must be true or false (got: ${STRICT_EXTRAS})"
fi

if [[ "$STRICT_EXTRAS" == "true" ]]; then
  say "Unexpected Releases"
  RELEASE_SCOPE="${VERIFY_RELEASE_SCOPE:-platform}" # platform|cluster
  case "$RELEASE_SCOPE" in
    platform|cluster) ;;
    *) bad "VERIFY_RELEASE_SCOPE must be one of: platform, cluster (got: ${RELEASE_SCOPE})" ;;
  esac

  allow_patterns=("${VERIFY_RELEASE_ALLOWLIST_DEFAULT[@]}")
  if [[ -n "${VERIFY_RELEASE_ALLOWLIST:-}" ]]; then
    IFS=',' read -r -a extra_allow <<< "${VERIFY_RELEASE_ALLOWLIST}"
    for p in "${extra_allow[@]}"; do
      [[ -n "$p" ]] || continue
      allow_patterns+=("$p")
    done
  fi

  extras_found=0
  for item in "${actual[@]}"; do
    if printf '%s\n' "${expected[@]}" | grep -qx "$item"; then
      continue
    fi
    if [[ "$RELEASE_SCOPE" == "platform" ]] && ! is_release_in_platform_scope "$item"; then
      continue
    fi
    if name_matches_any_pattern "$item" "${allow_patterns[@]}"; then
      warn "Allowlisted extra release: ${item}"
      continue
    fi
    bad "Unexpected release: ${item}"
    extras_found=1
  done
  if [[ "$extras_found" -eq 0 ]]; then
    ok "No unexpected releases (scope: ${RELEASE_SCOPE})"
  fi
else
  warn "Skipping unexpected release checks (VERIFY_RELEASE_STRICT_EXTRAS=false)"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Release inventory verification passed"
