"""Edit Git-tracked cluster settings (``platform-settings``). Local files only.

    platform-certs ISSUER     set CERT_ISSUER (letsencrypt-staging | letsencrypt-prod)

The file is edited line by line so comments and layout survive, then parsed
again to prove that only the intended value changed before it is written.
``DRY_RUN=true`` reports the change without writing it.
"""
from __future__ import annotations

import os
import re
import sys
from pathlib import Path

import yaml

from swhurl import ROOT

SETTINGS = Path('clusters/home/flux-system/sources/configmap-platform-settings.yaml')
ISSUERS = Path('infrastructure/cert-manager/issuers')


class SettingsError(Exception):
    pass


def platform_issuers(root: Path = ROOT) -> set[str]:
    """ClusterIssuers defined in Git that platform hosts may use (publicly trusted ones)."""
    names = set()
    for path in (root / ISSUERS).rglob('*.yaml'):
        for doc in yaml.safe_load_all(path.read_text()):
            if doc and doc.get('kind') == 'ClusterIssuer' and doc['metadata']['name'].startswith('letsencrypt-'):
                names.add(doc['metadata']['name'])
    return names


def load_settings(text: str) -> dict:
    doc = yaml.safe_load(text)
    if not isinstance(doc, dict) or doc.get('kind') != 'ConfigMap' or not isinstance(doc.get('data'), dict):
        raise SettingsError('expected a ConfigMap with a data section')
    return doc['data']


def set_value(text: str, key: str, value: str) -> str:
    """Return ``text`` with ``data.<key>`` set to ``value``, touching nothing else."""
    before = load_settings(text)
    if key not in before:
        raise SettingsError(f"Missing key '{key}'")
    pattern = re.compile(rf'^(?P<indent>\s+){re.escape(key)}:[^\n]*$', re.M)
    if len(pattern.findall(text)) != 1:
        raise SettingsError(f"expected exactly one '{key}:' line")
    updated = pattern.sub(lambda m: f"{m['indent']}{key}: {value}", text)
    after = load_settings(updated)
    if after != {**before, key: value}:
        raise SettingsError(f'editing {key} would change other settings; refusing to write')
    return updated


def set_cert_issuer(issuer: str, *, root: Path = ROOT, dry_run: bool = False) -> list[str]:
    """Set CERT_ISSUER; return the operator messages."""
    allowed = platform_issuers(root)
    if issuer not in allowed:
        raise SettingsError(f"unknown issuer '{issuer}'; expected one of {', '.join(sorted(allowed))}")
    path = root / SETTINGS
    if not path.is_file():
        raise SettingsError(f'Missing settings file: {SETTINGS}')
    text = path.read_text()
    current = load_settings(text).get('CERT_ISSUER')
    if current is None:
        raise SettingsError(f"Missing key 'CERT_ISSUER' in {SETTINGS}")
    messages = []
    if current == issuer:
        messages.append(f'[INFO] CERT_ISSUER already set to {issuer}')
    elif dry_run:
        set_value(text, 'CERT_ISSUER', issuer)  # prove the edit is safe even when not writing
        messages.append(f'[INFO] CERT_ISSUER would update to {issuer} in {SETTINGS}')
    else:
        updated = set_value(text, 'CERT_ISSUER', issuer)
        tmp = path.with_suffix(path.suffix + '.tmp')
        tmp.write_text(updated)
        tmp.replace(path)
        messages.append(f'[INFO] CERT_ISSUER updated to {issuer} in {SETTINGS}')
    messages.append('[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile')
    return messages


def platform_certs(argv: list[str] | None = None) -> int:
    if not argv or len(argv) != 1:
        print('usage: platform-certs letsencrypt-staging|letsencrypt-prod', file=sys.stderr)
        return 2
    try:
        messages = set_cert_issuer(argv[0], dry_run=os.environ.get('DRY_RUN', 'false') == 'true')
    except SettingsError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    print('\n'.join(messages))
    return 0
