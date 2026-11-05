#!/usr/bin/env bash
# check-templates: each app type, rendered from its stack's template, still fits the platform.
#
# For each stack in v2/contract/stacks.json: clone its template at the pinned revision and render each app type built
# with it with Copier, as make app-repo does (the type's answers are the only ones the template accepts, and each must
# be one the rendered app's .copier-answers.yml records). Then, for each rendered app:
#   swhurl-v2 names              every SWHURL_* or platform.swhurl.com/* name it uses is one v2/contract/names.json lists;
#   swhurl-v2 image-declaration  the label its image build will write;
#   swhurl-v2 declaration        the creation rules an instance of that label would face (a kind v2 does not model
#                                yet, refused as unsupported, is reported as a gap, not a failure).
# One set of answers no type has, a worker with a database, must be refused by the template's own validator.
# So what a build will produce is proved with no image, no registry and no write. Renders and checks run in parallel.
#
# SWHURL_V2 names the binary, as CI names the one it builds; without it the script builds one into its temporary
# folder. Needs network (GitHub), git, jq, and Copier on PATH or uvx.
set -Eeuo pipefail
if [ "$#" -ne 0 ]; then
  echo 'usage: check-templates.sh (no arguments; checks every stack)' >&2
  exit 2
fi
cd "$(git rev-parse --show-toplevel)"
stacks=v2/contract/stacks.json
names=v2/contract/names.json
app=contract-check
# How Copier says it refused an answer: the template's validator, or a choice the question does not offer.
refused="Validation error for question|Invalid choice for"
outside=(database=sqlite kind=worker) # no app type has these answers, so the template must refuse them
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

v2=${SWHURL_V2:-}
if [ -z "$v2" ]; then
  echo '[INFO] SWHURL_V2 names no binary: building swhurl-v2 from v2/'
  v2="$work/swhurl-v2"
  (cd v2 && GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -o "$v2" ./cmd/swhurl-v2)
fi
if command -v copier >/dev/null; then
  copier=(copier)
else
  copier=(uvx "$(jq -r .copier "$stacks")")
fi
# A template names no team (the creator chooses one): any registered team serves the check.
team=$(jq -r '.teams | keys_unsorted | .[0] // ""' v2-records/teams.json)

# render DIR TEMPLATE ANSWER...: Copier into DIR/app; DIR/kind is manifest, missing, unasked, refused or error, DIR/why
# says why. Copier ignores an answer to a question the template does not ask, so each answer must be one the app's
# .copier-answers.yml records.
render() {
  local dir=$1 template=$2 status=0 data=(--data "app_name=$app") answer
  shift 2
  for answer in "$@"; do data+=(--data "$answer"); done
  "${copier[@]}" copy --defaults --quiet --trust "${data[@]}" "$template" "$dir/app" >"$dir/copier.log" 2>&1 || status=$?
  if [ "$status" -eq 0 ]; then
    if [ -f "$dir/app/swhurl.yaml" ]; then echo manifest; else echo missing; fi >"$dir/kind"
    for answer in "$@"; do
      if ! grep -qx "${answer%%=*}: ${answer#*=}" "$dir/app/.copier-answers.yml" 2>/dev/null; then
        echo unasked >"$dir/kind"
        echo "the template does not record ${answer}: it no longer asks ${answer%%=*}, or answers it otherwise" >"$dir/why"
        break
      fi
    done
  elif grep -Eq "$refused" "$dir/copier.log"; then
    echo refused >"$dir/kind"
    grep -E "$refused" "$dir/copier.log" | tail -n 1 |
      sed -E "s/^[A-Za-z]+Error: //; s/^Validation error for question '[^']*': //; s/ *$//" >"$dir/why"
  else
    echo error >"$dir/kind"
    echo "copier copy exited $status: $(tail -c 400 "$dir/copier.log")" >"$dir/why"
  fi
}

