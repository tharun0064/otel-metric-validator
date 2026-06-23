#!/usr/bin/env bash
#
# One entrypoint to run the validator. Installs any missing Python deps into the
# current environment, then forwards all args to the CLI.
#
#   ./run.sh                 # one-shot; exits non-zero on a MISMATCH
#   ./run.sh --fail-only     # one-shot, hide OK rows
#   ./run.sh --watch         # run as a service (re-checks each interval)
#   ./run.sh --json          # machine-readable output
#   ./run.sh --docker ...    # run via docker compose instead
#
# Override the interpreter with $PYTHON. Reads ./\.env for ORACLE_* creds and
# VALIDATOR_* settings (copy .env.example).
set -euo pipefail
cd "$(dirname "$0")"

# --- docker passthrough -----------------------------------------------------
if [[ "${1:-}" == "--docker" ]]; then
  shift
  if [[ "${1:-}" == "--watch" ]]; then
    exec docker compose up --build
  fi
  exec docker compose run --rm validator "$@"
fi

# --- deps -------------------------------------------------------------------
PY="${PYTHON:-python3}"

if ! "$PY" -c "import oracledb, yaml" 2>/dev/null; then
  echo "[run.sh] installing dependencies …" >&2
  "$PY" -m pip install --quiet --disable-pip-version-check -r requirements.txt
fi

if [[ ! -f .env ]]; then
  echo "[run.sh] WARNING: no .env found — copy .env.example to .env and fill in creds." >&2
fi

exec "$PY" -m validator.cli "$@"
