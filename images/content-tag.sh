#!/usr/bin/env bash
# The content tag Validate publishes an image under, src-<16 hex>: the SHA-256 of the Git object IDs of what the
# image is built from, as committed at HEAD, one per line in the order below (the order changes the hash). CI's
# image jobs tag with it, make verify-platform compares the console's with it and images/pin-console.sh pins it.
# --committed refuses when an input differs from HEAD, so the tag names what the working tree holds.
# Usage: images/content-tag.sh console|backup [--committed]
set -Eeuo pipefail
cd "$(dirname "$0")/.."
case "${1:-}" in
  console) paths=(images/console .dockerignore pyproject.toml uv.lock tools v2
                  clusters/home/flux-system/sources/configmap-platform-settings.yaml) ;;
  backup) paths=(images/backup .dockerignore) ;;
  *) echo "usage: $0 console|backup [--committed]" >&2; exit 2 ;;
esac
if [[ "${2:-}" == --committed && -n "$(git status --porcelain -- "${paths[@]}")" ]]; then
  echo "the $1 image's inputs have uncommitted changes; commit and push them, wait for the Validate run" >&2
  exit 1
fi
ids=()
for p in "${paths[@]}"; do
  id=$(git rev-parse "HEAD:$p") # one missing path stops the script: a tag of fewer inputs would be wrong
  ids+=("$id")
done
hash=$(printf '%s\n' "${ids[@]}" | sha256sum | cut -c1-16)
echo "src-$hash"
