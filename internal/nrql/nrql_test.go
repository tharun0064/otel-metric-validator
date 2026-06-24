package nrql

import "testing"

func TestToMS(t *testing.T) {
	if got := ToMS(1_500_000_000); got != 1500 {
		t.Fatalf("ToMS: got %d, want 1500", got)
	}
}

func TestBuildDeltaQuery(t *testing.T) {
	got := BuildDeltaQuery("oracledb.executions", map[string]string{"oracle.db.pdb": "PDB2"}, 1000, 2000)
	want := "SELECT sum(`oracledb.executions`) FROM Metric WHERE oracle.db.pdb = 'PDB2' SINCE 1000 UNTIL 2000"
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildDeltaQueryNoAttrs(t *testing.T) {
	got := BuildDeltaQuery("oracledb.executions", map[string]string{}, 1000, 2000)
	want := "SELECT sum(`oracledb.executions`) FROM Metric SINCE 1000 UNTIL 2000"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestBuildLatestQuery(t *testing.T) {
	got := BuildLatestQuery("oracledb.pga_memory", map[string]string{}, 10, 20)
	want := "SELECT latest(`oracledb.pga_memory`) FROM Metric SINCE 10 UNTIL 20"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestWhereDropsEmptyAndSorts(t *testing.T) {
	got := where(map[string]string{"b": "2", "a": "1", "c": ""})
	want := " WHERE a = '1' AND b = '2'"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParseScalar(t *testing.T) {
	v, err := ParseScalar([]byte(`{"data":{"actor":{"account":{"nrql":{"results":[{"sum.oracledb.executions":123.0}]}}}}}`))
	if err != nil || v == nil || *v != 123 {
		t.Fatalf("got v=%v err=%v", v, err)
	}
}

func TestParseScalarNoRows(t *testing.T) {
	v, err := ParseScalar([]byte(`{"data":{"actor":{"account":{"nrql":{"results":[]}}}}}`))
	if err != nil || v != nil {
		t.Fatalf("empty results should be (nil,nil), got v=%v err=%v", v, err)
	}
}

func TestParseScalarErrors(t *testing.T) {
	v, err := ParseScalar([]byte(`{"errors":[{"message":"bad nrql"}]}`))
	if err == nil || v != nil {
		t.Fatalf("graphql errors should surface, got v=%v err=%v", v, err)
	}
}
