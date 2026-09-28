"""Runtime-input helpers.

    wait-secret-key NAMESPACE SECRET KEY [--timeout SECONDS] [--interval SECONDS]

--timeout defaults to TIMEOUT_SECS from the environment, else 300.

Waits until a Secret key has a non-empty value (for example after Flux applied a
changed SOPS Secret). Reads the Secret without ever printing its value.
"""
from __future__ import annotations

import argparse
import os
import sys
import time
from collections.abc import Callable

from swhurl.run import CommandError, Runner


def secret_key_present(runner: Runner, namespace: str, name: str, key: str) -> bool:
    try:
        secret = runner.json(['kubectl', '-n', namespace, 'get', 'secret', name, '-o', 'json'], secret_output=True)
    except CommandError:
        return False
    value = ((secret or {}).get('data') or {}).get(key, '')
    runner.add_secret(value)
    return bool(value)


def wait_for_secret_key(runner: Runner, namespace: str, name: str, key: str, *, timeout: float = 300,
                        interval: float = 5, clock: Callable[[], float] = time.monotonic,
                        sleep: Callable[[float], None] = time.sleep) -> bool:
    """Poll until the key is present or ``timeout`` seconds pass. Always checks at least once."""
    deadline = clock() + timeout
    while True:
        if secret_key_present(runner, namespace, name, key):
            return True
        if clock() >= deadline:
            return False
        sleep(interval)


def wait_secret_key(argv: list[str] | None = None, runner: Runner | None = None,
                    clock: Callable[[], float] = time.monotonic, sleep: Callable[[float], None] = time.sleep) -> int:
    parser = argparse.ArgumentParser(prog='swhurl wait-secret-key', description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('namespace')
    parser.add_argument('secret')
    parser.add_argument('key')
    parser.add_argument('--timeout', type=float, default=float(os.environ.get('TIMEOUT_SECS') or 300))
    parser.add_argument('--interval', type=float, default=5)
    args = parser.parse_args(argv)
    runner = runner or Runner()
    print(f'[INFO] Waiting for {args.namespace}/{args.secret}.{args.key} to be present')
    if wait_for_secret_key(runner, args.namespace, args.secret, args.key, timeout=args.timeout,
                           interval=args.interval, clock=clock, sleep=sleep):
        print(f'[INFO] {args.namespace}/{args.secret} is present')
        return 0
    print(f'[ERROR] Timed out waiting for {args.secret} propagation ({args.timeout:g}s)', file=sys.stderr)
    return 1
