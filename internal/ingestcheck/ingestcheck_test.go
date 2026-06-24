package ingestcheck

import (
	"testing"

	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingest"
)

type stubRunner struct {
	value *float64
	err   error
	last  string
}

func (s *stubRunner) Run(q string) (*float64, error) {
	s.last = q
	return s.value, s.err
}

func cfg() config.Config { return config.Config{TolGauge: 0.02, TolCounter: 0.05, AbsEpsilon: 1.0} }

func f(v float64) *float64 { return &v }

func seriesMap(s ingest.Series) map[string]ingest.Series {
	return map[string]ingest.Series{s.Key(): s}
}

func TestCounterDeltaOK(t *testing.T) {
	// emitted cumulative 100 -> 180 across two points ⇒ expected delta 80.
	s := ingest.Series{Metric: "oracledb.executions", Attrs: map[string]string{}, FirstValue: 100, FirstTS: 1_000_000_000, LastValue: 180, LastTS: 3_000_000_000, NPoints: 3}
	stub := &stubRunner{value: f(80)}
	out := checkWith(seriesMap(s), cfg(), stub)
	if out[0].Status != OK {
		t.Fatalf("want INGEST_OK, got %s (note %s)", out[0].Status, out[0].Note)
	}
	if out[0].Expected == nil || *out[0].Expected != 80 {
		t.Fatalf("expected delta 80, got %v", out[0].Expected)
	}
	// delta query should exclude the boundary point (SINCE first+1).
	if want := "SELECT sum(`oracledb.executions`) FROM Metric SINCE 1001 UNTIL 3001"; stub.last != want {
		t.Fatalf("query: %s", stub.last)
	}
}

func TestCounterDeltaMismatch(t *testing.T) {
	s := ingest.Series{Metric: "oracledb.executions", Attrs: map[string]string{}, FirstValue: 100, FirstTS: 1_000_000_000, LastValue: 180, LastTS: 3_000_000_000, NPoints: 3}
	out := checkWith(seriesMap(s), cfg(), &stubRunner{value: f(40)})
	if out[0].Status != Mismatch || !HasFailures(out) {
		t.Fatalf("bad delta should mismatch, got %s", out[0].Status)
	}
}

func TestGaugeLatest(t *testing.T) {
	s := ingest.Series{Metric: "oracledb.pga_memory", Attrs: map[string]string{}, FirstValue: 500, FirstTS: 1_000_000_000, LastValue: 600, LastTS: 2_000_000_000, NPoints: 2}
	stub := &stubRunner{value: f(600)}
	out := checkWith(seriesMap(s), cfg(), stub)
	if out[0].Status != OK {
		t.Fatalf("gauge latest should be OK, got %s", out[0].Status)
	}
	if want := "SELECT latest(`oracledb.pga_memory`) FROM Metric SINCE 1000 UNTIL 3000"; stub.last != want {
		t.Fatalf("query: %s", stub.last)
	}
}

func TestSinglePointCounterSkipped(t *testing.T) {
	s := ingest.Series{Metric: "oracledb.executions", Attrs: map[string]string{}, FirstValue: 100, FirstTS: 1_000_000_000, LastValue: 100, LastTS: 1_000_000_000, NPoints: 1}
	out := checkWith(seriesMap(s), cfg(), &stubRunner{value: f(0)})
	if out[0].Status != Skipped {
		t.Fatalf("single point should be skipped, got %s", out[0].Status)
	}
}

func TestUnmappedSkipped(t *testing.T) {
	// A metric the validator has no DB mapping for can't be ingest-checked.
	s := ingest.Series{Metric: "oracledb.not_a_real_metric", Attrs: map[string]string{}, FirstValue: 1, FirstTS: 1_000_000_000, LastValue: 2, LastTS: 3_000_000_000, NPoints: 3}
	out := checkWith(seriesMap(s), cfg(), &stubRunner{value: f(1)})
	if out[0].Status != Skipped {
		t.Fatalf("unmapped should be skipped, got %s", out[0].Status)
	}
}

func TestNoData(t *testing.T) {
	s := ingest.Series{Metric: "oracledb.executions", Attrs: map[string]string{}, FirstValue: 100, FirstTS: 1_000_000_000, LastValue: 180, LastTS: 3_000_000_000, NPoints: 3}
	out := checkWith(seriesMap(s), cfg(), &stubRunner{value: nil})
	if out[0].Status != NoData {
		t.Fatalf("nil NRDB value should be INGEST_NO_DATA, got %s", out[0].Status)
	}
}
