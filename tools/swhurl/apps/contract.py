"""The app contract: rules the generator writes and the policy check enforces.

Both sides import these, so generated output and the check cannot drift apart.
"""
from __future__ import annotations

from pathlib import Path

from swhurl.platform import base_domain, label

CHART = 'app-template'
CHART_VERSION = '5.2.1'
CHART_REPOSITORY = 'bjw-s'

COOKIE_DOMAIN = base_domain()
"""Every host under this domain receives the shared sign-in cookie."""
AUTH_MIDDLEWARE = 'ingress-oauth-auth-shared@kubernetescrd'

EXPOSURES = ('private', 'authenticated-web', 'public')
ENVIRONMENTS = ('staging', 'prod')
RETAINED_STORAGE_CLASS = 'local-path-retain'
STORAGE_CLASSES = {'local-path', RETAINED_STORAGE_CLASS}

MANAGED = label('managed')
APP = label('app')
ENVIRONMENT = label('environment')
EXPOSURE = label('exposure')
EXCEPTIONS = label('policy-exceptions')

INSTANCE_ROOTS = (Path('apps'), Path('tests/fixtures/apps/apps'))
"""Where instances live: the real ones and the generated test fixtures."""


def in_cookie_domain(host: str) -> bool:
    return host == COOKIE_DOMAIN or host.endswith('.' + COOKIE_DOMAIN)
