#!/usr/bin/env bash
# Prove Reloader's opt-in and namespace scope with disposable workloads:
#   - an annotated Deployment in a watched namespace restarts when its Secret changes
#   - an identical Deployment without the annotation does not
#   - an annotated Deployment in an unwatched namespace does not
# Creates only resources labelled platform.swhurl.com/reloader-test=true and removes them.
set +x
set -Eeuo pipefail

WATCHED_NS="${WATCHED_NS:-logging}"
UNWATCHED_NS=reloader-test
LABEL=platform.swhurl.com/reloader-test

if [[ "${DRY_RUN:-false}" == "true" ]]; then
  sed -n '2,6p' "$0"
  exit 0
fi
for cmd in kubectl jq; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done
kubectl -n platform-system rollout status deploy/reloader-reloader --timeout=2m >/dev/null \
  || { echo "[ERROR] Reloader is not running in platform-system" >&2; exit 1; }
if kubectl get namespace "$UNWATCHED_NS" >/dev/null 2>&1 || kubectl -n "$WATCHED_NS" get secret reloader-test >/dev/null 2>&1; then
  echo "[ERROR] Test resources already exist; clean up first" >&2; exit 1
fi

fail=0
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }

cleanup() {
  kubectl -n "$WATCHED_NS" delete deploy,secret -l "$LABEL=true" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  if [[ "$(kubectl get namespace "$UNWATCHED_NS" -o jsonpath="{.metadata.labels.${LABEL//./\\.}}" 2>/dev/null)" == "true" ]]; then
    kubectl delete namespace "$UNWATCHED_NS" --wait=false >/dev/null 2>&1 || true
  fi
  echo "[INFO] Cleaned up reloader test resources"
}
trap cleanup EXIT

kubectl create namespace "$UNWATCHED_NS" >/dev/null
kubectl label namespace "$UNWATCHED_NS" "$LABEL=true" >/dev/null

workload() {  # namespace name annotate(true|false)
  local annotations='{}'
  [[ "$3" == "true" ]] && annotations='{"secret.reloader.stakater.com/reload":"reloader-test"}'
  kubectl -n "$1" apply -f - >/dev/null <<EOF
{"apiVersion":"v1","kind":"Secret","metadata":{"name":"reloader-test","labels":{"$LABEL":"true"}},"stringData":{"value":"one"}}
EOF
  kubectl -n "$1" apply -f - >/dev/null <<EOF
{"apiVersion":"apps/v1","kind":"Deployment",
 "metadata":{"name":"$2","labels":{"$LABEL":"true"},"annotations":$annotations},
 "spec":{"replicas":1,"selector":{"matchLabels":{"app":"$2"}},
  "template":{"metadata":{"labels":{"app":"$2","$LABEL":"true"}},
   "spec":{"automountServiceAccountToken":false,"containers":[{"name":"c","image":"busybox:1.37","command":["sleep","3600000"],
    "envFrom":[{"secretRef":{"name":"reloader-test"}}],
    "resources":{"requests":{"cpu":"1m","memory":"8Mi"},"limits":{"memory":"16Mi"}}}]}}}}
EOF
}
template_hash() { kubectl -n "$1" get deploy "$2" -o json | jq -S '.spec.template.metadata.annotations // {}' | sha256sum | cut -c1-12; }

workload "$WATCHED_NS" reloader-test-optin true
workload "$WATCHED_NS" reloader-test-control false
workload "$UNWATCHED_NS" reloader-test-unwatched true
for target in "$WATCHED_NS/reloader-test-optin" "$WATCHED_NS/reloader-test-control" "$UNWATCHED_NS/reloader-test-unwatched"; do
  kubectl -n "${target%/*}" rollout status "deploy/${target#*/}" --timeout=3m >/dev/null
done
sleep 10
before_optin="$(template_hash "$WATCHED_NS" reloader-test-optin)"
before_control="$(template_hash "$WATCHED_NS" reloader-test-control)"
before_unwatched="$(template_hash "$UNWATCHED_NS" reloader-test-unwatched)"

kubectl -n "$WATCHED_NS" patch secret reloader-test -p '{"stringData":{"value":"two"}}' >/dev/null
kubectl -n "$UNWATCHED_NS" patch secret reloader-test -p '{"stringData":{"value":"two"}}' >/dev/null
echo "[INFO] Rotated test Secrets; waiting for Reloader"

restarted=false
for _ in $(seq 1 30); do
  if [[ "$(template_hash "$WATCHED_NS" reloader-test-optin)" != "$before_optin" ]]; then restarted=true; break; fi
  sleep 2
done
sleep 15
if $restarted && kubectl -n "$WATCHED_NS" rollout status deploy/reloader-test-optin --timeout=2m >/dev/null; then
  ok "opted-in workload in $WATCHED_NS restarted after its Secret changed"
else
  bad "opted-in workload in $WATCHED_NS did not restart"
fi
[[ "$(template_hash "$WATCHED_NS" reloader-test-control)" == "$before_control" ]] \
  && ok "workload without the annotation was not restarted" || bad "unannotated workload was restarted"
[[ "$(template_hash "$UNWATCHED_NS" reloader-test-unwatched)" == "$before_unwatched" ]] \
  && ok "workload in unwatched namespace $UNWATCHED_NS was not restarted" || bad "workload outside the watched namespaces was restarted"

(( fail )) && exit 1
printf "\nReloader test passed.\n"
