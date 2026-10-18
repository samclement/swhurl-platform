"""What an image offers and says about itself: where it serves its API description, its description,
and the commit its offered documents (offers/, copied by intake) were read at."""
from __future__ import annotations

from swhurl.apps import offers
from swhurl.apps.calling import API_VARIABLE, DESCRIPTION_VARIABLE
from swhurl.apps.model.draft import Draft, Entry


def apply(draft: Draft, entry: Entry) -> None:
    release = entry.release
    if release.kind == 'web' and release.api_path:
        draft.env[API_VARIABLE] = release.api_path
    if release.description:
        draft.env[DESCRIPTION_VARIABLE] = release.description
    if release.offers:
        draft.annotate(draft.release, offers.ANNOTATION, release.offers)
