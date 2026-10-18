"""What rendering works on: one instance's records (Entry), the platform's own inputs (Platform), and
the instance's documents as they are being built (Draft). Each capability module adds its part to a Draft."""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from swhurl.apps.instance import Instance
from swhurl.apps.model.records import App, Release, Settings

EVENTS_SECRET, APP_SECRET = 'events', 'secret'
"""The encrypted files an instance may hold (events.sops.yaml, secret.sops.yaml); rendering reads only that they exist."""


class RenderError(ValueError):
    """Records that cannot be rendered together: the message names the instance and what is missing."""


@dataclass(frozen=True)
class Entry:
    app: App
    release: Release
    settings: Settings
    encrypted: frozenset[str] = frozenset()


@dataclass(frozen=True)
class Platform:
    """Inputs that are the platform's, not an app's: read once from the checkout."""
    signing_keys: str = ''
    auto_deploy: bool = True  # staging follows new images (the generated test fixtures do not)
    egress: bool = True  # the egress proxy exists (not in scratch roots)
    controller_key: str = ''  # NATS: the stream controller's public key; '' where there is no NATS
    console_key: str = ''
    base: Path = Path('apps')  # where instances are kept, from the root


World = dict[Instance, Entry]


def environment(world: World, env: str) -> dict[str, Entry]:
    """The entries of one environment by app name."""
    return {each.app: entry for each, entry in world.items() if each.env == env}


@dataclass
class Draft:
    instance: Instance
    namespace: dict
    release: dict
    unit: dict
    resources: list[str]
    files: dict[str, list[dict]] = field(default_factory=dict)
    routed: bool = False  # Traefik may reach the app (it has a route)
    callers: list[str] = field(default_factory=list)

    @property
    def values(self) -> dict:
        return self.release['spec']['values']

    @property
    def controller(self) -> dict:
        return self.values['controllers']['main']

    @property
    def container(self) -> dict:
        return self.controller['containers']['main']

    @property
    def env(self) -> dict:
        return self.container.setdefault('env', {})

    def annotate(self, document: dict, key: str, value: str) -> None:
        document['metadata'].setdefault('annotations', {})[key] = value

    def env_from(self, secret: str, *, optional: bool = False) -> None:
        reference = {'secretRef': {'name': secret, **({'optional': True} if optional else {})}}
        self.container.setdefault('envFrom', []).append(reference)

    def reload(self, secret: str) -> None:
        """Restart the app when this Secret changes (Reloader)."""
        from swhurl.apps.contract import RELOAD

        annotations = self.controller.setdefault('annotations', {})
        annotations[RELOAD] = ','.join([name for name in annotations.get(RELOAD, '').split(',') if name] + [secret])

    def depend(self, unit: str) -> None:
        self.unit['spec']['dependsOn'].append({'name': unit})

    def decrypt(self) -> None:
        self.unit['spec']['decryption'] = {'provider': 'sops', 'secretRef': {'name': 'sops-age'}}

    def add_file(self, name: str, documents: list[dict]) -> None:
        self.files[name] = documents
        if name not in self.resources:
            self.resources.append(name)

    def list_file(self, name: str) -> None:
        """A file the instance keeps that rendering does not write (an encrypted Secret)."""
        if name not in self.resources:
            self.resources.append(name)
