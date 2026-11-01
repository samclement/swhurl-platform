"""Changes through GitHub: download ``main``, run its own tooling, commit to a ``console/*`` branch, open a PR.

``main``'s tarball is unpacked into a work tree, the tree's own tooling (not this image's) makes and checks
the change, so it follows the rules CI will apply even when the console is older than ``main``, and the files
it added, changed or deleted become one commit (blobs, a tree, a commit and a ref) on a new ``console/*``
branch, then a PR, all through ``github.GitHubAPI``. The console never writes any ref but a new
``console/*`` branch.
"""
from __future__ import annotations

import base64
import hashlib
import io
import os
import shutil
import sys
import tarfile
import tempfile
from collections.abc import Mapping
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

from swhurl import yamlio
from swhurl.apps import repo
from swhurl.apps.contract import APP_OWNER, APP_TYPES, STACKS, TEAMS_FILE, type_answers
from swhurl.apps.model import records, store
from swhurl.apps.model.records import SETTINGS_FILE, Settings
from swhurl.console import merge, v2
from swhurl.console.actions import ActionError, Job
from swhurl.console.github import AUTO_MERGE_LABEL, BASE, GitHub, GitHubAPI
from swhurl.console.merge import ACTIONS
from swhurl.run import Runner

AUTHOR = ('swhurl console', 'console@users.noreply.github.com')

EXPOSURE_LABELS = {
    'authenticated-web': ('Signed in', 'A web address under {domain}, behind Google sign-in: only the accounts on the sign-in list.'),
    'public': ('Public', 'Anyone on the internet, no sign-in. Needs a host outside {domain}.'),
    'private': ('Private', 'No web address: only the apps granted a call to it can reach it. For APIs, workers and jobs.'),
}
"""The form's plain-language names for each exposure: (title, description)."""


def snapshot(root: Path) -> dict[str, tuple[str, str]]:
    """``{path: (mode, sha256)}`` for every file under ``root``."""
    files = {}
    for path in sorted(root.rglob('*')):
        if path.is_file():
            mode = '100755' if os.access(path, os.X_OK) else '100644'
            files[path.relative_to(root).as_posix()] = (mode, hashlib.sha256(path.read_bytes()).hexdigest())
    return files


def download(api: GitHubAPI, revision: str, workdir: Path) -> Path:
    """Unpack the repository at ``revision`` into ``workdir/repo``."""
    with tarfile.open(fileobj=io.BytesIO(api.call('GET', f'/tarball/{revision}').content), mode='r:gz') as tar:
        tar.extractall(workdir / 'src', filter='data')
    (top,) = (workdir / 'src').iterdir()  # GitHub wraps the tree in <owner>-<repo>-<sha>/
    return top.rename(workdir / 'repo')




def publish(api: GitHubAPI, job: Job, tree: Path, before: dict, after: dict, changed: list[str],
            head: str, branch: str, title: str, body: str, auto_merge: bool, plan: str) -> str:
    """Publish a prepared, judged proposal. No policy or preparation callbacks cross this boundary."""
    entries = []
    for path in changed:
        if path not in after:
            entries.append({'path': path, 'mode': before[path][0], 'type': 'blob', 'sha': None})
            continue
        content = base64.b64encode((tree / path).read_bytes()).decode()
        blob = api.json('POST', '/git/blobs', {'content': content, 'encoding': 'base64'})['sha']
        entries.append({'path': path, 'mode': after[path][0], 'type': 'blob', 'sha': blob})
    base_tree = api.json('GET', f'/git/commits/{head}')['tree']['sha']
    new_tree = api.json('POST', '/git/trees', {'base_tree': base_tree, 'tree': entries})['sha']
    message = f'[console] {title}\n\n{body}\n\nRequested-by: {job.identity}\n'
    commit = api.json('POST', '/git/commits', {'message': message, 'tree': new_tree, 'parents': [head],
                                               'author': {'name': AUTHOR[0], 'email': AUTHOR[1]}})['sha']
    job.lines.append(f'Committed {commit[:7]} [console] {title}')
    job.lines.append(f'Creating branch {branch}')
    api.call('POST', '/git/refs', {'ref': f'refs/heads/{branch}', 'sha': commit})
    note = ('\n\nMerges itself when Validate and the trusted merge checks pass (label `auto-merge`; '
            'remove it to merge by hand).') if auto_merge else f'\n\n**Merge plan:** {plan}'
    pr = api.json('POST', '/pulls', {'title': f'[console] {title}', 'head': branch, 'base': BASE,
                                     'body': f'{body}{note}\n\nRequested-by: {job.identity}'})
    url = pr['html_url']
    job.lines.append(f'Opened {url}')
    if auto_merge:
        try:
            api.call('POST', f"/issues/{pr['number']}/labels", {'labels': [AUTO_MERGE_LABEL]})
            job.lines.append('Labelled auto-merge: it merges itself when Validate passes')
        except ActionError as error:  # the PR stands; it just waits for a person
            job.lines.append(f'Could not label it auto-merge ({error}); merge it yourself')
    return url


