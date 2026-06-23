"""Run the receiver's monitoring SQL directly against Oracle (ground truth).

Uses python-oracledb in thin mode by default (no Oracle Instant Client needed).
If the DB server enforces Native Network Encryption (NNE) — thin mode cannot
negotiate it — set VALIDATOR_ORACLE_THICK=1 (and optionally ORACLE_CLIENT_LIB_DIR)
to switch to thick mode, which requires an Oracle Instant Client install.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field

from . import metric_map
from .config import Config

_thick_initialized = False


def _ensure_thick_mode(oracledb, cfg: Config) -> None:
    """Initialize the Oracle Instant Client once, for NNE-enforcing servers."""
    global _thick_initialized
    if _thick_initialized:
        return
    kwargs = {"lib_dir": cfg.oracle_client_lib_dir} if cfg.oracle_client_lib_dir else {}
    oracledb.init_oracle_client(**kwargs)
    _thick_initialized = True


@dataclass
class ProbeResult:
    expected: dict[tuple, metric_map.Expected]  # Expected.key() -> Expected
    probe_time: float                            # epoch seconds when the probe ran
    errors: list[str] = field(default_factory=list)


def _rows_as_dicts(cursor) -> list[dict]:
    """Fetch all rows as dicts with UPPERCASE column names.

    Oracle returns duplicate column names for the resource-limit query (raw and
    normalized LIMIT_VALUE); last column wins, matching the receiver's row map.
    """
    cols = [d[0].upper() for d in cursor.description]
    result = []
    for rec in cursor.fetchall():
        row: dict[str, object] = {}
        for col, val in zip(cols, rec):
            row[col] = val  # later duplicate columns overwrite earlier ones
        result.append(row)
    return result


def probe(cfg: Config) -> ProbeResult:
    """Connect, run every mapped query once, and return expected data points."""
    import oracledb  # imported here so unit tests need not have the driver installed

    expected: dict[tuple, metric_map.Expected] = {}
    errors: list[str] = []

    if cfg.oracle_thick:
        _ensure_thick_mode(oracledb, cfg)
    conn = oracledb.connect(user=cfg.user, password=cfg.password, dsn=cfg.dsn)
    probe_time = time.time()
    try:
        for key in metric_map.all_query_keys():
            sql = metric_map.query_sql(key, cfg.is_cdb)
            try:
                with conn.cursor() as cur:
                    cur.execute(sql)
                    rows = _rows_as_dicts(cur)
            except Exception as exc:  # noqa: BLE001 - surface per-query failures, keep going
                errors.append(f"query {key!r} failed: {exc}")
                continue
            for exp in metric_map.expected_for(key, rows):
                expected[exp.key()] = exp
    finally:
        conn.close()

    return ProbeResult(expected=expected, probe_time=probe_time, errors=errors)
