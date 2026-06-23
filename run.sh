#!/usr/bin/env bash
#
# One entrypoint to run the validator. Bootstraps a local venv on first run,
# then forwards all args to the CLI.
#
#   ./run.sh                 # one-shot; exits non-zero on a MISMATCH
#   ./run.sh --fail-only     # one-shot, hide OK rows
#   ./run.sh --watch         # run as a service (re-checks each interval)
#   ./run.sh --json          # machine-readable output
#   ./run.sh --docker ...    # run via docker compose instead of the local venv
#
# Reads ./\.env for ORACLE_* creds and VALIDATOR_* settings (copy .env.example).
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

# --- local venv -------------------------------------------------------------
PY=python3
VENV=.venv

if [[ ! -d "$VENV" ]]; then
  echo "[run.sh] creating virtualenv in $VENV …" >&2
  "$PY" -m venv "$VENV"
  "$VENV/bin/pip" install --quiet --upgrade pip
  "$VENV/bin/pip" install --quiet -r requirements.txt
fi

if [[ ! -f .env ]]; then
  echo "[run.sh] WARNING: no .env found — copy .env.example to .env and fill in creds." >&2
fi

exec "$VENV/bin/python" -m validator.cli "$@"
