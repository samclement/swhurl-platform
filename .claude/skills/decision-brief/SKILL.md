---
name: decision-brief
description: Present a choice the operator has to make — where code lives, which tool or service, host timer vs CronJob, rename vs document, upgrade path — as a short brief with stated assumptions, the options they are likely to raise themselves, criteria, a recommendation and what would change it. Use before recommending an approach that is costly to reverse or that the operator has not already decided.
---

# Decision brief

> Draft (29 September 2026). Built from sessions where the operator had to supply the missing option or trade-off. Revise as steering shows gaps.

A recommendation is only useful if the operator can see what it rests on. Past briefs failed by hiding an assumption (the console "lives in this repo"), omitting an obvious option (systemd, the AWS CLI), or missing a criterion the operator cares about (staying in sync with platform upgrades).

## Write the brief

1. **Decision.** One sentence: what is being chosen and why now.
2. **Assumptions.** List what the options take for granted: where code and manifests live, who runs it (user or system, which account), what credentials it needs, which Flux unit owns it. Mark each as checked (with the file or command) or assumed.
3. **Options.** Two to four, including:
   - the one already used elsewhere in this repo (consistency is a criterion here);
   - the simplest thing that could work;
   - the option the operator is likely to ask about. If you considered and rejected something, include it with one line on why.
4. **Criteria.** Score the options only on what matters for this decision. Ones that recur on this platform:
   - reversibility and blast radius on the live cluster;
   - consistency with how similar things are already done;
   - fewer commands or places for the operator to remember;
   - stays in sync as the platform changes (charts, tooling, policy);
   - testable with `FakeRunner` or a throwaway live test;
   - fits the repo's boundaries (`AGENTS.md`, `docs/architecture.md`).
5. **Recommendation.** One option, the main reason, and what it costs.
6. **What would change it.** The fact or preference that would flip the recommendation. This is where the operator most often steers; make it easy.

Keep it to one screen. Use a table only when there are three or more options scored on shared criteria.

## After the decision

- Record a decision that changes what runs or how the repo is organised in `docs/plan.md` (open work) or the canonical page for the topic. Don't create a separate decision log.
- If the operator's answer revealed a criterion missing from the list above, say so at the end of the turn so it can be added here.
