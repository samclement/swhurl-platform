"""An app instance's page and its changes: flags, promotion review, and the pull requests that scale, expose, connect or remove it."""
from __future__ import annotations

import json
import logging
from urllib.parse import parse_qs

from starlette.requests import Request
from starlette.responses import RedirectResponse, Response
from starlette.routing import Route

from swhurl.apps import contract, ops
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.console import actions, changes, cluster, observation, promotion, proposal, stages, v2
from swhurl.console import github as gh
from swhurl.console.actions import ActionError
from swhurl.console.context import STACK_NAMES, Context, dashboard_urls, logs_url
from swhurl.run import CommandError

V2_SCALE_FIELDS = (('replicas', 'replicas', 'Replicas'), ('memory', 'memoryMiB', 'Memory (MiB)'))


def routes(ctx: Context) -> list[Route]:
    runner, github, jobs, page, open_prs, change_rows, stacks, connectable, propose = (
        ctx.runner, ctx.github, ctx.jobs, ctx.page, ctx.open_prs, ctx.change_rows, ctx.stacks, ctx.connectable,
        ctx.propose)
    document_source, request_files = ctx.document_source, ctx.request_files

    def read_app(request: Request) -> Response:
        found = Instance.named(request.path_params['app'], request.path_params['env'])
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        rows = ctx.apps()
        row = next((row for row in rows if row.instance == found), None)
        report = row.report if row else None
        if report is None:
            label = f'{found.app}/{found.env}'
            pending = [j for j in jobs.recent() if j.unit == label and j.action.startswith('new app')]
            prs, prs_error = open_prs()
            prs = [pr for pr in prs if stages.target_of(pr.branch) in (('new', label), ('promote', label))]
            if row:
                return page(request, 'pending.html', label=label, jobs=pending, prs=prs, prs_error=prs_error,
                            in_git=False, removing=True)
            if pending or prs:
                return page(request, 'pending.html', label=label, jobs=pending, prs=prs, prs_error=prs_error, in_git=False)
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        if report['live']['unit']['status'] == 'absent':
            return page(request, 'pending.html', label=f'{found.app}/{found.env}', jobs=[], prs=[], prs_error='', in_git=True)
        summary = next((a for a in cluster.summaries(rows) if a.app == found.app), None)
        promotion_problem = observation.promotion_problem(report) if found.env == 'staging' else ''
        row = summary.envs.get(found.env) if summary else None
        shown = row.connections if row else cluster.Connections()
        files = request_files.get()
        declared = cluster.Declared(row.inventory if row else None)
        card = flag_card(found)
        instance = json.loads(files(f'v2-records/{found.app}/{found.env}/instance.json') or '{}')
        desired_helm = files(f'v2-apps/instances/{found.app}/{found.env}/helmrelease.json')
        variables = ops.shown_variables(json.loads(desired_helm))[0] if desired_helm is not None else None
        teams, teams_error = registered_teams() if declared.declared else ({}, '')
        return page(request, 'app.html', i=found, report=report, desired_image=declared.image_reference,
                    settings=instance.get('configuration', {}), database=declared.database,
                    variables=variables, exposure=(instance.get('grants') or {}).get('exposure', ''), flag_rows=card['rows'], flag_problem=card['problem'], flags_writable=card['writable'],
                    writable=declared.declared and not declared.problem, v2=declared.declared,
                    flag_changed=request.query_params.get('flag', ''), team=row.team if row else '',
                    teams=teams, teams_error=teams_error,
                    connections=shown, relation_problem='', allows=declared.allows if declared.declared else None, connectable=connectable(found.env, found.app), logs=logs_url(found.app), dashboard=dashboard_urls(runner).get(found.app, ''),
                    stack=stacks([found.app]).get(found.app, ''), stack_names=STACK_NAMES, owner=contract.APP_OWNER,
                    scale_fields=V2_SCALE_FIELDS,
                    declared=declared, github=github,
                    promotion_problem=promotion_problem,
                    exposures=ops.EXPOSURE_LABELS, exposure_text=proposal.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN,
                    summary=summary, state=observation.state(report), unit_jobs=change_rows([j for j in jobs.recent() if j.unit in (found.unit, f'{found.app}/{found.env}')], others=False)[0])

    def effective(report: dict, name: str) -> object:
        """The value the write left the flag at, as v2 read it back: the card states what is stored, not what was asked."""
        row = next((row for row in report.get('rows') or [] if row.get('name') == name), {})
        return (row.get('override') or {}).get('value')

    def declared_of(app: str, env: str) -> cluster.Declared:
        """The instance's own records, as the Go document reports them: an instance it does not report has none."""
        row = next((r for r in ctx.apps() if r.instance == Instance.named(app, env)), None)
        return cluster.Declared(row.inventory if row else None)

    def registered_teams() -> tuple[dict[str, str], str]:
        """``(registered teams, problem)`` at the commit the page reads the records at; none on a problem."""
        if document_source is None:
            return {}, 'this console has no Git reader (no GitHub token)'
        try:
            text = document_source(contract.TEAMS_FILE.as_posix())
            return (contract.registered_teams(text), '') if text is not None else ({}, f'{contract.TEAMS_FILE} is not in Git')
        except Exception as error:  # noqa: BLE001 (GitHub unavailable or an unreadable registry: the form says so)
            return {}, f'cannot read {contract.TEAMS_FILE}: {error}'

    def v2_read_only(request: Request, app: str, env: str, change: str) -> Response | None:
        """The phase-1 refusal, kept for a console image without the binary: it cannot write v2 at all."""
        if v2.available(runner):
            return None
        label = f'{app}/{env}'
        command = {'remove': f'swhurl-v2 remove {label}', 'promote': f'swhurl-v2 promote {app}'}.get(
            change, f'swhurl-v2 edit {label} --set <field>=<value>')
        return page(request, 'error.html', status_code=409,
                    error=f'v2 writes {label}, and this console has no swhurl-v2 binary: run `{command}` in your own terminal.')

    def v2_arguments(change: str, app: str, form: dict,
                     declared: cluster.Declared) -> tuple[tuple[str, ...], str | None]:
        """``swhurl-v2 change``'s flags for one console change, after the console's own checks: what the form
        asks to add or remove, never the whole list, so a change merged but not yet applied is not undone."""
        action = changes.V2_ACTION[change]
        if change == 'allow':
            add = changes.v2_names(form.get('add', ''), changes.ALLOW_RE, 'an allowed caller is an app name or team:<team>')
            remove = set(changes.v2_names(form.get('remove', ''), changes.ALLOW_RE,
                                        'an allowed caller is an app name or team:<team>'))
            allows = ','.join(dict.fromkeys([*(name for name in declared.allows if name not in remove), *add]))
            if allows == ','.join(declared.allows):
                raise actions.ActionError('that changes nothing: name an app or team:<team> to allow or stop allowing')
        elif change == 'team':
            teams, problem = registered_teams()
            team = form.get('team', '').strip()
            if problem:
                raise actions.ActionError(f'cannot check the team chosen: {problem}')
            if team not in teams:
                raise actions.ActionError(f'choose a registered team: one of {", ".join(teams) or "none"}')
            if team == declared.team:
                raise actions.ActionError(f'{app} already belongs to {team}: that changes nothing')
        return changes.change_arguments(action, app, form)

    def flag_card(found: Instance) -> dict:
        """The Feature flags card, as `swhurl-v2 flags` reports it: every row, and why it could not read them all.

        v2 holds the declaration and the rules; this route passes on the document it printed, and its problem, in
        v2's own words. The declaration is the record's own copy of the image's offers document, so a page cannot show
        one commit's declaration beside another's rows."""
        _, root = request_files.get().inputs()
        answer = v2.run(runner, root, ['flags', '--records', v2.RECORDS, '--app', found.app,
                                       f'--env={found.env}', '--json'])
        return json.loads(answer.stdout)

    async def change_flag(request: Request) -> Response:
        """Set or clear one flag's override: written to NATS at once (no PR), and to the audit log."""
        app, env, name = (request.path_params[k] for k in ('app', 'env', 'flag'))
        found = Instance.named(app, env)
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        action = form.get('action', '')
        write = ['--set', f'{name}={form.get("value", "")}'] if action == 'set' else ['--clear', name]
        try:
            _, root = request_files.get().inputs()
            report = json.loads(v2.run(runner, root, ['flags', '--records', v2.RECORDS, '--app', app,
                                                      f'--env={env}', '--json', *write]).stdout)
        except ActionError as error:
            return page(request, 'error.html', status_code=400, error=f'{name}: {error}')
        identity = request.state.identity
        done = 'cleared' if action == 'clear' else f'= {json.dumps(effective(report, name))}'
        logging.getLogger('swhurl.console.audit').info(
            f'[AUDIT] {identity} flag {app}/{env} {name} {done}',
            extra={'identity': identity, 'action': 'flag', 'unit': f'{app}/{env}', 'state': 'done', 'flag': name})
        return RedirectResponse(f'/apps/{app}/{env}?flag={name}#flags', status_code=303)

    def app(request: Request) -> Response:
        try:
            return read_app(request)
        except (CommandError, cluster.ReadError, actions.ActionError, EditError, ValueError, TypeError, OSError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    def review_promotion(request: Request) -> Response:
        app = request.path_params['app']
        found = Instance.named(app, 'staging')
        if found is None or github is None:
            return page(request, 'error.html', status_code=409, error='Staging and a GitHub token are required.')
        try:
            existing = [pr for pr in gh.console_prs(runner, github) if promotion.matching(pr, app)]
            if existing:
                return page(request, 'promotion.html', review={'app': app}, prs=existing)
            declared = declared_of(app, 'staging')
            if declared.problem:
                raise actions.ActionError(declared.problem)
            if declared.declared:
                if refused := v2_read_only(request, app, 'staging', 'promote'):
                    return refused
                production = declared_of(app, 'prod')
                if not production.declared:
                    raise actions.ActionError('v2 has no first promotion: production has no records yet')
                reports = ctx.apps()
                report = next((row.report for row in reports if row.instance == found), None)
                if report is None:
                    raise actions.ActionError('Go promotion readiness unavailable: no staging report')
                review = {'app': app, 'v2': True, 'problem': observation.promotion_problem(report),
                          'outcome': 'same' if declared.image_reference == production.image_reference else 'update',
                          'image': declared.image_reference, 'previous': production.image_reference,
                          'address': '', 'diff': '', 'diff_files': 0, 'diff_problem': '', 'held': 'needs manual review',
                          'rollback': False, 'secrets': [], 'storage': None, 'needs_host': False, 'admits': [],
                          'setup': False}
                return page(request, 'promotion.html', review=review, prs=existing)
            raise actions.ActionError('this instance is not v2\'s: its records are not a v2 record pair, so it cannot be promoted here')
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        except (CommandError, cluster.ReadError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    async def change_app(request: Request) -> Response:
        """Scale, change access to, change the connections of, promote or remove a v2 instance: a held PR made by
        v2's binary. v2's records are the only ones the console writes."""
        app, env, change = (request.path_params[k] for k in ('app', 'env', 'change'))
        found = Instance.named(app, env)
        if found is None or change not in ('remove', 'promote', 'egress', 'schedule', *changes.V2_ACTION):
            return page(request, 'error.html', status_code=404, error='No such app instance or change.')
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        fields = parse_qs((await request.body()).decode(), keep_blank_values=True)
        form = {k: v[0] for k, v in fields.items()}
        try:
            declared = declared_of(app, env)
        except cluster.ReadError as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        if not declared.declared or declared.problem:
            return page(request, 'error.html', status_code=409,
                        error=declared.problem or f'{app}/{env} has no v2 records')
        if change == 'egress':
            return page(request, 'error.html', status_code=400,
                        error='the console offers no egress form: grant a host with '
                              f'`swhurl-v2 edit --app {app} --env={env} --set egress=<host>` and run the render it asks for')
        if change == 'schedule':
            return page(request, 'error.html', status_code=400,
                        error='v2 does not model a schedule: the console offers no schedule form')
        if refused := v2_read_only(request, app, env, change):
            return refused
        action = changes.V2_ACTION[change]
        try:
            if env not in changes.ACTIONS[action].envs:
                raise actions.ActionError(f'{change} from the {" or ".join(changes.ACTIONS[action].envs)} instance')
            if change == 'connect':
                for ticked in ('calls', 'subscribe'):  # one checkbox per app or event
                    form[ticked] = ','.join(value for value in fields.get(ticked, []) if value)
            if change == 'promote':
                reviewed = form.get('image', '').partition('@')[2]
                report = ctx.promotion_status(app, reviewed)
                if problem := observation.promotion_problem(report):
                    raise actions.ActionError(problem)
            flags, input = v2_arguments(change, app, form, declared)
            if change == 'connect':  # only what the form offered: the environment's web apps and events
                callable_apps, offered_events, problem = connectable(env, app)
                if problem:
                    raise actions.ActionError(f'cannot check the apps and events chosen: {problem}')
                changes.Connections.from_form(form).check_offered({row.instance.app for row in callable_apps},
                                                                  {name for name, _ in offered_events})
        except actions.ActionError as error:
            return page(request, 'error.html', status_code=400, error=str(error))
        except (CommandError, cluster.ReadError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        return propose(request, action, app, env, flags, input)

    async def use_offer(request: Request) -> Response:
        """The catalogue's Use: the app chosen there connects, as its own Connect to an app would."""
        chosen = parse_qs((await request.body()).decode()).get('app', [''])[0]
        if not chosen:
            return page(request, 'error.html', status_code=400, error='Choose the app that will use it.')
        request.path_params.update(app=chosen, change='connect')
        return await change_app(request)

    return [Route('/apps/{app}/{env}', app), Route('/catalogue/{env}/use', use_offer, methods=['POST']),
            Route('/apps/{app}/staging/promotion', review_promotion),
            Route('/apps/{app}/{env}/flags/{flag}', change_flag, methods=['POST']),
            Route('/apps/{app}/{env}/{change}', change_app, methods=['POST'])]
