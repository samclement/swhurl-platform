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
# Capabilities an app can ask for beyond running (docs/plan.md item 12). sqlite: a retained volume
# with the database file at SQLITE_PATH, given to the app as DATABASE_PATH.
DATABASES = ('sqlite',)
DATA_MOUNT = '/data'
SQLITE_PATH = f'{DATA_MOUNT}/app.db'
DATABASE_PATH_ENV = 'DATABASE_PATH'
DEFAULT_DATABASE_SIZE = '1Gi'
# How long an instance may take to become Ready before its unit and HelmRelease report failure
# (Helm waits this long, then retries once). Short, so a broken image or probe shows red quickly.
FAIL_AFTER = '3m'
# Every web app may take this long to first answer its health path before liveness checks begin (a
# startup probe, every STARTUP_PERIOD seconds). One allowance for all: a fast app is Ready as soon as it
# answers, and a JVM on a busy node needs most of it. It must leave Helm time to pull the image within
# FAIL_AFTER; the templates' smoke tests wait as long.
STARTUP_SECONDS = 120
STARTUP_PERIOD = 5

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


# Environment names the platform sets or may set. An app's secret keys arrive through envFrom, and a
# container's own env silently wins over envFrom, so a secret with one of these names would never be seen.
# New platform variables use the SWHURL_ prefix. KUBERNETES_* are the API server's address for in-cluster clients.
RESERVED_ENV_NAMES = (OTLP_HOST_IP, DATABASE_PATH_ENV)
RESERVED_ENV_PREFIXES = ('OTEL_', 'SWHURL_', 'KUBERNETES_')


def secret_key_problem(keys: list[str]) -> str:
    """Why these secret keys cannot be used, or '' if they can."""
    bad = [k for k in keys if not re.fullmatch(r'[A-Z_][A-Z0-9_]*', k)]
    if bad:
        return f'secret keys must be environment variable names (A-Z, 0-9, _): {", ".join(bad)}'
    reserved = [k for k in keys if k in RESERVED_ENV_NAMES or k.startswith(RESERVED_ENV_PREFIXES)]
    if reserved:
        return (f'reserved for the platform: {", ".join(reserved)} (the platform sets {", ".join(RESERVED_ENV_NAMES)} '
                f'and names starting {", ".join(RESERVED_ENV_PREFIXES)}); choose another name')
    duplicates = sorted({k for k in keys if keys.count(k) > 1})
    if duplicates:
        return f'secret keys repeated: {", ".join(duplicates)}'
    return ''


# swhurl.yaml: what an app needs from the platform, kept in the app's own repository (docs/apps.md).
# app-new --from-repo / --manifest reads it; each field becomes an app-new default, and flags given
# explicitly still win. Name, environment, image and host are per instance and never in the file.
MANIFEST_FILE = 'swhurl.yaml'
MANIFEST_VERSION = 1
KINDS = ('web', 'worker')
TELEMETRY = ('otlp', 'none')


class ManifestError(ValueError):
    pass


