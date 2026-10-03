"""check-templates: every stack template, in every combination of its questions, still fits the platform.

For each stack in ``contract.STACKS``: clone its template once, read the choice questions from its
copier.yml, render every combination with Copier (as ``make app-repo`` does) and turn the rendered
``swhurl.yaml`` into a staging and a prod instance with ``app-new --manifest`` in a scratch root. Each
instance must pass the app policy, and the two must not drift apart (``make check-apps`` rules).

This is the platform half of a template's contract: the template's own CI builds and runs the app, this
check proves the platform accepts what the template declares, in seconds and without building anything.
"""
from __future__ import annotations

import argparse
import itertools
import shutil
import tempfile
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

from swhurl.apps import new, policy, promotion, repo
from swhurl.apps.contract import MANIFEST_FILE, STACKS
from swhurl.apps.yaml_file import EditError
from swhurl.report import Report
from swhurl.run import CommandError, Runner

NAME = 'contract-check'
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
            code = new.main([NAME, '--root', str(root), '--manifest', str(manifest), '--env', env,
                             '--image', IMAGE, '--no-policy-check'])
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
    runner.run([*repo.copier_command(), 'copy', '--defaults', '--quiet', '--trust', *data, str(template), str(rendered)])
    manifest = rendered / MANIFEST_FILE
    if runner.dry_run:
        return True
    if not manifest.is_file():
        report.bad(f'{label}: the template rendered no {MANIFEST_FILE}')
        return False
    instances = []
    for env in ('staging', 'prod'):
        code, output = instance(root, manifest, env)
        if code:
            report.bad(f'{label}: app-new refused its {MANIFEST_FILE} for {env}: {output.strip()}')
            return False
        instances.append(root / 'apps' / NAME / env)
    problems = [f'{path.name}: {p}' for path in instances for p in policy.evaluate(path, runner)]
    problems += policy.drift(sorted(instances, key=lambda e: e.name != 'prod'))
    if problems:
        report.bad(f'{label}: ' + '; '.join(problems))
        return False
    report.ok(f'{label}: staging and prod pass the app policy')
    return True


def check(runner: Runner, report: Report, stacks: dict[str, str] = STACKS) -> int:
    work = Path(tempfile.mkdtemp(prefix='check-templates-'))
    failed = 0
    try:
        for stack, source in stacks.items():
            template = work / f'template-{stack}'
            runner.run(['git', 'clone', '--quiet', '--depth', '1', f'https://github.com/{source}.git', str(template)])
            questions = [] if runner.dry_run else repo.parse_questions((template / 'copier.yml').read_text())
            for answers in combinations(questions):
                try:
                    ok = check_combination(runner, report, template, stack, answers, work)
                except (CommandError, EditError) as error:
                    report.bad(f'{stack} {answers}: {error}')
                    ok = False
                failed += not ok
    finally:
        shutil.rmtree(work, ignore_errors=True)
    return 1 if failed else 0


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl check-templates', description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--stack', choices=sorted(STACKS), help='only this stack')
    args = parser.parse_args(argv)
    stacks = {args.stack: STACKS[args.stack]} if args.stack else STACKS
    return check(runner or Runner(), report or Report(), stacks)
