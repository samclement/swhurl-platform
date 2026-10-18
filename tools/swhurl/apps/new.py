"""Generate one app instance: namespace, app-template HelmRelease, Flux unit.

    make app-new NAME=<app> ARGS="--from-repo OWNER/<app> --env staging --image ghcr.io/OWNER/<app>:TAG@DIGEST"

Every app comes from a stack template (make app-repo, or the console's Start a new app, which
print or run this line): --from-repo reads the app's swhurl.yaml from GitHub (GITHUB_TOKEN if
set, for private repositories) and its fields become the defaults; flags still win. In this
repository the app must be the owner's repository NAME, run the image that repository
publishes and deploy to staging automatically (platform_origin_problem). --manifest reads a
local swhurl.yaml instead, for make check-templates and tests, which write to another --root.

Writes apps/NAME/ENV/ and clusters/home/app-NAME-ENV.yaml, and registers
the unit in clusters/home/kustomization.yaml. Refuses to overwrite, to expose a
worker, to put a public app inside the shared sign-in cookie domain, and to
create production directly (use app-promote). Secret names are recorded without
values (make app-secret sets them); a first production's placeholders are
SOPS-encrypted before this script returns, so plaintext never reaches Git.

The output is ordinary YAML: edit it by hand afterwards. `make check-apps`
checks the rendered result.
"""
from __future__ import annotations

import argparse
import os
import re
import shlex
import sys
from pathlib import Path

import yaml

from swhurl import ROOT, app_alerts
from swhurl.apps import declaration, events, policy
from swhurl.apps.calling import (
    API_VARIABLE,
    DESCRIPTION_VARIABLE,
    NOTIFY,
    TOKENS_VOLUME,
    identity_variables,
    signing_keys,
    tokens_volume,
)
from swhurl.apps.contract import (
    APP,
    APP_OWNER,
    AUTH_MIDDLEWARE,
    AUTO_DEPLOY_ENV,
    AUTO_DEPLOY_TAG_PATTERN,
    CHART,
    CHART_REPOSITORY,
    CHART_VERSION,
    COOKIE_DOMAIN,
    DATA_MOUNT,
    DATABASE_PATH_VARIABLE,
    DATABASES,
    DEFAULT_DATABASE_SIZE,
    ENVIRONMENT,
    EXPOSURE,
    EXPOSURES,
    FAIL_AFTER,
    IMAGE_AUTOMATION_FILE,
    JOB_DEADLINE_SECONDS,
    JOB_SCHEDULE,
    JOB_START_DEADLINE_SECONDS,
    JOB_TIME_ZONE,
    MANAGED,
    NETWORK_POLICY_FILE,
    QUOTA_FILE,
    RELOAD,
    RETAINED_STORAGE_CLASS,
    SECRET_FILE,
    SECRET_NAMES,
    SQLITE_PATH,
    STARTUP_PERIOD,
    STARTUP_SECONDS,
    TEAM,
    app_secret,
    default_host,
    image_policy_name,
    in_cookie_domain,
    network_policy,
    otlp_variables,
    resource_quota,
    schedule_problem,
    secret_key_problem,
    teams,
)
from swhurl.apps.instance import ENVIRONMENTS, NAME_RE, Instance, instance_folder, instance_unit_file
from swhurl.run import CommandError, Runner

IMAGE_RE = re.compile(r'^(?P<repo>[a-z0-9][a-z0-9._/:-]*?)(?::(?P<tag>[A-Za-z0-9._-]+))?(?:@(?P<digest>sha256:[0-9a-f]{64}))?$')


class GenerationError(Exception):
    pass


def parse_image(image: str) -> dict:
    match = IMAGE_RE.match(image)
    if not match or not (match['tag'] or match['digest']):
        raise GenerationError(f'image must be REPO:TAG, REPO@sha256:... or both: {image}')
    if match['tag'] == 'latest':
        raise GenerationError('image tag "latest" is not allowed; pin a version')
    image = {'repository': match['repo']}
    for key in ('tag', 'digest'):
        if match[key]:
            image[key] = match[key]
    return image


