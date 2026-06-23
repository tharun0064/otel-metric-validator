# otel-metric-validator — Specification

This is the spec the framework implements. It defines **what** is validated and
**how**, so the behaviour is auditable and `internal/metricmap` can be kept
in lockstep with the receiver. If the receiver changes, update this doc and the
map together.

The framework is a standalone Go module. It connects to Oracle with the same
driver the receiver uses (`github.com/sijms/go-ora/v2`, pure Go), so it negotiates
Native Network Encryption without an Oracle Instant Client.

---

## 1. Purpose & scope

Verify that the metric values an OpenTelemetry collector emits for the
`nroracledbreceiver` match the values obtained by running the receiver's *own*
monitoring SQL directly against Oracle.

- **In scope:** metric data points (name + attributes + value) that map directly
  from a single query row, optionally via a fixed unit transform.
- **Out of scope (this version):** receiver-*computed* metrics (utilization,
  ratios, rates derived from `v$sysmetric`/`v$osstat`), events
  (query_sample / top_query / session_wait), trace/log signals, and any change to
  the receiver or labs.

## 2. Source of truth

The mapping mirrors, verbatim where possible:

| What | File | Location |
|---|---|---|
| Monitoring SQL | `opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go` | const block, lines ~39–125 |
| `v$sysstat` stat → `Record<Metric>` | same | switch, lines ~455–570 |
| session / resource-limit / tablespace / SGA / storage / data-dict handlers | same | lines ~574–870 |
| metric type / unit / attributes / enum values | `…/nroracledbreceiver/metadata.yaml` | per-metric |
| emitted instrumentation scope | `…/internal/metadata/generated_status.go` | `ScopeName` |

Fork `ScopeName` = `github.com/newrelic-forks/opentelemetry-collector-contrib/receiver/nroracledbreceiver`;
upstream emits `…/receiver/oracledbreceiver`. The validator filters emitted
metrics by the **substring `oracledbreceiver`** (`metricmap.ScopeMatches`), so it
works against either build; an empty scope name is also accepted.

## 3. Data model

```
Expected (DB side)  : metric, attrs{}, value, value_type ∈ {sum, gauge}
Emitted  (OTel side): metric, attrs{}, value, time_unix_nano
join key            : (metric, frozenset(attrs))    # empty-valued attrs dropped
```

**Attribute normalization:** any attribute whose value is `""`/`None` is dropped
from the key. This makes an empty `oracle.db.pdb` (non-CDB connections) match an
absent one on the emitted side.

## 4. Query → metric mapping (Phase 1 — validated)

Connection mode selects the SQL variant (`VALIDATOR_CONTAINER_MODE`):
`pdb` → base query; `cdb` → the `v$con_sysstat` / `CDB_*` variant that adds
`PDB_NAME` → `oracle.db.pdb`.

### 4.1 `select * from v$sysstat`  (CDB: `v$con_sysstat` join `v$containers`)

Row `NAME` selects the metric; `VALUE` is the value. `PDB_NAME` (CDB only) →
`oracle.db.pdb`.

**Cumulative counters (`value_type = sum`), no transform:**

| stat NAME | metric |
|---|---|
| `execute count` | `oracledb.executions` |
| `parse count (total)` | `oracledb.parse_calls` |
| `parse count (hard)` | `oracledb.hard_parses` |
| `enqueue deadlocks` | `oracledb.enqueue_deadlocks` |
| `exchange deadlocks` | `oracledb.exchange_deadlocks` |
| `logons cumulative` | `oracledb.logons` |
| `user commits` | `oracledb.user_commits` |
| `user rollbacks` | `oracledb.user_rollbacks` |
| `physical reads` | `oracledb.physical_reads` |
| `physical reads direct` | `oracledb.physical_reads_direct` |
| `physical read IO requests` | `oracledb.physical_read_io_requests` |
| `physical writes` | `oracledb.physical_writes` |
| `physical writes direct` | `oracledb.physical_writes_direct` |
| `physical write IO requests` | `oracledb.physical_write_io_requests` |
| `session logical reads` | `oracledb.logical_reads` |
| `db block gets` | `oracledb.db_block_gets` |
| `consistent gets` | `oracledb.consistent_gets` |
| `queries parallelized` | `oracledb.queries_parallelized` |
| `DDL statements parallelized` | `oracledb.ddl_statements_parallelized` |
| `DML statements parallelized` | `oracledb.dml_statements_parallelized` |
| `Parallel operations not downgraded` | `oracledb.parallel_operations_not_downgraded` |
| `Parallel operations downgraded to serial` | `oracledb.parallel_operations_downgraded_to_serial` |
| `Parallel operations downgraded 1 to 25 pct` | `oracledb.parallel_operations_downgraded_1_to_25_pct` |
| `Parallel operations downgraded 25 to 50 pct` | `oracledb.parallel_operations_downgraded_25_to_50_pct` |
| `Parallel operations downgraded 50 to 75 pct` | `oracledb.parallel_operations_downgraded_50_to_75_pct` |
| `Parallel operations downgraded 75 to 99 pct` | `oracledb.parallel_operations_downgraded_75_to_99_pct` |

