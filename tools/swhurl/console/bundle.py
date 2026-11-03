"""The filtered immutable repository bytes used by status and Activity, never live evidence."""
from __future__ import annotations

import io
import tarfile
from pathlib import Path

from swhurl.console import v2
from swhurl.console.actions import ActionError


def selected(path: Path) -> bool:
    return (path.as_posix() in (v2.PROFILE, v2.CALLS, v2.EGRESS) or
            (path.parts and path.parts[0] == v2.RECORDS and path.suffix == '.json') or
            (len(path.parts) == 5 and path.parts[:2] == ('v2-apps', 'instances')
             and path.name == 'helmrelease.json'))


def filtered(content: bytes, *, prefixed: bool = True) -> dict[str, bytes]:
    """Share the archive filter with Activity; reject traversal, never follow archive links."""
    found = {}
    try:
        archive = tarfile.open(fileobj=io.BytesIO(content), mode='r:*')
    except tarfile.TarError:
        raise ActionError('cannot read repository archive') from None
    try:
        with archive:
            for member in archive.getmembers():
                original = Path(member.name)
                if original.is_absolute() or '..' in original.parts:
                    raise ValueError('invalid archive path')
                path = Path(*original.parts[1:]) if prefixed else original
                if selected(path) and member.isfile():
                    found[path.as_posix()] = archive.extractfile(member).read()
    except (tarfile.TarError, EOFError, OSError, ValueError) as error:
        raise ActionError(f'cannot read repository archive: {error}') from None
    return found


def extract(inputs: dict[str, bytes], root: Path) -> None:
    for path, content in inputs.items():
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
