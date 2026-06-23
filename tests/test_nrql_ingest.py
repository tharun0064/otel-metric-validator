"""Unit tests for the NRQL ingest delta-check (no network)."""

import json

from validator import ingest_check, nrql_probe
from validator.comparator_status import (
    INGEST_MISMATCH, INGEST_NO_DATA, INGEST_OK, INGEST_SKIPPED,
)
from validator.config import Config
from validator.ingest_reader import Series, read_otlp_series
from validator.metric_map import SCOPE_NAME


def _cfg(**over):
    base = dict(
        host="h", port=1521, service="s", user="u", password="p",
        ingest_path="x", ingest_format="otlp-json", container_mode="pdb",
        tol_gauge=0.02, tol_counter=0.05, abs_epsilon=1.0, watch_interval=30.0,
        nr_api_key="k", nr_account_id="123", nr_nerdgraph_url="http://nerdgraph",
    )
    base.update(over)
    return Config(**base)


# ---- query builders ----
def test_delta_query_shape_and_window():
    q = nrql_probe.build_delta_query("oracledb.executions", {"oracle.db.pdb": "PDB2"}, 1000, 2000)
    assert q == ("SELECT sum(oracledb.executions) FROM Metric "
                 "WHERE oracle.db.pdb = 'PDB2' SINCE 1000 UNTIL 2000")


def test_latest_query_no_attrs():
    q = nrql_probe.build_latest_query("oracledb.pga_memory", {}, 10, 20)
    assert q == "SELECT latest(oracledb.pga_memory) FROM Metric SINCE 10 UNTIL 20"


def test_where_escapes_single_quotes():
    q = nrql_probe.build_delta_query("m", {"tablespace_name": "O'BRIEN"}, 1, 2)
    assert "tablespace_name = 'O\\'BRIEN'" in q


def test_graphql_embeds_account_and_nrql():
    g = nrql_probe.build_graphql("999", "SELECT sum(m) FROM Metric")
    assert "account(id: 999)" in g and 'nrql(query: "SELECT sum(m) FROM Metric")' in g


def test_to_ms():
    assert nrql_probe.to_ms(1_500_000_000) == 1500


# ---- response parser ----
def test_parse_scalar_sum():
    resp = {"data": {"actor": {"account": {"nrql": {"results": [{"sum.oracledb.executions": 4242.0}]}}}}}
    val, err = nrql_probe.parse_nrql_scalar(resp)
    assert val == 4242.0 and err is None


def test_parse_scalar_empty_results():
    resp = {"data": {"actor": {"account": {"nrql": {"results": []}}}}}
    val, err = nrql_probe.parse_nrql_scalar(resp)
    assert val is None and err is None


def test_parse_scalar_graphql_error():
    resp = {"errors": [{"message": "bad nrql"}]}
    val, err = nrql_probe.parse_nrql_scalar(resp)
    assert val is None and "bad nrql" in err


# ---- ingest_check core (client stubbed) ----
class _StubClient:
    def __init__(self, value, err=None):
        self.value, self.err = value, err
        self.last_nrql = None

    def run(self, nrql):
        self.last_nrql = nrql
        return self.value, self.err


def _series(metric, fv, ft, lv, lt, n=2, attrs=None):
    return Series(metric, attrs or {}, fv, ft, lv, lt, n)


def test_counter_delta_ok(monkeypatch):
    cfg = _cfg()
    # emitted cumulative 1000 -> 1100 => expected delta 100; NRDB sum returns 100
    s = _series("oracledb.executions", 1000.0, 1_000_000_000, 1100.0, 11_000_000_000)
    stub = _StubClient(100.0)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: stub)
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_OK
    assert res[0].expected == 100.0
    assert "SINCE 1001 UNTIL 11000" in stub.last_nrql   # first-point delta excluded


def test_counter_delta_mismatch(monkeypatch):
    cfg = _cfg()
    s = _series("oracledb.executions", 1000.0, 1_000_000_000, 1100.0, 11_000_000_000)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: _StubClient(500.0))  # NRDB wrong
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_MISMATCH
    assert ingest_check.has_failures(res)


def test_gauge_uses_latest(monkeypatch):
    cfg = _cfg()
    s = _series("oracledb.pga_memory", 2000.0, 1_000_000_000, 2400.0, 11_000_000_000)
    stub = _StubClient(2400.0)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: stub)
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_OK
    assert res[0].expected == 2400.0          # latest, not a delta
    assert "latest(oracledb.pga_memory)" in stub.last_nrql


def test_single_point_counter_skipped(monkeypatch):
    cfg = _cfg()
    s = _series("oracledb.executions", 1000.0, 1_000_000_000, 1000.0, 1_000_000_000, n=1)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: _StubClient(0.0))
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_SKIPPED


def test_computed_metric_skipped(monkeypatch):
    cfg = _cfg()
    s = _series("oracledb.host.cpu.utilization", 1.0, 1_000_000_000, 2.0, 11_000_000_000)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: _StubClient(2.0))
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_SKIPPED


def test_no_data(monkeypatch):
    cfg = _cfg()
    s = _series("oracledb.executions", 1000.0, 1_000_000_000, 1100.0, 11_000_000_000)
    monkeypatch.setattr(nrql_probe, "NrqlClient", lambda cfg: _StubClient(None, None))
    res = ingest_check.check_ingest({s.key(): s}, cfg)
    assert res[0].status == INGEST_NO_DATA


# ---- series reader endpoints ----
def test_read_otlp_series_endpoints(tmp_path):
    def line(value, ts):
        return json.dumps({"resourceMetrics": [{"scopeMetrics": [{
            "scope": {"name": SCOPE_NAME},
            "metrics": [{"name": "oracledb.executions", "sum": {"dataPoints": [
                {"attributes": [], "asInt": str(value), "timeUnixNano": str(ts)}]}}],
        }]}]})
    p = tmp_path / "m.json"
    p.write_text(line(1000, 100) + "\n" + line(1050, 200) + "\n" + line(1100, 300) + "\n")
    series = read_otlp_series(str(p))
    s = series[("oracledb.executions", frozenset())]
    assert (s.first_value, s.last_value, s.n_points) == (1000.0, 1100.0, 3)
    assert s.first_ts == 100 and s.last_ts == 300
