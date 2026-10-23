"""An app in one environment (GLOSSARY.md): its names, where it is kept in Git, and how to recognise one.

Everything else follows from the pair: ``demo-orders`` in ``staging`` is the namespace
``demo-orders-staging``, the Flux unit ``app-demo-orders-staging`` (``clusters/home/<unit>.yaml``)
and the folder ``apps/demo-orders/staging``. This module is the one place that says so, in both
directions. Standard library only, so every part of the tooling can import it.
"""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from swhurl.apps.model import records

ENVIRONMENTS = ('staging', 'prod')
INSTANCE_ROOTS = (Path('apps'), Path('tests/fixtures/apps/apps'))
"""Where instances are kept, from the repository root: the platform's own first, then the generated fixtures."""
UNITS = Path('clusters/home')


@dataclass(frozen=True)
class Instance:
    """Always a valid one: a name from outside (a URL, a unit, a namespace, a path) goes through
    ``named`` or an ``of_*`` reader, which answer None when it does not name an instance."""
    app: str
    env: str

    def __post_init__(self) -> None:
        if not isinstance(self.app, str) or not records.NAME_RE.match(self.app) or self.env not in ENVIRONMENTS:
            raise ValueError(f'no instance {self.app}/{self.env}: APP must be a DNS label and ENV one of {", ".join(ENVIRONMENTS)}')

    def __str__(self) -> str:
        return f'{self.app}/{self.env}'

    @property
    def namespace(self) -> str:
        return f'{self.app}-{self.env}'

    @property
    def unit(self) -> str:
        return f'app-{self.app}-{self.env}'

    def folder(self, root: Path, base: Path = INSTANCE_ROOTS[0]) -> Path:
        """Its manifests, in the checkout at ``root``."""
        return instance_folder(root, self.app, self.env, base)

    def unit_file(self, root: Path) -> Path:
        """The file holding its Flux unit, in the checkout at ``root``."""
        return instance_unit_file(root, self.app, self.env)

    @classmethod
    def named(cls, app: object, env: object) -> Instance | None:
        try:
            return cls(app, env)  # type: ignore[arg-type]
        except ValueError:
            return None

    @classmethod
    def of_namespace(cls, namespace: str) -> Instance | None:
        """``<app>-<env>``; the app's own name may contain hyphens."""
        app, _, env = namespace.rpartition('-')
        return cls.named(app, env)

    @classmethod
    def of_unit(cls, unit: str) -> Instance | None:
        return cls.of_namespace(unit.removeprefix('app-')) if unit.startswith('app-') else None

    @classmethod
    def of_path(cls, path: str) -> Instance | None:
        """A Flux unit's ``spec.path`` (``./apps/<app>/<env>``), under any of the instance roots."""
        parts = Path(path.removeprefix('./')).parts
        if len(parts) < 3 or Path(*parts[:-2]) not in INSTANCE_ROOTS:
            return None
        return cls.named(parts[-2], parts[-1])


def instance_folder(root: Path, app: str, env: str, base: Path = INSTANCE_ROOTS[0]) -> Path:
    """Where an instance's manifests are (or would be) in the checkout at ``root``. For the commands that
    still pass ``app`` and ``env`` as text and say themselves what is wrong with them: nothing is checked here."""
    return root / base / app / env


def instance_unit_file(root: Path, app: str, env: str) -> Path:
    """Where an instance's Flux unit is (or would be) in the checkout at ``root``; as unchecked as ``instance_folder``."""
    return root / UNITS / f'app-{app}-{env}.yaml'
