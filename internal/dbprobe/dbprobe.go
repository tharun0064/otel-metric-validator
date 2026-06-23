// Package dbprobe runs the receiver's monitoring SQL directly against Oracle
// (ground truth), using the same driver the receiver uses: sijms/go-ora.
//
// go-ora is a pure-Go driver that negotiates Oracle Native Network Encryption,
// so unlike the python-oracledb thin client this needs no Oracle Instant Client
// and no "thick mode".
package dbprobe

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/newrelic-forks/otel-metric-validator/internal/config"
	"github.com/newrelic-forks/otel-metric-validator/internal/metricmap"
)

// Result holds the expected data points derived from the DB and the probe time.
type Result struct {
	Expected  map[string]metricmap.Expected
	ProbeTime time.Time
	Errors    []string
}

// DSN builds the go-ora connection URL exactly as the receiver does.
func DSN(cfg config.Config) string {
	return go_ora.BuildUrl(cfg.Host, cfg.Port, cfg.Service, cfg.User, cfg.Password, nil)
}

// Probe connects, runs every mapped query once, and returns expected data points.
func Probe(cfg config.Config) (Result, error) {
	db, err := sql.Open("oracle", DSN(cfg))
	if err != nil {
		return Result{}, fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	res := Result{Expected: map[string]metricmap.Expected{}, ProbeTime: time.Now()}
	for _, key := range metricmap.AllQueryKeys() {
		query := metricmap.QuerySQL(key, cfg.IsCDB())
		rows, err := runQuery(db, query)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("query %q failed: %v", key, err))
			continue
		}
		for _, exp := range metricmap.ExpectedFor(key, rows) {
			res.Expected[exp.Key()] = exp
		}
	}
	return res, nil
}

// runQuery executes SQL and returns rows as maps with UPPERCASE column names.
// Duplicate column names (e.g. the resource-limit LIMIT_VALUE) follow last-wins,
// matching the receiver's row map.
func runQuery(db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	upper := make([]string, len(cols))
	for i, c := range cols {
		upper[i] = strings.ToUpper(c)
	}

	var out []map[string]any
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, name := range upper {
			row[name] = holders[i] // later duplicate columns overwrite earlier ones
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
