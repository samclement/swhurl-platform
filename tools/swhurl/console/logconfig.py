"""One-line JSON for console application, Uvicorn and action audit logs."""
from __future__ import annotations

import datetime as dt
import json
import logging

AUDIT_FIELDS = ('identity', 'action', 'unit', 'state', 'job_id', 'pr_url')


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        entry = {'time': dt.datetime.fromtimestamp(record.created, dt.UTC).isoformat(),
                 'level': record.levelname, 'logger': record.name, 'message': record.getMessage()}
        if record.name == 'uvicorn.access' and isinstance(record.args, tuple) and len(record.args) == 5:
            client, method, path, protocol, status = record.args
            entry.update(client_address=client, method=method, path=path, protocol=protocol, status=status)
        entry.update({key: getattr(record, key) for key in AUDIT_FIELDS if hasattr(record, key)})
        if record.exc_info:
            entry['exception'] = self.formatException(record.exc_info)
        return json.dumps(entry, ensure_ascii=False, separators=(',', ':'))


CONFIG = {
    'version': 1,
    'disable_existing_loggers': False,
    'formatters': {'json': {'()': JsonFormatter}},
    'handlers': {'stdout': {'class': 'logging.StreamHandler', 'stream': 'ext://sys.stdout', 'formatter': 'json'}},
    'root': {'handlers': ['stdout'], 'level': 'INFO'},
    'loggers': {name: {'handlers': ['stdout'], 'level': 'INFO', 'propagate': False}
                for name in ('uvicorn', 'uvicorn.error', 'uvicorn.access')},
}
