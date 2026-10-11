"""Calls between apps (docs/apps.md#calling-another-app): the variables, token files and
names that follow from an app declaring which apps it calls.

A caller's declaration is a variable on its HelmRelease, SWHURL_SERVICE_<APP>_URL, holding the
callee's address. For each it also gets a short-lived service-account token only that callee
accepts. Every instance is told who it is; a web app is also told who issues tokens, the
cluster's public signing keys and who may call it. The generator, the derivation (derive.py) and
the policy check all read these from here.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

from swhurl import ROOT

APP_VARIABLE = 'SWHURL_APP'
ENVIRONMENT_VARIABLE = 'SWHURL_ENV'
ISSUER_VARIABLE = 'SWHURL_AUTH_ISSUER'
KEYS_VARIABLE = 'SWHURL_AUTH_KEYS'
CALLERS_VARIABLE = 'SWHURL_ALLOWED_CALLERS'
API_VARIABLE = 'SWHURL_API_PATH'
"""Where a web app serves its API description, if it says so (provides.api): shown on the console's Services page."""
TOKENS_MOUNT = '/var/run/swhurl/tokens'  # the stack templates' helpers read <this>/<callee>/token
TOKENS_VOLUME = 'tokens'
TOKEN_SECONDS = 600  # the shortest the API server issues; the kubelet replaces the file at 80%
ISSUER = 'https://kubernetes.default.svc.cluster.local'
"""This cluster's service-account token issuer (make verify-platform compares it with the live one)."""
SIGNING_KEYS_FILE = Path('apps/signing-keys.json')
"""The cluster's public token-signing keys. Apps cannot fetch them (the API refuses anonymous
requests), so they are kept here and given to each web app; make platform-signing-keys refreshes it."""
_CALLEE_VARIABLE = re.compile(r'^SWHURL_SERVICE_([A-Z0-9_]+)_URL$')


def callee_variable(app: str) -> str:
    """The variable that holds an app's address: orders-api -> SWHURL_SERVICE_ORDERS_API_URL."""
    return f'SWHURL_SERVICE_{app.upper().replace("-", "_")}_URL'


def callee_address(app: str, port: int) -> str:
    """The callee in the caller's own environment; Kubernetes expands $(SWHURL_ENV), which must be defined first."""
    return f'http://{app}.{app}-$({ENVIRONMENT_VARIABLE}).svc.cluster.local:{port}'


def declared_calls(variables: dict) -> list[str]:
    """The apps a container's variables say it calls, in order (app names have no underscores, so this reverses callee_variable)."""
    return [match[1].lower().replace('_', '-') for key in variables or {} if (match := _CALLEE_VARIABLE.match(str(key)))]


def allowed_callers(variables: dict) -> list[str]:
    return [name for name in str((variables or {}).get(CALLERS_VARIABLE, '')).split(',') if name]


def signing_keys(root: Path = ROOT) -> str:
    """The public key set as one line of JSON, from ``root`` (a scratch root falls back to this checkout); '' if absent."""
    for base in (root, ROOT):
        path = base / SIGNING_KEYS_FILE
        if path.is_file():
            return json.dumps(json.loads(path.read_text()), sort_keys=True, separators=(',', ':'))
    return ''


def identity_variables(app: str, env: str, web: bool, keys: str) -> dict:
    """What every instance is told about itself, and what a web app needs to check a caller's token."""
    identity = {APP_VARIABLE: app, ENVIRONMENT_VARIABLE: env}
    return identity | ({ISSUER_VARIABLE: ISSUER, KEYS_VARIABLE: keys} if web else {})


def tokens_volume(callees: list[str]) -> dict:
    """One token file per callee at TOKENS_MOUNT/<callee>/token, each valid for that callee only: a
    token with an audience is refused by the Kubernetes API itself."""
    return {'type': 'projected',
            'sources': [{'serviceAccountToken': {'audience': callee, 'expirationSeconds': TOKEN_SECONDS,
                                                 'path': f'{callee}/token'}} for callee in callees],
            'globalMounts': [{'path': TOKENS_MOUNT, 'readOnly': True}]}
