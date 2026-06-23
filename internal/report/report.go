// Package report renders comparison results as human tables or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/newrelic-forks/otel-metric-validator/internal/compare"
	"github.com/newrelic-forks/otel-metric-validator/internal/ingestcheck"
)

var dbOrder = map[string]int{
	compare.Mismatch:        0,
	compare.MissingInIngest: 1,
	compare.OK:              2,
	compare.MissingInDB:     3,
	compare.Skipped:         4,
}

var ingestOrder = map[string]int{
	ingestcheck.Mismatch: 0,
	ingestcheck.Error:    1,
	ingestcheck.NoData:   2,
	ingestcheck.OK:       3,
	ingestcheck.Skipped:  4,
}

func attrsStr(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(attrs))
	for k, v := range attrs {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func fmtNum(v *float64) string {
	if v == nil {
		return "-"
	}
	if *v == math.Trunc(*v) && !math.IsInf(*v, 0) {
		return fmt.Sprintf("%d", int64(*v))
	}
	return fmt.Sprintf("%.4g", *v)
}

// delta renders rel_diff as a percentage when expected is non-zero, else absolute.
func delta(rel, expected *float64) string {
	if rel == nil {
		return ""
	}
	if expected != nil && *expected != 0 {
		return fmt.Sprintf("%.2f%%", *rel*100)
	}
	return fmt.Sprintf("%.4g", *rel)
}

func table(headers []string, lines [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, ln := range lines {
		for i, cell := range ln {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	fmtRow := func(cells []string) string {
		padded := make([]string, len(cells))
		for i, c := range cells {
			padded[i] = c + strings.Repeat(" ", widths[i]-len(c))
		}
		return strings.Join(padded, "  ")
	}
	sep := make([]string, len(headers))
	for i, w := range widths {
		sep[i] = strings.Repeat("-", w)
	}
	out := []string{fmtRow(headers), fmtRow(sep)}
	for _, ln := range lines {
		out = append(out, fmtRow(ln))
	}
	return strings.Join(out, "\n")
}

// RenderTable renders the DB-check results.
func RenderTable(results []compare.Result, showOK bool) string {
	rows := make([]compare.Result, 0, len(results))
	for _, r := range results {
		if showOK || r.Status != compare.OK {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, oj := order(dbOrder, rows[i].Status), order(dbOrder, rows[j].Status)
		if oi != oj {
			return oi < oj
		}
		return rows[i].Metric < rows[j].Metric
	})
	var lines [][]string
	for _, r := range rows {
		lines = append(lines, []string{
			r.Status, r.Metric, attrsStr(r.Attrs),
			fmtNum(r.Expected), fmtNum(r.Actual), delta(r.RelDiff, r.Expected),
		})
	}
	return table([]string{"STATUS", "METRIC", "ATTRS", "EXPECTED(DB)", "ACTUAL(OTEL)", "Δ"}, lines)
}

// RenderIngestTable renders the ingest-check results.
func RenderIngestTable(results []ingestcheck.Result, showOK bool) string {
	rows := make([]ingestcheck.Result, 0, len(results))
	for _, r := range results {
		if showOK || r.Status != ingestcheck.OK {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, oj := order(ingestOrder, rows[i].Status), order(ingestOrder, rows[j].Status)
		if oi != oj {
			return oi < oj
		}
		return rows[i].Metric < rows[j].Metric
	})
	var lines [][]string
	for _, r := range rows {
		kind := ""
		if r.ValueType == "sum" {
			kind = "Δ"
		} else if r.ValueType != "" {
			kind = "latest"
		}
		lines = append(lines, []string{
			r.Status, r.Metric, attrsStr(r.Attrs), kind,
			fmtNum(r.Expected), fmtNum(r.Actual), delta(r.RelDiff, r.Expected),
		})
	}
	return table([]string{"STATUS", "METRIC", "ATTRS", "TYPE", "EXPECTED", "NRDB", "Δ"}, lines)
}

// RenderJSON renders the DB-check results as indented JSON.
func RenderJSON(results []compare.Result) string { return mustJSON(results) }

// RenderIngestJSON renders the ingest-check results as indented JSON.
func RenderIngestJSON(results []ingestcheck.Result) string { return mustJSON(results) }

// SummaryLine renders status counts as "K=v  K2=v2".
func SummaryLine(counts map[string]int) string {
	if len(counts) == 0 {
		return "(no results)"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, counts[k])
	}
	return strings.Join(parts, "  ")
}

func order(m map[string]int, status string) int {
	if v, ok := m[status]; ok {
		return v
	}
	return 99
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}
