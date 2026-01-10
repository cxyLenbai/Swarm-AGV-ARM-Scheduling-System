#!/usr/bin/env bash
set -euo pipefail

DATABASE_URL="${DATABASE_URL:-}"
if [[ -z "${DATABASE_URL}" ]]; then
  echo "DATABASE_URL is required" >&2
  exit 1
fi

if ! command -v psql >/dev/null 2>&1; then
  echo "psql is required on PATH" >&2
  exit 1
fi

MIGRATIONS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../migrations" && pwd)"
shopt -s nullglob
files=("${MIGRATIONS_DIR}"/*.up.sql)
if [[ ${#files[@]} -eq 0 ]]; then
  echo "no migrations found" >&2
  exit 1
fi

for file in "${files[@]}"; do
  echo "Applying $(basename "${file}")"
  psql "${DATABASE_URL}" -v ON_ERROR_STOP=1 -f "${file}"
done
