"""Print the config label an app's image build writes from its swhurl.yaml: what the image needs, for v2.

The stack templates call this internal command from a pinned platform checkout, before the image
build, and pass the one line it prints to the build (`--label`, or build-push-action's `labels`). The
image then states it, and the platform reads it with the manifest and no credential (v2's registry
adapter), so nothing reads the app repository again to learn what an image needs.

The document is v2's contract, not this file's: the six fields it may state, absent when the image
does not have them, and a version v2 accepts. `secrets` and `memoryMiB` are stated so an image that
adds one is held rather than run without it. It reads the app directory it is given and nothing else,
and writes nothing.

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
    document = parsed.image_label(publishes_of(app, parsed))
    return f'{declaration.IMAGE_LABEL}={json.dumps(document, separators=(",", ":"), ensure_ascii=False)}'


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
