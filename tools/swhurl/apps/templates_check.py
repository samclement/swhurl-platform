"""check-templates: each app type, rendered from its stack's template, still fits the platform.

For each stack in ``contract.STACKS``: clone its template at the pinned revision, render each app type
built with it (``contract.APP_TYPES``, the only answers the template accepts) with Copier, as ``make
app-repo`` does, run the image-declaration producer over the rendered app, and hand the label to
``swhurl-v2 declaration``. That command decodes the label and applies the creation rules an instance of it
would face, so what a build will produce is proved with no image, no registry and no write. One set of
answers no type has (``OUTSIDE_TYPES``) must be refused by the template's validator.

Renders and checks run in parallel, one process each (``--jobs``, default every CPU).

This is the platform half of a template's contract: the template's own CI builds and runs each type, this
check proves the platform accepts what the template declares, in seconds and without building anything.

``SWHURL_V2`` names the ``swhurl-v2`` binary (default: the one on PATH). Build it with
``cd v2 && go build -o /tmp/swhurl-v2 ./cmd/swhurl-v2``.
"""
from __future__ import annotations

import argparse
import os
import shutil
import tempfile
from concurrent.futures import ProcessPoolExecutor
from io import StringIO
from pathlib import Path

from swhurl.apps import declaration, image_declaration, repo
from swhurl.apps.contract import (
    APP_TYPES,
    STACK_REVISIONS,
    STACKS,
    AppType,
    teams,
    type_answers,
)
from swhurl.report import Report
from swhurl.run import CommandError, Runner

NAME = 'contract-check'
REFUSED = 'Validation error for question'  # how Copier says the template's validator refused an answer
# A worker with a database: no app type has it, so the template's validator must refuse it.
OUTSIDE_TYPES = {'kind': 'worker', 'database': 'sqlite'}


def binary() -> str:
    """The v2 binary that applies the creation rules, as the check's environment names it."""
    return os.environ.get('SWHURL_V2') or shutil.which('swhurl-v2') or 'swhurl-v2'


def declaration_label(rendered: Path) -> str:
    """The label a build of this rendered app writes, from the producer the templates call."""
    return image_declaration.label_of(rendered)


def label_of(stack: str, answers: dict[str, str]) -> str:
    return f"{stack} {' '.join(f'{k}={v}' for k, v in sorted(answers.items()))}"


def render_combination(runner: Runner, template: Path, stack: str, answers: dict[str, str],
                       work: Path) -> tuple[str, str | bytes]:
    """Copier, as ``make app-repo`` runs it, for one set of answers: ``('manifest', its swhurl.yaml)``, ``('refused', why)``,
    ``('missing', '')`` or ``('error', what failed)``."""
    rendered = work / 'app'
    shutil.rmtree(rendered, ignore_errors=True)
    data = ['--data', f'app_name={NAME}'] + [arg for k, v in sorted(answers.items()) for arg in ('--data', f'{k}={v}')]
    command = [*repo.copier_command(), 'copy', '--defaults', '--quiet', '--trust', *data, str(template), str(rendered)]
    try:
        rendered_by = runner.run(command, check=False)
    except CommandError as error:
        return 'error', str(error)
    said = f'{rendered_by.stderr or ""}{rendered_by.stdout or ""}'
    if rendered_by.returncode and REFUSED in said:
        # The template's own validator refuses answers that are not an app type.
        return 'refused', [line for line in said.splitlines() if REFUSED in line][-1].split(':', 2)[-1].strip()
    if rendered_by.returncode:
        return 'error', f'copier copy exited {rendered_by.returncode}: {said.strip()[-400:]}'
    manifest = rendered / declaration.FILE
    if runner.dry_run:
        return 'manifest', b''
    return ('manifest', rendered) if manifest.is_file() else ('missing', '')


def check_manifest(runner: Runner, report: Report, label: str, rendered: Path, work: Path) -> bool:
    """The platform half for one rendered app: the producer's label, and the creation rules v2 applies to it."""
    try:
        text = declaration_label(rendered)
    except image_declaration.LabelError as error:
        report.bad(f'{label}: the image-declaration producer refused the app: {error}')
        return False
    label_path = work / 'label.txt'
    shutil.rmtree(work / 'root', ignore_errors=True)
    work.mkdir(parents=True, exist_ok=True)
    label_path.write_text(text + '\n')
    # A template names no team (the creator chooses one): any registered team serves the check.
    result = runner.run([binary(), 'declaration', '--label', str(label_path), '--app', NAME,
                         '--team', next(iter(teams()), ''), '--exposure', 'private'], check=False)
    if result.returncode:
        said = (result.stderr or result.stdout or '').strip()
        if said.startswith('unsupported:'):
            # A kind v2 does not model yet (the job kind): the label is what the platform will refuse by name when
            # someone tries to create it, so the check reports the gap instead of failing the template.
            report.warn(f'{label}: v2 does not model it yet ({said})')
            return True
        report.bad(f'{label}: swhurl-v2 declaration refused the label: {said}')
        return False
    report.ok(f'{label}: the label applies to a new instance')
    return True


