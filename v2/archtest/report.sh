#!/usr/bin/env bash
# The v2 change report (make v2-report, docs/iteration-2-plan.md#guardrails): v2/ in the working
# tree, or at AFTER, compared with v2/ at the merge base of BASE and that revision. Revisions are
# read with git archive into a temporary directory, never through the index or working tree; the
# comparison is archtest's TestChangeReport. Exit 0 with a report, 1 when an input cannot be read,
# 2 for a wrong FORMAT or AFTER.
set -Eeuo pipefail

base_ref=${BASE:-origin/main}
after_ref=${AFTER:-}
format=${FORMAT:-text}
case $format in
  text | json) ;;
  *) echo "v2-report: FORMAT is text or json, not '$format'" >&2; exit 2 ;;
esac

repo=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# extract REV DIR: v2/ at REV into DIR/v2, which stays empty when REV has no v2.
extract() {
  mkdir -p "$2/v2"
  if git -C "$repo" cat-file -e "$1:v2" 2>/dev/null; then
    git -C "$repo" archive "$1" v2 | tar -x -C "$2"
  fi
}

# name REF COMMIT: how the report names a revision, "origin/main@1a2b3c4d" or just "1a2b3c4d".
name() {
  if [[ $2 == "$1"* ]]; then echo "${2:0:8}"; else echo "$1@${2:0:8}"; fi
}

# carried DIR FILE: the paths (NUL-separated, relative to the repository root) of the working tree's
# files under DIR as Git carries them - tracked, plus untracked but not ignored - so a build artifact
# there is not a change to v2.
carried() {
  git -C "$repo" ls-files -z --cached --others --exclude-standard -- "$1" > "$2"
}

if [[ -n $after_ref ]]; then
  after=$(git -C "$repo" rev-parse --verify --quiet "$after_ref^{commit}") ||
    { echo "v2-report: AFTER=$after_ref is not a commit" >&2; exit 2; }
  extract "$after" "$tmp/after"
  after_dir=$tmp/after/v2 after_name=$(name "$after_ref" "$after") after_list=()
else
  after=HEAD after_dir=$repo/v2 after_name="working tree"
  carried v2 "$tmp/paths"
  after_list=(-after-list "$tmp/paths")
fi
base=$(git -C "$repo" merge-base "$base_ref" "$after") ||
  { echo "v2-report: BASE=$base_ref has no merge base with ${after_ref:-HEAD}" >&2; exit 1; }
extract "$base" "$tmp/base"
mkdir "$tmp/out"

cd "$repo/v2"
export GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0
if ! go test -count=1 -run '^TestChangeReport$' ./archtest -args \
  -base "$tmp/base/v2" -base-name "$(name "$base_ref" "$base")" \
  -after "$after_dir" -after-name "$after_name" -out "$tmp/out" ${after_list[@]+"${after_list[@]}"} > "$tmp/log" 2>&1; then
  cat "$tmp/log" >&2
  exit 1
fi
cat "$tmp/out/report.$([[ $format == json ]] && echo json || echo txt)"
