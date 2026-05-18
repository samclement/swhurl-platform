#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/00_lib.sh"
ensure_context

fail=0
say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }

say "Flux Kustomizations"
while IFS= read -r line; do
  name=$(echo "$line" | awk '{print $1}')
  ready=$(echo "$line" | awk '{print $2}')
  if [[ "$ready" == "True" ]]; then
    ok "$name"
  else
    bad "$name is not Ready"
  fi
done < <(flux get kustomizations -n flux-system --no-header 2>/dev/null | awk '{print $1, $3}')

say "Token Alignment"
src="$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.CLICKSTACK_INGESTION_KEY}' 2>/dev/null || true)"
dst="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"
if [[ -n "$src" && "$src" == "$dst" ]]; then
  ok "otel ingestion key aligned"
elif [[ -z "$src" ]]; then
  bad "platform-runtime-inputs.CLICKSTACK_INGESTION_KEY is empty"
else
  bad "otel token mismatch (run: make runtime-inputs-refresh-otel)"
fi

(( fail )) && exit 1
printf "\nValidation passed.\n"
