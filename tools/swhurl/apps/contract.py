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

# OpenTelemetry: apps with an SDK send OTLP to the collector DaemonSet on their own node,
# which listens with host networking on 4318 (HTTP) and 4317 (gRPC) and adds the ingestion
# key and pod attributes (platform/otel/helmrelease-daemonset.yaml). HOST_IP is the node IP.
OTLP_HOST_IP = 'HOST_IP'
OTLP_ENDPOINT = f'http://$({OTLP_HOST_IP}):4318'
OTLP_PROTOCOL = 'http/protobuf'


def otlp_env(service: str) -> dict:
    """The container env that points an app's OpenTelemetry SDK at the cluster collector."""
    return {
        OTLP_HOST_IP: {'valueFrom': {'fieldRef': {'fieldPath': 'status.hostIP'}}},
        'OTEL_EXPORTER_OTLP_ENDPOINT': OTLP_ENDPOINT,
        'OTEL_EXPORTER_OTLP_PROTOCOL': OTLP_PROTOCOL,
        'OTEL_SERVICE_NAME': service,
    }


INSTANCE_ROOTS = (Path('apps'), Path('tests/fixtures/apps/apps'))
"""Where instances live: the real ones and the generated test fixtures."""


def in_cookie_domain(host: str) -> bool:
    return host == COOKIE_DOMAIN or host.endswith('.' + COOKIE_DOMAIN)
