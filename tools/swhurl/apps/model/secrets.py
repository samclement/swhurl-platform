"""An app's own secrets: the names its image reads become variables from one Secret, whose values only
the operator sets by hand; the encrypted file is listed when present or prepared as ciphertext."""
from __future__ import annotations

from swhurl import interface
from swhurl.apps.contract import SECRET_FILE, app_secret
from swhurl.apps.defaults import secret_key_problem
from swhurl.apps.model.draft import APP_SECRET, Draft, Entry, RenderError


def apply(draft: Draft, entry: Entry) -> None:
    names, app = entry.release.secrets, draft.instance.app
    if problem := secret_key_problem(list(names)):
        raise RenderError(f'{draft.instance}: {problem}')
    if names:
        draft.annotate(draft.release, interface.SECRET_NAMES, ','.join(names))
    if names or (APP_SECRET in entry.encrypted or APP_SECRET in entry.pending):  # a value may be set before the image that reads it
        # Optional: the app starts before the Secret has its first value.
        draft.env_from(app_secret(app), optional=True)
        draft.reload(app_secret(app))
    if (APP_SECRET in entry.encrypted or APP_SECRET in entry.pending):
        draft.list_file(SECRET_FILE)
        draft.decrypt()
