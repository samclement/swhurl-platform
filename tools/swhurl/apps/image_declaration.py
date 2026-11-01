"""Print the config label an app's image build writes from its swhurl.yaml: what the image needs, for v2.

The stack templates call this internal command from a pinned platform checkout, before the image
build, and pass the one line it prints to the build (`--label`, or build-push-action's `labels`). The
image then states it, and the platform reads it with the manifest and no credential (v2's registry
adapter), so nothing reads the app repository again to learn what an image needs.

The document is v2's contract, not this file's: the six fields it may state, absent when the image
does not have them, and a version v2 accepts. `secrets` and `memoryMiB` are stated so an image that
adds one is held rather than run without it. It reads the app directory it is given and nothing else,
and writes nothing.

The documents the app offers travel in the same document, base64-encoded, so the label a build writes is
everything a create or a deploy needs: the description, the API path, the memory, the volume and the bytes.
The encoded line is capped at 120 KiB (the value reaches buildx as one argument), and the producer names the
documents and their sizes when it refuses one.

Usage: python3 -m swhurl image-declaration [--app-dir DIR]
Exit: 0 the label was printed; 2 the declaration cannot be used.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from swhurl.apps import declaration, offers

USAGE = 'usage: image-declaration [--app-dir DIR]'
MAX_LABEL_BYTES = 120 * 1024
"""The encoded document's cap: buildx receives it as one argument (MAX_ARG_STRLEN is 131,071 bytes)."""


class LabelError(Exception):
    """The declaration cannot produce a label; the message names the file and the problem."""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, usage=USAGE)
    parser.add_argument('--app-dir', type=Path, default=None,
                        help="the app's checkout, which holds its swhurl.yaml (default: the working directory)")
    args = parser.parse_args(argv)
    try:
        print(label_of(args.app_dir or Path.cwd()))
    except LabelError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    return 0


def label_of(app: Path) -> str:
    """The one line an image build passes as a config label: `<key>=<compact JSON document>`."""
    where = app / declaration.FILE
    try:
        parsed = declaration.parse(reads(where), str(where))
    except declaration.DeclarationError as error:
        raise LabelError(str(error)) from None
    documents = documents_of(app, parsed)
    document = parsed.image_label(publishes_of(app, parsed), documents)
    encoded = json.dumps(document, separators=(",", ":"), ensure_ascii=False)
    if len(encoded) > MAX_LABEL_BYTES:
        sizes = ', '.join(f'{role} ({len(data)} bytes)' for role, data in documents.items())
        raise LabelError(f'the declaration is {len(encoded)} bytes encoded, over the {MAX_LABEL_BYTES}-byte cap a '
                         f'build argument allows: {sizes or "no documents"}')
    return f'{declaration.IMAGE_LABEL}={encoded}'


def documents_of(app: Path, parsed: declaration.Declaration) -> dict[str, bytes]:
    """The files the app offers, by v2's role: the API and events documents it names, and the flags its
    swhurl.yaml declares. The API path and its document are stated together or not at all, because v2 refuses
    one without the other."""
    if bool(parsed.api_file) != bool(parsed.api_path):
        raise LabelError(f'{declaration.FILE}: provides.api (the path the app serves) and provides.apiFile (the '
                         f'document in the repository) are stated together or not at all')
    documents: dict[str, bytes] = {}
    if parsed.api_file:
        documents['api'] = reads_bytes(app / parsed.api_file)
    if parsed.events_file:
        documents['events'] = reads_bytes(app / parsed.events_file)
    if parsed.flags:  # what the console's flag form is built from (docs/apps.md#feature-flags)
        documents['flags'] = (json.dumps(parsed.flags, indent=2) + '\n').encode()
    return documents


def reads_bytes(path: Path) -> bytes:
    """A document's bytes, unchanged: the record's copy must be byte-identical to the file the build read."""
    try:
        return path.read_bytes()
    except OSError as error:
        raise LabelError(f'{path}, which {declaration.FILE} names: {error.strerror or error}') from None


def publishes_of(app: Path, parsed: declaration.Declaration) -> tuple[str, ...]:
    """The events the image publishes: what the AsyncAPI document swhurl.yaml names says it sends."""
    if not parsed.events_file:
        return ()
    where = app / parsed.events_file
    try:
        text = where.read_text(encoding='utf-8')
    except OSError as error:
        raise LabelError(f'{where}, which {declaration.FILE} names: {error.strerror or error}') from None
    try:
        return tuple(event.name for event in offers.events(text, str(where)))
    except offers.OfferError as error:
        raise LabelError(str(error)) from None


def reads(path: Path) -> str:
    """The file's text. A file the app does not have is a refusal that names it: the build has nothing to
    state, and the failure is in the app's own files rather than in the platform."""
    try:
        return path.read_text(encoding='utf-8')
    except OSError as error:
        raise LabelError(f'{path}: {error.strerror or error}') from None
