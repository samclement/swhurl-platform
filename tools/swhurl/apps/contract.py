"""The app contract: rules the generator writes and the policy check enforces.

Both sides import these, so generated output and the check cannot drift apart.
"""
from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, yamlio
from swhurl.platform import base_domain, label

CHART = 'app-template'
CHART_VERSION = '5.2.1'
CHART_REPOSITORY = 'bjw-s'
# Capabilities an app can ask for beyond running (docs/plan.md item 12). sqlite: a retained volume
# with the database file at SQLITE_PATH, given to the app as DATABASE_PATH.
DATABASES = ('sqlite',)
DATA_MOUNT = '/data'
SQLITE_PATH = f'{DATA_MOUNT}/app.db'
DATABASE_PATH_VARIABLE = 'DATABASE_PATH'
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
RETAINED_STORAGE_CLASS = 'local-path-retain'
STORAGE_CLASSES = {'local-path', RETAINED_STORAGE_CLASS}

MANAGED = label('managed')
APP = label('app')
ENVIRONMENT = label('environment')
EXPOSURE = label('exposure')
EXCEPTIONS = label('policy-exceptions')

# Teams: an app belongs to one team, a name registered in TEAMS_FILE; there is no default, so every app names one. The team is a Namespace label,
# the same in every environment of the app; telemetry carries it as swhurl.team (platform/otel).
TEAM = label('team')
TEAMS_FILE = Path('apps/teams.yaml')


def teams(root: Path = ROOT) -> dict[str, str]:
    """Registered team names and descriptions, from ``root`` (a scratch root falls back to this checkout)."""
    for base in (root, ROOT):
        path = base / TEAMS_FILE
        if path.is_file():
            registered = (yamlio.load(path.read_text()) or {}).get('teams') or {}
            return {name: (entry or {}).get('description', '') for name, entry in registered.items()}
    return {}


# Isolation: every instance carries a ResourceQuota and a NetworkPolicy, generated here so the
# generator, the edit commands and the policy check agree.
QUOTA_FILE = 'resourcequota.yaml'
NETWORK_POLICY_FILE = 'networkpolicy.yaml'
RELOAD = 'secret.reloader.stakater.com/reload'
"""On an app's controller: the Secrets whose change restarts it (Reloader), comma-separated."""
ISOLATION_FILES = (QUOTA_FILE, NETWORK_POLICY_FILE)
# One size for every instance namespace: room for app-scale's maximum replicas at template sizes.
QUOTA = {'pods': '14', 'requests.cpu': '2', 'requests.memory': '2Gi', 'limits.memory': '4Gi'}
# What else runs in an instance's namespace beside the app: the SQLite backup or restore pod
# (sqlite_backup.pod_manifest) and cert-manager's HTTP-01 solver. The quota check leaves this free.
QUOTA_HEADROOM = {'pods': '2', 'requests.cpu': '20m', 'requests.memory': '96Mi', 'limits.memory': '320Mi'}
TRAEFIK = {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'kube-system'}},
           'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'traefik'}}}
"""Where routed requests come from: the k3s Traefik pods."""


def quantity(value: object) -> float:
    """A Kubernetes quantity as a number: CPU in cores, memory in bytes, counts as they are."""
    text = str(value)
    for suffix, factor in (('Ki', 2**10), ('Mi', 2**20), ('Gi', 2**30), ('m', 0.001)):
        if text.endswith(suffix):
            return float(text[:-len(suffix)]) * factor
    return float(text)


def over_quota(workloads: list[tuple[int, list[dict]]]) -> list[str]:
    """The QUOTA keys that ``workloads`` exceed, leaving QUOTA_HEADROOM free. Each workload is the pods
    it may run at once (a rolling update adds one) and the resources of each container in a pod."""
    needed = {key: quantity(value) for key, value in QUOTA_HEADROOM.items()}
    for pods, containers in workloads:
        needed['pods'] += pods
        for resources in containers:
            for key, section, name in (('requests.cpu', 'requests', 'cpu'), ('requests.memory', 'requests', 'memory'),
                                       ('limits.memory', 'limits', 'memory')):
                needed[key] += pods * quantity(((resources or {}).get(section) or {}).get(name, 0))
    return [key for key, value in needed.items() if value > quantity(QUOTA[key])]


def resource_quota(app: str, namespace: str) -> dict:
    return {'apiVersion': 'v1', 'kind': 'ResourceQuota', 'metadata': {'name': app, 'namespace': namespace},
            'spec': {'hard': dict(QUOTA)}}


