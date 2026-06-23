"""CLI: one-shot validation (default) or --watch service.

Two checks:
  - DB check (always): emitted (collector) vs DB ground truth.
  - Ingest check (--check-ingest): emitted cumulative endpoints vs NRDB via NRQL,
    verifying NR's cumulative->delta conversion (and gauge passthrough).

Exit codes (one-shot): 0 = clean, 1 = a MISMATCH/INGEST_MISMATCH, 2 = setup error.
"""

from __future__ import annotations

import argparse
import sys
import time

from . import comparator, db_probe, ingest_check, ingest_reader, report
from .config import Config, ConfigError, load_config, load_dotenv


def _run_once(cfg: Config, args):
    emitted = ingest_reader.read_ingest(cfg.ingest_path, cfg.ingest_format)
    probe = db_probe.probe(cfg)
    for err in probe.errors:
        print(f"[db] {err}", file=sys.stderr)

    results = comparator.compare(probe.expected, emitted, cfg)
    if args.metric:
        results = [r for r in results if args.metric in r.metric]

    ic_results = None
    if args.check_ingest:
        if cfg.ingest_format != "otlp-json":
            print("[ingest] --check-ingest requires VALIDATOR_INGEST_FORMAT=otlp-json; skipping", file=sys.stderr)
        else:
            series = ingest_reader.read_otlp_series(cfg.ingest_path)
            ic_results = ingest_check.check_ingest(series, cfg)
            if args.metric:
                ic_results = [r for r in ic_results if args.metric in r.metric]

    return results, ic_results


def _emit(results, ic_results, args) -> None:
    if args.json:
        out = {"db_check": None, "ingest_check": None}
        import json as _json
        out["db_check"] = _json.loads(report.render_json(results))
        if ic_results is not None:
            out["ingest_check"] = _json.loads(report.render_ingest_json(ic_results))
        print(_json.dumps(out, indent=2))
        return

    print("== DB check (collector vs database) ==")
    print(report.render_table(results, show_ok=not args.fail_only))
    print()
    print("summary:", report.summary_line(comparator.summarize(results)))
    if ic_results is not None:
        print()
        print("== Ingest check (collector vs NRDB via NRQL) ==")
        print(report.render_ingest_table(ic_results, show_ok=not args.fail_only))
        print()
        print("summary:", report.summary_line(ingest_check.summarize(ic_results)))


def _failed(results, ic_results) -> bool:
    return comparator.has_failures(results) or (ic_results is not None and ingest_check.has_failures(ic_results))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="validator", description="Validate OTel oracle metrics against the DB and NRDB.")
    parser.add_argument("--env-file", default=".env", help="path to a .env file to preload (default: .env)")
    parser.add_argument("--watch", action="store_true", help="loop forever on the watch interval")
    parser.add_argument("--check-ingest", action="store_true", help="also verify NRDB ingest (delta) via NRQL")
    parser.add_argument("--json", action="store_true", help="machine-readable JSON output")
    parser.add_argument("--metric", help="only report metrics whose name contains this substring")
    parser.add_argument("--fail-only", action="store_true", help="hide OK rows in the tables")
    args = parser.parse_args(argv)

    load_dotenv(args.env_file)
    try:
        cfg = load_config()
        if args.check_ingest:
            cfg.require_nr()
    except ConfigError as exc:
        print(f"config error: {exc}", file=sys.stderr)
        return 2

    if not args.watch:
        try:
            results, ic_results = _run_once(cfg, args)
        except FileNotFoundError:
            print(f"ingest file not found: {cfg.ingest_path}", file=sys.stderr)
            return 2
        _emit(results, ic_results, args)
        return 1 if _failed(results, ic_results) else 0

    print(f"watching every {cfg.watch_interval:.0f}s (ctrl-c to stop)…", file=sys.stderr)
    try:
        while True:
            try:
                results, ic_results = _run_once(cfg, args)
                failed = _failed(results, ic_results)
                ts = time.strftime("%Y-%m-%d %H:%M:%S")
                db_sum = report.summary_line(comparator.summarize(results))
                ic_sum = "" if ic_results is None else "  ingest[" + report.summary_line(ingest_check.summarize(ic_results)) + "]"
                print(f"[{ts}] {'FAIL' if failed else 'pass'}  db[{db_sum}]{ic_sum}")
                if failed or args.json:
                    _emit(results, ic_results, args)
            except FileNotFoundError:
                print(f"ingest file not found yet: {cfg.ingest_path}", file=sys.stderr)
            time.sleep(cfg.watch_interval)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
