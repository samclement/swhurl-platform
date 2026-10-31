"""The image webhook on each app repository: a published image reaches Flux at once.

    make app-hooks            # every app with automatic deploys; DRY_RUN=true shows the plan

GitHub calls Flux's ``app-images`` Receiver (platform/flux-webhook/image-receiver.yaml) when the
app's workflow publishes to GHCR, so the app's ``ImageRepository`` is scanned then rather than at
its hourly interval. This creates the webhook where it is missing and repoints it after the token
was rotated (the Receiver's path is a hash of the token); a correct webhook is left alone. It uses
your ``gh`` login and reads the token from SOPS; the token goes to ``gh`` on stdin only.

``make app-repo`` and the console's **Start a new app** create the same webhook with the repository.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
from pathlib import Path

from swhurl import platform, yamlio
from swhurl.apps.model import store, workload
from swhurl.run import CommandError, Runner

ROOT = platform.ROOT
RECEIVER = 'app-images'
RECEIVER_NAMESPACE = 'flux-system'
TOKEN_FILE = Path('platform/flux-webhook/image-secret.sops.yaml')
# The console's copy (Secret console/console-github), so it can create the webhook on a new repository.
CONSOLE_TOKEN_KEY = 'IMAGE_WEBHOOK_TOKEN'
EVENTS = ('package', 'registry_package')
REGISTRY = 'ghcr.io/'


class HookError(Exception):
    pass


def webhook_host(root: Path = ROOT) -> str:
    return f'flux-webhook.{platform.base_domain(root)}'


def hook_url(token: str, host: str) -> str:
    """Where the Receiver listens: Flux derives the path from the token, its name and namespace."""
    digest = hashlib.sha256(f'{token}{RECEIVER}{RECEIVER_NAMESPACE}'.encode()).hexdigest()
    return f'https://{host}/hook/{digest}'


def hook_body(token: str, host: str) -> dict:
    """The repository webhook GitHub's API creates or updates."""
    return {'name': 'web', 'active': True, 'events': list(EVENTS),
            'config': {'url': hook_url(token, host), 'content_type': 'json', 'secret': token, 'insecure_ssl': '0'}}


def find(hooks: list[dict] | None, host: str) -> dict | None:
    """The repository's webhook to this platform, if any."""
    return next((h for h in hooks or [] if f'//{host}/' in (h.get('config') or {}).get('url', '')), None)


def correct(hook: dict, token: str, host: str) -> bool:
    return (bool(hook.get('active')) and hook['config'].get('url') == hook_url(token, host)
            and sorted(hook.get('events') or []) == sorted(EVENTS))


def read_token(runner: Runner, root: Path = ROOT) -> str:
    key_file = os.environ.get('SOPS_AGE_KEY_FILE') or str(root / platform.AGE_KEY)
    try:
        doc = yamlio.load(runner.output(['sops', 'decrypt', root / TOKEN_FILE], secret_output=True,
                                           env={'SOPS_AGE_KEY_FILE': key_file}))
        token = str(doc['stringData']['token'])
    except (CommandError, KeyError, TypeError):
        raise HookError(f'cannot read the image webhook token from {TOKEN_FILE} (age key at {key_file}?)') from None
    runner.add_secret(token)
    return token


def ensure(runner: Runner, repository: str, token: str, host: str) -> str:
    """Create or repoint ``repository``'s image webhook; return ``ok``, ``created`` or ``updated``."""
    runner.add_secret(token)
    api = f'repos/{repository}/hooks'
    hook = find(runner.json(['gh', 'api', api]), host)
    if hook and correct(hook, token, host):
        return 'ok'
    body = hook_body(token, host)
    if hook:
        body.pop('name')
        runner.run(['gh', 'api', '--method', 'PATCH', f'{api}/{hook["id"]}', '--input', '-'],
                   input=json.dumps(body), mutating=True)
        return 'updated'
    runner.run(['gh', 'api', '--method', 'POST', api, '--input', '-'], input=json.dumps(body), mutating=True)
    return 'created'


def auto_deploy_repositories(root: Path = ROOT) -> dict[str, str]:
    """``app: OWNER/REPO`` for each platform app's staging Release (automatic deploys).

    v1 states a staging instance in ``apps/``; a v2-owned one states the same repository in its own release
    record, which is the truth for it, so both trees are read here.
    """
    world, errors = store.world(root)
    if errors:
        raise HookError('; '.join(errors))
    found = {each.app: entry.release.image.repository.removeprefix(REGISTRY) for each, entry in world.items()
             if each.env == 'staging' and entry.release.image.repository == workload.platform_image(each.app)}
    found.update(v2_staging_repositories(root))
    return found


def v2_staging_repositories(root: Path) -> dict[str, str]:
    """The repository each v2-owned staging instance's image is built from, read from its release record."""
    found = {}
    for record in sorted((root / 'v2-records').glob('*/staging/release.json')):
        try:
            release = json.loads(record.read_text())
            image, repository = release['artifact']['image'], release['source']['repository']
        except (OSError, ValueError, KeyError, TypeError):
            continue  # a record that cannot state them adds no webhook; verify-platform reports it
        app = record.parent.parent.name
        if image.removeprefix(REGISTRY) == workload.platform_image(app).removeprefix(REGISTRY):
            found[app] = repository
    return found


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    if argv:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return 2
    runner = runner or Runner.from_environment(cwd=ROOT)
    failed = 0
    try:
        repositories = auto_deploy_repositories()
        token, host = read_token(runner), webhook_host()
    except HookError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    for app, repository in repositories.items():
        try:
            state = ensure(runner, repository, token, host)
        except CommandError as error:
            print(f'[ERROR] {app}: {error}', file=sys.stderr)
            failed += 1
            continue
        if state == 'ok':
            outcome = 'is in place'
        elif runner.dry_run:
            outcome = f'would be {state}'
        else:
            outcome = state
        print(f'[OK] {app}: image webhook on {repository} {outcome}')
    return 1 if failed else 0
