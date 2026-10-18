"""An app's own secrets: the names its image reads become variables from one Secret, whose values only
the operator sets (make app-secret); the encrypted file is listed once it exists."""
from __future__ import annotations

from swhurl.apps.contract import SECRET_FILE, SECRET_NAMES, app_secret
from swhurl.apps.model.draft import APP_SECRET, Draft, Entry


def apply(draft: Draft, entry: Entry) -> None:
    names, app = entry.release.secrets, draft.instance.app
    if names:
        draft.annotate(draft.release, SECRET_NAMES, ','.join(names))
    if names or APP_SECRET in entry.encrypted:  # a value may be set before the image that reads it
        # Optional: the app starts before the Secret has its first value.
        draft.env_from(app_secret(app), optional=True)
        draft.reload(app_secret(app))
    if APP_SECRET in entry.encrypted:
        draft.list_file(SECRET_FILE)
        draft.decrypt()
