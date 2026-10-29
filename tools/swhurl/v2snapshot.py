"""Export one pinned revision of this repository as the JSON snapshot v2's shadow report reads.

Every document comes from Git's object database at that revision (`git show`), so a dirty working
tree, a later commit or another checkout changes nothing. The export carries records and object
identities only: SOPS-encrypted files are skipped, no manifest body is written out, and no secret is
read. It writes one named file and nothing else.
"""
from __future__ import annotations

import json
import sys
from dataclasses import dataclass

import yaml

from swhurl import ROOT, yamlio
from swhurl.run import Runner

USAGE = 'usage: v2-snapshot --revision REV --out FILE'

# The record that names an instance: v1 keeps a YAML record under apps/, and a v2-owned instance keeps its JSON
# pair under v2-records/. A record's suffix is also how it is parsed.
INSTANCE_RECORDS = ('instance.json', 'instance.yaml')
RECORDS = ('instance.yaml', 'release.yaml', 'helmrelease.yaml')
# A v2-owned instance's records are its own pair; its committed manifest is the compiled HelmRelease, which the
# export carries as a declared object rather than as a record.
OWNED_RECORDS = ('instance.json', 'release.json')
UNIT_ROOT = 'clusters/home/'
# An instance v2 owns keeps its records here, out of apps/ so v1 neither renders nor checks it, and its
# compiled output is v2's own root: both are read at the same revision as everything else.
OWNED_ROOT = 'v2-records'
OWNED_OUTPUT = 'v2-apps/instances'


class SnapshotError(Exception):
    """The snapshot could not be taken; the message names the path or revision, never content."""


@dataclass(frozen=True)
class Args:
    revision: str
    out: str


def main(argv: list[str], runner: Runner | None = None) -> int:
    try:
        args = parse(argv)
    except SnapshotError as exc:
        print(f'[ERROR] {exc}', file=sys.stderr)
        return 2
    runner = runner or Runner.from_environment(cwd=ROOT)
    if runner.dry_run:
        print(f'Plan (v2-snapshot): read revision {args.revision!r} from Git and write {args.out}')
        return 0
    try:
        revision = resolve(runner, args.revision)
        document = snapshot(runner, revision, tracked(runner, revision))
        write(args.out, document)
    except SnapshotError as exc:
        print(f'[ERROR] {exc}', file=sys.stderr)
        return 2
    except OSError as exc:
        print(f'[ERROR] cannot write {args.out}: {exc.strerror}', file=sys.stderr)
        return 1
    print(f'[OK] wrote {args.out}: revision {revision[:12]}, {len(document["instances"])} instance(s), '
          f'{len(document["residual"])} unit(s) without records')
    return 0


def parse(argv: list[str]) -> Args:
    """Two required flags, each given once; anything else is a usage error."""
    values = {}
    rest = list(argv)
    while rest:
        flag = rest.pop(0)
        if flag not in ('--revision', '--out') or not rest or flag in values:
            raise SnapshotError(USAGE)
        values[flag] = rest.pop(0)
    if set(values) != {'--revision', '--out'} or '' in values.values():
        raise SnapshotError(USAGE)
    return Args(values['--revision'], values['--out'])


def resolve(runner: Runner, revision: str) -> str:
    """The full commit hash one revision names, resolved once so every later read uses it."""
    resolved = runner.output(['git', 'rev-parse', '--verify', f'{revision}^{{commit}}']).strip()
    if len(resolved) != 40 or any(character not in '0123456789abcdef' for character in resolved):
        raise SnapshotError(f'not a commit: {revision}')
    return resolved


def tracked(runner: Runner, revision: str) -> list[str]:
    """The paths this export reads, from that revision's tree, sorted so the export is reproducible."""
    listed = runner.output(['git', 'ls-tree', '-r', '--name-only', revision, '--',
                            'apps', 'clusters/home', OWNED_ROOT, 'v2-apps'])
    return sorted(line for line in listed.splitlines() if line)


