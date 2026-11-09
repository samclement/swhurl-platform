"""A console change: run ``swhurl-v2 change`` in this pod, which opens the pull request, and keep its log.

The command downloads ``main`` at one commit, unpacks it into a scratch folder, applies the change there with v2's
own use cases, keeps only the files the kind owns, and writes the commit, branch and pull request through GitHub's
API. The console no longer downloads, snapshots or publishes anything: it names the form's choices as v2's flags,
streams the command's lines into the job, and takes the link from the last one (``Opened <url>``). A refusal is
v2's own words, and a change with nothing to carry makes no GitHub write.
"""
from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from swhurl.apps import repo
from swhurl.apps.calling import NOTIFY
from swhurl.apps.contract import APP_OWNER, APP_TYPES, STACKS, TEAMS_FILE, type_answers
from swhurl.apps.model import records
from swhurl.console import changes, v2
from swhurl.console.actions import ActionError, Job

PLAN = re.compile(r'(?m)^\*\*Merge plan:\*\* it (.+)\.$')


def merge_plan(body: str) -> str:
    """What a person needs before merging this pull request, from its own body: '' when merging is all."""
    found = PLAN.search(body or '')
    return found[1] if found else ''


def merge_mode(pr) -> str:
    """How an open console pull request merges: a person, and what it needs first when its body says."""
    plan = merge_plan(pr.body)
    return f'You merge it on GitHub; it {plan}' if plan else 'You merge it on GitHub'

EXPOSURE_LABELS = {
    'authenticated-web': ('Signed in', 'A web address under {domain}, behind Google sign-in: only the accounts on the sign-in list.'),
    'private': ('Private', 'No web address: only the apps granted a call to it can reach it. For APIs, workers and jobs.'),
}
"""The form's plain-language names for each exposure: (title, description)."""


def open_change(runner, github, job: Job, action: str, name: str, env: str, flags: tuple[str, ...],
                input: str | None = None, *, preface: str = '') -> str:
    """Run v2's change in this pod: it opens the pull request and prints its URL last. The console keeps the job's
    log and its link, never the change itself. ``input`` is a channel address's one way in."""
    argv = [v2.BINARY, 'change', '--kind', action]
    argv += ['--app', name, '--env', env] if changes.ACTIONS[action].envs else ['--team', name]
    argv += ['--requested-by', job.identity, '--repository', github.repo, *flags]
    if preface:
        argv += ['--preface', preface]
    opened = ''
    for line in runner.stream(argv, env={v2.TOKEN_ENV: github.token}, input=input):
        job.lines.append(line)
        if line.startswith('Opened '):
            opened = line.removeprefix('Opened ').strip()
    return opened


def open_new_app(runner, github, job: Job, chosen: NewApp, image: str) -> str:
    """The New app form's pull request: v2 creates the instance from the image the new repository published, then
    renders what the records derive, and the body says which repository and template the console made."""
    made, template = chosen.request, STACKS[chosen.stack]
    flags, input = changes.change_arguments('new', chosen.name, {'image': image, 'flags': chosen.flags})
    preface = (f'The console created https://github.com/{made.repo} from [{template}](https://github.com/{template}) '
               'and waited for its first image.\n\n')
    return open_change(runner, github, job, 'new', chosen.name, 'staging', flags, input, preface=preface)


NEW_APP = 'new-app'  # a job's resume record: the New app form's choices


@dataclass(frozen=True)
class NewApp:
    """The New app form's choices: the repository to create and the v2 settings for its staging
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
        if name == NOTIFY:
            raise ActionError(f'{NOTIFY} is reserved for the platform notification service')
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
        settings = [f'team={team}']
        exposure, host = form.get('exposure', '').strip() or APP_TYPES[app_type].exposure, form.get('host', '').strip()
        if host:
            raise ActionError('v2 derives the address from the app name and the exposure: it does not model a host')
        if exposure == 'public':
            raise ActionError('v2 does not model public exposure yet: choose signed-in, or private for an API, '
                              'worker or job')
        if exposure and answers.get('kind') == 'web':
            if exposure not in EXPOSURE_LABELS:
                raise ActionError(f'exposure must be one of {", ".join(records.EXPOSURES)}')
            settings.append(f'exposure={exposure}')
        chosen = changes.Connections.from_form(form)
        if callable_apps is not None and offered_events is not None:
            chosen.check_offered(callable_apps, offered_events)
        return cls(name, stack, description, answers, (*settings, *chosen.settings()))

    @classmethod
    def from_record(cls, record: Mapping[str, Any]) -> NewApp:
        """The choices a job kept; ``ValueError``, ``KeyError`` or ``TypeError`` if they cannot be used."""
        name, stack, flags = str(record['name']), str(record['stack']), tuple(str(flag) for flag in record['extra'])
        if not records.NAME_RE.match(name) or stack not in STACKS or not all('=' in flag for flag in flags):
            raise ValueError(name)
        if any(flag.startswith(('publish=', 'host=')) for flag in flags):
            raise ActionError('these choices are no longer supported: the image states what it publishes, and v2 '
                              'derives the address; start a new job')
        return cls(name, stack, str(record.get('description') or ''),
                   {str(k): str(v) for k, v in (record.get('answers') or {}).items()}, flags)

    @property
    def record(self) -> dict[str, Any]:
        return {'kind': NEW_APP, 'name': self.name, 'stack': self.stack, 'description': self.description,
                'answers': dict(self.answers), 'extra': list(self.flags)}

    @property
    def target(self) -> str:
        return changes.target('new', self.name, 'staging')

    @property
    def request(self) -> repo.Request:
        return repo.Request(self.name, self.stack, APP_OWNER, self.description, dict(self.answers))
