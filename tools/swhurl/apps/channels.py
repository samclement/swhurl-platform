"""Notification channels and which team's apps notify them (docs/services.md#notification-channels).

A channel is a named destination: one SOPS-encrypted Secret in platform/notify/channels whose address is the
credential (an ntfy topic URL). v2 owns the team registry (v2-records/teams.json, written by swhurl-v2 team) and
renders ROUTES_FILE from it, names only; its render refuses a tree whose channel Secrets and registry disagree. This
module reads what Git holds and decides nothing.
"""
from __future__ import annotations

import json
from pathlib import Path

from swhurl import interface

NOTIFY_DIR = Path('platform/notify')
ROUTES_FILE = NOTIFY_DIR / 'routes.json'
CHANNELS_DIR = NOTIFY_DIR / 'channels'
NAMESPACE = 'notify'
ROUTES_NAME = 'notification-routes'
ROUTES_KEY = 'routes.json'
ADDRESS_KEY = 'address'
TRANSPORT = interface.label('notify-transport')


def routes_text(text: str | bytes) -> dict:
    """The routes a ConfigMap's text states: each channel with its transport, and each team's channels."""
    return json.loads(json.loads(text)['data'][ROUTES_KEY])


def routes(root: Path) -> dict | None:
    """The routes Git holds, or None where this root has no notify unit (the scratch roots of tests)."""
    path = root / ROUTES_FILE
    return routes_text(path.read_text()) if path.is_file() else None