**Counter with transform:**

| stat NAME | metric | transform | rationale |
|---|---|---|---|
| `CPU used by this session` | `oracledb.cpu_time` | `value / 100` | stat is in tens of ms → seconds |

**Gauge from `v$sysstat`:**

| stat NAME | metric | value_type |
|---|---|---|
| `session pga memory` | `oracledb.pga_memory` | gauge (current allocated bytes — verified non-monotonic) |

**Multi-attribute counters (`value_type = sum`):**

| stat NAME | metric | fixed attributes |
|---|---|---|
| `physical read bytes` | `oracledb.physical_io.transferred` | `disk.io.direction=read, disk.io.type=buffered` |
| `physical write bytes` | `oracledb.physical_io.transferred` | `disk.io.direction=write, disk.io.type=buffered` |
| `physical read total bytes` | `oracledb.physical_io.transferred` | `disk.io.direction=read, disk.io.type=total` |
| `physical write total bytes` | `oracledb.physical_io.transferred` | `disk.io.direction=write, disk.io.type=total` |
| `physical read total IO requests` | `oracledb.physical_io.requests` | `disk.io.direction=read, disk.io.block_size=all` |
| `physical write total IO requests` | `oracledb.physical_io.requests` | `disk.io.direction=write, disk.io.block_size=all` |
| `physical read total multi block requests` | `oracledb.physical_io.requests` | `disk.io.direction=read, disk.io.block_size=multi` |
| `physical write total multi block requests` | `oracledb.physical_io.requests` | `disk.io.direction=write, disk.io.block_size=multi` |
| `physical writes from cache` | `oracledb.physical_io.cache_writes` | — |
| `bytes received via SQL*Net from client` | `oracledb.sqlnet.io.transferred` | `network.io.direction=receive, destination.type=client` |
| `bytes sent via SQL*Net to client` | `oracledb.sqlnet.io.transferred` | `network.io.direction=transmit, destination.type=client` |
| `bytes received via SQL*Net from dblink` | `oracledb.sqlnet.io.transferred` | `network.io.direction=receive, destination.type=dblink` |
| `bytes sent via SQL*Net to dblink` | `oracledb.sqlnet.io.transferred` | `network.io.direction=transmit, destination.type=dblink` |

### 4.2 `v$session` count  →  `oracledb.sessions.usage` (gauge)

Group-by `status,type` (CDB adds `c.name`). Value = `count(*)`.
Attributes: `session_type=TYPE`, `session_status=STATUS`, `oracle.db.pdb=PDB_NAME`.

### 4.3 `v$resource_limit`  →  usage/limit gauges

