#!/usr/bin/env bash
# Install or remove a host systemd timer: dns (Route 53 dynamic DNS, every 10
# minutes) or backup (MongoDB backup to S3, daily). Both are system units under
# /etc/systemd/system that run a script from this checkout as the invoking user,
# append their output to /var/log/swhurl-platform/<unit>.log (read by the OTel
# DaemonSet) and rotate it at 5 MiB.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly LOG_DIR=/var/log/swhurl-platform
readonly UNIT_DIR=/etc/systemd/system
readonly MARKER="Managed template for"

usage() { echo "Usage: ./host/install-timer.sh dns|backup [--dry-run] [--delete]"; }
info() { printf '[HOST][INFO] %s\n' "$*"; }
die() { printf '[HOST][ERROR] %s\n' "$*" >&2; exit 1; }
as_root() { if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then "$@"; else sudo "$@"; fi; }

NAME="" DRY_RUN=false DELETE=false
for arg in "$@"; do
  case "$arg" in
    dns|backup) NAME="$arg" ;;
    --dry-run) DRY_RUN=true ;;
    --delete) DELETE=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
case "$NAME" in
  dns) UNIT=aws-dns-updater TEMPLATE=dynamic-dns WHAT="dynamic DNS for the records in host/dns.env, every 10 minutes" ;;
  backup) UNIT=swhurl-backup-mongodb TEMPLATE=backup-mongodb WHAT="make backup-mongodb, daily at 03:30" ;;
  *) usage >&2; exit 2 ;;
esac

RUN_USER="${SUDO_USER:-$(id -un)}"
RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
[[ -n "$RUN_HOME" ]] || die "Could not find the home directory of $RUN_USER"

printf 'Host timer plan (%s):\n' "$([[ "$DELETE" == true ]] && echo delete || echo install)"
printf '  - units: %s/%s.service, %s.timer\n' "$UNIT_DIR" "$UNIT" "$UNIT"
printf '  - runs: %s, in %s as %s\n' "$WHAT" "$ROOT_DIR" "$RUN_USER"
printf '  - output: %s/%s.log\n' "$LOG_DIR" "$UNIT"
if [[ "$DRY_RUN" == true ]]; then
  echo "Dry run: nothing changed."
  exit 0
fi
if [[ "$(uname -s)" != Linux ]] || ! command -v systemctl >/dev/null; then
  die "Needs Linux with systemd"
fi

# Fail early when sudo cannot be used; sudo's own message says why.
if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  command -v sudo >/dev/null || die "Needs sudo"
  if [[ ! -t 0 ]] && ! sudo -n true 2>/dev/null; then
    die "sudo needs a password but stdin is not a terminal; run this in a terminal"
  fi
  sudo -v || die "sudo failed (see its message above)"
fi

for kind in service timer; do
  path="$UNIT_DIR/$UNIT.$kind"
  if [[ -e "$path" ]] && ! grep -q "$MARKER" "$path"; then
    die "$path exists and was not installed by this script; refusing to touch it"
  fi
done

# Files only the previous DNS installer created; removed if present.
remove_legacy_dns_files() {
  [[ "$NAME" == dns ]] || return 0
  local path
  for path in /etc/swhurl-platform/dynamic-dns.env "$RUN_HOME/.local/scripts/aws-dns-updater.sh"; do
    if [[ -e "$path" ]]; then
      as_root rm -f "$path"
      info "Removed unused $path"
    fi
  done
  as_root rmdir /etc/swhurl-platform 2>/dev/null || true
}

if [[ "$DELETE" == true ]]; then
  as_root systemctl disable --now "$UNIT.timer" "$UNIT.service" >/dev/null 2>&1 || true
  as_root rm -f "$UNIT_DIR/$UNIT.service" "$UNIT_DIR/$UNIT.timer"
  as_root systemctl daemon-reload
  remove_legacy_dns_files
  info "Removed $UNIT.timer and $UNIT.service (logs in $LOG_DIR are kept)"
  exit 0
fi

[[ -d "$LOG_DIR" ]] || as_root install -d -m 0755 "$LOG_DIR"
changed=false
for kind in service timer; do
  content="$(sed -e "s|__RUN_USER__|$RUN_USER|g" -e "s|__RUN_HOME__|$RUN_HOME|g" -e "s|__REPO_DIR__|$ROOT_DIR|g" \
    "$ROOT_DIR/host/templates/systemd/$TEMPLATE.$kind.tmpl")"
  path="$UNIT_DIR/$UNIT.$kind"
  if [[ -f "$path" ]] && cmp -s <(printf '%s\n' "$content") "$path"; then
    info "$UNIT.$kind already up to date"
    continue
  fi
  printf '%s\n' "$content" | as_root tee "$path" >/dev/null || die "Could not write $path"
  info "Wrote $path"
  changed=true
done
if [[ "$changed" == true ]]; then
  as_root systemctl daemon-reload
fi
# Only the timer starts the service; drop any boot-time link an older unit had.
as_root systemctl disable "$UNIT.service" >/dev/null 2>&1 || true
as_root systemctl enable --now "$UNIT.timer" >/dev/null
remove_legacy_dns_files
info "Installed $UNIT.timer. Run now: sudo systemctl start $UNIT. Output: $LOG_DIR/$UNIT.log and ClickStack."
