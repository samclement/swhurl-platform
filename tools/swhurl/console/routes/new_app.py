"""The New app form and its job: create the app's repository, wait for its image and open the pull request that adds it."""
from __future__ import annotations

import time
from urllib.parse import parse_qs

from starlette.requests import Request
from starlette.responses import RedirectResponse, Response
from starlette.routing import Route

from swhurl.apps import contract, repo
from swhurl.console import actions, proposal, repos, stages
from swhurl.console.context import STACK_NAMES, Context

INCLUDED = {'frontend': 'Browser frontend', 'database': 'SQLite', 'api': 'API description', 'events': 'Events',
            'flags': 'Feature flags'}
"""What an app type includes, as the New app form names it: template answers it turns on, and EVERY_APP."""
EVERY_APP = ('events', 'flags')
"""Included in every app whatever its type (no template question)."""
INCLUDED_TEXT = {'frontend': 'a page built with Vite+ that the app serves at /, calling its own /api',
                 'database': 'a database file the platform keeps on a volume and backs up nightly',
                 'api': 'an OpenAPI document of its routes, so other apps can find and call it',
                 'events': 'an AsyncAPI document of the events it publishes, with an example event',
                 'flags': 'an example feature flag, set per environment from the console'}


def routes(ctx: Context) -> list[Route]:
    runner, github, app_repos, repo_opener, jobs, page, connectable, resumable = (
        ctx.runner, ctx.github, ctx.app_repos, ctx.repo_opener, ctx.jobs, ctx.page, ctx.connectable, ctx.resumable)
    platform_teams, platform_channels = ctx.platform_teams, ctx.platform_channels

    questions_cache: dict[str, tuple[float, list[repo.Question]]] = {}

    def stack_questions(stack: str) -> list[repo.Question]:
        """The stack template's questions (its copier.yml on GitHub), read at most every 5 minutes."""
        cached = questions_cache.get(stack)
        if cached is not None and time.monotonic() - cached[0] <= 300:
            return cached[1]
        found = repo.template_questions(stack, **({'opener': repo_opener} if repo_opener else {}))
        questions_cache[stack] = (time.monotonic(), found)
        return found

    def new_repo_form(request: Request, status_code: int = 200, error: str = '', form=None) -> Response:
        form = form or {}
        teams, teams_error = platform_teams()
        callable_apps, offered_events, connections_error = connectable()
        return page(request, 'new_repo.html', status_code=status_code,
                    app_types=contract.APP_TYPES, included=INCLUDED, included_text=INCLUDED_TEXT, every_app=EVERY_APP,
                    stack_types={stack: ' '.join(key for key, found in contract.APP_TYPES.items() if stack in found.stacks)
                                 for stack in contract.STACKS},
                    stacks=contract.STACKS, stack_names=STACK_NAMES, stack_text=contract.STACK_DESCRIPTIONS,
                    owner=contract.APP_OWNER, teams=teams, teams_error=teams_error,
                    team_channels=platform_channels()[1],
                    callable_apps=callable_apps, offered_events=offered_events, connections_error=connections_error,
                    chosen_calls=[n for n in form.get('calls', '').split(',') if n],
                    chosen_events=[n for n in form.get('subscribe', '').split(',') if n],
                    exposure_text=proposal.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN, form=form, error=error,
                    github=github, app_repos=app_repos)

    async def new_repo(request: Request) -> Response:
        fields = parse_qs((await request.body()).decode(), keep_blank_values=True)
        form = {k: v[0] for k, v in fields.items()}
        for ticked in ('calls', 'subscribe'):  # one checkbox per app or event
            form[ticked] = ','.join(value for value in fields.get(ticked, []) if value)
        if github is None or app_repos is None:
            return new_repo_form(request, 409, 'Both GitHub tokens must be configured (console-github Secret).', form)
        try:
            questions = stack_questions(form.get('stack', '').strip()) if form.get('stack', '').strip() in contract.STACKS else []
            callable_apps, offered_events, problem = connectable()
            if problem and (form['calls'] or form['subscribe']):
                raise actions.ActionError(f'cannot check the apps and events chosen: {problem}')
            chosen = proposal.NewApp.from_form(form, questions, platform_teams()[0], {row.instance.app for row in callable_apps},
                                              {name for name, _ in offered_events})
        except (actions.ActionError, repo.RepoError) as error:
            return new_repo_form(request, 400, str(error), form)
        try:
            job = jobs.submit(stages.NEW_APP_JOB, chosen.target, request.state.identity,
                              new_app_work(chosen, create=True), resume=chosen.record)
        except actions.ActionError as error:
            return new_repo_form(request, 409, str(error), form)
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    def new_app_work(chosen: proposal.NewApp, *, create: bool):
        """Create the app's repository (or, finishing an earlier job, find the one it created), wait for
        its image and open the platform pull request that adds its staging instance."""
        def work(job: actions.Job) -> None:
            client = repos.AppRepos(runner, app_repos)
            opener = {'opener': repo_opener} if repo_opener else {}
            try:
                image = (repos.create_app_repo(runner, client, job, chosen.request, **opener) if create
                         else repos.finish_app_repo(client, job, chosen.request, **opener))
            finally:
                client.close()
            job.link = proposal.open_new_app(runner, github, job, chosen, image)
        return work

    def resume_job(request: Request) -> Response:
        """Finish a new-app job that stopped after creating its repository: the same choices, no second repository."""
        found = jobs.get(request.path_params['id'])
        told = resumable(found) if found else None
        if told is None:
            return page(request, 'error.html', status_code=404, error='No such job, or nothing of it is left to finish.')
        if github is None or app_repos is None:
            return page(request, 'error.html', status_code=409, error='Both GitHub tokens must be configured (console-github Secret).')
        try:
            job = jobs.submit(stages.FINISH_JOB, found.unit, request.state.identity,
                              new_app_work(proposal.NewApp.from_record(told), create=False), resume=dict(told))
        except (KeyError, TypeError, ValueError):
            return page(request, 'error.html', status_code=409, error='The record of that job cannot be used to finish it; '
                        'add the app with the make app-new line in docs/apps.md.')
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    return [Route('/jobs/{id:int}/resume', resume_job, methods=['POST']),
            Route('/new', new_repo_form), Route('/new/repo', new_repo, methods=['POST'])]
