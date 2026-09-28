#!/usr/bin/env bash
# Start a disposable local PostgreSQL cluster without Docker or sudo.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -n "${LUNARA_TEST_DATABASE_URL:-}" ]]; then
  exec go test -race ./...
fi
for executable in initdb pg_ctl go; do
  command -v "$executable" >/dev/null || { echo "Missing executable: $executable" >&2; exit 1; }
done
mkdir -p .local
lunara_test_dir="$(mktemp -d "$PWD/.local/pgtest.XXXXXX")"
cleanup() {
  pg_ctl -D "$lunara_test_dir/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf -- "$lunara_test_dir"
}
trap cleanup EXIT
initdb -D "$lunara_test_dir/data" -U lunara_test -A trust --no-locale -E UTF8 >"$lunara_test_dir/initdb.log"
# Unique private socket directory; no TCP listener or system cluster changes.
pg_ctl -D "$lunara_test_dir/data" -l "$lunara_test_dir/postgres.log" -o "-k $lunara_test_dir -p 55439 -c listen_addresses='' -c unix_socket_permissions=0700" -w start
export LUNARA_TEST_DATABASE_URL="host=$lunara_test_dir port=55439 user=lunara_test dbname=postgres sslmode=disable"
go test -race ./...
