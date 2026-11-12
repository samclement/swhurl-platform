-- Aggregate coverage of logs observed in the last {minutes:UInt32} minutes.
-- Run as documented in docs/services.md#structured-logs. No bodies or attribute values are returned.
-- No rows means no logs were observed in this window, not that every running container is healthy.
SELECT ResourceAttributes['k8s.namespace.name'] AS namespace,
       ResourceAttributes['k8s.pod.name'] AS pod,
       ResourceAttributes['k8s.container.name'] AS container,
       ResourceAttributes['service.name'] AS service,
       if(LogAttributes['log.parser'] != '', LogAttributes['log.parser'],
          if(ScopeName = 'node-logger', 'native-otlp', 'unclassified')) AS parser,
       count() AS records,
       countIf(SeverityNumber > 0) AS with_severity,
       countIf(TraceId != '') AS with_trace,
       countIf(mapContains(LogAttributes, 'log.record.original')) AS with_original,
       max(Timestamp) AS latest
FROM default.otel_logs
WHERE Timestamp > now() - INTERVAL {minutes:UInt32} MINUTE
GROUP BY namespace, pod, container, service, parser
ORDER BY namespace, pod, container, parser
FORMAT JSONEachRow
