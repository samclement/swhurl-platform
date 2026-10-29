#!/usr/bin/env bash
# The platform files v2 renders from the records must be what Git holds: the egress allocation, the proxy's
# configuration and policy, the auth folder, and NATS's user list. Renders them into a temporary directory and
# compares, so it writes nothing in the checkout. The snapshot is of the working tree's tracked files (git stash create makes
# a commit object and touches neither the index nor the tree), so an uncommitted record change counts.
# make check-v2 runs it, which is how CI runs it. How to re-render: docs/services.md#egress-proxy.
set -Eeuo pipefail

cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

revision=$(git stash create)
make -s v2-snapshot REVISION="${revision:-HEAD}" OUT="$work/snapshot.json" >/dev/null
(cd v2 && go build -o "$work/swhurl-v2" ./cmd/swhurl-v2)
profile=v2/testdata/profile.json

"$work/swhurl-v2" egress --snapshot "$work/snapshot.json" --profile "$profile" \
  --previous platform/egress/allocation.json 2>/dev/null >"$work/allocation.json"
"$work/swhurl-v2" proxy --allocation "$work/allocation.json" --profile "$profile" --out "$work/out" >/dev/null
"$work/swhurl-v2" auth --snapshot "$work/snapshot.json" --profile "$profile" --out "$work/out" >/dev/null
"$work/swhurl-v2" users --snapshot "$work/snapshot.json" --profile "$profile" --out "$work/out" >/dev/null

failed=0
# The allocation records the commit it was taken at; every other fact must match the records.
if ! python3 -I - platform/egress/allocation.json "$work/allocation.json" <<'EOF'
import json, sys
committed, rendered = (json.load(open(path)) for path in sys.argv[1:])
committed.pop('revision', None)
rendered.pop('revision', None)
sys.exit(committed != rendered)
EOF
then
  echo "platform/egress/allocation.json is not what the records render (docs/services.md#egress-proxy):"
  diff <(python3 -I -m json.tool platform/egress/allocation.json) <(python3 -I -m json.tool "$work/allocation.json") || true
  failed=1
fi
# The rendered files are one long line each: name the file rather than print it.
for file in configmap.json networkpolicy.json; do
  if ! cmp -s "platform/egress/$file" "$work/out/egress/$file"; then
    echo "platform/egress/$file is not what the allocation renders"
    failed=1
  fi
done
if ! diff -rq platform/auth "$work/out/auth"; then
  echo "platform/auth is not what the records render"
  failed=1
fi
# NATS's user list is what the server serves today: the same users, the same keys and the same permissions, in
# the same order. The values inside the ConfigMap are JSON here and YAML there; both are YAML, so Helm reads
# them alike, and the comparison is what they say rather than how they are spelled.
if ! python3 -I - "platform/nats/users.yaml" "$work/out/nats/users.json" <<'EOF'
import json, sys, yaml
committed, rendered = yaml.safe_load(open(sys.argv[1])), json.load(open(sys.argv[2]))
ours, theirs = json.loads(rendered['data']['values.yaml']), yaml.safe_load(committed['data']['values.yaml'])
if ours != theirs or dict(rendered, data=None) != dict(committed, data=None):
    print('the user list differs from platform/nats/users.yaml')
    for index, (mine, yours) in enumerate(zip(ours['config']['merge']['authorization']['users'],
                                              theirs['config']['merge']['authorization']['users'])):
        if mine != yours:
            print(f'  user {index}: rendered {json.dumps(mine)}')
            print(f'  user {index}: committed {json.dumps(yours)}')
            break
    else:
        print(f'  {len(ours["config"]["merge"]["authorization"]["users"])} rendered, '
              f'{len(theirs["config"]["merge"]["authorization"]["users"])} committed')
    sys.exit(1)
sys.exit(0)
EOF
then
  failed=1
fi
[ "$failed" = 0 ] && echo "[OK] platform/egress, platform/auth and platform/nats are what the records render"
exit "$failed"
