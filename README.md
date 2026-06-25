# otel-metric-validator

Cross-checks the Oracle metrics an OpenTelemetry collector **emits** (via the
`nroracledbreceiver` fork) against **ground truth** obtained by running the
receiver's *own* monitoring SQL directly against the database — and flags any
disagreement.

```
 collector  ──file exporter──▶  otel-metrics.json ─┐
 (nroracledbreceiver)                              ├─▶ validator ─▶ report (OK / MISMATCH / …)
 Oracle DB  ──receiver's SQL (run directly)────────┘
```

It catches receiver regressions in query, column mapping, or unit transforms
(e.g. `cpu_time` must be divided by 100 — "tens of ms" → seconds).

A standalone **Go** module. It connects to Oracle with the **same driver the
receiver uses** (`github.com/sijms/go-ora/v2`, pure Go), so it negotiates Oracle
Native Network Encryption with no Oracle Instant Client and no "thick mode".

> **New here?** Start with the step-by-step guide:
> [`docs/USAGE.md`](docs/USAGE.md). For how the three-way comparison and the delta
> math work (with a worked example, SQL, and NRQL), see
> [`docs/COMPARISON.md`](docs/COMPARISON.md). The formal mapping & semantics live in
> [`specs/SPEC.md`](specs/SPEC.md).

## How it works

1. **Ingest** — reads the value the collector emitted, from either:
   - `otlp-json`: newline-delimited OTLP JSON from a `file` exporter (**recommended**), or
   - `debug-log`: text from the `debug`/`logging` exporter.
2. **Probe** — connects to Oracle (go-ora) and runs the same queries the receiver
   uses (`v$sysstat`/`v$con_sysstat`, `v$resource_limit`, `v$session`, `v$sgainfo`,
   tablespace views, …), applying the receiver's transforms. The SQL strings are
   copied verbatim from the receiver's `scraper.go`.
3. **Compare** — joins on `(metric, attributes)` and checks each value within a
   tolerance (separate defaults for cumulative counters vs gauges).

## Setup

```bash
cd otel-metric-validator
cp .env.example .env          # fill in ORACLE_* creds + VALIDATOR_INGEST_PATH
```

Enable a file exporter on your collector — see
[`collector/file-exporter.partial.yaml`](collector/file-exporter.partial.yaml) —
and point `VALIDATOR_INGEST_PATH` at the JSON file it writes.

## Run — `./run.sh`

`run.sh` is the single entrypoint. It builds the binary (`go build`, needs the Go
toolchain ≥ 1.23) and runs it:

```bash
./run.sh                 # one-shot; prints a table, exits non-zero on any MISMATCH
./run.sh --fail-only     # one-shot, hide OK rows
./run.sh --watch         # run as a service (re-checks each VALIDATOR_WATCH_INTERVAL)
./run.sh --json          # machine-readable output
./run.sh --metric cpu    # filter to metrics whose name contains "cpu"
./run.sh --check-ingest  # ALSO verify NRDB ingest (delta) via NRQL (needs NR creds)
```

### `--check-ingest` — verify NR's delta conversion

NR delta-converts cumulative counters at ingest. With `--check-ingest` the
validator adds a second section that reads the emitted *cumulative* endpoints and
asks NRDB (NerdGraph NRQL) for the corresponding aggregate:

- **counters:** `sum(metric)` over the window must equal `last − first` emitted
  (telescoping) → confirms the stored deltas are correct;
- **gauges:** `latest(metric)` must equal the emitted latest.

Needs `NEW_RELIC_API_KEY` + `NEW_RELIC_ACCOUNT_ID` (and `otlp-json` format). See
§11 of [`specs/SPEC.md`](specs/SPEC.md).

(Equivalent without the wrapper: `go run ./cmd/validator [...]`.)

## Run — Docker

```bash
# collector's file exporter must write to ./shared/otel-metrics.json
docker compose up                       # build + run in --watch mode
docker compose run --rm validator       # one-shot
docker compose run --rm validator --json
```

The image is a multi-stage build producing a static binary on `distroless/static`.
`run.sh` can also delegate to Docker: `./run.sh --docker --watch` /
`./run.sh --docker --fail-only`. Set `ORACLE_HOST=host.docker.internal` in `.env`
to reach a DB on the host, or attach the service to your collector's compose
network (see the comment at the bottom of `docker-compose.yaml`).

### Statuses

| status | meaning |
|---|---|
| `OK` | DB and collector agree within tolerance |
| `MISMATCH` | values disagree beyond tolerance → **non-zero exit** |
| `MISSING_IN_INGEST` | DB reports it, collector didn't emit it (metric disabled?) — warning |
| `MISSING_IN_DB` | collector emitted it, validator has no DB mapping — warning |
| `SKIPPED` | receiver-computed metric (v$sysmetric/osstat utilization & ratios) — not yet validated |

## Coverage

All of the receiver's directly-derivable metrics are validated — the validator
runs the same ten monitoring queries the receiver does:

- **Counters & direct gauges:** executions, parses, reads/writes, gets, commits,
  rollbacks, logons, deadlocks, `cpu_time` (÷100), `pga_memory`, physical/SQL\*Net
  I/O, sessions/processes/transactions/locks usage & limits, tablespace, SGA,
  data-dictionary hit ratio, storage usage.
- **`v$sysmetric` gauges:** the `*.utilization` / ratio metrics
  (`buffer_cache`, `host.cpu`, `database.cpu`, `library_cache`, `shared_pool`,
  `database.wait`, `parse`, `execution`, `redo_allocation`), `sort.ratio`,
  `parse.rate`, and `sql_service.response.duration` (÷100). Oracle computes these
  inside the view; the validator reads the same value.
- **`v$osstat` / misc:** `system.cpu.physical.count`, `system.memory.limit`,
  `oracledb.system.cpu.load`, `oracledb.recycle_bin.limit`, `storage.utilization`
  (= used/allocated).

`v$sysmetric` values are **60-second-window snapshots**, so these gauges can drift
more between the collector scrape and the validator probe than the counters do —
the gauge tolerance absorbs it, or compare under `--watch` across cycles. Nothing
is reported `SKIPPED` in normal operation; the status remains available for any
future receiver-computed metric.

## Note on timing

The collector scrapes on its own interval; the validator probes the DB a moment
later. Cumulative counters advance in between, so the counter tolerance
(`VALIDATOR_TOLERANCE_COUNTER`, default 5%) absorbs reasonable drift. This skew is
inherent and reported transparently — tighten/loosen tolerances via env vars.

## Tests

```bash
go test ./...         # no DB or collector required (pure unit tests)
```

## Specification

[`specs/SPEC.md`](specs/SPEC.md) is the authoritative spec this framework
implements — the full query→metric→attribute→transform mapping, comparison
semantics, status/exit codes, the config contract, and a package↔spec
cross-reference. Read it to audit coverage or extend the map.

## Source of truth

The mapping in `internal/metricmap` mirrors
`opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go` (see
§2 of [`specs/SPEC.md`](specs/SPEC.md) for exact file/line references); the SQL
strings are copied verbatim from it. If that scraper's SQL/mapping/transforms
change, update `specs/SPEC.md` and `internal/metricmap` together.
