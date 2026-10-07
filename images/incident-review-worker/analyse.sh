#!/usr/bin/env bash
# Analyse step of the incident reviewer (docs/plan.md section 14, "Runtime layout").
#
# Reads the redacted bundle, asks the pinned Codex CLI for one schema-constrained diagnosis and
# records the outcome. Every handled outcome exits 0 with a status file; the decide step turns it
# into a message, a state change and the Job's exit code. The model key is read from a file and
# passed to `codex login` on stdin: never in argv, never in the environment.
set -euo pipefail

WORK=${WORK:-/work}
REVIEW_HOME=${REVIEW_HOME:-/opt/review}
SCRATCH=${SCRATCH:-/scratch}
OPENAI_KEY_FILE=${OPENAI_KEY_FILE:-/run/secrets/openai/OPENAI_API_KEY}
ANALYSE_SECONDS=${ANALYSE_SECONDS:-300}
MODEL=${CODEX_MODEL:-}
MODE=${INCIDENT_REVIEW_MODE:-analyse}

bundle=$WORK/bundle/bundle.json
out=$WORK/diagnosis
started=$(date +%s)
cli=$(codex --version 2>/dev/null | tr -cd 'A-Za-z0-9. -' | cut -c1-100 || true)

finish() { # finish STATUS: write the status file and stop
  local model=$MODEL
  [[ $model =~ ^[A-Za-z0-9._-]{1,100}$ ]] || model=
  [[ $1 == ok ]] || rm -f "$out/diagnosis.json"
  printf '{"version":1,"status":"%s","cli":"%s","model":"%s","seconds":%d,"usage":null}\n' \
    "$1" "$cli" "$model" "$(($(date +%s) - started))" >"$out/status.json"
  echo "incident review analyse: status=$1"
  exit 0
}

rm -f "$out/status.json" "$out/diagnosis.json"
[[ -s $bundle ]] || finish skipped
[[ $MODE != collect-only ]] || finish disabled
[[ $MODEL =~ ^[A-Za-z0-9._-]{1,100}$ && -s $OPENAI_KEY_FILE && $ANALYSE_SECONDS =~ ^[0-9]+$ ]] || finish error

CODEX_HOME=$(mktemp -d "$SCRATCH/codex-home.XXXXXX")
export CODEX_HOME
workspace=$(mktemp -d "$SCRATCH/workspace.XXXXXX")
trap 'rm -rf "$CODEX_HOME" "$workspace"' EXIT

codex login --with-api-key <"$OPENAI_KEY_FILE" >/dev/null 2>&1 || finish error

# The CLI's own output can echo bundle or model text, so none of it reaches the pod log.
code=0
cat "$REVIEW_HOME/prompt.md" "$bundle" |
  timeout -s TERM -k 10 "$ANALYSE_SECONDS" codex exec --sandbox read-only --skip-git-repo-check --ephemeral \
    --ignore-user-config --color never --model "$MODEL" --cd "$workspace" \
    --output-schema "$REVIEW_HOME/diagnosis.schema.json" --output-last-message "$out/diagnosis.json" - \
    >/dev/null 2>&1 || code=$?

case $code in
  0) if [[ -s $out/diagnosis.json ]]; then finish ok; else finish error; fi ;;
  124 | 137 | 143) finish timeout ;;
  *) finish error ;;
esac
