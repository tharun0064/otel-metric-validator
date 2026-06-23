"""Query New Relic (NerdGraph NRQL) to see what actually landed in NRDB.

Used by the ingest check to confirm NR's cumulative->delta conversion is correct.
HTTP uses only the standard library. The query builders and the response parser
are pure functions (unit-tested); the network call is a thin wrapper.
"""

from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass

from .config import Config

NANOS_PER_MS = 1_000_000


def to_ms(time_unix_nano: int) -> int:
    return int(time_unix_nano // NANOS_PER_MS)


def _escape(v: str) -> str:
    return str(v).replace("\\", "\\\\").replace("'", "\\'")


def _where(attrs: dict[str, str]) -> str:
    """Build a NRQL WHERE clause from attributes (drops empty values)."""
    parts = [f"{k} = '{_escape(v)}'" for k, v in sorted(attrs.items()) if v not in (None, "")]
    return " WHERE " + " AND ".join(parts) if parts else ""


def build_delta_query(metric: str, attrs: dict[str, str], since_ms: int, until_ms: int) -> str:
    """sum(metric) over (since, until] == total delta NR stored for the series."""
    return f"SELECT sum({metric}) FROM Metric{_where(attrs)} SINCE {since_ms} UNTIL {until_ms}"


def build_latest_query(metric: str, attrs: dict[str, str], since_ms: int, until_ms: int) -> str:
    return f"SELECT latest({metric}) FROM Metric{_where(attrs)} SINCE {since_ms} UNTIL {until_ms}"


def build_graphql(account_id: str, nrql: str) -> str:
    # NRQL goes inside a GraphQL double-quoted string; json.dumps later escapes it.
    return (
        "{ actor { account(id: " + str(account_id) + ") "
        '{ nrql(query: "' + nrql + '") { results } } } }'
    )


def parse_nrql_scalar(response: dict) -> tuple[float | None, str | None]:
    """Pull the single numeric value out of a NerdGraph NRQL response.

    Returns (value, error). value is None when there are no rows.
    """
    if response.get("errors"):
        msgs = "; ".join(e.get("message", str(e)) for e in response["errors"])
        return None, f"graphql errors: {msgs}"
    try:
        results = response["data"]["actor"]["account"]["nrql"]["results"]
    except (KeyError, TypeError):
        return None, "unexpected response shape"
    if not results:
        return None, None
    for v in results[0].values():
        if isinstance(v, (int, float)) and not isinstance(v, bool):
            return float(v), None
    return None, None  # row present but no numeric column (e.g. all nulls)


@dataclass
class NrqlClient:
    cfg: Config
    timeout: float = 30.0

    def run(self, nrql: str) -> tuple[float | None, str | None]:
        body = json.dumps({"query": build_graphql(self.cfg.nr_account_id, nrql)}).encode()
        req = urllib.request.Request(
            self.cfg.nr_nerdgraph_url,
            data=body,
            headers={"Content-Type": "application/json", "API-Key": self.cfg.nr_api_key},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                payload = json.loads(resp.read().decode())
        except Exception as exc:  # noqa: BLE001 - report network/HTTP failure, don't crash the run
            return None, f"request failed: {exc}"
        return parse_nrql_scalar(payload)
