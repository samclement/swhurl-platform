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

Change it afterwards with the make app-* commands: `make check-apps` checks the rendered
result, and refuses a hand edit the app model cannot express (apps/model, docs/plan.md section 24).
"""
from __future__ import annotations

import argparse
import os
import re
import shlex
import shutil
import sys
from dataclasses import replace
from pathlib import Path

import yaml

from swhurl import ROOT, app_alerts
from swhurl.apps import declaration, events, offers, policy
from swhurl.apps.calling import (
    NOTIFY,
)
from swhurl.apps.contract import (
    APP_OWNER,
    AUTO_DEPLOY_ENV,
    AUTO_DEPLOY_TAG_PATTERN,
    COOKIE_DOMAIN,
    DATABASE_PATH_VARIABLE,
    DATABASES,
    DEFAULT_DATABASE_SIZE,
    EXPOSURES,
    JOB_SCHEDULE,
    JOB_TIME_ZONE,
    RETAINED_STORAGE_CLASS,
    SECRET_FILE,
    SQLITE_PATH,
    app_secret,
    default_host,
    in_cookie_domain,
    schedule_problem,
    secret_key_problem,
    teams,
)
from swhurl.apps.instance import ENVIRONMENTS, NAME_RE, Instance, instance_folder, instance_unit_file
from swhurl.apps.model import workload
from swhurl.apps.model.draft import APP_SECRET, EVENTS_SECRET, Entry
from swhurl.apps.model.exposure import DEFAULT_ISSUER
from swhurl.apps.model.records import Allocations, App, Image, Release, Resources, Settings, Storage
from swhurl.apps.yaml_file import EditError
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
    if args.database and args.kind != 'web':
        raise GenerationError(f'a {args.kind} keeps no database: it keeps what it needs in an API it calls (docs/apps.md#app-types)')
    if args.kind == 'job':
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


def records(args) -> Entry:
    """The new instance's records, from the request (its swhurl.yaml's fields and the flags)."""
    image = parse_image(args.image)
    web = args.kind == 'web'
    resources = Resources(args.cpu, args.memory, args.memory_limit)
    release = Release(
        Image(image['repository'], image.get('tag', ''), image.get('digest', '')), args.kind, args.uid,
        command=tuple(shlex.split(args.command)) if args.command else (), port=args.port if web else None,
        health_path=args.health_path if web else '', telemetry=bool(args.otlp),
        resources=Resources() if resources == workload.DEFAULT_RESOURCES else resources, database=args.database or '',
        secrets=tuple(args.secret_keys or ()), description=args.description or '',
        api_path=(args.api_path or '') if web else '', publishes=tuple(args.publish or ()))
    settings = Settings(
        exposure=args.exposure, host=args.host if args.host and args.host != default_host(args.name, args.env) else '',
        issuer='' if args.issuer == DEFAULT_ISSUER else args.issuer, calls=tuple(args.calls or ()),
        subscriptions=tuple(args.subscribe or ()), schedule='' if args.schedule == JOB_SCHEDULE else args.schedule,
        allocations=Allocations(database=Storage(args.database_size, RETAINED_STORAGE_CLASS) if args.database else None))
    return Entry(App(args.team, args.alerts or ''), release, settings)


def discard(instance: Path) -> None:
    """Remove an instance being created, and its app's folder if nothing else is in it."""
    shutil.rmtree(instance, ignore_errors=True)
    if instance.parent.is_dir() and not any(instance.parent.iterdir()):
        instance.parent.rmdir()


def generate(args, root: Path, runner: Runner | None = None, *, opener: declaration.Opener | None = None) -> list[Path]:
    """Create the instance: its records rendered with every other instance's (apps/model), after its own
    credentials are made (a NATS key where there is NATS; a first production's Secret placeholders) and,
    for an app that describes itself, what it offers read from its repository at the image's commit.
    Nothing is left behind when a step fails."""
    from swhurl.apps import release as intake
    from swhurl.apps.model import sync as model_sync

    resolve(args)
    validate(args, root)
    created = Instance(args.name, args.env)  # validate() has refused any other name
    instance = instance_folder(root, args.name, args.env)
    prepared = {instance / events.SECRET_FILE} if args.events_key else set()
    if args.events_key and not (instance / events.SECRET_FILE).is_file():
        raise GenerationError(f'--events-key needs its seed in {instance / events.SECRET_FILE}')
    if instance.exists() and set(instance.iterdir()) - prepared or instance_unit_file(root, args.name, args.env).exists():
        raise GenerationError(f'{args.name}/{args.env} already exists; refusing to overwrite')
    entry, planned = records(args), {}
    try:
        if getattr(args, 'offers', False) and args.from_repo:
            # The app describes its API or events (provides.apiFile, provides.eventsFile): keep the documents
            # beside the instance from the start, and publish what the events document says.
            token = os.environ.get('GITHUB_TOKEN', '')
            image = entry.release.image
            repository, commit = intake.image_source(image, args.name, args.env)
            found = intake.image_declaration(image, args.name, args.env, token=token, **({'opener': opener} if opener else {}))
            files, described, served = intake.read_offers(repository, commit, found, token=token, **({'opener': opener} if opener else {}))
            entry = replace(entry, release=intake.offered(entry.release, commit, files, described, served))
            planned = intake.offer_files(instance / offers.OFFERS_DIR, repository, commit, files)
    except EditError as error:
        raise GenerationError(f'what the app offers could not be read: {error}') from None
    instance.mkdir(parents=True, exist_ok=True)
    try:
        encrypted = set()
        if args.secret_keys and args.env == 'prod':
            # A first production never runs without a value: make check-secrets refuses the placeholders.
            write_encrypted_secret(instance, app_secret(args.name), created.namespace, args.secret_keys, runner=runner)
            encrypted.add(APP_SECRET)
        allocations = entry.settings.allocations
        if args.events_key:
            allocations = replace(allocations, events_key=args.events_key)
            encrypted.add(EVENTS_SECRET)
        elif events.has_nats(root) or entry.release.publishes or entry.settings.subscriptions:
            # Every instance signs in to NATS as itself: its feature flag overrides, and events if it declares them.
            allocations = replace(allocations, events_key=intake.new_nats_key(instance, args.name, args.env, runner=runner))
            encrypted.add(EVENTS_SECRET)
        entry = replace(entry, settings=replace(entry.settings, allocations=allocations), encrypted=frozenset(encrypted))
        prepared = model_sync.prepare(root, created, lambda _: entry, adding=True)
        written = model_sync.apply(root, prepared, register=args.register)
        intake.write_files(planned)
    except (EditError, CommandError) as error:
        discard(instance)
        raise GenerationError(str(error)) from None
    except Exception:
        discard(instance)
        raise
    return sorted({*written, *(instance / name for name in ('events.sops.yaml', SECRET_FILE) if (instance / name).is_file()),
                   *planned})


def dump(docs) -> str:
    return yaml.safe_dump_all(docs, sort_keys=False, default_flow_style=False)


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
    p.add_argument('--events-key', metavar='PUBLIC', help='the NATS public key whose seed is already in the new '
                   f"instance's {events.SECRET_FILE} (the merge gate replaying a new app's pull request); default: make one")
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
        written = generate(args, args.root.resolve(), opener=opener)
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
