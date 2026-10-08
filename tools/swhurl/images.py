"""``console-image``: pin the console HelmRelease to the image published for this checkout (Git edit only).

The publish workflow tags every console image ``src-<hash>``, where the hash
covers everything the image is built from (``platform.CONSOLE_IMAGE_INPUTS``,
as committed at ``HEAD``). This computes the same hash, asks GHCR (anonymously;
the package is public) for that tag's digest, and writes the tag and digest
into ``platform/console/helmrelease.yaml``. Commit and push to deploy.

The publish workflow runs it after each build as ``console-image --expect
src-<built> --commit`` on the latest ``main``: it pins, commits and pushes, so
the console deploys itself. ``--expect`` makes it do nothing when ``main`` has
since moved to other image inputs (that commit's own run pins its image);
a push rejected because ``main`` moved is rebased and retried once.

The incident reviewer (``platform/incident-review/helmrelease.yaml``) runs two
published images: this console image for its collect and decide steps, and the
analysis worker image. Their ``tag:`` and ``digest:`` lines end in ``# image: console``
or ``# image: worker``; ``console-image`` keeps the first pair equal to the console's
pin, and ``worker-image`` pins the second the same way from its own inputs.
"""
from __future__ import annotations

import argparse
import hashlib
import re
import sys
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.run import CommandError, Runner

REGISTRY = 'ghcr.io'
REPOSITORY = 'samclement/swhurl-console'
RELEASE = Path('platform/console/helmrelease.yaml')
REVIEWER = Path('platform/incident-review/helmrelease.yaml')
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


CONSOLE = Image('console', REPOSITORY, platform.CONSOLE_IMAGE_INPUTS, 'Publish console image',
                ((RELEASE, None), (REVIEWER, 'console')))
WORKER = Image('incident review worker', 'samclement/swhurl-incident-review-worker', platform.WORKER_IMAGE_INPUTS,
               'Publish incident review worker image', ((REVIEWER, 'worker'),))


class ImageError(Exception):
    pass


def content_tag(runner: Runner, root: Path = ROOT, revision: str = 'HEAD', image: Image = CONSOLE) -> str:
    """``src-<hash>`` exactly as the publish workflow computes it (object IDs, one per line, sha256, 16 hex)."""
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
        raise ImageError('the image inputs have uncommitted changes; commit and push them, wait for the publish run')
    tag = content_tag(runner, root, image=image)
    digest = published_digest(runner, tag, image.repository)
    if not digest:
        raise ImageError(f'{REGISTRY}/{image.repository}:{tag} is not published yet; push, then wait for the '
                         f'"{image.workflow}" run after Validate passes')
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


def commit_and_push(runner: Runner, root: Path, tag: str, expect: str | None, image: Image = CONSOLE,
                    files: list[Path] | None = None) -> str:
    """Commit the pin and push it to main; one rebase-and-retry if main moved. Returns what happened."""
    git = ['git', '-C', str(root)]
    runner.run([*git, 'commit', '--quiet', '-m', f'deploy: {image.name} image {tag} (publish workflow)', '--',
                *[str(path) for path in (files or [RELEASE])]], mutating=True)
    if runner.run([*git, 'push', 'origin', 'HEAD:main'], mutating=True, check=False).returncode == 0:
        return 'pushed'
    runner.run([*git, 'pull', '--rebase', '--quiet', 'origin', 'main'], mutating=True)
    if expect and content_tag(runner, root, image=image) != expect:
        return 'skipped: main moved to other image inputs while pushing; their own run pins them'
    runner.run([*git, 'push', 'origin', 'HEAD:main'], mutating=True)
    return 'pushed after rebasing'


def main(argv: list[str] | None = None, runner: Runner | None = None, root: Path = ROOT,
         image: Image = CONSOLE) -> int:
    command = 'console-image' if image is CONSOLE else 'worker-image'
    parser = argparse.ArgumentParser(prog=f'swhurl {command}', description=__doc__.split('\n\n')[0])
    parser.add_argument('--expect', help='only pin if HEAD builds this src-<hash> tag (the publish workflow)')
    parser.add_argument('--commit', action='store_true', help='commit the pin and push it to main (the publish workflow)')
    args = parser.parse_args(argv or [])
    runner = runner or Runner.from_environment()
    try:
        if args.expect and (current := content_tag(runner, root, image=image)) != args.expect:
            print(f'[OK] nothing to do: main now builds {current}, not {args.expect}; its own publish run pins it')
            return 0
        tag, digest, changed = pin_release(runner, root, image)
        if not changed:
            print(f'[OK] already pinned to {tag}@{digest}')
            return 0
        files = ', '.join(str(path) for path in changed)
        if runner.dry_run:
            print(f'  would pin {files} to {tag}@{digest}')
            return 0
        print(f'[OK] pinned {files} to {tag}@{digest}')
        if args.commit:
            print(f'[OK] {commit_and_push(runner, root, tag, args.expect, image, changed)}')
        else:
            print('[INFO] Next: commit, push, make flux-reconcile')
        return 0
    except (ImageError, CommandError, KeyError, TypeError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1


def worker_main(argv: list[str] | None = None, runner: Runner | None = None, root: Path = ROOT) -> int:
    """``worker-image``: pin the incident reviewer to the analysis worker image published for this checkout."""
    return main(argv, runner, root, WORKER)
