// Package metricmap is a standalone mirror of the nroracledbreceiver scraper.
//
// For each Oracle monitoring query the receiver runs, it records how rows become
// metric data points: which stat/row maps to which metric, what attributes it
// attaches, and any unit transform (e.g. cpu_time is divided by 100).
//
// The SQL strings are copied verbatim from the receiver source of truth:
//
//	opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go
//
// Phase 1 (implemented here): metrics that map directly from a single query row,
// optionally with a fixed unit transform. Phase 2 (receiver-computed values such
// as the v$sysmetric utilization/ratio metrics) are listed in ComputedSkip and
// reported as SKIPPED rather than silently dropped.
package metricmap

import (
	"sort"
	"strconv"
	"strings"
)

// SCOPE is the fork's instrumentation scope name.
const SCOPE = "github.com/newrelic-forks/opentelemetry-collector-contrib/receiver/nroracledbreceiver"

// scopeMarker is the substring shared by both the fork's scope
// (…/nroracledbreceiver) and upstream's (…/oracledbreceiver), so the validator
// works against either build.
const scopeMarker = "oracledbreceiver"

// ScopeMatches reports whether an emitted scope name belongs to the oracle
// receiver (fork or upstream). An empty scope name matches (best-effort).
func ScopeMatches(name string) bool {
	return name == "" || strings.Contains(name, scopeMarker)
}

// Value types: SUM is a cumulative monotonic counter (counter tolerance), GAUGE
// is a level snapshot (gauge tolerance).
const (
	SUM   = "sum"
	GAUGE = "gauge"
)

// Expected is one expected data point derived from the database.
type Expected struct {
	Metric    string
	Attrs     map[string]string
	Value     float64
	ValueType string
}

// Key returns the canonical (metric, attrs) join key. Empty-valued attributes are
// dropped so a missing/empty oracle.db.pdb matches on both sides.
func (e Expected) Key() string { return Key(e.Metric, e.Attrs) }

// Key builds the canonical join key for a metric and its attributes.
func Key(metric string, attrs map[string]string) string {
	return metric + "\x00" + NormAttrs(attrs)
}

// NormAttrs renders attributes as a stable string, dropping empty values.
func NormAttrs(attrs map[string]string) string {
	parts := make([]string, 0, len(attrs))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// AttrsFromKey reconstructs the attribute map from a Key (for reporting).
func AttrsFromKey(key string) (string, map[string]string) {
	metric, rest, _ := strings.Cut(key, "\x00")
	attrs := map[string]string{}
	if rest != "" {
		for _, kv := range strings.Split(rest, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				attrs[k] = v
			}
		}
	}
	return metric, attrs
}

