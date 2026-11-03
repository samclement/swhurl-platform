#!/usr/bin/env bash
# The operator commands that are one or two kubectl or flux calls (docs/commands.md):
#
#   ops.sh logs <app> <env>          make app-logs      (TAIL=N, FOLLOW=true, PREVIOUS=true)
#   ops.sh reconcile <app> <env>     make app-reconcile
#   ops.sh suspend|resume <target>   make suspend|resume TARGET=kustomization/<name>|helmrelease/<ns>/<name>
#
# DRY_RUN=true prints what reconcile, suspend and resume would change and changes nothing; logs has no dry run.
set -Eeuo pipefail

NAME='[a-z0-9]([-a-z0-9.]*[a-z0-9])?'  # a Kubernetes object name, so none reads as a flag

usage() { echo "Usage: make $1" >&2; exit 2; }
refuse() { echo "[ERROR] $1" >&2; exit 2; }
dry() { [[ ${DRY_RUN:-false} == true ]]; }

# change: a command that changes the cluster, printed instead of run under DRY_RUN=true.
change() {
  if dry; then echo "  would run: $*"; else "$@"; fi
}

instance() {  # <make target> <app> <env>: refuses anything but an app name and staging or prod
  [[ -n $2 && -n $3 ]] || usage "$1 APP=<app> ENV=<env>"
  [[ $2 =~ ^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$ && $3 =~ ^(staging|prod)$ ]] \
    || refuse "no instance $2/$3: APP must be a DNS label and ENV one of staging, prod"
}

logs() {
  instance app-logs "$1" "$2"
  dry && refuse 'make app-logs has no dry run: DRY_RUN=true would be ignored, so nothing was done'
  local flags=(--all-containers "--tail=${TAIL:-100}")
  [[ ${PREVIOUS:-false} == true ]] && flags+=(--previous)
  [[ ${FOLLOW:-false} == true ]] && flags+=(--follow)
  exec kubectl -n "$1-$2" logs "deploy/$1" "${flags[@]}"
}

reconcile() {
  instance app-reconcile "$1" "$2"
  dry && echo "Plan (app-reconcile $1/$2):"
  change flux reconcile source git swhurl-platform -n flux-system --timeout=5m
  change flux reconcile kustomization "app-$1-$2" -n flux-system --timeout=10m
}

suspend_resume() {  # <suspend|resume> <target>
  local action=$1 text=$2 kind namespace name
  if [[ $text == kustomization/* ]]; then
    kind=kustomization namespace=flux-system name=${text#kustomization/}
    [[ $name =~ ^$NAME$ ]] || refuse 'Use kustomization/<name>'
  elif [[ $text == helmrelease/* ]]; then
    local rest=${text#helmrelease/}
    kind=helmrelease namespace=${rest%%/*} name=${rest#*/}
    [[ $rest == */* && $namespace =~ ^$NAME$ && $name =~ ^$NAME$ ]] || refuse 'Use helmrelease/<namespace>/<name>'
  else
    usage "$action TARGET=kustomization/<name>|helmrelease/<ns>/<name>"
  fi
  kubectl -n "$namespace" get "$kind" "$name" >/dev/null 2>&1 || refuse "$kind $namespace/$name not found"
  dry && echo "Plan ($action $text):"
  change flux "$action" "$kind" "$name" -n "$namespace"
  if [[ $action == suspend ]] && ! dry; then
    echo "[INFO] Git changes stop applying to $text; running workloads and data are untouched."
    [[ $kind == kustomization ]] && echo '[INFO] HelmReleases it created keep reconciling their last applied spec; suspend them separately to freeze Helm.'
  fi
  return 0
}

case ${1:-} in
  logs | reconcile) "$1" "${2:-}" "${3:-}" ;;
  suspend | resume) suspend_resume "$1" "${2:-}" ;;
  *) echo "usage: $0 logs|reconcile <app> <env> | suspend|resume <target>" >&2; exit 2 ;;
esac