def resolve(args) -> None:
    """Fill values derived from others: a signed-in app's host; a database's volume."""
    if args.exposure == 'authenticated-web' and not args.host and NAME_RE.match(args.name):
        args.host = default_host(args.name, args.env)
    if args.database == 'sqlite':
        args.persistence = args.persistence or args.database_size
        args.mount_path = DATA_MOUNT


def validate(args, root: Path = ROOT) -> None:
    if not NAME_RE.match(args.name):
        raise GenerationError('NAME must be a DNS label (lowercase, digits, hyphens, max 40 chars)')
    if args.name == NOTIFY:
        raise GenerationError(f'{NOTIFY} is the platform\'s notify service: every app carries a token for that name; choose another')
    try:
        app_alerts.thresholds(getattr(args, 'alerts', ''))
    except ValueError as error:
        raise GenerationError(f'--alerts: {error}') from None
    if not args.team:
        raise GenerationError('every app belongs to a team: give --team NAME (or team: in swhurl.yaml); registered in '
                              f'apps/teams.yaml: {", ".join(teams(root)) or "none yet (make team-add NAME=<team>)"}')
    if args.team not in teams(root):
        raise GenerationError(f'unknown team {args.team!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    if args.secret_keys and (problem := secret_key_problem(args.secret_keys)):
        raise GenerationError(problem)
    if args.kind != 'web' and args.exposure != 'private':
        raise GenerationError(f'a {args.kind} has no Service or route; use --exposure private')
    if args.kind == 'job':
        if args.persistence or args.database:
            raise GenerationError('a job keeps no volume or database: a run has no pod to keep it in between runs')
        if problem := schedule_problem(args.schedule):
            raise GenerationError(f'--schedule: {problem}')
    elif args.schedule != JOB_SCHEDULE:
        raise GenerationError('--schedule applies to kind job only')
    if args.exposure in ('authenticated-web', 'public'):
        if not args.host:
            raise GenerationError(f'--host is required for {args.exposure}')
        if args.exposure == 'authenticated-web' and not in_cookie_domain(args.host):
            raise GenerationError(f'authenticated-web hosts must be under {COOKIE_DOMAIN} (shared sign-in cookie)')
        if args.exposure == 'public' and in_cookie_domain(args.host):
            raise GenerationError(f'public hosts must be outside {COOKIE_DOMAIN}: the shared sign-in cookie would reach them')
    elif args.host:
        raise GenerationError('--host only applies to authenticated-web or public exposure')
    if args.kind == 'web' and not args.health_path:
        raise GenerationError('web apps need --health-path (the app\'s real readiness endpoint)')
    if args.env == 'prod' and 'digest' not in parse_image(args.image):
        raise GenerationError('production instances must pin an image digest (REPO:TAG@sha256:...)')
    if auto_deploys(args):
        image = parse_image(args.image)
        if not re.match(AUTO_DEPLOY_TAG_PATTERN, image.get('tag', '')) or 'digest' not in image:
            raise GenerationError('automatic deploys need an image REPO:<run>-<sha>@sha256:... as the swhurl '
                                  "template's workflow publishes it (--no-auto-deploy is for fixtures under another --root)")


def auto_deploys(args) -> bool:
    """--auto-deploy applies to staging only; production changes through app-promote."""
    return bool(args.auto_deploy) and args.env == AUTO_DEPLOY_ENV


def image_automation(args) -> list[dict]:
    """App-scoped Flux image resources: the policy whose report starts the Deploy workflow."""
    repository = parse_image(args.image)['repository']
    labels = {MANAGED: 'true', APP: args.name}
    return [
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImageRepository',
         'metadata': {'name': args.name, 'namespace': 'flux-system', 'labels': labels},
         # The app repository's image webhook starts the scan (apps/hooks.py); the interval is the
         # fallback for a missed delivery.
         'spec': {'image': repository, 'interval': '1h'}},
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImagePolicy',
         'metadata': {'name': image_policy_name(args.name), 'namespace': 'flux-system', 'labels': labels},
         'spec': {'imageRepositoryRef': {'name': args.name},
                  'filterTags': {'pattern': AUTO_DEPLOY_TAG_PATTERN, 'extract': '$run'},
                  'policy': {'numerical': {'order': 'asc'}},
                  # Tags are immutable, so re-reading the current tag's digest hourly is plenty; a new
                  # tag's digest is read when the repository scan finds it. Always requires an interval.
                  'digestReflectionPolicy': 'Always', 'interval': '1h'}},
    ]


