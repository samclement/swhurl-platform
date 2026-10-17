"""Reading YAML: PyYAML's safe loader, through libyaml where PyYAML was built with it.

The pure-Python parser made the checks that read every instance and unit take seconds each, and the
verifier's tests minutes. Both loaders build the same objects and raise ``yaml.YAMLError``.
"""
import yaml

LOADER = getattr(yaml, 'CSafeLoader', yaml.SafeLoader)


def load(stream):
    """``yaml.safe_load``, faster."""
    return yaml.load(stream, Loader=LOADER)


def load_all(stream):
    """``yaml.safe_load_all``, faster."""
    return yaml.load_all(stream, Loader=LOADER)
