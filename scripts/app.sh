#!/usr/bin/env bash
# Operate one generated app instance (namespace <app>-<env>, Flux unit
# homelab-app-<app>-<env>, HelmRelease <app>).
#
#   app.sh status    APP ENV   desired vs applied revision and image, replicas, route, failure reason
#   app.sh logs      APP ENV   recent logs from the instance's workload (FOLLOW=true to stream)
#   app.sh reconcile APP ENV   fetch Git and reconcile only this instance
#   app.sh check     APP ENV   render and check this instance against the app contract (offline)
set +x
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
action="${1:-}"; app="${2:-}"; env="${3:-}"
if [[ -z "$action" || -z "$app" || -z "$env" ]]; then
  sed -n '5,8p' "$0" >&2; exit 2
fi
ns="$app-$env"; unit="homelab-app-$app-$env"

if [[ "$action" == "check" ]]; then
  exec env PYTHONPATH="$ROOT/tools" python3 -m swhurl app-policy "$ROOT/tenants/apps/$app/$env"
fi
for cmd in kubectl flux jq; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done
kubectl -n flux-system get kustomization "$unit" >/dev/null 2>&1 || { echo "[ERROR] Flux unit $unit not found" >&2; exit 1; }

condition() {  # kind name namespace -> "True|False|Unknown: message"
  kubectl -n "$3" get "$1" "$2" -o json 2>/dev/null \
    | jq -r '(.status.conditions // [] | map(select(.type == "Ready")) | first) as $c | if $c then "\($c.status): \($c.message)" else "Unknown: no Ready condition" end'
}

status() {
  local source_rev unit_rev desired running hr_json problems
  source_rev="$(kubectl -n flux-system get gitrepository swhurl-platform -o jsonpath='{.status.artifact.revision}')"
  unit_rev="$(kubectl -n flux-system get kustomization "$unit" -o jsonpath='{.status.lastAppliedRevision}')"
  printf 'Instance   %s (namespace %s)\n' "$app/$env" "$ns"
  printf 'Git        desired %s, applied %s%s\n' "${source_rev##*:}" "${unit_rev##*:}" \
    "$([[ "$source_rev" == "$unit_rev" ]] && echo '' || echo '  <- not yet applied')"
  printf 'Flux unit  %s\n' "$(condition kustomization "$unit" flux-system)"
  printf 'Release    %s\n' "$(condition helmrelease "$app" "$ns")"

  hr_json="$(kubectl -n "$ns" get helmrelease "$app" -o json 2>/dev/null || echo '{}')"
  desired="$(jq -r '.spec.values.controllers.main.containers.main.image // {} | "\(.repository // "?"):\(.tag // "")\(if .digest then "@" + .digest else "" end)"' <<< "$hr_json")"
  running="$(kubectl -n "$ns" get pods -l "app.kubernetes.io/instance=$app" -o json \
    | jq -r '[.items[].status.containerStatuses[]? | .imageID | sub("^docker-pullable://"; "")] | unique | join(", ")')"
  printf 'Image      desired %s\n           running %s\n' "$desired" "${running:-none}"
  kubectl -n "$ns" get deploy,statefulset,daemonset -l "app.kubernetes.io/instance=$app" -o json \
    | jq -r '.items[] | "Replicas   \(.kind)/\(.metadata.name): \(.status.readyReplicas // .status.numberReady // 0)/\(.spec.replicas // .status.desiredNumberScheduled) ready"'
  kubectl -n "$ns" get ingress -o json \
    | jq -r '.items[] | "Route      https://\(.spec.rules[].host)"'
  kubectl -n "$ns" get certificate -o json 2>/dev/null \
    | jq -r '.items[] | "TLS        \(.metadata.name): \((.status.conditions // []) | map(select(.type == "Ready"))[0].status // "Unknown")"'

  problems="$(kubectl -n "$ns" get pods -o json | jq -r '.items[] | .metadata.name as $p
    | (.status.containerStatuses // [])[] | select(.ready | not)
    | "\($p): \(.state.waiting.reason // .state.terminated.reason // "not ready") \(.state.waiting.message // "" | .[0:160])"')"
  if [[ -n "$problems" ]]; then
    printf 'Problems\n'; sed 's/^/  /' <<< "$problems"
  fi
}

case "$action" in
  status) status ;;
  logs)
    kubectl -n "$ns" logs "deploy/$app" --all-containers --tail="${TAIL:-100}" \
      $([[ "${FOLLOW:-false}" == "true" ]] && echo --follow) ;;
  reconcile)
    flux reconcile source git swhurl-platform -n flux-system --timeout=5m
    flux reconcile kustomization "$unit" -n flux-system --timeout=10m ;;
  *) sed -n '5,8p' "$0" >&2; exit 2 ;;
esac