def run_tool(runner: Runner, job: Job, clone: Path, command: str, argv: list[str], input: str | None = None,
             tool: str = 'python') -> None:
    """The clone's own tooling (``app-new``, ``app-promote``, ...) or v2's binary. ``input`` is a secret's
    one way in (never an argument, never in the job's lines). A python tool leaves no bytecode: every file it
    writes in the clone becomes part of the commit. A v2 refusal is shown in v2's own words."""
    if tool == 'v2':
        result = runner.run([v2.BINARY, *argv], cwd=clone, check=False)
        job.lines += (result.stdout + result.stderr).splitlines()
        if result.returncode:
            raise ActionError(v2.refusal(result))
        return
    result = runner.run([sys.executable, '-m', 'swhurl', command, *argv], cwd=clone, check=False, input=input,
                        env={'PYTHONPATH': str(clone / 'tools'), 'PYTHONDONTWRITEBYTECODE': '1'})
    job.lines += (result.stdout + result.stderr).splitlines()
    if result.returncode:
        raise ActionError(f'{command} refused this change (see above); nothing was written to GitHub')

def open_action(runner: Runner, github: GitHub, job: Job, action: str, name: str, env: str,
                commands: tuple[merge.Command, ...], *, preface: str = '', auto_merge: bool = True) -> str:
    """Prepare and judge sequentially, then publish only owned inputs. Failed preparation or reads
    make no GitHub writes; promotion readiness is checked once, last, before the first write."""
    api = GitHubAPI(runner, github)
    workdir = Path(tempfile.mkdtemp(prefix='console-'))
    try:
        head = api.json('GET', f'/git/ref/heads/{BASE}')['object']['sha']
        try:
            branch = merge.branch(action, name, env, head[:7])
        except ValueError as error:
            raise ActionError(str(error)) from None
        entry = ACTIONS[action]
        job.lines.append(f'Downloading {github.repo} {BASE} ({head[:7]})')
        tree = download(api, head, workdir)
        before = snapshot(tree)
        previous = entry.read(tree, name, env)
        for command in commands:
            argv = list(command.argv)
            if command.tool == 'v2':  # the commit and the reviewed revision come from the tree just downloaded
                expect = (v2.instance_revision(runner, tree, name, entry.target_env or env)
                          if command.name == 'promote' else '')
                argv = v2.resolve(argv, revision=head, expect=expect)
            run_tool(runner, job, tree, command.name, argv, input=command.input, tool=command.tool)
        if entry.kind == 'new-app':
            # A new app's PR carries no egress port: app-new renders one for CLI use; the gate allocates it on newest main.
            path = tree / 'apps' / name / 'staging' / SETTINGS_FILE
            settings = Settings.parse(yamlio.load(path.read_text()))
            settings = replace(settings, allocations=replace(settings.allocations, egress_port=None))
            path.write_text(store.text(settings.document()))
        proposed = entry.read(tree, name, env)
        after = snapshot(tree)
        statuses = {p: 'D' if p not in after else 'A' if p not in before else 'M'
                    for p in after.keys() | before.keys() if after.get(p) != before.get(p)}
        changed = sorted(p for p, status in statuses.items() if status in entry.keeps and entry.owns(name, env, p))
        if not changed:
            raise ActionError('the change left no files changed; nothing to propose')
        reason = merge.judge(action, name, env, changed, previous, proposed)
        encrypted = merge.encrypted_reason(action, name, env, {p: statuses[p] for p in changed}, tree)
        auto_merge = auto_merge and entry.gated and not (reason or encrypted)
        plan = ''
        for path in changed:
            job.lines.append(f'{statuses[path]} {path}')
        if not auto_merge:
            why = ' and '.join(dict.fromkeys(filter(None, (reason, encrypted)))) or 'needs manual review'
            plan = f'Manual merge required: this pull request {why}. Review the changes, then merge it yourself.'
            job.lines.append(f'Merge plan: {plan}')
        body = preface + 'Generated by the console with:\n\n    ' + ' && '.join(c.as_make() for c in commands)
        if runner.dry_run:
            job.lines.append(f'Dry run: would commit {len(changed)} file(s) to {branch}; no PR opened')
            return ''
        return publish(api, job, tree, before, after, changed, head, branch,
                       entry.title.format(name=name, env=env), body, auto_merge, plan)
    finally:
        api.client.close()
        shutil.rmtree(workdir, ignore_errors=True)