def build_values(args) -> dict:
    image = parse_image(args.image)
    container: dict = {
        'image': image,
        'resources': {
            'requests': {'cpu': args.cpu, 'memory': args.memory},
            'limits': {'memory': args.memory_limit},
        },
        'securityContext': {
            'allowPrivilegeEscalation': False,
            'readOnlyRootFilesystem': True,
            'capabilities': {'drop': ['ALL']},
        },
    }
    if args.command:
        container['command'] = shlex.split(args.command)
    # Identity first: Kubernetes expands $(SWHURL_ENV) in later values only if it is defined before them.
    container['env'] = identity_variables(args.name, args.env, args.kind == 'web',
                                    getattr(args, 'signing_keys', None) or signing_keys())
    if args.kind == 'web' and args.api_path:
        container['env'][API_VARIABLE] = args.api_path
    if args.description:
        container['env'][DESCRIPTION_VARIABLE] = args.description
    if args.otlp:
        container['env'] |= otlp_variables(args.name)
    if args.database == 'sqlite':
        container['env'][DATABASE_PATH_VARIABLE] = SQLITE_PATH
    if args.secret_keys:
        # Optional: the app starts before the Secret has its first value (make app-secret).
        container['envFrom'] = [{'secretRef': {'name': app_secret(args.name), 'optional': True}}]
    if args.kind == 'web':
        def probe():
            return {'enabled': True, 'custom': True,
                    'spec': {'httpGet': {'path': args.health_path, 'port': args.port}}}
        # Liveness waits until the app first answers, for up to STARTUP_SECONDS (a JVM can take a while
        # when the node is busy); without this, three failed liveness checks would restart it mid-start.
        startup = probe()
        startup['spec'] |= {'periodSeconds': STARTUP_PERIOD, 'failureThreshold': STARTUP_SECONDS // STARTUP_PERIOD}
        container['probes'] = {'readiness': probe(), 'liveness': probe(), 'startup': startup}

    controller: dict = {'containers': {'main': container}}
    if args.kind == 'job':
        # One pod per run, never two at once; a failed run is not retried (the next scheduled one runs).
        controller['type'] = 'cronjob'
        controller['cronjob'] = {'schedule': args.schedule, 'timeZone': JOB_TIME_ZONE, 'concurrencyPolicy': 'Forbid',
                                 'startingDeadlineSeconds': JOB_START_DEADLINE_SECONDS,
                                 'activeDeadlineSeconds': JOB_DEADLINE_SECONDS, 'backoffLimit': 0,
                                 'successfulJobsHistory': 1, 'failedJobsHistory': 1}
    if args.persistence:
        # One writer: the volume is ReadWriteOnce on one node, and a database file must not have two
        # processes writing it. Stop the old pod before starting the new one; never run two (rule single-writer).
        controller['replicas'] = 1
        controller['strategy'] = 'Recreate'
    if args.secret_keys:
        controller['annotations'] = {RELOAD: app_secret(args.name)}

    values: dict = {
        'defaultPodOptions': {
            'automountServiceAccountToken': False,
            'securityContext': {
                'runAsNonRoot': True,
                'runAsUser': args.uid,
                'runAsGroup': args.uid,
                'fsGroup': args.uid,
                'seccompProfile': {'type': 'RuntimeDefault'},
            },
        },
        'controllers': {'main': controller},
        'persistence': {'tmp': {'type': 'emptyDir', 'globalMounts': [{'path': '/tmp'}]}},
    }
    if args.persistence:
        values['persistence']['data'] = {
            'type': 'persistentVolumeClaim',
            'storageClass': RETAINED_STORAGE_CLASS,
            'accessMode': 'ReadWriteOnce',
            'size': args.persistence,
            'retain': True,
            'globalMounts': [{'path': args.mount_path}],
        }
    values['persistence'][TOKENS_VOLUME] = tokens_volume([])  # for the notify service; declared calls add theirs
    if args.kind == 'web':
        values['service'] = {'main': {'controller': 'main', 'ports': {'http': {'port': args.port}}}}
    if args.exposure in ('authenticated-web', 'public'):
        values['ingress'] = {'main': ingress_values(args.name, args.host, args.exposure, args.issuer)}
    return values


def ingress_values(name: str, host: str, exposure: str, issuer: str) -> dict:
    """The app-template route for a signed-in or public instance (app-expose writes the same)."""
    annotations = {'cert-manager.io/cluster-issuer': issuer}
    if exposure == 'authenticated-web':
        annotations['traefik.ingress.kubernetes.io/router.middlewares'] = AUTH_MIDDLEWARE
    return {
        'className': 'traefik',
        'annotations': annotations,
        'hosts': [{'host': host, 'paths': [{'path': '/', 'service': {'identifier': 'main', 'port': 'http'}}]}],
        'tls': [{'hosts': [host], 'secretName': f'{name}-tls'}],
    }


def depends_on(exposure: str) -> list[str]:
    """The units an instance waits for: signed-in routes need the sign-in middleware."""
    return ['infra-base'] + (['platform-oauth2-proxy'] if exposure == 'authenticated-web' else [])


def dump(docs) -> str:
    return yaml.safe_dump_all(docs, sort_keys=False, default_flow_style=False)


def isolation_files(app: str, namespace: str, exposure: str, port: int | None,
                    callers: list[str] | tuple[str, ...] = ()) -> dict[str, str]:
    """The instance's quota and NetworkPolicy by file name: Traefik is admitted only when there is a
    route, and the apps allowed to call it (``callers``) from their own namespaces."""
    routed = port if exposure in ('authenticated-web', 'public') else None
    return {QUOTA_FILE: dump([resource_quota(app, namespace)]),
            NETWORK_POLICY_FILE: dump([network_policy(app, namespace, routed, port, callers)])}


def generate(args, root: Path, runner: Runner | None = None) -> list[Path]:
    resolve(args)
    validate(args, root)
    args.signing_keys = signing_keys(root)
    created = Instance(args.name, args.env)  # validate() has refused any other name
    namespace, unit = created.namespace, created.unit
    instance = instance_folder(root, args.name, args.env)
    unit_file = instance_unit_file(root, args.name, args.env)
    for path in (instance, unit_file):
        if path.exists():
            raise GenerationError(f'{path} already exists; refusing to overwrite')

    labels = {
        MANAGED: 'true',
        APP: args.name,
        ENVIRONMENT: args.env,
        EXPOSURE: args.exposure,
        TEAM: args.team,
    }
    ns = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace, 'labels': labels}}
    if args.persistence:
        # Keeps the namespace (and so the claim) when the app unit is removed; see destroy-data.
        ns['metadata']['annotations'] = {'kustomize.toolkit.fluxcd.io/prune': 'disabled'}
    if getattr(args, 'alerts', ''):
        ns['metadata'].setdefault('annotations', {})[app_alerts.ANNOTATION] = args.alerts
    release = {
        'apiVersion': 'helm.toolkit.fluxcd.io/v2',
        'kind': 'HelmRelease',
        'metadata': {'name': args.name, 'namespace': namespace,
                     **({'annotations': {SECRET_NAMES: ','.join(args.secret_keys)}} if args.secret_keys else {})},
        'spec': {
            'interval': '30m',
            'timeout': FAIL_AFTER,
            'chart': {'spec': {'chart': CHART, 'version': CHART_VERSION,
                               'sourceRef': {'kind': 'HelmRepository', 'name': CHART_REPOSITORY, 'namespace': 'flux-system'},
                               'interval': '30m'}},
            'install': {'remediation': {'retries': 1}},
            'upgrade': {'remediation': {'retries': 1}},
            'values': build_values(args),
        },
    }
    resources = ['namespace.yaml', 'helmrelease.yaml', QUOTA_FILE, NETWORK_POLICY_FILE]
    # Staging starts with the names and no values (make app-secret sets them). A first production gets
    # placeholders, so make check-secrets refuses it until every value is set: it never runs without one.
    placeholders = bool(args.secret_keys) and args.env == 'prod'
    if placeholders:
        resources.insert(1, SECRET_FILE)
    release_text = dump([release])
    if auto_deploys(args):
        resources.append(IMAGE_AUTOMATION_FILE)
    files = {
        instance / 'namespace.yaml': dump([ns]),
        instance / 'helmrelease.yaml': release_text,
        instance / 'kustomization.yaml': dump([{'apiVersion': 'kustomize.config.k8s.io/v1beta1',
                                               'kind': 'Kustomization', 'resources': resources}]),
    }
    for name, text in isolation_files(args.name, namespace, args.exposure,
                                      args.port if args.kind == 'web' else None).items():
        files[instance / name] = text

    if auto_deploys(args):
        files[instance / IMAGE_AUTOMATION_FILE] = dump(image_automation(args))

    depends = depends_on(args.exposure)
    if auto_deploys(args):
        depends.append('platform-image-automation')
    spec = {
        'dependsOn': [{'name': d} for d in depends],
        'interval': '10m',
        'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform'},
        'path': './' + instance.relative_to(root).as_posix(),
    }
    if placeholders:
        spec['decryption'] = {'provider': 'sops', 'secretRef': {'name': 'sops-age'}}
    # App unit: default MirrorPrune, so removing it from Git uninstalls the instance.
    spec |= {'prune': True, 'wait': True, 'timeout': FAIL_AFTER}
    files[unit_file] = dump([{'apiVersion': 'kustomize.toolkit.fluxcd.io/v1', 'kind': 'Kustomization',
                              'metadata': {'name': unit, 'namespace': 'flux-system'}, 'spec': spec}])

    written = []
    try:
        for path, text in files.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
            written.append(path)
        if placeholders:
            written.append(write_encrypted_secret(instance, app_secret(args.name), namespace, args.secret_keys, runner=runner))
    except Exception:
        for path in written:
            path.unlink(missing_ok=True)
        raise

    if args.register:
        register(root, unit_file.name)
    if args.secret_keys:
        watch_namespace(root, namespace)
    return written


