# How the validator compares: collector logs ↔ DB ↔ NRDB

This explains, end to end, how `otel-metric-validator` checks that a metric is
correct at every hop — from the database, through the collector, into New Relic —
and shows the exact SQL and NRQL it generates, with a worked delta example.

For the formal mapping see [`../specs/SPEC.md`](../specs/SPEC.md); for usage see
[`USAGE.md`](USAGE.md).

---

## 1. The three sources

A single metric (say `oracledb.executions` for PDB2) exists in three places. The
validator reads all three and cross-checks them:

| # | Source | What it is | How the validator reads it |
|---|---|---|---|
| A | **DB (ground truth)** | the live value in Oracle's `V$` views | runs the receiver's *own* SQL directly via go-ora |
| B | **Collector logs / output** | what the receiver emitted | reads the OTLP-JSON the `file` exporter wrote (the "logs") |
| C | **NRDB** | what New Relic actually stored | NRQL (NerdGraph) `sum()` / `latest()` |

Two independent comparisons are run:

```
              ┌── A: DB  (receiver SQL, run directly) ─────────┐
              │                                                ├─►  DB check     (A vs B)
 collector ──►│── B: OTLP-JSON file (the emitted "logs") ──────┤
              │                                                └─►  ingest check (B vs C)
              └── C: NRDB (NRQL) ──────────────────────────────┘
```

- **DB check (A vs B):** does the value the collector *emitted* match what the
  database *reports*? Catches receiver bugs in query, column mapping, or unit
  transforms.
- **Ingest check (B vs C):** does what NR *stored* match what the collector
  *emitted*? Confirms New Relic's cumulative→delta conversion. Enabled with
  `--check-ingest`.

The collector's `file` exporter output is what we call the **logs** here — it's a
newline-delimited OTLP-JSON record of exactly what the collector shipped, so it's
the trustworthy "what was emitted" reference for both comparisons.

---

## 2. How the rows are joined

Every value is keyed by **`(metric name, attributes)`**, e.g.

```
oracledb.executions  {oracle.db.pdb=PDB2}
oracledb.physical_io.requests  {disk.io.direction=read, disk.io.block_size=all, oracle.db.pdb=CDB$ROOT}
```

Empty-valued attributes are dropped before keying, so a missing `oracle.db.pdb`
on one side matches an empty one on the other. The DB-side `Expected` and the
emitted `Emitted` are matched on this key; the result is `OK`, `MISMATCH`,
`MISSING_IN_INGEST` (DB only), `MISSING_IN_DB` (emitted only), or `SKIPPED`.

> Resource attributes (`host.name`, `oracledb.instance.name`, …) are **not** part
> of this join key — the SQL probe has no such column. They're used only to scope
> the NRQL in the ingest check (§5).

---

## 3. How the SQL is written (source A)

The validator does **not** invent SQL. It uses the receiver's monitoring queries
verbatim — the same strings from
`opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go` — copied
into `internal/metricmap`. There is one query per logical group, with a PDB and a
CDB-root variant where the receiver has one.

Examples (exactly what `--show-queries` prints under `== DB calls ==`):

```sql
-- v$sysstat counters/gauges (PDB mode)
select * from v$sysstat

-- v$sysstat, CDB-root mode → per-PDB via v$con_sysstat
SELECT s.name AS NAME, s.value AS VALUE, c.name AS PDB_NAME
FROM v$con_sysstat s JOIN v$containers c ON s.con_id = c.con_id

-- sessions (CDB)
select s.status, s.type, c.name as PDB_NAME, count(*) as VALUE
FROM v$session s, v$containers c WHERE s.con_id = c.con_id(+)
GROUP BY s.status, s.type, c.name

-- resource limits
select RESOURCE_NAME, CURRENT_UTILIZATION, LIMIT_VALUE, … from v$resource_limit
```

Each query's rows are turned into `(metric, attributes, value)` by an extractor
that mirrors the receiver's `Record*` switch, including:

