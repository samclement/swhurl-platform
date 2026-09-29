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
"""
from __future__ import annotations

import argparse
import hashlib
import re
import sys
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.run import CommandError, Runner

REGISTRY = 'ghcr.io'
REPOSITORY = 'samclement/swhurl-console'
RELEASE = Path('platform/console/helmrelease.yaml')
INDEX_TYPES = ('application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json')


class ImageError(Exception):
    pass


def content_tag(runner: Runner, root: Path = ROOT, revision: str = 'HEAD') -> str:
    """``src-<hash>`` exactly as publish-console.yml computes it (object IDs, one per line, sha256, 16 hex)."""
    ids = [runner.output(['git', '-C', str(root), 'rev-parse', f'{revision}:{path}']).strip()
           for path in platform.CONSOLE_IMAGE_INPUTS]
    return 'src-' + hashlib.sha256(''.join(f'{i}\n' for i in ids).encode()).hexdigest()[:16]


def published_digest(runner: Runner, tag: str) -> str | None:
    """The index digest GHCR serves for ``tag``, or None if it is not published."""
    token = runner.json(['curl', '--silent', '--show-error', '--fail',
                         f'https://{REGISTRY}/token?scope=repository:{REPOSITORY}:pull'])['token']
    runner.add_secret(token)
    reply = runner.run(['curl', '--silent', '--show-error', '--head', '--config', '-',
                        *[a for t in INDEX_TYPES for a in ('--header', f'Accept: {t}')],
                        f'https://{REGISTRY}/v2/{REPOSITORY}/manifests/{tag}'],
                       input=f'header = "Authorization: Bearer {token}"\n', check=False)
    lines = reply.stdout.splitlines()
    if not lines or ' 200' not in lines[0]:
        return None
    for line in lines[1:]:
        name, _, value = line.partition(':')
        if name.strip().lower() == 'docker-content-digest':
            return value.strip()
    return None


def pin(text: str, tag: str, digest: str) -> str:
    """Replace the single image ``tag:`` and ``digest:`` lines, keeping everything else (comments included)."""
    for key, value in (('tag', tag), ('digest', digest)):
        text, count = re.subn(rf'^(\s+{key}: )\S+$', rf'\g<1>{value}', text, flags=re.M)
        if count != 1:
            raise ImageError(f'{RELEASE} must have exactly one image {key}: line (found {count})')
    return text


def pin_release(runner: Runner, root: Path) -> tuple[str, str, bool]:
    """Pin the release file to this checkout's image: (tag, digest, whether the file changed)."""
    if runner.output(['git', '-C', str(root), 'status', '--porcelain', '--', *platform.CONSOLE_IMAGE_INPUTS]).strip():
        raise ImageError('the image inputs have uncommitted changes; commit and push them, wait for the publish run')
    tag = content_tag(runner, root)
    digest = published_digest(runner, tag)
    if not digest:
        raise ImageError(f'{REGISTRY}/{REPOSITORY}:{tag} is not published yet; push, then wait for the '
                         '"Publish console image" run after Validate passes')
    path = root / RELEASE
    text = path.read_text()
    updated = pin(text, tag, digest)
    if updated != text and not runner.dry_run:
        path.write_text(updated)
    return tag, digest, updated != text


def commit_and_push(runner: Runner, root: Path, tag: str, expect: str | None) -> str:
    """Commit the pin and push it to main; one rebase-and-retry if main moved. Returns what happened."""
    git = ['git', '-C', str(root)]
    runner.run([*git, 'commit', '--quiet', '-m', f'deploy: console image {tag} (publish workflow)', '--', str(RELEASE)],
               mutating=True)
    if runner.run([*git, 'push', 'origin', 'HEAD:main'], mutating=True, check=False).returncode == 0:
        return 'pushed'
    runner.run([*git, 'pull', '--rebase', '--quiet', 'origin', 'main'], mutating=True)
    if expect and content_tag(runner, root) != expect:
        return 'skipped: main moved to other image inputs while pushing; their own run pins them'
    runner.run([*git, 'push', 'origin', 'HEAD:main'], mutating=True)
    return 'pushed after rebasing'


def main(argv: list[str] | None = None, runner: Runner | None = None, root: Path = ROOT) -> int:
    parser = argparse.ArgumentParser(prog='swhurl console-image', description=__doc__.split('\n\n')[0])
    parser.add_argument('--expect', help='only pin if HEAD builds this src-<hash> tag (the publish workflow)')
    parser.add_argument('--commit', action='store_true', help='commit the pin and push it to main (the publish workflow)')
    args = parser.parse_args(argv or [])
    runner = runner or Runner.from_environment()
    try:
        if args.expect and (current := content_tag(runner, root)) != args.expect:
            print(f'[OK] nothing to do: main now builds {current}, not {args.expect}; its own publish run pins it')
            return 0
        tag, digest, changed = pin_release(runner, root)
        if not changed:
            print(f'[OK] already pinned to {tag}@{digest}')
            return 0
        if runner.dry_run:
            print(f'  would pin {RELEASE} to {tag}@{digest}')
            return 0
        print(f'[OK] pinned {RELEASE} to {tag}@{digest}')
        if args.commit:
            print(f'[OK] {commit_and_push(runner, root, tag, args.expect)}')
        else:
            print('[INFO] Next: commit, push, make reconcile UNIT=platform-console')
        return 0
    except (ImageError, CommandError, KeyError, TypeError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
