#!/usr/bin/env bash
# make flux-install: Flux's controllers at the version clusters/home/flux-system/install/flux.json pins, with the
# patches in the kustomization.yaml beside it, applied server-side as the flux field manager, as flux install does.
# A plain flux install would drop the patches; swhurl-v2 verify (make verify-platform) checks the running controllers
# against the same flux.json. Components flux.json lists as removed are deleted, since applying never prunes.
#
# DRY_RUN=true prints the live diff and what it would run, and changes nothing.
set -Eeuo pipefail
cd "$(dirname "$0")/.."

INSTALL=clusters/home/flux-system/install
SETTINGS=$INSTALL/flux.json
# flux install's own components, which every installation has; flux.json's extraComponents come on top.
DEFAULT_COMPONENTS=(source-controller kustomize-controller helm-controller notification-controller)

fail() { echo "[ERROR] $1" >&2; exit 1; }
dry() { [[ ${DRY_RUN:-false} == true ]]; }
change() { if dry; then echo "  would run: $*"; else "$@"; fi; }

version=$(jq -er '.version | strings' "$SETTINGS")
mapfile -t extra < <(jq -r '.extraComponents[]' "$SETTINGS")
mapfile -t removed < <(jq -r '.removedComponents[]' "$SETTINGS")

client=$(flux version --client)
tr -s '[:space:]' '\n' <<<"$client" | grep -qxF "v$version" \
  || fail "flux CLI is ${client:-unknown}, but $SETTINGS pins $version; install flux $version (or bump its version to upgrade)"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$INSTALL" "$work/install"
flux install --export --namespace flux-system "--components-extra=$(IFS=,; echo "${extra[*]}")" >"$work/install/gotk-components.yaml"
kubectl kustomize "$work/install" >"$work/rendered.yaml"
while IFS=$'\t' read -r name arg; do
  grep -qF -- "$arg" "$work/rendered.yaml" || fail "rendered $name lacks $arg"
done < <(jq -r '.args | to_entries[] | .key as $name | .value[] | [$name, .] | @tsv' "$SETTINGS")

echo "[INFO] Flux $version with the patches in $INSTALL"
if dry; then
  # kubectl diff exits 1 when there are differences, and above 1 when it could not compare.
  status=0
  kubectl diff --server-side --field-manager=flux -f "$work/rendered.yaml" || status=$?
  ((status <= 1)) || fail "kubectl diff failed (exit $status)"
  ((status == 1)) || echo '[OK] no changes'
fi
change kubectl apply --server-side --field-manager=flux --force-conflicts -f "$work/rendered.yaml"
for name in "${DEFAULT_COMPONENTS[@]}" "${extra[@]}"; do
  change kubectl -n flux-system rollout status "deployment/$name" --timeout=5m
done
for name in "${removed[@]}"; do
  label="app.kubernetes.io/component=$name,app.kubernetes.io/part-of=flux"
  change kubectl -n flux-system delete deployment,serviceaccount -l "$label" --ignore-not-found
  change kubectl delete customresourcedefinitions -l "$label" --ignore-not-found
done
dry || echo '[OK] Flux controllers applied and rolled out'
