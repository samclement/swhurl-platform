"""Facts about this platform that more than one command needs.

Repository paths and the inventory of what Git defines: the Flux units (``units``)
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
ROOT_UNITS = CLUSTER / 'flux-system/kustomizations.yaml'
SOURCES = CLUSTER / 'flux-system/sources'
SETTINGS = SOURCES / 'configmap-platform-settings.yaml'
HELM_REPOSITORIES = SOURCES / 'helmrepositories.yaml'
GIT_REPOSITORIES = SOURCES / 'gitrepositories.yaml'
ISSUERS = Path('infra/issuers')
# v2's platform profile.
PROFILE = Path('v2/testdata/profile.json')
AGE_KEY = Path('age.agekey')
# The repository the console clones and opens PRs against.
GITHUB_REPO = 'samclement/swhurl-platform'
# The console's GitHub token (Secret console/console-github, from platform/console/secret.sops.yaml).
CONSOLE_TOKEN_SECRET = 'console-github'
CONSOLE_TOKEN_KEY = 'GITHUB_TOKEN'
# The second token in the same Secret: creates new app repositories (all repositories; console/repos.py).
APP_REPOS_TOKEN_KEY = 'APP_REPOS_TOKEN'


def base_domain(root: Path = ROOT) -> str:
    """``BASE_DOMAIN`` from platform-settings: parent of platform hosts and the sign-in cookie."""
    data = yamlio.load((root / SETTINGS).read_text()).get('data') or {}
    return data['BASE_DOMAIN']


def github_repository(root: Path = ROOT) -> str:
    """``owner/name`` of the GitHub repository Flux applies (the swhurl-platform GitRepository)."""
    url = yamlio.load((root / GIT_REPOSITORIES).read_text())['spec']['url']
    return url.removeprefix('https://github.com/').removesuffix('.git')


def flux_unit_documents(root: Path = ROOT) -> Iterator[tuple[Path, dict]]:
    """Every Flux Kustomization defined in Git, with the file that defines it.

    A unit file may be YAML or JSON: JSON is a YAML subset, both are read the same way, and the v2
    compiler writes its units as JSON. Either must be found, or a unit Flux applies goes unchecked.
    """
    files = [root / ROOT_UNITS, *sorted(p for p in (root / CLUSTER).iterdir() if p.suffix in ('.yaml', '.yml', '.json'))]
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

