"""A notice: one message for a person, and the senders that carry it.

The host heartbeat builds a ``Notice`` and hands it to a sender: ``Ntfy`` over HTTP,
``Recorded`` in tests. Dashboard tooling shares the topic reader. The checker and notify
service are Go; Flux Alert and ClickStack's webhooks post to ntfy themselves, from
templates of their own (platform/alerts, clickstack_ntfy.py).
"""
from __future__ import annotations

import base64
import binascii
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from typing import TYPE_CHECKING
from urllib.parse import urlsplit

from swhurl.run import CommandError, Runner

from .errors import NotificationError

if TYPE_CHECKING:  # imported where a notice is sent: the dashboard command runs without httpx
    import httpx

PRIORITIES = {'normal': 3, 'high': 4}
"""The two priorities, by the name the notify service's API uses and the number ntfy and the outboxes use."""
PLATFORM_SECRET = ('console', 'notification-ntfy')
PLATFORM_KEY = 'NTFY_PLATFORM_URL'
"""The platform topic's address: this key of that Secret, which the checker's pod gets as a variable of the same name."""


@dataclass(frozen=True)
class Notice:
    title: str
    message: str = ''
    high: bool = False  # a failure or an alert; anything else is normal
    link: str = ''  # opened from the notification

    @property
    def priority(self) -> str:
        return 'high' if self.high else 'normal'

    def fields(self) -> dict:
        """As an outbox entry keeps it, which is also what ntfy is sent."""
        found = {'title': self.title, 'message': self.message, 'priority': PRIORITIES[self.priority],
                 'tags': ['warning' if self.high else 'white_check_mark']}
        return found | {'click': self.link} if self.link else found

    @classmethod
    def of(cls, entry: Mapping) -> Notice:
        """The notice in an outbox entry (``fields`` and whatever its outbox added)."""
        return cls(entry['title'], entry.get('message', ''), entry.get('priority', 0) >= PRIORITIES['high'],
                   entry.get('click', ''))

    def to_ntfy(self, topic: str) -> dict:
        """The body of ntfy's JSON publish API."""
        return self.fields() | {'topic': topic}

    def to_notify_request(self, app: str, env: str, key: str) -> dict:
        """The body of the notify service's ``POST /v1/notify`` (docs/services.md#notify-service)."""
        return {'app': app, 'env': env, 'key': key, 'title': self.title, 'message': self.message,
                'priority': self.priority, 'click': self.link}


Sender = Callable[[str, Notice], None]
"""Carries a notice to an address; raises NotificationError when it was not accepted."""


def ntfy_target(address: str) -> tuple[str, str]:
    """An ntfy address as its server and topic. The error never repeats the address: it is the credential.
    swhurl-v2 team refuses such an address before it is encrypted; this is the sender's own guard."""
    parts = urlsplit(address)
    topic = parts.path.strip('/')
    if (parts.scheme != 'https' or not parts.netloc or not topic or '/' in topic
            or parts.query or parts.fragment or parts.username or parts.password):
        raise NotificationError('an ntfy address is an HTTPS topic URL without a query or fragment, '
                                'such as https://ntfy.sh/<topic>')
    return f'{parts.scheme}://{parts.netloc}', parts.path.strip('/')


@dataclass(frozen=True)
class Ntfy:
    """Sends over HTTP."""
    transport: httpx.BaseTransport | None = None  # tests

    def __call__(self, address: str, notice: Notice) -> None:
        import httpx
        server, topic = ntfy_target(address)
        try:
            with httpx.Client(transport=self.transport, timeout=10) as client:
                client.post(server, json=notice.to_ntfy(topic)).raise_for_status()
        except httpx.HTTPError:
            # HTTP exceptions can include the topic URL or response body. Never surface either.
            raise NotificationError('ntfy delivery failed; notification remains pending') from None


@dataclass
class Recorded:
    """Keeps what it is given, for tests; ``refuse`` makes every send fail with that message."""
    refuse: str = ''
    sent: list[tuple[str, Notice]] = field(default_factory=list)

    def __call__(self, address: str, notice: Notice) -> None:
        if self.refuse:
            raise NotificationError(self.refuse)
        self.sent.append((address, notice))


def platform_topic(runner: Runner, environ: Mapping[str, str] | None = None) -> str:
    """The address of the platform topic, registered as a secret. It is read from the cluster, or from
    ``environ`` where the Secret is mounted as variables (the checker's pod, which may not read Secrets)."""
    if environ is not None:
        address = environ.get(PLATFORM_KEY, '')
        if not address:
            raise NotificationError(f'{PLATFORM_KEY} is missing; notification remains pending')
    else:
        namespace, name = PLATFORM_SECRET
        try:
            doc = runner.json(['kubectl', '-n', namespace, 'get', 'secret', name, '-o', 'json'], secret_output=True)
            address = base64.b64decode(doc['data'][PLATFORM_KEY], validate=True).decode()
        except (CommandError, KeyError, TypeError, ValueError, binascii.Error):
            raise NotificationError('notification destination is unavailable') from None
        if not address:
            raise NotificationError('notification destination is empty')
    runner.add_secret(address)
    runner.add_secret(ntfy_target(address)[1])
    return address