def snapshot(runner: Runner, revision: str, paths: list[str]) -> dict:
    documents = set(paths)
    instances = [instance_path(path) for path in paths
                 if path.count('/') == 3 and path.endswith(tuple('/' + name for name in INSTANCE_RECORDS))
                 and (path.startswith('apps/') or path.startswith(OWNED_ROOT + '/'))]
    duplicated = sorted({instance for instance in instances if instances.count(instance) > 1})
    if duplicated:
        # Both shapes for one instance is a contradiction, not a preference: the export refuses rather than
        # choosing one and letting the other go stale.
        raise SnapshotError(f'an instance keeps two record shapes: {duplicated[0][0]}/{duplicated[0][1]}')
    document = {'version': 1, 'revision': revision, 'instances': [], 'residual': []}
    for application, environment in sorted(instances):
        record_root, output_root = roots(paths, application, environment)
        records, missing = read_records(runner, revision, documents, application, environment, record_root)
        base = f'{output_root}/{application}/{environment}'
        objects = declared(runner, revision, documents, sorted(p for p in paths if p.startswith(base + '/')))
        objects += [object_of(records[role]) for role in ('helmrelease',) if object_of(records.get(role)) is not None]
        if unit := unit_object(runner, revision, documents, application, environment):
            objects.append(unit)
        document['instances'].append({
            'application': application, 'environment': environment,
            'format': 'v2' if record_root == OWNED_ROOT else 'v1',
            'records': records, 'missing': missing,
            'objects': sorted(objects, key=lambda entry: (entry['kind'], entry['namespace'], entry['name'])),
        })
    document['residual'] = residual(runner, revision, documents, paths, {f'app-{a}-{e}' for a, e in instances})
    return document


def instance_path(path: str) -> tuple[str, str]:
    """``apps/<app>/<env>/instance.yaml`` or ``v2-records/<app>/<env>/instance.yaml`` names one instance."""
    _, application, environment, _ = path.split('/')
    return application, environment


def roots(paths: list[str], application: str, environment: str) -> tuple[str, str]:
    """Where one instance's records and committed manifests are: v1's trees, or v2's for an instance v2 owns.

    A v2-owned instance is one whose JSON pair is under v2-records/, whatever else the tree holds."""
    if any(f'{OWNED_ROOT}/{application}/{environment}/{name}' in paths for name in OWNED_RECORDS):
        return OWNED_ROOT, OWNED_OUTPUT
    return 'apps', 'apps'


def read_records(runner: Runner, revision: str, documents: set[str], application: str, environment: str,
                 record_root: str = 'apps') -> tuple[dict, list[str]]:
    """The records as JSON. A record the revision does not hold is named, not invented.

    An app record may be v2's or v1's wherever the app still has a v1 instance, so it is looked for in both
    places; the instance's own records are read only from the root its instance record was found in. A v2-owned
    instance keeps its app record as JSON at the root (one per app) and its pair as JSON under its environment,
    and the manifest that declares its objects is its compiled HelmRelease."""
    sought = {f'{record_root}/{application}/app.yaml': 'app', f'apps/{application}/app.yaml': 'app'}
    if record_root == OWNED_ROOT:
        # The v2 app record is one per app, not per environment: it is the team owner for every environment.
        sought = {f'{OWNED_ROOT}/{application}/app.json': 'app', **sought}
    names = OWNED_RECORDS if record_root == OWNED_ROOT else RECORDS
    for name in names:
        sought[f'{record_root}/{application}/{environment}/{name}'] = name.rsplit('.', 1)[0]
    records, missing = {}, []
    for path, role in sought.items():
        if role in records:
            continue
        if path not in documents:
            if role != 'app':
                missing.append(path)
            continue
        text = runner.output(['git', 'show', f'{revision}:{path}'])
        try:
            records[role] = load_record(path, text) or {}
        except (yaml.YAMLError, ValueError):
            raise SnapshotError(f'a record is not {record_format(path)}: {path}') from None
    return records, missing


