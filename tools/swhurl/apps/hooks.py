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

import yaml

from swhurl import platform
from swhurl.apps.contract import IMAGE_AUTOMATION_FILE
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
        doc = yaml.safe_load(runner.output(['sops', 'decrypt', root / TOKEN_FILE], secret_output=True,
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
    """``app: OWNER/REPO`` for each app with automatic deploys, from the image its ImageRepository scans.

    The app's workflow publishes ``ghcr.io/OWNER/REPO`` from the repository of the same name, and
    GitHub sends a package's events to the repository it is linked to.
    """
    found = {}
    for path in sorted((root / 'apps').glob(f'*/*/{IMAGE_AUTOMATION_FILE}')):
        for doc in yaml.safe_load_all(path.read_text()):
            image = ((doc or {}).get('spec') or {}).get('image', '')
            if (doc or {}).get('kind') == 'ImageRepository' and image.startswith(REGISTRY):
                found[path.parts[-3]] = image.removeprefix(REGISTRY)
    return found


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    if argv:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return 2
    runner = runner or Runner.from_environment(cwd=ROOT)
    failed = 0
    try:
        token, host = read_token(runner), webhook_host()
    except HookError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    for app, repository in auto_deploy_repositories().items():
        try:
            state = ensure(runner, repository, token, host)
        except CommandError as error:
            print(f'[ERROR] {app}: {error}', file=sys.stderr)
            failed += 1
            continue
        outcome = 'is in place' if state == 'ok' else f'would be {state}' if runner.dry_run else state
        print(f'[OK] {app}: image webhook on {repository} {outcome}')
    return 1 if failed else 0
