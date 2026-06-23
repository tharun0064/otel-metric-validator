"""Environment-driven configuration for the validator.

Reads from the process environment. Optionally pre-loads a `.env` file (simple
KEY=VALUE format, no interpolation) without overriding values already set in the
real environment.
"""

from __future__ import annotations

import os
import re
from dataclasses import dataclass
from pathlib import Path

# An inline comment starts at the first '#' that follows whitespace, matching the
# usual dotenv convention. A '#' with no preceding space is kept as part of the
# value (e.g. passwords ending in '##', or a user like 'C##DB_MONITOR').
_INLINE_COMMENT = re.compile(r"\s#")


def load_dotenv(path: str | os.PathLike[str]) -> None:
    """Minimal .env loader: KEY=VALUE per line, '#' comments, quotes stripped.

    - Full-line comments (line starts with '#') are ignored.
    - For unquoted values, a trailing inline comment (whitespace + '#') is
      stripped; a '#' not preceded by whitespace stays in the value.
    - Quoted values are taken verbatim between the quotes (no comment stripping).

    Existing environment variables take precedence (we never overwrite them),
    so an explicit `export FOO=bar` always wins over the file.
    """
    p = Path(path)
    if not p.is_file():
        return
    for raw in p.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip()
        if value[:1] in ('"', "'"):
            quote = value[0]
            end = value.find(quote, 1)
            value = value[1:end] if end != -1 else value[1:]
        else:
            m = _INLINE_COMMENT.search(value)
            if m:
                value = value[: m.start()]
            value = value.strip()
        if key and key not in os.environ:
            os.environ[key] = value


class ConfigError(ValueError):
    """Raised when required configuration is missing or invalid."""


@dataclass(frozen=True)
class Config:
    # Oracle connection
    host: str
    port: int
    service: str
    user: str
    password: str

    # Ingest source
    ingest_path: str
    ingest_format: str  # "otlp-json" | "debug-log"

    # Behaviour
    container_mode: str  # "pdb" | "cdb"
    tol_gauge: float
    tol_counter: float
    abs_epsilon: float
    watch_interval: float

    # New Relic NRQL ingest check (optional; required only with --check-ingest)
    nr_api_key: str
    nr_account_id: str
    nr_nerdgraph_url: str

    # Oracle thick mode — required when the DB server enforces Native Network
    # Encryption (NNE), which python-oracledb thin mode does not support.
    oracle_thick: bool = False
    oracle_client_lib_dir: str = ""  # optional Instant Client dir for init_oracle_client

    @property
    def dsn(self) -> str:
        return f"{self.host}:{self.port}/{self.service}"

    @property
    def is_cdb(self) -> bool:
        return self.container_mode == "cdb"

    def require_nr(self) -> None:
        """Validate NR settings; call only when the ingest check is requested."""
        missing = []
        if not self.nr_api_key:
            missing.append("NEW_RELIC_API_KEY")
        if not self.nr_account_id:
            missing.append("NEW_RELIC_ACCOUNT_ID")
        if missing:
            raise ConfigError(
                "--check-ingest needs: " + ", ".join(missing) + "  (see .env.example)"
            )


_REQUIRED = ("ORACLE_HOST", "ORACLE_SERVICE", "ORACLE_MONITORING_USER", "ORACLE_MONITORING_PASSWORD")
_VALID_FORMATS = ("otlp-json", "debug-log")
_VALID_MODES = ("pdb", "cdb")


def load_config(env: dict[str, str] | None = None) -> Config:
    """Build a Config from `env` (defaults to os.environ). Fail fast on errors."""
    e = os.environ if env is None else env

    missing = [k for k in _REQUIRED if not e.get(k)]
    if missing:
        raise ConfigError(
            "missing required environment variables: " + ", ".join(missing)
            + "  (see .env.example)"
        )

    fmt = e.get("VALIDATOR_INGEST_FORMAT", "otlp-json").strip().lower()
    if fmt not in _VALID_FORMATS:
        raise ConfigError(f"VALIDATOR_INGEST_FORMAT must be one of {_VALID_FORMATS}, got {fmt!r}")

    mode = e.get("VALIDATOR_CONTAINER_MODE", "pdb").strip().lower()
    if mode not in _VALID_MODES:
        raise ConfigError(f"VALIDATOR_CONTAINER_MODE must be one of {_VALID_MODES}, got {mode!r}")

    ingest_path = e.get("VALIDATOR_INGEST_PATH", "").strip()
    if not ingest_path:
        raise ConfigError("VALIDATOR_INGEST_PATH is required (path to the collector's metric output)")

    def _float(key: str, default: float) -> float:
        try:
            return float(e.get(key, default))
        except (TypeError, ValueError):
            raise ConfigError(f"{key} must be a number, got {e.get(key)!r}")

    def _int(key: str, default: int) -> int:
        try:
            return int(e.get(key, default))
        except (TypeError, ValueError):
            raise ConfigError(f"{key} must be an integer, got {e.get(key)!r}")

    lib_dir = e.get("ORACLE_CLIENT_LIB_DIR", "").strip()
    thick = e.get("VALIDATOR_ORACLE_THICK", "").strip().lower() in ("1", "true", "yes", "on")
    thick = thick or bool(lib_dir)  # specifying a client lib dir implies thick mode

    return Config(
        host=e["ORACLE_HOST"].strip(),
        port=_int("ORACLE_PORT", 1521),
        service=e["ORACLE_SERVICE"].strip(),
        user=e["ORACLE_MONITORING_USER"].strip(),
        password=e["ORACLE_MONITORING_PASSWORD"],
        ingest_path=ingest_path,
        ingest_format=fmt,
        container_mode=mode,
        tol_gauge=_float("VALIDATOR_TOLERANCE_GAUGE", 0.02),
        tol_counter=_float("VALIDATOR_TOLERANCE_COUNTER", 0.05),
        abs_epsilon=_float("VALIDATOR_ABS_EPSILON", 1.0),
        watch_interval=_float("VALIDATOR_WATCH_INTERVAL", 30.0),
        nr_api_key=e.get("NEW_RELIC_API_KEY", "").strip(),
        nr_account_id=e.get("NEW_RELIC_ACCOUNT_ID", "").strip(),
        nr_nerdgraph_url=e.get("NEW_RELIC_NERDGRAPH_URL", "https://api.newrelic.com/graphql").strip(),
        oracle_thick=thick,
        oracle_client_lib_dir=lib_dir,
    )
