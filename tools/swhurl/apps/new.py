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
create production directly (v2 promotes into an existing production, so a first
production has no records yet). Secret names are recorded without values; set each
value by hand in the instance's secret.sops.yaml.

`make check-apps` checks the rendered result; a v2-owned instance changes with
`swhurl-v2 edit`, `promote` or `remove` (apps/model, docs/plan.md section 24).
"""
from __future__ import annotations

import argparse
import os
import re
import shlex
import sys
from dataclasses import replace
from pathlib import Path

from swhurl import ROOT, app_alerts, interface, yamlio
from swhurl.apps import declaration, offers, policy
from swhurl.apps.calling import (
    NOTIFY,
)
from swhurl.apps.contract import (
    APP_OWNER,
    AUTO_DEPLOY_ENV,
    AUTO_DEPLOY_TAG_PATTERN,
    COOKIE_DOMAIN,
    JOB_SCHEDULE,
    JOB_TIME_ZONE,
    default_host,
    in_cookie_domain,
    schedule_problem,
    teams,
)
from swhurl.apps.defaults import DEFAULT_DATABASE_SIZE, secret_key_problem
from swhurl.apps.errors import EditError
from swhurl.apps.instance import ENVIRONMENTS, Instance, instance_folder, instance_unit_file
from swhurl.apps.model import records as app_records
from swhurl.apps.model import workload
from swhurl.apps.model.draft import Entry
from swhurl.apps.model.exposure import DEFAULT_ISSUER
from swhurl.apps.model.records import App, Choices, Grants, Image, Release, Resources, Settings
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
    if args.exposure == 'authenticated-web' and not args.host and app_records.NAME_RE.match(args.name):
        args.host = default_host(args.name, args.env)


def validate(args, root: Path = ROOT) -> None:
    if not app_records.NAME_RE.match(args.name):
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
        api_path=getattr(args, 'api_path', '') if web else '')  # provides.api only (--from-repo, --manifest)
    settings = Settings(
        grants=Grants(exposure=args.exposure, calls=tuple(args.calls or ()), subscriptions=tuple(args.subscribe or ())),
        choices=Choices(host=args.host if args.host and args.host != default_host(args.name, args.env) else '',
                        issuer='' if args.issuer == DEFAULT_ISSUER else args.issuer,
                        schedule='' if args.schedule == JOB_SCHEDULE else args.schedule))
    return Entry(App(args.team, args.alerts or ''), release, settings)


def generate(args, root: Path, runner: Runner | None = None, *, opener: declaration.Opener | None = None) -> list[Path]:
    """Prepare a new instance, allocations, encrypted credentials and offers with every other instance's
    derived files, then apply the complete plan. Planning failures leave the checkout unchanged."""
    from swhurl.apps import release as intake
    from swhurl.apps.model import write

    resolve(args)
    validate(args, root)
    created = Instance(args.name, args.env)  # validate() has refused any other name
    instance = instance_folder(root, args.name, args.env)
    if (instance.exists() and any(instance.iterdir())) or instance_unit_file(root, args.name, args.env).exists():
        raise GenerationError(f'{args.name}/{args.env} already exists; refusing to overwrite')
    entry, planned = records(args), {}
    try:
        if getattr(args, 'offers', False) and args.from_repo:
            # The app describes its API or events (provides.apiFile, provides.eventsFile): keep the documents
            # beside the instance from the start, and publish what the events document says.
            token = os.environ.get('GITHUB_TOKEN', '')
            image = entry.release.image
            repository, commit = intake.image_source(image, args.name, args.env)
            found = args.declaration  # parse_args read it at the image's commit
            files, described, served = intake.read_offers(repository, commit, found, token=token, **({'opener': opener} if opener else {}))
            entry = replace(entry, release=intake.offered(entry.release, commit, files, described, served))
            planned = intake.offer_files(instance / offers.OFFERS_DIR, repository, commit, files)
    except EditError as error:
        raise GenerationError(f'what the app offers could not be read: {error}') from None
    try:
        prepared = write.prepare(root, {created: write.Create(entry, args.database_size)}, runner=runner)
        prepared.writes.update(planned)
        return sorted(write.apply(root, prepared))
    except EditError as error:
        raise GenerationError(str(error)) from None


def dump(docs) -> str:
    return yamlio.dump_all(docs)


REPO_RE = re.compile(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')


def declared(spec: str, image: str, *, token: str = '', opener: declaration.Opener = declaration._open) -> declaration.Declaration:
    """The swhurl.yaml of ``OWNER/REPO`` on GitHub at the commit the image's tag names (``<run>-<sha>``): the
    one declaration a new instance's needs and offers both come from."""
    if '@' in spec:
        raise GenerationError(f'--from-repo takes OWNER/REPO without @REF: {declaration.FILE} is read at the commit '
                              f'the --image tag names: {spec}')
    if not REPO_RE.match(spec):
        raise GenerationError(f'--from-repo must be OWNER/REPO: {spec}')
    try:
        commit = offers.commit_of(parse_image(image).get('tag', ''))
    except offers.OfferError as error:
        raise GenerationError(f'--image: {error}') from None
    try:
        found = declaration.read(spec, commit, token=token, opener=opener)
    except declaration.DeclarationError as error:
        raise GenerationError(str(error)) from None
    if found is None:
        raise GenerationError(f'could not read {declaration.FILE} from {spec}@{commit}: it has no {declaration.FILE} '
                              'there, or the repository is private (set GITHUB_TOKEN)')
    return found