def open_new_app(runner: Runner, github: GitHub, job: Job, chosen: NewApp, image: str) -> str:
    made, template = chosen.request, STACKS[chosen.stack]
    commands = ACTIONS['new'].commands(chosen.name, 'staging', {'repo': made.repo, 'image': image, 'flags': chosen.flags})
    return open_action(runner, github, job, 'new', chosen.name, 'staging', commands,
                       preface=(f'The console created https://github.com/{made.repo} from [{template}](https://github.com/{template}) '
                                'and waited for its first image.\n\n'))


NEW_APP = 'new-app'  # a job's resume record: the New app form's choices


@dataclass(frozen=True)
class NewApp:
    """The New app form's choices: the repository to create and the app-new flags for its staging
    instance. ``record`` is what a job keeps to finish the work later, and ``from_record`` reads it back."""
    name: str
    stack: str
    description: str
    answers: Mapping[str, str]
    flags: tuple[str, ...]

    @classmethod
    def from_form(cls, form: Mapping[str, str], questions: list[repo.Question], teams: Mapping[str, str] | None,
                  callable_apps: set[str] | None = None, offered_events: set[str] | None = None) -> NewApp:
        """The form's ``type`` sets every template answer (contract.APP_TYPES) and the exposure staging
        starts with, unless the form chose one; ``questions`` are the stack template's own (its
        copier.yml), and a type answer the template no longer offers is refused. ``teams`` are the
        registered ones; the form's team must be among them. A worker gets no exposure or host: its
        swhurl.yaml makes it private. When the lists the form offered are given, a call or subscription
        outside them is refused."""
        name, stack = form.get('name', '').strip(), form.get('stack', '').strip()
        description = ' '.join(form.get('description', '').split())[:200]
        if not records.NAME_RE.match(name):
            raise ActionError('name must be a DNS label: lowercase letters, digits and hyphens, at most 40 characters')
        if stack not in STACKS:
            raise ActionError(f'stack must be one of {", ".join(sorted(STACKS))}')
        app_type = form.get('type', '').strip()
        try:
            answers = type_answers(app_type, stack)
        except ValueError as error:
            raise ActionError(str(error)) from None
        for question in questions:
            if question.name in answers and answers[question.name] not in question.choices:
                raise ActionError(f'the {stack} template no longer offers {question.name}={answers[question.name]} '
                                  f'(it offers {", ".join(question.choices)}): the {APP_TYPES[app_type].title} type needs updating')
        team = form.get('team', '').strip()
        if not teams:
            raise ActionError(f'no team is registered ({TEAMS_FILE}): add one on the Teams page first; every app belongs to a team')
        if team not in teams:
            raise ActionError(f'choose the team that owns the app: one of {", ".join(teams)} ({TEAMS_FILE})')
        flags = [f'--team={team}']
        exposure, host = form.get('exposure', '').strip() or APP_TYPES[app_type].exposure, form.get('host', '').strip()
        if answers.get('kind') != 'web':
            if host:
                raise ActionError('a worker has no web address; leave Host empty')
        else:
            if exposure:
                if exposure not in records.EXPOSURES:
                    raise ActionError(f'exposure must be one of {", ".join(records.EXPOSURES)}')
                flags.append(f'--exposure={exposure}')
            if merge.checked_host(host):
                flags.append(f'--host={host}')
        chosen = merge.Connections.from_form(form)
        if callable_apps is not None and offered_events is not None:
            chosen.check_offered(callable_apps, offered_events)
        return cls(name, stack, description, answers, (*flags, *chosen.new_app_flags()))

    @classmethod
    def from_record(cls, record: Mapping[str, Any]) -> NewApp:
        """The choices a job kept; ``ValueError``, ``KeyError`` or ``TypeError`` if they cannot be used."""
        name, stack, flags = str(record['name']), str(record['stack']), tuple(str(flag) for flag in record['extra'])
        if not records.NAME_RE.match(name) or stack not in STACKS or not all(flag.startswith('--') for flag in flags):
            raise ValueError(name)
        if any(flag.startswith('--publish=') for flag in flags):
            raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by '
                              'provides.eventsFile, then start a new job')
        return cls(name, stack, str(record.get('description') or ''),
                   {str(k): str(v) for k, v in (record.get('answers') or {}).items()}, flags)

    @property
    def record(self) -> dict[str, Any]:
        return {'kind': NEW_APP, 'name': self.name, 'stack': self.stack, 'description': self.description,
                'answers': dict(self.answers), 'extra': list(self.flags)}

    @property
    def target(self) -> str:
        return merge.target('new', self.name, 'staging')

    @property
    def request(self) -> repo.Request:
        return repo.Request(self.name, self.stack, APP_OWNER, self.description, dict(self.answers))
