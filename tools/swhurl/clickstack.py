"""ClickStack helpers the remaining Python shares: ClickHouse queries.

``make clickstack-bootstrap`` is ``swhurl-v2 clickstack-bootstrap``; what is left here serves ``verify-logs`` and
the team scenario.
"""
from __future__ import annotations

from swhurl.run import Runner

CLICKHOUSE_POD = 'clickstack-clickhouse-clickhouse-0-0-0'  # operator naming: <cluster>-clickhouse-<shard>-<replica>-0


def clickhouse(runner: Runner, query: str) -> str:
    return runner.output(['kubectl', '-n', 'observability', 'exec', CLICKHOUSE_POD, '--',
                          'clickhouse-client', '-q', query]).strip()