def parser(defaults: dict | None = None) -> argparse.ArgumentParser:
    """The app-new options, with swhurl.yaml's values as the defaults (explicit flags still win)."""
    p = argparse.ArgumentParser(prog='swhurl app-new', description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('name')
    source = p.add_mutually_exclusive_group()
    source.add_argument('--from-repo', metavar='OWNER/REPO',
                        help=f"the app's repository: defaults from its {declaration.FILE} on GitHub, at the commit the --image tag names")
    source.add_argument('--manifest', type=Path, metavar='PATH',
                        help=f'defaults from a local {declaration.FILE} (check-templates and tests)')
    p.add_argument('--env', default='staging', choices=ENVIRONMENTS, help='staging only; v2 has no first-production path')
    p.add_argument('--image', required=True, help=f'ghcr.io/{APP_OWNER}/<name>:<run>-<sha>@sha256:... as the app repository publishes it '
                                                        '(any REPO:TAG or digest form under another --root)')
    p.add_argument('--kind', choices=list(app_records.KINDS), default='web')
    p.add_argument('--schedule', default=JOB_SCHEDULE,
                   help=f'job only: when it runs, five cron fields in {JOB_TIME_ZONE} (default "{JOB_SCHEDULE}")')
    p.add_argument('--alerts', default='', metavar='SPEC',
                   help='the app\'s own default-alert settings: none, or error-logs=20,errors=none (default: the platform\'s)')
    p.add_argument('--team', help='the owning team, a name in apps/teams.yaml (required, here or as team: in swhurl.yaml)')
    p.add_argument('--exposure', choices=app_records.EXPOSURES, default='private',
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
    p.add_argument('--database', choices=app_records.DATABASES,
                   help=f'sqlite: a retained volume with the database at {interface.SQLITE_PATH}, passed as ${interface.DATABASE_PATH_VARIABLE}; '
                        'one replica, stop-first updates')
    p.add_argument('--database-size', default=DEFAULT_DATABASE_SIZE, metavar='SIZE', help='the database volume (default %(default)s)')
    p.add_argument('--subscribe', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated <app>.<event> it receives: each must be published in this environment')
    p.add_argument('--description', metavar='TEXT', help="one line saying what the app does (swhurl.yaml's description): "
                   'SWHURL_DESCRIPTION, which the templates serve in their API description')
    p.add_argument('--calls', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated apps this one calls: absent apps or consent wait without access')
    p.add_argument('--secret-keys', type=lambda s: [k.strip() for k in s.split(',') if k.strip()],
                   help='comma-separated secret variable names the app reads; values set by hand, never here')
    p.add_argument('--otlp', action=argparse.BooleanOptionalAction, default=False,
                   help='the app has an OpenTelemetry SDK: point it at the cluster collector (OTEL_* env)')
    p.add_argument('--auto-deploy', action=argparse.BooleanOptionalAction, default=False,
                   help='staging only: Flux deploys each newer image the app publishes (tags <run>-<sha>); '
                        'production still changes through a promotion')
    p.add_argument('--issuer', default='letsencrypt-prod', choices=['letsencrypt-prod', 'letsencrypt-staging', 'selfsigned'])
    p.add_argument('--root', type=Path, default=ROOT)
    p.add_argument('--no-policy-check', dest='policy_check', action='store_false',
                   help='skip rendering the new instance against the app policy (needs helm)')
    if defaults:
        p.set_defaults(**defaults)
    return p


def parse_args(argv=None, opener: declaration.Opener = declaration._open) -> argparse.Namespace:
    argv = sys.argv[1:] if argv is None else argv
    if any(arg.partition('=')[0] == '--publish' for arg in argv):
        raise GenerationError('--publish is no longer supported: declare send operations in asyncapi.yaml '
                              'named by swhurl.yaml provides.eventsFile')
    pre = argparse.ArgumentParser(add_help=False)
    pre.add_argument('--from-repo')
    pre.add_argument('--manifest', type=Path)
    pre.add_argument('--image')
    known, _ = pre.parse_known_args(argv)
    defaults = None
    if known.from_repo and known.manifest:
        raise GenerationError('--from-repo and --manifest cannot be combined')
    if known.from_repo and known.image:  # without --image the parser below says it is required
        found = declared(known.from_repo, known.image, token=os.environ.get('GITHUB_TOKEN', ''), opener=opener)
        defaults = {**found.seed, 'declaration': found}  # generate reads what the app offers from this one
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
    if args.from_repo != repository:
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
            raise GenerationError('app-new creates staging only: a first production is not possible yet, because a '
                                  'promotion only moves an existing production')
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
        print(f'[INFO] Values missing for {", ".join(args.secret_keys)}: set each one by hand in '
              f'apps/{args.name}/{args.env}/secret.sops.yaml (the app starts without them)')
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
