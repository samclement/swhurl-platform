"""Generate one app instance: namespace, app-template HelmRelease, Flux unit.

    make app-new NAME=<app> ARGS="--env staging|prod --image IMAGE [options]"
    python3 -m swhurl app-new NAME --env staging|prod --image IMAGE [options]

Writes apps/NAME/ENV/ and clusters/home/app-NAME-ENV.yaml, and registers
the unit in clusters/home/kustomization.yaml. Refuses to overwrite, to expose a
worker, to put a public app inside the shared sign-in cookie domain, and to
deploy production without an image digest. Secret stubs are SOPS-encrypted
before this script returns, so plaintext never reaches Git.

The output is ordinary YAML: edit it by hand afterwards. `make check-apps`
checks the rendered result.
"""
from __future__ import annotations

import argparse
import re
import shlex
import sys
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import policy
from swhurl.apps.contract import (
    APP,
    AUTH_MIDDLEWARE,
    AUTO_DEPLOY_ENV,
    AUTO_DEPLOY_TAG_PATTERN,
    CHART,
    CHART_REPOSITORY,
    CHART_VERSION,
    COOKIE_DOMAIN,
    ENVIRONMENT,
    ENVIRONMENTS,
    EXPOSURE,
    EXPOSURES,
    IMAGE_AUTOMATION_FILE,
    MANAGED,
    PRESETS,
    RETAINED_STORAGE_CLASS,
    add_image_markers,
    default_host,
    image_policy_name,
    in_cookie_domain,
    otlp_env,
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
    """Fill values derived from others: a signed-in app's host."""
    if args.exposure == 'authenticated-web' and not args.host and NAME_RE.match(args.name):
        args.host = default_host(args.name, args.env)


def validate(args) -> None:
    if not NAME_RE.match(args.name):
        raise GenerationError('NAME must be a DNS label (lowercase, digits, hyphens, max 40 chars)')
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
                                  "template's workflow publishes it (or use --no-auto-deploy)")


def auto_deploys(args) -> bool:
    """--auto-deploy applies to staging only; production changes through app-promote."""
    return bool(args.auto_deploy) and args.env == AUTO_DEPLOY_ENV


def image_automation(args) -> list[dict]:
    """The ImageRepository and ImagePolicy Flux uses to find newer images of this app (flux-system)."""
    repository = parse_image(args.image)['repository']
    labels = {MANAGED: 'true', APP: args.name}
    return [
        {'apiVersion': 'image.toolkit.fluxcd.io/v1', 'kind': 'ImageRepository',
         'metadata': {'name': args.name, 'namespace': 'flux-system', 'labels': labels},
         'spec': {'image': repository, 'interval': '1m'}},
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
    if args.otlp:
        container['env'] = otlp_env(args.name)
    if args.secret_keys:
        container['envFrom'] = [{'secretRef': {'name': f'{args.name}-secret'}}]
    if args.kind == 'web':
        def probe():
            return {'enabled': True, 'custom': True,
                    'spec': {'httpGet': {'path': args.health_path, 'port': args.port}}}
        container['probes'] = {'readiness': probe(), 'liveness': probe()}

    controller: dict = {'containers': {'main': container}}
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


def generate(args, root: Path) -> list[Path]:
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
            'chart': {'spec': {'chart': CHART, 'version': CHART_VERSION,
                               'sourceRef': {'kind': 'HelmRepository', 'name': CHART_REPOSITORY, 'namespace': 'flux-system'},
                               'interval': '30m'}},
            'install': {'remediation': {'retries': 3}},
            'upgrade': {'remediation': {'retries': 3}},
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
    spec = {
        'dependsOn': [{'name': d} for d in depends],
        'interval': '10m',
        'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform'},
        'path': './' + instance.relative_to(root).as_posix(),
    }
    if args.secret_keys:
        spec['decryption'] = {'provider': 'sops', 'secretRef': {'name': 'sops-age'}}
    # App unit: default MirrorPrune, so removing it from Git uninstalls the instance.
    spec |= {'prune': True, 'wait': True, 'timeout': '10m'}
    files[unit_file] = dump([{'apiVersion': 'kustomize.toolkit.fluxcd.io/v1', 'kind': 'Kustomization',
                              'metadata': {'name': unit, 'namespace': 'flux-system'}, 'spec': spec}])

    written = []
    try:
        for path, text in files.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
            written.append(path)
        if args.secret_keys:
            written.append(write_encrypted_secret(instance, f'{args.name}-secret', namespace, args.secret_keys))
    except Exception:
        for path in written:
            path.unlink(missing_ok=True)
        raise

    if args.register:
        register(root, unit_file.name)
    if args.secret_keys:
        watch_namespace(root, namespace)
    return written


def write_encrypted_secret(instance: Path, name: str, namespace: str, keys: list[str]) -> Path:
    path = instance / 'secret.sops.yaml'
    secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': namespace},
              'type': 'Opaque', 'stringData': {k: 'REPLACE_ME' for k in keys}}
    path.write_text(dump([secret]))
    try:
        Runner().run(['sops', '--encrypt', '--in-place', str(path)])
    except CommandError as error:
        path.unlink(missing_ok=True)
        raise GenerationError(f'could not SOPS-encrypt {path} (is there a .sops.yaml rule?): {error}') from error
    if 'sops:' not in path.read_text():
        path.unlink()
        raise GenerationError(f'{path} was not encrypted; removed it')
    return path


def register(root: Path, filename: str) -> None:
    path = root / 'clusters/home/kustomization.yaml'
    text = path.read_text()
    if f'- {filename}' not in text:
        path.write_text(text.rstrip('\n') + f'\n  - {filename}\n')


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


def parser(preset: str | None = None) -> argparse.ArgumentParser:
    """The app-new options, with a preset's values as the defaults (explicit flags still win)."""
    p = argparse.ArgumentParser(prog='swhurl app-new', description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('name')
    p.add_argument('--preset', choices=sorted(PRESETS),
                   help='defaults for an app built from the swhurl template: '
                        + '; '.join(f'{n}: ' + ', '.join(f'{k}={v}' for k, v in d.items()) for n, d in PRESETS.items()))
    p.add_argument('--env', required=True, choices=ENVIRONMENTS)
    p.add_argument('--image', required=True, help='REPO:TAG, REPO@sha256:..., or REPO:TAG@sha256:... (digest required for prod)')
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
    p.add_argument('--persistence', metavar='SIZE', help='add a retained volume, e.g. 1Gi')
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
    if preset:
        p.set_defaults(**PRESETS[preset])
    return p


def parse_args(argv=None) -> argparse.Namespace:
    pre = argparse.ArgumentParser(add_help=False)
    pre.add_argument('--preset', choices=sorted(PRESETS))
    known, _ = pre.parse_known_args(argv)
    return parser(known.preset).parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    try:
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
        print(f'[WARN] could not run the app policy ({error}); run make check-apps before committing')
        return 0
    if problems:
        print('[BAD] the generated instance violates the app contract (generator and policy disagree):')
        for problem in problems:
            print(f'       {problem}')
        return 1
    print('[OK] generated instance passes the app policy')
    print('[INFO] Next: commit, push and make flux-reconcile')
    return 0
