#!/usr/bin/env bash
set -Eeuo pipefail

# Idempotent DNS registration for <subdomain>.swhurl.com using the
# aws-dns-updater systemd service/timer from @samclement's gist.
# - Linux + systemd only. On macOS or non-systemd, this is a no-op.
# - Uses $SWHURL_SUBDOMAIN if set, otherwise defaults to "homelab".
# - Rewrites unit files only when the configured subdomain/user differs.

log() { printf "[%s] %s\n" "$(date +'%Y-%m-%dT%H:%M:%S%z')" "$*"; }
warn() { log "WARN: $*"; }
die() { log "ERROR: $*" >&2; exit 1; }

# Load config.env if present (repo root is one level up from scripts/)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
if [[ -f "$ROOT_DIR/config.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/config.env"
fi

SWHURL_SUBDOMAIN="${SWHURL_SUBDOMAIN:-homelab}"

DELETE_MODE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE_MODE=true ;;
    --help|-h)
      cat <<USAGE
Usage: $(basename "$0") [--delete]
  Idempotently install or remove a systemd service/timer that updates
  <subdomain>.swhurl.com via Route53 using aws-dns-updater.sh.

Env:
  SWHURL_SUBDOMAIN   Subdomain (default: homelab)
USAGE
      exit 0
      ;;
  esac
done

OS="$(uname -s || true)"
if [[ "$OS" != "Linux" ]]; then
  log "Non-Linux host detected ($OS); skipping DNS registration."
  exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
  log "systemd not available; skipping DNS registration."
  exit 0
fi

USER_NAME="${SUDO_USER:-$(id -un)}"
USER_HOME="$(getent passwd "$USER_NAME" | cut -d: -f6 || echo "$HOME")"
[[ -n "$USER_HOME" ]] || die "Could not determine home directory for $USER_NAME"

SERVICE_NAME="aws-dns-updater.service"
TIMER_NAME="aws-dns-updater.timer"
SERVICE_PATH="/etc/systemd/system/$SERVICE_NAME"
TIMER_PATH="/etc/systemd/system/$TIMER_NAME"

HELPER_DIR="$USER_HOME/.local/scripts"
HELPER_SCRIPT="$HELPER_DIR/aws-dns-updater.sh"
HELPER_URL="https://gist.githubusercontent.com/samclement/9bef6ded89ede2085439cbde97e532b8/raw"

mkdir -p "$HELPER_DIR"

desired_execstart="ExecStart=/bin/bash $HELPER_SCRIPT $SWHURL_SUBDOMAIN"

create_or_update_unit() {
  local path="$1"; shift
  local content="$1"; shift
  local changed=0
  if [[ -f "$path" ]]; then
    # Compare current content with desired; update only if different
    if ! diff -q <(printf "%s" "$content") "$path" >/dev/null 2>&1; then
      log "Updating $(basename "$path")"
      printf "%s" "$content" | sudo tee "$path" >/dev/null
      changed=1
    else
      log "$(basename "$path") already up-to-date"
    fi
  else
    log "Creating $(basename "$path")"
    printf "%s" "$content" | sudo tee "$path" >/dev/null
    changed=1
  fi
  return $changed
}

unit_changed=0

service_content="# Managed-By: swhurl-platform
[Unit]
Description=AWS Dynamic DNS Update Service
After=network.target

[Service]
Type=simple
$desired_execstart
User=$USER_NAME
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
"

timer_content="# Managed-By: swhurl-platform
[Unit]
Description=Run AWS Dynamic DNS Update Service every 10 minutes

[Timer]
OnBootSec=10min
OnUnitActiveSec=10min
AccuracySec=1min

[Install]
WantedBy=timers.target
"

if [[ "$DELETE_MODE" == true ]]; then
  log "Deleting DNS updater units if managed by this repo"
  # Only stop/disable/remove if units reference our HELPER_SCRIPT and subdomain
  needs_remove=false
  if [[ -f "$SERVICE_PATH" ]] && grep -q "^ExecStart=.*/aws-dns-updater.sh $SWHURL_SUBDOMAIN$" "$SERVICE_PATH"; then
    needs_remove=true
  fi
  if [[ "$needs_remove" == true ]]; then
    sudo systemctl stop "$TIMER_NAME" "$SERVICE_NAME" || true
    sudo systemctl disable "$TIMER_NAME" "$SERVICE_NAME" >/dev/null || true
    sudo rm -f "$SERVICE_PATH" "$TIMER_PATH" || true
    sudo systemctl daemon-reload || true
    log "Removed systemd units for ${SWHURL_SUBDOMAIN}.swhurl.com"
  else
    log "Units not managed for subdomain '$SWHURL_SUBDOMAIN'; nothing to delete."
  fi
  exit 0
fi

# Ensure helper script exists and is executable (after potential delete-mode early exit)
if [[ ! -x "$HELPER_SCRIPT" ]]; then
  log "Installing helper script to $HELPER_SCRIPT"
  curl -fsSL "$HELPER_URL" -o "$HELPER_SCRIPT" || die "Failed to download helper script"
  chmod +x "$HELPER_SCRIPT"
else
  log "Helper script already present: $HELPER_SCRIPT"
fi

if create_or_update_unit "$SERVICE_PATH" "$service_content"; then unit_changed=1; fi
if create_or_update_unit "$TIMER_PATH" "$timer_content"; then unit_changed=1; fi

if (( unit_changed == 1 )); then
  log "Reloading systemd units"
  sudo systemctl daemon-reload
fi

# Enable and start units (idempotent)
sudo systemctl enable "$SERVICE_NAME" >/dev/null || true
sudo systemctl enable "$TIMER_NAME" >/dev/null || true

# If service is running and config changed, restart; otherwise ensure started
if (( unit_changed == 1 )); then
  log "(Re)starting $SERVICE_NAME and $TIMER_NAME"
  sudo systemctl restart "$SERVICE_NAME" || true
  sudo systemctl restart "$TIMER_NAME" || true
else
  sudo systemctl start "$SERVICE_NAME" || true
  sudo systemctl start "$TIMER_NAME" || true
fi

# Status summary
log "Configured domain: ${SWHURL_SUBDOMAIN}.swhurl.com"
log "Service status:"
sudo systemctl --no-pager --full status "$SERVICE_NAME" | sed -n '1,15p' || true
log "Timer status:"
sudo systemctl --no-pager --full status "$TIMER_NAME" | sed -n '1,15p' || true

log "Done."
