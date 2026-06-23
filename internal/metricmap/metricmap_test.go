package metricmap

import "testing"

func TestCPUTimeTransform(t *testing.T) {
	rows := []map[string]any{{"NAME": "CPU used by this session", "VALUE": "5000"}}
	out := ExpectedFor("sysstat", rows)
	if len(out) != 1 {
		t.Fatalf("want 1 expected, got %d", len(out))
	}
	if out[0].Metric != "oracledb.cpu_time" || out[0].Value != 50.0 {
		t.Fatalf("cpu_time transform: got metric=%s value=%v, want oracledb.cpu_time 50", out[0].Metric, out[0].Value)
	}
	if out[0].ValueType != SUM {
		t.Fatalf("cpu_time should be SUM, got %s", out[0].ValueType)
	}
}

func TestSimpleCounter(t *testing.T) {
	rows := []map[string]any{{"NAME": "execute count", "VALUE": int64(1234)}}
	out := ExpectedFor("sysstat", rows)
	if len(out) != 1 || out[0].Metric != "oracledb.executions" || out[0].Value != 1234 {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestPGAIsGauge(t *testing.T) {
	rows := []map[string]any{{"NAME": "session pga memory", "VALUE": 999.0}}
	out := ExpectedFor("sysstat", rows)
	if len(out) != 1 || out[0].ValueType != GAUGE || out[0].Metric != "oracledb.pga_memory" {
		t.Fatalf("pga should be gauge: %+v", out)
	}
}

func TestIOTransferredAttrs(t *testing.T) {
	rows := []map[string]any{{"NAME": "physical read total bytes", "VALUE": 100.0, "PDB_NAME": "PDB2"}}
	out := ExpectedFor("sysstat", rows)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	want := map[string]string{"disk.io.direction": "read", "disk.io.type": "total", "oracle.db.pdb": "PDB2"}
	if out[0].Metric != "oracledb.physical_io.transferred" {
		t.Fatalf("metric: %s", out[0].Metric)
	}
	for k, v := range want {
		if out[0].Attrs[k] != v {
			t.Fatalf("attr %s=%q, want %q (got %+v)", k, out[0].Attrs[k], v, out[0].Attrs)
		}
	}
}

func TestResourceLimitLastWins(t *testing.T) {
	// processes row -> usage + limit (two metrics from CURRENT_UTILIZATION / LIMIT_VALUE).
	rows := []map[string]any{{"RESOURCE_NAME": "processes", "CURRENT_UTILIZATION": "10", "LIMIT_VALUE": "300"}}
	out := ExpectedFor("resource_limits", rows)
	got := map[string]float64{}
	for _, e := range out {
		got[e.Metric] = e.Value
		if e.ValueType != GAUGE {
			t.Fatalf("%s should be gauge", e.Metric)
		}
	}
	if got["oracledb.processes.usage"] != 10 || got["oracledb.processes.limit"] != 300 {
		t.Fatalf("resource limit: %+v", got)
	}
}

func TestTablespaceUsesBlockSize(t *testing.T) {
	rows := []map[string]any{{"TABLESPACE_NAME": "USERS", "USED_SPACE": 10.0, "TABLESPACE_SIZE": 100.0, "BLOCK_SIZE": 8192.0}}
	out := ExpectedFor("tablespace", rows)
	got := map[string]float64{}
	for _, e := range out {
		got[e.Metric] = e.Value
	}
	if got["oracledb.tablespace_size.usage"] != 10*8192 {
		t.Fatalf("usage: %v", got["oracledb.tablespace_size.usage"])
	}
	if got["oracledb.tablespace_size.limit"] != 100*8192 {
		t.Fatalf("limit: %v", got["oracledb.tablespace_size.limit"])
	}
}

func TestTablespaceUnlimited(t *testing.T) {
	rows := []map[string]any{{"TABLESPACE_NAME": "USERS", "USED_SPACE": 10.0, "TABLESPACE_SIZE": nil, "BLOCK_SIZE": 8192.0}}
	out := ExpectedFor("tablespace", rows)
	var limit float64 = 0
	found := false
	for _, e := range out {
		if e.Metric == "oracledb.tablespace_size.limit" {
			limit, found = e.Value, true
		}
	}
	if !found || limit != -1 {
		t.Fatalf("unlimited tablespace should yield -1, got %v (found=%v)", limit, found)
	}
}

func TestSGAMaxIsLimit(t *testing.T) {
	rows := []map[string]any{
		{"NAME": "Maximum SGA Size", "BYTES": 1000.0},
		{"NAME": "Fixed SGA Size", "BYTES": 50.0},
	}
	out := ExpectedFor("sga", rows)
	got := map[string]float64{}
	for _, e := range out {
		got[e.Metric] = e.Value
	}
	if got["oracledb.sga.limit"] != 1000 || got["oracledb.sga.usage"] != 50 {
		t.Fatalf("sga: %+v", got)
	}
}

func TestValueTypeOf(t *testing.T) {
	if vt, ok := ValueTypeOf("oracledb.executions"); !ok || vt != SUM {
		t.Fatalf("executions should be SUM")
	}
	if vt, ok := ValueTypeOf("oracledb.pga_memory"); !ok || vt != GAUGE {
		t.Fatalf("pga should be GAUGE")
	}
	if _, ok := ValueTypeOf("oracledb.not_a_metric"); ok {
		t.Fatalf("unknown metric should be unmapped")
	}
}

func TestKeyDropsEmptyAttrs(t *testing.T) {
	a := Key("m", map[string]string{"oracle.db.pdb": ""})
	b := Key("m", map[string]string{})
	if a != b {
		t.Fatalf("empty attr should match absent: %q vs %q", a, b)
	}
}

func TestCDBQuerySelected(t *testing.T) {
	if QuerySQL("sysstat", true) == QuerySQL("sysstat", false) {
		t.Fatalf("cdb and pdb sysstat SQL should differ")
	}
}

func TestSysmetricDirectAndTransform(t *testing.T) {
	rows := []map[string]any{
		{"METRIC_NAME": "Buffer Cache Hit Ratio", "VALUE": 99.5, "PDB_NAME": "PDB2"},
		{"METRIC_NAME": "SQL Service Response Time", "VALUE": 50.0}, // /100 -> 0.5
		{"METRIC_NAME": "Memory Sorts Ratio", "VALUE": 100.0},       // static attr sort.type=memory
	}
	out := ExpectedFor("sysmetric", rows)
	got := map[string]Expected{}
	for _, e := range out {
		got[e.Metric] = e
	}
	if e := got["oracledb.buffer_cache.utilization"]; e.Value != 99.5 || e.Attrs["oracle.db.pdb"] != "PDB2" || e.ValueType != GAUGE {
		t.Fatalf("buffer_cache: %+v", e)
	}
	if e := got["oracledb.sql_service.response.duration"]; e.Value != 0.5 {
		t.Fatalf("sql_service should be value/100, got %v", e.Value)
	}
	if e := got["oracledb.sort.ratio"]; e.Attrs["oracledb.sort.type"] != "memory" {
		t.Fatalf("sort.ratio attr: %+v", e.Attrs)
	}
}

func TestOSStat(t *testing.T) {
	rows := []map[string]any{
		{"STAT_NAME": "NUM_CPUS", "VALUE": "8"},
		{"STAT_NAME": "LOAD", "VALUE": 1.5},
		{"STAT_NAME": "PHYSICAL_MEMORY_BYTES", "VALUE": "66878488576"},
	}
	out := ExpectedFor("osstat", rows)
	got := map[string]float64{}
	for _, e := range out {
		got[e.Metric] = e.Value
	}
	if got["system.cpu.physical.count"] != 8 || got["oracledb.system.cpu.load"] != 1.5 || got["system.memory.limit"] != 66878488576 {
		t.Fatalf("osstat: %+v", got)
	}
}

func TestStorageUtilization(t *testing.T) {
	rows := []map[string]any{{"USED_DB_SIZE": 50.0, "ALLOCATED_DB_SIZE": 200.0}}
	out := ExpectedFor("storage", rows)
	got := map[string]float64{}
	for _, e := range out {
		got[e.Metric] = e.Value
	}
	if got["oracledb.storage.usage"] != 50 {
		t.Fatalf("usage: %v", got["oracledb.storage.usage"])
	}
	if got["oracledb.storage.utilization"] != 0.25 {
		t.Fatalf("utilization should be used/allocated=0.25, got %v", got["oracledb.storage.utilization"])
	}
}

func TestRecycleBin(t *testing.T) {
	out := ExpectedFor("recycle_bin", []map[string]any{{"RECYCLE_BIN_SIZE_BYTES": 4096.0}})
	if len(out) != 1 || out[0].Metric != "oracledb.recycle_bin.limit" || out[0].Value != 4096 {
		t.Fatalf("recycle_bin: %+v", out)
	}
}

func TestComputedSkipEmpty(t *testing.T) {
	if len(ComputedSkip) != 0 {
		t.Fatalf("ComputedSkip should be empty now: %+v", ComputedSkip)
	}
}
