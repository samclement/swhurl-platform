#!/usr/bin/env bash
# Install or remove the daily MongoDB backup as a systemd user timer. It runs
# `make backup-mongodb` from this checkout as the current user, with that
# user's kubeconfig and AWS credentials. Needs lingering to run while logged out.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT=swhurl-backup-mongodb
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
MARKER="Managed template for the ClickStack MongoDB backup"
DRY_RUN=false
DELETE=false

usage() { echo "Usage: ./host/backup-timer.sh [--dry-run] [--delete]"; }
info() { printf '[HOST][INFO] %s\n' "$*"; }
die() { printf '[HOST][ERROR] %s\n' "$*" >&2; exit 1; }
run() { if [[ "$DRY_RUN" == true ]]; then echo "  would run: $*"; else "$@"; fi; }

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    --delete) DELETE=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

for kind in service timer; do
  target="$UNIT_DIR/$UNIT.$kind"
  if [[ -e "$target" ]] && ! grep -q "$MARKER" "$target"; then
    die "$target exists and is not managed by this script; refusing to touch it"
  fi
done

if [[ "$DELETE" == true ]]; then
  run systemctl --user disable --now "$UNIT.timer"
  run rm -f "$UNIT_DIR/$UNIT.service" "$UNIT_DIR/$UNIT.timer"
  run systemctl --user daemon-reload
  [[ "$DRY_RUN" == true ]] || info "Removed $UNIT.timer (backups already taken are kept)"
  exit 0
fi

run mkdir -p "$UNIT_DIR"
for kind in service timer; do
  rendered="$(sed "s|__REPO_DIR__|$ROOT_DIR|" "$ROOT_DIR/host/templates/systemd/backup-mongodb.$kind.tmpl")"
  if [[ "$DRY_RUN" == true ]]; then
    echo "  would write $UNIT_DIR/$UNIT.$kind"
  else
    printf '%s\n' "$rendered" > "$UNIT_DIR/$UNIT.$kind"
  fi
done
run systemctl --user daemon-reload
run systemctl --user enable --now "$UNIT.timer"
[[ "$DRY_RUN" == true ]] && exit 0
info "Installed $UNIT.timer (daily at 03:30; run now: systemctl --user start $UNIT)"
if [[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null)" != yes ]]; then
  info "Lingering is off, so the timer runs only while you are logged in. Enable once: sudo loginctl enable-linger $USER"
fi
