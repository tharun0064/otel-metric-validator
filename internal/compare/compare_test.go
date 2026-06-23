package compare

import (
	"testing"

	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingest"
	"github.com/newrelic-forks/otel-metric-validator/internal/metricmap"
)

func cfg() config.Config {
	return config.Config{
		TolGauge: 0.02, TolCounter: 0.05, AbsEpsilon: 1.0,
	}
}

func exp(metric string, value float64, vt string, attrs map[string]string) map[string]metricmap.Expected {
	if attrs == nil {
		attrs = map[string]string{}
	}
	e := metricmap.Expected{Metric: metric, Attrs: attrs, Value: value, ValueType: vt}
	return map[string]metricmap.Expected{e.Key(): e}
}

func emt(metric string, value float64, attrs map[string]string) map[string]ingest.Emitted {
	if attrs == nil {
		attrs = map[string]string{}
	}
	e := ingest.Emitted{Metric: metric, Attrs: attrs, Value: value}
	return map[string]ingest.Emitted{e.Key(): e}
}

func TestOKWithinCounterTolerance(t *testing.T) {
	r := Compare(exp("oracledb.executions", 1000, metricmap.SUM, nil), emt("oracledb.executions", 1040, nil), cfg())
	if r[0].Status != OK {
		t.Fatalf("want OK, got %s", r[0].Status)
	}
}

func TestMismatchBeyondCounterTolerance(t *testing.T) {
	r := Compare(exp("oracledb.executions", 1000, metricmap.SUM, nil), emt("oracledb.executions", 1200, nil), cfg())
	if r[0].Status != Mismatch || !HasFailures(r) {
		t.Fatalf("want MISMATCH+failure, got %s", r[0].Status)
	}
}

func TestGaugeTighterTolerance(t *testing.T) {
	r := Compare(exp("oracledb.pga_memory", 1000, metricmap.GAUGE, nil), emt("oracledb.pga_memory", 1040, nil), cfg())
	if r[0].Status != Mismatch {
		t.Fatalf("4%% on a gauge should mismatch (2%% tol), got %s", r[0].Status)
	}
}

func TestNearZeroAbsEpsilon(t *testing.T) {
	r := Compare(exp("oracledb.enqueue_deadlocks", 0, metricmap.SUM, nil), emt("oracledb.enqueue_deadlocks", 1, nil), cfg())
	if r[0].Status != OK {
		t.Fatalf("within abs epsilon should be OK, got %s", r[0].Status)
	}
}

func TestMissingInIngestNotFailure(t *testing.T) {
	r := Compare(exp("oracledb.executions", 100, metricmap.SUM, nil), map[string]ingest.Emitted{}, cfg())
	if r[0].Status != MissingInIngest || HasFailures(r) {
		t.Fatalf("missing-in-ingest should be non-fatal, got %s", r[0].Status)
	}
}

func TestComputedSkipped(t *testing.T) {
	r := Compare(map[string]metricmap.Expected{}, emt("oracledb.host.cpu.utilization", 42, nil), cfg())
	if r[0].Status != Skipped || HasFailures(r) {
		t.Fatalf("computed metric should be SKIPPED, got %s", r[0].Status)
	}
}

func TestUnmappedMissingInDB(t *testing.T) {
	r := Compare(map[string]metricmap.Expected{}, emt("oracledb.some_unmapped", 1, nil), cfg())
	if r[0].Status != MissingInDB {
		t.Fatalf("want MISSING_IN_DB, got %s", r[0].Status)
	}
}

func TestPDBEmptyMatchesAbsent(t *testing.T) {
	e := exp("oracledb.executions", 100, metricmap.SUM, map[string]string{"oracle.db.pdb": ""})
	a := emt("oracledb.executions", 102, map[string]string{})
	r := Compare(e, a, cfg())
	if len(r) != 1 || r[0].Status != OK {
		t.Fatalf("empty pdb should join with absent: %+v", r)
	}
}
