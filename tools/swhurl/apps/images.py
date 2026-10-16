"""Which image staging should run: the newest tag an app's ImagePolicy accepts, and its digest.

Flux's ImagePolicy (in the instance's image-automation.yaml) picks the tag in the cluster, and its
Alert starts the Deploy workflow (docs/apps.md#deploy-a-new-image). The workflow cannot ask the
cluster, so it reads the registry here with the same filter: the tags matching ``filterTags.pattern``,
ordered by the number ``extract`` names, the highest winning. The digest is the sha256 of the tag's
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
from pathlib import Path

import yaml

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


def policy(path: Path) -> Policy:
    """The ImagePolicy and its ImageRepository in an instance's image-automation.yaml."""
    documents = [d for d in yaml.safe_load_all(path.read_text()) if isinstance(d, dict)]
    found = {d.get('kind'): d for d in documents}
    chosen, source = found.get('ImagePolicy'), found.get('ImageRepository')
    if not chosen or not source:
        raise ImageError(f'{path} has no ImagePolicy and ImageRepository')
    spec, image = chosen.get('spec') or {}, str((source.get('spec') or {}).get('image', ''))
    tags = spec.get('filterTags') or {}
    if not image.startswith(REGISTRY + '/') or 'numerical' not in (spec.get('policy') or {}):
        raise ImageError(f'{path}: only a numerical ImagePolicy on a {REGISTRY} image is read here')
    if (spec['policy']['numerical'] or {}).get('order', 'asc') != 'asc':
        raise ImageError(f'{path}: only the highest number wins (order: asc)')
    return Policy(chosen['metadata']['name'], image.removeprefix(REGISTRY + '/'), re.compile(str(tags.get('pattern', ''))),
                  str(tags.get('extract', '')).removeprefix('$'))


def _get(opener: Opener, url: str, headers: dict[str, str], what: str) -> bytes:
    try:
        return opener(urllib.request.Request(url, headers={'User-Agent': 'swhurl-app-deploy', **headers}))
    except urllib.error.HTTPError as error:
        raise ImageError(f'could not read {what}: {error.code} {error.reason}') from None
    except urllib.error.URLError as error:
        raise ImageError(f'could not reach {REGISTRY} for {what}: {error.reason}') from None


def newest(chosen: Policy, *, opener: Opener = _open) -> Image:
    """The tag the policy picks from the registry now, and its digest."""
    scope = urllib.parse.quote(f'repository:{chosen.repository}:pull')
    token = json.loads(_get(opener, f'https://{REGISTRY}/token?scope={scope}', {}, f'a pull token for {chosen.repository}'))['token']
    auth = {'Authorization': f'Bearer {token}'}
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
    body = _get(opener, f'https://{REGISTRY}/v2/{chosen.repository}/manifests/{tag}', {**auth, 'Accept': MANIFESTS},
                f'{chosen.repository}:{tag}')
    return Image(tag, 'sha256:' + hashlib.sha256(body).hexdigest())
