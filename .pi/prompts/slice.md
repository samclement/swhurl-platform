---
description: Start one v2 slice session, lean context, stop at the slice boundary
argument-hint: "<slice>"
---
Work on v2 slice: ${1:-the next gate in docs/v2-status.md}

This session covers this slice only. When it is done, write the record and stop; the next slice starts a new session.

Work this session at `high` thinking: the slice's contracts, acceptance criteria and decisions are made here, never at `low`. Subtasks may run cheaper: send them to a low-thinking subagent (`scout` is `thinking: low`; a call may also name `model: "deepseek/deepseek-flash:low"`) so their tokens cost little and only their summary returns here.

Keep the transcript lean, because every turn re-sends it:
- Read `docs/v2-status.md` by section (status, next gate, resume steps), then the approved contract for this slice in `docs/iteration-2-plan.md` with targeted `read` offsets and `rg -n` narrowed to paths. Do not read whole long documents.
- Send broad exploration to a subagent (`scout`) so greps, file dumps and test output stay in its context, and only the summary enters this one. Never switch this session below `high` to save tokens; cheaper thinking belongs to the subagent doing a bounded subtask.
- Keep a script in a file and call it again rather than re-pasting the same heredoc; each copy is re-sent on every later turn.

Before editing, state in two lines: the slice's contract and its acceptance criteria, and what this session will not touch.

Finish by recording dated evidence in `docs/current-state.md` and the workstream record in `docs/workstreams.md`, running the checks from `AGENTS.md`, and reporting progress against the goals table. A change to `v2/` also needs `make check-v2`.
