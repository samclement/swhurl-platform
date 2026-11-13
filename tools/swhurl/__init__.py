"""What is left of the v1 operator tooling: the Runner, YAML I/O and the chart acquisition the ClickStack
render test uses. Tests import it with ``tools/`` on ``PYTHONPATH``; no make target runs it.
"""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
"""Repository root."""
