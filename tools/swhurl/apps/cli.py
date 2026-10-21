"""App edit command arguments, terminal input, exit codes and post-command checks."""
from __future__ import annotations

import argparse
import os
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from swhurl import ROOT, app_alerts, platform
from swhurl.apps import edit, egress, images, offers, ops, promotion
from swhurl.apps.errors import EditError
from swhurl.apps.instance import ENVIRONMENTS, Instance
from swhurl.apps.model import records, workload, write
from swhurl.apps.new import check_generated
from swhurl.apps.release import instance_dir, set_offers
from swhurl.run import CommandError, Runner


def run(action, check_dir: bool) -> int:
    """Run an edit; on success check the edited instance against the app policy."""
    try:
        result = action()
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    if check_dir and result:
        return check_generated(result)
    print('[INFO] Next: make check-apps, then commit, push and make flux-reconcile')
    return 0


def main_promote(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-promote', description='Copy an app image (tag and digest) between environments')
    p.add_argument('app')
    p.add_argument('--expect-image', help='refuse if the source no longer names this exact repository, tag and digest')
    p.add_argument('--host', help='distinct production hostname for first public promotion')
    p.add_argument('--prepare-only', action='store_true', help=argparse.SUPPRESS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()

    def changed(applied: str, newest: str) -> list[str] | None:
        """The paths that differ between two commits in this checkout's history."""
        root = args.root.resolve()
        if not (root / '.git').exists():
            return None
        found = runner.run(['git', '-C', root, 'diff', '--name-only', applied, newest], check=False)
        return None if found.returncode else found.stdout.splitlines()

    def change():
        root = args.root.resolve()
        world, _ = write.read(root)
        decision = promotion.select(root, world, args.app)
        if args.expect_image and args.expect_image != decision.image:
            raise EditError('staging image changed since it was reviewed; refresh and review it again')
        if decision.outcome == 'same':
            raise EditError('production already runs this digest; same image in both')
        if not args.prepare_only:  # console checks once, after preparing scratch files and before publication
            source_instance = Instance.named(args.app, 'staging')
            status = promotion.settled(ops.gather_status(runner, source_instance), changed) if source_instance else None
            problem = promotion.source_problem(status, decision.image)
            if problem:
                raise EditError(problem)
        return edit.promote(root, args.app, expect_image=args.expect_image, host=args.host, runner=runner)

    try:
        return run(change, True)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1


def main_scale(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-scale', description='Change replicas or resources of an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--replicas', type=int)
    p.add_argument('--cpu')
    p.add_argument('--memory')
    p.add_argument('--memory-limit')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)

    def change():
        return edit.scale(args.root.resolve(), args.app, args.env, replicas=args.replicas, cpu=args.cpu,
                     memory=args.memory, memory_limit=args.memory_limit)
    return run(change, True)


def main_schedule(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-schedule', description='Change when a job runs')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--schedule', required=True, help='five cron fields in Etc/UTC, for example "*/10 * * * *", or @hourly, @daily')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.schedule(args.root.resolve(), args.app, args.env, args.schedule), True)


def main_expose(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-expose', description='Change who can reach an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--exposure', required=True, choices=records.EXPOSURES,
                   help='private: no route; authenticated-web: Google sign-in; public: no sign-in, host outside the domain')
    p.add_argument('--host', help='default for authenticated-web: the current host if signed-in, else the derived one')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.expose(args.root.resolve(), args.app, args.env, args.exposure, args.host), True)


def main_team(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-team', description='Move an app to a team registered in apps/teams.yaml')
    p.add_argument('app')
    p.add_argument('--team', required=True)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.set_team(args.root.resolve(), args.app, args.team), True)


def main_alerts(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-alerts', description='Change an app\'s default alerts: their thresholds, or none')
    p.add_argument('app')
    for preset in app_alerts.PRESETS:
        p.add_argument(f'--{preset.key}', metavar='N|none', help=f'{preset.label}: fire at N in {preset.interval} '
                                                                  f'(default {preset.threshold}), or none')
    p.add_argument('--none', action='store_true', help='no default alerts for this app')
    p.add_argument('--default', action='store_true', help='back to the defaults')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    changes = {preset.key: value for preset in app_alerts.PRESETS
               if (value := getattr(args, preset.key.replace('-', '_'))) is not None}
    return run(lambda: edit.set_alerts(args.root.resolve(), args.app, changes, none=args.none, default=args.default), True)


