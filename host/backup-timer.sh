#!/usr/bin/env bash
# Install or remove the daily MongoDB backup as a system timer, like the
# dynamic DNS one. It runs `make backup-mongodb` from this checkout as the
# invoking user, with that user's kubeconfig and AWS credentials.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=host/lib.sh
source "$ROOT_DIR/host/lib.sh"

readonly UNIT=swhurl-backup-mongodb
readonly SERVICE_PATH="/etc/systemd/system/${UNIT}.service"
readonly TIMER_PATH="/etc/systemd/system/${UNIT}.timer"
readonly MARKER="Managed template for the ClickStack MongoDB backup"
DRY_RUN=false
DELETE=false

usage() { echo "Usage: ./host/backup-timer.sh [--dry-run] [--delete]"; }

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    --delete) DELETE=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

run_user="$(host_run_user)"
run_home="$(host_user_home "$run_user")"
printf 'Host backup timer plan:\n'
printf '  - mode: %s\n' "$([[ "$DELETE" == true ]] && echo delete || echo apply)"
printf '  - units: %s, %s\n' "$SERVICE_PATH" "$TIMER_PATH"
printf '  - runs: make backup-mongodb in %s as %s, daily at 03:30\n' "$ROOT_DIR" "$run_user"
if [[ "$DRY_RUN" == true ]]; then
  echo "Host backup timer dry run: exiting without executing."
  exit 0
fi
host_has_systemd || exit 0
host_require_sudo

for path in "$SERVICE_PATH" "$TIMER_PATH"; do
  if [[ -e "$path" ]] && ! grep -q "$MARKER" "$path"; then
    host_die "$path exists and is not managed by this script; refusing to touch it"
  fi
done

if [[ "$DELETE" == true ]]; then
  host_sudo systemctl disable --now "$UNIT.timer" >/dev/null 2>&1 || true
  host_sudo rm -f "$SERVICE_PATH" "$TIMER_PATH"
  host_sudo systemctl daemon-reload
  host_log_info "Removed $UNIT.timer (backups already taken are kept)"
  exit 0
fi

service="$(sed -e "s|__RUN_USER__|${run_user}|g" -e "s|__RUN_HOME__|${run_home}|g" -e "s|__REPO_DIR__|${ROOT_DIR}|g" \
  "$ROOT_DIR/host/templates/systemd/backup-mongodb.service.tmpl")"
timer="$(cat "$ROOT_DIR/host/templates/systemd/backup-mongodb.timer.tmpl")"
changed=0
host_write_if_changed "$SERVICE_PATH" "$service" && changed=1
host_write_if_changed "$TIMER_PATH" "$timer" && changed=1
if (( changed == 1 )); then
  host_sudo systemctl daemon-reload
fi
host_sudo systemctl enable --now "$UNIT.timer" >/dev/null
host_log_info "Installed $UNIT.timer; run now: sudo systemctl start $UNIT; logs: journalctl -u $UNIT"