// ---------------------------------------------------------------------------
// SQL (verbatim from scraper.go). Keyed by logical name; pdb vs cdb variants.
// ---------------------------------------------------------------------------
const (
	statsSQL    = "select * from v$sysstat"
	statsCDBSQL = `
		SELECT s.name AS NAME, s.value AS VALUE, c.name AS PDB_NAME
		FROM v$con_sysstat s
		JOIN v$containers c ON s.con_id = c.con_id`

	sessionCountSQL    = "select status, type, count(*) as VALUE FROM v$session GROUP BY status, type"
	sessionCountCDBSQL = "select s.status, s.type, c.name as PDB_NAME, count(*) as VALUE FROM v$session s, v$containers c WHERE s.con_id = c.con_id(+) GROUP BY s.status, s.type, c.name"

	systemResourceLimitsSQL = "select RESOURCE_NAME, CURRENT_UTILIZATION, LIMIT_VALUE, CASE WHEN TRIM(INITIAL_ALLOCATION) LIKE 'UNLIMITED' THEN '-1' ELSE TRIM(INITIAL_ALLOCATION) END as INITIAL_ALLOCATION, CASE WHEN TRIM(LIMIT_VALUE) LIKE 'UNLIMITED' THEN '-1' ELSE TRIM(LIMIT_VALUE) END as LIMIT_VALUE from v$resource_limit"

	tablespaceUsageSQL = `
		select um.TABLESPACE_NAME, um.USED_SPACE, um.TABLESPACE_SIZE, ts.BLOCK_SIZE
		FROM DBA_TABLESPACE_USAGE_METRICS um INNER JOIN DBA_TABLESPACES ts
		ON um.TABLESPACE_NAME = ts.TABLESPACE_NAME`
	tablespaceUsageCDBSQL = `
		SELECT c.name AS PDB_NAME, t.TABLESPACE_NAME, m.USED_SPACE, m.TABLESPACE_SIZE, t.BLOCK_SIZE
		FROM CDB_TABLESPACE_USAGE_METRICS m, CDB_TABLESPACES t, v$containers c
		WHERE m.con_id(+) = t.con_id AND t.con_id = c.con_id AND m.TABLESPACE_NAME(+) = t.TABLESPACE_NAME`

	sgaInfoSQL          = "SELECT NAME, BYTES FROM v$sgainfo"
	dataDictHitRatioSQL = "SELECT (1-(SUM(getmisses)/SUM(gets))) * 100 as DATA_DICTIONARY_HIT_RATIO FROM v$rowcache WHERE getmisses + gets <> 0"
	storageUsageSQL     = "WITH total_bytes AS (SELECT SUM(bytes) AS total FROM dba_data_files) SELECT (total - (SELECT SUM(bytes) FROM dba_free_space)) AS USED_DB_SIZE, total AS ALLOCATED_DB_SIZE FROM total_bytes"
)

// sgaMaxComponent is the v$sgainfo NAME that maps to oracledb.sga.limit.
const sgaMaxComponent = "Maximum SGA Size"

// QuerySQL returns the SQL for a query key, choosing the CDB or PDB variant.
func QuerySQL(key string, isCDB bool) string {
	pdb, cdb := "", ""
	switch key {
	case "sysstat":
		pdb, cdb = statsSQL, statsCDBSQL
	case "session_count":
		pdb, cdb = sessionCountSQL, sessionCountCDBSQL
	case "resource_limits":
		pdb, cdb = systemResourceLimitsSQL, systemResourceLimitsSQL
	case "tablespace":
		pdb, cdb = tablespaceUsageSQL, tablespaceUsageCDBSQL
	case "sga":
		pdb, cdb = sgaInfoSQL, sgaInfoSQL
	case "data_dict":
		pdb, cdb = dataDictHitRatioSQL, dataDictHitRatioSQL
	case "storage":
		pdb, cdb = storageUsageSQL, storageUsageSQL
	}
	if isCDB {
		return cdb
	}
	return pdb
}

// AllQueryKeys lists the Phase-1 query keys.
func AllQueryKeys() []string {
	return []string{"sysstat", "session_count", "resource_limits", "tablespace", "sga", "data_dict", "storage"}
}

// ---------------------------------------------------------------------------
// v$sysstat: stat NAME -> metric
// ---------------------------------------------------------------------------

