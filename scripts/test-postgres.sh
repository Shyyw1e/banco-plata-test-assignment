#!/bin/sh
# Own one disposable Compose project. Never read .env or reuse a developer DB.
set -eu
mode=${1:-integration}
case "$mode" in integration|e2e) ;; *) echo 'usage: test-postgres.sh integration|e2e' >&2; exit 2 ;; esac
cd "$(dirname "$0")/.."
GO=${GO:-go}
project="quotes-test-${mode}-$(date +%s)-$$"
# Docker chooses a free host port; concurrent tests need not share a fixed port.
export POSTGRES_PORT=0 POSTGRES_DB=quotes POSTGRES_USER=quotes POSTGRES_PASSWORD=quotes_local
compose() { docker compose --env-file .env.example -p "$project" "$@"; }
cleanup() {
    status=$?
    trap - EXIT
    if ! compose down -v; then status=1; fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose up -d --wait postgres
endpoint=$(compose port postgres 5432)
port=${endpoint##*:}
export TEST_DATABASE_URL="postgres://quotes:quotes_local@127.0.0.1:${port}/quotes?sslmode=disable"
if [ "$mode" = integration ]; then
    compose run --rm migrate up
    "$GO" test -tags=integration -race ./... -count=1 -timeout=90s
else
    "$GO" test -tags=e2e -race ./tests/e2e -count=1 -timeout=120s
fi
