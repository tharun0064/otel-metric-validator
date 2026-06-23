"""Join emitted (collector) vs expected (DB) data points and classify each."""

from __future__ import annotations

from dataclasses import dataclass

from .comparator_status import (  # noqa: F401  (re-export for convenience)
    OK, MISMATCH, MISSING_IN_INGEST, MISSING_IN_DB, SKIPPED,
)
from .config import Config
from .metric_map import COMPUTED_SKIP, SUM, Expected
from .ingest_reader import Emitted


@dataclass
class Result:
    metric: str
    attrs: dict
    status: str
    expected: float | None
    actual: float | None
    rel_diff: float | None
    value_type: str | None
    note: str = ""


def _within_tolerance(expected: float, actual: float, value_type: str, cfg: Config) -> tuple[bool, float]:
    diff = abs(actual - expected)
    if abs(expected) <= cfg.abs_epsilon:
        return diff <= cfg.abs_epsilon, diff
    rel = diff / abs(expected)
    tol = cfg.tol_counter if value_type == SUM else cfg.tol_gauge
    return rel <= tol, rel


def _attrs_of(key: tuple) -> dict:
    return {k: v for k, v in key[1]}


def compare(
    expected: dict[tuple, Expected],
    emitted: dict[tuple, Emitted],
    cfg: Config,
) -> list[Result]:
    results: list[Result] = []
    for key in sorted(set(expected) | set(emitted), key=lambda k: (k[0], sorted(k[1]))):
        metric = key[0]
        attrs = _attrs_of(key)
        exp = expected.get(key)
        emt = emitted.get(key)

        if exp is not None and emt is not None:
            ok, rel = _within_tolerance(exp.value, emt.value, exp.value_type, cfg)
            results.append(Result(
                metric, attrs, OK if ok else MISMATCH,
                exp.value, emt.value, rel, exp.value_type,
            ))
        elif exp is not None:
            # DB has it but the collector did not emit it (metric may be disabled).
            results.append(Result(
                metric, attrs, MISSING_IN_INGEST,
                exp.value, None, None, exp.value_type,
                note="present in DB, not in collector output (metric disabled?)",
            ))
        else:  # emitted only
            if metric in COMPUTED_SKIP:
                results.append(Result(
                    metric, attrs, SKIPPED, None, emt.value, None, None,
                    note="receiver-computed (v$sysmetric/osstat); not yet validated",
                ))
            else:
                results.append(Result(
                    metric, attrs, MISSING_IN_DB, None, emt.value, None, None,
                    note="no DB mapping for this metric/attrs",
                ))
    return results


def summarize(results: list[Result]) -> dict[str, int]:
    counts: dict[str, int] = {}
    for r in results:
        counts[r.status] = counts.get(r.status, 0) + 1
    return counts


def has_failures(results: list[Result]) -> bool:
    """A run fails only on a real value disagreement (MISMATCH)."""
    return any(r.status == MISMATCH for r in results)
