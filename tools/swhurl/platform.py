"""Facts about this platform that more than one command needs.

Repository paths, label names, and the inventory of what Git defines: the Flux units (``units``)
and the namespaces their manifests declare (``namespaces``). Code, tests and the doc check read
the inventory rather than retyping a unit's name, file or inputs.
"""
from __future__ import annotations

from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, yamlio

# Repository paths (relative to the repository root).
CLUSTER = Path('clusters/home')
UNITS_DOC = Path('docs/architecture.md')  # its Flux units table is checked against units() (make check-repo)
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
# What the console image is built from (images/console/Dockerfile); validate.yml's console-image job hashes the same paths.
CONSOLE_IMAGE_INPUTS = ('images/console', '.dockerignore', 'pyproject.toml', 'uv.lock', 'tools', str(SETTINGS))
# Labels and annotations under the platform's domain.
LABEL_DOMAIN = 'platform.swhurl.com'


def label(name: str) -> str:
    """``platform.swhurl.com/<name>``."""
    return f'{LABEL_DOMAIN}/{name}'


def base_domain(root: Path = ROOT) -> str:
    """``BASE_DOMAIN`` from platform-settings: parent of platform hosts and the sign-in cookie."""
    data = yamlio.load((root / SETTINGS).read_text()).get('data') or {}
    return data['BASE_DOMAIN']


def github_repository(root: Path = ROOT) -> str:
    """``owner/name`` of the GitHub repository Flux applies (the swhurl-platform GitRepository)."""
    url = yamlio.load((root / GIT_REPOSITORIES).read_text())['spec']['url']
    return url.removeprefix('https://github.com/').removesuffix('.git')


def flux_unit_documents(root: Path = ROOT) -> Iterator[tuple[Path, dict]]:
    """Every Flux Kustomization defined in Git, with the file that defines it."""
    files = [root / ROOT_UNITS, *sorted(p for p in (root / CLUSTER).iterdir() if p.suffix in ('.yaml', '.yml'))]
    for path in files:
        for doc in yamlio.load_all(path.read_text()):
            if (isinstance(doc, dict) and doc.get('apiVersion') == 'kustomize.toolkit.fluxcd.io/v1'
                    and doc.get('kind') == 'Kustomization'):
                yield path, doc


@dataclass(frozen=True)
class Unit:
    """One Flux unit (GLOSSARY.md) as Git defines it."""
    name: str
    file: Path  # the file that defines it, from the repository root
    path: Path  # the directory it applies, from the repository root
    depends_on: tuple[str, ...]
    substitutes: bool  # its manifests get the platform settings (${...})
    decrypts: bool  # it decrypts SOPS Secrets
    labels: dict
    spec: dict

    @property
    def is_app(self) -> bool:
        """An app instance's unit (``app-<app>-<env>``) rather than a shared one."""
        return self.name.startswith('app-')


def units(root: Path = ROOT) -> list[Unit]:
    """Every Flux unit defined in Git: the two root units, the shared ones, then one per app instance."""
    found = []
    for file, doc in flux_unit_documents(root):
        spec = doc.get('spec') or {}
        found.append(Unit(doc['metadata']['name'], file.relative_to(root), Path(spec.get('path', '')),
                          tuple(d['name'] for d in spec.get('dependsOn') or []), 'postBuild' in spec,
                          'decryption' in spec, doc['metadata'].get('labels') or {}, spec))
    return found


def namespaces(root: Path = ROOT) -> set[str]:
    """The namespaces the shared units' manifests declare (an app instance's is its own: ``<app>-<env>``)."""
    return {doc['metadata']['name'] for unit in units(root) if not unit.is_app
            for file in sorted((root / unit.path).rglob('*.yaml'))
            for doc in yamlio.load_all(file.read_text()) if isinstance(doc, dict) and doc.get('kind') == 'Namespace'}


def decrypted_secret_files(root: Path = ROOT) -> dict[str, list[Path]]:
    """For each unit that decrypts SOPS, the encrypted Secret files under its path."""
    return {unit.name: sorted(p.relative_to(root) for p in (root / unit.path).rglob('*.sops.y*ml'))
            for unit in units(root) if unit.decrypts}
