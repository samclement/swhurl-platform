"""Calls between apps (docs/apps.md#calling-another-app): the variables, token files and
names that follow from an app declaring which apps it calls.

The truth is calls on the caller and allows on the callee in instance.yaml. A pair connects
only when both agree (allows may name the caller or its team). make app-sync renders the
SWHURL_SERVICE_<APP>_URL variable, a short-lived service-account token only that callee accepts,
the callee's allowed-callers variable and its NetworkPolicy. A call without a grant, a grant
without a call or a call to an absent instance renders nothing for the pair. Every instance
is told who it is; a web app also gets the token issuer and the cluster's public signing keys.
This module renders and reads calls; interface.py owns their variable and token-path names.
"""
from __future__ import annotations

import json
from pathlib import Path

from swhurl import ROOT, interface

NOTIFY = 'notify'
"""The notify service (docs/services.md#notify-service): every instance gets its address and a token
for this audience, so its code can send a message to its team's channel. No app may have this name."""
NOTIFY_URL = f'http://{NOTIFY}.{NOTIFY}.svc.cluster.local:8080'
TOKENS_VOLUME = 'tokens'
TOKEN_SECONDS = 600  # the shortest the API server issues; the kubelet replaces the file at 80%
ISSUER = 'https://kubernetes.default.svc.cluster.local'
"""This cluster's service-account token issuer (make verify-platform compares it with the live one)."""
SIGNING_KEYS_FILE = Path('apps/signing-keys.json')
"""The cluster's public token-signing keys. Apps cannot fetch them (the API refuses anonymous
requests), so they are kept here and given to each web app; make platform-signing-keys refreshes it."""


def callee_address(app: str, port: int) -> str:
    """The callee in the caller's own environment; Kubernetes expands $(SWHURL_ENV), which must be defined first."""
    return f'http://{app}.{app}-$({interface.ENVIRONMENT_VARIABLE}).svc.cluster.local:{port}'


def declared_calls(variables: dict) -> list[str]:
    """The apps a container's variables say it calls, in order (app names have no underscores, so this reverses interface.callee_variable)."""
    return [match[1].lower().replace('_', '-') for key in variables or {} if (match := interface.SERVICE_VARIABLE_RE.match(str(key)))]


def allowed_callers(variables: dict) -> list[str]:
    return [name for name in str((variables or {}).get(interface.CALLERS_VARIABLE, '')).split(',') if name]


def signing_keys(root: Path = ROOT) -> str:
    """The public key set as one line of JSON, from ``root`` (a scratch root falls back to this checkout); '' if absent."""
    for base in (root, ROOT):
        path = base / SIGNING_KEYS_FILE
        if path.is_file():
            return json.dumps(json.loads(path.read_text()), sort_keys=True, separators=(',', ':'))
    return ''


def identity_variables(app: str, env: str, web: bool, keys: str) -> dict:
    """What every instance is told about itself and where the notify service is, and what a web app
    needs to check a caller's token."""
    identity = {interface.APP_VARIABLE: app, interface.ENVIRONMENT_VARIABLE: env, interface.NOTIFY_VARIABLE: NOTIFY_URL}
    return identity | ({interface.ISSUER_VARIABLE: ISSUER, interface.KEYS_VARIABLE: keys} if web else {})


def token_audiences(callees: list[str]) -> list[str]:
    """Whom an instance carries a token for: each app it calls, and the notify service."""
    return [*callees, NOTIFY]


def tokens_volume(callees: list[str]) -> dict:
    """One token file per audience at interface.TOKENS_MOUNT/<audience>/token, each valid for that one only: a
    token with an audience is refused by the Kubernetes API itself."""
    return {'type': 'projected',
            'sources': [{'serviceAccountToken': {'audience': audience, 'expirationSeconds': TOKEN_SECONDS,
                                                 'path': f'{audience}/token'}} for audience in token_audiences(callees)],
            'globalMounts': [{'path': interface.TOKENS_MOUNT, 'readOnly': True}]}