def record_format(path: str) -> str:
    """What a record's suffix says it is, so a diagnostic never pretends to know."""
    return 'JSON' if path.endswith('.json') else 'YAML'


def load_record(path: str, text: str):
    """One record, parsed by its suffix: v2's pair is JSON and v1's records are YAML."""
    return json.loads(text) if path.endswith('.json') else yamlio.load(text)


def declared(runner: Runner, revision: str, documents: set[str], paths: list[str]) -> list[dict]:
    """Every object the committed manifests in one instance's directory declare, as identities.

    SOPS-encrypted files are skipped: their content is not read, and their identities are not this
    export's to state. A manifest without a name declares no object.
    """
    objects = []
    for path in paths:
        if path.rsplit('/', 1)[-1] in RECORDS or not path.endswith(('.yaml', '.json')):
            continue
        if path.endswith('.sops.yaml'):
            continue
        if path not in documents:
            continue
        text = runner.output(['git', 'show', f'{revision}:{path}'])
        try:
            loaded = list(yamlio.load_all(text))
        except yaml.YAMLError:
            raise SnapshotError(f'a manifest is not YAML: {path}') from None
        for manifest in loaded:
            identity = object_of(manifest)
            if identity is not None:
                objects.append(identity)
    return sorted(objects, key=lambda entry: (entry['kind'], entry['namespace'], entry['name']))


def unit_object(runner: Runner, revision: str, documents: set[str], application: str, environment: str) -> dict | None:
    """The instance's own Flux unit: the object that applies everything else it declares."""
    for suffix in ('.yaml', '.json'):
        path = f'clusters/home/app-{application}-{environment}{suffix}'
        if path not in documents:
            continue
        try:
            return object_of(yamlio.load(runner.output(['git', 'show', f'{revision}:{path}'])))
        except yaml.YAMLError:
            raise SnapshotError(f'a unit is not YAML: {path}') from None
    return None


def object_of(manifest: object) -> dict | None:
    """The identity of one manifest, or None when it declares no Kubernetes object."""
    if not isinstance(manifest, dict) or not isinstance(manifest.get('kind'), str):
        return None
    metadata = manifest.get('metadata')
    name = metadata.get('name') if isinstance(metadata, dict) else None
    if not isinstance(name, str) or not name:
        return None
    namespace = metadata.get('namespace') if isinstance(metadata, dict) else None
    return {'apiVersion': str(manifest.get('apiVersion', '')), 'kind': manifest['kind'],
            'namespace': namespace if isinstance(namespace, str) else '', 'name': name}


def residual(runner: Runner, revision: str, documents: set[str], paths: list[str], known: set[str]) -> list[dict]:
    """Units that apply output no instance record explains: their objects are ownership evidence too."""
    found = []
    for path in paths:
        if not (path.startswith(UNIT_ROOT) and path.endswith(('.yaml', '.json'))):
            continue
        unit = path.removeprefix(UNIT_ROOT).rsplit('.', 1)[0]
        if not unit.startswith('app-') or unit in known or path not in documents:
            continue
        text = runner.output(['git', 'show', f'{revision}:{path}'])
        try:
            identity = object_of(yamlio.load(text))
        except yaml.YAMLError:
            raise SnapshotError(f'a unit is not YAML: {path}') from None
        if identity is not None:
            found.append({'unit': unit, 'object': identity})
    return sorted(found, key=lambda entry: entry['unit'])


def write(path: str, document: dict) -> None:
    """One file, deterministically encoded: sorted keys, no escaping surprises."""
    with open(path, 'w', encoding='utf-8') as out:
        json.dump(document, out, indent=2, sort_keys=True, ensure_ascii=False)
        out.write('\n')
