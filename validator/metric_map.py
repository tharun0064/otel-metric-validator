"""Declarative mirror of the nroracledbreceiver scraper.

For each Oracle monitoring query the receiver runs, we record how it turns rows
into metric data points: which stat/row maps to which metric, what attributes it
attaches, and any unit transform (e.g. cpu_time is divided by 100).

Source of truth:
  opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go
    - SQL constants            (lines ~39-125)
    - v$sysstat stat->Record   (switch ~455-570)
    - session / resource_limit / tablespace / sga / storage / data_dict handlers

Phase 1 (implemented here): metrics that map directly from a single query row,
optionally with a fixed unit transform. Phase 2 (receiver-computed values such as
the v$sysmetric utilization/ratio metrics) are listed in COMPUTED_SKIP and reported
as SKIPPED rather than silently dropped.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Callable

SCOPE_NAME = "github.com/newrelic-forks/opentelemetry-collector-contrib/receiver/nroracledbreceiver"

SUM = "sum"      # cumulative monotonic counter -> counter tolerance
GAUGE = "gauge"  # level snapshot -> gauge tolerance


@dataclass(frozen=True)
class Expected:
    """One expected data point derived from the DB."""
    metric: str
    attrs: dict[str, str]
    value: float
    value_type: str

    def key(self) -> tuple[str, frozenset[tuple[str, str]]]:
        return (self.metric, _norm_attrs(self.attrs))


def _norm_attrs(attrs: dict[str, str]) -> frozenset[tuple[str, str]]:
    """Drop empty-valued attributes so a missing/empty oracle.db.pdb matches on both sides."""
    return frozenset((k, str(v)) for k, v in attrs.items() if v not in (None, ""))


# ---------------------------------------------------------------------------
# SQL (verbatim from scraper.go). Keyed by logical name; pdb vs cdb variants.
# ---------------------------------------------------------------------------
_STATS = "select * from v$sysstat"
_STATS_CDB = (
    "SELECT s.name AS NAME, s.value AS VALUE, c.name AS PDB_NAME "
    "FROM v$con_sysstat s JOIN v$containers c ON s.con_id = c.con_id"
)
_SESSION = "select status, type, count(*) as VALUE FROM v$session GROUP BY status, type"
_SESSION_CDB = (
    "select s.status, s.type, c.name as PDB_NAME, count(*) as VALUE "
    "FROM v$session s, v$containers c WHERE s.con_id = c.con_id(+) "
    "GROUP BY s.status, s.type, c.name"
)
_RESOURCE_LIMITS = (
    "select RESOURCE_NAME, CURRENT_UTILIZATION, LIMIT_VALUE, "
    "CASE WHEN TRIM(INITIAL_ALLOCATION) LIKE 'UNLIMITED' THEN '-1' ELSE TRIM(INITIAL_ALLOCATION) END as INITIAL_ALLOCATION, "
    "CASE WHEN TRIM(LIMIT_VALUE) LIKE 'UNLIMITED' THEN '-1' ELSE TRIM(LIMIT_VALUE) END as LIMIT_VALUE "
    "from v$resource_limit"
)
_TABLESPACE = (
    "select um.TABLESPACE_NAME, um.USED_SPACE, um.TABLESPACE_SIZE, ts.BLOCK_SIZE "
    "FROM DBA_TABLESPACE_USAGE_METRICS um INNER JOIN DBA_TABLESPACES ts "
    "ON um.TABLESPACE_NAME = ts.TABLESPACE_NAME"
)
_TABLESPACE_CDB = (
    "SELECT c.name AS PDB_NAME, t.TABLESPACE_NAME, m.USED_SPACE, m.TABLESPACE_SIZE, t.BLOCK_SIZE "
    "FROM CDB_TABLESPACE_USAGE_METRICS m, CDB_TABLESPACES t, v$containers c "
    "WHERE m.con_id(+) = t.con_id AND t.con_id = c.con_id AND m.TABLESPACE_NAME(+) = t.TABLESPACE_NAME"
)
_SGA = "SELECT NAME, BYTES FROM v$sgainfo"
_DATA_DICT = (
    "SELECT (1-(SUM(getmisses)/SUM(gets))) * 100 as DATA_DICTIONARY_HIT_RATIO "
    "FROM v$rowcache WHERE getmisses + gets <> 0"
)
_STORAGE = (
    "WITH total_bytes AS (SELECT SUM(bytes) AS total FROM dba_data_files) "
    "SELECT (total - (SELECT SUM(bytes) FROM dba_free_space)) AS USED_DB_SIZE, "
    "total AS ALLOCATED_DB_SIZE FROM total_bytes"
)

SGA_MAX_COMPONENT = "Maximum SGA Size"


def query_sql(key: str, is_cdb: bool) -> str:
    table = {
        "sysstat": (_STATS, _STATS_CDB),
        "session_count": (_SESSION, _SESSION_CDB),
        "resource_limits": (_RESOURCE_LIMITS, _RESOURCE_LIMITS),
        "tablespace": (_TABLESPACE, _TABLESPACE_CDB),
        "sga": (_SGA, _SGA),
        "data_dict": (_DATA_DICT, _DATA_DICT),
        "storage": (_STORAGE, _STORAGE),
    }
    pdb_sql, cdb_sql = table[key]
    return cdb_sql if is_cdb else pdb_sql


def all_query_keys() -> list[str]:
    return ["sysstat", "session_count", "resource_limits", "tablespace", "sga", "data_dict", "storage"]


# ---------------------------------------------------------------------------
# v$sysstat: stat NAME -> metric
# ---------------------------------------------------------------------------
# Simple cumulative counters: Record<Metric>(value, pdb), no transform.
_SYSSTAT_COUNTERS: dict[str, str] = {
    "execute count": "oracledb.executions",
    "parse count (total)": "oracledb.parse_calls",
    "parse count (hard)": "oracledb.hard_parses",
    "enqueue deadlocks": "oracledb.enqueue_deadlocks",
    "exchange deadlocks": "oracledb.exchange_deadlocks",
    "logons cumulative": "oracledb.logons",
    "user commits": "oracledb.user_commits",
    "user rollbacks": "oracledb.user_rollbacks",
    "physical reads": "oracledb.physical_reads",
    "physical reads direct": "oracledb.physical_reads_direct",
    "physical read IO requests": "oracledb.physical_read_io_requests",
    "physical writes": "oracledb.physical_writes",
    "physical writes direct": "oracledb.physical_writes_direct",
    "physical write IO requests": "oracledb.physical_write_io_requests",
    "queries parallelized": "oracledb.queries_parallelized",
    "DDL statements parallelized": "oracledb.ddl_statements_parallelized",
    "DML statements parallelized": "oracledb.dml_statements_parallelized",
    "Parallel operations not downgraded": "oracledb.parallel_operations_not_downgraded",
    "Parallel operations downgraded to serial": "oracledb.parallel_operations_downgraded_to_serial",
    "Parallel operations downgraded 1 to 25 pct": "oracledb.parallel_operations_downgraded_1_to_25_pct",
    "Parallel operations downgraded 25 to 50 pct": "oracledb.parallel_operations_downgraded_25_to_50_pct",
    "Parallel operations downgraded 50 to 75 pct": "oracledb.parallel_operations_downgraded_50_to_75_pct",
    "Parallel operations downgraded 75 to 99 pct": "oracledb.parallel_operations_downgraded_75_to_99_pct",
    "session logical reads": "oracledb.logical_reads",
    "db block gets": "oracledb.db_block_gets",
    "consistent gets": "oracledb.consistent_gets",
}

# Counters that carry a unit transform.
_SYSSTAT_TRANSFORMED: dict[str, tuple[str, Callable[[float], float]]] = {
    # value is expressed in tens of milliseconds -> seconds
    "CPU used by this session": ("oracledb.cpu_time", lambda v: v / 100.0),
}

# Gauges sourced from v$sysstat (level, not cumulative).
_SYSSTAT_GAUGES: dict[str, str] = {
    "session pga memory": "oracledb.pga_memory",
}

# Multi-attribute counters: stat -> (metric, fixed attrs).
_SYSSTAT_IO: dict[str, tuple[str, dict[str, str]]] = {
    "physical read bytes": ("oracledb.physical_io.transferred", {"disk.io.direction": "read", "disk.io.type": "buffered"}),
    "physical write bytes": ("oracledb.physical_io.transferred", {"disk.io.direction": "write", "disk.io.type": "buffered"}),
    "physical read total bytes": ("oracledb.physical_io.transferred", {"disk.io.direction": "read", "disk.io.type": "total"}),
    "physical write total bytes": ("oracledb.physical_io.transferred", {"disk.io.direction": "write", "disk.io.type": "total"}),
    "physical read total IO requests": ("oracledb.physical_io.requests", {"disk.io.direction": "read", "disk.io.block_size": "all"}),
    "physical write total IO requests": ("oracledb.physical_io.requests", {"disk.io.direction": "write", "disk.io.block_size": "all"}),
    "physical read total multi block requests": ("oracledb.physical_io.requests", {"disk.io.direction": "read", "disk.io.block_size": "multi"}),
    "physical write total multi block requests": ("oracledb.physical_io.requests", {"disk.io.direction": "write", "disk.io.block_size": "multi"}),
    "physical writes from cache": ("oracledb.physical_io.cache_writes", {}),
    "bytes received via SQL*Net from client": ("oracledb.sqlnet.io.transferred", {"network.io.direction": "receive", "destination.type": "client"}),
    "bytes sent via SQL*Net to client": ("oracledb.sqlnet.io.transferred", {"network.io.direction": "transmit", "destination.type": "client"}),
    "bytes received via SQL*Net from dblink": ("oracledb.sqlnet.io.transferred", {"network.io.direction": "receive", "destination.type": "dblink"}),
    "bytes sent via SQL*Net to dblink": ("oracledb.sqlnet.io.transferred", {"network.io.direction": "transmit", "destination.type": "dblink"}),
}

# v$resource_limit RESOURCE_NAME -> [(metric, column, value_type)]
_RESOURCE_LIMIT_MAP: dict[str, list[tuple[str, str]]] = {
    "processes": [("oracledb.processes.usage", "CURRENT_UTILIZATION"), ("oracledb.processes.limit", "LIMIT_VALUE")],
    "sessions": [("oracledb.sessions.limit", "LIMIT_VALUE")],
    "enqueue_locks": [("oracledb.enqueue_locks.usage", "CURRENT_UTILIZATION"), ("oracledb.enqueue_locks.limit", "LIMIT_VALUE")],
    "dml_locks": [("oracledb.dml_locks.usage", "CURRENT_UTILIZATION"), ("oracledb.dml_locks.limit", "LIMIT_VALUE")],
    "enqueue_resources": [("oracledb.enqueue_resources.usage", "CURRENT_UTILIZATION"), ("oracledb.enqueue_resources.limit", "LIMIT_VALUE")],
    "transactions": [("oracledb.transactions.usage", "CURRENT_UTILIZATION"), ("oracledb.transactions.limit", "LIMIT_VALUE")],
}

# Receiver-computed metrics (mostly v$sysmetric / v$osstat derived). Reported as SKIPPED.
COMPUTED_SKIP: frozenset[str] = frozenset({
    "oracledb.database.cpu.utilization",
    "oracledb.host.cpu.utilization",
    "oracledb.buffer_cache.utilization",
    "oracledb.library_cache.utilization",
    "oracledb.shared_pool.utilization",
    "oracledb.database.wait.utilization",
    "oracledb.execution.utilization",
    "oracledb.parse.utilization",
    "oracledb.parse.rate",
    "oracledb.sort.ratio",
    "oracledb.redo_allocation.utilization",
    "oracledb.sql_service.response.duration",
    "oracledb.storage.utilization",
    "oracledb.system.cpu.load",
    "system.cpu.physical.count",
    "system.memory.limit",
    "oracledb.recycle_bin.limit",
})


def _build_value_types() -> dict[str, str]:
    """metric name -> 'sum' | 'gauge', for consumers that only know the metric name."""
    vt: dict[str, str] = {}
    for metric in _SYSSTAT_COUNTERS.values():
        vt[metric] = SUM
    for metric, _ in _SYSSTAT_TRANSFORMED.values():
        vt[metric] = SUM
    for metric, _ in _SYSSTAT_IO.values():
        vt[metric] = SUM
    for metric in _SYSSTAT_GAUGES.values():
        vt[metric] = GAUGE
    for pairs in _RESOURCE_LIMIT_MAP.values():
        for metric, _ in pairs:
            vt[metric] = GAUGE
    for metric in (
        "oracledb.sessions.usage",
        "oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit",
        "oracledb.sga.usage", "oracledb.sga.limit",
        "oracledb.data_dictionary.hit_ratio", "oracledb.storage.usage",
    ):
        vt[metric] = GAUGE
    return vt


METRIC_VALUE_TYPE: dict[str, str] = _build_value_types()


def value_type_of(metric: str) -> str | None:
    """Return 'sum'/'gauge' for a Phase-1 metric, or None if not directly mapped."""
    return METRIC_VALUE_TYPE.get(metric)


def _to_float(raw: object) -> float | None:
    if raw is None or raw == "":
        return None
    try:
        return float(raw)
    except (TypeError, ValueError):
        return None


# ---------------------------------------------------------------------------
# Extractors: rows (list of dict, UPPERCASE column names) -> list[Expected]
# ---------------------------------------------------------------------------
def _pdb_attr(row: dict) -> dict[str, str]:
    pdb = row.get("PDB_NAME")
    return {"oracle.db.pdb": pdb} if pdb else {}


def extract_sysstat(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        name = row.get("NAME")
        value = _to_float(row.get("VALUE"))
        if name is None or value is None:
            continue
        base = _pdb_attr(row)
        if name in _SYSSTAT_COUNTERS:
            out.append(Expected(_SYSSTAT_COUNTERS[name], dict(base), value, SUM))
        elif name in _SYSSTAT_TRANSFORMED:
            metric, fn = _SYSSTAT_TRANSFORMED[name]
            out.append(Expected(metric, dict(base), fn(value), SUM))
        elif name in _SYSSTAT_GAUGES:
            out.append(Expected(_SYSSTAT_GAUGES[name], dict(base), value, GAUGE))
        elif name in _SYSSTAT_IO:
            metric, attrs = _SYSSTAT_IO[name]
            out.append(Expected(metric, {**attrs, **base}, value, SUM))
    return out


def extract_session_count(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        value = _to_float(row.get("VALUE"))
        if value is None:
            continue
        attrs = {"session_type": row.get("TYPE", ""), "session_status": row.get("STATUS", "")}
        attrs.update(_pdb_attr(row))
        out.append(Expected("oracledb.sessions.usage", attrs, value, GAUGE))
    return out


def extract_resource_limits(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        name = row.get("RESOURCE_NAME")
        for metric, col in _RESOURCE_LIMIT_MAP.get(name, []):
            value = _to_float(row.get(col))
            if value is None:
                continue
            out.append(Expected(metric, {}, value, GAUGE))
    return out


def extract_tablespace(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        used = _to_float(row.get("USED_SPACE"))
        block = _to_float(row.get("BLOCK_SIZE"))
        ts_name = row.get("TABLESPACE_NAME")
        if used is None or block is None or not ts_name:
            continue
        attrs = {"tablespace_name": ts_name}
        attrs.update(_pdb_attr(row))
        out.append(Expected("oracledb.tablespace_size.usage", dict(attrs), used * block, GAUGE))
        size = row.get("TABLESPACE_SIZE")
        size_f = _to_float(size)
        limit_val = -1.0 if (size in (None, "")) else size_f * block
        out.append(Expected("oracledb.tablespace_size.limit", dict(attrs), limit_val, GAUGE))
    return out


def extract_sga(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        name = row.get("NAME")
        value = _to_float(row.get("BYTES"))
        if name is None or value is None:
            continue
        if name == SGA_MAX_COMPONENT:
            out.append(Expected("oracledb.sga.limit", {}, value, GAUGE))
        else:
            out.append(Expected("oracledb.sga.usage", {"oracledb.sga.component.name": name}, value, GAUGE))
    return out


def extract_data_dict(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        value = _to_float(row.get("DATA_DICTIONARY_HIT_RATIO"))
        if value is not None:
            out.append(Expected("oracledb.data_dictionary.hit_ratio", {}, value, GAUGE))
    return out


def extract_storage(rows: list[dict]) -> list[Expected]:
    out: list[Expected] = []
    for row in rows:
        value = _to_float(row.get("USED_DB_SIZE"))
        if value is not None:
            out.append(Expected("oracledb.storage.usage", {}, value, GAUGE))
    return out


_EXTRACTORS: dict[str, Callable[[list[dict]], list[Expected]]] = {
    "sysstat": extract_sysstat,
    "session_count": extract_session_count,
    "resource_limits": extract_resource_limits,
    "tablespace": extract_tablespace,
    "sga": extract_sga,
    "data_dict": extract_data_dict,
    "storage": extract_storage,
}


def expected_for(query_key: str, rows: list[dict]) -> list[Expected]:
    """Run the extractor for a query's rows. Rows use UPPERCASE column keys."""
    return _EXTRACTORS[query_key](rows)