def manifest_job(runner: Runner, label: str, rendered: Path, work: Path) -> tuple[bool, list[tuple[str, str]]]:
    """One rendered app in its own scratch folder; returns whether it passed and its (level, message) results."""
    part = Report(StringIO())
    try:
        ok = check_manifest(runner, part, label, rendered, work)
    except CommandError as error:
        part.bad(f'{label}: {error}')
        ok = False
    return ok, [(entry.level, entry.message) for entry in part.entries]


def check(runner: Runner, report: Report, stacks: dict[str, str] = STACKS, jobs: int = 1,
          types: dict[str, AppType] = APP_TYPES) -> int:
    """``jobs`` above 1 runs renders and checks in that many processes (a FakeRunner needs 1: it records in-process).
    Each app type built with a stack must answer that template's questions with choices it offers, render, and
    pass; ``OUTSIDE_TYPES`` must be refused."""
    work = Path(tempfile.mkdtemp(prefix='check-templates-'))
    failed = 0

    def run(job, todo):
        if jobs > 1 and not runner.dry_run and len(todo) > 1:
            with ProcessPoolExecutor(max_workers=jobs) as pool:
                return list(pool.map(job, *zip(*todo, strict=True)))
        return [job(*args) for args in todo]
    try:
        for stack, source in stacks.items():
            template = work / f'template-{stack}'
            runner.run(['git', 'clone', '--quiet', '--depth', '1', f'https://github.com/{source}.git', str(template)])
            runner.run(['git', 'fetch', '--quiet', '--depth', '1', 'origin', STACK_REVISIONS[stack]], cwd=template)
            runner.run(['git', 'checkout', '--quiet', '--detach', 'FETCH_HEAD'], cwd=template)
            # Copier renders a plain folder as it stands (0.7 s) but clones a Git one for every render (1.9 s);
            # the checkout above pins the revision, and only the answers file's _commit differs.
            shutil.rmtree(template / '.git', ignore_errors=True)
            questions = [] if runner.dry_run else repo.parse_questions((template / 'copier.yml').read_text())
            answered: list[tuple[str, dict[str, str]]] = []  # (label, answers) of each type built with this stack
            for key, app_type in types.items():
                if stack not in app_type.stacks:
                    continue
                answers = type_answers(key, stack)
                try:
                    if not runner.dry_run:
                        repo.check_answers(answers, questions)
                    answered.append((f'{label_of(stack, answers)} ({key})', answers))
                except repo.RepoError as error:
                    report.bad(f'{stack}: the {app_type.title} type ({key}) no longer fits the template: {error}')
                    failed += 1
            todo = [*answered, (label_of(stack, OUTSIDE_TYPES), OUTSIDE_TYPES)]
            rendered = run(render_combination, [(runner, template, stack, answers, work / f'{stack}-{i}')
                                                 for i, (_, answers) in enumerate(todo)])
            (outside_label, _), (outside, why) = todo[-1], rendered[-1]
            if outside == 'refused':
                report.ok(f'{outside_label}: the template refuses it ({why})')
            elif not runner.dry_run:
                report.bad(f'{outside_label}: no app type has these answers, but the template ' +
                           ('accepts them' if outside == 'manifest' else f'failed otherwise: {why}'))
                failed += 1
            checks = []
            for (label, _), (kind, found) in zip(answered, rendered, strict=False):
                if kind == 'manifest':
                    checks.append((runner, label, found, work / f'{stack}-check-{len(checks)}'))
                else:
                    report.bad(f'{label}: ' + {'missing': f'the template rendered no {declaration.FILE}',
                                               'refused': f'the template refuses it ({found})'}.get(kind, str(found)))
                    failed += 1
            if runner.dry_run:
                continue
            for ok, entries in run(manifest_job, checks):  # in type order, whichever finished first
                for level, message in entries:
                    getattr(report, level)(message)
                failed += not ok
    finally:
        shutil.rmtree(work, ignore_errors=True)
    return 1 if failed else 0


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl check-templates', description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--stack', choices=sorted(STACKS), help='only this stack')
    parser.add_argument('--jobs', type=int, default=os.cpu_count() or 1, help='renders at once (default: every CPU)')
    args = parser.parse_args(argv)
    jobs = args.jobs if runner is None else 1
    runner, report = runner or Runner(), report or Report()
    stacks = {args.stack: STACKS[args.stack]} if args.stack else STACKS
    return check(runner, report, stacks, jobs)