# check DIR LABEL: the platform half for one rendered app, as [OK], [WARN] and [BAD] lines in DIR/report.
check() {
  local dir=$1 label=$2 status=0 unlisted said
  unlisted=$("$v2" names --catalogue "$names" --app-dir "$dir/app" 2>"$dir/names.err") || status=$?
  if [ "$status" -eq 1 ] && [ -n "$unlisted" ]; then
    echo "[BAD] $label: $names does not list ${unlisted//$'\n'/, }"
  elif [ "$status" -ne 0 ]; then
    echo "[BAD] $label: swhurl-v2 names failed: $(cat "$dir/names.err")"
  fi
  status=0
  "$v2" image-declaration --app-dir "$dir/app" >"$dir/label.txt" 2>"$dir/producer.err" || status=$?
  if [ "$status" -ne 0 ]; then
    echo "[BAD] $label: the image-declaration producer refused the app: $(cat "$dir/producer.err")"
    return
  fi
  status=0
  said=$("$v2" declaration --label "$dir/label.txt" --app "$app" --team "$team" --exposure private 2>&1 >/dev/null) ||
    status=$?
  if [ "$status" -eq 0 ]; then
    echo "[OK] $label: the label applies to a new instance"
  elif [[ $said == unsupported:* ]]; then
    echo "[WARN] $label: v2 does not model it yet ($said)"
  else
    echo "[BAD] $label: swhurl-v2 declaration refused the label: $said"
  fi
}

# report FILE LABEL: one rendered app's lines, in type order; fails when any is [BAD], and when a check that stopped
# early left no result line.
report() {
  if ! grep -Eq '^\[(OK|WARN|BAD)\]' "$1" 2>/dev/null; then
    echo "[BAD] $2: the check stopped before it reported a result"
    return 1
  fi
  cat "$1"
  ! grep -q '^\[BAD\]' "$1"
}

failed=0
if ! stack_list=$(jq -er '.stacks | keys_unsorted[]' "$stacks"); then
  echo "[BAD] $stacks lists no stack"
  exit 1
fi
for stack in $stack_list; do
  repository=$(jq -er --arg s "$stack" '.stacks[$s].repository' "$stacks")
  revision=$(jq -er --arg s "$stack" '.stacks[$s].revision' "$stacks")
  template="$work/template-$stack"
  git clone --quiet --depth 1 "https://github.com/$repository.git" "$template"
  git -C "$template" fetch --quiet --depth 1 origin "$revision"
  git -C "$template" checkout --quiet --detach FETCH_HEAD
  # Copier renders a plain folder as it stands but clones a Git one for every render; the checkout pins the revision.
  rm -rf "$template/.git"

  # One line per type built with this stack: its key, then its answers sorted by name.
  if ! type_list=$(jq -er --arg s "$stack" '.types | to_entries[] | select(.value.stacks | index($s))
    | "\(.key) \(.value.answers | to_entries | sort_by(.key) | map("\(.key)=\(.value)") | join(" "))"' "$stacks"); then
    echo "[BAD] $stack: $stacks lists no app type built with it"
    failed=1
    continue
  fi
  mapfile -t types <<<"$type_list"
  labels=() # one per type, then the outside answers
  for i in "${!types[@]}"; do
    read -r key answers <<<"${types[$i]}"
    labels+=("$stack $answers ($key)")
    mkdir -p "$work/$stack-$i"
    # shellcheck disable=SC2086 # the answers are name=value words
    render "$work/$stack-$i" "$template" $answers &
  done
  outside_dir="$work/$stack-outside"
  mkdir -p "$outside_dir"
  render "$outside_dir" "$template" "${outside[@]}" &
  wait

  outside_label="$stack ${outside[*]}"
  case "$(cat "$outside_dir/kind" 2>/dev/null)" in
    refused) echo "[OK] $outside_label: the template refuses it ($(cat "$outside_dir/why"))" ;;
    manifest | missing | unasked) echo "[BAD] $outside_label: no app type has these answers, but the template accepts them"; failed=1 ;;
    *) echo "[BAD] $outside_label: no app type has these answers, but the template failed otherwise: $(cat "$outside_dir/why" 2>/dev/null)"; failed=1 ;;
  esac
  for i in "${!types[@]}"; do
    dir="$work/$stack-$i"
    case "$(cat "$dir/kind" 2>/dev/null)" in
      manifest) check "$dir" "${labels[$i]}" >"$dir/report" & ;;
      missing) echo "[BAD] ${labels[$i]}: the template rendered no swhurl.yaml" >"$dir/report" ;;
      refused) echo "[BAD] ${labels[$i]}: the template refuses it ($(cat "$dir/why"))" >"$dir/report" ;;
      unasked) echo "[BAD] ${labels[$i]}: $(cat "$dir/why")" >"$dir/report" ;;
      *) echo "[BAD] ${labels[$i]}: $(cat "$dir/why" 2>/dev/null || echo 'the render stopped before it reported')" >"$dir/report" ;;
    esac
  done
  wait
  for i in "${!types[@]}"; do
    report "$work/$stack-$i/report" "${labels[$i]}" || failed=1
  done
done
exit "$failed"
