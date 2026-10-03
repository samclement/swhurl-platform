"""Validated ntfy publication and durable outbox delivery."""
from __future__ import annotations

import logging
from collections.abc import Callable
from urllib.parse import urlsplit

import httpx

from swhurl.run import Runner

from .errors import NotificationError
from .evaluate import HISTORY
from .state import save_state


def publish(url: str, notification: dict) -> None:
    parts = urlsplit(url)
    topic = parts.path.strip('/')
    if (parts.scheme != 'https' or not parts.netloc or not topic or '/' in topic
            or parts.query or parts.fragment or parts.username or parts.password):
        raise NotificationError('ntfy destination must be an HTTPS topic URL without a query or fragment')
    payload = {k: v for k, v in notification.items() if k in {'title', 'message', 'priority', 'tags', 'click'}}
    payload['topic'] = topic
    try:
        response = httpx.post(f'{parts.scheme}://{parts.netloc}', json=payload, timeout=10)
        response.raise_for_status()
    except httpx.HTTPError:
        # HTTP exceptions can include the topic URL or response body. Never surface either.
        raise NotificationError('ntfy delivery failed; notification remains pending') from None


def deliver(runner: Runner, state: dict, sender: Callable[[dict], None], now: float) -> None:
    save_state(runner, state)  # durable outbox before any network side effect
    while state['pending']:
        notice = state['pending'][0]
        sender(notice)
        state['seen'][notice['key']] = now + HISTORY
        state['pending'].pop(0)
        save_state(runner, state)
        logging.getLogger(__name__).info('Notification delivered: %s', notice['title'])


