"""Minute-by-minute Helm lifecycle and Kubernetes health notifications.

ConfigMap state is a delivery outbox and incident history, not desired Git state.
Delivery is at least once: a crash after ntfy accepts a message but before the
checkpoint may repeat it. No Kubernetes mutation except this one ConfigMap.
"""
from __future__ import annotations

import argparse
import copy
import datetime as dt
import logging
import logging.config
import os
from collections.abc import Callable

import httpx as httpx

from swhurl.console.logconfig import CONFIG
from swhurl.run import CommandError, Runner

from .delivery import SERVICE_URL, deliver, forward, publish
from .errors import NotificationError
from .evaluate import CONSOLE_GRACE as CONSOLE_GRACE
from .evaluate import FAILURES as FAILURES
from .evaluate import GRACE as GRACE
from .evaluate import HISTORY as HISTORY
from .evaluate import RECOVERY as RECOVERY
from .evaluate import REMINDER as REMINDER
from .evaluate import SUCCESS as SUCCESS
from .evaluate import evaluate, incident, snapshot
from .evaluate import timestamp as timestamp
from .state import MAX_STATE_BYTES as MAX_STATE_BYTES
from .state import STATE_NAME as STATE_NAME
from .state import STATE_NAMESPACE as STATE_NAMESPACE
from .state import empty_state, read_state
from .state import save_state as save_state


def check(runner: Runner, *, now: float, base: str, sender: Callable[[dict], None] | None = None,
          copies: bool = False) -> dict:
    previous = read_state(runner)
    forwarded = {'accepted': 0, 'refused': 0}

    def counted(notice: dict) -> None:
        """``sender``, counting what the notify service took of the copies meant for it."""
        if 'about' not in notice:
            return sender(notice)
        if forwarded['refused']:  # one failure is enough for this pass: do not wait once per copy
            raise NotificationError('notify service did not accept the previous copy')
        try:
            sender(notice)
        except NotificationError:
            forwarded['refused'] += 1
            raise
        forwarded['accepted'] += 1
    try:
        data = snapshot(runner)
        state = evaluate(previous, data, now, base, copies)
    except (CommandError, KeyError, TypeError, ValueError) as error:
        # Successful state read is required to preserve incident clocks and the pending outbox.
        state = copy.deepcopy(previous) if previous is not None else empty_state(now)
        incident(state, state['monitor'], 'cannot read cluster health', title='notification monitor',
                 detail='Kubernetes snapshot failed; app health is unknown', grace=GRACE,
                 now=now, click=f'{base}/platform')
        for entry in state['instances'].values():
            entry['incident'].pop('healthy_since', None)
        if not runner.dry_run:
            deliver(runner, state, counted, now)
        raise NotificationError(f'Kubernetes snapshot failed ({type(error).__name__}); no recovery inferred') from None
    if not runner.dry_run:
        if sender is None:
            raise NotificationError('notification sender is required outside dry run')
        deliver(runner, state, counted, now)
        if forwarded['refused'] or forwarded['accepted']:
            # The operator hears on the platform topic when team copies are not getting through, and
            # as soon as one is accepted again. A pass that tried no copy says nothing either way.
            record = state.setdefault('notify_service', {})
            if not forwarded['refused']:
                record['healthy_since'] = now - RECOVERY
            incident(state, record, 'unreachable' if forwarded['refused'] else '', title='notify service',
                     detail='Copies of app messages for team channels are not being accepted',
                     grace=GRACE, now=now, click=f'{base}/platform')
            save_state(runner, state)
    return state


DESTINATION = 'NTFY_PLATFORM_URL'  # the one platform topic; priority travels in each message


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true', help='Read and evaluate without posting or saving state')
    args = parser.parse_args(argv)
    runner = runner or Runner.from_environment()
    runner.dry_run |= args.dry_run
    logging.config.dictConfig(CONFIG)
    now = dt.datetime.now(dt.UTC).timestamp()
    base = os.environ.get('CONSOLE_URL', '').rstrip('/')
    if not base:
        from swhurl.platform import base_domain
        base = f'https://console.{base_domain()}'

    def sender(notice: dict) -> None:
        if 'about' in notice:
            return forward(notice)
        url = os.environ.get(DESTINATION, '')
        runner.add_secret(url)
        if not url:
            raise NotificationError(f'{DESTINATION} is missing; notification remains pending')
        publish(url, notice)

    try:
        if not runner.dry_run and not os.environ.get(DESTINATION):
            raise NotificationError(f'{DESTINATION} is missing; checker cannot deliver notifications')
        state = check(runner, now=now, base=base, sender=sender, copies=bool(os.environ.get(SERVICE_URL)))
        logging.getLogger(__name__).info('Notification check completed: %d instances, %d pending, dry_run=%s',
                                        len(state['instances']), len(state['pending']), runner.dry_run)
        if runner.dry_run:
            for notice in state['pending']:
                logging.getLogger(__name__).info('Would notify: %s (priority %s)', notice['title'], notice['priority'])
        return 0
    except (NotificationError, CommandError) as error:
        logging.getLogger(__name__).error('%s', runner.redact(str(error)))
        return 1
