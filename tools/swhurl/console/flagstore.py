"""Feature flag overrides as the console reads and writes them (docs/apps.md#feature-flags).

An override is a value in NATS's key-value store: bucket ``flags-<env>``, key ``<env>.<app>.<flag>``,
the value as JSON. The app watches its own keys; the console signs in with its own nkey (the Secret
``console-nats``, ``NATS_SEED``) and may write any app's keys and nothing else (apps/events.py).

A key-value bucket is a JetStream stream ``KV_<bucket>`` whose subjects are ``$KV.<bucket>.<key>``:

    read     request $JS.API.STREAM.MSG.GET.KV_<bucket>  {"last_by_subj": "$KV.<bucket>.<key>"}
    write    publish $KV.<bucket>.<key> with a reply subject: JetStream answers with the stored sequence
    clear    the same with the header ``KV-Operation: DEL`` and no value (the app's watch sees a delete)

Standard library only, like nkeys.py: the console image has no NATS client or crypto library. One short
connection per page or change, so nothing here outlives a request.
"""
from __future__ import annotations

import base64
import json
import os
import secrets
import socket
from dataclasses import dataclass

from swhurl.apps import events, nkeys

SEED_VARIABLE = 'NATS_SEED'
URL_VARIABLE = 'NATS_URL'  # the in-cluster address unless set (a local console --dev port-forwards)
TIMEOUT = 5.0
NO_VALUE = object()  # a key that was never set or was cleared


class FlagStoreError(RuntimeError):
    """NATS could not be reached or refused the request."""


def bucket(env: str) -> str:
    return events.flags_bucket(env)


def key(env: str, app: str, flag: str) -> str:
    return f'{env}.{app}.{flag}'


def typed(flag: dict, text: str) -> object:
    """A form's text as the declared type of ``flag``; ValueError naming what is wrong."""
    from swhurl.apps import declaration
    kind = flag.get('type')
    text = text.strip()
    if kind == 'boolean':
        if text not in ('true', 'false'):
            raise ValueError('choose true or false')
        value: object = text == 'true'
    elif kind == 'integer':
        try:
            value = int(text)
        except ValueError:
            raise ValueError('give a whole number') from None
    elif kind == 'number':
        try:
            value = float(text) if any(c in text for c in '.eE') else int(text)
        except ValueError:
            raise ValueError('give a number') from None
    else:
        value = text
    problem = declaration.flag_problem('override', {**{k: v for k, v in flag.items() if k not in ('staging', 'prod')}, 'default': value})
    if problem:
        raise ValueError(problem.replace('flags.override.default', 'the value'))
    return value


