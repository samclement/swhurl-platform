#!/usr/bin/env bash
# Explicit lifecycle operations. Deployment and uninstall stay Git-driven;
# these commands cover what Git cannot express safely.
#
#   lifecycle.sh suspend kustomization/<name> | helmrelease/<ns>/<name>
#   lifecycle.sh resume  kustomization/<name> | helmrelease/<ns>/<name>
#   lifecycle.sh destroy-data pvc/<ns>/<name> | pv/<name>   (CONFIRM=<same target>)
set +x
set -Eeuo pipefail

DRY_RUN="${DRY_RUN:-false}"
CONFIRM="${CONFIRM:-}"
FLUX_NS=flux-system

die() { echo "[ERROR] $*" >&2; exit 2; }
usage() { sed -n '4,7p' "$0" >&2; exit 2; }

action="${1:-}"; target="${2:-}"
[[ -n "$action" && -n "$target" ]] || usage
IFS=/ read -r kind a b extra <<< "$target"
[[ -z "$extra" ]] || die "Invalid target: $target"

# Validate the target shape before touching the cluster.
case "$action/$kind" in
  suspend/kustomization|resume/kustomization) [[ -n "$a" && -z "$b" ]] || die "Use kustomization/<name>" ;;
  suspend/helmrelease|resume/helmrelease) [[ -n "$a" && -n "$b" ]] || die "Use helmrelease/<namespace>/<name>" ;;
  destroy-data/pvc) [[ -n "$a" && -n "$b" ]] || die "Use pvc/<namespace>/<name>" ;;
  destroy-data/pv) [[ -n "$a" && -z "$b" ]] || die "Use pv/<name>" ;;
  *) usage ;;
esac
if [[ "$action" == "destroy-data" && "$CONFIRM" != "$target" ]]; then
  die "destroy-data permanently deletes $target and its data. Re-run with CONFIRM=$target"
fi

run() {
  if [[ "$DRY_RUN" == "true" ]]; then echo "  would run: $*"; else "$@"; fi
}

suspend_resume() {
  local ns name
  if [[ "$kind" == "kustomization" ]]; then ns="$FLUX_NS"; name="$a"; else ns="$a"; name="$b"; fi
  kubectl -n "$ns" get "$kind" "$name" >/dev/null || die "$kind $ns/$name not found"
  if [[ "$DRY_RUN" == "true" ]]; then echo "Plan ($action $target):"; fi
  run flux "$action" "$kind" "$name" -n "$ns"
  if [[ "$action" == "suspend" ]]; then
    echo "[INFO] Git changes stop applying to $target; running workloads and data are untouched."
    if [[ "$kind" == "kustomization" ]]; then
      echo "[INFO] HelmReleases it created keep reconciling their last applied spec; suspend them separately to freeze Helm."
    fi
  fi
}

# Refuse while anything would recreate or is still using the claim.
check_claim_released() {
  local ns="$1" pvc="$2" json release owner
  json="$(kubectl -n "$ns" get pvc "$pvc" -o json)" || die "PVC $ns/$pvc not found"
  # Running, pending or terminating pods still hold the volume; finished ones do not.
  if kubectl -n "$ns" get pods -o json | jq -e --arg pvc "$pvc" \
    '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | .spec.volumes[]?.persistentVolumeClaim.claimName] | index($pvc)' >/dev/null; then
    die "PVC $ns/$pvc is mounted by a pod; stop or uninstall the workload first"
  fi
  release="$(jq -r '.metadata.annotations["meta.helm.sh/release-name"] // empty' <<< "$json")"
  if [[ -n "$release" ]] && kubectl -n "$ns" get secret -l "owner=helm,name=$release" -o name | grep -q .; then
    die "Helm release $ns/$release still exists and would recreate the claim; uninstall it first"
  fi
  owner="$(jq -r '.metadata.labels["kustomize.toolkit.fluxcd.io/name"] // empty' <<< "$json")"
  if [[ -n "$owner" ]] && kubectl -n "$(jq -r '.metadata.labels["kustomize.toolkit.fluxcd.io/namespace"] // "flux-system"' <<< "$json")" get kustomization "$owner" >/dev/null 2>&1; then
    die "Flux Kustomization $owner still manages the claim; remove it from Git first"
  fi
}

destroy_data() {
  local pv
  command -v jq >/dev/null 2>&1 || die "Missing required command: jq"
  if [[ "$kind" == "pvc" ]]; then
    check_claim_released "$a" "$b"
    pv="$(kubectl -n "$a" get pvc "$b" -o jsonpath='{.spec.volumeName}')"
  else
    pv="$a"
    [[ "$(kubectl get pv "$pv" -o jsonpath='{.status.phase}')" == "Released" ]] \
      || die "PV $pv is not Released; destroy its claim with pvc/<namespace>/<name> instead"
  fi
  if [[ "$DRY_RUN" == "true" ]]; then echo "Plan (destroy-data $target): all checks passed"; fi
  # Switching to Delete makes the provisioner remove the host directory once
  # the volume is released; with Retain it would be left behind.
  if [[ -n "$pv" ]]; then
    run kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
  fi
  if [[ "$kind" == "pvc" ]]; then
    run kubectl -n "$a" delete pvc "$b" --wait=true --timeout=2m
  fi
  if [[ -n "$pv" ]]; then
    run kubectl wait --for=delete "pv/$pv" --timeout=2m
  fi
  if [[ "$DRY_RUN" != "true" ]]; then echo "[OK] Destroyed $target${pv:+ (PV $pv and its data)}"; fi
}

command -v kubectl >/dev/null 2>&1 || die "Missing required command: kubectl"
case "$action" in
  suspend|resume) command -v flux >/dev/null 2>&1 || die "Missing required command: flux"; suspend_resume ;;
  destroy-data) destroy_data ;;
esac