def write_encrypted_secret(instance: Path, name: str, namespace: str, keys: list[str], *, runner: Runner | None = None) -> Path:
    path = instance / SECRET_FILE
    secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': namespace},
              'type': 'Opaque', 'stringData': {k: 'REPLACE_ME' for k in keys}}
    path.write_text(dump([secret]))
    try:
        (runner or Runner()).run(['sops', '--encrypt', '--in-place', str(path)])
    except CommandError as error:
        path.unlink(missing_ok=True)
        raise GenerationError(f'could not SOPS-encrypt {path} (is there a .sops.yaml rule?): {error}') from error
    if 'sops:' not in path.read_text():
        path.unlink()
        raise GenerationError(f'{path} was not encrypted; removed it')
    return path


def register(root: Path, filename: str) -> None:
    from swhurl.apps.yaml_file import EditError, YamlFile

    try:
        document = YamlFile(root / 'clusters/home/kustomization.yaml')
        resources = document.data.get('resources')
        if not isinstance(resources, list) or getattr(resources.yaml_anchor(), 'value', None):
            raise EditError('unit registration needs an ordinary resources list')
        if filename not in resources:
            resources.append(filename)
            resources.fa.set_block_style()
            document.save()
    except EditError as error:
        raise GenerationError(str(error)) from None


