"""Facts about this platform that more than one command needs.

Repository paths, label names, shared ClickStack queries, and discovery of the
Flux units defined in Git. Keep one copy here rather than a constant per module.
"""
from __future__ import annotations

from collections.abc import Iterator
from pathlib import Path

import yaml

from swhurl import ROOT

# Repository paths (relative to the repository root).
CLUSTER = Path('clusters/home')
ROOT_UNITS = CLUSTER / 'flux-system/kustomizations.yaml'
SOURCES = CLUSTER / 'flux-system/sources'
SETTINGS = SOURCES / 'configmap-platform-settings.yaml'
HELM_REPOSITORIES = SOURCES / 'helmrepositories.yaml'
BOOTSTRAP_PATHS = (CLUSTER / 'flux-system', SOURCES)
ISSUERS = Path('infrastructure/cert-manager/issuers')
INGESTION_SECRET = Path('platform-services/otel/base/secret-hyperdx.sops.yaml')
AGE_KEY = Path('age.agekey')

# Labels and annotations under the platform's domain.
LABEL_DOMAIN = 'platform.swhurl.com'


def label(name: str) -> str:
    """``platform.swhurl.com/<name>``."""
    return f'{LABEL_DOMAIN}/{name}'


# ClickStack MongoDB queries (run with ``mongosh hyperdx --quiet --eval``).
TEAM_KEY_SCRIPT = ('const keys = db.teams.distinct("apiKey").filter(k => typeof k === "string" && k.length > 0);'
                   ' if (keys.length !== 1) quit(2); print(keys[0]);')
"""Print the one team ingestion key; exit 2 unless exactly one exists."""
COLLECTION_COUNTS_SCRIPT = ('const c = {}; db.getCollectionNames().sort().forEach(n => '
                            '{ c[n] = db[n].countDocuments(); }); print(JSON.stringify(c));')
"""Print ``{collection: document count}`` as JSON."""


def flux_unit_documents(root: Path = ROOT) -> Iterator[tuple[Path, dict]]:
    """Every Flux Kustomization defined in Git, with the file that defines it."""
    files = [root / ROOT_UNITS, *sorted(p for p in (root / CLUSTER).iterdir() if p.suffix in ('.yaml', '.yml'))]
    for path in files:
        for doc in yaml.safe_load_all(path.read_text()):
            if (isinstance(doc, dict) and doc.get('apiVersion') == 'kustomize.toolkit.fluxcd.io/v1'
                    and doc.get('kind') == 'Kustomization'):
                yield path, doc


def decrypted_secret_files(root: Path = ROOT) -> dict[str, list[Path]]:
    """For each unit that decrypts SOPS, the encrypted Secret files under its path."""
    found = {}
    for _, unit in flux_unit_documents(root):
        spec = unit.get('spec') or {}
        if 'decryption' in spec:
            path = root / spec['path']
            found[unit['metadata']['name']] = sorted(p.relative_to(root) for p in path.rglob('*.sops.y*ml'))
    return found
