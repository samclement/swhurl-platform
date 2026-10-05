"""Injectable boundaries for future incident-review model and GitHub adapters.

This module defines contracts and validation wrappers only. It contains no
network client and cannot contact a model provider or GitHub by itself.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Protocol

from swhurl.incident_review import (
    PolicyError,
    validate_diagnosis,
    validate_patch,
)


class ModelAdapter(Protocol):
    """A model implementation supplied by the caller, never constructed here."""

    def diagnose(self, evidence: dict[str, Any]) -> dict[str, Any]: ...

    def propose_patch(self, evidence: dict[str, Any], diagnosis: dict[str, Any]) -> dict[str, Any]: ...


@dataclass(frozen=True)
class DraftPullRequest:
    """Minimal result returned by an injected GitHub adapter."""

    number: int
    url: str


class GitHubAdapter(Protocol):
    """A GitHub implementation exposing only draft PR creation."""

    def create_draft_pull_request(self, *, repository: str, base_revision: str,
                                  title: str, body: str, files: list[dict[str, str]]) -> DraftPullRequest: ...


def request_diagnosis(adapter: ModelAdapter, evidence: dict[str, Any]) -> dict[str, Any]:
    """Call an injected model and accept only a diagnosis grounded in evidence."""
    return validate_diagnosis(adapter.diagnose(evidence), evidence)


def request_patch(adapter: ModelAdapter, evidence: dict[str, Any],
                  diagnosis: dict[str, Any]) -> dict[str, Any]:
    """Call an injected model and accept only a statically allowed candidate patch."""
    validated_diagnosis = validate_diagnosis(diagnosis, evidence)
    if validated_diagnosis["recommend_no_change"]:
        raise PolicyError("patch request refused because diagnosis recommends no change")
    patch = adapter.propose_patch(evidence, validated_diagnosis)
    return validate_patch(patch, repository=evidence["repository"],
                          base_revision=evidence["base_revision"])


def open_draft_pull_request(adapter: GitHubAdapter, patch: dict[str, Any], *,
                            title: str, body: str) -> DraftPullRequest:
    """Submit an already policy-checked patch through the draft-only adapter seam."""
    if not isinstance(patch, dict):
        raise PolicyError("draft PR patch must be an object")
    repository, base_revision = patch.get("repository"), patch.get("base_revision")
    if not isinstance(repository, str) or not isinstance(base_revision, str):
        raise PolicyError("draft PR patch needs a repository and base revision")
    validated = validate_patch(patch, repository=repository, base_revision=base_revision)
    if not isinstance(title, str) or not title.strip() or len(title) > 200:
        raise PolicyError("draft PR title must be non-empty and bounded")
    if not isinstance(body, str) or not body.strip() or len(body) > 10_000:
        raise PolicyError("draft PR body must be non-empty and bounded")
    result = adapter.create_draft_pull_request(
        repository=validated["repository"], base_revision=validated["base_revision"],
        title=title, body=body, files=validated["files"])
    if (not isinstance(result, DraftPullRequest) or isinstance(result.number, bool) or
            not isinstance(result.number, int) or result.number < 1 or
            result.url != f"https://github.com/{validated['repository']}/pull/{result.number}"):
        raise PolicyError("GitHub adapter returned an invalid draft PR reference")
    return result