def watch_namespace(root: Path, namespace: str) -> None:
    """Add the namespace to Reloader's scoped list so Secret changes restart the app."""
    path = root / 'platform/reloader/helmrelease.yaml'
    if not path.exists():
        return
    text = path.read_text()
    match = re.search(r'^(\s*namespaces:\s*)\[([^\]]*)\]', text, re.M)
    if not match:
        raise GenerationError(f'cannot find reloader.namespaces list in {path}')
    current = [n.strip() for n in match[2].split(',') if n.strip()]
    if namespace not in current:
        text = text[:match.start()] + f'{match[1]}[{", ".join(current + [namespace])}]' + text[match.end():]
        path.write_text(text)


REPO_RE = re.compile(r'^(?P<repo>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:@(?P<ref>[A-Za-z0-9_./-]+))?$')


def declared(spec: str, *, token: str = '', opener: declaration.Opener = declaration._open) -> dict:
    """app-new's defaults from the swhurl.yaml of ``OWNER/REPO[@REF]`` on GitHub (default branch without a ref)."""
    match = REPO_RE.match(spec)
    if not match:
        raise GenerationError(f'--from-repo must be OWNER/REPO or OWNER/REPO@REF: {spec}')
    try:
        found = declaration.read(match['repo'], match['ref'] or '', token=token, opener=opener)
    except declaration.DeclarationError as error:
        raise GenerationError(str(error)) from None
    if found is None:
        raise GenerationError(f'could not read {declaration.FILE} from {spec}: {spec} has no {declaration.FILE}, or is private (set GITHUB_TOKEN)')
    return found.seed