`RESOURCE_NAME` selects the metric pair; `LIMIT_VALUE` is the normalized
(`UNLIMITED`→`-1`) column (duplicate column name — **last column wins**, matching
the receiver's row map).

| RESOURCE_NAME | metric ← column |
|---|---|
| `processes` | `oracledb.processes.usage ← CURRENT_UTILIZATION`, `oracledb.processes.limit ← LIMIT_VALUE` |
| `sessions` | `oracledb.sessions.limit ← LIMIT_VALUE` (usage comes from §4.2) |
| `enqueue_locks` | `…enqueue_locks.usage/limit` |
| `dml_locks` | `…dml_locks.usage/limit` |
| `enqueue_resources` | `…enqueue_resources.usage/limit` |
| `transactions` | `…transactions.usage/limit` |

### 4.4 Tablespace (`DBA_TABLESPACE_USAGE_METRICS`; CDB: `CDB_*`)  → gauges

- `oracledb.tablespace_size.usage = USED_SPACE × BLOCK_SIZE`
- `oracledb.tablespace_size.limit = TABLESPACE_SIZE × BLOCK_SIZE` (or `-1` when size is empty/unlimited)
- attrs: `tablespace_name=TABLESPACE_NAME`, `oracle.db.pdb=PDB_NAME` (CDB)

### 4.5 `v$sgainfo`  → gauges

Per row: `BYTES` is the value, `NAME` the component.
- `NAME == "Maximum SGA Size"` → `oracledb.sga.limit`
- otherwise → `oracledb.sga.usage` with `oracledb.sga.component.name=NAME`

### 4.6 `v$rowcache`  →  `oracledb.data_dictionary.hit_ratio` (gauge)
Value = `DATA_DICTIONARY_HIT_RATIO`.

### 4.7 storage (`dba_data_files`/`dba_free_space`)  →  `oracledb.storage.usage` (gauge)
Value = `USED_DB_SIZE` bytes. (`storage.utilization` is computed → Phase 2.)

## 5. Phase 2 — `SKIPPED` (declared, not yet validated)

Reported as `SKIPPED` (never silently dropped). These are computed by the
receiver from `v$sysmetric` / `v$osstat`; validating them means replicating the
arithmetic. Enumerated in `metric_map.COMPUTED_SKIP`:

`*.utilization` family (`database.cpu`, `host.cpu`, `buffer_cache`, `library_cache`,
`shared_pool`, `database.wait`, `execution`, `parse`, `redo_allocation`, `storage`),
`oracledb.parse.rate`, `oracledb.sort.ratio`,
`oracledb.sql_service.response.duration`, `oracledb.system.cpu.load`,
`system.cpu.physical.count`, `system.memory.limit`, `oracledb.recycle_bin.limit`.

## 6. Ingest formats

### 6.1 `otlp-json` (recommended)
Newline-delimited OTLP/JSON from a `file` exporter. Per line:
`resourceMetrics[] → scopeMetrics[] (filtered to ScopeName) → metrics[] →
(gauge|sum).dataPoints[]`. From each data point: `name`, attributes
(`{key,value:{stringValue|intValue|doubleValue|boolValue}}`), value
(`asDouble` or `asInt`), `timeUnixNano`. The **latest** point per (metric, attrs)
wins.

### 6.2 `debug-log` (best-effort)
Text from the `debug`/`logging` exporter (verbosity detailed). State machine on
`-> Name:`, `Data point attributes:` (`-> key: Type(value)`), `Value:`. Format may
drift across collector versions; `otlp-json` is authoritative.

## 7. Comparison semantics

For each join key in `expected ∪ emitted`:

| condition | status | fails run? |
|---|---|---|
| in both, within tolerance | `OK` | no |
| in both, beyond tolerance | `MISMATCH` | **yes** |
| DB only | `MISSING_IN_INGEST` (metric likely disabled in collector) | no |
| OTel only, name ∈ `COMPUTED_SKIP` | `SKIPPED` | no |
| OTel only, otherwise | `MISSING_IN_DB` (no DB mapping) | no |

**Tolerance** (`compare.WithinTolerance`):
- if `|expected| ≤ VALIDATOR_ABS_EPSILON` → pass when `|actual−expected| ≤ abs_epsilon`
  (avoids divide-by-near-zero for rare-event counters);
- else relative: `|actual−expected| / |expected| ≤ tol`, where
  `tol = VALIDATOR_TOLERANCE_COUNTER` for `sum`, `VALIDATOR_TOLERANCE_GAUGE` for `gauge`.

**Exit codes (one-shot):** `0` no mismatch · `1` ≥1 `MISMATCH` · `2` setup error
(bad config / missing ingest file). Only `MISMATCH` fails — `MISSING_*` and
`SKIPPED` are informational.

**Timing:** the collector scrapes on its own interval; the probe runs a moment
later, so cumulative counters advance in between. The counter tolerance absorbs
this drift; the probe runs immediately after reading the ingest snapshot to
minimize skew. This is inherent and reported transparently.

## 8. Configuration contract (env)

| var | required | default | meaning |
|---|---|---|---|
| `ORACLE_HOST` | ✓ | — | DB host |
| `ORACLE_PORT` | | `1521` | DB port |
| `ORACLE_SERVICE` | ✓ | — | service / PDB name |
| `ORACLE_MONITORING_USER` | ✓ | — | user with SELECT on the V$ views |
| `ORACLE_MONITORING_PASSWORD` | ✓ | — | password |
| `VALIDATOR_INGEST_PATH` | ✓ | — | path to collector output |
| `VALIDATOR_INGEST_FORMAT` | | `otlp-json` | `otlp-json` \| `debug-log` |
| `VALIDATOR_CONTAINER_MODE` | | `pdb` | `pdb` \| `cdb` |
| `VALIDATOR_TOLERANCE_GAUGE` | | `0.02` | relative tolerance, gauges |
| `VALIDATOR_TOLERANCE_COUNTER` | | `0.05` | relative tolerance, counters |
| `VALIDATOR_ABS_EPSILON` | | `1` | absolute tolerance near zero |
| `VALIDATOR_WATCH_INTERVAL` | | `30` | seconds between `--watch` cycles |
| `NEW_RELIC_API_KEY` | ✓ for `--check-ingest` | — | NerdGraph (user) API key |
| `NEW_RELIC_ACCOUNT_ID` | ✓ for `--check-ingest` | — | NR account id |
| `NEW_RELIC_NERDGRAPH_URL` | | `https://api.newrelic.com/graphql` | NerdGraph endpoint (prod/staging) |

## 9. Module ↔ spec map

| package | implements |
|---|---|
| `internal/config` | §8 (env load, `.env`, inline-comment handling) |
| `internal/metricmap` | §2, §4, §5 (`ComputedSkip`), `ValueTypeOf` (§11) |
| `internal/dbprobe` | §4 execution (go-ora `sql.Open`, run SQL, last-column-wins, build `Expected`) |
| `internal/ingest` | §6, plus `ReadOTLPSeries` endpoints (§11) |
| `internal/compare` | §7 (statuses + tolerance) |
| `internal/nrql` | §11 NerdGraph client + NRQL builders + parser |
| `internal/ingestcheck` | §11 delta/latest verification |
| `internal/report` | §7 + §11 rendering |
| `cmd/validator` | run modes + exit codes (§7, §11) |

## 10. Invariants / tests

`*_test.go` (run with `go test ./...`) assert the spec without a DB or collector:
- `cpu_time` transform = `÷100`; counters untransformed; `pga_memory` is gauge.
- IO/SQL\*Net fixed attributes; session/resource-limit/tablespace/SGA extraction.
- OTLP-JSON parsing (gauge & sum, latest-wins, foreign-scope filtered) and
  debug-log parsing.
- tolerance bands (counter vs gauge), near-zero epsilon, status classification,
  and empty-pdb ↔ absent-pdb matching.
- NRQL builders/parser, the telescoping delta math, gauge-uses-latest, and the
  skip/no-data paths (§11).

## 11. NRQL ingest check (`--check-ingest`)

A third leg that verifies what actually landed in NRDB after New Relic's ingest
pipeline — in particular that the **cumulative → delta** conversion is correct.

**Principle.** The collector emits *cumulative* counters; NR delta-converts at
ingest and stores per-interval deltas. By the telescoping property, for a series
with cumulative values `C(t0) … C(tN)`:

```
Σ deltas over (t0, tN]  ==  C(tN) − C(t0)
```

So for each emitted **counter** series we compute `expected = last − first` from
the OTLP file's endpoints and query NRDB:

```
SELECT sum(<metric>) FROM Metric WHERE <attrs> SINCE <t0_ms + 1> UNTIL <tN_ms>
```

`SINCE t0+1ms` excludes the delta stored *at* `t0` (which belongs to the prior
interval), so the NRDB sum equals `C(tN) − C(t0)`. Pass if within
`VALIDATOR_TOLERANCE_COUNTER`.

**Gauges** are stored as-is (no delta), so `expected = last emitted value` and we
query `SELECT latest(<metric>) … SINCE t0 UNTIL tN+1s`, comparing within
`VALIDATOR_TOLERANCE_GAUGE`.

**Inputs.** Requires `VALIDATOR_INGEST_FORMAT=otlp-json` (needs per-point
timestamps; the debug-log path is not supported) and NR creds (§8). Series
endpoints come from `ingest.ReadOTLPSeries` (min/max `timeUnixNano` per
`(metric, attrs)`).

**Transport.** `nrql.Client` POSTs a NerdGraph query
(`{ actor { account(id) { nrql(query:"…"){ results } } } }`) with header
`API-Key`, using only `net/http`. `nrql.ParseScalar` returns the single numeric
result (or `nil` when there are no rows).

**Statuses / exit code.**

| status | meaning | fails run? |
|---|---|---|
| `INGEST_OK` | NRDB value within tolerance of expected | no |
| `INGEST_MISMATCH` | NRDB value disagrees → bad delta/value | **yes** |
| `INGEST_NO_DATA` | NRQL returned no rows (ingest lag, or attr/window mismatch) | no |
| `INGEST_ERROR` | NerdGraph/HTTP/GraphQL error | no |
| `INGEST_SKIPPED` | computed metric, unmapped, or `<2` points for a delta | no |

The one-shot exit code is `1` if **either** a `MISMATCH` (§7) or an
`INGEST_MISMATCH` occurs.

**Caveats.** Counter resets / collector restarts inside the window break the
telescoping identity (NR resets the delta baseline) and will surface as
`INGEST_MISMATCH` — widen the window selection or ignore across restarts. Ingest
lag means a freshly written window may read as `INGEST_NO_DATA` until NRDB
catches up.