def main_calls(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-calls', description='Change which apps an instance may call')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help='comma-separated apps to call (each needs this environment)')
    p.add_argument('--remove', type=names, default=[], help='comma-separated apps to stop calling')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.set_calls(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_allows(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-allows', description='Change which apps may call an instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help='comma-separated app names or team:<team> to allow')
    p.add_argument('--remove', type=names, default=[], help='comma-separated entries to stop allowing')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.set_allows(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_egress(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-egress', description='Change which hosts outside the cluster an instance may reach')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help=f'comma-separated host[:port] (port {egress.DEFAULT_PORT} '
                                                         'by default; *.example.com is any name under example.com)')
    p.add_argument('--remove', type=names, default=[], help='comma-separated host[:port] to stop granting')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.set_egress(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_events(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-events', description='Set the events an instance receives; '
                                'publications come from asyncapi.yaml named by provides.eventsFile')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--subscribe', type=names, help='comma-separated <app>.<event> it receives ("" for none)')
    p.add_argument('--add-subscribe', type=names, default=[], help='comma-separated <app>.<event> to start receiving')
    p.add_argument('--remove-subscribe', type=names, default=[], help='comma-separated <app>.<event> to stop receiving')
    p.add_argument('--root', type=Path, default=ROOT)
    argv = sys.argv[1:] if argv is None else argv
    if any(arg.partition('=')[0] in ('--publish', '--add-publish', '--remove-publish') for arg in argv):
        p.error('publishing flags are no longer supported: edit asyncapi.yaml named by swhurl.yaml provides.eventsFile')
    args = p.parse_args(argv)
    return run(lambda: edit.set_events(args.root.resolve(), args.app, args.env, subscribe=args.subscribe,
                                  add_subscribe=args.add_subscribe, remove_subscribe=args.remove_subscribe), True)


def main_offers(argv: list[str] | None = None, opener: offers.Opener = offers._open) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-offers',
                                description="Read what an app offers (its API and event documents) from its repository at its image's commit")
    p.add_argument('app', nargs='?')
    p.add_argument('env', nargs='?', choices=ENVIRONMENTS)
    p.add_argument('--all', choices=ENVIRONMENTS, metavar='ENV', help='every instance of this environment that an app repository publishes')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    if bool(args.all) == bool(args.app and args.env):
        p.error('give APP and ENV, or --all ENV')
    root, token = args.root.resolve(), os.environ.get('GITHUB_TOKEN', '')
    if not args.all:
        # A change is checked against the app policy: run() takes the instance's folder, not set_offers' answer.
        return run(lambda: set_offers(root, args.app, args.env, token=token, opener=opener) and instance_dir(root, args.app, args.env), True)
    failed = 0
    try:
        world, _ = write.read(root)
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    for each, entry in world.items():
        if each.env != args.all:
            continue
        app, image = each.app, entry.release.image
        if not image.repository.startswith('ghcr.io/') or not offers.IMAGE_COMMIT.match(image.tag):
            continue  # not built from an app repository (a fixture, a pinned third-party image)
        try:
            set_offers(root, app, args.all, token=token, opener=opener)
        except EditError as error:  # the others are still read; this one keeps what it had
            failed += 1
            print(f'[ERROR] {app}/{args.all}: {error}', file=sys.stderr)
    return 1 if failed else 0


def main_deploy(argv: list[str] | None = None, opener: offers.Opener = offers._open) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-deploy',
                                description="Bring staging to each app's newest image, with what that image needs and offers")
    p.add_argument('app', nargs='?', help='one app (default: every staging instance with an ImagePolicy)')
    p.add_argument('--refresh', action='store_true',
                   help="read swhurl.yaml again for an image staging already runs (after a change to how the platform applies it)")
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    root, token = args.root.resolve(), os.environ.get('GITHUB_TOKEN', '')
    if args.app:
        return run(lambda: edit.deploy_staging(root, args.app, token=token, opener=opener, refresh=args.refresh)
                   and instance_dir(root, args.app, 'staging'), True)
    try:
        world, _ = write.read(root)
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    deployed = [each.app for each, entry in world.items() if each.env == 'staging'
                and entry.release.image.repository == workload.platform_image(each.app)]

    def newest(app: str) -> images.Image | EditError:
        try:
            return edit.newest_image(root, app, opener=opener)
        except EditError as error:
            return error
    with ThreadPoolExecutor(max_workers=8) as pool:  # registry reads only; the edits below run one at a time
        found = dict(zip(deployed, pool.map(newest, deployed), strict=True))
    failed = 0
    for app in deployed:
        try:
            if isinstance(found[app], EditError):
                raise found[app]
            edit.deploy_staging(root, app, token=token, opener=opener, found=found[app], refresh=args.refresh)
        except EditError as error:  # the others still deploy; this one keeps its image
            failed += 1
            print(f'[ERROR] {app}/staging: {error}', file=sys.stderr)
    return 1 if failed else 0


def main_secret(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-secret', description='Set one secret value of an app instance '
                                '(read from standard input, or asked for without echo); needs the age key')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('name', help='the variable name, such as API_TOKEN')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    if runner is None:  # the age key as make check-secrets finds it
        key_file = os.environ.get('SOPS_AGE_KEY_FILE') or str(ROOT / platform.AGE_KEY)
        if not Path(key_file).is_file():
            print(f'[ERROR] age key not found at {key_file}; set SOPS_AGE_KEY_FILE', file=sys.stderr)
            return 2
        runner = Runner(env={'SOPS_AGE_KEY_FILE': key_file})
    if sys.stdin.isatty():
        import getpass
        value = getpass.getpass(f'{args.name} for {args.app}/{args.env} (not shown): ')
    else:
        value = sys.stdin.read().removesuffix('\n')
    return run(lambda: edit.set_secret(args.root.resolve(), args.app, args.env, args.name, value, runner=runner), True)


def main_sync(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-sync', description='Rewrite what the platform derives in every instance')
    p.add_argument('--root', type=Path, default=ROOT)
    p.add_argument('--rotate-nkey', metavar='APP/ENV', help='replace one existing complete NATS identity (may interrupt connections)')
    args = p.parse_args(argv)
    target = None
    if args.rotate_nkey is not None:
        parts = args.rotate_nkey.split('/')
        target = Instance.named(*parts) if len(parts) == 2 else None
        if target is None:
            p.error('--rotate-nkey requires one app/env target (demo-orders/staging)')
    return run(lambda: edit.sync(args.root.resolve(), rotate=target), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: edit.remove(args.root.resolve(), args.app, args.env), False)