// sysstatCounters are simple cumulative counters (no transform).
var sysstatCounters = map[string]string{
	"execute count":                               "oracledb.executions",
	"parse count (total)":                         "oracledb.parse_calls",
	"parse count (hard)":                          "oracledb.hard_parses",
	"enqueue deadlocks":                           "oracledb.enqueue_deadlocks",
	"exchange deadlocks":                          "oracledb.exchange_deadlocks",
	"logons cumulative":                           "oracledb.logons",
	"user commits":                                "oracledb.user_commits",
	"user rollbacks":                              "oracledb.user_rollbacks",
	"physical reads":                              "oracledb.physical_reads",
	"physical reads direct":                       "oracledb.physical_reads_direct",
	"physical read IO requests":                   "oracledb.physical_read_io_requests",
	"physical writes":                             "oracledb.physical_writes",
	"physical writes direct":                      "oracledb.physical_writes_direct",
	"physical write IO requests":                  "oracledb.physical_write_io_requests",
	"queries parallelized":                        "oracledb.queries_parallelized",
	"DDL statements parallelized":                 "oracledb.ddl_statements_parallelized",
	"DML statements parallelized":                 "oracledb.dml_statements_parallelized",
	"Parallel operations not downgraded":          "oracledb.parallel_operations_not_downgraded",
	"Parallel operations downgraded to serial":    "oracledb.parallel_operations_downgraded_to_serial",
	"Parallel operations downgraded 1 to 25 pct":  "oracledb.parallel_operations_downgraded_1_to_25_pct",
	"Parallel operations downgraded 25 to 50 pct": "oracledb.parallel_operations_downgraded_25_to_50_pct",
	"Parallel operations downgraded 50 to 75 pct": "oracledb.parallel_operations_downgraded_50_to_75_pct",
	"Parallel operations downgraded 75 to 99 pct": "oracledb.parallel_operations_downgraded_75_to_99_pct",
	"session logical reads":                       "oracledb.logical_reads",
	"db block gets":                               "oracledb.db_block_gets",
	"consistent gets":                             "oracledb.consistent_gets",
}

// cpuTimeStat is the only transformed counter: value is in tens of milliseconds.
const cpuTimeStat = "CPU used by this session"

// sysstatGauges are level snapshots sourced from v$sysstat.
var sysstatGauges = map[string]string{
	"session pga memory": "oracledb.pga_memory",
}

// ioEntry is a multi-attribute counter mapping.
type ioEntry struct {
	metric string
	attrs  map[string]string
}

var sysstatIO = map[string]ioEntry{
	"physical read bytes":                       {"oracledb.physical_io.transferred", map[string]string{"disk.io.direction": "read", "disk.io.type": "buffered"}},
	"physical write bytes":                      {"oracledb.physical_io.transferred", map[string]string{"disk.io.direction": "write", "disk.io.type": "buffered"}},
	"physical read total bytes":                 {"oracledb.physical_io.transferred", map[string]string{"disk.io.direction": "read", "disk.io.type": "total"}},
	"physical write total bytes":                {"oracledb.physical_io.transferred", map[string]string{"disk.io.direction": "write", "disk.io.type": "total"}},
	"physical read total IO requests":           {"oracledb.physical_io.requests", map[string]string{"disk.io.direction": "read", "disk.io.block_size": "all"}},
	"physical write total IO requests":          {"oracledb.physical_io.requests", map[string]string{"disk.io.direction": "write", "disk.io.block_size": "all"}},
	"physical read total multi block requests":  {"oracledb.physical_io.requests", map[string]string{"disk.io.direction": "read", "disk.io.block_size": "multi"}},
	"physical write total multi block requests": {"oracledb.physical_io.requests", map[string]string{"disk.io.direction": "write", "disk.io.block_size": "multi"}},
	"physical writes from cache":                {"oracledb.physical_io.cache_writes", map[string]string{}},
	"bytes received via SQL*Net from client":    {"oracledb.sqlnet.io.transferred", map[string]string{"network.io.direction": "receive", "destination.type": "client"}},
	"bytes sent via SQL*Net to client":          {"oracledb.sqlnet.io.transferred", map[string]string{"network.io.direction": "transmit", "destination.type": "client"}},
	"bytes received via SQL*Net from dblink":    {"oracledb.sqlnet.io.transferred", map[string]string{"network.io.direction": "receive", "destination.type": "dblink"}},
	"bytes sent via SQL*Net to dblink":          {"oracledb.sqlnet.io.transferred", map[string]string{"network.io.direction": "transmit", "destination.type": "dblink"}},
}

// resourceLimitCol pairs a metric with the v$resource_limit column it reads.
type resourceLimitCol struct {
	metric string
	col    string
}

