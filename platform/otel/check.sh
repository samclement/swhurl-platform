#!/usr/bin/env bash
# check-otel: each OTel collector's config as its HelmRelease renders it, judged by the collector image the render
# names (docs/contributing.md#validation). For each platform/otel/helmrelease-*.yaml:
#   helm template   renders the release's chart at its pinned version from its HelmRepository, with its values;
#   validate        runs in the rendered workload's image with the rendered config, unmodified, and the rendered
#                   container's --feature-gates;
#   log fixtures    when the config has transform processors, tests/fixtures/logs.json goes through its logs
#                   pipeline's transform and filter processors in the same image (fixtures.jq builds the input and
#                   the runtime config and compares the output; a failure never prints a log body or a value).
# Only the pod's environment is stubbed, never the config: an empty read-only directory at each of the workload's
# volume mounts, a service-account token placeholder with a throwaway CA, the API server's variables, and each
# container variable's literal value or, where the cluster fills it in (valueFrom), 127.0.0.1.
# A collector that rejects a config or a fixture is a finding: the script reports it, checks the rest and exits 1.
# A render, container runtime or tool failure stops the script at once with exit 1.
#
# CONTAINER_RUNTIME names the runtime (podman by default; CI sets docker). Needs helm, kubectl (YAML to JSON
# offline: patch --local never contacts a cluster), jq, openssl and curl; reads the chart and the image from the
# network, so the image is pulled once.
set -Eeuo pipefail
if [ "$#" -ne 0 ]; then
  echo 'usage: check.sh (no arguments; CONTAINER_RUNTIME=docker uses docker)' >&2
  exit 2
