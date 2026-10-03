"""Alert from the host when the in-cluster notification checker goes stale."""
from __future__ import annotations

import argparse
import base64
import binascii
import datetime as dt
import json
import logging
import logging.config
import math
import os
import re
import tempfile
from pathlib import Path

from swhurl.console.logconfig import CONFIG
from swhurl.run import CommandError, Runner

from .delivery import publish
from .errors import NotificationError

CRONJOB_NAME = 'console-notifications'
CRONJOB_NAMESPACE = 'console'
SECRET_NAME = 'notification-ntfy'
SECRET_KEY = 'NTFY_FAILURES_URL'
REMINDER_SECONDS = 3600
STATE_PATH = Path.home() / '.local/state/swhurl-platform/notification-heartbeat.json'


def parse_duration(value: str) -> float:
    match = re.fullmatch(r'(\d+(?:\.\d+)?)(s|m|h)', value)
    if not match:
        raise argparse.ArgumentTypeError('duration must be a positive number followed by s, m or h')
    amount = float(match.group(1))
    if amount <= 0 or not math.isfinite(amount):
        raise argparse.ArgumentTypeError('duration must be greater than zero')
    return amount * {'s': 1, 'm': 60, 'h': 3600}[match.group(2)]


def heartbeat_action(cronjob: dict | None, state: dict, now: float,
                     max_age: float) -> tuple[str, str, dict]:
    """Return the needed transition, reason and candidate state without I/O."""
    reason = ''
    if cronjob is None:
        reason = 'CronJob is missing'
    elif (cronjob.get('spec') or {}).get('suspend'):
        reason = 'CronJob is suspended'
    else:
        last = (cronjob.get('status') or {}).get('lastSuccessfulTime')
        if last:
            try:
                taken = dt.datetime.fromisoformat(last.replace('Z', '+00:00')).timestamp()
            except (AttributeError, TypeError, ValueError):
                reason = 'last successful time is invalid'
            else:
                if now - taken <= max_age:
                    if state['alerting_since'] is not None:
                        return 'recover', '', {'alerting_since': None, 'last_alert': None}
                    return 'none', '', state.copy()
                reason = f'last success was {int((now - taken) // 60)} minutes ago'
        else:
            reason = 'no successful Job has completed'

    if state['alerting_since'] is None:
        return 'alert', reason, {'alerting_since': now, 'last_alert': now}
    if now - state['last_alert'] >= REMINDER_SECONDS:
        return 'remind', reason, {'alerting_since': state['alerting_since'], 'last_alert': now}
    return 'none', reason, state.copy()


def read_state(path: Path) -> dict:
    try:
        doc = json.loads(path.read_text())
    except FileNotFoundError:
        return {'alerting_since': None, 'last_alert': None}
    except (OSError, json.JSONDecodeError):
        raise NotificationError('heartbeat state cannot be read; leaving it unchanged') from None
    if (not isinstance(doc, dict) or set(doc) != {'alerting_since', 'last_alert'}
            or (doc['alerting_since'] is None) != (doc['last_alert'] is None)):
        raise NotificationError('heartbeat state is invalid; leaving it unchanged')
    if doc['alerting_since'] is not None:
        for key in ('alerting_since', 'last_alert'):
            value = doc[key]
            if (isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value)):
                raise NotificationError('heartbeat state is invalid; leaving it unchanged')
    return doc


def save_state(path: Path, state: dict) -> None:
    """Atomically store the tiny incident flag with private permissions."""
    temporary = None
    try:
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(path.parent, 0o700)
        with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=path.parent,
                                         prefix='.notification-heartbeat-', delete=False) as stream:
            temporary = Path(stream.name)
            os.chmod(temporary, 0o600)
            json.dump(state, stream, separators=(',', ':'))
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        temporary = None
    except OSError:
        raise NotificationError('heartbeat state cannot be saved') from None
    finally:
        if temporary is not None:
            try:
                temporary.unlink()
            except OSError:
                pass


def destination(runner: Runner) -> str:
    doc = runner.json(['kubectl', '-n', CRONJOB_NAMESPACE, 'get', 'secret', SECRET_NAME, '-o', 'json'])
    try:
        raw = base64.b64decode(doc['data'][SECRET_KEY], validate=True)
        value = raw.decode()
    except (KeyError, TypeError, ValueError, UnicodeDecodeError, binascii.Error):
        raise NotificationError('notification destination is unavailable') from None
    if not value:
        raise NotificationError('notification destination is empty')
    runner.add_secret(value)
    return value


def get_cronjob(runner: Runner) -> dict | None:
    command = ['kubectl', '-n', CRONJOB_NAMESPACE, 'get', 'cronjob', CRONJOB_NAME, '-o', 'json']
    result = runner.run(command, check=False, secret_output=True)
    if result.returncode:
        error = (result.stderr or result.stdout).lower()
        if 'notfound' in error or 'not found' in error:
            return None
        raise CommandError(command, 'cannot read notification checker', result.returncode)
    try:
        doc = json.loads(result.stdout)
        if not isinstance(doc, dict):
            raise ValueError
        return doc
    except (json.JSONDecodeError, ValueError):
        raise NotificationError('notification checker status is invalid') from None


def main(argv: list[str] | None = None, runner: Runner | None = None, *,
         now: float | None = None, state_path: Path | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true', help='Read and decide without posting or saving state')
    parser.add_argument('--max-age', type=parse_duration, default=parse_duration('10m'),
                        help='maximum checker age before alerting (default 10m; use s, m or h)')
    args = parser.parse_args(argv)
    runner = runner or Runner.from_environment()
    runner.dry_run |= args.dry_run
    logging.config.dictConfig(CONFIG)
    log = logging.getLogger(__name__)
    path = state_path or STATE_PATH
    now = now if now is not None else dt.datetime.now(dt.UTC).timestamp()
    try:
        cronjob = get_cronjob(runner)
        state = read_state(path)
        action, reason, updated = heartbeat_action(cronjob, state, now, args.max_age)
        if action == 'none':
            log.info('Notification heartbeat: %s', 'stale; reminder not due' if reason else 'fresh')
            return 0
        title = 'notification checker recovered' if action == 'recover' else 'notification checker stale'
        message = ('The console-notifications CronJob is healthy again.' if action == 'recover' else
                   f'The console-notifications CronJob is stale: {reason}. Check with make verify-platform.')
        priority = 3 if action == 'recover' else 4
        if runner.dry_run:
            log.info('Would notify: %s (priority %d)', title, priority)
            return 0
        url = destination(runner)
        publish(url, {'title': title, 'message': message, 'priority': priority,
                      'tags': ['white_check_mark'] if action == 'recover' else ['warning']})
        save_state(path, updated)
        log.info('Notification heartbeat sent: %s', title)
        return 0
    except (CommandError, NotificationError, OSError, ValueError) as error:
        log.error('%s', runner.redact(str(error)) if isinstance(error, NotificationError)
                  else 'cannot read notification checker')
        return 1
