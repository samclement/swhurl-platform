"""Activity and a job's page: every change with how far it has got, and a running job's output as it arrives."""
from __future__ import annotations

import asyncio
import json
import time

from starlette.requests import Request
from starlette.responses import PlainTextResponse, Response, StreamingResponse
from starlette.routing import Route

from swhurl.console import stages
from swhurl.console.context import Context, moved, short_hashes
from swhurl.console.reading import LOG as TIMING

STREAM_POLL = 0.25
KEEPALIVE = 15.0


def routes(ctx: Context) -> list[Route]:
    runner, github, app_repos, jobs, page, change_rows, resumable = (
        ctx.runner, ctx.github, ctx.app_repos, ctx.jobs, ctx.page, ctx.change_rows, ctx.resumable)

    def activity(request: Request) -> Response:
        started = time.monotonic()
        try:
            with runner.once():
                rows, prs_error = change_rows(jobs.recent(), others=True)
            # What is still moving or waiting for someone comes first; then what ended, newest first.
            return page(request, 'activity.html', moving=[row for row in rows if not row.track.settled],
                        ended=[row for row in rows if row.track.settled], prs_error=prs_error)
        finally:
            TIMING.info('activity elapsed_ms=%.1f', (time.monotonic() - started) * 1000)

    def job(request: Request) -> Response:
        found = jobs.get(request.path_params['id'])
        if found is None:
            return page(request, 'error.html', status_code=404, error='No such job (only the newest jobs are kept).')
        with runner.once():
            (row,), _error = change_rows([found], others=False)
        return page(request, 'job.html', job=found, track=row.track, stage=stages.prepared(found), pr=row.pr,
                    can_resume=bool(resumable(found) and github and app_repos))

    async def job_events(request: Request) -> Response:
        found = jobs.get(request.path_params['id'])
        if found is None or not found.live:  # another pod's job: its page re-reads itself instead
            return PlainTextResponse('No such job here.\n', status_code=404)

        # EventSource supplies Last-Event-ID on reconnect. The query is the
        # initial page's already-rendered line count.
        raw = request.headers.get('last-event-id')
        if raw is None:
            raw = request.query_params.get('after', '0')
        try:
            sent = max(0, int(raw))
        except (TypeError, ValueError):
            sent = 0
        sent = min(sent, len(found.lines))

        async def stream():
            nonlocal sent
            last_activity = time.monotonic()
            while True:
                finished = found.finished is not None
                new = found.lines[sent:]
                for line in new:
                    sent += 1
                    body = json.dumps({'html': str(short_hashes(line))}, ensure_ascii=False)
                    yield f'id: {sent}\nevent: line\ndata: {body}\n\n'
                    last_activity = time.monotonic()
                if finished and not new:
                    body = json.dumps({'state': found.state,
                                       'finished': found.finished.strftime('%H:%M:%S'), 'link': found.link})
                    yield f'event: done\ndata: {body}\n\n'
                    return
                idle = time.monotonic() - last_activity
                if idle >= KEEPALIVE:
                    yield ': keep-alive\n\n'
                    last_activity = time.monotonic()
                await asyncio.sleep(min(STREAM_POLL, max(0.001, KEEPALIVE - (time.monotonic() - last_activity))))

        return StreamingResponse(stream(), media_type='text/event-stream',
                                 headers={'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no'})

    return [Route('/activity', activity), Route('/jobs', moved('/activity')), Route('/jobs/{id:int}', job),
            Route('/jobs/{id:int}/events', job_events)]
