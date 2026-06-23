#!/usr/bin/env bash
#
# One entrypoint to run the Go validator. Builds the binary, then runs it.
#
#   ./run.sh                 # one-shot; exits non-zero on a MISMATCH
#   ./run.sh --fail-only     # one-shot, hide OK rows
#   ./run.sh --watch         # run as a service (re-checks each interval)
#   ./run.sh --json          # machine-readable output
#   ./run.sh --check-ingest  # also verify NRDB ingest (delta) via NRQL
#   ./run.sh --docker ...    # run via docker compose instead
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

# --- local build + run ------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  echo "[run.sh] Go toolchain not found on PATH. Install Go 1.23+ or use ./run.sh --docker." >&2
  exit 2
fi

mkdir -p bin
go build -o bin/validator ./cmd/validator

if [[ ! -f .env ]]; then
  echo "[run.sh] WARNING: no .env found — copy .env.example to .env and fill in creds." >&2
fi

exec ./bin/validator "$@"
