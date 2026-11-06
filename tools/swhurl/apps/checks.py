"""Live checks of the apps that Go does not run yet: the image webhooks each auto-deploy app's repository calls.

The other app checks (image automation, isolation, signing keys, events, egress) are ``swhurl-v2 verify --scope
platform``'s. Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

from pathlib import Path

from swhurl import ROOT
from swhurl.apps import hooks
from swhurl.kube import ready_condition
from swhurl.report import Report
from swhurl.run import CommandError, Runner

COMPILED = Path('v2-apps/instances')  # what v2 compiled from the records: make check-apps compares the two


def check_image_webhooks(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The app-images Receiver is Ready and each auto-deploy app's repository calls it (docs/services.md#image-webhook)."""
    report.section('Image Webhooks')
    try:
        receiver = runner.json(['kubectl', '-n', hooks.RECEIVER_NAMESPACE, 'get',
                                'receivers.notification.toolkit.fluxcd.io', hooks.RECEIVER, '-o', 'json'])
        status, message = ready_condition(receiver)
    except (CommandError, TypeError):
        status, message = 'Unknown', 'could not read it'
    name = f'{hooks.RECEIVER_NAMESPACE}/{hooks.RECEIVER}'
    if status == 'True':
        report.ok(f'Flux receiver {name} is Ready')
    else:
        report.bad(f'Flux receiver {name} is not Ready: {message} (make reconcile UNIT=platform-flux-webhook)')
    host = hooks.webhook_host(root)
    fallback = 'Flux finds its new images within the hour'
    try:
        repositories = hooks.auto_deploy_repositories(root)
    except hooks.HookError as error:
        report.bad(str(error))
        return
    for app, repository in repositories.items():
        try:
            hook = hooks.find(runner.json(['gh', 'api', f'repos/{repository}/hooks']), host)
        except CommandError as error:
            report.warn(f'{app}: could not list the GitHub webhooks of {repository} with gh: {error}')
            continue
        last = (hook or {}).get('last_response') or {}
        if not hook:
            report.warn(f'{app}: no image webhook on {repository}; {fallback} (make app-hooks)')
        elif not hook.get('active') or not set(hooks.EVENTS) <= set(hook.get('events') or []):
            report.warn(f'{app}: the image webhook on {repository} is disabled or lacks its events; {fallback} '
                        '(make app-hooks)')
        elif last.get('code') in (200, None):
            report.ok(f'{app}: image webhook on {repository}'
                      + (', latest delivery returned 200' if last.get('code') else ', no delivery yet'))
        else:
            report.warn(f'{app}: the latest image webhook delivery from {repository} returned {last.get("code")} '
                        f'{last.get("message") or ""}'.rstrip() + f'; {fallback} (after a token rotation: make app-hooks)')
