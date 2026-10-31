"""The New app page is closed; an interrupted creation can still be finished.

The console does not create a v2 instance. A new app arrives as `app-repo` (the repository and its
first image), `app-new` (v1's staging records) and `swhurl-v2 migrate` (v2's records), and the v1 records
and unit are deleted before Flux applies the v2 unit. The page says that, and nothing else.

A job that stopped after creating its repository may still be finished: `app-new` is decision 1b's kept
create path, and the instance it writes is the staging post that `migrate` consumes.
"""
from __future__ import annotations

from starlette.requests import Request
from starlette.responses import RedirectResponse, Response
from starlette.routing import Route

from swhurl.console import actions, proposal, repos, stages
from swhurl.console.context import Context

POINTER = ('Use `make app-repo NAME=<app> TYPE=<type> STACK=<stack>` for the repository, then '
           '`make app-new NAME=<app> ARGS="--from-repo samclement/<app> --env staging --team <team> --image <image>"`, '
           'then `swhurl-v2 migrate --snapshot <snapshot> --app <app> --env staging --profile v2/testdata/profile.json '
           '--source-repository samclement/<app> --source-commit <commit> --out .` and delete the v1 records and unit.')


def routes(ctx: Context) -> list[Route]:
    runner, github, app_repos, jobs, page, resumable = (
        ctx.runner, ctx.github, ctx.app_repos, ctx.jobs, ctx.page, ctx.resumable)

    def disabled(request: Request) -> Response:
        return ctx.page(request, 'new_disabled.html', pointer=POINTER)

    def refused(request: Request) -> Response:
        return ctx.page(request, 'error.html', status_code=409,
                        error='The console no longer creates apps. ' + POINTER)

    def new_app_work(chosen: proposal.NewApp):
        """Find the repository an earlier job created, wait for its image and open its platform pull request."""
        def work(job: actions.Job) -> None:
            client = repos.AppRepos(runner, app_repos)
            opener = {'opener': ctx.repo_opener} if ctx.repo_opener else {}
            try:
                image = repos.finish_app_repo(client, job, chosen.request, **opener)
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
                              new_app_work(proposal.NewApp.from_record(told)), resume=dict(told))
        except (KeyError, TypeError, ValueError):
            return page(request, 'error.html', status_code=409, error='The record of that job cannot be used to finish it; '
                        'add the app with the make app-new line in docs/apps.md.')
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        return RedirectResponse(f'/jobs/{job.id}', status_code=303)

    return [Route('/jobs/{id:int}/resume', resume_job, methods=['POST']),
            Route('/new', disabled), Route('/new/repo', refused, methods=['POST'])]
