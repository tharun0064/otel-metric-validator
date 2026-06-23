"""Render comparison results as a human table or JSON."""

from __future__ import annotations

import json

from .comparator import Result
from .comparator_status import MISMATCH, MISSING_IN_DB, MISSING_IN_INGEST, OK, SKIPPED

_ORDER = [MISMATCH, MISSING_IN_INGEST, OK, MISSING_IN_DB, SKIPPED]


def _attrs_str(attrs: dict) -> str:
    if not attrs:
        return ""
    return ",".join(f"{k}={v}" for k, v in sorted(attrs.items()))


def _fmt(v: float | None) -> str:
    if v is None:
        return "-"
    if v == int(v):
        return str(int(v))
    return f"{v:.4g}"


def render_table(results: list[Result], show_ok: bool = True) -> str:
    rows = [r for r in results if show_ok or r.status != OK]
    rows.sort(key=lambda r: (_ORDER.index(r.status) if r.status in _ORDER else 99, r.metric))

    headers = ("STATUS", "METRIC", "ATTRS", "EXPECTED(DB)", "ACTUAL(OTEL)", "Δ")
    lines = []
    for r in rows:
        delta = "" if r.rel_diff is None else (f"{r.rel_diff*100:.2f}%" if r.expected else f"{r.rel_diff:.4g}")
        lines.append((
            r.status, r.metric, _attrs_str(r.attrs),
            _fmt(r.expected), _fmt(r.actual), delta,
        ))

    widths = [len(h) for h in headers]
    for ln in lines:
        for i, cell in enumerate(ln):
            widths[i] = max(widths[i], len(cell))

    def fmt_row(cells):
        return "  ".join(c.ljust(widths[i]) for i, c in enumerate(cells))

    out = [fmt_row(headers), fmt_row(["-" * w for w in widths])]
    out += [fmt_row(ln) for ln in lines]
    return "\n".join(out)


_INGEST_ORDER = ["INGEST_MISMATCH", "INGEST_ERROR", "INGEST_NO_DATA", "INGEST_OK", "INGEST_SKIPPED"]


def render_ingest_table(results, show_ok: bool = True) -> str:
    rows = [r for r in results if show_ok or r.status != "INGEST_OK"]
    rows.sort(key=lambda r: (_INGEST_ORDER.index(r.status) if r.status in _INGEST_ORDER else 99, r.metric))

    headers = ("STATUS", "METRIC", "ATTRS", "TYPE", "EXPECTED", "NRDB", "Δ")
    lines = []
    for r in rows:
        kind = "Δ" if r.value_type == "sum" else ("latest" if r.value_type else "")
        delta = "" if r.rel_diff is None else (f"{r.rel_diff*100:.2f}%" if r.expected else f"{r.rel_diff:.4g}")
        lines.append((r.status, r.metric, _attrs_str(r.attrs), kind,
                      _fmt(r.expected), _fmt(r.actual), delta))

    widths = [len(h) for h in headers]
    for ln in lines:
        for i, cell in enumerate(ln):
            widths[i] = max(widths[i], len(cell))

    def fmt_row(cells):
        return "  ".join(c.ljust(widths[i]) for i, c in enumerate(cells))

    out = [fmt_row(headers), fmt_row(["-" * w for w in widths])]
    out += [fmt_row(ln) for ln in lines]
    return "\n".join(out)


def render_ingest_json(results) -> str:
    return json.dumps(
        [
            {
                "status": r.status, "metric": r.metric, "attrs": r.attrs,
                "expected": r.expected, "actual": r.actual, "rel_diff": r.rel_diff,
                "value_type": r.value_type, "nrql": r.nrql, "note": r.note,
            }
            for r in results
        ],
        indent=2,
    )


def render_json(results: list[Result]) -> str:
    return json.dumps(
        [
            {
                "status": r.status,
                "metric": r.metric,
                "attrs": r.attrs,
                "expected": r.expected,
                "actual": r.actual,
                "rel_diff": r.rel_diff,
                "value_type": r.value_type,
                "note": r.note,
            }
            for r in results
        ],
        indent=2,
    )


def summary_line(counts: dict[str, int]) -> str:
    parts = [f"{k}={v}" for k, v in sorted(counts.items())]
    return "  ".join(parts) if parts else "(no results)"
