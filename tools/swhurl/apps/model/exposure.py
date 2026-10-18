"""Exposure: a signed-in or public instance gets a route (Ingress) with TLS, and Traefik may reach it."""
from __future__ import annotations

from swhurl.apps.contract import AUTH_MIDDLEWARE, default_host
from swhurl.apps.model.draft import Draft, Entry

DEFAULT_ISSUER = 'letsencrypt-prod'
OAUTH_UNIT = 'platform-oauth2-proxy'
ROUTED = ('authenticated-web', 'public')


def ingress_values(name: str, host: str, exposure: str, issuer: str) -> dict:
    """The app-template route for a signed-in or public instance."""
    annotations = {'cert-manager.io/cluster-issuer': issuer}
    if exposure == 'authenticated-web':
        annotations['traefik.ingress.kubernetes.io/router.middlewares'] = AUTH_MIDDLEWARE
    return {
        'className': 'traefik',
        'annotations': annotations,
        'hosts': [{'host': host, 'paths': [{'path': '/', 'service': {'identifier': 'main', 'port': 'http'}}]}],
        'tls': [{'hosts': [host], 'secretName': f'{name}-tls'}],
    }


def apply(draft: Draft, entry: Entry) -> None:
    settings, app = entry.settings, draft.instance.app
    if settings.exposure not in ROUTED:
        return
    host = settings.host or default_host(app, draft.instance.env)
    draft.values['ingress'] = {'main': ingress_values(app, host, settings.exposure, settings.issuer or DEFAULT_ISSUER)}
    draft.routed = True
    if settings.exposure == 'authenticated-web':
        draft.depend(OAUTH_UNIT)