def parser(defaults: dict | None = None) -> argparse.ArgumentParser:
    """The app-new options, with swhurl.yaml's values as the defaults (explicit flags still win)."""
    p = argparse.ArgumentParser(prog='swhurl app-new', description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('name')
    source = p.add_mutually_exclusive_group()
    source.add_argument('--from-repo', metavar='OWNER/REPO[@REF]',
                        help=f"the app's repository: defaults from its {declaration.FILE} on GitHub")
    source.add_argument('--manifest', type=Path, metavar='PATH',
                        help=f'defaults from a local {declaration.FILE} (check-templates and tests)')
    p.add_argument('--env', default='staging', choices=ENVIRONMENTS, help='staging only; production is created by app-promote')
    p.add_argument('--image', required=True, help=f'ghcr.io/{APP_OWNER}/<name>:<run>-<sha>@sha256:... as the app repository publishes it '
                                                        '(any REPO:TAG or digest form under another --root)')
    p.add_argument('--kind', choices=list(declaration.KINDS), default='web')
    p.add_argument('--schedule', default=JOB_SCHEDULE,
                   help=f'job only: when it runs, five cron fields in {JOB_TIME_ZONE} (default "{JOB_SCHEDULE}"; make app-schedule later)')
    p.add_argument('--alerts', default='', metavar='SPEC',
                   help='the app\'s own default-alert settings: none, or error-logs=20,errors=none (default: the platform\'s)')
    p.add_argument('--team', help='the owning team, a name in apps/teams.yaml (required, here or as team: in swhurl.yaml)')
    p.add_argument('--exposure', choices=EXPOSURES, default='private',
                   help=f'private: no route; authenticated-web: shared sign-in on {COOKIE_DOMAIN}; '
                        f'public: no sign-in, host outside {COOKIE_DOMAIN}')
    p.add_argument('--host', help=f'default for authenticated-web: <name>.{COOKIE_DOMAIN} in prod, '
                                  f'<env>-<name>.{COOKIE_DOMAIN} otherwise')
    p.add_argument('--port', type=int, default=8080)
    p.add_argument('--health-path', help='HTTP readiness/liveness path (required for web)')
    p.add_argument('--command', help='container command, shell-quoted')
    p.add_argument('--uid', type=int, default=65532, help='non-root UID/GID the image runs as')
    p.add_argument('--cpu', default='10m')
    p.add_argument('--memory', default='32Mi')
    p.add_argument('--memory-limit', default='128Mi')
    p.add_argument('--database', choices=DATABASES,
                   help=f'sqlite: a retained volume with the database at {SQLITE_PATH}, passed as ${DATABASE_PATH_VARIABLE}; '
                        'one replica, stop-first updates')
    p.add_argument('--database-size', default=DEFAULT_DATABASE_SIZE, metavar='SIZE', help='the database volume (default %(default)s)')
    p.add_argument('--persistence', metavar='SIZE', help='add a retained volume, e.g. 1Gi (one replica, stop-first updates)')
    p.add_argument('--mount-path', default='/data')
    p.add_argument('--publish', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated events this app publishes (make app-events later)')
    p.add_argument('--subscribe', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated <app>.<event> it receives: each must be published in this environment')
    p.add_argument('--api-path', metavar='PATH', help='web only: where the app serves its API description, for the Services page')
    p.add_argument('--description', metavar='TEXT', help="one line saying what the app does (swhurl.yaml's description): "
                   'SWHURL_DESCRIPTION, which the templates serve in their API description')
    p.add_argument('--calls', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated apps this one calls: each must already have this environment (make app-calls later)')
    p.add_argument('--secret-keys', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated secret variable names the app reads; values set later with make app-secret')
    p.add_argument('--otlp', action=argparse.BooleanOptionalAction, default=False,
                   help='the app has an OpenTelemetry SDK: point it at the cluster collector (OTEL_* env)')
    p.add_argument('--auto-deploy', action=argparse.BooleanOptionalAction, default=False,
                   help='staging only: Flux deploys each newer image the app publishes (tags <run>-<sha>); '
                        'production still changes through app-promote')
    p.add_argument('--issuer', default='letsencrypt-prod', choices=['letsencrypt-prod', 'letsencrypt-staging', 'selfsigned'])
    p.add_argument('--root', type=Path, default=ROOT)
    p.add_argument('--no-register', dest='register', action='store_false',
                   help='do not add the unit to clusters/home/kustomization.yaml')
    p.add_argument('--no-policy-check', dest='policy_check', action='store_false',
                   help='skip rendering the new instance against the app policy (needs helm)')
    if defaults:
        p.set_defaults(**defaults)
    return p


def parse_args(argv=None, opener: declaration.Opener = declaration._open) -> argparse.Namespace:
    pre = argparse.ArgumentParser(add_help=False)
    pre.add_argument('--from-repo')
    pre.add_argument('--manifest', type=Path)
    known, _ = pre.parse_known_args(argv)
    defaults = None
    if known.from_repo and known.manifest:
        raise GenerationError('--from-repo and --manifest cannot be combined')
    if known.from_repo:
        defaults = declared(known.from_repo, token=os.environ.get('GITHUB_TOKEN', ''), opener=opener)
    elif known.manifest:
        try:
            defaults = declaration.parse(known.manifest.read_text(), str(known.manifest)).seed
        except OSError as error:
            raise GenerationError(f'cannot read {known.manifest}: {error.strerror}') from None
        except declaration.DeclarationError as error:
            raise GenerationError(str(error)) from None
    return parser(defaults).parse_args(argv)


def platform_origin_problem(args) -> str | None:
    """Why this is not an app the platform created (make app-repo, or the console's Start a new app)."""
    repository = f'{APP_OWNER}/{args.name}'
    create = f'create the app with make app-repo NAME={args.name} (or the console) and run the line it prints'
    if (args.from_repo or '').split('@')[0] != repository:
        return f'--from-repo {repository} is required: every app comes from a stack template; {create}'
    if parse_image(args.image)['repository'] != f'ghcr.io/{repository}':
        return f'the image must be ghcr.io/{repository}, which the app repository publishes; {create}'
    if not auto_deploys(args):
        return f'{repository} must deploy to staging automatically: leave out --no-auto-deploy'
    return None


def main(argv=None, opener: declaration.Opener = declaration._open) -> int:
    try:
        args = parse_args(argv, opener)
    except GenerationError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    try:
        if args.env != 'staging':
            raise GenerationError('app-new creates staging only; create production with make app-promote APP=' + args.name)
        problem = platform_origin_problem(args) if args.root.resolve() == ROOT else None
        if problem:
            raise GenerationError(problem)
        written = generate(args, args.root.resolve())
        if events.has_nats(args.root.resolve()) or args.calls or args.publish or args.subscribe:
            from swhurl.apps import edit  # edit imports this module
            try:
                if events.has_nats(args.root.resolve()):
                    # Every instance signs in to NATS as itself: its feature flag overrides, and events if it declares them.
                    edit.nats_key(args.root.resolve(), args.name, args.env)
                    edit.sync_env(args.root.resolve(), args.env)
                if args.calls:
                    edit.set_calls(args.root.resolve(), args.name, args.env, add=args.calls)
                if args.publish or args.subscribe:
                    edit.set_events(args.root.resolve(), args.name, args.env, publish=args.publish or None,
                                subscribe=args.subscribe or None)
            except edit.EditError as error:
                # Nothing half-made: the instance without its calls or events would start and fail.
                edit.remove(args.root.resolve(), args.name, args.env)
                raise GenerationError(f'NATS key, --calls, --publish or --subscribe: {error}') from None
        if getattr(args, 'offers', False) and args.from_repo:
            # The app describes its API or events (provides.apiFile, provides.eventsFile): keep the
            # documents beside the instance from the start, and publish what the events document says.
            from swhurl.apps import edit
            try:
                edit.set_offers(args.root.resolve(), args.name, args.env, token=os.environ.get('GITHUB_TOKEN', ''), opener=opener)
            except edit.EditError as error:
                edit.remove(args.root.resolve(), args.name, args.env)
                raise GenerationError(f'what the app offers could not be read: {error}') from None
    except GenerationError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    for path in written:
        print(f'[OK] wrote {path.relative_to(args.root.resolve())}')
    if args.secret_keys:
        print(f'[INFO] Values missing for {", ".join(args.secret_keys)}: in your own terminal, make app-secret '
              f'APP={args.name} ENV={args.env} NAME=<name> for each (the app starts without them)')
    if args.policy_check:
        return check_generated(instance_folder(args.root.resolve(), args.name, args.env))
    print('[INFO] Next: make check-apps, then commit, push and make flux-reconcile')
    return 0


def check_generated(instance: Path, runner: Runner | None = None) -> int:
    """Render what was just generated against the same contract check-apps enforces."""
    try:
        problems = policy.evaluate(instance, runner)
    except CommandError as error:
        print(f'[ERROR] could not validate the app policy ({error})', file=sys.stderr)
        print('[INFO] Files remain for review. Fix the validation failure and run make check-apps before committing.')
        return 1
    if problems:
        print('[BAD] the generated instance violates the app contract (generator and policy disagree):')
        for problem in problems:
            print(f'       {problem}')
        return 1
    print('[OK] generated instance passes the app policy')
    print('[INFO] Next: commit, push and make flux-reconcile')
    return 0
