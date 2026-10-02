"""Round-trip YAML files for app edits; generation and policy keep using PyYAML."""
from __future__ import annotations

import io
from pathlib import Path

from ruamel.yaml import YAML
from ruamel.yaml.comments import CommentedMap
from ruamel.yaml.error import YAMLError
from ruamel.yaml.util import load_yaml_guess_indent


class EditError(Exception):
    pass


class YamlFile:
    """One mapping document, retaining comments, quotes and collection styles."""

    def __init__(self, path: Path):
        self.path = path
        self.yaml = YAML()
        self.yaml.preserve_quotes = True
        self.yaml.width = 4096
        try:
            text = path.read_text()
            self.data, indent, offset = load_yaml_guess_indent(text, yaml=self.yaml)
        except (OSError, YAMLError) as error:
            raise EditError(f'cannot edit {path}: {error}') from None
        if not isinstance(self.data, CommentedMap):
            raise EditError(f'{path}: expected one YAML mapping document')
        # Guess sequence indentation independently of mapping indentation: both
        # indentless lists and indented lists are common in handwritten manifests.
        mapping_indents = [value.lc.col - parent.lc.col
                           for parent in self._mappings(self.data)
                           for value in parent.values()
                           if isinstance(value, CommentedMap) and not value.fa.flow_style()
                           and value.lc.col > parent.lc.col]
        self.yaml.indent(mapping=min(mapping_indents, default=2),
                         sequence=indent or 2, offset=offset or 0)
        self.yaml.explicit_start = any(line == '---' for line in text.splitlines())
        self.yaml.explicit_end = text.rstrip().endswith('\n...')

    @staticmethod
    def _mappings(value, seen=None):
        seen = set() if seen is None else seen
        if id(value) in seen:
            return
        seen.add(id(value))
        if isinstance(value, dict):
            yield value
            for child in value.values():
                yield from YamlFile._mappings(child, seen)
        elif isinstance(value, list):
            for child in value:
                yield from YamlFile._mappings(child, seen)

    def render(self) -> str:
        stream = io.StringIO()
        self.yaml.dump(self.data, stream)
        return stream.getvalue()

    def save(self) -> None:
        self.path.write_text(self.render())


def mapping(parent: dict, key: str, *, create: bool = False) -> dict:
    """Require an ordinary editable mapping; don't silently replace custom shapes."""
    if key not in parent and create:
        parent[key] = CommentedMap()
    value = parent.get(key)
    if not isinstance(value, dict):
        raise EditError(f'{key}: expected a YAML mapping; edit this custom structure by hand')
    if isinstance(value, CommentedMap) and (value.merge or value.yaml_anchor()):
        raise EditError(f'{key}: uses a YAML anchor or merge; edit shared settings by hand')
    return value
