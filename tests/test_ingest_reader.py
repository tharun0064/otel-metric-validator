"""Unit tests for the ingest readers (OTLP JSON + debug log)."""

import json

from validator import ingest_reader
from validator.metric_map import SCOPE_NAME


def _otlp_line(metrics):
    return json.dumps({
        "resourceMetrics": [{
            "resource": {"attributes": []},
            "scopeMetrics": [{
                "scope": {"name": SCOPE_NAME},
                "metrics": metrics,
            }],
        }]
    })


def test_otlp_json_gauge_and_sum(tmp_path):
    metrics = [
        {"name": "oracledb.pga_memory", "gauge": {"dataPoints": [
            {"attributes": [{"key": "oracle.db.pdb", "value": {"stringValue": "PDB2"}}],
             "asInt": "2450077376", "timeUnixNano": "100"}]}},
        {"name": "oracledb.cpu_time", "sum": {"dataPoints": [
            {"attributes": [{"key": "oracle.db.pdb", "value": {"stringValue": "PDB2"}}],
             "asDouble": 75.93, "timeUnixNano": "100"}]}},
    ]
    p = tmp_path / "m.json"
    p.write_text(_otlp_line(metrics) + "\n")

    out = ingest_reader.read_otlp_json(str(p))
    pga = out[("oracledb.pga_memory", frozenset({("oracle.db.pdb", "PDB2")}))]
    cpu = out[("oracledb.cpu_time", frozenset({("oracle.db.pdb", "PDB2")}))]
    assert pga.value == 2450077376.0
    assert cpu.value == 75.93


def test_otlp_json_keeps_latest_by_timestamp(tmp_path):
    older = _otlp_line([{"name": "oracledb.executions", "sum": {"dataPoints": [
        {"attributes": [], "asInt": "100", "timeUnixNano": "100"}]}}])
    newer = _otlp_line([{"name": "oracledb.executions", "sum": {"dataPoints": [
        {"attributes": [], "asInt": "250", "timeUnixNano": "200"}]}}])
    p = tmp_path / "m.json"
    p.write_text(older + "\n" + newer + "\n")

    out = ingest_reader.read_otlp_json(str(p))
    assert out[("oracledb.executions", frozenset())].value == 250.0


def test_otlp_json_filters_foreign_scope(tmp_path):
    line = json.dumps({"resourceMetrics": [{"scopeMetrics": [{
        "scope": {"name": "some.other.receiver"},
        "metrics": [{"name": "oracledb.executions", "sum": {"dataPoints": [
            {"attributes": [], "asInt": "100", "timeUnixNano": "1"}]}}],
    }]}]})
    p = tmp_path / "m.json"
    p.write_text(line + "\n")
    assert ingest_reader.read_otlp_json(str(p)) == {}


DEBUG_LOG = """\
2026-06-22T10:48:36 info Metrics {"resource": "..."}
Metric #0
Descriptor:
     -> Name: oracledb.cpu_time
     -> Unit: s
     -> DataType: Sum
NumberDataPoints #0
Data point attributes:
     -> oracle.db.pdb: Str(PDB2)
StartTimestamp: 2026-06-22 10:00:00 +0000 UTC
Timestamp: 2026-06-22 10:48:36 +0000 UTC
Value: 75.930000
Metric #1
Descriptor:
     -> Name: oracledb.sessions.usage
     -> DataType: Gauge
NumberDataPoints #0
Data point attributes:
     -> session_type: Str(USER)
     -> session_status: Str(ACTIVE)
Value: 12
"""


def test_debug_log_parser(tmp_path):
    p = tmp_path / "c.log"
    p.write_text(DEBUG_LOG)
    out = ingest_reader.read_debug_log(str(p))

    cpu = out[("oracledb.cpu_time", frozenset({("oracle.db.pdb", "PDB2")}))]
    assert cpu.value == 75.93
    sess = out[("oracledb.sessions.usage",
                frozenset({("session_type", "USER"), ("session_status", "ACTIVE")}))]
    assert sess.value == 12.0