fi
runtime=${CONTAINER_RUNTIME:-podman}
cd "$(git rev-parse --show-toplevel)"
here=platform/otel
umask 022 # the collector runs as its image's user and reads what is mounted
work=$(mktemp -d)
chmod 755 "$work"
container=
cleanup() {
  if [ -n "$container" ]; then "$runtime" rm -f "$container" >/dev/null 2>&1 || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
stop() {
  echo "[BAD] $1" >&2
  exit 1
}

# kubectl reads YAML as Flux and Helm do; the empty kubeconfig names no cluster.
: >"$work/kubeconfig"
json() { KUBECONFIG="$work/kubeconfig" kubectl patch --local --type merge -p '{}' -o json -f "$1" | jq -s .; }
# Helm keeps its repository index and chart cache here, not in the operator's.
export HELM_CACHE_HOME="$work/helm" HELM_CONFIG_HOME="$work/helm" HELM_DATA_HOME="$work/helm"

json clusters/home/flux-system/sources/helmrepositories.yaml >"$work/repositories.json" \
  || stop 'could not read the HelmRepositories'
mkdir "$work/empty" "$work/serviceaccount" "$work/config"
echo placeholder >"$work/serviceaccount/token"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout /dev/null -subj /CN=check-otel \
  -days 1 -out "$work/serviceaccount/ca.crt" 2>"$work/error" || stop "openssl: $(head -c 2000 "$work/error")"

# docker.io/<name> for a reference without a registry, as the kubelet resolves it.
qualified() {
  case $1 in
    */*) case ${1%%/*} in *.* | *:* | localhost) echo "$1" ;; *) echo "docker.io/$1" ;; esac ;;
    *) echo "docker.io/library/$1" ;;
  esac
}

# Runs tests/fixtures/logs.json's cases of one pipeline through the rendered processors; prints the problems.
fixtures() {
  local image=$1 kind=$2 port=''
  shift 2
  local dir="$work/fixtures"
  rm -rf "$dir" && mkdir -p "$dir/pods"
  jq --arg kind "$kind" -L "$here" 'include "fixtures"; cases($kind)' tests/fixtures/logs.json >"$work/cases.json"
  jq --slurpfile cases "$work/cases.json" -L "$here" 'include "fixtures"; runtime($cases[0])' \
    "$work/relay.json" >"$dir/runtime.json"
  if [ "$(jq '.service.pipelines.logs.processors | length' "$dir/runtime.json")" = 0 ]; then
    echo 'the logs pipeline has no transform or filter processor to run the fixtures through'
    return
  fi
  jq -L "$here" 'include "fixtures"; payload' "$work/cases.json" >"$work/payload.json"
  mapfile -t framed < <(jq -r -L "$here" 'include "fixtures"; .[] | select(.framing) | framed_path
    | ltrimstr("/var/log/pods/")' "$work/cases.json")
  for i in "${!framed[@]}"; do
    mkdir -p "$(dirname "$dir/pods/${framed[$i]}")"
    jq -r --argjson i "$i" '[.[] | select(.framing)][$i].framing[]' "$work/cases.json" >"$dir/pods/${framed[$i]}"
  done

  container=check-otel-$$-$RANDOM
  "$runtime" run --rm --name "$container" -p 127.0.0.1::4318 -v "$dir:/fixtures:ro" -v "$dir/pods:/var/log/pods:ro" \
    "$image" --config=file:/fixtures/runtime.json "$@" >"$work/output.json" 2>/dev/null &
  local pid=$! status=0
  # Its own log is not shown: a processor's error can quote a record. Parallel checks can delay startup, so the
  # collector has 30 seconds, and it is stopped as soon as every record has flushed.
  local deadline=$((SECONDS + 30))
  until port=$("$runtime" port "$container" 4318/tcp 2>/dev/null | head -n 1) && [ -n "$port" ] \
    && [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 "http://$port/" || true)" != 000 ]; do
    # Readiness is any HTTP answer to a GET, so the fixtures are posted exactly once.
    if ! kill -0 "$pid" 2>/dev/null || [ "$SECONDS" -ge "$deadline" ]; then
      quit 'the fixture collector did not become ready for OTLP input'
      return
    fi
    sleep 0.1
  done
  if ! curl -sS --fail --max-time 5 -H 'Content-Type: application/json' --data-binary "@$work/payload.json" \
    "http://$port/v1/logs" >"$work/response.json" 2>/dev/null; then
    # Not retried: the collector may have accepted the batch, and a second post would duplicate every record.
    quit 'the fixture collector response failed after the single OTLP submission'
    return
  fi
  if ! jq -e '(.partialSuccess.rejectedLogRecords // 0 | tonumber) == 0' "$work/response.json" >/dev/null 2>&1; then
    quit 'the collector rejected fixture records'
    return
  fi
  # The last line can be half written: an output jq cannot read yet is not complete.
  until jq -e -s --slurpfile cases "$work/cases.json" -L "$here" 'include "fixtures"; records | complete($cases[0])' \
    "$work/output.json" >/dev/null 2>&1; do
    if ! kill -0 "$pid" 2>/dev/null; then
      quit 'the fixture collector stopped before all records were flushed'
      return
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      quit 'the fixture collector timed out before all records were flushed'
      return
    fi
    sleep 0.1
  done
  "$runtime" stop --time 10 "$container" >/dev/null 2>&1 || true # SIGTERM: the collector flushes and exits
  wait "$pid" || status=$?
  container=
  if [ "$status" != 0 ]; then
    echo "the fixture collector exited $status"
    return
  fi
  jq -r -s --slurpfile cases "$work/cases.json" -L "$here" 'include "fixtures"; records | verdict($cases[0])[]' \
    "$work/output.json" 2>/dev/null || echo 'the fixture collector output was incomplete'
}

# A fixture problem that ends the run: report it and remove the collector.
quit() {
  echo "$1"
  "$runtime" rm -f "$container" >/dev/null 2>&1 || true
  container=
}

failures=0
for file in "$here"/helmrelease-*.yaml; do
  json "$file" >"$work/release.json" || stop "could not read $file"
  read -r name namespace chart version source < <(jq -r '.[0] | [.spec.releaseName // .metadata.name,
    .spec.targetNamespace // .metadata.namespace, .spec.chart.spec.chart, .spec.chart.spec.version,
    .spec.chart.spec.sourceRef.name] | @tsv' "$work/release.json") || stop "$file: not a HelmRelease with a chart"
  url=$(jq -r --arg name "$source" '.[] | select(.kind == "HelmRepository" and .metadata.name == $name) | .spec.url' \
    "$work/repositories.json")
  [ -n "$url" ] || stop "$file: no HelmRepository $source"
  jq '.[0].spec.values // {}' "$work/release.json" >"$work/values.json"
  helm template "$name" "$chart" --repo "$url" --version "$version" --namespace "$namespace" \
    -f "$work/values.json" >"$work/rendered.yaml" 2>"$work/error" \
    || stop "$file: helm template $chart $version failed: $(head -c 2000 "$work/error")"
  json "$work/rendered.yaml" >"$work/objects.json" || stop "$file: could not read the rendered objects"

  jq -j '[.[] | select(.kind == "ConfigMap" and (.data.relay | type) == "string")] | if length == 1 then .[0].data.relay
    else error("expected one ConfigMap with a relay key, found \(length)") end' "$work/objects.json" \
    >"$work/config/relay.yaml" 2>"$work/error" || stop "$file: $(head -c 2000 "$work/error")"
  jq '[.[] | select(.kind == "DaemonSet" or .kind == "Deployment" or .kind == "StatefulSet")]
    | if length == 1 then .[0].spec.template.spec.containers[0] else error("expected one collector workload, found \(length)") end' \
    "$work/objects.json" >"$work/container.json" 2>"$work/error" || stop "$file: $(head -c 2000 "$work/error")"
  image=$(qualified "$(jq -r .image "$work/container.json")")
  mapfile -t gates < <(jq -r '.args[]? | select(startswith("--feature-gates="))' "$work/container.json")
  mapfile -t environment < <(jq -r '.env[]? | "\(.name)=\(if has("value") then .value else "127.0.0.1" end)"' \
    "$work/container.json")
  mapfile -t mounts < <(jq -r '.volumeMounts[]?.mountPath' "$work/container.json")
  pod=(-e KUBERNETES_SERVICE_HOST=127.0.0.1 -e KUBERNETES_SERVICE_PORT=443
    -v "$work/serviceaccount:/var/run/secrets/kubernetes.io/serviceaccount:ro")
  for variable in "${environment[@]}"; do pod+=(-e "$variable"); done
  for mount in "${mounts[@]}"; do pod+=(-v "$work/empty:$mount:ro"); done

  status=0
  "$runtime" run --rm --network none "${pod[@]}" -v "$work/config:/check:ro" "$image" \
    validate "${gates[@]}" --config=file:/check/relay.yaml >"$work/validate.log" 2>&1 || status=$?
  case $status in
    0) echo "[OK] $name: $image accepts the rendered config ($file)" ;;
    1)
      echo "[BAD] $name: $image rejects the rendered config ($file):"
      sed 's/^/  /' "$work/validate.log"
      failures=$((failures + 1))
      continue
      ;;
    *) stop "$name: $runtime could not run $image (exit $status): $(head -c 2000 "$work/validate.log")" ;;
  esac

  # The fixtures' runtime config is built from this JSON, so it is read as YAML 1.1, as the Python harness read it.
  { printf 'apiVersion: check-otel/v1\nkind: Relay\n'; cat "$work/config/relay.yaml"; } >"$work/relay.yaml"
  json "$work/relay.yaml" | jq '.[0] | del(.apiVersion, .kind)' >"$work/relay.json" \
    || stop "$file: could not read the rendered config"
  jq -e '.processors // {} | keys | any(startswith("transform/"))' "$work/relay.json" >/dev/null || continue
  kind=node
  if jq -e '.processors | has("transform/events")' "$work/relay.json" >/dev/null; then kind=events; fi
  fixtures "$image" "$kind" "${gates[@]}" >"$work/problems"
  if [ -s "$work/problems" ]; then
    sed 's/^/[BAD] /' "$work/problems"
    failures=$((failures + $(wc -l <"$work/problems")))
  else
    echo "[OK] $name: $(jq length "$work/cases.json") log fixtures passed through $image ($kind)"
  fi
done
[ "$failures" = 0 ] || exit 1
