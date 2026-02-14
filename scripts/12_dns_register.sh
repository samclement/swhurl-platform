#!/usr/bin/env bash
set -Eeuo pipefail

# Idempotent DNS registration for one or more <subdomain>.swhurl.com names
# using a local aws-dns-updater script and systemd service/timer.
# - Linux + systemd only. On macOS or non-systemd, this is a no-op.
# - Supports multiple subdomains via SWHURL_SUBDOMAINS (space/comma-separated).
# - Backwards compatible with SWHURL_SUBDOMAIN (single value).

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
# Optionally layer a profile for overrides (domain/subdomains, etc.)
if [[ -n "${PROFILE_FILE:-}" && -f "$PROFILE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
elif [[ -f "$ROOT_DIR/profiles/local.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/local.env"
fi

DELETE_MODE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE_MODE=true ;;
    --help|-h)
      cat <<USAGE
Usage: $(basename "$0") [--delete]
  Install or remove a systemd service/timer that updates
  one or more <subdomain>.swhurl.com Route53 A records.

Env:
  SWHURL_SUBDOMAINS   Space or comma-separated subdomains (e.g. "homelab oauth.homelab clickstack.homelab hubble.homelab")
  SWHURL_SUBDOMAIN    Back-compat single subdomain (ignored if SWHURL_SUBDOMAINS is set)
  BASE_DOMAIN         If ends with .swhurl.com, defaults will be derived when SWHURL_SUBDOMAINS is empty
  PROFILE_FILE        Optional profile file (run.sh --profile) for overrides
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
LOCAL_HELPER_SOURCE="$SCRIPT_DIR/aws-dns-updater.sh"

mkdir -p "$HELPER_DIR"

# Determine subdomain list
subdomains_raw="${SWHURL_SUBDOMAINS:-}"
if [[ -z "$subdomains_raw" ]]; then
  if [[ -n "${SWHURL_SUBDOMAIN:-}" ]]; then
    subdomains_raw="$SWHURL_SUBDOMAIN"
  elif [[ -n "${BASE_DOMAIN:-}" && "$BASE_DOMAIN" =~ \.swhurl\.com$ ]]; then
    # Derive defaults from BASE_DOMAIN=<base>.swhurl.com
    base_subdomain="${BASE_DOMAIN%.swhurl.com}"
    base_subdomain="${base_subdomain%.}"
    subdomains_raw="$base_subdomain oauth.$base_subdomain clickstack.$base_subdomain hubble.$base_subdomain minio.$base_subdomain minio-console.$base_subdomain"
    warn "SWHURL_SUBDOMAINS not set; derived defaults: $subdomains_raw"
  else
    subdomains_raw="homelab"
    warn "SWHURL_SUBDOMAINS not set; defaulting to '$subdomains_raw'"
  fi
fi

# Normalize separators to spaces and quote each for ExecStart
subdomains_raw="${subdomains_raw//,/ }"
read -r -a SUBDOMAINS <<< "$subdomains_raw"
if [[ ${#SUBDOMAINS[@]} -eq 0 ]]; then
  die "No subdomains provided or derived"
fi

quoted_subdomains=()
for s in "${SUBDOMAINS[@]}"; do
  [[ -n "$s" ]] || continue
  quoted_subdomains+=("\"$s\"")
done

desired_execstart=("ExecStart=/bin/bash $HELPER_SCRIPT" "${quoted_subdomains[@]}")
desired_execstart_line="${desired_execstart[*]}"

create_or_update_unit() {
  local path="$1"; shift
  local content="$1"; shift
  local changed=0
  if [[ -f "$path" ]]; then
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
$desired_execstart_line
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
  needs_remove=false
  if [[ -f "$SERVICE_PATH" ]] && grep -q "^ExecStart=.*/aws-dns-updater.sh" "$SERVICE_PATH"; then
    needs_remove=true
  fi
  if [[ "$needs_remove" == true ]]; then
    sudo systemctl stop "$TIMER_NAME" "$SERVICE_NAME" || true
    sudo systemctl disable "$TIMER_NAME" "$SERVICE_NAME" >/dev/null || true
    sudo rm -f "$SERVICE_PATH" "$TIMER_PATH" || true
    sudo systemctl daemon-reload || true
    log "Removed systemd units"
  else
    log "Units not recognized as managed; nothing to delete."
  fi
  exit 0
fi

# Install/refresh helper script from this repo
if [[ ! -f "$LOCAL_HELPER_SOURCE" ]]; then
  die "Local helper script not found: $LOCAL_HELPER_SOURCE"
fi
if ! cmp -s "$LOCAL_HELPER_SOURCE" "$HELPER_SCRIPT" 2>/dev/null; then
  log "Installing helper script to $HELPER_SCRIPT"
  install -m 0755 "$LOCAL_HELPER_SOURCE" "$HELPER_SCRIPT"
else
  log "Helper script already up-to-date: $HELPER_SCRIPT"
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

if (( unit_changed == 1 )); then
  log "(Re)starting $SERVICE_NAME and $TIMER_NAME"
  sudo systemctl restart "$SERVICE_NAME" || true
  sudo systemctl restart "$TIMER_NAME" || true
else
  sudo systemctl start "$SERVICE_NAME" || true
  sudo systemctl start "$TIMER_NAME" || true
fi

log "Configured subdomains: ${SUBDOMAINS[*]} (under swhurl.com)"
log "Service status:"
sudo systemctl --no-pager --full status "$SERVICE_NAME" | sed -n '1,15p' || true
log "Timer status:"
sudo systemctl --no-pager --full status "$TIMER_NAME" | sed -n '1,15p' || true

log "Done."
