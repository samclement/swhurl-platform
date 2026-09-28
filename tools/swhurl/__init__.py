"""Operator tooling for the Swhurl platform.

Run as ``python3 -m swhurl <command>`` with ``tools/`` on ``PYTHONPATH``; the
Makefile does this for every target. External tools (kubectl, flux, helm,
sops, age) are always called through :class:`swhurl.run.Runner`.
"""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
"""Repository root."""
