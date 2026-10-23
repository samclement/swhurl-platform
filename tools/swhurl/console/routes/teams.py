"""The Teams page and its changes: teams and their notification channels, each as a pull request."""
from __future__ import annotations

from urllib.parse import parse_qs

from starlette.requests import Request
from starlette.responses import Response
from starlette.routing import Route

from swhurl.console import actions, cluster, merge
from swhurl.console.context import STACK_NAMES, Context, dashboard_urls


def routes(ctx: Context) -> list[Route]:
    runner, github, jobs, page, reading, stacks, propose = ctx.runner, ctx.github, ctx.jobs, ctx.page, ctx.reading, ctx.stacks, ctx.propose
    platform_teams, platform_channels, teams_cache, channels_cache = (
        ctx.platform_teams, ctx.platform_channels, ctx.teams_cache, ctx.channels_cache)

    def teams_page(request: Request, status_code: int = 200, error: str = '') -> Response:
        """Every team with the apps that belong to it, and the changes that keep the two in step."""
        def read():
            registered, registry_error = platform_teams()
            owned: dict[str, dict[str, dict[str, cluster.AppRow]]] = {}  # team, app, environment
            for row in cluster.apps(runner):
                owned.setdefault(row.team, {}).setdefault(row.instance.app, {})[row.instance.env] = row
            # Apps whose team is not registered (or that carry none) are shown too: they are what a
            # careless change would leave behind, and the list says how to bring them back.
            unregistered = {team: apps for team, apps in owned.items() if team not in registered}
            if registry_error:
                # The registry is unknown, so nothing can be called unregistered: show the teams the
                # apps name instead of raising a false alarm.
                registered = registered | {team: '' for team in owned if team}
                unregistered = {team: apps for team, apps in owned.items() if not team}
            latest: dict[str, actions.Job] = {}  # each team's newest job, shown in its row
            for job in jobs.recent():
                if job.unit.startswith('team:'):
                    latest.setdefault(job.unit[5:], job)
            channels, team_channels = platform_channels()
            return {'teams': registered, 'owned': owned, 'unregistered': unregistered, 'registry_error': registry_error,
                    'channels': channels, 'team_channels': team_channels,
                    'error': error, 'team_jobs': latest,
                    'stacks': stacks(sorted({app for apps in owned.values() for app in apps})),
                    'dashboards': dashboard_urls(runner),
                    'stack_names': STACK_NAMES}
        return reading(request, 'teams.html', read, status_code=status_code)

    async def change_team(request: Request) -> Response:
        """Add, edit or remove a team: a PR made by the clone's team-* command, which refuses anything
        that would leave an app without a registered team."""
        action = request.path_params['action']
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        try:
            key, name = f'team-{action}', form.get('name', '').strip()
            if key not in merge.ACTIONS or action not in ('add', 'edit', 'remove'):
                raise actions.ActionError('no such team change')
            commands = merge.ACTIONS[key].commands(name, '', form)
        except actions.ActionError as error:
            return teams_page(request, 400, str(error))
        return propose(request, key, name, '', commands, then=(teams_cache, channels_cache))  # the next page reads the registry again

    async def change_channel(request: Request) -> Response:
        """Give a team its notification channel (or a new address for it), or remove it: a PR made by
        the clone's team-channel command. The address goes to that command's standard input and is
        redacted from every message; it is never rendered back, logged or put in the pull request."""
        action = request.path_params['action']
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        runner.add_secret(form.get('address', '').strip())
        try:
            key, name = f'team-channel-{action}', form.get('name', '').strip()
            if key not in merge.ACTIONS:
                raise actions.ActionError('no such channel change')
            commands = merge.ACTIONS[key].commands(name, '', form)
        except actions.ActionError as error:
            return teams_page(request, 400, str(error))
        return propose(request, key, name, '', commands, then=(channels_cache,))

    return [Route('/teams', teams_page), Route('/teams/{action}', change_team, methods=['POST']),
            Route('/team-channel/{action}', change_channel, methods=['POST'])]
