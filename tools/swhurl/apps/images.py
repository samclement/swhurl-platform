"""Which image staging should run: the newest tag an app's ImagePolicy accepts, and its digest.

Flux's rendered ImagePolicy picks the tag in the cluster, and its Alert starts the Deploy workflow
(docs/apps.md#deploy-a-new-image). The workflow cannot ask the cluster, so it reads the registry here
with the same platform tag contract and the repository in the Release record: matching tags ordered
by the extracted run number, the highest winning. The digest is the sha256 of the tag's
manifest as the registry stores it, which is what Flux records (``digestReflectionPolicy: Always``).

Only public GHCR packages are read (an anonymous pull token). Nothing here edits an instance.
"""
from __future__ import annotations

import hashlib
import json
import re
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass

from swhurl.apps.offers import Opener, _open

REGISTRY = 'ghcr.io'
PAGE = 1000
MANIFESTS = ', '.join(('application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json',
                       'application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json'))


class ImageError(Exception):
    pass


@dataclass(frozen=True)
class Policy:
    name: str
    repository: str  # owner/name on ghcr.io
    pattern: re.Pattern
    extract: str


@dataclass(frozen=True)
class Image:
    tag: str
    digest: str


def _get(opener: Opener, url: str, headers: dict[str, str], what: str) -> bytes:
    try:
        return opener(urllib.request.Request(url, headers={'User-Agent': 'swhurl-app-deploy', **headers}))
    except urllib.error.HTTPError as error:
        raise ImageError(f'could not read {what}: {error.code} {error.reason}') from None
    except urllib.error.URLError as error:
        raise ImageError(f'could not reach {REGISTRY} for {what}: {error.reason}') from None


def _pull_token(repository: str, opener: Opener) -> dict[str, str]:
    scope = urllib.parse.quote(f'repository:{repository}:pull')
    token = json.loads(_get(opener, f'https://{REGISTRY}/token?scope={scope}', {}, f'a pull token for {repository}'))['token']
    return {'Authorization': f'Bearer {token}'}


def _body(opener: Opener, repository: str, tag: str, auth: dict[str, str]) -> bytes:
    return _get(opener, f'https://{REGISTRY}/v2/{repository}/manifests/{tag}', {**auth, 'Accept': MANIFESTS},
                f'{repository}:{tag}')


def _digest(body: bytes) -> str:
    return 'sha256:' + hashlib.sha256(body).hexdigest()


def platform_digest(repository: str, tag: str, platform: str = 'linux/amd64', *, opener: Opener = _open) -> str:
    """The digest of the manifest ``tag`` names for one platform.

    A tag naming one manifest is that manifest's digest. A tag naming an index lists a manifest per
    platform, and the platform's own entry is the digest a build declares as its artifact: v2's records
    state it as ``artifact.digest``, so this is what a v2-owned instance is compared with. An index
    without the platform is refused, not guessed.
    """
    body = _body(opener, repository, tag, _pull_token(repository, opener))
    try:
        document = json.loads(body)
    except ValueError:
        return _digest(body)
    if not isinstance(document, dict) or not document.get('manifests'):
        return _digest(body)
    os_name, architecture = platform.split('/', 1)
    for entry in document['manifests']:
        stated = (entry.get('platform') or {}) if isinstance(entry, dict) else {}
        if (stated.get('os'), stated.get('architecture')) == (os_name, architecture):
            return entry['digest']
    raise ImageError(f'{repository}:{tag} has no {platform} manifest, so its digest cannot be stated')


def newest(chosen: Policy, *, opener: Opener = _open) -> Image:
    """The tag the policy picks from the registry now, and its digest."""
    auth = _pull_token(chosen.repository, opener)
    tags: list[str] = []
    while True:  # the registry pages its tag list; ?last= continues after the last one seen
        after = f'&last={urllib.parse.quote(tags[-1])}' if tags else ''
        page = json.loads(_get(opener, f'https://{REGISTRY}/v2/{chosen.repository}/tags/list?n={PAGE}{after}', auth,
                               f'the tags of {chosen.repository}')).get('tags') or []
        tags += page
        if len(page) < PAGE:
            break
    ranked = [(int(m[chosen.extract]) if chosen.extract else 0, tag) for tag in tags if (m := chosen.pattern.match(tag))]
    if not ranked:
        raise ImageError(f'{chosen.repository} has no tag matching {chosen.pattern.pattern} ({chosen.name})')
    tag = max(ranked)[1]
    return Image(tag, _digest(_body(opener, chosen.repository, tag, auth)))
