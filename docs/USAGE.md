# Using otel-metric-validator

A practical, end-to-end guide. For *how* the comparison and delta math work (with
a worked example, SQL, and NRQL) see [`COMPARISON.md`](COMPARISON.md); for the
formal mapping/semantics see [`../specs/SPEC.md`](../specs/SPEC.md); for a
high-level overview see [`../README.md`](../README.md).

---

## What you get

Two independent checks:

1. **DB check** (always on) — compares the metric value the **collector emitted**
   against the value from running the **receiver's own SQL** directly on Oracle.
   Catches receiver bugs in query / column mapping / unit transforms.
2. **Ingest check** (`--check-ingest`) — compares the **emitted cumulative**
   values against what landed in **NRDB** via NRQL, confirming New Relic's
   cumulative→delta conversion is correct.

```
 collector (nroracledbreceiver) ──file exporter──▶ otel-metrics.json ─┐
                                                                       ├─▶ validator
 Oracle  ──receiver's SQL, run directly───────────────────────────────┘     │
 NRDB    ──NRQL (NerdGraph), only with --check-ingest──────────────────────▶ │
                                                                         report + exit code
```

The validator is a Go program. It talks to Oracle with the same pure-Go driver
the receiver uses (`sijms/go-ora`), so it negotiates Native Network Encryption
out of the box — no Oracle Instant Client required.

---

## Prerequisites

- Go 1.23+ (for the local run) **or** Docker (for the container run).
- Network access to the Oracle instance the collector monitors, as a user with
  `SELECT` on the `V$`/`DBA_` views (the receiver's monitoring user works).
- A collector running the **`nroracledbreceiver`** fork (or upstream
  `oracledbreceiver`) with a **file exporter** enabled (steps below).
- For `--check-ingest` only: a New Relic **NerdGraph user API key** and **account id**.

---

## Step 1 — Point the collector's output at a file

The validator reads the metrics the collector emits. The robust way is a `file`
exporter writing newline-delimited OTLP JSON. Merge
[`../collector/file-exporter.partial.yaml`](../collector/file-exporter.partial.yaml)
into your collector config:

```yaml
exporters:
  file/validator:
    path: /shared/otel-metrics.json
    rotation:
      max_megabytes: 10
      max_backups: 2
service:
  pipelines:
    metrics/oracle:
      exporters: [otlphttp, file/validator]   # keep your existing exporters, add this one
```

Mount `/shared` somewhere you can read it, and make sure the path matches
`VALIDATOR_INGEST_PATH` (next step).

> Prefer scraping logs instead? Set `VALIDATOR_INGEST_FORMAT=debug-log` and feed it
> the `debug` exporter's stdout (`docker logs otel-collector > /shared/collector.log`).
> Works, but the text format is best-effort and **not** supported by `--check-ingest`.

## Step 2 — Configure

```bash
cd otel-metric-validator
cp .env.example .env
```

Edit `.env`:

```dotenv
ORACLE_HOST=localhost
ORACLE_PORT=1521
ORACLE_SERVICE=FREEPDB1
ORACLE_MONITORING_USER=nrmon
ORACLE_MONITORING_PASSWORD=MonTest123

VALIDATOR_INGEST_PATH=/shared/otel-metrics.json
VALIDATOR_INGEST_FORMAT=otlp-json
VALIDATOR_CONTAINER_MODE=pdb        # use 'cdb' if the receiver connects to the CDB root

# only needed for --check-ingest
NEW_RELIC_API_KEY=NRAK-...
NEW_RELIC_ACCOUNT_ID=1234567
NEW_RELIC_NERDGRAPH_URL=https://api.newrelic.com/graphql   # staging: https://staging-api.newrelic.com/graphql
```

**`pdb` vs `cdb`:** if the receiver's datasource points at a PDB (e.g.
`.../FREEPDB1`), keep `pdb`. If it connects to the CDB root (e.g. a `C##` common
user), set `cdb` so the validator uses the per-PDB `v$con_sysstat`/`CDB_*` queries
and matches the `oracle.db.pdb` attribute.

## Step 3 — Run

### Local (`run.sh`)

`run.sh` builds the binary with `go build` (needs Go ≥ 1.23 on PATH), then runs it:

```bash
./run.sh                  # one-shot DB check; exits non-zero on a MISMATCH
./run.sh --fail-only      # hide OK rows
./run.sh --check-ingest   # also run the NRDB delta check
./run.sh --watch          # service mode; re-checks every VALIDATOR_WATCH_INTERVAL
./run.sh --metric cpu     # filter to metrics whose name contains "cpu"
./run.sh --json           # machine-readable output
```

### Docker

```bash
# collector must write to ./shared/otel-metrics.json
docker compose up                       # build + run in --watch
docker compose run --rm validator       # one-shot
docker compose run --rm validator --check-ingest
```

Set `ORACLE_HOST=host.docker.internal` to reach a DB on the host, or attach the
service to your collector's compose network (see the bottom of
`docker-compose.yaml`). `run.sh --docker …` delegates to compose.