var resourceLimitMap = map[string][]resourceLimitCol{
	"processes":         {{"oracledb.processes.usage", "CURRENT_UTILIZATION"}, {"oracledb.processes.limit", "LIMIT_VALUE"}},
	"sessions":          {{"oracledb.sessions.limit", "LIMIT_VALUE"}},
	"enqueue_locks":     {{"oracledb.enqueue_locks.usage", "CURRENT_UTILIZATION"}, {"oracledb.enqueue_locks.limit", "LIMIT_VALUE"}},
	"dml_locks":         {{"oracledb.dml_locks.usage", "CURRENT_UTILIZATION"}, {"oracledb.dml_locks.limit", "LIMIT_VALUE"}},
	"enqueue_resources": {{"oracledb.enqueue_resources.usage", "CURRENT_UTILIZATION"}, {"oracledb.enqueue_resources.limit", "LIMIT_VALUE"}},
	"transactions":      {{"oracledb.transactions.usage", "CURRENT_UTILIZATION"}, {"oracledb.transactions.limit", "LIMIT_VALUE"}},
}

// ComputedSkip lists receiver-computed metrics (v$sysmetric / v$osstat derived)
// that are reported as SKIPPED rather than validated.
var ComputedSkip = map[string]bool{
	"oracledb.database.cpu.utilization":      true,
	"oracledb.host.cpu.utilization":          true,
	"oracledb.buffer_cache.utilization":      true,
	"oracledb.library_cache.utilization":     true,
	"oracledb.shared_pool.utilization":       true,
	"oracledb.database.wait.utilization":     true,
	"oracledb.execution.utilization":         true,
	"oracledb.parse.utilization":             true,
	"oracledb.parse.rate":                    true,
	"oracledb.sort.ratio":                    true,
	"oracledb.redo_allocation.utilization":   true,
	"oracledb.sql_service.response.duration": true,
	"oracledb.storage.utilization":           true,
	"oracledb.system.cpu.load":               true,
	"system.cpu.physical.count":              true,
	"system.memory.limit":                    true,
	"oracledb.recycle_bin.limit":             true,
}

// metricValueType maps a metric name to SUM/GAUGE, for consumers (ingest check)
// that only know the metric name.
var metricValueType = buildValueTypes()

func buildValueTypes() map[string]string {
	vt := map[string]string{}
	for _, m := range sysstatCounters {
		vt[m] = SUM
	}
	vt["oracledb.cpu_time"] = SUM
	for _, e := range sysstatIO {
		vt[e.metric] = SUM
	}
	for _, m := range sysstatGauges {
		vt[m] = GAUGE
	}
	for _, pairs := range resourceLimitMap {
		for _, p := range pairs {
			vt[p.metric] = GAUGE
		}
	}
	for _, m := range []string{
		"oracledb.sessions.usage",
		"oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit",
		"oracledb.sga.usage", "oracledb.sga.limit",
		"oracledb.data_dictionary.hit_ratio", "oracledb.storage.usage",
	} {
		vt[m] = GAUGE
	}
	return vt
}

// ValueTypeOf returns SUM/GAUGE for a Phase-1 metric, ok=false if not mapped.
func ValueTypeOf(metric string) (string, bool) {
	vt, ok := metricValueType[metric]
	return vt, ok
}

// ---------------------------------------------------------------------------
// Extractors: rows (UPPERCASE column keys) -> []Expected
// ---------------------------------------------------------------------------

func pdbAttr(row map[string]any) map[string]string {
	if pdb := asString(row["PDB_NAME"]); pdb != "" {
		return map[string]string{"oracle.db.pdb": pdb}
	}
	return map[string]string{}
}

// ExpectedFor runs the extractor for a query's rows.
func ExpectedFor(queryKey string, rows []map[string]any) []Expected {
	switch queryKey {
	case "sysstat":
		return extractSysstat(rows)
	case "session_count":
		return extractSessionCount(rows)
	case "resource_limits":
		return extractResourceLimits(rows)
	case "tablespace":
		return extractTablespace(rows)
	case "sga":
		return extractSGA(rows)
	case "data_dict":
		return extractDataDict(rows)
	case "storage":
		return extractStorage(rows)
	}
	return nil
}

