# The log fixtures (tests/fixtures/logs.json) through a collector's own log processors. check.sh runs the
# collector; this module builds its input and its runtime config and compares what it exported. A verdict names
# the case and the field, never a log body or a value.

# OTLP/JSON AnyValue, for synthetic records only.
def any_value:
  if type == "string" then {stringValue: .}
  elif type == "boolean" then {boolValue: .}
  elif type == "number" then (if . == floor then {intValue: tostring} else {doubleValue: .} end)
  elif type == "object" then {kvlistValue: {values: (to_entries | map({key, value: (.value | any_value)}))}}
  elif type == "array" then {arrayValue: {values: map(any_value)}}
  else {} end;

def attributes: to_entries | map({key, value: (.value | any_value)});

def unpack:
  if has("kvlistValue") then [.kvlistValue.values[]? | {(.key): (.value | unpack)}] | add // {}
  elif has("arrayValue") then [.arrayValue.values[]? | unpack]
  elif has("intValue") then .intValue | tonumber
  else [.[]][0] end;

def record_attributes: [.attributes[]? | {(.key): (.value | unpack)}] | add // {};

# The cases of one pipeline: "node" (the DaemonSet's) or "events" (the cluster collector's).
def cases($kind): map(select((.pipeline // "node") == $kind));

def framed_path: "/var/log/pods/fixture_\(.resource["k8s.pod.name"])_00000000-0000-0000-0000-000000000000/main/0.log";

# Every unframed case as one OTLP request; input: cases.
def payload:
  {resourceLogs: map(select(.framing | not) | {
    resource: {attributes: ((.resource // {}) | attributes)},
    scopeLogs: [{logRecords: [{body: (.input | any_value),
                               attributes: ({"test.case": .name} + (.attributes // {}) | attributes)} + (.record // {})]}]})};

# The rendered logs pipeline's transform and filter processors, in its order, between an OTLP receiver and a file
# exporter on stdout; a framed case reads its lines through a copy of the rendered file_log receiver, which tags
# them after the container parser. Input: the rendered config.
def runtime($cases):
  [.service.pipelines.logs.processors[]? | select(startswith("transform/") or startswith("filter/"))] as $names
  | .receivers.file_log as $file_log
  | [$cases[] | select(.framing)] as $framed
  | {receivers: ({otlp: {protocols: {http: {endpoint: "0.0.0.0:4318"}}}}
                 + ($framed | map(.name as $case | {key: "file_log/\($case)", value: (
                     $file_log + {include: [framed_path], exclude: [], start_at: "beginning", poll_interval: "100ms"}
                     | .operators += [{type: "add", field: "attributes[\"test.case\"]", value: $case}])})
                   | from_entries)),
     processors: (.processors | with_entries(select(.key | IN($names[])))),
     exporters: {file: {path: "/dev/stdout", flush_interval: "100ms"}},
     service: {telemetry: {logs: {level: "error"}},
               pipelines: {logs: {receivers: (["otlp"] + ($framed | map("file_log/\(.name)"))),
                                  processors: $names, exporters: ["file"]}}}};

# Every log record in the exporter's output; input: its lines, slurped.
def records: [.[] | .resourceLogs[]? | .scopeLogs[]? | .logRecords[]?];

# True once a record arrived for every case that is not meant to be dropped. The unframed cases travel in one
# request, so a record a processor drops is already gone when the others flush. Input: records.
def complete($cases):
  [.[] | record_attributes["test.case"]] as $seen
  | all($cases[] | select(.dropped | not); .name as $name | any($seen[]; . == $name));

def expected_errors($record; $attrs):
  .name as $name
  | .expected | to_entries[]
  | if .key == "attributes" then
      .value | to_entries[] | select($attrs[.key] != .value) | "\($name): incorrect attribute \(.key)"
    else
      .key as $key
      | (if $key == "body" then ($record.body // {} | unpack)
         elif $record | has($key) then $record[$key] else 0 end) as $actual
      | select($actual != .value) | "\($name): incorrect \($key)"
    end;

def preserved_errors($record; $attrs):
  .name as $name | .expected as $expected
  | ((.record // {}) | to_entries[] | select((.key as $key | $expected | has($key) | not) and $record[.key] != .value)
     | "\($name): existing \(.key) changed"),
    ((.attributes // {}) | to_entries[] | select($attrs[.key] != .value) | "\($name): existing attribute \(.key) changed");

def original_errors($attrs):
  (if has("original") then .original else .input end) as $original
  | if ($original | type) != "string" then empty
    elif (if .expected | has("body") then .expected.body else $original end) == $original then empty
    elif $attrs["log.record.original"] != $original then "\(.name): original line missing or changed"
    else empty end;

def case_errors($matches):
  if .dropped then
    if ($matches | length) > 0 then "\(.name): expected the record to be dropped, got \($matches | length)" else empty end
  elif ($matches | length) != 1 then "\(.name): expected one record, got \($matches | length)"
  else $matches[0] as [$record, $attrs]
    | expected_errors($record; $attrs), preserved_errors($record; $attrs), original_errors($attrs)
  end;

# Each case's problems, in case order; input: records.
def verdict($cases):
  map([., record_attributes]) as $found
  | [$cases[] | .name as $name | case_errors([$found[] | select(.[1]["test.case"] == $name)])];