def network_policy(app: str, namespace: str, routed_port: int | None, port: int | None = None,
                   callers: list[str] | tuple[str, ...] = ()) -> dict:
    """Who may connect to the app's pods: Traefik on the app port when the instance has a route, the
    apps allowed to call it (``callers``, on ``port``, from their namespace of the same environment),
    and nobody else.

    It selects the app's own pods only: a policy on every pod in the namespace would also block
    cert-manager's HTTP-01 solver (port 8089) and with it certificate issuance.
    """
    sources = [TRAEFIK] if routed_port else []
    env = namespace.removeprefix(f'{app}-')
    sources += [{'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': f'{caller}-{env}'}},
                 'podSelector': {'matchLabels': {'app.kubernetes.io/instance': caller}}} for caller in callers]
    ingress = [{'from': sources, 'ports': [{'port': routed_port or port, 'protocol': 'TCP'}]}] if sources else []
    return {'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
            'metadata': {'name': app, 'namespace': namespace},
            'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/instance': app}},
                     'policyTypes': ['Ingress'], 'ingress': ingress}}

# OpenTelemetry: apps with an SDK send OTLP to the collector DaemonSet on their own node,
# which listens with host networking on 4318 (HTTP) and 4317 (gRPC) and adds the ingestion
# key and pod attributes (platform/otel/helmrelease-daemonset.yaml). HOST_IP is the node IP.
OTLP_HOST_IP = 'HOST_IP'
OTLP_ENDPOINT = f'http://$({OTLP_HOST_IP}):4318'
OTLP_PROTOCOL = 'http/protobuf'