- **column → value** (`VALUE`, `BYTES`, `CURRENT_UTILIZATION`, …),
- **attribute extraction** (`PDB_NAME → oracle.db.pdb`, `STATUS/TYPE → session_*`),
- **unit transforms** — e.g. `cpu_time` is `value / 100` ("tens of ms" → seconds),
  `tablespace_size.usage = USED_SPACE × BLOCK_SIZE`.

The result is the **ground-truth value** the collector *should* have emitted.

### There is no dynamic `WHERE` on the DB side
This is the key difference from the NRQL side. The SQL is **never** parameterized
per metric or per PDB. Each query fetches **all** rows for its view, and the only
`WHERE` clauses are the receiver's own **static** ones, kept verbatim — e.g.:

```sql
... FROM v$sysmetric WHERE group_id = 2
... FROM v$rowcache  WHERE getmisses + gets <> 0
... FROM v$osstat    WHERE STAT_NAME IN ('LOAD', 'NUM_CPUS', 'PHYSICAL_MEMORY_BYTES')
... FROM v$session s, v$containers c WHERE s.con_id = c.con_id(+)   -- an outer join, not a filter
```

The per-metric and per-attribute **filtering happens in Go**, in the extractor —
not in SQL. For example, for `v$sysstat` the validator runs `select * from
v$sysstat` (all stats), then in code:

- matches each row's `NAME` against the stat→metric table (e.g. `"db block gets"
  → oracledb.db_block_gets`), and
- reads the attribute columns the row carries (`PDB_NAME → oracle.db.pdb`,
  `STATUS/TYPE → session_status/session_type`).

So the "filter" that selects a single `(metric, attrs)` on the DB side is the
**row-to-metric mapping + column reads in code**, not a SQL `WHERE`. That's why the
DB-check value for, say, `oracledb.executions {oracle.db.pdb=PDB2}` comes from the
one `v$con_sysstat` row whose `NAME='execute count'` and `PDB_NAME='PDB2'` — picked
out in Go, not by a SQL predicate.

### v$sysmetric is queried twice in CDB mode
To match the receiver exactly, in CDB mode the validator runs **both**
`v$con_sysmetric` (per-PDB) and `v$sysmetric`, taking from `v$sysmetric` only the
metric names not already returned per-PDB (the instance-scoped ones like Host CPU,
Library Cache). See `metricmap.ExpectedForSysmetricCDB`.

---

## 4. How the logs (source B) are read

The `file` exporter writes one OTLP-JSON object per line per scrape. The reader
walks `resourceMetrics → scopeMetrics → metrics → (gauge|sum).dataPoints`, keeping
only the oracle receiver's scope, and pulls from each data point:

- `name`, the data-point `attributes`, the value (`asInt`/`asDouble`), `timeUnixNano`,
- and the **resource** attributes (`host.name`, …) for NRQL scoping.

It produces two views of the same file:

- **latest point per series** → used by the DB check (compare current emitted
  value to the DB), and
- **first & last point per series** (a `Series`) → used by the ingest check to
  form a delta (§5).

---

## 5. The delta: how logs are turned into a delta and compared with NRDB

This is the heart of the ingest check. **Cumulative counters** (executions, reads,
bytes, …) are emitted by the collector as an ever-growing total. New Relic
**delta-converts** them at ingest: it stores the per-interval *increase*. So to
verify NR stored them correctly we use the **telescoping identity**:

```
sum of all per-interval deltas over (t0, tN]  ==  C(tN) − C(t0)
```

where `C(t)` is the cumulative value at time `t`.

So the validator:

1. From the **logs**, takes the series' first and last cumulative points:
   `C(t0)` at `t0` and `C(tN)` at `tN`.
2. Computes **EXPECTED = C(tN) − C(t0)** — the true increase over the window.
3. Asks **NRDB** for `sum(metric)` over the same window. If NR's delta conversion
   is correct, that sum must equal EXPECTED.
4. Compares within the counter tolerance.

**Gauges** (pga_memory, utilizations, sizes) are stored as-is — no delta — so the
ingest check uses `latest()` and compares to the last emitted value.

### Window boundaries (why `+1`)
NR stamps each delta at the **end** of its interval, and NRQL `SINCE` is inclusive
while `UNTIL` is **exclusive**. So:

- `SINCE t0_ms + 1` → drops the delta stamped at `t0` (it belongs to the interval
  *before* the window), keeping `t1 … tN`.
- `UNTIL tN_ms + 1` → includes the delta stamped at `tN` (the final interval).

Net: the window sums exactly the deltas `t1 … tN` = `C(tN) − C(t0)` = EXPECTED.

### Worked example — `oracledb.executions` on PDB2

Suppose the OTLP file (logs) for `oracledb.executions {oracle.db.pdb=PDB2}` has,
within the chosen window, these cumulative points:

| time | cumulative value emitted |
|---|---|
| `t0 = 10:00:00.000` | `1,814,000,000` |
| … (scrapes every 10 s) | … |
| `tN = 10:30:00.000` | `1,814,421,413` |

**Step 1 – delta from the logs:**
```
EXPECTED = C(tN) − C(t0) = 1,814,421,413 − 1,814,000,000 = 421,413
```

**Step 2 – ask NRDB for the same window** (the actual NRQL emitted):
```sql
SELECT sum(`oracledb.executions`) FROM Metric
WHERE host.name = 'newrelicoracletb….oraclevcn.com:1521' AND oracle.db.pdb = 'PDB2'
SINCE 1782303666335 UNTIL 1782305146334
```
(`SINCE` = `t0_ms + 1`, `UNTIL` = `tN_ms + 1`, in epoch-ms.)

NRDB returns, say, `402,102` → it summed 30 minutes of per-interval deltas.

**Step 3 – compare:**
```
Δ = |402,102 − 421,413| / 421,413 = 4.58%
```
Within the 5 % counter tolerance → **INGEST_OK**. (If NR had instead stored the
raw cumulative and `sum()` returned ~1.8 B, this would be a glaring `INGEST_MISMATCH`
— which is exactly why a *bounded* window matters: it makes EXPECTED a small
partial delta that can't be confused with the cumulative. See §7.)

---

## 6. How the NRQL is written (source C)

Two shapes, generated in `internal/nrql`. The metric name is **backtick-quoted**
(names ending in a NRQL reserved word like `oracledb.sga.limit` otherwise fail to
parse), and a `WHERE` clause is built from the attributes (sorted, non-empty,
single-quoted), plus the resource scope attribute(s).

**Counter (delta):**
```sql
SELECT sum(`<metric>`) FROM Metric
WHERE host.name = '<instance>' [AND <attr> = '<v>' …]
SINCE <t0_ms+1> UNTIL <tN_ms+1>
```

**Gauge (latest):**
```sql
SELECT latest(`<metric>`) FROM Metric
WHERE host.name = '<instance>' [AND <attr> = '<v>' …]
SINCE <t0_ms> UNTIL <tN_ms+1000>
```

The `host.name = '…'` filter (configurable via `VALIDATOR_NR_SCOPE_ATTRS`, default
`host.name`) **scopes the query to this one DB instance** — without it, `sum()` /
`latest()` would aggregate across every Oracle instance reporting to the account.

### How the NRQL `WHERE` is built
Unlike the DB side, the NRQL `WHERE` **is** constructed dynamically, from the
series' attributes plus the resource scope attribute(s). The rules (`nrql.where`):

1. Start from the data-point attributes of the series, e.g.
   `{disk.io.direction: read, disk.io.block_size: all, oracle.db.pdb: CDB$ROOT}`.
2. Merge in the configured scope attributes from the OTLP **resource**, e.g.
   `{host.name: newrelicoracletb….oraclevcn.com:1521}` (the ingest check only).
3. **Drop any attribute whose value is empty** — so an absent `oracle.db.pdb`
   produces no clause, matching how the join key treats empties.
4. For each remaining `k → v`, emit `k = '<v>'`, single-quoting the value and
   escaping `\` and `'` in it.
