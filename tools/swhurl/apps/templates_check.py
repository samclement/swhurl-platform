"""check-templates: every stack template, in every combination of its questions, still fits the platform.

For each stack in ``contract.STACKS``: clone its template once, read the choice questions from its
copier.yml, render every combination with Copier (as ``make app-repo`` does) and turn the rendered
``swhurl.yaml`` into a staging and a prod instance with ``app-new --manifest`` in a scratch root. Each
instance must pass the app policy, and the two must not drift apart (``make check-apps`` rules).

Combinations run in parallel, one process each (``--jobs``, default every CPU): each is Copier, kustomize
and Helm processes in its own scratch folder.

This is the platform half of a template's contract: the template's own CI builds and runs the app, this
check proves the platform accepts what the template declares, in seconds and without building anything.
"""
from __future__ import annotations

import argparse
import itertools
import os
import shutil
import tempfile
from concurrent.futures import ProcessPoolExecutor
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

from swhurl.apps import declaration, new, policy, promotion, repo
from swhurl.apps.contract import (
    APP_TYPES,
    STACK_REVISIONS,
    STACKS,
    AppType,
    teams,
    type_answers,
)
from swhurl.apps.instance import ENVIRONMENTS, instance_folder
from swhurl.apps.model import shadow
from swhurl.apps.yaml_file import EditError
from swhurl.report import Report
from swhurl.run import CommandError, Runner

NAME = 'contract-check'
REFUSED = 'Validation error for question'  # how Copier says the template's validator refused an answer
# A template app's staging image (auto-deploy needs <run>-<sha>) pinned with a digest, as prod requires.
IMAGE = f'ghcr.io/samclement/{NAME}:1-abcdef0@sha256:' + '0' * 64


def combinations(questions: list[repo.Question]) -> list[dict[str, str]]:
    """Every combination of the questions' choices, in file order (one empty combination if there are none)."""
    return [dict(zip([q.name for q in questions], picked, strict=True))
            for picked in itertools.product(*[q.choices for q in questions])]


def instance(root: Path, manifest: Path, env: str) -> tuple[int, str]:
    """``app-new --manifest`` into ``root`` without its own policy run; returns (exit code, its output)."""
    out = StringIO()
    with redirect_stdout(out), redirect_stderr(out):
        if env == 'staging':
            # A template names no team (the creator chooses one): any registered team serves the check.
            code = new.main([NAME, '--root', str(root), '--manifest', str(manifest), '--env', env,
                             '--image', IMAGE, '--no-policy-check', '--team', next(iter(teams()), '')])
        else:
            source, _ = promotion.read(root, NAME)
            promotion.create(root, NAME, source)
            code = 0
    return code, out.getvalue()


def check_combination(runner: Runner, report: Report, template: Path, stack: str, answers: dict[str, str],
                      work: Path) -> bool:
    label = f"{stack} {' '.join(f'{k}={v}' for k, v in answers.items()) or '(no questions)'}"
    rendered, root = work / 'app', work / 'root'
    for path in (rendered, root):
        shutil.rmtree(path, ignore_errors=True)
    (root / 'clusters/home').mkdir(parents=True)
    (root / 'clusters/home/kustomization.yaml').write_text('resources: []\n')
    data = ['--data', f'app_name={NAME}'] + [arg for k, v in sorted(answers.items()) for arg in ('--data', f'{k}={v}')]
    command = [*repo.copier_command(), 'copy', '--defaults', '--quiet', '--trust', *data, str(template), str(rendered)]
    rendered_by = runner.run(command, check=False)
    said = f'{rendered_by.stderr or ""}{rendered_by.stdout or ""}'
    if rendered_by.returncode and REFUSED in said:
        # The template's own validator refuses the combination (a job with a database): no app can have it.
        why = [line for line in said.splitlines() if REFUSED in line][-1].split(':', 2)[-1].strip()
        report.info(f'{label}: the template refuses it ({why})')
        return True
    if rendered_by.returncode:
        raise CommandError(tuple(command), f'copier copy exited {rendered_by.returncode}: {said.strip()[-400:]}',
                           rendered_by.returncode)
    manifest = rendered / declaration.FILE
    if runner.dry_run:
        return True
    if not manifest.is_file():
        report.bad(f'{label}: the template rendered no {declaration.FILE}')
        return False
    instances = []
    for env in ENVIRONMENTS:
        code, output = instance(root, manifest, env)
        if code:
            report.bad(f'{label}: app-new refused its {declaration.FILE} for {env}: {output.strip()}')
            return False
        instances.append(instance_folder(root, NAME, env))
    # promotion.create has run the policy on prod, exactly as it wrote it; only staging is left.
    problems = [f'{instances[0].name}: {p}' for p in policy.evaluate(instances[0], runner)]
    problems += policy.drift(sorted(instances, key=lambda e: e.name != 'prod'))
    problems += [f'app model: {p}' for p in shadow.problems(root)]
    if problems:
        report.bad(f'{label}: ' + '; '.join(problems))
        return False
    report.ok(f'{label}: staging and prod pass the app policy')
    return True


def combination_job(runner: Runner, template: Path, stack: str, answers: dict[str, str],
                    work: Path) -> tuple[bool, list[tuple[str, str]]]:
    """One combination in its own scratch folder; returns whether it passed and its (level, message) results."""
    part = Report(StringIO())
    try:
        ok = check_combination(runner, part, template, stack, answers, work)
    except (CommandError, EditError) as error:
        part.bad(f'{stack} {answers}: {error}')
        ok = False
    return ok, [(entry.level, entry.message) for entry in part.entries]


def check(runner: Runner, report: Report, stacks: dict[str, str] = STACKS, jobs: int = 1,
          types: dict[str, AppType] = APP_TYPES) -> int:
    """``jobs`` above 1 runs the combinations in that many processes (a FakeRunner needs 1: it records in-process).
    Each app type built with a stack must answer that template's questions with choices it offers."""
    work = Path(tempfile.mkdtemp(prefix='check-templates-'))
    failed = 0
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
            for key, app_type in types.items():
                if stack not in app_type.stacks or runner.dry_run:
                    continue
                try:
                    repo.check_answers(type_answers(key, stack), questions)
                    report.ok(f'{stack}: the template offers every answer of the {app_type.title} type')
                except repo.RepoError as error:
                    report.bad(f'{stack}: the {app_type.title} type ({key}) no longer fits the template: {error}')
                    failed += 1
            todo = [(runner, template, stack, answers, work / f'{stack}-{i}')
                    for i, answers in enumerate(combinations(questions))]
            if jobs > 1 and not runner.dry_run:
                with ProcessPoolExecutor(max_workers=jobs) as pool:
                    results = list(pool.map(combination_job, *zip(*todo, strict=True)))
            else:
                results = [combination_job(*args) for args in todo]
            for ok, entries in results:  # in combination order, whichever finished first
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
    parser.add_argument('--jobs', type=int, default=os.cpu_count() or 1, help='combinations at once (default: every CPU)')
    args = parser.parse_args(argv)
    stacks = {args.stack: STACKS[args.stack]} if args.stack else STACKS
    return check(runner or Runner(), report or Report(), stacks, args.jobs if runner is None else 1)
