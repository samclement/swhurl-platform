"""Writing an instance's records to the checkout, and what intake reads for a Release.

``change_release`` is how an operation's result reaches the files: it makes any NATS key the records
now need (an effect before rendering, never undone on success), renders every instance
(apps/model/sync.py), lets the operation refuse the rendered result, then writes the files and the
offers documents beside them. ``set_offers`` and its helpers read what an image declares and offers
from its repository at the commit it was built from (docs/apps.md#swhurlyaml).
"""
from __future__ import annotations

import json
import re
from dataclasses import replace
from pathlib import Path

from swhurl import yamlio
from swhurl.apps import declaration, events, nkeys, offers
from swhurl.apps.derive import instance_dir
from swhurl.apps.instance import Instance
from swhurl.apps.model import sync as model_sync
from swhurl.apps.model.draft import EVENTS_SECRET, Entry
from swhurl.apps.model.events import keyed as events_keyed
from swhurl.apps.model.records import Image, Release
from swhurl.apps.yaml_file import EditError
from swhurl.run import CommandError, Runner


def dump(docs) -> str:
    return yamlio.dump_all(docs)


def new_nats_key(instance: Path, app: str, env: str, *, runner: Runner | None = None) -> str:
    """Write a new NATS seed into the instance's encrypted Secret and return its public key. The seed
    goes from memory into a file that sops encrypts in place: it is never printed."""
    secret = instance / events.SECRET_FILE
    seed, public = nkeys.generate()
    secret.write_text(dump([{'apiVersion': 'v1', 'kind': 'Secret',
                             'metadata': {'name': events.secret_name(app), 'namespace': Instance(app, env).namespace},
                             'type': 'Opaque', 'stringData': {events.SEED_VARIABLE: seed}}]))
    try:
        (runner or Runner()).run(['sops', '--encrypt', '--in-place', str(secret)])
    except CommandError as error:
        secret.unlink(missing_ok=True)
        raise EditError(f'could not SOPS-encrypt {secret.name}; nothing was changed: {error}') from None
    if 'sops:' not in secret.read_text():
        secret.unlink()
        raise EditError(f'{secret.name} was not encrypted; removed it')
    return public


def offer_files(folder: Path, repository: str, commit: str, files: dict[str, str]) -> dict[Path, str | None]:
    """What the offers directory should hold: path -> text, or None for a file to delete."""
    if not files:
        return {path: None for path in sorted(folder.iterdir())} if folder.is_dir() else {}
    source = offers.Source(repository, commit, next((n for n in files if n not in (offers.EVENTS_NAME, offers.FLAGS_NAME)), ''),
                           offers.EVENTS_NAME if offers.EVENTS_NAME in files else '',
                           offers.FLAGS_NAME if offers.FLAGS_NAME in files else '').document()
    wanted = {offers.SOURCE_FILE: '# Read from the app\'s repository by make app-offers: do not edit.\n' + dump([source]), **files}
    planned: dict[Path, str | None] = {path: None for path in sorted(folder.iterdir()) if path.name not in wanted} if folder.is_dir() else {}
    planned.update({folder / name: text for name, text in wanted.items()
                    if not (folder / name).is_file() or (folder / name).read_text() != text})
    return planned


def write_files(planned: dict[Path, str | None]) -> bool:
    for path, text in planned.items():
        if text is None:
            path.unlink()
        else:
            path.parent.mkdir(exist_ok=True)
            path.write_text(text)
    for folder in {path.parent for path in planned}:
        if folder.is_dir() and not any(folder.iterdir()):
            folder.rmdir()
    return bool(planned)


def change_release(root: Path, each: Instance, entry: Entry, planned: dict[Path, str | None], *,
                   runner: Runner | None = None,
                   adding: bool = False, keyed: bool = False) -> bool:
    """Make ``entry`` (a new Release, and what the operation chose with it) the instance's records, or a
    new instance's (``adding``): give it a NATS key if it now has events (or ``keyed``) and none, render,
    then write the rendered files and the offers documents (``planned``). A refusal writes nothing. Whether anything changed."""
    instance = each.folder(root)
    created = ''
    if (keyed or entry.release.publishes or entry.settings.subscriptions) and not events_keyed(entry):
        created = new_nats_key(instance, each.app, each.env, runner=runner)
        entry = replace(entry, settings=replace(entry.settings, allocations=replace(entry.settings.allocations, events_key=created)),
                        encrypted=entry.encrypted | {EVENTS_SECRET})
    try:
        prepared = model_sync.prepare(root, each, lambda _: entry, adding=adding)
    except EditError:
        if created:
            (instance / events.SECRET_FILE).unlink(missing_ok=True)
        raise
    return bool(model_sync.apply(root, prepared)) | write_files(planned)


def offered(release: Release, commit: str, files: dict[str, str], described: list[offers.Event] | None,
            api_path: str) -> Release:
    """A release with what it offers: the commit its documents were read at, where a web app serves its
    API description, and, with an events document, the events it publishes."""
    release = replace(release, offers=commit if files else '')
    if api_path and release.kind == 'web':
        release = replace(release, api_path=api_path)
    if described is not None:
        release = replace(release, publishes=tuple(event.name for event in described))
    return release


