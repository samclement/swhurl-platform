"""``console-image``: pin the console HelmRelease to the image published for this checkout (Git edit only).

Validate's console-image job tags every console image ``src-<hash>``, where the hash
covers everything the image is built from (``platform.CONSOLE_IMAGE_INPUTS``,
as committed at ``HEAD``). This computes the same hash, asks GHCR (anonymously;
the package is public) for that tag's digest, and writes the tag and digest
into ``platform/console/helmrelease.yaml``. Commit and push to deploy.

Validate's deploy-console job runs it, once every check has passed, as ``console-image --expect
src-<built> --commit``: it pins on the newest ``main`` and pushes like every bot (``bots.push_main``),
so the console deploys itself. ``--expect`` makes it do nothing when ``main`` has since moved to other
image inputs (that commit's own run pins its image).

"""
from __future__ import annotations

import argparse
import hashlib
import re
import sys
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, bots, platform
from swhurl.run import CommandError, Runner

REGISTRY = 'ghcr.io'
REPOSITORY = 'samclement/swhurl-console'
RELEASE = Path('platform/console/helmrelease.yaml')
INDEX_TYPES = ('application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json')


@dataclass(frozen=True)
class Image:
    """One published image: what it is built from and which manifest lines carry its pin."""
    name: str
    repository: str
    inputs: tuple[str, ...]
    workflow: str
    # (file, marker): with a marker only lines ending ``# image: <marker>`` are pinned and the file may be
    # absent; without one the file must exist and hold exactly one ``tag:`` and one ``digest:`` line.
    pins: tuple[tuple[Path, str | None], ...]


NOTIFY_RELEASE = Path('platform/notify/helmrelease.yaml')
CONSOLE = Image('console', REPOSITORY, platform.CONSOLE_IMAGE_INPUTS, 'Validate',
                ((RELEASE, None), (NOTIFY_RELEASE, 'console')))  # the notify service runs the same image


class ImageError(Exception):
    pass


def content_tag(runner: Runner, root: Path = ROOT, revision: str = 'HEAD', image: Image = CONSOLE) -> str:
    """``src-<hash>`` exactly as Validate's console-image job computes it (object IDs, one per line, sha256, 16 hex)."""
    ids = [runner.output(['git', '-C', str(root), 'rev-parse', f'{revision}:{path}']).strip()
           for path in image.inputs]
    return 'src-' + hashlib.sha256(''.join(f'{i}\n' for i in ids).encode()).hexdigest()[:16]


def published_digest(runner: Runner, tag: str, repository: str = REPOSITORY) -> str | None:
    """The index digest GHCR serves for ``tag``, or None if it is not published."""
    try:
        token = runner.json(['curl', '--silent', '--show-error', '--fail',
                             f'https://{REGISTRY}/token?scope=repository:{repository}:pull'])['token']
    except CommandError:
        return None  # GHCR refuses a token for a package that does not exist yet
    runner.add_secret(token)
    reply = runner.run(['curl', '--silent', '--show-error', '--head', '--config', '-',
                        *[a for t in INDEX_TYPES for a in ('--header', f'Accept: {t}')],
                        f'https://{REGISTRY}/v2/{repository}/manifests/{tag}'],
                       input=f'header = "Authorization: Bearer {token}"\n', check=False)
    lines = reply.stdout.splitlines()
    if not lines or ' 200' not in lines[0]:
        return None
    for line in lines[1:]:
        name, _, value = line.partition(':')
        if name.strip().lower() == 'docker-content-digest':
            return value.strip()
    return None


def pin(text: str, tag: str, digest: str, marker: str | None = None, path: Path = RELEASE) -> str:
    """Replace the image ``tag:`` and ``digest:`` lines, keeping everything else (comments included).

    Without a marker there must be exactly one of each; with one, every line ending ``# image: <marker>``
    is replaced and there must be at least one of each.
    """
    for key, value in (('tag', tag), ('digest', digest)):
        if marker is None:
            text, count = re.subn(rf'^(\s+{key}: )\S+$', rf'\g<1>{value}', text, flags=re.M)
            if count != 1:
                raise ImageError(f'{path} must have exactly one image {key}: line (found {count})')
        else:
            text, count = re.subn(rf'^(\s+{key}: )\S+( # image: {re.escape(marker)})$', rf'\g<1>{value}\g<2>', text,
                                  flags=re.M)
            if count < 1:
                raise ImageError(f'{path} has no {key}: line marked "# image: {marker}"')
    return text


def pin_release(runner: Runner, root: Path, image: Image = CONSOLE) -> tuple[str, str, list[Path]]:
    """Pin every manifest that runs ``image`` to this checkout's build: (tag, digest, files changed)."""
    if runner.output(['git', '-C', str(root), 'status', '--porcelain', '--', *image.inputs]).strip():
        raise ImageError('the image inputs have uncommitted changes; commit and push them, wait for the Validate run')
    tag = content_tag(runner, root, image=image)
    digest = published_digest(runner, tag, image.repository)
    if not digest:
        raise ImageError(f'{REGISTRY}/{image.repository}:{tag} is not published yet; push, then wait for the '
                         f'"{image.workflow}" run of that push to build it')
    changed = []
    for relative, marker in image.pins:
        path = root / relative
        if marker is not None and not path.exists():
            continue
        text = path.read_text()
        updated = pin(text, tag, digest, marker, relative)
        if updated != text:
            changed.append(relative)
            if not runner.dry_run:
                path.write_text(updated)
    return tag, digest, changed


def pinned(runner: Runner, image: Image, expect: str | None, *, render: bool = True) -> bots.Change:
    """The pin as a bot change: nothing when ``work`` builds other inputs than ``expect`` or is already pinned.
    ``render``: on a main whose records Render has not rendered yet the checks would fail, so render it too
    (as the merge gate does); the commit then carries those files."""
    def change(work: Path, _base: str) -> bots.Commit | None:
        if expect and (current := content_tag(runner, work, image=image)) != expect:
            print(f'[OK] nothing to do: main now builds {current}, not {expect}; its own Validate run pins it')
            return None
        tag, digest, changed = pin_release(runner, work, image)
        if not changed:
            print(f'[OK] already pinned to {tag}@{digest}')
            return None
        print(f'{"  would pin" if runner.dry_run else "[OK] pinned"} {", ".join(map(str, changed))} to {tag}@{digest}')
        if render:
            bots.swhurl(runner, work, 'app-sync')
        return bots.Commit(f'deploy: {image.name} image {tag} (Validate)')
    return change


def main(argv: list[str] | None = None, runner: Runner | None = None, root: Path = ROOT,
         image: Image = CONSOLE) -> int:
    parser = argparse.ArgumentParser(prog='swhurl console-image', description=__doc__.split('\n\n')[0])
    parser.add_argument('--expect', help="only pin if HEAD builds this src-<hash> tag (Validate's deploy job)")
    parser.add_argument('--commit', action='store_true', help="pin the newest main and push it (Validate's deploy job)")
    args = parser.parse_args(argv or [])
    runner = runner or Runner.from_environment()
    try:
        if args.commit and not runner.dry_run:
            made = bots.push_main(runner, root, pinned(runner, image, args.expect))
            if made:
                print(f'[OK] pushed {made[:12]} to main')
            return 0
        if pinned(runner, image, args.expect, render=False)(root, '') and not runner.dry_run:
            print('[INFO] Next: commit, push, make flux-reconcile')
        return 0
    except (ImageError, CommandError, RuntimeError, KeyError, TypeError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
