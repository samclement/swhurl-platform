"""Durable outbox delivery: every pending entry is offered to a sender, in order."""
from __future__ import annotations

import logging
from collections.abc import Callable

from swhurl.run import Runner

from .errors import NotificationError
from .evaluate import HISTORY
from .state import save_state

CHANNEL_RETRY = 7200  # seconds a copy that cannot be sent stays in the outbox


def copy_of(notice: dict) -> str:
    """What a copy is for: a team's channel (the notify service's outbox) or the notify service (the
    checker's); empty for a message to the platform topic, which is never skipped."""
    if 'channel' in notice:
        return f"channel {notice['channel']}"
    return 'notify service' if 'about' in notice else ''


def deliver(runner: Runner, state: dict, sender: Callable[[dict], None], now: float, *,
            save: Callable[[Runner, dict], None] = save_state, history: float = HISTORY) -> None:
    """Post every pending notice in order. ``save`` checkpoints the caller's own state store."""
    log = logging.getLogger(__name__)
    save(runner, state)  # durable outbox before any network side effect
    waiting = []  # copies that could not be sent: they never hold up the platform topic or each other
    try:
        while state['pending']:
            notice = state['pending'][0]
            try:
                sender(notice)
            except NotificationError as error:
                if not copy_of(notice):
                    raise
                state['pending'].pop(0)
                retry = now - notice['queued'] < CHANNEL_RETRY
                if retry:
                    waiting.append(notice)
                else:  # given up: remembered like a delivered one, so it is not queued again
                    state['seen'][notice['key']] = now + notice.get('history', history)
                    save(runner, state)
                log.error('%s: %s (%s)', copy_of(notice), error, 'will retry' if retry else 'gave up')
                continue
            state['seen'][notice['key']] = now + notice.get('history', history)
            state['pending'].pop(0)
            save(runner, state)
            log.info('Notification delivered: %s%s', notice['title'], f' ({copy_of(notice)})' if copy_of(notice) else '')
    finally:
        if waiting:
            state['pending'].extend(waiting)
            save(runner, state)


