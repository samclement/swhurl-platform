#!/usr/bin/env bash
# Pin the console to the image Validate published for this checkout (docs/console.md#deploy-a-new-console): the
# content tag of HEAD (images/content-tag.sh, refused while an input is uncommitted), the index digest GHCR serves
# for it (anonymously; the package is public) and the tag and digest lines of the manifests that run it. Edits files
# only: commit and push to deploy. Validate's deploy job runs it on the newest main through host/push-main.sh.
#
# platform/console/helmrelease.yaml holds exactly one tag: and one digest: line; in the notify service's HelmRelease,
# which runs the same image, the lines ending "# image: console" are pinned. Anything else stops with nothing written.
# Usage: images/pin-console.sh
set -Eeuo pipefail
cd "$(dirname "$0")/.."
repository=samclement/swhurl-console
releases=(platform/console/helmrelease.yaml platform/notify/helmrelease.yaml)
markers=('' console)

tag=$(bash images/content-tag.sh console --committed)

# The token goes to curl on stdin, never on its command line or to the output.
token=$(curl -fsS "https://ghcr.io/token?scope=repository:$repository:pull" 2>/dev/null |
  sed -n 's/.*"token" *: *"\([^"]*\)".*/\1/p') || true
digest=
if [[ -n "$token" ]]; then
  digest=$(printf 'header = "Authorization: Bearer %s"\n' "$token" |
    curl -sS --head --config - \
      --header 'Accept: application/vnd.oci.image.index.v1+json' \
      --header 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' \
      "https://ghcr.io/v2/$repository/manifests/$tag" |
    tr -d '\r' | awk 'NR == 1 && $2 != 200 { exit } tolower($1) == "docker-content-digest:" { print $2; exit }') || true
fi
if [[ -z "$digest" ]]; then
  echo "[ERROR] ghcr.io/$repository:$tag is not published yet; push, then wait for the \"Validate\" run of that push to build it" >&2
  exit 1
fi

# One file's pinned text on stdout. Without a marker exactly one tag: and one digest: line is replaced; with one,
# every such line ending "# image: <marker>", and there must be at least one of each.
pin() {
  awk -v tag="$tag" -v digest="$digest" -v marker="$2" -v file="$1" '
    function replace(key, value,   body, suffix) {
      suffix = marker == "" ? "" : " # image: " marker
      if (substr($0, length($0) - length(suffix) + 1) != suffix) return 0
      body = substr($0, 1, length($0) - length(suffix))
      if (body !~ ("^[ \t]+" key ": [^ \t]+$")) return 0
      sub(key ": [^ \t]+$", key ": " value, body)
      $0 = body suffix
      return 1
    }
    function check(key, count) {
      if (marker == "" && count != 1) { printf "[ERROR] %s must have exactly one image %s: line (found %d)\n", file, key, count > "/dev/stderr"; return 1 }
      if (marker != "" && count < 1) { printf "[ERROR] %s has no %s: line marked \"# image: %s\"\n", file, key, marker > "/dev/stderr"; return 1 }
      return 0
    }
    { tags += replace("tag", tag); digests += replace("digest", digest); print }
    END { failed = check("tag", tags); failed = check("digest", digests) || failed; exit failed }' "$1"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
changed=()
for i in "${!releases[@]}"; do
  file=${releases[$i]} marker=${markers[$i]}
  if [[ -n "$marker" && ! -e "$file" ]]; then continue; fi
  pin "$file" "$marker" >"$work/$i"
  if ! cmp -s "$file" "$work/$i"; then changed+=("$i"); fi
done
if (( ${#changed[@]} == 0 )); then
  echo "[OK] already pinned to $tag@$digest"
  exit 0
fi
names=()
for i in "${changed[@]}"; do
  cat "$work/$i" >"${releases[$i]}"
  names+=("${releases[$i]}")
done
echo "[OK] pinned ${names[*]} to $tag@$digest"
