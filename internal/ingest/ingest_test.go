package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

const scope = "github.com/newrelic-forks/opentelemetry-collector-contrib/receiver/nroracledbreceiver"

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func line(scopeName, metric, kind string, attrs, ts, intVal string) string {
	attrJSON := ""
	if attrs != "" {
		attrJSON = `,"attributes":[` + attrs + `]`
	}
	return `{"resourceMetrics":[{"scopeMetrics":[{"scope":{"name":"` + scopeName + `"},"metrics":[` +
		`{"name":"` + metric + `","` + kind + `":{"dataPoints":[{"asInt":"` + intVal + `","timeUnixNano":"` + ts + `"` + attrJSON + `}]}}]}]}]}`
}

func TestReadOTLPLatestWins(t *testing.T) {
	body := line(scope, "oracledb.executions", "sum", "", "1000", "100") + "\n" +
		line(scope, "oracledb.executions", "sum", "", "2000", "150") + "\n"
	p := write(t, "m.json", body)
	got, err := ReadOTLPJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got[mapKey("oracledb.executions", nil)]
	if !ok || e.Value != 150 {
		t.Fatalf("latest should win: %+v", got)
	}
}

func TestReadOTLPScopeFilter(t *testing.T) {
	body := line("some.other.scope", "foo.bar", "sum", "", "1000", "5") + "\n"
	p := write(t, "m.json", body)
	got, _ := ReadOTLPJSON(p)
	if len(got) != 0 {
		t.Fatalf("non-oracle scope should be filtered, got %+v", got)
	}
}

func TestReadOTLPUpstreamScopeAccepted(t *testing.T) {
	// Upstream collectors emit …/receiver/oracledbreceiver (no "nr" prefix).
	upstream := "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/oracledbreceiver"
	body := line(upstream, "oracledb.executions", "sum", "", "1000", "9") + "\n"
	p := write(t, "m.json", body)
	got, _ := ReadOTLPJSON(p)
	if _, ok := got[mapKey("oracledb.executions", nil)]; !ok {
		t.Fatalf("upstream oracledbreceiver scope should be accepted: %+v", got)
	}
}

func TestReadOTLPAttrs(t *testing.T) {
	attr := `{"key":"oracle.db.pdb","value":{"stringValue":"PDB2"}}`
	body := line(scope, "oracledb.executions", "sum", attr, "1000", "7") + "\n"
	p := write(t, "m.json", body)
	got, _ := ReadOTLPJSON(p)
	if _, ok := got[mapKey("oracledb.executions", map[string]string{"oracle.db.pdb": "PDB2"})]; !ok {
		t.Fatalf("attr key not built: %+v", got)
	}
}

func TestReadOTLPSeriesEndpoints(t *testing.T) {
	body := line(scope, "oracledb.executions", "sum", "", "1000", "100") + "\n" +
		line(scope, "oracledb.executions", "sum", "", "2000", "130") + "\n" +
		line(scope, "oracledb.executions", "sum", "", "3000", "180") + "\n"
	p := write(t, "m.json", body)
	series, _ := ReadOTLPSeries(p, 0)
	s, ok := series[mapKey("oracledb.executions", nil)]
	if !ok {
		t.Fatal("series missing")
	}
	if s.FirstValue != 100 || s.LastValue != 180 || s.NPoints != 3 {
		t.Fatalf("endpoints: %+v", s)
	}
	if s.FirstTS != 1000 || s.LastTS != 3000 {
		t.Fatalf("timestamps: %+v", s)
	}
}

func TestReadOTLPSeriesWindow(t *testing.T) {
	// timestamps in ns; sinceNanos=2500 drops the first two points.
	body := line(scope, "oracledb.executions", "sum", "", "1000", "100") + "\n" +
		line(scope, "oracledb.executions", "sum", "", "2000", "130") + "\n" +
		line(scope, "oracledb.executions", "sum", "", "3000", "180") + "\n"
	p := write(t, "m.json", body)
	series, _ := ReadOTLPSeries(p, 2500)
	s := series[mapKey("oracledb.executions", nil)]
	if s.FirstTS != 3000 || s.FirstValue != 180 || s.NPoints != 1 {
		t.Fatalf("window should keep only the point at ts=3000: %+v", s)
	}
}

func TestReadDebugLog(t *testing.T) {
	body := `2024-01-01 Metric #0
     -> Name: oracledb.executions
     -> DataType: Sum
     Data point attributes:
          -> oracle.db.pdb: Str(PDB2)
     Value: 4242
`
	p := write(t, "c.log", body)
	got, err := ReadDebugLog(p)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got[mapKey("oracledb.executions", map[string]string{"oracle.db.pdb": "PDB2"})]
	if !ok || e.Value != 4242 {
		t.Fatalf("debug-log parse: %+v", got)
	}
}

// mapKey mirrors metricmap.Key for assertions without importing it directly here.
func mapKey(metric string, attrs map[string]string) string {
	e := Emitted{Metric: metric, Attrs: attrs}
	return e.Key()
}
