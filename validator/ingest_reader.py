"""Read what the collector ingested/emitted for the oracle receiver.

Two input formats:
  - otlp-json : newline-delimited OTLP/JSON written by a `file` exporter (robust).
  - debug-log : text produced by the `debug`/`logging` exporter (best-effort).

Both return the latest emitted data point per (metric, attributes), keyed the same
way metric_map keys its Expected values so the comparator can join them directly.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass

from .metric_map import SCOPE_NAME, _norm_attrs


@dataclass(frozen=True)
class Emitted:
    metric: str
    attrs: dict[str, str]
    value: float
    time_unix_nano: int

    def key(self) -> tuple:
        return (self.metric, _norm_attrs(self.attrs))


@dataclass
class Series:
    """First and last emitted points for a (metric, attrs) series across the file.

    For cumulative counters, (last_value - first_value) is the total delta NR
    should have stored over (first_ts, last_ts].
    """
    metric: str
    attrs: dict[str, str]
    first_value: float
    first_ts: int
    last_value: float
    last_ts: int
    n_points: int

    def key(self) -> tuple:
        return (self.metric, _norm_attrs(self.attrs))


# ---------------------------------------------------------------------------
# OTLP/JSON
# ---------------------------------------------------------------------------
def _attr_value(v: dict) -> str:
    for k in ("stringValue", "intValue", "doubleValue", "boolValue"):
        if k in v:
            return str(v[k])
    return ""


def _dp_value(dp: dict) -> float | None:
    if "asDouble" in dp:
        try:
            return float(dp["asDouble"])
        except (TypeError, ValueError):
            return None
    if "asInt" in dp:
        try:
            return float(int(dp["asInt"]))
        except (TypeError, ValueError):
            return None
    return None


def _iter_otlp_points(obj: dict):
    for rm in obj.get("resourceMetrics", []):
        for sm in rm.get("scopeMetrics", []):
            scope_name = (sm.get("scope") or {}).get("name", "")
            if scope_name and SCOPE_NAME not in scope_name:
                continue
            for metric in sm.get("metrics", []):
                name = metric.get("name")
                if not name:
                    continue
                body = metric.get("gauge") or metric.get("sum") or {}
                for dp in body.get("dataPoints", []):
                    value = _dp_value(dp)
                    if value is None:
                        continue
                    attrs = {a["key"]: _attr_value(a.get("value", {})) for a in dp.get("attributes", [])}
                    ts = int(dp.get("timeUnixNano", 0) or 0)
                    yield Emitted(name, attrs, value, ts)


def read_otlp_json(path: str) -> dict[tuple, Emitted]:
    latest: dict[tuple, Emitted] = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            for pt in _iter_otlp_points(obj):
                k = pt.key()
                cur = latest.get(k)
                if cur is None or pt.time_unix_nano >= cur.time_unix_nano:
                    latest[k] = pt
    return latest


# ---------------------------------------------------------------------------
# debug / logging exporter (detailed text) — best effort
# ---------------------------------------------------------------------------
_NAME_RE = re.compile(r"->\s*Name:\s*(\S+)")
_ATTR_RE = re.compile(r"->\s*([\w.]+):\s*\w+\(([^)]*)\)")  # e.g. -> oracle.db.pdb: Str(PDB2)
_VALUE_RE = re.compile(r"Value:\s*([-\d.eE+]+)")


def read_debug_log(path: str) -> dict[tuple, Emitted]:
    latest: dict[tuple, Emitted] = {}
    cur_metric: str | None = None
    cur_attrs: dict[str, str] = {}
    in_attrs = False

    def flush(value: float):
        if cur_metric is None:
            return
        pt = Emitted(cur_metric, dict(cur_attrs), value, 0)
        latest[pt.key()] = pt  # later lines win (file is chronological)

    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            m = _NAME_RE.search(line)
            if m:
                cur_metric = m.group(1)
                cur_attrs = {}
                in_attrs = False
                continue
            if "Data point attributes:" in line:
                cur_attrs = {}
                in_attrs = True
                continue
            if in_attrs:
                am = _ATTR_RE.search(line)
                if am:
                    cur_attrs[am.group(1)] = am.group(2)
                    continue
                in_attrs = False  # attributes block ended
            vm = _VALUE_RE.search(line)
            if vm:
                try:
                    flush(float(vm.group(1)))
                except ValueError:
                    pass
                cur_attrs = {}
    return latest


def read_otlp_series(path: str) -> dict[tuple, Series]:
    """Collapse all OTLP-JSON points into per-series first/last endpoints.

    Only meaningful for the file exporter (which appends a line per scrape).
    """
    series: dict[tuple, Series] = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            for pt in _iter_otlp_points(obj):
                k = pt.key()
                s = series.get(k)
                if s is None:
                    series[k] = Series(pt.metric, dict(pt.attrs), pt.value, pt.time_unix_nano,
                                       pt.value, pt.time_unix_nano, 1)
                    continue
                s.n_points += 1
                if pt.time_unix_nano <= s.first_ts:
                    s.first_value, s.first_ts = pt.value, pt.time_unix_nano
                if pt.time_unix_nano >= s.last_ts:
                    s.last_value, s.last_ts = pt.value, pt.time_unix_nano
    return series


def read_ingest(path: str, fmt: str) -> dict[tuple, Emitted]:
    if fmt == "otlp-json":
        return read_otlp_json(path)
    if fmt == "debug-log":
        return read_debug_log(path)
    raise ValueError(f"unknown ingest format: {fmt!r}")
