"""Retain a tested PR image for one day and publish it when main has the same inputs.

The stack workflows call this internal command from a pinned platform checkout.
Only successful same-repository PR runs supply candidates. Missing, expired or
unverifiable candidates fall back to the ordinary build; publication errors fail.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tarfile
import tempfile
from datetime import UTC, datetime, timedelta
from pathlib import Path

from swhurl import ROOT
from swhurl.run import CommandError, Runner

SCHEMA = 1
PLATFORM = 'linux/amd64'
BUILDKIT = 'moby/buildkit:buildx-stable-1'
# Skopeo 1.22.3; OCI copies preserve the PR's runtime and build attestations.
# Pin an immutable upstream release: rolling stable tags are rebuilt and their old digests disappear.
SKOPEO = 'quay.io/skopeo/stable:v1.22.3-immutable@sha256:c0ee1f4edca5c01cb8d5611124f92f3cc47196ecab68aee5d0f90834e00574d5'
DIGEST = re.compile(r'^sha256:[0-9a-f]{64}$')
GIT_SHA = re.compile(r'^[0-9a-f]{40,64}$')
REPOSITORY = re.compile(r'^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$')
INDEX = 'application/vnd.oci.image.index.v1+json'
DAY = timedelta(days=1)


class Rebuild(Exception):
    """Reuse cannot be proved; the workflow must build normally."""


def hashed(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def file_hash(path: Path) -> str:
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256')
    return digest.hexdigest()


def image_digest(runner: Runner, reference: str) -> str:
    value = runner.output(['docker', 'buildx', 'imagetools', 'inspect', reference,
                           '--format', '{{.Manifest.Digest}}']).strip()
    if not DIGEST.fullmatch(value):
        raise Rebuild(f'cannot resolve {reference} to a digest')
    return value


def pin_dockerfile(runner: Runner, text: str) -> tuple[str, dict[str, str]]:
    """Resolve and pin external FROMs so a mutable tag cannot race the fingerprint."""
    aliases: set[str] = set()
    bases: dict[str, str] = {}
    lines = []
    for line in text.splitlines(keepends=True):
        if re.match(r'^\s*#\s*syntax\s*=', line, re.I):
            raise Rebuild('custom Dockerfile frontends require the ordinary build')
        if not re.match(r'^\s*FROM\s', line, re.I):
            lines.append(line)
            continue
        match = re.fullmatch(r'(\s*FROM\s+)([^\s$]+)(?:\s+AS\s+([\w.-]+))?\s*', line, re.I)
        if not match:
            raise Rebuild('dynamic FROMs or platform overrides require the ordinary build')
        prefix, reference, alias = match.groups()
        if reference.lower() not in aliases and reference.lower() != 'scratch':
            if reference not in bases:
                bases[reference] = image_digest(runner, reference)
            reference = reference.split('@')[0] + '@' + bases[reference]
        lines.append(prefix + reference + (f' AS {alias}' if alias else '') + '\n')
        if alias:
            aliases.add(alias.lower())
    if not lines or not bases:
        raise Rebuild('no external base image was resolved')
    return ''.join(lines), bases


def fingerprint(runner: Runner, root: Path, recipe: str, target: str) -> tuple[dict, str]:
    if runner.output(['git', '-C', root, 'status', '--porcelain', '--untracked-files=no']).strip():
        raise Rebuild('tracked build inputs changed after checkout')
    tree = runner.output(['git', '-C', root, 'rev-parse', 'HEAD^{tree}']).strip()
    if not GIT_SHA.fullmatch(tree):
        raise Rebuild('the checked-out source tree could not be identified')
    repository, version = recipe.split('@', 1)
    if not REPOSITORY.fullmatch(repository) or not re.fullmatch(r'v\d+\.\d+\.\d+', version):
        raise Rebuild('the build recipe must use an immutable template release')
    source = runner.json(['gh', 'api', f'repos/{repository}/contents/.github/workflows/app.yml?ref={version}'])
    recipe_sha = (source or {}).get('sha', '')
    if not GIT_SHA.fullmatch(recipe_sha):
        raise Rebuild('the shared build workflow could not be identified')
    pinned, bases = pin_dockerfile(runner, (root / 'Dockerfile').read_text())
    builder = image_digest(runner, BUILDKIT)
    inputs = {'schema': SCHEMA, 'tree': tree, 'recipe': recipe, 'recipe_sha': recipe_sha,
              'helper': hashed(Path(__file__).read_bytes() + (ROOT / 'tools/swhurl/run.py').read_bytes()),
              'platform': PLATFORM, 'target': target, 'bases': bases, 'builder': builder}
    key = hashed(json.dumps(inputs, sort_keys=True, separators=(',', ':')).encode())
    return {'inputs': inputs, 'key': key, 'builder_image': BUILDKIT + '@' + builder}, pinned


def archive_image(path: Path) -> tuple[str, str]:
    """Read the immutable OCI root and runtime config without extracting any paths."""
    with tarfile.open(path) as archive:
        def document(name: str, expected: str = '') -> dict:
            member = archive.getmember(name)
            if not member.isfile() or member.size > 2 * 1024 * 1024:
                raise Rebuild('invalid OCI metadata member')
            stream = archive.extractfile(member)
            if stream is None:
                raise Rebuild('missing OCI metadata')
            data = stream.read()
            if expected and 'sha256:' + hashed(data) != expected:
                raise Rebuild('OCI metadata digest mismatch')
            value = json.loads(data)
            if not isinstance(value, dict):
                raise Rebuild('OCI metadata must be an object')
            return value

        def blob(digest: str) -> dict:
            if not DIGEST.fullmatch(digest):
                raise Rebuild('invalid OCI descriptor digest')
            return document('blobs/sha256/' + digest.removeprefix('sha256:'), digest)

        roots = document('index.json').get('manifests', [])
        if len(roots) != 1:
            raise Rebuild('OCI archive must identify one image root')
        root = roots[0]
        manifest = blob(root['digest'])
        if manifest.get('mediaType') in (INDEX, 'application/vnd.docker.distribution.manifest.list.v2+json'):
            runtimes = [entry for entry in manifest.get('manifests', [])
                        if entry.get('platform', {}).get('os') == 'linux'
                        and entry.get('platform', {}).get('architecture') == 'amd64']
            if len(runtimes) != 1:
                raise Rebuild('OCI archive must identify one linux/amd64 runtime')
            manifest = blob(runtimes[0]['digest'])
        config = manifest['config']['digest']
        settings = blob(config)
        if (settings.get('os'), settings.get('architecture')) != ('linux', 'amd64'):
            raise Rebuild('OCI runtime platform mismatch')
        return root['digest'], config


def find_candidate(runner: Runner, repository: str, state: dict, work: Path,
                   now: datetime) -> Path | None:
    name = 'swhurl-tested-' + state['key']
    runs = runner.json(['gh', 'api',
                       f'repos/{repository}/actions/workflows/container.yml/runs?event=pull_request&status=success&per_page=20'])
    for run in (runs or {}).get('workflow_runs', []):
        if (run.get('event') != 'pull_request' or run.get('status') != 'completed'
                or run.get('conclusion') != 'success'
                or (run.get('head_repository') or {}).get('full_name', '').lower() != repository.lower()):
            continue
        run_id = run.get('id')
        if not isinstance(run_id, int) or run_id <= 0:
            continue
        artifacts = runner.json(['gh', 'api', f'repos/{repository}/actions/runs/{run_id}/artifacts?name={name}'])
        for artifact in (artifacts or {}).get('artifacts', []):
            if artifact.get('name') != name or artifact.get('expired'):
                continue
            created = datetime.fromisoformat(artifact['created_at'].replace('Z', '+00:00'))
            if not timedelta(0) <= now - created < DAY:
                continue
            candidate = Path(tempfile.mkdtemp(prefix='candidate-', dir=work))
            runner.run(['gh', 'run', 'download', str(run_id), '--repo', repository,
                        '--name', name, '--dir', candidate], mutating=True)
            proof = json.loads((candidate / 'proof.json').read_text())
            image = candidate / 'image.oci.tar'
            if (not isinstance(proof, dict) or image.is_symlink() or not image.is_file()
                    or proof.get('schema') != SCHEMA or proof.get('inputs') != state['inputs']
                    or proof.get('key') != state['key'] or proof.get('repository') != repository
                    or proof.get('run_id') != run_id or proof.get('archive_sha256') != file_hash(image)):
                raise Rebuild('retained image proof does not match the merged build inputs')
            digest, config = archive_image(image)
            if (digest, config) != (proof.get('image_digest'), proof.get('tested_config')):
                raise Rebuild('retained image differs from the image tested on the PR')
            state.update({'image_digest': digest, 'archive': str(image), 'candidate_run': run_id,
                          'archive_sha256': proof['archive_sha256']})
            return candidate
    return None


def prepare(runner: Runner, root: Path, recipe: str, target: str, work: Path,
            environment: dict[str, str], now: datetime) -> dict:
    state = {'reuse': False, 'retain': False, 'dockerfile': str(root / 'Dockerfile')}
    try:
        identified, pinned = fingerprint(runner, root, recipe, target)
        state.update(identified)
        dockerfile = work / 'Dockerfile.pinned'
        dockerfile.write_text(pinned)
        state['dockerfile'] = str(dockerfile)
        repository = environment.get('GITHUB_REPOSITORY', '').lower()
        if not REPOSITORY.fullmatch(repository):
            raise Rebuild('no app repository identity')
        state['repository'] = repository
        event_name = environment.get('GITHUB_EVENT_NAME')
        if event_name == 'pull_request':
            event = json.loads(Path(environment['GITHUB_EVENT_PATH']).read_text())
            pr = event.get('pull_request') or {}
            state['retain'] = ((pr.get('head', {}).get('repo') or {}).get('full_name', '').lower() == repository
                               and pr.get('base', {}).get('ref') == 'main')
        elif event_name == 'push' and environment.get('GITHUB_REF') == 'refs/heads/main':
            state['reuse'] = find_candidate(runner, repository, state, work, now) is not None
        state['reason'] = ('matching tested PR image' if state['reuse'] else
                           'build and retain after tests' if state['retain'] else 'no matching tested PR image')
    except (Rebuild, CommandError, OSError, ValueError, KeyError, TypeError, AttributeError, tarfile.TarError) as error:
        state.update(reuse=False, retain=False, reason=f'ordinary build: {error}')
    return state


def retain(runner: Runner, state: dict, archive: Path, output: Path, environment: dict[str, str]) -> dict:
    if not state.get('retain'):
        raise Rebuild('this run cannot retain a PR image')
    tested = runner.output(['docker', 'inspect', '--format', '{{.Id}}', 'app:smoke']).strip()
    digest, config = archive_image(archive)
    if not DIGEST.fullmatch(tested) or tested != config:
        raise Rebuild('the OCI export differs from the smoke-tested image')
    proof = {'schema': SCHEMA, 'inputs': state['inputs'], 'key': state['key'],
             'repository': state['repository'], 'run_id': int(environment['GITHUB_RUN_ID']),
             'archive_sha256': file_hash(archive), 'image_digest': digest, 'tested_config': tested}
    output.write_text(json.dumps(proof, sort_keys=True) + '\n')
    return proof


def load(runner: Runner, archive: Path) -> None:
    """Load the runtime for smoke tests without discarding the OCI provenance."""
    _, config = archive_image(archive)
    runner.run(['docker', 'run', '--rm', '--volume', f'{archive.parent}:/image:ro',
                '--volume', '/var/run/docker.sock:/var/run/docker.sock', SKOPEO, 'copy',
                '--override-os', 'linux', '--override-arch', 'amd64',
                f'oci-archive:/image/{archive.name}', 'docker-daemon:app:smoke'], mutating=True)
    actual = runner.output(['docker', 'inspect', '--format', '{{.Id}}', 'app:smoke']).strip()
    if actual != config:
        raise Rebuild('loaded smoke-test runtime differs from the retained OCI image')


def publish(runner: Runner, state: dict, reference: str, environment: dict[str, str]) -> str:
    if not state.get('reuse') or not DIGEST.fullmatch(state.get('image_digest', '')):
        raise Rebuild('this run has no verified retained image')
    expected = 'ghcr.io/' + state['repository'] + ':'
    if not reference.startswith(expected) or not re.fullmatch(r'\d+-[0-9a-f]{7,40}', reference[len(expected):]):
        raise Rebuild('the destination must be this app\'s normal deployment tag')
    archive = Path(state['archive'])
    if file_hash(archive) != state['archive_sha256']:
        raise Rebuild('retained archive changed before publication')
    auth = Path(environment.get('DOCKER_CONFIG', str(Path.home() / '.docker'))) / 'config.json'
    runner.run(['docker', 'run', '--rm', '--volume', f'{archive.parent}:/image:ro',
                '--volume', f'{auth}:/auth.json:ro', SKOPEO, 'copy', '--all', '--preserve-digests',
                '--authfile', '/auth.json', f'oci-archive:/image/{archive.name}', f'docker://{reference}'],
               mutating=True, secret_output=True)
    actual = image_digest(runner, reference)
    if actual != state['image_digest']:
        raise Rebuild('published image digest differs from the tested PR image')
    return actual


def outputs(path: Path | None, values: dict) -> None:
    if path:
        with path.open('a') as stream:
            for name, value in values.items():
                if isinstance(value, bool):
                    value = str(value).lower()
                if isinstance(value, (str, int)) and '\n' not in str(value):
                    stream.write(f'{name}={value}\n')


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('prepare', 'load', 'retain', 'publish'))
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--root', type=Path, default=Path.cwd())
    parser.add_argument('--recipe', default='')
    parser.add_argument('--target', default='')
    parser.add_argument('--archive', type=Path)
    parser.add_argument('--proof', type=Path)
    parser.add_argument('--image', default='')
    args = parser.parse_args(argv)
    if args.action == 'retain' and (args.archive is None or args.proof is None):
        parser.error('retain requires --archive and --proof')
    if args.action == 'load' and args.archive is None:
        parser.error('load requires --archive')
    runner = runner or Runner.from_environment()
    for key in ('GH_TOKEN', 'GITHUB_TOKEN'):
        runner.add_secret(os.environ.get(key))
    if runner.dry_run:
        print(f'[PLAN] {args.action} a tested PR image; write no artifacts or image tags')
        return 0
    output = Path(os.environ['GITHUB_OUTPUT']) if os.environ.get('GITHUB_OUTPUT') else None
    try:
        if args.action == 'prepare':
            args.state.parent.mkdir(parents=True, exist_ok=True)
            state = prepare(runner, args.root.resolve(), args.recipe, args.target, args.state.parent,
                            dict(os.environ), datetime.now(UTC))
            args.state.write_text(json.dumps(state, sort_keys=True) + '\n')
            outputs(output, {k: v for k, v in state.items() if k in
                             ('reuse', 'retain', 'dockerfile', 'builder_image', 'key', 'candidate_run', 'image_digest')})
            print('[OK] ' + runner.redact(state['reason']))
        elif args.action == 'load':
            load(runner, args.archive)
            print('[OK] Loaded the OCI runtime for smoke tests; original export is unchanged')
        elif args.action == 'retain':
            proof = retain(runner, json.loads(args.state.read_text()), args.archive, args.proof, dict(os.environ))
            outputs(output, {'artifact': 'swhurl-tested-' + proof['key']})
            print('[OK] Retained export matches the image tested on this PR')
        else:
            digest = publish(runner, json.loads(args.state.read_text()), args.image, dict(os.environ))
            outputs(output, {'digest': digest})
            print(f'[OK] Published the tested PR image: {args.image}@{digest}')
    except (Rebuild, CommandError, OSError, ValueError, KeyError, tarfile.TarError) as error:
        print('[ERROR] ' + runner.redact(str(error)), file=sys.stderr)
        return 1
    return 0
