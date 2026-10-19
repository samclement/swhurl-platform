"""Reading and writing YAML: PyYAML's safe loader and dumper, through libyaml where PyYAML was built with it.

The pure-Python parser made the checks that read every instance and unit take seconds each, and the
verifier's tests minutes. Both loaders build the same objects and raise ``yaml.YAMLError``; both
dumpers write the same bytes, libyaml about eight times faster (rendering every instance on each edit).

Text already parsed is not parsed again: one verifier run reads the same files hundreds of times, and a
copy costs a tenth of a parse. The key is the text itself, so a changed file is always parsed afresh,
and every caller gets its own copy to change.
"""
import copy
from functools import lru_cache

import yaml

LOADER = getattr(yaml, 'CSafeLoader', yaml.SafeLoader)
DUMPER = getattr(yaml, 'CSafeDumper', yaml.SafeDumper)
NO_WRAP = 2**31 - 1  # libyaml takes an int width; the pure-Python dumper also took float('inf')


@lru_cache(maxsize=1024)
def _parsed(text: str) -> tuple:
    return tuple(yaml.load_all(text, Loader=LOADER))


def load(stream):
    """``yaml.safe_load``, faster."""
    if not isinstance(stream, str):
        return yaml.load(stream, Loader=LOADER)
    docs = _parsed(stream)
    if len(docs) > 1:  # as safe_load: one document only
        return yaml.load(stream, Loader=LOADER)
    return copy.deepcopy(docs[0]) if docs else None


def load_all(stream):
    """``yaml.safe_load_all``, faster; as lazy (it raises when read, after the documents before the error)."""
    if not isinstance(stream, str):
        yield from yaml.load_all(stream, Loader=LOADER)
        return
    try:
        docs = _parsed(stream)
    except yaml.YAMLError:
        yield from yaml.load_all(stream, Loader=LOADER)
        return
    for doc in docs:
        yield copy.deepcopy(doc)


def dump_all(docs, *, wrap: bool = True) -> str:
    """``yaml.safe_dump_all`` with keys in the order given and block style, faster; ``wrap=False`` keeps
    long values on one line."""
    return yaml.dump_all(docs, Dumper=DUMPER, sort_keys=False, default_flow_style=False,
                         **({} if wrap else {'width': NO_WRAP}))


def dump(doc, *, wrap: bool = True) -> str:
    """One document, as ``dump_all``."""
    return dump_all([doc], wrap=wrap)
