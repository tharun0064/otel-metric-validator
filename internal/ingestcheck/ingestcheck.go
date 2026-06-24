// Package ingestcheck verifies NR's ingest pipeline stored the correct values.
//
// The collector emits cumulative counters; New Relic delta-converts them at
// ingest. By the telescoping property, sum(metric) over (t0, tN] in NRDB must
// equal the emitted cumulative end-to-end difference C(tN)-C(t0). Gauges are
// stored as-is, so latest(metric) must equal the emitted latest.
package ingestcheck

import (
	"sort"

	"github.com/newrelic-forks/otel-metric-validator/internal/compare"
	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingest"
	"github.com/newrelic-forks/otel-metric-validator/internal/metricmap"
	"github.com/newrelic-forks/otel-metric-validator/internal/nrql"
)

// Ingest-check statuses.
const (
	OK       = "INGEST_OK"
	Mismatch = "INGEST_MISMATCH"
	NoData   = "INGEST_NO_DATA"
	Error    = "INGEST_ERROR"
	Skipped  = "INGEST_SKIPPED"
)

// Result is one ingest-check row.
type Result struct {
	Metric    string            `json:"metric"`
	Attrs     map[string]string `json:"attrs"`
	Status    string            `json:"status"`
	Expected  *float64          `json:"expected"`
	Actual    *float64          `json:"actual"`
	RelDiff   *float64          `json:"rel_diff"`
	ValueType string            `json:"value_type"`
	NRQL      string            `json:"nrql"`
	Note      string            `json:"note"`
}

type runner interface {
	Run(nrqlQuery string) (*float64, error)
}

func checkOne(s ingest.Series, cfg config.Config, client runner) Result {
	vt, mapped := metricmap.ValueTypeOf(s.Metric)
	if metricmap.ComputedSkip[s.Metric] || !mapped {
		return Result{Metric: s.Metric, Attrs: s.Attrs, Status: Skipped, ValueType: vt,
			Note: "not a directly-mapped Phase-1 metric"}
	}

	sinceMS := nrql.ToMS(s.FirstTS)
	untilMS := nrql.ToMS(s.LastTS)

	var expected float64
	var query string
	if vt == metricmap.SUM {
		if s.NPoints < 2 || untilMS <= sinceMS {
			return Result{Metric: s.Metric, Attrs: s.Attrs, Status: Skipped, ValueType: vt,
				Note: "need >=2 emitted points across time for a delta"}
		}
		expected = s.LastValue - s.FirstValue
		// NR stamps each delta at the END of its interval; NRQL UNTIL is exclusive.
		// SINCE first+1 drops the first point's delta (it belongs to the prior
		// interval); UNTIL last+1 includes the final interval's delta. Net window
		// sum = C(last) - C(first) = expected.
		query = nrql.BuildDeltaQuery(s.Metric, s.Attrs, sinceMS+1, untilMS+1)
	} else {
		expected = s.LastValue
		query = nrql.BuildLatestQuery(s.Metric, s.Attrs, sinceMS, untilMS+1000)
	}

	actual, err := client.Run(query)
	if err != nil {
		return Result{Metric: s.Metric, Attrs: s.Attrs, Status: Error, Expected: &expected,
			ValueType: vt, NRQL: query, Note: err.Error()}
	}
	if actual == nil {
		return Result{Metric: s.Metric, Attrs: s.Attrs, Status: NoData, Expected: &expected,
			ValueType: vt, NRQL: query, Note: "NRQL returned no data (ingest lag, or attrs/window mismatch)"}
	}

	ok, rel := compare.WithinTolerance(expected, *actual, vt, cfg)
	status := OK
	if !ok {
		status = Mismatch
	}
	return Result{Metric: s.Metric, Attrs: s.Attrs, Status: status, Expected: &expected,
		Actual: actual, RelDiff: &rel, ValueType: vt, NRQL: query}
}

// Check runs the ingest check over every emitted series.
func Check(series map[string]ingest.Series, cfg config.Config) []Result {
	client := nrql.New(cfg)
	return checkWith(series, cfg, client)
}

func checkWith(series map[string]ingest.Series, cfg config.Config, client runner) []Result {
	results := make([]Result, 0, len(series))
	for _, s := range series {
		results = append(results, checkOne(s, cfg, client))
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Status != results[j].Status {
			return results[i].Status < results[j].Status
		}
		return results[i].Metric < results[j].Metric
	})
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

// HasFailures reports whether any ingest result is a real mismatch.
func HasFailures(results []Result) bool {
	for _, r := range results {
		if r.Status == Mismatch {
			return true
		}
	}
	return false
}