func extractSysstat(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		name := asString(row["NAME"])
		value, ok := toFloat(row["VALUE"])
		if name == "" || !ok {
			continue
		}
		base := pdbAttr(row)
		if metric, ok := sysstatCounters[name]; ok {
			out = append(out, Expected{metric, base, value, SUM})
		} else if name == cpuTimeStat {
			out = append(out, Expected{"oracledb.cpu_time", base, value / 100.0, SUM})
		} else if metric, ok := sysstatGauges[name]; ok {
			out = append(out, Expected{metric, base, value, GAUGE})
		} else if e, ok := sysstatIO[name]; ok {
			out = append(out, Expected{e.metric, mergeAttrs(e.attrs, base), value, SUM})
		}
	}
	return out
}

func extractSessionCount(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		value, ok := toFloat(row["VALUE"])
		if !ok {
			continue
		}
		attrs := map[string]string{
			"session_type":   asString(row["TYPE"]),
			"session_status": asString(row["STATUS"]),
		}
		for k, v := range pdbAttr(row) {
			attrs[k] = v
		}
		out = append(out, Expected{"oracledb.sessions.usage", attrs, value, GAUGE})
	}
	return out
}

func extractResourceLimits(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		name := asString(row["RESOURCE_NAME"])
		for _, p := range resourceLimitMap[name] {
			if value, ok := toFloat(row[p.col]); ok {
				out = append(out, Expected{p.metric, map[string]string{}, value, GAUGE})
			}
		}
	}
	return out
}

func extractTablespace(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		used, uok := toFloat(row["USED_SPACE"])
		block, bok := toFloat(row["BLOCK_SIZE"])
		tsName := asString(row["TABLESPACE_NAME"])
		if !uok || !bok || tsName == "" {
			continue
		}
		attrs := map[string]string{"tablespace_name": tsName}
		for k, v := range pdbAttr(row) {
			attrs[k] = v
		}
		out = append(out, Expected{"oracledb.tablespace_size.usage", copyAttrs(attrs), used * block, GAUGE})
		sizeRaw := row["TABLESPACE_SIZE"]
		size, sok := toFloat(sizeRaw)
		limit := -1.0
		if sizeRaw != nil && sok {
			limit = size * block
		}
		out = append(out, Expected{"oracledb.tablespace_size.limit", copyAttrs(attrs), limit, GAUGE})
	}
	return out
}

func extractSGA(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		name := asString(row["NAME"])
		value, ok := toFloat(row["BYTES"])
		if name == "" || !ok {
			continue
		}
		if name == sgaMaxComponent {
			out = append(out, Expected{"oracledb.sga.limit", map[string]string{}, value, GAUGE})
		} else {
			out = append(out, Expected{"oracledb.sga.usage", map[string]string{"oracledb.sga.component.name": name}, value, GAUGE})
		}
	}
	return out
}

func extractDataDict(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		if value, ok := toFloat(row["DATA_DICTIONARY_HIT_RATIO"]); ok {
			out = append(out, Expected{"oracledb.data_dictionary.hit_ratio", map[string]string{}, value, GAUGE})
		}
	}
	return out
}

func extractStorage(rows []map[string]any) []Expected {
	var out []Expected
	for _, row := range rows {
		if value, ok := toFloat(row["USED_DB_SIZE"]); ok {
			out = append(out, Expected{"oracledb.storage.usage", map[string]string{}, value, GAUGE})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mergeAttrs(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func copyAttrs(a map[string]string) map[string]string { return mergeAttrs(a, nil) }

func asString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return strings.TrimSpace(toString(v))
	}
}

func toString(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	default:
		return ""
	}
}

// toFloat converts a DB/JSON scalar to float64, ok=false for nil/empty/non-numeric.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case nil:
		return 0, false
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case []byte:
		return parseFloat(string(n))
	case string:
		return parseFloat(n)
	default:
		return 0, false
	}
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
