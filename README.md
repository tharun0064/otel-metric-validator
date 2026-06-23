# otel-metric-validator

Cross-checks the Oracle metrics an OpenTelemetry collector **ingests** (via the
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

> **New here?** Start with the step-by-step guide:
> [`docs/USAGE.md`](docs/USAGE.md). The formal mapping & semantics live in
> [`specs/SPEC.md`](specs/SPEC.md).

## How it works

1. **Ingest** — reads the value the collector emitted, from either:
   - `otlp-json`: newline-delimited OTLP JSON from a `file` exporter (**recommended**), or
   - `debug-log`: text from the `debug`/`logging` exporter.
2. **Probe** — connects to Oracle (python-oracledb, thin mode) and runs the same
   queries the receiver uses (`v$sysstat`/`v$con_sysstat`, `v$resource_limit`,
   `v$session`, `v$sgainfo`, tablespace views, …), applying the receiver's transforms.
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

`run.sh` is the single entrypoint. On first run it installs any missing deps
(`requirements.txt`) into the current Python environment, then forwards all
arguments to the CLI (override the interpreter with `$PYTHON`):

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

(Equivalent without the wrapper: `python -m validator.cli [...]`.)

## Run — Docker

```bash
# collector's file exporter must write to ./shared/otel-metrics.json
docker compose up                       # build + run in --watch mode
docker compose run --rm validator       # one-shot
docker compose run --rm validator --json
```

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

- **Validated (Phase 1):** the cumulative counters and direct gauges that map
  one-to-one from a query row — executions, parses, reads/writes, gets, commits,
  rollbacks, logons, deadlocks, `cpu_time` (÷100), `pga_memory`, physical/SQL\*Net
  I/O, sessions/processes/transactions/locks usage & limits, tablespace, SGA,
  data-dictionary hit ratio, storage usage.
- **Skipped (Phase 2):** the `v$sysmetric`/`v$osstat`-derived utilization & ratio
  metrics the receiver *computes* (e.g. `*.utilization`, `parse.rate`,
  `sql_service.response.duration`). They appear as `SKIPPED` so coverage gaps are
  explicit; validating them means replicating the receiver's arithmetic and can be
  added incrementally in `validator/metric_map.py`.

## Note on timing

The collector scrapes on its own interval; the validator probes the DB a moment
later. Cumulative counters advance in between, so the counter tolerance
(`VALIDATOR_TOLERANCE_COUNTER`, default 5%) absorbs reasonable drift. This skew is
inherent and reported transparently — tighten/loosen tolerances via env vars.

## Tests

```bash
pytest                # no DB or collector required (pure unit tests)
```

## Specification

[`specs/SPEC.md`](specs/SPEC.md) is the authoritative spec this framework
implements — the full query→metric→attribute→transform mapping, comparison
semantics, status/exit codes, the config contract, and a module↔spec
cross-reference. Read it to audit coverage or extend the map.

## Source of truth

The mapping in `validator/metric_map.py` mirrors
`opentelemetry-collector-contrib/receiver/nroracledbreceiver/scraper.go` (see
§2 of [`specs/SPEC.md`](specs/SPEC.md) for exact file/line references). If that
scraper's SQL/mapping/transforms change, update `specs/SPEC.md` and
`metric_map.py` together.
