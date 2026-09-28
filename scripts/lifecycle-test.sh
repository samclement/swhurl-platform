#!/usr/bin/env bash
# Prove the lifecycle contract on a disposable app (tests/fixtures/lifecycle-app,
# reconciled from the pushed Git revision). Touches only resources it creates:
# Flux Kustomizations named lifecycle-test*, namespace lifecycle-test and its PVs.
#
#   1. suspend leaves the workload and data running; resume reconciles again
#   2. uninstall (deleting an app-style unit) removes the workload but keeps
#      the prune-protected namespace, claim and data
#   3. destroy-data refuses without CONFIRM, then deletes the claim, PV and data
#   4. a unit with deletionPolicy: Orphan (as the shared units use) leaves its
#      resources running when the unit itself is deleted
set +x
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS=lifecycle-test
UNIT=lifecycle-test
ORPHAN_UNIT=lifecycle-test-orphan
LABEL=platform.swhurl.com/lifecycle-test
LIFECYCLE=(env PYTHONPATH="$ROOT/tools" python3 -m swhurl lifecycle)

if [[ "${DRY_RUN:-false}" == "true" ]]; then
  sed -n '2,12p' "$0"
  exit 0
fi
for cmd in kubectl flux jq; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done

fail=0
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
step() { printf "\n== %s ==\n" "$1"; }

if kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl -n flux-system get kustomization "$UNIT" "$ORPHAN_UNIT" >/dev/null 2>&1; then
  echo "[ERROR] $NS namespace or $UNIT Kustomizations already exist; clean up first" >&2
  exit 1
fi

cleanup() {
  kubectl -n flux-system delete kustomization "$UNIT" "$ORPHAN_UNIT" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  if [[ "$(kubectl get namespace "$NS" -o jsonpath="{.metadata.labels.${LABEL//./\\.}}" 2>/dev/null)" == "true" ]]; then
    kubectl delete namespace "$NS" --wait=true --timeout=2m >/dev/null 2>&1 || true
  fi
  for pv in $(kubectl get pv -o json | jq -r --arg ns "$NS" '.items[] | select(.spec.claimRef.namespace == $ns) | .metadata.name'); do
    kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null 2>&1 || true
    kubectl wait --for=delete "pv/$pv" --timeout=2m >/dev/null 2>&1 || kubectl delete pv "$pv" --ignore-not-found >/dev/null 2>&1 || true
  done
  echo "[INFO] Cleaned up $NS test resources"
}
trap cleanup EXIT

apply_unit() {  # name deletionPolicy
  kubectl apply -f - >/dev/null <<EOF
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: $1
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: GitRepository
    name: swhurl-platform
  path: ./tests/fixtures/lifecycle-app
  prune: true
  deletionPolicy: $2
  wait: true
  timeout: 5m
EOF
  kubectl -n flux-system wait --for=condition=Ready "kustomization/$1" --timeout=6m >/dev/null
}
marker() {  # read the marker file through a short-lived pod
  kubectl -n "$NS" delete pod reader --ignore-not-found >/dev/null
  kubectl -n "$NS" run reader --image=busybox:1.37 --restart=Never --quiet \
    --overrides='{"spec":{"automountServiceAccountToken":false,"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"data"}}],"containers":[{"name":"reader","image":"busybox:1.37","command":["cat","/data/marker"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}]}}' >/dev/null
  kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded pod/reader --timeout=2m >/dev/null
  kubectl -n "$NS" logs reader
  kubectl -n "$NS" delete pod reader --wait=true >/dev/null
}

step "Install disposable app"
apply_unit "$UNIT" MirrorPrune
kubectl -n "$NS" rollout status deploy/writer --timeout=3m >/dev/null
original="$(kubectl -n "$NS" exec deploy/writer -- cat /data/marker)"
pv="$(kubectl -n "$NS" get pvc data -o jsonpath='{.spec.volumeName}')"
[[ -n "$original" ]] && ok "workload wrote data to $pv" || bad "no marker written"

step "Suspend and resume"
"${LIFECYCLE[@]}" suspend "kustomization/$UNIT" >/dev/null
[[ "$(kubectl -n flux-system get kustomization "$UNIT" -o jsonpath='{.spec.suspend}')" == "true" ]] && ok "unit suspended" || bad "unit not suspended"
[[ "$(kubectl -n "$NS" get deploy writer -o jsonpath='{.status.availableReplicas}')" == "1" ]] && ok "workload still running while suspended" || bad "workload stopped while suspended"
"${LIFECYCLE[@]}" resume "kustomization/$UNIT" >/dev/null
kubectl -n flux-system wait --for=condition=Ready "kustomization/$UNIT" --timeout=3m >/dev/null && ok "unit resumed and Ready" || bad "unit not Ready after resume"

step "Uninstall (delete app unit)"
kubectl -n flux-system delete kustomization "$UNIT" --wait=true >/dev/null
kubectl -n "$NS" wait --for=delete deploy/writer --timeout=2m >/dev/null 2>&1 && ok "workload pruned" || bad "workload not pruned"
kubectl -n "$NS" wait --for=delete pod -l app=writer --timeout=2m >/dev/null 2>&1 || true
kubectl get namespace "$NS" >/dev/null 2>&1 && ok "prune-protected namespace kept" || bad "namespace deleted"
[[ "$(kubectl -n "$NS" get pvc data -o jsonpath='{.status.phase}' 2>/dev/null)" == "Bound" ]] && ok "prune-protected claim kept" || bad "claim deleted"
[[ "$(marker)" == "$original" ]] && ok "data intact after uninstall" || bad "data changed or missing after uninstall"

step "Destroy data"
if CONFIRM= "${LIFECYCLE[@]}" destroy-data "pvc/$NS/data" >/dev/null 2>&1; then bad "destroy-data ran without CONFIRM"; else ok "destroy-data refuses without CONFIRM"; fi
[[ "$(kubectl -n "$NS" get pvc data -o jsonpath='{.metadata.name}' 2>/dev/null)" == "data" ]] && ok "claim untouched by refused destroy" || bad "claim removed by refused destroy"
CONFIRM="pvc/$NS/data" "${LIFECYCLE[@]}" destroy-data "pvc/$NS/data" >/dev/null
! kubectl -n "$NS" get pvc data >/dev/null 2>&1 && ok "claim destroyed" || bad "claim still exists"
! kubectl get pv "$pv" >/dev/null 2>&1 && ok "PV $pv and its data destroyed" || bad "PV $pv still exists"

step "Orphan deletion policy (shared units)"
apply_unit "$ORPHAN_UNIT" Orphan
kubectl -n "$NS" rollout status deploy/writer --timeout=3m >/dev/null
kubectl -n flux-system delete kustomization "$ORPHAN_UNIT" --wait=true >/dev/null
sleep 10
[[ "$(kubectl -n "$NS" get deploy writer -o jsonpath='{.status.availableReplicas}' 2>/dev/null)" == "1" ]] && ok "workload kept running after its Orphan unit was deleted" || bad "Orphan unit deletion removed the workload"

(( fail )) && exit 1
printf "\nLifecycle test passed.\n"
