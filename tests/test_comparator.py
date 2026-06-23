"""Unit tests for comparison/tolerance logic."""

from validator import comparator
from validator.comparator_status import (
    MISMATCH, MISSING_IN_DB, MISSING_IN_INGEST, OK, SKIPPED,
)
from validator.config import Config
from validator.ingest_reader import Emitted
from validator.metric_map import GAUGE, SUM, Expected


def _cfg(**over):
    base = dict(
        host="h", port=1521, service="s", user="u", password="p",
        ingest_path="x", ingest_format="otlp-json", container_mode="pdb",
        tol_gauge=0.02, tol_counter=0.05, abs_epsilon=1.0, watch_interval=30.0,
        nr_api_key="", nr_account_id="", nr_nerdgraph_url="https://api.newrelic.com/graphql",
    )
    base.update(over)
    return Config(**base)


def _exp(metric, value, vt=SUM, attrs=None):
    return Expected(metric, attrs or {}, value, vt)


def _emt(metric, value, attrs=None):
    return Emitted(metric, attrs or {}, value, 0)


def test_ok_within_counter_tolerance():
    cfg = _cfg()
    e = _exp("oracledb.executions", 1000.0)
    a = _emt("oracledb.executions", 1040.0)  # +4% < 5%
    res = comparator.compare({e.key(): e}, {a.key(): a}, cfg)
    assert res[0].status == OK


def test_mismatch_beyond_counter_tolerance():
    cfg = _cfg()
    e = _exp("oracledb.executions", 1000.0)
    a = _emt("oracledb.executions", 1200.0)  # +20% > 5%
    res = comparator.compare({e.key(): e}, {a.key(): a}, cfg)
    assert res[0].status == MISMATCH
    assert comparator.has_failures(res)


def test_gauge_tighter_tolerance():
    cfg = _cfg()
    e = _exp("oracledb.pga_memory", 1000.0, vt=GAUGE)
    a = _emt("oracledb.pga_memory", 1040.0)  # +4% > 2% gauge tol
    res = comparator.compare({e.key(): e}, {a.key(): a}, cfg)
    assert res[0].status == MISMATCH


def test_near_zero_uses_absolute_epsilon():
    cfg = _cfg()
    e = _exp("oracledb.enqueue_deadlocks", 0.0)
    a = _emt("oracledb.enqueue_deadlocks", 1.0)  # within abs_epsilon=1
    res = comparator.compare({e.key(): e}, {a.key(): a}, cfg)
    assert res[0].status == OK


def test_missing_in_ingest_is_not_failure():
    cfg = _cfg()
    e = _exp("oracledb.executions", 100.0)
    res = comparator.compare({e.key(): e}, {}, cfg)
    assert res[0].status == MISSING_IN_INGEST
    assert not comparator.has_failures(res)


def test_computed_metric_skipped():
    cfg = _cfg()
    a = _emt("oracledb.host.cpu.utilization", 42.0)
    res = comparator.compare({}, {a.key(): a}, cfg)
    assert res[0].status == SKIPPED
    assert not comparator.has_failures(res)


def test_unmapped_emitted_metric_missing_in_db():
    cfg = _cfg()
    a = _emt("oracledb.some_unmapped_metric", 1.0)
    res = comparator.compare({}, {a.key(): a}, cfg)
    assert res[0].status == MISSING_IN_DB


def test_pdb_empty_matches_absent():
    cfg = _cfg()
    e = _exp("oracledb.executions", 100.0, attrs={"oracle.db.pdb": ""})
    a = _emt("oracledb.executions", 102.0, attrs={})
    res = comparator.compare({e.key(): e}, {a.key(): a}, cfg)
    assert len(res) == 1 and res[0].status == OK
