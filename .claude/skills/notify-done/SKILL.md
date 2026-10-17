---
name: notify-done
description: Send a push notification to the operator's phone through ntfy.sh when a task finishes, fails or is blocked waiting for them. Use at the end of every task in this repo, just before the final report, and whenever work stops for operator input (a confirm-first action, a sudo command, a login).
---

# Notify when done

The operator subscribes to `https://ntfy.sh/swhurl-homelab-agent-notifications-7253836` on their phone. Send one message when the turn ends with work finished, failed, or waiting for them; not for intermediate steps.

```bash
curl -fsS -m 10 \
  -H "Title: <title>" \
  -H "Priority: <default|high>" \
  -H "Tags: <white_check_mark|x|raised_hand>" \
  -d "<body>" \
  https://ntfy.sh/swhurl-homelab-agent-notifications-7253836 >/dev/null
```

| Outcome | Title | Priority | Tags |
| --- | --- | --- | --- |
| Done and verified | `Done: <task in a few words>` | `default` | `white_check_mark` |
| Failed or stopped on an error | `Failed: <task>` | `high` | `x` |
| Waiting for the operator | `Needs you: <task>` | `high` | `raised_hand` |

The body is one to three short lines: what happened, the commit (short hash) if any, and the next action for the operator if one is owed (for example "run `make host-dns` in your own terminal").

## Rules

- **The topic is public**: anyone who knows its name can read it. Never send Secret values, tokens, keys, kubeconfig content or private hostnames beyond what is already in this public repository.
- Plain text only; ntfy shows the body as-is.
- A failed `curl` must not fail the task: report it in the final message and carry on.
- One notification per finished task. The final message in the terminal still carries the full report; the notification only says it is ready.
