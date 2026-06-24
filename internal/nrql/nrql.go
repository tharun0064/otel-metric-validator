// Package nrql queries New Relic (NerdGraph NRQL) to see what landed in NRDB.
//
// Used by the ingest check to confirm NR's cumulative->delta conversion. The
// query builders and the response parser are pure functions (unit-tested); the
// network call is a thin wrapper.
package nrql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/newrelic-forks/otel-metric-validator/internal/config"
)

const nanosPerMS = 1_000_000

// ToMS converts a Unix-nanosecond timestamp to Unix milliseconds.
func ToMS(timeUnixNano int64) int64 { return timeUnixNano / nanosPerMS }

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `'`, `\'`)
}

func where(attrs map[string]string) string {
	parts := make([]string, 0, len(attrs))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s = '%s'", k, escape(v)))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return " WHERE " + strings.Join(parts, " AND ")
}

// BuildDeltaQuery builds sum(metric) over (since, until] — the total delta NR
// stored for the series. The metric is backtick-quoted because names ending in
// a NRQL reserved word (e.g. oracledb.sga.limit) otherwise fail to parse.
func BuildDeltaQuery(metric string, attrs map[string]string, sinceMS, untilMS int64) string {
	return fmt.Sprintf("SELECT sum(`%s`) FROM Metric%s SINCE %d UNTIL %d", metric, where(attrs), sinceMS, untilMS)
}

// BuildLatestQuery builds latest(metric) over the window (metric backtick-quoted;
// see BuildDeltaQuery).
func BuildLatestQuery(metric string, attrs map[string]string, sinceMS, untilMS int64) string {
	return fmt.Sprintf("SELECT latest(`%s`) FROM Metric%s SINCE %d UNTIL %d", metric, where(attrs), sinceMS, untilMS)
}

// BuildGraphQL wraps an NRQL string in the NerdGraph actor.account.nrql query.
func BuildGraphQL(accountID, nrql string) string {
	return "{ actor { account(id: " + accountID + ") " +
		`{ nrql(query: "` + nrql + `") { results } } } }`
}

// ParseScalar pulls the single numeric value out of a NerdGraph NRQL response.
// Returns value=nil when there are no rows; err set on GraphQL/shape errors.
func ParseScalar(payload []byte) (*float64, error) {
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Actor struct {
				Account struct {
					NRQL struct {
						Results []map[string]json.RawMessage `json:"results"`
					} `json:"nrql"`
				} `json:"account"`
			} `json:"actor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("unexpected response shape: %s", snippet(payload))
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}
	results := resp.Data.Actor.Account.NRQL.Results
	if len(results) == 0 {
		return nil, nil
	}
	// Stable order so a deterministic numeric column is chosen.
	keys := make([]string, 0, len(results[0]))
	for k := range results[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var num float64
		if err := json.Unmarshal(results[0][k], &num); err == nil {
			return &num, nil
		}
	}
	return nil, nil // row present but no numeric column
}

// Client posts NRQL queries to NerdGraph.
type Client struct {
	cfg     config.Config
	http    *http.Client
	timeout time.Duration
}

// New builds a Client from config.
func New(cfg config.Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{}, timeout: 30 * time.Second}
}

// Run executes an NRQL query and returns the scalar result.
func (c *Client) Run(nrqlQuery string) (*float64, error) {
	body, _ := json.Marshal(map[string]string{"query": BuildGraphQL(c.cfg.NRAccountID, nrqlQuery)})
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.NRNerdGraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("API-Key", c.cfg.NRAPIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, c.cfg.NRNerdGraphURL, snippet(buf.Bytes()))
	}
	return ParseScalar(buf.Bytes())
}

// snippet returns a trimmed, length-capped view of a response body for errors.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		s = "(empty body)"
	}
	return s
}
