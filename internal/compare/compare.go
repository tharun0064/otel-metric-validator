// Package compare joins emitted (collector) vs expected (DB) data points and
// classifies each within tolerance.
package compare

import (
	"math"
	"sort"

	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingest"
	"github.com/newrelic-forks/otel-metric-validator/internal/metricmap"
)

// DB-check statuses.
const (
	OK              = "OK"
	Mismatch        = "MISMATCH"
	MissingInIngest = "MISSING_IN_INGEST"
	MissingInDB     = "MISSING_IN_DB"
	Skipped         = "SKIPPED"
)

// Result is one classified comparison row.
type Result struct {
	Metric    string            `json:"metric"`
	Attrs     map[string]string `json:"attrs"`
	Status    string            `json:"status"`
	Expected  *float64          `json:"expected"`
	Actual    *float64          `json:"actual"`
	RelDiff   *float64          `json:"rel_diff"`
	ValueType string            `json:"value_type"`
	Note      string            `json:"note"`
}

// WithinTolerance reports whether actual is within tolerance of expected, and the
// relative (or absolute, near zero) difference used.
func WithinTolerance(expected, actual float64, valueType string, cfg config.Config) (bool, float64) {
	diff := math.Abs(actual - expected)
	if math.Abs(expected) <= cfg.AbsEpsilon {
		return diff <= cfg.AbsEpsilon, diff
	}
	rel := diff / math.Abs(expected)
	tol := cfg.TolGauge
	if valueType == metricmap.SUM {
		tol = cfg.TolCounter
	}
	return rel <= tol, rel
}

// Compare joins expected (DB) and emitted (collector) maps and classifies each key.
func Compare(expected map[string]metricmap.Expected, emitted map[string]ingest.Emitted, cfg config.Config) []Result {
	keys := map[string]bool{}
	for k := range expected {
		keys[k] = true
	}
	for k := range emitted {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var results []Result
	for _, key := range ordered {
		metric, attrs := metricmap.AttrsFromKey(key)
		exp, hasExp := expected[key]
		emt, hasEmt := emitted[key]

		switch {
		case hasExp && hasEmt:
			ok, rel := WithinTolerance(exp.Value, emt.Value, exp.ValueType, cfg)
			status := OK
			if !ok {
				status = Mismatch
			}
			results = append(results, Result{
				Metric: metric, Attrs: attrs, Status: status,
				Expected: f(exp.Value), Actual: f(emt.Value), RelDiff: f(rel), ValueType: exp.ValueType,
			})
		case hasExp:
			results = append(results, Result{
				Metric: metric, Attrs: attrs, Status: MissingInIngest,
				Expected: f(exp.Value), ValueType: exp.ValueType,
				Note: "present in DB, not in collector output (metric disabled?)",
			})
		default:
			if metricmap.ComputedSkip[metric] {
				results = append(results, Result{
					Metric: metric, Attrs: attrs, Status: Skipped, Actual: f(emt.Value),
					Note: "receiver-computed (v$sysmetric/osstat); not yet validated",
				})
			} else {
				results = append(results, Result{
					Metric: metric, Attrs: attrs, Status: MissingInDB, Actual: f(emt.Value),
					Note: "no DB mapping for this metric/attrs",
				})
			}
		}
	}
	return results
}

// Summarize counts results by status.
func Summarize(results []Result) map[string]int {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	return counts
}

// HasFailures reports whether any result is a real value disagreement.
func HasFailures(results []Result) bool {
	for _, r := range results {
		if r.Status == Mismatch {
			return true
		}
	}
	return false
}

func f(v float64) *float64 { return &v }
