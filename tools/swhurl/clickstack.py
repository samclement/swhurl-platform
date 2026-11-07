"""ClickStack helpers the remaining Python shares: mongosh scripts over stdin and ClickHouse queries.

``make clickstack-bootstrap`` is ``swhurl-v2 clickstack-bootstrap``; what is left here serves the MongoDB restore
test (``recovery.py``), ``verify-logs`` and the team scenario. Scripts reach MongoDB over stdin
(``kubectl exec -i``), so no secret appears in a command line.
"""
from __future__ import annotations

import json
from dataclasses import dataclass

from swhurl.run import Runner

CLICKHOUSE_POD = 'clickstack-clickhouse-clickhouse-0-0-0'  # operator naming: <cluster>-clickhouse-<shard>-<replica>-0
MARKER = 'RESULT '


def _result(text: str) -> dict:
    """The JSON a script printed after ``RESULT ``; tools print warnings around it."""
    for line in text.splitlines():
        if line.startswith(MARKER):
            return json.loads(line[len(MARKER):])
    raise ValueError('no result line')


@dataclass(frozen=True)
class MongoPod:
    """Where a mongosh script runs: the restore test's throwaway MongoDB."""
    namespace: str
    pod: str
    container: str


def mongo(runner: Runner, uri: str, script: str, *, pod: MongoPod, mutating: bool = False) -> dict:
    """Run a mongosh ``script`` against ``uri``; it must print one ``RESULT`` line.

    The script and the connection string go over stdin, never in argv."""
    program = f'db = connect({json.dumps(uri)});\n{script}\n'
    out = runner.output(['kubectl', '-n', pod.namespace, 'exec', '-i', pod.pod, '-c', pod.container, '--',
                         'mongosh', '--quiet', '--nodb', '--norc', '--file', '/dev/stdin'],
                        input=program, secret_output=True, mutating=mutating)
    return _result(out)


TEAM_KEYS = ('print("RESULT " + JSON.stringify({keys: db.teams.distinct("apiKey")'
             '.filter(k => typeof k === "string" && k.length > 0)}));')
COLLECTION_COUNTS = ('const c = {}; db.getCollectionNames().sort().forEach(n => '
                     '{ c[n] = db[n].countDocuments(); }); print("RESULT " + JSON.stringify(c));')


def clickhouse(runner: Runner, query: str) -> str:
    return runner.output(['kubectl', '-n', 'observability', 'exec', CLICKHOUSE_POD, '--',
                          'clickhouse-client', '-q', query]).strip()