def write_offers(root: Path, app: str, env: str, repository: str, commit: str, files: dict[str, str],
                 described: list[offers.Event] | None, *, api_path: str = '', runner: Runner | None = None) -> bool:
    """Keep an instance's offers (``files``: name in the offers directory -> text) and what follows from
    them on its release (``offered``). Returns whether anything changed. Without files the directory
    and the note of their commit go; what it publishes is left as it is. A refusal (an event another
    app still receives is gone) writes nothing."""
    each = Instance(app, env)
    world, _ = model_sync.read(root)
    if each not in world:
        instance_dir(root, app, env)  # says which instance is missing
    entry = world[each]
    planned = offer_files(each.folder(root) / offers.OFFERS_DIR, repository, commit, files)
    release = offered(entry.release, commit, files, described, api_path)
    return change_release(root, each, replace(entry, release=release), planned, runner=runner)


def image_source(image: Image, app: str, env: str) -> tuple[str, str]:
    """``(OWNER/REPO, commit)`` an image was built from."""
    if not image.repository.startswith('ghcr.io/'):
        raise EditError(f'{app}/{env} runs {image.repository or "no image"}: only an app published from its own GitHub repository has offers')
    try:
        return image.repository.removeprefix('ghcr.io/'), offers.commit_of(image.tag)
    except offers.OfferError as error:
        raise EditError(str(error)) from None


def image_declaration(image: Image, app: str, env: str, *, token: str = '',
                      opener: offers.Opener = offers._open) -> declaration.Declaration:
    """The swhurl.yaml of an image, read at the commit it was built from."""
    repository, commit = image_source(image, app, env)
    try:
        declared = declaration.read(repository, commit, token=token, opener=opener)
    except declaration.DeclarationError as error:
        raise EditError(str(error)) from None
    if declared is None:
        raise EditError(f'{repository}@{commit} has no {declaration.FILE} (or the repository is private: set GITHUB_TOKEN)')
    return declared


def read_offers(repository: str, commit: str, declared: declaration.Declaration, *, token: str = '',
                opener: offers.Opener = offers._open) -> tuple[dict[str, str], list[offers.Event] | None, str]:
    """``(files, described events, API path)``: the documents an image's swhurl.yaml names (provides),
    read at its commit, and where it says it serves its API description."""
    try:
        files, described = {}, None
        for key, kind, path in (('apiFile', 'api', declared.api_file), ('eventsFile', 'events', declared.events_file)):
            if not path:
                continue
            text = offers.fetch(repository, commit, str(path), token=token, opener=opener)
            if text is None:
                raise offers.OfferError(f'{declaration.FILE} names {path} (provides.{key}), which {repository}@{commit} does not have')
            if kind == 'api':
                offers.check_api(text, str(path))
                files['openapi' + (Path(str(path)).suffix or '.yaml')] = text
            else:
                described = offers.events(text, str(path))
                files[offers.EVENTS_NAME] = text
        if declared.flags:  # what the console's flag form is built from (docs/apps.md#feature-flags)
            files[offers.FLAGS_NAME] = json.dumps(declared.flags, indent=2) + '\n'
    except (offers.OfferError, declaration.DeclarationError) as error:
        raise EditError(str(error)) from None
    served = declared.api_path if re.fullmatch(r'/[A-Za-z0-9_./-]*', declared.api_path) else ''
    return files, described, served


def describe_offers(app: str, env: str, commit: str, files: dict[str, str], described: list[offers.Event] | None,
                    declared: declaration.Declaration, changed: bool) -> None:
    what = ', '.join(filter(None, [next((f'its API ({n})' for n in files if n not in (offers.EVENTS_NAME, offers.FLAGS_NAME)), ''),
                                   described is not None and f'events: {", ".join(e.name for e in described) or "none"}',
                                   declared.flags and f'flags: {", ".join(declared.flags)}']))
    print(f'[OK] {app}/{env} at {commit} offers {what or "nothing"}' + ('' if changed else ' (unchanged)'))


def set_offers(root: Path, app: str, env: str, *, token: str = '', opener: offers.Opener = offers._open,
               runner: Runner | None = None) -> bool:
    """Read what ``app/env`` offers from its repository, at the commit its image was built from: the
    documents its swhurl.yaml names (provides, the file's live field: declaration.py). Returns whether
    anything changed."""
    world, _ = model_sync.read(root)
    if Instance(app, env) not in world:
        instance_dir(root, app, env)  # says which instance is missing
    image = world[Instance(app, env)].release.image
    repository, commit = image_source(image, app, env)
    declared = image_declaration(image, app, env, token=token, opener=opener)
    files, described, served = read_offers(repository, commit, declared, token=token, opener=opener)
    changed = write_offers(root, app, env, repository, commit, files, described, api_path=served, runner=runner)
    describe_offers(app, env, commit, files, described, declared, changed)
    return changed
