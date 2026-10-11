"""Refresh the cluster's public token-signing keys in Git (apps/signing-keys.json).

    make platform-signing-keys

Apps check a caller's service-account token against these keys but cannot fetch them: this API
server refuses anonymous requests and apps carry no API token. So the keys are read here with the
operator's access, kept in Git, and given to every web app (SWHURL_AUTH_KEYS). They are public keys.

Run it after the cluster's service-account signing key changes (a rebuilt cluster, a rotation):
until then every call between apps is refused. It rewrites the file and every instance that
carries the keys; commit and push to apply. `make verify-platform` reports a difference.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import edit
from swhurl.apps.contract import SIGNING_KEYS_FILE
from swhurl.run import CommandError, Runner

JWKS = ['kubectl', 'get', '--raw', '/openid/v1/jwks']


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl platform-signing-keys', description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    root, runner = args.root.resolve(), runner or Runner()
    try:
        live = runner.json(JWKS)
    except CommandError as error:
        print(f'[ERROR] could not read the signing keys from the cluster: {error}')
        return 1
    if not isinstance(live, dict) or not live.get('keys'):
        print('[ERROR] the cluster returned no signing keys')
        return 1
    path = root / SIGNING_KEYS_FILE
    if path.is_file() and json.loads(path.read_text()) == live:
        print(f'[OK] {SIGNING_KEYS_FILE} already holds the cluster\'s {len(live["keys"])} signing key(s)')
        return 0
    path.write_text(json.dumps(live, indent=2, sort_keys=True) + '\n')
    print(f'[OK] wrote {SIGNING_KEYS_FILE} ({len(live["keys"])} key(s))')
    return edit.run(lambda: edit.sync(root), False)