def manifest_defaults(doc: object, source: str = MANIFEST_FILE) -> dict:
    """app-new defaults from a swhurl.yaml document; raise ManifestError naming the first problem."""
    def fail(message: str) -> ManifestError:
        return ManifestError(f'{source}: {message}')

    if not isinstance(doc, dict):
        raise fail('must be a mapping')
    if doc.get('version') != MANIFEST_VERSION:
        raise fail(f'version must be {MANIFEST_VERSION} (this platform reads version {MANIFEST_VERSION})')
    allowed = {'version', 'stack', 'kind', 'port', 'healthPath', 'uid', 'telemetry', 'database', 'databaseSize',
               'secrets', 'resources', 'exposure', 'autoDeploy'}
    unknown = sorted(set(doc) - allowed)
    if unknown:
        raise fail(f'unknown field(s) {", ".join(unknown)}; allowed: {", ".join(sorted(allowed))}')

    def field(key: str, kind: type | tuple, choices: tuple = ()):
        value = doc.get(key)
        if value is None:
            return None
        if not isinstance(value, kind) or isinstance(value, bool) and kind is int:
            raise fail(f'{key} must be {getattr(kind, "__name__", kind)}')
        if choices and value not in choices:
            raise fail(f'{key} must be one of {", ".join(choices)}')
        return value

    kind = field('kind', str, KINDS)
    if not kind:
        raise fail(f'kind is required ({", ".join(KINDS)})')
    defaults: dict = {'kind': kind, 'uid': field('uid', int) or TEMPLATE_UID,
                      'otlp': field('telemetry', str, TELEMETRY) == 'otlp',
                      'auto_deploy': bool(field('autoDeploy', bool)),
                      'exposure': field('exposure', str, EXPOSURES)
                      or ('authenticated-web' if kind == 'web' else 'private')}
    if kind == 'web':
        defaults['port'] = field('port', int) or TEMPLATE_PORT
        defaults['health_path'] = field('healthPath', str)
        if not defaults['health_path']:
            raise fail('healthPath is required for kind web (the app\'s readiness endpoint)')
    elif any(doc.get(k) is not None for k in ('port', 'healthPath')):
        raise fail('port and healthPath apply to kind web only')
    if field('database', str, DATABASES):
        defaults['database'] = doc['database']
        if field('databaseSize', str):
            defaults['database_size'] = doc['databaseSize']
    elif doc.get('databaseSize') is not None:
        raise fail('databaseSize needs database')
    secrets = field('secrets', list)
    if secrets:
        if not all(isinstance(k, str) for k in secrets):
            raise fail('secrets must be environment variable names (A-Z, 0-9, _)')
        if problem := secret_key_problem(secrets):
            raise fail(problem)
        defaults['secret_keys'] = secrets
    resources = field('resources', dict) or {}
    names = {'cpu': 'cpu', 'memory': 'memory', 'memoryLimit': 'memory_limit'}
    if set(resources) - set(names):
        raise fail(f'resources may set {", ".join(names)} only')
    for key, dest in names.items():
        if resources.get(key) is not None:
            if not isinstance(resources[key], str):
                raise fail(f'resources.{key} must be a quantity string, for example "64Mi"')
            defaults[dest] = resources[key]
    field('stack', str)
    return defaults


# Stacks: one Copier template repository each (docs/plan.md section 8). make app-repo renders one into a
# new repository; the template's own CI renders it and runs the shared app checks on the result.
STACKS = {'typescript': 'samclement/swhurl-app-template-typescript',
          'kotlin': 'samclement/swhurl-app-template-kotlin'}
STACK_DESCRIPTIONS = {
    'typescript': 'Node 24 and TypeScript: starts in a second, about 60 MB of memory.',
    'kotlin': 'Kotlin on Micronaut, Java 25 (a trimmed runtime, about 150 MB image): about 10 s to start, '
              'about 200 MB of memory.',
}
"""One line per stack for the New app form; both offer web or worker and SQLite."""
COPIER = 'copier@9.18.2'
APP_OWNER = 'samclement'
APP_WORKFLOW = 'Container'
"""The workflow in every app repository that checks and publishes its image (calls the template's app.yml)."""


# Apps built from the template repository (samclement/swhurl-app-template-typescript) follow
# these conventions; each preset is the swhurl.yaml such an app would carry.
TEMPLATE_PORT = 8080
TEMPLATE_HEALTH_PATH = '/healthz'
TEMPLATE_UID = 65532
PRESET_MANIFESTS = {
    'swhurl-web': {'version': 1, 'kind': 'web', 'port': TEMPLATE_PORT, 'healthPath': TEMPLATE_HEALTH_PATH,
                   'uid': TEMPLATE_UID, 'telemetry': 'otlp', 'autoDeploy': True},
    'swhurl-worker': {'version': 1, 'kind': 'worker', 'uid': TEMPLATE_UID, 'telemetry': 'otlp', 'autoDeploy': True},
}
PRESETS = {name: manifest_defaults(doc, f'preset {name}') for name, doc in PRESET_MANIFESTS.items()}
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
