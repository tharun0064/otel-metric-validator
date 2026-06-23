"""Unit tests for the receiver->metric mapping (no DB needed)."""

from validator import metric_map as mm
from validator.metric_map import GAUGE, SUM


def _by_metric(expected):
    out = {}
    for e in expected:
        out.setdefault(e.metric, []).append(e)
    return out


def test_cpu_time_divided_by_100():
    rows = [{"NAME": "CPU used by this session", "VALUE": "7593"}]
    exp = mm.extract_sysstat(rows)
    assert len(exp) == 1
    assert exp[0].metric == "oracledb.cpu_time"
    assert exp[0].value == 75.93          # 7593 / 100
    assert exp[0].value_type == SUM


def test_simple_counter_no_transform():
    rows = [{"NAME": "execute count", "VALUE": "12345"}]
    exp = mm.extract_sysstat(rows)
    assert exp[0].metric == "oracledb.executions"
    assert exp[0].value == 12345.0
    assert exp[0].value_type == SUM


def test_pga_memory_is_gauge():
    rows = [{"NAME": "session pga memory", "VALUE": "2450077376"}]
    exp = mm.extract_sysstat(rows)
    assert exp[0].metric == "oracledb.pga_memory"
    assert exp[0].value_type == GAUGE


def test_pdb_attribute_attached_in_cdb_rows():
    rows = [{"NAME": "execute count", "VALUE": "10", "PDB_NAME": "PDB2"}]
    exp = mm.extract_sysstat(rows)
    assert exp[0].attrs == {"oracle.db.pdb": "PDB2"}


def test_io_transferred_attrs():
    rows = [
        {"NAME": "physical read bytes", "VALUE": "100"},
        {"NAME": "physical write total bytes", "VALUE": "200"},
    ]
    by = _by_metric(mm.extract_sysstat(rows))
    pts = {frozenset(e.attrs.items()): e.value for e in by["oracledb.physical_io.transferred"]}
    assert pts[frozenset({("disk.io.direction", "read"), ("disk.io.type", "buffered")})] == 100.0
    assert pts[frozenset({("disk.io.direction", "write"), ("disk.io.type", "total")})] == 200.0


def test_sqlnet_attrs():
    rows = [{"NAME": "bytes sent via SQL*Net to client", "VALUE": "555"}]
    exp = mm.extract_sysstat(rows)
    assert exp[0].metric == "oracledb.sqlnet.io.transferred"
    assert exp[0].attrs == {"network.io.direction": "transmit", "destination.type": "client"}


def test_session_count_attrs_gauge():
    rows = [{"TYPE": "USER", "STATUS": "ACTIVE", "VALUE": "12"}]
    exp = mm.extract_session_count(rows)
    assert exp[0].metric == "oracledb.sessions.usage"
    assert exp[0].attrs == {"session_type": "USER", "session_status": "ACTIVE"}
    assert exp[0].value == 12.0
    assert exp[0].value_type == GAUGE


def test_resource_limits_usage_and_limit():
    rows = [{"RESOURCE_NAME": "processes", "CURRENT_UTILIZATION": "80", "LIMIT_VALUE": "300"}]
    by = _by_metric(mm.extract_resource_limits(rows))
    assert by["oracledb.processes.usage"][0].value == 80.0
    assert by["oracledb.processes.limit"][0].value == 300.0


def test_tablespace_multiplies_blocks_by_block_size():
    rows = [{"TABLESPACE_NAME": "USERS", "USED_SPACE": "10", "TABLESPACE_SIZE": "100", "BLOCK_SIZE": "8192"}]
    by = _by_metric(mm.extract_tablespace(rows))
    assert by["oracledb.tablespace_size.usage"][0].value == 10 * 8192
    assert by["oracledb.tablespace_size.limit"][0].value == 100 * 8192
    assert by["oracledb.tablespace_size.usage"][0].attrs == {"tablespace_name": "USERS"}


def test_tablespace_unlimited_size_is_minus_one():
    rows = [{"TABLESPACE_NAME": "USERS", "USED_SPACE": "10", "TABLESPACE_SIZE": "", "BLOCK_SIZE": "8192"}]
    by = _by_metric(mm.extract_tablespace(rows))
    assert by["oracledb.tablespace_size.limit"][0].value == -1


def test_sga_usage_vs_limit():
    rows = [
        {"NAME": "Maximum SGA Size", "BYTES": "1000"},
        {"NAME": "Database Buffers", "BYTES": "400"},
    ]
    by = _by_metric(mm.extract_sga(rows))
    assert by["oracledb.sga.limit"][0].value == 1000.0
    assert by["oracledb.sga.usage"][0].value == 400.0
    assert by["oracledb.sga.usage"][0].attrs == {"oracledb.sga.component.name": "Database Buffers"}


def test_query_sql_picks_cdb_variant():
    assert "v$con_sysstat" in mm.query_sql("sysstat", is_cdb=True)
    assert "v$sysstat" in mm.query_sql("sysstat", is_cdb=False)
