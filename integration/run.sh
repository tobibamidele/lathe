#!/usr/bin/env bash
# End-to-end test of the whole toolchain against a real database.
#
#   ./run.sh sqlite
#   ./run.sh postgres      (needs DATABASE_URL_POSTGRES or the default below)
#   ./run.sh mysql         (needs DATABASE_URL_MYSQL or the default below)
#
# It generates code, writes and applies migrations, runs the CRUD tests, then
# evolves the schema over existing data, verifies the data survived, reverts
# the migration, verifies the old shape, and applies it again.
set -euo pipefail
cd "$(dirname "$0")"

D="${1:?usage: run.sh sqlite|postgres|mysql}"
LATHE=../bin/lathe
(cd .. && go build -o bin/lathe ./cmd/lathe)

case "$D" in
  sqlite)
    export DATABASE_URL="${DATABASE_URL_SQLITE:-/tmp/lathe_e2e.db}"
    reset_db() { rm -f "$DATABASE_URL" "$DATABASE_URL-wal" "$DATABASE_URL-shm"; }
    ;;
  postgres)
    export DATABASE_URL="${DATABASE_URL_POSTGRES:-postgres://lathe:lathe@127.0.0.1:5432/lathe_test?sslmode=disable}"
    reset_db() {
      PGPASSWORD="${PGPASSWORD:-lathe}" psql -q -h "${PGHOST:-127.0.0.1}" -U "${PGUSER:-lathe}" -d postgres \
        -c "DROP DATABASE IF EXISTS lathe_test" -c "CREATE DATABASE lathe_test"
    }
    ;;
  mysql)
    export DATABASE_URL="${DATABASE_URL_MYSQL:-lathe:lathe@tcp(127.0.0.1:3306)/lathe_test}"
    reset_db() {
      mysql -u"${MYSQL_ADMIN_USER:-lathe}" -p"${MYSQL_ADMIN_PASSWORD:-lathe}" -h"${MYSQL_HOST:-127.0.0.1}" \
        -e "DROP DATABASE IF EXISTS lathe_test; CREATE DATABASE lathe_test" 2>/dev/null
    }
    ;;
  *) echo "unknown dialect $D" >&2; exit 2 ;;
esac
export LATHE_DIALECT="$D"

step() { printf '\n\033[1m== [%s] %s\033[0m\n' "$D" "$*"; }
gotest() {
  # -v output is filtered to one line per test so skips cannot hide
  go test -count=1 -v ${LATHE_TEST_FLAGS:-} "$@" ./... | grep -E '^(--- |ok |FAIL|panic|\s+.*_test.go)'
}

reset_db
rm -rf "migrations/$D" db
unset LATHE_STAGE LATHE_SEED LATHE_AFTER_ROLLBACK || true

step "stage 1: generate, diff, apply"
$LATHE generate
$LATHE migrate diff init
$LATHE migrate up
$LATHE migrate status

step "stage 1: CRUD tests and migration runner tests"
LATHE_SEED=1 gotest -tags stage1

step "stage 2: evolve the schema over existing data"
export LATHE_STAGE=2
$LATHE migrate diff evolve
echo "--- $(ls migrations/$D | grep evolve.up)"
cat migrations/$D/*_evolve.up.sql
$LATHE migrate up
$LATHE migrate status
$LATHE generate
gotest -tags stage2 -run 'TestDataSurvivedTheMigration|TestMigrationsAreApplied|TestUpIsIdempotent'

step "revert stage 2 and verify the old shape and data"
$LATHE migrate down
unset LATHE_STAGE
$LATHE generate
LATHE_AFTER_ROLLBACK=1 gotest -tags stage1 -run 'TestZAfterRollback'

step "re-apply stage 2 and exercise it"
export LATHE_STAGE=2
$LATHE migrate up
$LATHE generate
gotest -tags stage2 -run 'TestRelationsOnMigratedTables|TestEvolvedSchemaWorks|TestMigrationsAreApplied|TestUpIsIdempotent|TestEditedMigrationIsRejected'

step "no drift: the schema matches the last snapshot"
$LATHE migrate diff drift_check
printf '\n\033[1;32m[%s] all end-to-end checks passed\033[0m\n' "$D"
