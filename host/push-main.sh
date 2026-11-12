#!/usr/bin/env bash
# How a bot pushes to main (docs/architecture.md#how-changes-reach-the-cluster): the Render and Deploy v2 workflows'
# one step each, and Validate's console image pin.
#
# Each attempt starts from the newest main in a fresh worktree, makes the change, checks the result, commits the
# whole tree with main as its parent and pushes without force. A push lost to another writer starts again from the
# newer main, up to five times; a change or check that fails ends the run with nothing pushed, and is not retried.
#
#   render                       v2's one render (make v2-render); checked with check-repo and check-apps
#   deploy                       make v2-deploy, then the render; checked with check-v2. An instance that refused or
#                                failed is held: the others are pushed and the run exits 1
#   console-image --expect TAG   images/pin-console.sh and the render, only while main builds TAG (src-<hash>);
#                                checked with check-repo and check-apps
#
# The commit identity comes from GIT_AUTHOR_* and GIT_COMMITTER_* in the environment.
set -Eeuo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
attempts=5
usage="usage: $0 render | deploy | console-image --expect src-<hash>"

change=${1:-}
expect=
case "$change" in
  render | deploy) (( $# == 1 )) || { echo "$usage" >&2; exit 2; } ;;
  console-image) [[ $# == 3 && "$2" == --expect && "$3" == src-* ]] || { echo "$usage" >&2; exit 2; }; expect=$3 ;;
  *) echo "$usage" >&2; exit 2 ;;
esac

work='' tmp=''
cleanup() {
  if [[ -n "$work" ]]; then git -C "$root" worktree remove --force "$work" >/dev/null 2>&1 || true; fi
  if [[ -n "$tmp" ]]; then rm -rf "$tmp"; fi
  work='' tmp=''
}
trap cleanup EXIT

# Every path the worktree changed, staged.
changed() { git -C "$work" add -A && git -C "$work" diff --cached --name-only; }

# The second path component of every path under $1/, sorted and comma-separated.
names_under() { sed -n "s|^$1/\([^/]*\)/.*|\1|p" | sort -u | paste -sd, - | sed 's/,/, /g'; }

# Sets message (empty: nothing to push) and held (1: an instance refused or failed).
held=0
make_change() {
  message=
  case "$change" in
    render)
      (cd "$work" && make -s v2-render) >/dev/null
      paths=$(changed)
      if [[ -z "$paths" ]]; then echo 'main is rendered (another writer rendered it); nothing to do.'; return; fi
      apps=$(names_under apps <<<"$paths")
      message=$(printf 'render: %s (render workflow)\n\n%s' "${apps:-shared files}" "$paths") ;;
    deploy)
      held=0
      (cd "$work" && make v2-deploy) 2>&1 || held=1
      (cd "$work" && make -s v2-render) >/dev/null
      paths=$(changed)
      if [[ -z "$paths" ]]; then echo 'Nothing changed.'; return; fi
      moved=$(names_under v2-records <<<"$paths")
      message=$(printf 'deploy: %s (v2 deploy workflow)\n\n%s' "${moved:-the v2 staging images}" "$paths") ;;
    console-image)
      current=$(bash "$work/images/content-tag.sh" console)
      if [[ "$current" != "$expect" ]]; then
        echo "[OK] nothing to do: main now builds $current, not $expect; its own Validate run pins it"; return
      fi
      bash "$work/images/pin-console.sh"
      if [[ -z "$(changed)" ]]; then return; fi
      # On a main whose records Render has not rendered yet the checks would fail, so the pin carries the render.
      (cd "$work" && make -s v2-render) >/dev/null
      message="deploy: console image $expect (Validate)" ;;
  esac
}

# The checks the tree must pass. A failure shows the whole output: CI has no other copy of it.
check() {
  local checks=(-j2 --output-sync=target check-repo check-apps)
  if [[ "$change" == deploy ]]; then checks=(check-v2); fi
  if ! (cd "$work" && make "${checks[@]}") >"$tmp/checks" 2>&1; then
    cat "$tmp/checks" >&2
    echo "[ERROR] make ${checks[*]} failed; nothing was pushed" >&2
    exit 1
  fi
}

for (( attempt = 1; attempt <= attempts; attempt++ )); do
  git -C "$root" fetch --quiet origin main
  base=$(git -C "$root" rev-parse origin/main)
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/bot-XXXXXX")
  work=$tmp/work
  git -C "$root" worktree add --quiet --detach "$work" "$base"
  make_change
  if [[ -z "$message" ]]; then exit "$held"; fi
  check
  git -C "$work" add -A
  tree=$(git -C "$work" write-tree)
  made=$(printf '%s\n' "$message" | git -C "$work" commit-tree "$tree" -p "$base" -F -)
  # Not forced: a main that moved since the fetch refuses it, and the change is made again.
  if git -C "$work" push --quiet origin "$made:refs/heads/main"; then
    echo "pushed ${made:0:12} to main"
    exit "$held"
  fi
  echo "main moved before the push (attempt $attempt of $attempts); trying again on the newer main"
  cleanup
done
echo "[ERROR] main kept moving: $attempts pushes lost; the next run tries again" >&2
exit 1