---

## Reading the output

### DB check

```
== DB check (collector vs database) ==
STATUS  METRIC               ATTRS               EXPECTED(DB)  ACTUAL(OTEL)  Δ
------  -------------------  ------------------  ------------  ------------  -----
OK      oracledb.executions  oracle.db.pdb=PDB2  1100          1100          0.00%

summary: OK=1
```

| status | meaning | action |
|---|---|---|
| `OK` | DB and collector agree within tolerance | — |
| `MISMATCH` | values disagree → **non-zero exit** | investigate the receiver mapping/transform |
| `MISSING_IN_INGEST` | DB has it, collector didn't emit it | usually the metric is disabled in the collector — fine |
| `MISSING_IN_DB` | collector emitted it, validator has no DB mapping | expected only for metrics not in `internal/metricmap` (e.g. a new receiver metric) |
| `SKIPPED` | declared in `ComputedSkip` | none in normal operation — reserved for future receiver-computed metrics |

### Ingest check (`--check-ingest`)

```
== Ingest check (collector vs NRDB via NRQL) ==
STATUS     METRIC               ATTRS               TYPE  EXPECTED  NRDB  Δ
---------  -------------------  ------------------  ----  --------  ----  -----
INGEST_OK  oracledb.executions  oracle.db.pdb=PDB2  Δ     100       100   0.00%
```

- `TYPE = Δ` → counter; `EXPECTED` is the emitted cumulative `last − first`, `NRDB`
  is `sum(metric)` over the window. Equal ⇒ delta conversion correct.
- `TYPE = latest` → gauge; `EXPECTED` is the emitted latest, `NRDB` is `latest(metric)`.

| status | meaning |
|---|---|
| `INGEST_OK` | NRDB matches expected within tolerance |
| `INGEST_MISMATCH` | bad delta/value → **non-zero exit** |
| `INGEST_NO_DATA` | NRQL returned nothing yet — ingest lag, or attr/window mismatch |
| `INGEST_ERROR` | NerdGraph/HTTP/auth error (check key, account, URL/region) |
| `INGEST_SKIPPED` | computed metric, unmapped, or <2 emitted points for a delta |

**Exit code** (one-shot): `0` clean · `1` any `MISMATCH`/`INGEST_MISMATCH` · `2` setup error.

---

## CI usage

The one-shot run is CI-friendly — non-zero exit on a real disagreement:

```bash
./run.sh --fail-only            # DB check only
./run.sh --check-ingest --fail-only   # + ingest, once NRDB has caught up
```

For ingest checks in CI, allow for ingest lag: let the collector run a few scrape
intervals, then validate a window that's already a minute or two in the past.

---

## Tuning & troubleshooting

| symptom | likely cause / fix |
|---|---|
| many `MISMATCH` on counters by a few % | normal scrape↔probe drift — raise `VALIDATOR_TOLERANCE_COUNTER` |
| `MISMATCH` on a gauge | tighten/loosen `VALIDATOR_TOLERANCE_GAUGE`; confirm it's truly a gauge in `metadata.yaml` |
| everything `MISSING_IN_INGEST` (DB has values, 0 series read) | the ingest file is empty/stale (collector not writing there) or its scope name doesn't contain `oracledbreceiver` — check the `[info] read N series` line |
| everything `MISSING_IN_DB` | wrong `VALIDATOR_CONTAINER_MODE` (pdb vs cdb) |
| `MISSING_IN_INGEST` for a metric you want | enable it in the collector's receiver config |
| `ORA-…` / login failures on connect | wrong creds or service; for a CDB root use the `C##` common user and `VALIDATOR_CONTAINER_MODE=cdb` |
| `INGEST_NO_DATA` | data hasn't landed yet (lag), or the NRQL window/attrs don't match — widen the window |
| `INGEST_ERROR: request failed` | check `NEW_RELIC_API_KEY`, `NEW_RELIC_ACCOUNT_ID`, and prod-vs-staging `NEW_RELIC_NERDGRAPH_URL` |
| ingest check all `INGEST_SKIPPED` | only `otlp-json` is supported; the file needs ≥2 scrapes for a delta |

Tolerances and the watch interval are all env vars — see §8 of
[`../specs/SPEC.md`](../specs/SPEC.md).

---

## Extending coverage

All of the receiver's current metrics are validated (`ComputedSkip` is empty). If
the receiver adds a new metric or query, mirror it in `internal/metricmap` — add
the SQL to `QuerySQL`/`AllQueryKeys` and an extractor, plus a unit test — and update
`specs/SPEC.md` in the same change (§2 and §9 explain the maintenance contract). If
a new metric is genuinely computed and not worth replicating, add it to
`ComputedSkip` so it surfaces as `SKIPPED` rather than `MISSING_IN_DB`.

---

## Verify your setup quickly

```bash
go test ./...          # unit tests, no DB/collector/NR needed
go build ./...         # confirms the build
```
