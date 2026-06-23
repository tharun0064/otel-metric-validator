"""Verify NR's ingest pipeline stored the correct values (esp. counter deltas).

The collector emits *cumulative* counters; New Relic delta-converts them at ingest.
By the telescoping property, sum(metric) over (t0, tN] in NRDB must equal the
emitted cumulative end-to-end difference  C(tN) - C(t0).  Gauges are stored as-is,
so latest(metric) must equal the emitted latest.

We therefore read the emitted series (first/last point) from the OTLP-JSON file,
ask NRDB for the corresponding aggregate, and compare.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import nrql_probe
from .comparator import _within_tolerance
from .comparator_status import (
    INGEST_ERROR, INGEST_MISMATCH, INGEST_NO_DATA, INGEST_OK, INGEST_SKIPPED,
)
from .config import Config
from .ingest_reader import Series
from .metric_map import COMPUTED_SKIP, GAUGE, SUM, value_type_of


@dataclass
class IngestResult:
    metric: str
    attrs: dict
    status: str
    expected: float | None   # from emitted OTLP (delta for sums, latest for gauges)
    actual: float | None     # from NRDB via NRQL
    rel_diff: float | None
    value_type: str | None
    nrql: str = ""
    note: str = ""


def _check_one(series: Series, cfg: Config, client: nrql_probe.NrqlClient) -> IngestResult:
    metric, attrs = series.metric, series.attrs
    vt = value_type_of(metric)

    if metric in COMPUTED_SKIP or vt is None:
        return IngestResult(metric, attrs, INGEST_SKIPPED, None, None, None, vt,
                            note="not a directly-mapped Phase-1 metric")

    since_ms = nrql_probe.to_ms(series.first_ts)
    until_ms = nrql_probe.to_ms(series.last_ts)

    if vt == SUM:
        # Need at least two distinct points spanning time to form a delta.
        if series.n_points < 2 or until_ms <= since_ms:
            return IngestResult(metric, attrs, INGEST_SKIPPED, None, None, None, vt,
                                note="need >=2 emitted points across time for a delta")
        expected = series.last_value - series.first_value
        # Exclude the first point's own delta (it belongs to the prior interval).
        nrql = nrql_probe.build_delta_query(metric, attrs, since_ms + 1, until_ms)
    else:  # gauge
        expected = series.last_value
        nrql = nrql_probe.build_latest_query(metric, attrs, since_ms, until_ms + 1000)

    actual, err = client.run(nrql)
    if err is not None:
        return IngestResult(metric, attrs, INGEST_ERROR, expected, None, None, vt, nrql=nrql, note=err)
    if actual is None:
        return IngestResult(metric, attrs, INGEST_NO_DATA, expected, None, None, vt, nrql=nrql,
                            note="NRQL returned no data (ingest lag, or attrs/window mismatch)")

    ok, rel = _within_tolerance(expected, actual, vt, cfg)
    return IngestResult(metric, attrs, INGEST_OK if ok else INGEST_MISMATCH,
                        expected, actual, rel, vt, nrql=nrql)


def check_ingest(series_map: dict[tuple, Series], cfg: Config) -> list[IngestResult]:
    client = nrql_probe.NrqlClient(cfg)
    results = [_check_one(s, cfg, client) for s in series_map.values()]
    results.sort(key=lambda r: (r.status, r.metric))
    return results


def summarize(results: list[IngestResult]) -> dict[str, int]:
    counts: dict[str, int] = {}
    for r in results:
        counts[r.status] = counts.get(r.status, 0) + 1
    return counts


def has_failures(results: list[IngestResult]) -> bool:
    return any(r.status == INGEST_MISMATCH for r in results)
