#!/usr/bin/env bash
set -euo pipefail
if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: query-csv.sh CSV_DIRECTORY [SQL_FILE]" >&2
  exit 2
fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
csv_dir=$1
sql_file=${2:-$script_dir/queries.sql}
if [[ ! -d "$csv_dir" || ! -r "$sql_file" ]]; then
  echo "CSV directory or SQL file is missing" >&2
  exit 2
fi
sql_file=$(realpath -- "$sql_file")
views_file=$script_dir/views.sql
shopt -s nullglob
segments=("$csv_dir"/*.csv)
if [[ ${#segments[@]} -eq 0 ]]; then
  echo "CSV directory contains no finalized segments" >&2
  exit 2
fi
if [[ $(duckdb --version) != v2.0.0\ * ]]; then
  echo "DuckDB v2.0.0 required. Build Docker target dev-observability." >&2
  exit 2
fi
cd -- "$csv_dir"
# views.sql defines the *_v1 views, so SQL_FILE (the report by default) can use them.
cat -- "$views_file" "$sql_file" | duckdb -bail -init /dev/null :memory:
