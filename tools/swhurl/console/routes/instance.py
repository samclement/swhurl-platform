"""An app instance's page and its changes: flags, promotion review, and the pull requests that scale, expose, connect or remove it."""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import logging
from dataclasses import replace
from urllib.parse import parse_qs

from starlette.requests import Request
from starlette.responses import RedirectResponse, Response
from starlette.routing import Route

from swhurl import yamlio
from swhurl.apps import connections as links
from swhurl.apps import contract, declaration, new, ops
from swhurl.apps import promotion as gate
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.apps.model import records
from swhurl.apps.state import verdict
from swhurl.console import actions, cluster, flagstore, merge, promotion, proposal, stages
from swhurl.console import github as gh
from swhurl.console.context import STACK_NAMES, Context, dashboard_urls, logs_url
from swhurl.console.reading import Files
from swhurl.run import CommandError


def routes(ctx: Context) -> list[Route]:
    runner, github, jobs, flags, described, page, open_prs, change_rows, stacks, connectable, propose = (
        ctx.runner, ctx.github, ctx.jobs, ctx.flags, ctx.described, ctx.page, ctx.open_prs, ctx.change_rows, ctx.stacks,
        ctx.connectable, ctx.propose)
    document_source, file_source, platform_file, request_files = ctx.document_source, ctx.file_source, ctx.platform_file, ctx.request_files

    def read_app(request: Request) -> Response:
        found = Instance.named(request.path_params['app'], request.path_params['env'])
        status, rows = runner.together(lambda: ops.gather_status(runner, found),
                                       lambda: cluster.apps(runner)) if found else (None, [])
        if status is None and found:
            # Created here but not applied yet: say so instead of "no such app" (its links appear at once).
            label = f'{found.app}/{found.env}'
            pending = [j for j in jobs.recent() if j.unit == label and j.action.startswith('new app')]
            prs, prs_error = open_prs()
            prs = [pr for pr in prs if stages.target_of(pr.branch) in (('new', label), ('promote', label))]
            in_git = False
            if not pending and not prs and github:
                in_git = promotion.main_tree(runner, github, lambda root: found.folder(root).exists())
            if pending or prs or in_git:
                return page(request, 'pending.html', label=label, jobs=pending, prs=prs, prs_error=prs_error, in_git=in_git)
        if status is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        summary = next((a for a in cluster.summaries(rows) if a.app == found.app), None)
        promotion_problem = gate.source_problem(promotion.settled_status(runner, github, status), status.desired_image) if found.env == 'staging' else ''
        row = summary.envs.get(found.env) if summary else None
        shown = row.connections if row else cluster.Connections()
        files = request_files.get()
        (flag_list, flag_problem), settings, (relation_rows, relation_problem), secrets, minimum = files.together(
            lambda: flag_rows(found, shown.offers), lambda: instance_settings(found),
            lambda: links.intent(found, shown.rows, document_source, together=files.together),
            lambda: secret_states(found, status), lambda: image_minimum(found))
        shown = replace(shown, rows=relation_rows)
        return page(request, 'app.html', status=status, secret_states=secrets, flag_rows=flag_list, flag_problem=flag_problem, flags_writable=flags is not None,
                    flag_changed=request.query_params.get('flag', ''), team=(row.team if row else '') or ops.team_of(runner, found),
                    connections=shown, relation_problem=relation_problem, allows=settings.grants.allows if settings else None, connectable=connectable(found.env, found.app), logs=logs_url(found.app), dashboard=dashboard_urls(runner).get(found.app, ''),
                    stack=stacks([found.app]).get(found.app, ''), stack_names=STACK_NAMES, owner=contract.APP_OWNER,
                    scale_fields=merge.SCALE_FIELDS, minimum=minimum, github=github,
                    promotion_problem=promotion_problem,
                    exposures=ops.EXPOSURE_LABELS, exposure_text=proposal.EXPOSURE_LABELS, domain=contract.COOKIE_DOMAIN,
                    summary=summary, state=verdict(status), unit_jobs=change_rows([j for j in jobs.recent() if j.unit in (found.unit, f'{found.app}/{found.env}')], others=False)[0])

    def secret_states(found: Instance, status: ops.InstanceStatus) -> dict[str, str] | None:
        """Each secret name and whether Git has its value (ops.secret_states); None when Git cannot be read.
        Only the Secret file's key names are read: SOPS leaves them in plain text, the values stay encrypted."""
        if document_source is None:
            return None
        try:
            text = document_source(f'apps/{found.app}/{found.env}/{contract.SECRET_FILE}')
        except Exception:  # noqa: BLE001 (GitHub unavailable: the page says unknown, it does not fail)
            return None
        return ops.secret_states(status.secret_names, contract.stored_secrets(text))

    def flag_rows(found: Instance, offers_commit: str) -> tuple[list[dict], str]:
        """Each flag the instance's image declares, with its override, then each override of a flag it does
        not declare; and why overrides could not be read. An override older than STALE_DAYS is ``stale``."""
        declared = described.of(found.app, found.env, offers_commit)
        problem, overrides = '', {}
        if flags is None:
            problem = 'This console has no NATS key (console-nats Secret), so overrides cannot be read or set.' if declared.flags else ''
        else:
            try:
                overrides = flags.overrides(found.env, found.app)
            except flagstore.FlagStoreError as error:
                problem = f'Overrides could not be read: {error}'
        if not declared.flags and not overrides:
            return [], problem or declared.problem
        now = dt.datetime.now(dt.UTC)

        def row(name: str, flag: dict | None) -> dict:
            value = flag.get(found.env, flag['default']) if flag else None
            override = overrides.get(name)
            age = (now - override.at).days if override and override.at else None
            return {'name': name, 'flag': flag or {}, 'declared': value, 'undeclared': flag is None,
                    'override': override.value if override else flagstore.NO_VALUE, 'overridden': override is not None,
                    'age': age, 'stale': age is not None and age >= flagstore.STALE_DAYS,
                    'effective': value if override is None else override.value}
        declared_flags = declared.flags or {}
        return [row(name, flag) for name, flag in declared_flags.items()] + \
            [row(name, None) for name in overrides if name not in declared_flags], problem

    async def change_flag(request: Request) -> Response:
        """Set or clear one flag's override: written to NATS at once (no PR), and to the audit log."""
        app, env, name = (request.path_params[k] for k in ('app', 'env', 'flag'))
        found = Instance.named(app, env)
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such app instance.')
        if flags is None:
            return page(request, 'error.html', status_code=409, error='This console has no NATS key (console-nats Secret).')
        form = {k: v[0] for k, v in parse_qs((await request.body()).decode(), keep_blank_values=True).items()}
        try:
            rows = await asyncio.to_thread(cluster.apps, runner)
            row = next((r for r in rows if r.instance == found), None)
            declared = described.of(app, env, row.connections.offers if row else '').flags or {}
            if form.get('action') == 'clear':
                if not declaration.FLAG_NAME.fullmatch(name):
                    raise ValueError('not a flag name')
                await asyncio.to_thread(flags.clear, env, app, name)
                done = 'cleared'
            elif name not in declared:
                raise ValueError(f'{app}/{env} runs an image that does not declare {name}')
            else:
                value = flagstore.typed(declared[name], form.get('value', ''))
                await asyncio.to_thread(flags.set, env, app, name, value)
                done = f'= {json.dumps(value)}'
        except ValueError as error:
            return page(request, 'error.html', status_code=400, error=f'{name}: {error}')
        except (flagstore.FlagStoreError, cluster.ReadError, CommandError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        identity = request.state.identity
        logging.getLogger('swhurl.console.audit').info(
            f'[AUDIT] {identity} flag {app}/{env} {name} {done}',
            extra={'identity': identity, 'action': 'flag', 'unit': f'{app}/{env}', 'state': 'done', 'flag': name})
        return RedirectResponse(f'/apps/{app}/{env}?flag={name}#flags', status_code=303)

    def app(request: Request) -> Response:
        # Keep the reader alive through rendering: template offer descriptions read files too.
        remote = file_source is platform_file
        with Files(runner, github if remote else None, None if remote else file_source) as files:
            token = request_files.set(files)
            try:
                with runner.once():
                    return read_app(request)
            except (CommandError, cluster.ReadError, actions.ActionError, EditError) as error:
                return page(request, 'error.html', status_code=502, error=str(error))
            finally:
                request_files.reset(token)

    def instance_settings(found: Instance) -> records.Settings | None:
        """The instance's Settings in Git (its calls and who it allows); None when Git cannot be read."""
        if document_source is None:
            return None
        try:
            text = document_source(f'apps/{found.app}/{found.env}/{records.SETTINGS_FILE}')
            return records.Settings.parse(yamlio.load(text)) if text is not None else None
        except Exception:  # noqa: BLE001 (GitHub unavailable or an unreadable record: the page shows what runs)
            return None

    def image_minimum(found: Instance) -> dict | None:
        """The resources the image needs at least, by Scale field, from the instance's Release in Git (written
        with each image, as make app-scale reads it); None when Git cannot be read."""
        if document_source is None:
            return None
        try:
            text = document_source(f'apps/{found.app}/{found.env}/{records.RELEASE_FILE}')
            if text is None:
                return None
            resources = records.Release.parse(yamlio.load(text)).resources
        except Exception:  # noqa: BLE001 (GitHub unavailable or an unreadable record: the form says nothing)
            return None
        return {field: value for field in ('cpu', 'memory', 'memory_limit') if (value := getattr(resources, field))}

    def review_promotion(request: Request) -> Response:
        app = request.path_params['app']
        found = Instance.named(app, 'staging')
        if found is None or github is None:
            return page(request, 'error.html', status_code=409, error='Staging and a GitHub token are required.')
        try:
            existing = [pr for pr in gh.console_prs(runner, github) if promotion.matching(pr, app)]
            if existing:
                return page(request, 'promotion.html', review={'app': app}, prs=existing)
            status = promotion.staging_status(runner, github, found)
            if status is None:
                raise actions.ActionError('staging is missing; deploy staging before promoting')
            reviewed = promotion.main_tree(runner, github, lambda root: promotion.review(root, app, status, runner))
            return page(request, 'promotion.html', review=reviewed, prs=existing)
        except (EditError, new.GenerationError, actions.ActionError) as error:
            return page(request, 'error.html', status_code=409, error=str(error))
        except (CommandError, cluster.ReadError) as error:
            return page(request, 'error.html', status_code=502, error=str(error))

    async def change_app(request: Request) -> Response:
        """Promote, scale, change access to, change the connections of or remove an instance: a PR made by the clone's app-* commands."""
        app, env, change = (request.path_params[k] for k in ('app', 'env', 'change'))
        found = Instance.named(app, env)
        if found is None or change != 'promote' and change not in (*merge.INSTANCE_KINDS, 'remove'):
            return page(request, 'error.html', status_code=404, error='No such app instance or change.')
        if github is None:
            return page(request, 'error.html', status_code=409, error='No GitHub token is configured (console-github Secret).')
        fields = parse_qs((await request.body()).decode(), keep_blank_values=True)
        form = {k: v[0] for k, v in fields.items()}
        try:
            if env not in merge.ACTIONS[change].envs:
                raise actions.ActionError(f'{change} from the {" or ".join(merge.ACTIONS[change].envs)} instance')
            if change == 'connect':
                for ticked in ('calls', 'subscribe'):  # one checkbox per app or event
                    form[ticked] = ','.join(value for value in fields.get(ticked, []) if value)
            commands = merge.ACTIONS[change].commands(app, env, form)  # malformed: refused before reading the cluster
            if change == 'connect':  # only what the form offered: the environment's web apps and events
                callable_apps, offered_events, problem = connectable(env, app)
                if problem:
                    raise actions.ActionError(f'cannot check the apps and events chosen: {problem}')
                merge.Connections.from_form(form).check_offered({row.instance.app for row in callable_apps},
                                                                  {name for name, _ in offered_events})
        except (actions.ActionError, EditError, new.GenerationError) as error:
            return page(request, 'error.html', status_code=400, error=str(error))
        except CommandError as error:
            return page(request, 'error.html', status_code=502, error=str(error))
        return propose(request, change, app, env, commands, auto_merge=form.get('hold') != 'on')

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
