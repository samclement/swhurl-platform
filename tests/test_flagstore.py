"""The console's flag store against a stand-in NATS server: sign-in, read, write and clear on the wire."""
import base64
import json
import socket
import socketserver
import threading
import unittest

from swhurl.apps import nkeys
from swhurl.console import flagstore

NONCE = 'a-nonce-from-the-server'


class FakeNats(socketserver.BaseRequestHandler):
    """Just enough of NATS: INFO with a nonce, CONNECT checked, and a key-value stream in memory."""

    def handle(self):
        server = self.server
        self.request.sendall(f'INFO {json.dumps({"nonce": NONCE, "headers": True})}\r\n'.encode())
        reader = self.request.makefile('rb')
        subscription = None
        while line := reader.readline():
            words = line.decode().strip().split(' ', 1)
            verb, rest = words[0], words[1] if len(words) > 1 else ''
            if verb == 'CONNECT':
                server.connects.append(json.loads(rest))
                if json.loads(rest).get('nkey') != server.allowed:
                    self.request.sendall(b"-ERR 'Authorization Violation'\r\n")
                    return
            elif verb == 'SUB':
                subscription = rest.split()[0].removesuffix('.*')
            elif verb == 'PING':
                self.request.sendall(b'PONG\r\n')
            elif verb in ('PUB', 'HPUB'):
                parts = rest.split()
                subject, reply = parts[0], parts[1]
                head_size, size = (int(parts[2]), int(parts[3])) if verb == 'HPUB' else (0, int(parts[2]))
                body = reader.read(size + 2)[:size]
                head, payload = body[:head_size].decode(), body[head_size:]
                assert reply.startswith(subscription + '.'), (reply, subscription)
                self.request.sendall(self.answer(subject, head, payload, reply))

    def answer(self, subject, head, payload, reply):
        store = self.server.store
        if subject.startswith('$JS.API.STREAM.MSG.GET.'):
            wanted = json.loads(payload)['last_by_subj']
            if wanted not in store:
                answer = {'error': {'code': 404, 'description': 'no message found'}}
            else:
                stored_head, data = store[wanted]
                answer = {'message': {'subject': wanted, 'data': base64.b64encode(data).decode(),
                                      **({'hdrs': base64.b64encode(stored_head.encode()).decode()} if stored_head else {})}}
        elif subject.startswith('$KV.flags-staging.'):
            store[subject] = (head, payload)
            self.server.writes.append((subject, head, payload))
            answer = {'stream': 'KV_flags-staging', 'seq': len(self.server.writes)}
        else:  # no stream listens: NATS answers "no responders"
            status = b'NATS/1.0 503\r\n\r\n'
            return f'HMSG {reply} 1 {len(status)} {len(status)}\r\n'.encode() + status + b'\r\n'
        data = json.dumps(answer).encode()
        return f'MSG {reply} 1 {len(data)}\r\n'.encode() + data + b'\r\n'


class FlagStoreTests(unittest.TestCase):
    def setUp(self):
        self.seed, public = nkeys.generate()
        self.server = socketserver.ThreadingTCPServer(('127.0.0.1', 0), FakeNats)
        self.server.daemon_threads = True
        self.server.allowed, self.server.store, self.server.writes, self.server.connects = public, {}, [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.store = flagstore.FlagStore(f'nats://127.0.0.1:{self.server.server_address[1]}', self.seed)

    def test_the_console_signs_in_with_its_key_and_signs_the_servers_nonce(self):
        self.store.overrides('staging', 'orders', [])
        connect, = self.server.connects
        raw = nkeys.decode_seed(self.seed)
        self.assertEqual(connect['nkey'], self.server.allowed)
        self.assertEqual(base64.urlsafe_b64decode(connect['sig'] + '=='), nkeys.sign(raw, NONCE.encode()))
        self.assertTrue(connect['headers'], 'a clear is a header')

    def test_set_read_and_clear_on_the_wire(self):
        self.assertEqual(self.store.overrides('staging', 'orders', ['shout', 'banner']), {})
        self.store.set('staging', 'orders', 'shout', False)
        self.store.set('staging', 'orders', 'banner', 'sale')
        self.assertEqual(self.store.overrides('staging', 'orders', ['shout', 'banner']), {'shout': False, 'banner': 'sale'})
        self.store.clear('staging', 'orders', 'shout')
        self.assertEqual(self.store.overrides('staging', 'orders', ['shout', 'banner']), {'banner': 'sale'})
        self.assertEqual(self.server.writes, [
            ('$KV.flags-staging.staging.orders.shout', '', b'false'),
            ('$KV.flags-staging.staging.orders.banner', '', b'"sale"'),
            ('$KV.flags-staging.staging.orders.shout', 'NATS/1.0\r\nKV-Operation: DEL\r\n\r\n', b'')])

    def test_a_refusal_or_a_missing_bucket_is_an_error_not_a_hang(self):
        with self.assertRaisesRegex(flagstore.FlagStoreError, 'NATS refused the console: Authorization Violation'):
            flagstore.FlagStore(self.store.url, nkeys.generate()[0]).overrides('staging', 'orders', ['shout'])
        with self.assertRaisesRegex(flagstore.FlagStoreError, r'nothing answers \$KV.flags-prod'):
            self.store.set('prod', 'orders', 'shout', True)
        with socket.socket() as unused:
            unused.bind(('127.0.0.1', 0))
            port = unused.getsockname()[1]
        with self.assertRaisesRegex(flagstore.FlagStoreError, 'cannot reach NATS'):
            flagstore.FlagStore(f'nats://127.0.0.1:{port}', self.seed).overrides('staging', 'orders', ['shout'])

    def test_a_form_value_becomes_the_declared_type(self):
        integer = {'type': 'integer', 'min': 1, 'max': 100, 'default': 20}
        self.assertEqual(flagstore.typed({'type': 'boolean', 'default': False}, 'true'), True)
        self.assertEqual(flagstore.typed(integer, ' 42 '), 42)
        self.assertEqual(flagstore.typed({'type': 'number', 'default': 0}, '0.25'), 0.25)
        self.assertEqual(flagstore.typed({'type': 'string', 'enum': ['none', 'sale'], 'default': 'none'}, 'sale'), 'sale')
        for flag, text, message in ((integer, '500', 'outside min and max'), (integer, 'x', 'whole number'),
                                    ({'type': 'boolean', 'default': True}, 'yes', 'true or false'),
                                    ({'type': 'string', 'enum': ['a'], 'default': 'a'}, 'b', 'must be one of a')):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, message):
                flagstore.typed(flag, text)

    def test_from_env_needs_the_seed(self):
        self.assertIsNone(flagstore.FlagStore.from_env({}))
        self.assertEqual(flagstore.FlagStore.from_env({'NATS_SEED': self.seed}).url, 'nats://nats.messaging.svc.cluster.local:4222')


if __name__ == '__main__':
    unittest.main()