5. **Sort** the clauses (lexicographically by the rendered `k = 'v'` string) so the
   query is deterministic, then join them with ` AND ` and prefix ` WHERE `.

The metric itself is **not** in the `WHERE` — it's named directly inside the
aggregate (`` sum(`oracledb.db_block_gets`) ``), so there is no `metricName = …`
clause (that's a dashboard idiom, not what the validator emits).

Worked example — the series
`oracledb.physical_io.requests {disk.io.block_size=all, disk.io.direction=read, oracle.db.pdb=CDB$ROOT}`
with scope `host.name` becomes:

```sql
SELECT sum(`oracledb.physical_io.requests`) FROM Metric
WHERE disk.io.block_size = 'all'
  AND disk.io.direction = 'read'
  AND host.name = 'newrelicoracletb….oraclevcn.com:1521'
  AND oracle.db.pdb = 'CDB$ROOT'
SINCE <t0_ms+1> UNTIL <tN_ms+1>
```

(The clauses are alphabetical because of the sort: `disk.io.block_size`,
`disk.io.direction`, `host.name`, `oracle.db.pdb`.)

### The two `WHERE`s are not the same — and that's intentional
- **DB side:** broad query + static receiver `WHERE`; the `(metric, attrs)`
  selection is done in Go.
- **NRDB side:** a dynamic `WHERE` rebuilt from the *same* attributes the DB
  extractor produced — **plus** `host.name` scoping that has no DB equivalent
  (the SQL probe targets a single instance by connection, so it needs no such
  filter).

Both ultimately identify the **same logical series**; they just reach it through
code-side mapping (DB) versus a generated predicate (NRDB).

---

## 7. Why the window is bounded

If the ingest window spans the whole OTLP file, `t0` is near collector start where
the cumulative ≈ 0, so `EXPECTED = C(tN) − C(t0) ≈ C(tN)` ≈ the full cumulative —
and NRDB's `sum()` over that same wide window ≈ the cumulative too. The check then
trivially matches and can't distinguish a correct delta conversion from NR simply
storing the cumulative.

`VALIDATOR_INGEST_WINDOW_MINUTES=N` restricts the series to the last *N* minutes so
`t0` is a non-zero mid-cumulative and `EXPECTED` is a genuine **partial** delta
(e.g. 421,413, not 1.8 B). Now the comparison really exercises the conversion.

---

## 8. Statuses & exit codes

| check | status | meaning | fails run? |
|---|---|---|---|
| DB | `OK` | DB and emitted agree within tolerance | no |
| DB | `MISMATCH` | disagree → receiver mapping/transform bug | **yes** |
| DB | `MISSING_IN_INGEST` | DB has it, collector didn't emit it | no |
| DB | `MISSING_IN_DB` | emitted, no DB mapping | no |
| DB | `SKIPPED` | reserved (computed metric) | no |
| ingest | `INGEST_OK` | NRDB matches EXPECTED | no |
| ingest | `INGEST_MISMATCH` | bad delta / value | **yes** |
| ingest | `INGEST_NO_DATA` | NRQL returned nothing (lag, or attr/window mismatch) | no |
| ingest | `INGEST_ERROR` | NerdGraph/HTTP/auth/region error | no |
| ingest | `INGEST_SKIPPED` | <2 points for a delta, or unmapped | no |

One-shot exit: `0` clean · `1` any `MISMATCH`/`INGEST_MISMATCH` · `2` setup error.

---

## 9. See exactly what ran

```bash
./run.sh --check-ingest --show-queries --metric executions
```
prints, before the report, every Oracle SQL (`== DB calls ==`) and every NRQL
(`== NRQL calls ==`) with the real `SINCE/UNTIL` and `WHERE` filled in — so any row
in the report can be reproduced by pasting the query into the NR query editor (use
the `sum(...)`/`latest(...)` query as-is; don't add a `= <value>` filter, since the
report's NRDB number is the aggregate result, not a stored data-point value).
