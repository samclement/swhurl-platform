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
create production directly (use app-promote). Secret stubs are SOPS-encrypted
before this script returns, so plaintext never reaches Git.

The output is ordinary YAML: edit it by hand afterwards. `make check-apps`
checks the rendered result.
"""
from __future__ import annotations

import argparse
import os
import re
import shlex
import sys
import urllib.error
import urllib.request
from collections.abc import Callable
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import policy
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
    DATABASE_PATH_ENV,
    DATABASES,
    DEFAULT_DATABASE_SIZE,
    ENVIRONMENT,
    ENVIRONMENTS,
    EXPOSURE,
    EXPOSURES,
    FAIL_AFTER,
    IMAGE_AUTOMATION_FILE,
    MANAGED,
    MANIFEST_FILE,
    RETAINED_STORAGE_CLASS,
    SQLITE_PATH,
    STARTUP_PERIOD,
    STARTUP_SECONDS,
    ManifestError,
    add_image_markers,
    default_host,
    image_policy_name,
    in_cookie_domain,
    manifest_defaults,
    otlp_env,
    secret_key_problem,
)
from swhurl.run import CommandError, Runner

NAME_RE = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
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


def validate(args) -> None:
    if not NAME_RE.match(args.name):
        raise GenerationError('NAME must be a DNS label (lowercase, digits, hyphens, max 40 chars)')
    if args.secret_keys and (problem := secret_key_problem(args.secret_keys)):
        raise GenerationError(problem)
    if args.kind == 'worker' and args.exposure != 'private':
        raise GenerationError('a worker has no Service or route; use --exposure private')
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
    """App-scoped Flux image resources, including its own Git writer for named deploy events."""
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
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImageUpdateAutomation',
         'metadata': {'name': f'{args.name}-staging', 'namespace': 'flux-system', 'labels': labels},
         # Runs when its ImagePolicy picks a new image; the interval only retries a missed one.
         'spec': {'interval': '1h', 'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform-write'},
                  'git': {'checkout': {'ref': {'branch': 'main'}},
                          'commit': {'author': {'name': 'fluxcdbot',
                                                'email': 'fluxcdbot@users.noreply.github.com'},
                                     'messageTemplate': f'deploy: staging image for {args.name}\n'
                                                        '{{ range .Changed.Changes }}\n'
                                                        '- {{ .OldValue }} -> {{ .NewValue }}\n'
                                                        '{{- end }}'},
                          'push': {'branch': 'main'}},
                  'update': {'path': f'./apps/{args.name}/staging', 'strategy': 'Setters'}}},
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
    if args.otlp:
        container['env'] = otlp_env(args.name)
    if args.database == 'sqlite':
        container['env'] = {**container.get('env', {}), DATABASE_PATH_ENV: SQLITE_PATH}
    if args.secret_keys:
        container['envFrom'] = [{'secretRef': {'name': f'{args.name}-secret'}}]
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
    if args.persistence:
        # One writer: the volume is ReadWriteOnce on one node, and a database file must not have two
        # processes writing it. Stop the old pod before starting the new one; never run two (rule single-writer).
        controller['replicas'] = 1
        controller['strategy'] = 'Recreate'
    if args.secret_keys:
        controller['annotations'] = {'secret.reloader.stakater.com/reload': f'{args.name}-secret'}

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


def generate(args, root: Path, runner: Runner | None = None) -> list[Path]:
    resolve(args)
    validate(args)
    namespace = f'{args.name}-{args.env}'
    unit = f'app-{args.name}-{args.env}'
    instance = root / 'apps' / args.name / args.env
    unit_file = root / 'clusters/home' / f'app-{args.name}-{args.env}.yaml'
    for path in (instance, unit_file):
        if path.exists():
            raise GenerationError(f'{path} already exists; refusing to overwrite')

    labels = {
        MANAGED: 'true',
        APP: args.name,
        ENVIRONMENT: args.env,
        EXPOSURE: args.exposure,
    }
    ns = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace, 'labels': labels}}
    if args.persistence:
        # Keeps the namespace (and so the claim) when the app unit is removed; see destroy-data.
        ns['metadata']['annotations'] = {'kustomize.toolkit.fluxcd.io/prune': 'disabled'}
    release = {
        'apiVersion': 'helm.toolkit.fluxcd.io/v2',
        'kind': 'HelmRelease',
        'metadata': {'name': args.name, 'namespace': namespace},
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
    resources = ['namespace.yaml', 'helmrelease.yaml']
    if args.secret_keys:
        resources.insert(1, 'secret.sops.yaml')
    release_text = dump([release])
    if auto_deploys(args):
        resources.append(IMAGE_AUTOMATION_FILE)
        release_text = add_image_markers(release_text, image_policy_name(args.name))
    files = {
        instance / 'namespace.yaml': dump([ns]),
        instance / 'helmrelease.yaml': release_text,
        instance / 'kustomization.yaml': dump([{'apiVersion': 'kustomize.config.k8s.io/v1beta1',
                                               'kind': 'Kustomization', 'resources': resources}]),
    }

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
    if args.secret_keys:
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
        if args.secret_keys:
            written.append(write_encrypted_secret(instance, f'{args.name}-secret', namespace, args.secret_keys, runner=runner))
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
    path = instance / 'secret.sops.yaml'
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
Opener = Callable[[urllib.request.Request], bytes]


def _open(request: urllib.request.Request) -> bytes:
    with urllib.request.urlopen(request, timeout=30) as reply:  # noqa: S310 (https only, built below)
        return reply.read()


def fetch_manifest(spec: str, *, token: str = '', opener: Opener = _open,
                   api: str = 'https://api.github.com') -> str:
    """swhurl.yaml from ``OWNER/REPO[@REF]`` on GitHub (default branch without a ref)."""
    match = REPO_RE.match(spec)
    if not match:
        raise GenerationError(f'--from-repo must be OWNER/REPO or OWNER/REPO@REF: {spec}')
    url = f'{api}/repos/{match["repo"]}/contents/{MANIFEST_FILE}' + (f'?ref={match["ref"]}' if match['ref'] else '')
    headers = {'Accept': 'application/vnd.github.raw+json', 'X-GitHub-Api-Version': '2022-11-28',
               'User-Agent': 'swhurl-app-new'}
    if token:
        headers['Authorization'] = f'Bearer {token}'
    try:
        return opener(urllib.request.Request(url, headers=headers)).decode()
    except urllib.error.HTTPError as error:
        hint = f'{spec} has no {MANIFEST_FILE}, or is private (set GITHUB_TOKEN)' if error.code == 404 else error.reason
        raise GenerationError(f'could not read {MANIFEST_FILE} from {spec}: {error.code} {hint}') from None
    except urllib.error.URLError as error:
        raise GenerationError(f'could not reach GitHub for {spec}: {error.reason}') from None


def load_manifest(text: str, source: str) -> dict:
    try:
        return manifest_defaults(yaml.safe_load(text), source)
    except yaml.YAMLError as error:
        raise GenerationError(f'{source}: not valid YAML: {error}') from None
    except ManifestError as error:
        raise GenerationError(str(error)) from None


def parser(defaults: dict | None = None) -> argparse.ArgumentParser:
    """The app-new options, with swhurl.yaml's values as the defaults (explicit flags still win)."""
    p = argparse.ArgumentParser(prog='swhurl app-new', description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('name')
    source = p.add_mutually_exclusive_group()
    source.add_argument('--from-repo', metavar='OWNER/REPO[@REF]',
                        help=f"the app's repository: defaults from its {MANIFEST_FILE} on GitHub")
    source.add_argument('--manifest', type=Path, metavar='PATH',
                        help=f'defaults from a local {MANIFEST_FILE} (check-templates and tests)')
    p.add_argument('--env', default='staging', choices=ENVIRONMENTS, help='staging only; production is created by app-promote')
    p.add_argument('--image', required=True, help=f'ghcr.io/{APP_OWNER}/<name>:<run>-<sha>@sha256:... as the app repository publishes it '
                                                        '(any REPO:TAG or digest form under another --root)')
    p.add_argument('--kind', choices=['web', 'worker'], default='web')
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
                   help=f'sqlite: a retained volume with the database at {SQLITE_PATH}, passed as ${DATABASE_PATH_ENV}; '
                        'one replica, stop-first updates')
    p.add_argument('--database-size', default=DEFAULT_DATABASE_SIZE, metavar='SIZE', help='the database volume (default %(default)s)')
    p.add_argument('--persistence', metavar='SIZE', help='add a retained volume, e.g. 1Gi (one replica, stop-first updates)')
    p.add_argument('--mount-path', default='/data')
    p.add_argument('--secret-keys', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated keys for an encrypted Secret stub (values REPLACE_ME)')
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


def parse_args(argv=None, opener: Opener = _open) -> argparse.Namespace:
    pre = argparse.ArgumentParser(add_help=False)
    pre.add_argument('--from-repo')
    pre.add_argument('--manifest', type=Path)
    known, _ = pre.parse_known_args(argv)
    defaults = None
    if known.from_repo and known.manifest:
        raise GenerationError('--from-repo and --manifest cannot be combined')
    if known.from_repo:
        text = fetch_manifest(known.from_repo, token=os.environ.get('GITHUB_TOKEN', ''), opener=opener)
        defaults = load_manifest(text, f'{known.from_repo}:{MANIFEST_FILE}')
    elif known.manifest:
        try:
            text = known.manifest.read_text()
        except OSError as error:
            raise GenerationError(f'cannot read {known.manifest}: {error.strerror}') from None
        defaults = load_manifest(text, str(known.manifest))
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
        return (f'{repository} must deploy to staging automatically: set autoDeploy: true in its {MANIFEST_FILE} '
                '(the stack templates do)')
    return None


def main(argv=None, opener: Opener = _open) -> int:
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
    except GenerationError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    for path in written:
        print(f'[OK] wrote {path.relative_to(args.root.resolve())}')
    if args.secret_keys:
        print(f'[INFO] Set real values: sops {written[-1].relative_to(args.root.resolve())}')
    if args.policy_check:
        return check_generated(args.root.resolve() / 'apps' / args.name / args.env)
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