class _Connection:
    """One NATS connection, signed in with an nkey: requests and publishes with a reply, nothing else."""

    def __init__(self, url: str, seed: str, *, timeout: float = TIMEOUT):
        host, _, port = url.removeprefix('nats://').partition(':')
        try:
            self.sock = socket.create_connection((host, int(port or 4222)), timeout=timeout)
        except OSError as error:
            raise FlagStoreError(f'cannot reach NATS at {url}: {error}') from None
        self.buffer = b''
        self.inbox = f'{events.CONSOLE_INBOX}.{secrets.token_hex(8)}'
        info = json.loads(self._line().removeprefix(b'INFO '))
        raw = nkeys.decode_seed(seed)
        signature = base64.urlsafe_b64encode(nkeys.sign(raw, info.get('nonce', '').encode())).decode().rstrip('=')
        connect = {'verbose': False, 'pedantic': False, 'headers': True, 'no_responders': True, 'protocol': 1,
                   'name': 'swhurl-console', 'nkey': nkeys.encode_public(nkeys.public_key(raw)), 'sig': signature}
        self._send(f'CONNECT {json.dumps(connect)}\r\nSUB {self.inbox}.* 1\r\nPING\r\n'.encode())
        while (line := self._line()) != b'PONG':
            if line.startswith(b'-ERR'):
                raise FlagStoreError(f'NATS refused the console: {line[5:].decode().strip(chr(39))}')

    def _send(self, data: bytes) -> None:
        self.sock.sendall(data)

    def _fill(self) -> None:
        try:
            chunk = self.sock.recv(65536)
        except TimeoutError:
            raise FlagStoreError('NATS did not answer in time') from None
        if not chunk:
            raise FlagStoreError('NATS closed the connection')
        self.buffer += chunk

    def _line(self) -> bytes:
        while b'\r\n' not in self.buffer:
            self._fill()
        line, _, self.buffer = self.buffer.partition(b'\r\n')
        return line

    def _exactly(self, size: int) -> bytes:
        while len(self.buffer) < size + 2:
            self._fill()
        data, self.buffer = self.buffer[:size], self.buffer[size + 2:]
        return data

    def request(self, subject: str, payload: bytes = b'', headers: dict[str, str] | None = None) -> tuple[dict[str, str], bytes]:
        """Publish with a reply subject and wait for the one answer: ``(its headers, its body)``."""
        reply = f'{self.inbox}.{secrets.token_hex(4)}'
        if headers:
            head = ('NATS/1.0\r\n' + ''.join(f'{k}: {v}\r\n' for k, v in headers.items()) + '\r\n').encode()
            self._send(f'HPUB {subject} {reply} {len(head)} {len(head) + len(payload)}\r\n'.encode() + head + payload + b'\r\n')
        else:
            self._send(f'PUB {subject} {reply} {len(payload)}\r\n'.encode() + payload + b'\r\n')
        while True:
            line = self._line()
            if line == b'PING':
                self._send(b'PONG\r\n')
            elif line.startswith(b'-ERR'):
                raise FlagStoreError(f'NATS refused it: {line[5:].decode().strip(chr(39))}')
            elif line.startswith((b'MSG ', b'HMSG ')):
                parts = line.decode().split()
                head_size, size = (int(parts[-2]), int(parts[-1])) if parts[0] == 'HMSG' else (0, int(parts[-1]))
                body = self._exactly(size)
                found = {}
                for header in body[:head_size].decode().split('\r\n')[1:]:
                    name, _, value = header.partition(':')
                    if name:
                        found[name.strip()] = value.strip()
                if parts[1] == reply:
                    status = body[:head_size].decode().split('\r\n')[0][len('NATS/1.0'):].strip()
                    if status.startswith('503'):
                        raise FlagStoreError(f'nothing answers {subject} (is the bucket there?)')
                    return found, body[head_size:]

    def close(self) -> None:
        self.sock.close()


@dataclass
class FlagStore:
    """The console's view of overrides; ``None`` from ``from_env`` when it has no NATS key."""
    url: str
    seed: str

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> FlagStore | None:
        env = os.environ if env is None else env
        seed = env.get(SEED_VARIABLE, '').strip()
        return cls(env.get(URL_VARIABLE, '') or events.URL, seed) if seed else None

    def _connect(self) -> _Connection:
        return _Connection(self.url, self.seed)

    def overrides(self, env: str, app: str, names: list[str]) -> dict[str, object]:
        """The override of each named flag that has one (decoded JSON)."""
        found: dict[str, object] = {}
        connection = self._connect()
        try:
            for name in names:
                _, body = connection.request(f'$JS.API.STREAM.MSG.GET.KV_{bucket(env)}',
                                             json.dumps({'last_by_subj': f'$KV.{bucket(env)}.{key(env, app, name)}'}).encode())
                answer = json.loads(body or b'{}')
                if 'error' in answer:
                    if answer['error'].get('code') == 404:
                        continue
                    raise FlagStoreError(f'reading {name}: {answer["error"].get("description", answer["error"])}')
                message = answer.get('message') or {}
                head = base64.b64decode(message.get('hdrs') or '').decode()
                if 'KV-Operation' in head:  # cleared (DEL) or purged: no override
                    continue
                try:
                    found[name] = json.loads(base64.b64decode(message.get('data') or ''))
                except ValueError:
                    found[name] = NO_VALUE  # not JSON: the app ignores it too
        finally:
            connection.close()
        return found

    def _write(self, env: str, app: str, name: str, payload: bytes, headers: dict[str, str] | None) -> None:
        connection = self._connect()
        try:
            _, body = connection.request(f'$KV.{bucket(env)}.{key(env, app, name)}', payload, headers)
        finally:
            connection.close()
        answer = json.loads(body or b'{}')
        if 'error' in answer:
            raise FlagStoreError(f'NATS did not store it: {answer["error"].get("description", answer["error"])}')

    def set(self, env: str, app: str, name: str, value: object) -> None:
        self._write(env, app, name, json.dumps(value).encode(), None)

    def clear(self, env: str, app: str, name: str) -> None:
        self._write(env, app, name, b'', {'KV-Operation': 'DEL'})
