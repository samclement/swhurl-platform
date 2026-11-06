"""Live checks of the console that Go does not run yet: its GitHub tokens.

Its image against this checkout and its job record are ``swhurl-v2 verify --scope platform``'s. Each takes a Runner
and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import base64
import binascii
import datetime as dt

from swhurl import platform
from swhurl.report import Report
from swhurl.run import CommandError, Runner

TOKEN_WARN_DAYS = 14


def check_console_token(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """GitHub accepts the console's tokens, and none is about to expire (read from GitHub's reply).

    GITHUB_TOKEN (pull requests here) is required; APP_REPOS_TOKEN (new app repositories) is optional:
    without it only the console's New app page is off."""
    report.section('Console GitHub Token')
    name = platform.CONSOLE_TOKEN_SECRET
    try:
        secret = runner.json(['kubectl', '-n', 'console', 'get', 'secret', name, '-o', 'json'], secret_output=True)
    except CommandError:
        secret = None
    data = (secret or {}).get('data') or {}
    for key, label, required in ((platform.CONSOLE_TOKEN_KEY, 'console token', True),
                                 (platform.APP_REPOS_TOKEN_KEY, 'console repository token', False)):
        try:
            token = base64.b64decode(data.get(key, '')).decode().strip()
        except (binascii.Error, UnicodeDecodeError):
            token = ''
        runner.add_secret(token)
        if token in ('', 'REPLACE_ME'):
            message = f'console/{name}.{key} is not set; set it with: sops platform/console/secret.sops.yaml'
            if required:
                report.bad(message)
            else:
                report.warn(message + " (the console's New app page is off until then)")
            continue
        check_github_token(runner, report, token, label, now)


def check_github_token(runner: Runner, report: Report, token: str, label: str, now: dt.datetime | None) -> None:
    reply = runner.run(['curl', '--silent', '--show-error', '--config', '-', '--output', '/dev/null', '--dump-header', '-',
                        f'https://api.github.com/repos/{platform.GITHUB_REPO}'],
                       input=f'header = "Authorization: Bearer {token}"\n', check=False, secret_output=True)
    lines = reply.stdout.splitlines()
    status = lines[0].split()[1] if lines and len(lines[0].split()) > 1 else 'no reply'
    headers = dict(line.split(':', 1) for line in lines[1:] if ':' in line)
    expires = {k.strip().lower(): v.strip() for k, v in headers.items()}.get('github-authentication-token-expiration')
    if status != '200':
        report.bad(f'GitHub did not accept the {label} (HTTP {status}), so the console cannot open pull requests: '
                   'create a new one and set it as docs/operations.md#secrets says')
        return
    if not expires:
        report.ok(f'GitHub accepts the {label} (no expiry date)')
        return
    try:
        when = dt.datetime.strptime(expires[:19], '%Y-%m-%d %H:%M:%S').replace(tzinfo=dt.UTC)
    except ValueError:
        report.warn(f'GitHub accepts the {label}; could not read its expiry {expires!r}')
        return
    days = ((when - (now or dt.datetime.now(dt.UTC))).total_seconds()) / 86400
    message = f'GitHub accepts the {label}; it expires {when:%Y-%m-%d} ({days:.0f} days)'
    if days < TOKEN_WARN_DAYS:
        report.warn(message + '; create a new one and set it as docs/operations.md#secrets says')
    else:
        report.ok(message)
