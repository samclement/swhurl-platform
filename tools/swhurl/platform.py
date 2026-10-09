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
GIT_REPOSITORIES = SOURCES / 'gitrepositories.yaml'
BOOTSTRAP_PATHS = (CLUSTER / 'flux-system', SOURCES)
ISSUERS = Path('infra/issuers')
INGESTION_SECRET = Path('platform/otel/secret.sops.yaml')
# Off-host copies of the encrypted MongoDB backups (versioned, 90-day lifecycle; see docs/operations.md).
BACKUP_S3_URI = 's3://swhurl-platform-backups-110927251694/clickstack-mongodb/'
# App SQLite backups: one prefix per namespace below this (backup-sqlite).
SQLITE_S3_URI = 's3://swhurl-platform-backups-110927251694/app-sqlite/'
# The collectors' copy of the ClickStack team ingestion key (namespace logging).
INGESTION_SECRET_NAME = 'clickstack-ingestion-key'
INGESTION_KEY = 'CLICKSTACK_INGESTION_KEY'
AGE_KEY = Path('age.agekey')
# The repository the console clones and opens PRs against.
GITHUB_REPO = 'samclement/swhurl-platform'
# The console's GitHub token (Secret console/console-github, from platform/console/secret.sops.yaml).
CONSOLE_TOKEN_SECRET = 'console-github'
CONSOLE_TOKEN_KEY = 'GITHUB_TOKEN'
# The second token in the same Secret: creates new app repositories (all repositories; console/repos.py).
APP_REPOS_TOKEN_KEY = 'APP_REPOS_TOKEN'
# What the console image is built from (images/console/Dockerfile); publish-console.yml hashes the same paths.
CONSOLE_IMAGE_INPUTS = ('images/console', '.dockerignore', 'pyproject.toml', 'uv.lock', 'tools', str(SETTINGS))
# Labels and annotations under the platform's domain.
LABEL_DOMAIN = 'platform.swhurl.com'


def label(name: str) -> str:
    """``platform.swhurl.com/<name>``."""
    return f'{LABEL_DOMAIN}/{name}'


def base_domain(root: Path = ROOT) -> str:
    """``BASE_DOMAIN`` from platform-settings: parent of platform hosts and the sign-in cookie."""
    data = yaml.safe_load((root / SETTINGS).read_text()).get('data') or {}
    return data['BASE_DOMAIN']


def github_repository(root: Path = ROOT) -> str:
    """``owner/name`` of the GitHub repository Flux applies (the swhurl-platform GitRepository)."""
    url = yaml.safe_load((root / GIT_REPOSITORIES).read_text())['spec']['url']
    return url.removeprefix('https://github.com/').removesuffix('.git')


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
