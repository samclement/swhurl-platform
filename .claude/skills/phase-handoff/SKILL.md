---
name: phase-handoff
description: Outline multi-step work before starting it and hand off cleanly after each phase — plan section 0 updated, a three-line status (done, left, next) and any operator actions. Use when starting or finishing a phase of docs/plan.md, a cleanup step, or any change spanning several commits or reconciles.
---

# Phase handoff

> Draft (29 September 2026). Built from sessions where the operator had to ask what was left, whether it was tracked, or for an outline before execution. Revise as steering shows gaps.

## Before starting

Post an outline and wait for approval when the work spans more than one commit, touches the live cluster, or deletes anything:

- **Goal** in one sentence.
- **Steps**, each ending in something checkable (a commit, `make check`, a reconcile, a live verification).
- **Operator actions** needed and when (logins, `sudo`, browser checks), labelled as in `AGENTS.md`.
- **Out of scope**, so the operator can pull it back in.

Once the outline is approved, carry on step to step without asking again. Stop only for the confirm-first actions in `AGENTS.md` or when a step changes the plan.

## After each phase

1. Update `docs/plan.md` section 0: mark done items with the date and remove finished detail (Git keeps it); keep what is left specific enough to start cold.
2. Record live evidence in `docs/current-state.md` as `AGENTS.md` describes.
3. End the turn with:
   - **Done:** what changed, with commits.
   - **Left:** the remaining items, in order, pointing at section 0.
   - **Next:** the one next step, and any decision only the operator can make.
   - **Test it yourself** if anything the operator can see changed.

Keep this short. The plan file is the record; the message is the pointer.