def otlp_variables(service: str) -> dict:
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
RESERVED_VARIABLE_NAMES = (OTLP_HOST_IP, DATABASE_PATH_VARIABLE,  # and every SWHURL_ name above
                           # the egress proxy's (egress.py, which cannot be imported here)
                           'HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'NODE_USE_ENV_PROXY', 'JAVA_TOOL_OPTIONS')
RESERVED_VARIABLE_PREFIXES = ('OTEL_', 'SWHURL_', 'KUBERNETES_')


def secret_key_problem(keys: list[str]) -> str:
    """Why these secret keys cannot be used, or '' if they can."""
    bad = [k for k in keys if not re.fullmatch(r'[A-Z_][A-Z0-9_]*', k)]
    if bad:
        return f'secret keys must be environment variable names (A-Z, 0-9, _): {", ".join(bad)}'
    reserved = [k for k in keys if k in RESERVED_VARIABLE_NAMES or k.startswith(RESERVED_VARIABLE_PREFIXES)]
    if reserved:
        return (f'reserved for the platform: {", ".join(reserved)} (the platform sets {", ".join(RESERVED_VARIABLE_NAMES)} '
                f'and names starting {", ".join(RESERVED_VARIABLE_PREFIXES)}); choose another name')
    duplicates = sorted({k for k in keys if keys.count(k) > 1})
    if duplicates:
        return f'secret keys repeated: {", ".join(duplicates)}'
    return ''


# An app's own Secret: every key becomes a variable (envFrom, optional so the app starts before its first
# value is set). The names its image needs (swhurl.yaml's secrets) are noted on the HelmRelease; the values
# live in SECRET_FILE, which only the operator can write (make app-secret): a name without a value there
# is "value missing", a value no name asks for is unused.
SECRET_FILE = 'secret.sops.yaml'
SECRET_NAMES = label('secrets')
"""On an app's HelmRelease: the secret names its image needs, comma-separated."""


def app_secret(app: str) -> str:
    return f'{app}-secret'


def declared_secrets(release: dict | None) -> list[str]:
    """The secret names a release notes its image needs."""
    text = str((((release or {}).get('metadata') or {}).get('annotations') or {}).get(SECRET_NAMES, ''))
    return [name for name in text.split(',') if name]


def stored_secrets(text: str | None) -> list[str]:
    """The keys an app's SECRET_FILE holds a value for, from its text (SOPS leaves key names readable)."""
    docs = [d for d in yamlio.load_all(text or '') if isinstance(d, dict)]
    return [str(key) for doc in docs for key in (doc.get('stringData') or doc.get('data') or {})]


# Stacks: one Copier template repository each (docs/plan.md section 8). make app-repo renders one into a
# new repository; the template's own CI renders it and runs the shared app checks on the result.
STACKS = {'typescript': 'samclement/swhurl-app-template-typescript',
          'kotlin': 'samclement/swhurl-app-template-kotlin'}
# Questions, rendering and contract checks use the same reviewed catalogue commit.
STACK_REVISIONS = {
    'typescript': 'a46945a454de778e936b6692b5f3e36077e9ea5f',
    'kotlin': '80d0a72a0186089a008d86b5d58e4be77c902f01',
}
STACK_DESCRIPTIONS = {
    'typescript': 'Node 24 and TypeScript: starts in a second, about 60 MB of memory.',
    'kotlin': 'Kotlin on Micronaut, Java 25 (a trimmed runtime, about 150 MB image): about 10 s to start, '
              'about 200 MB of memory.',
}
"""One line per stack for the New app form."""


@dataclass(frozen=True)
class AppType:
    """What a team is building. A new app is created by type: the type sets every template answer and the
    exposure staging starts with (docs/apps.md#app-types). Changing a type here changes new apps only:
    each app's .copier-answers.yml keeps the answers it was made with."""
    title: str
    text: str
    stacks: tuple[str, ...]
    answers: dict[str, str]
    exposure: str  # what staging starts with; changed later with make app-expose


APP_TYPES = {
    'web-app': AppType('Web app', 'A site people use in a browser. Its data belongs in an API it calls; its own '
                       'SQLite keeps what only it uses.', ('typescript',),
                       {'kind': 'web', 'database': 'sqlite', 'api': 'openapi', 'events': 'asyncapi', 'flags': 'example'},
                       'authenticated-web'),
    'api': AppType('API', 'Data and operations other apps call; private: browsers reach it through a web app.',
                   ('typescript', 'kotlin'),
                   {'kind': 'web', 'database': 'sqlite', 'api': 'openapi', 'events': 'asyncapi', 'flags': 'example'},
                   'private'),
    'worker': AppType('Worker', 'Reacts to events, or works in a steady loop; no web address.', ('typescript', 'kotlin'),
                      {'kind': 'worker', 'database': 'none', 'api': 'none', 'events': 'asyncapi', 'flags': 'example'},
                      'private'),
}


def type_answers(app_type: str, stack: str) -> dict[str, str]:
    """The template answers for a type built with a stack; ValueError naming the choices otherwise."""
    if app_type not in APP_TYPES:
        raise ValueError(f'type must be one of {", ".join(APP_TYPES)}')
    found = APP_TYPES[app_type]
    if stack not in found.stacks:
        raise ValueError(f'a {found.title} is built with {" or ".join(found.stacks)}, not {stack}')
    return dict(found.answers)
COPIER = 'copier@9.18.2'
APP_OWNER = 'samclement'
APP_WORKFLOW = 'Container'
"""The workflow in every app repository that checks and publishes its image (calls the template's app.yml)."""


# What the stack templates (samclement/swhurl-app-template-*) use; swhurl.yaml's defaults.
TEMPLATE_PORT = 8080
TEMPLATE_UID = 65532


def default_host(name: str, env: str) -> str:
    """A signed-in app's host when none is given: <name>.<domain> in prod, <env>-<name>.<domain> otherwise."""
    return f'{name}.{COOKIE_DOMAIN}' if env == 'prod' else f'{env}-{name}.{COOKIE_DOMAIN}'


# Automatic staging deploys (docs/apps.md#deploy-a-new-image): for an app with --auto-deploy,
# app-new writes an ImageRepository and ImagePolicy (in flux-system) into the staging instance. The
# policy's report starts the Deploy workflow, which reads the registry with the same filter
# (apps/images.py) and writes the tag and digest. Tags must be <run number>-<commit sha>, as the
# template's workflow publishes them; the highest run wins.
IMAGE_AUTOMATION_FILE = 'image-automation.yaml'
AUTO_DEPLOY_TAG_PATTERN = r'^(?P<run>[0-9]+)-[0-9a-f]{7,40}$'
AUTO_DEPLOY_ENV = 'staging'


def image_policy_name(app: str) -> str:
    return f'{app}-{AUTO_DEPLOY_ENV}'


"""Where instances live: the real ones and the generated test fixtures."""


def in_cookie_domain(host: str) -> bool:
    return host == COOKIE_DOMAIN or host.endswith('.' + COOKIE_DOMAIN)
