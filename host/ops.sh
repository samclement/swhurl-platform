#!/usr/bin/env bash
# The operator commands that are one or two kubectl or flux calls (docs/commands.md):
#
#   ops.sh logs <app> <env>          make app-logs      (TAIL=N, FOLLOW=true, PREVIOUS=true)
#   ops.sh reconcile <app> <env>     make app-reconcile
#   ops.sh backup mongodb|sqlite     make backup-mongodb, make backup-sqlite
#
# DRY_RUN=true prints what reconcile and backup would change and changes nothing; logs has no dry run.
set -Eeuo pipefail

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

BACKUP_LABEL='platform.swhurl.com/backup=true'
MONGODB='observability/backup-mongodb'
BACKUP_WAIT=960  # seconds: the jobs' own activeDeadlineSeconds (900) and a minute to report it

backup() {  # <mongodb|sqlite>: a Job from each backup CronJob, run now; waits for them all and shows their logs
  local which=$1 targets=() target listed
  if [[ $which == mongodb ]]; then
    targets=("$MONGODB")
  else
    listed=$(kubectl get cronjobs --all-namespaces -l "$BACKUP_LABEL" \
      -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}')
    while IFS= read -r target; do
      [[ -n $target && $target != "$MONGODB" ]] && targets+=("$target")
    done <<<"$listed"
  fi
  if ((${#targets[@]} == 0)); then
    echo '[INFO] No app SQLite backup job: nothing to run.'
    return 0
  fi
  local suffix
  suffix=manual-$(date -u +%Y%m%dt%H%M%Sz)
  job() { local name="${1#*/}-$suffix"; echo "${name:0:63}"; }  # <namespace>/<cronjob>: its Job's name
  if dry; then
    echo 'Plan (backup jobs):'
    for target in "${targets[@]}"; do
      echo "  - kubectl -n ${target%%/*} create job $(job "$target") --from=cronjob/${target#*/}, wait, show its log"
    done
    return 0
  fi
  for target in "${targets[@]}"; do
    kubectl -n "${target%%/*}" create job "$(job "$target")" "--from=cronjob/${target#*/}"
  done
  local waiting=("${targets[@]}") failed=() still state deadline=$((SECONDS + BACKUP_WAIT))
  while ((${#waiting[@]})); do
    still=()
    for target in "${waiting[@]}"; do
      # Complete or Failed once the Job has finished; nothing while it runs (or while a read fails).
      state=$(kubectl -n "${target%%/*}" get job "$(job "$target")" \
        -o jsonpath='{range .status.conditions[?(@.status=="True")]}{.type}{"\n"}{end}' | grep -xE 'Complete|Failed' || true)
      if [[ -z $state ]] && ((SECONDS < deadline)); then
        still+=("$target")
        continue
      fi
      kubectl -n "${target%%/*}" logs "job/$(job "$target")" --tail=20 2>&1 | sed "s|^|  ${target%%/*}: |" || true
      if [[ $state == Complete ]]; then
        echo "[OK] ${target%%/*}/$(job "$target")"
      elif [[ $state == Failed ]]; then
        failed+=("${target%%/*}/$(job "$target")")
        echo "[ERROR] ${target%%/*}/$(job "$target") failed"
      else
        failed+=("${target%%/*}/$(job "$target")")
        echo "[ERROR] ${target%%/*}/$(job "$target") did not finish in $BACKUP_WAIT s"
      fi
    done
    waiting=("${still[@]}")
    if ((${#waiting[@]})); then sleep 5; fi
  done
  if ((${#failed[@]})); then
    echo "[ERROR] backup failed for ${failed[*]}" >&2
    return 1
  fi
}

case ${1:-} in
  logs | reconcile) "$1" "${2:-}" "${3:-}" ;;
  backup) [[ ${2:-} =~ ^(mongodb|sqlite)$ ]] || usage 'backup-mongodb | make backup-sqlite'; backup "$2" ;;
  *) echo "usage: $0 logs|reconcile <app> <env> | backup mongodb|sqlite" >&2; exit 2 ;;
esac
