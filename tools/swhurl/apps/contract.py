"""The app contract: rules the generator writes and the policy check enforces.

Both sides import these, so generated output and the check cannot drift apart.
"""
from __future__ import annotations

import re
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


# Apps built from the template repository (samclement/swhurl-app-template-typescript) follow
# these conventions, so a preset can fill everything but the name, image and exposure.
TEMPLATE_PORT = 8080
TEMPLATE_HEALTH_PATH = '/healthz'
TEMPLATE_UID = 65532
PRESETS = {
    'swhurl-web': {'kind': 'web', 'exposure': 'authenticated-web', 'port': TEMPLATE_PORT,
                   'health_path': TEMPLATE_HEALTH_PATH, 'uid': TEMPLATE_UID, 'otlp': True, 'auto_deploy': True},
    'swhurl-worker': {'kind': 'worker', 'exposure': 'private', 'uid': TEMPLATE_UID, 'otlp': True,
                      'auto_deploy': True},
}
"""app-new defaults per preset; flags given explicitly still win."""


def default_host(name: str, env: str) -> str:
    """A signed-in app's host when none is given: <name>.<domain> in prod, <env>-<name>.<domain> otherwise."""
    return f'{name}.{COOKIE_DOMAIN}' if env == 'prod' else f'{env}-{name}.{COOKIE_DOMAIN}'


# Automatic staging deploys (Flux image automation, platform/image-automation): for an app with
# --auto-deploy, app-new writes an ImageRepository and ImagePolicy (in flux-system, where the one
# ImageUpdateAutomation finds them) into the staging instance, and marks the staging image's tag
# and digest lines so Flux rewrites them when a newer image is published. Tags must be
# <run number>-<commit sha>, as the template's workflow publishes them; the highest run wins.
IMAGE_AUTOMATION_FILE = 'image-automation.yaml'
AUTO_DEPLOY_TAG_PATTERN = r'^(?P<run>[0-9]+)-[0-9a-f]{7,40}$'
AUTO_DEPLOY_ENV = 'staging'
_MARKER = re.compile(r' # \{"\$imagepolicy": "flux-system:([a-z0-9-]+):(tag|digest)"\}$', re.M)


def image_policy_name(app: str) -> str:
    return f'{app}-{AUTO_DEPLOY_ENV}'


def add_image_markers(text: str, policy: str) -> str:
    """Mark the single image tag and digest lines of a HelmRelease for Flux's setters."""
    for field in ('tag', 'digest'):
        text, count = re.subn(rf'^(\s+{field}: \S+)$', rf'\g<1> # {{"$imagepolicy": "flux-system:{policy}:{field}"}}',
                              text, count=1, flags=re.M)
        if count != 1:
            raise ValueError(f'no image {field}: line to mark for automatic deploys')
    return text


def strip_image_markers(text: str) -> tuple[str, str | None]:
    """The HelmRelease text without Flux's setter markers, and the policy they named (or None)."""
    policies = set(m[0] for m in _MARKER.findall(text))
    return _MARKER.sub('', text), (policies.pop() if len(policies) == 1 else None)


INSTANCE_ROOTS = (Path('apps'), Path('tests/fixtures/apps/apps'))
"""Where instances live: the real ones and the generated test fixtures."""


def in_cookie_domain(host: str) -> bool:
    return host == COOKIE_DOMAIN or host.endswith('.' + COOKIE_DOMAIN)
