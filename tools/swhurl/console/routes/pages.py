"""Pages that show the platform and its apps (Overview, Apps, Catalogue, Platform, Egress, a Flux unit) and the Flux unit actions."""
from __future__ import annotations

from collections import Counter

from starlette.requests import Request
from starlette.responses import PlainTextResponse, Response
from starlette.routing import Route

from swhurl.apps import contract
from swhurl.apps.instance import ENVIRONMENTS, Instance
from swhurl.console import actions, catalogue, cluster
from swhurl.console.context import STACK_NAMES, Context, dashboard_urls, in_place, moved, refusal, started


def routes(ctx: Context) -> list[Route]:
    runner, jobs, described, page, reading, open_prs, stacks = (
        ctx.runner, ctx.jobs, ctx.described, ctx.page, ctx.reading, ctx.open_prs, ctx.stacks)

    def overview(request: Request) -> Response:
        def read():
            checks, found, (prs, prs_error), rows = runner.together(
                lambda: cluster.platform_checks(runner), lambda: cluster.units(runner), open_prs, lambda: cluster.apps(runner))
            return {'checks': checks, 'units': found, 'apps': rows, 'problems': cluster.problems(checks, found),
                    'updating': cluster.updating(found),
                    'prs': prs, 'prs_error': prs_error, 'recent': [j for j in jobs.recent() if j.state != 'running'][:5]}
        return reading(request, 'overview.html', read)

    def apps(request: Request) -> Response:
        """Every app as a list (optionally one team's) or, with ?view=map, one environment as a map of who calls whom."""
        view, team = request.query_params.get('view', 'list'), request.query_params.get('team', '')
        env = request.query_params.get('env', 'staging')
        attention = request.query_params.get('show') == 'attention'

        def read():
            rows = cluster.apps(runner)
            summaries = cluster.summaries(rows)
            teams = sorted({summary.team for summary in summaries if summary.team})
            shown = [summary for summary in summaries if not team or summary.team == team]
            # What needs you comes first; within each group, by name.
            shown.sort(key=lambda summary: (all(row.state.key == 'healthy' for row in summary.envs.values()), summary.app))
            states = [row.state.key for summary in shown for row in summary.envs.values()]
            tally = {'healthy': states.count('healthy'), 'updating': states.count('updating'), 'apps': len(shown),
                     'attention': len(states) - states.count('healthy') - states.count('updating')}
            if attention:
                shown = [summary for summary in shown if any(row.state.key != 'healthy' for row in summary.envs.values())]
            return {'apps': shown, 'teams': teams, 'tally': tally, 'attention': attention, 'team': team,
                    'stacks': stacks([summary.app for summary in shown]) if view != 'map' else {},
                    'stack_names': STACK_NAMES, 'owner': contract.APP_OWNER,
                    'dashboards': dashboard_urls(runner) if view != 'map' else {}, 'view': 'map' if view == 'map' else 'list', 'env': env if env in ENVIRONMENTS else 'staging',
                    'map': cluster.app_map([row for row in rows if not team or row.team == team],
                                           env if env in ENVIRONMENTS else 'staging') if view == 'map' else None}
        return reading(request, 'apps.html', read)

    def catalogue_page(request: Request) -> Response:
        """What the apps of one environment offer each other: APIs and events, described where the app describes them."""
        env = request.query_params.get('env', 'staging')
        env = env if env in ENVIRONMENTS else 'staging'
        kind, team = request.query_params.get('kind', ''), request.query_params.get('team', '')

        def read():
            rows = cluster.apps(runner, states=False)
            found = catalogue.entries(rows, env, described)
            shown = [e for e in found if (not kind or e.kind == kind) and (not team or e.team == team)]
            # An app that only uses is listed while no kind is chosen: it offers neither.
            grouped = [g for g in catalogue.groups(rows, env, shown) if (g.entries or not kind) and (not team or g.team == team)]
            return {'entries': shown, 'groups': grouped, 'team_of': {g.app: g.team for g in catalogue.groups(rows, env, [])},
                    'teams': sorted({row.team for row in rows if row.instance.env == env and row.team}), 'env': env,
                    'kind': kind if kind in ('api', 'event') else '', 'team': team, 'owner': contract.APP_OWNER}
        return reading(request, 'catalogue.html', read)

    def platform(request: Request) -> Response:
        def read():
            checks, found = runner.together(lambda: cluster.platform_checks(runner), lambda: cluster.units(runner))
            layers = [(title, text, [u for u in found if u.layer == title])
                      for title, text in [(t, d) for _, t, d in cluster.LAYERS] + [(cluster.OTHER_LAYER, '')]]
            revisions = Counter(u.revision for u in found if u.revision).most_common(1)
            return {'checks': checks, 'units': found, 'problems': cluster.problems(checks, found), 'updating': cluster.updating(found),
                    'layers': [layer for layer in layers if layer[2]],
                    'groups': [(section, cluster.condensed(group)) for section, group in checks.sections
                               if section != cluster.FLUX_SECTION],
                    # What nearly every unit has applied: said once, so a row shows a revision only when it differs.
                    'revision': revisions[0][0] if revisions else ''}
        return reading(request, 'platform.html', read)

    def egress_page(request: Request) -> Response:
        """Every instance's port on the egress proxy and the hosts outside the cluster it is granted, by environment."""
        def read():
            rows = cluster.apps(runner, states=False)
            by_env = [(env, [row for row in rows if row.instance.env == env]) for env in ENVIRONMENTS]
            granted = [row for row in rows if row.connections.egress]
            return {'by_env': [(env, found) for env, found in by_env if found], 'instances': len(rows),
                    'granted': len(granted), 'hosts': len({host for row in granted for host in row.connections.egress})}
        return reading(request, 'egress.html', read)

    def unit(request: Request) -> Response:
        def read():
            found = {u.name: u for u in cluster.units(runner)}
            name = request.path_params['unit']
            if name not in found:
                raise LookupError(name)
            return {'u': found[name], 'app': Instance.of_unit(name),
                    'unit_jobs': [j for j in jobs.recent() if j.unit == name]}
        try:
            return reading(request, 'unit.html', read)
        except LookupError:
            return page(request, 'error.html', status_code=404, error='No Flux unit by that name.')

    def start(request: Request) -> Response:
        try:
            job = jobs.start(request.path_params['action'], request.path_params['unit'], request.state.identity)
        except (actions.ActionError, cluster.ReadError) as error:
            if in_place(request):
                return refusal(error)
            return page(request, 'error.html', status_code=409 if isinstance(error, actions.ActionError) else 502, error=str(error))
        return started(request, job)

    def healthz(_request: Request) -> Response:
        return PlainTextResponse('ok\n')

    return [Route('/', overview), Route('/apps', apps), Route('/services', moved('/apps?view=map')), Route('/catalogue', catalogue_page),
            Route('/platform', platform), Route('/egress', egress_page), Route('/units', moved('/platform#units')), Route('/units/{unit}', unit),
            Route('/healthz', healthz), Route('/units/{unit}/{action}', start, methods=['POST'])]
