"""The console's second GitHub token: ``APP_REPOS_TOKEN`` in the ``console-github`` Secret.

It covers every repository of its owner, because GitHub lets a fine-grained token create repositories
only then; it could therefore change or delete any of them. Nothing in Python writes with it any more:
the console hands it to ``swhurl-v2 app-repo`` in the child's environment only, and that command is the
one that creates a repository, writes its first commit and adds its image webhook. The image webhook's
token is the same Secret's ``IMAGE_WEBHOOK_TOKEN``, and both stay out of argv and out of the job's log.
"""
from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass

from swhurl import platform
from swhurl.apps.contract import APP_OWNER
from swhurl.imagehooks import CONSOLE_TOKEN_KEY
from swhurl.run import Runner


@dataclass(frozen=True)
class AppReposToken:
    token: str
    owner: str = APP_OWNER
    image_webhook_token: str = ''  # signs the image webhook; without it new repositories get none


def token_from_env(runner: Runner, env: Mapping[str, str] | None = None) -> AppReposToken | None:
    env = os.environ if env is None else env
    token = env.get(platform.APP_REPOS_TOKEN_KEY, '').strip()
    if token in ('', 'REPLACE_ME'):
        return None
    runner.add_secret(token)
    webhook = env.get(CONSOLE_TOKEN_KEY, '').strip()
    runner.add_secret(webhook)
    return AppReposToken(token, image_webhook_token='' if webhook == 'REPLACE_ME' else webhook)
