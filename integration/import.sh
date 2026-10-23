#!/usr/bin/env bash
# End-to-end test for `lathe import`.
#
#   ./import.sh
#
# Builds a throwaway Go module, writes a set of SQL migrations whose later
# files rename/alter/drop what earlier files created, imports them, generates
# models from the result and verifies the final shape. The generated schema and
# models are left behind as an artifact and their location is printed.
set -euo pipefail
cd "$(dirname "$0")"

ROOT=$(cd .. && pwd)
WORK="${LATHE_IMPORT_WORK:-/tmp/opencode/lathe-import-e2e}"
LATHE="$WORK/bin/lathe"

fail() { printf '\n\033[1;31mimport e2e failed: %s\033[0m\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1m== [import] %s\033[0m\n' "$*"; }

assert_contains() {
  grep -Fq "$2" "$1" || fail "expected $1 to contain: $2"
}
assert_missing() {
  if grep -Fq "$2" "$1"; then fail "expected $1 not to contain: $2"; fi
}

rm -rf "$WORK"
mkdir -p "$WORK/bin"
(cd "$ROOT" && go build -o "$LATHE" ./cmd/lathe)

step "create a module and chronological migrations"
mkdir -p "$WORK/migrations/nested" "$WORK/schema"

cat >"$WORK/go.mod" <<EOF
module example.com/importe2e

go 1.22

require github.com/tobibamidele/lathe v0.0.0

replace github.com/tobibamidele/lathe => $ROOT
EOF

cat >"$WORK/migrations/001_init.sql" <<'SQL'
CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tags (
    id SERIAL PRIMARY KEY,
    label TEXT NOT NULL
);

CREATE TABLE legacy_posts (
    id BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title VARCHAR(200) NOT NULL,
    body TEXT,
    tag_id INTEGER REFERENCES tags(id)
);

CREATE INDEX legacy_posts_author_idx ON legacy_posts(author_id);
SQL

cat >"$WORK/migrations/002_rename.sql" <<'SQL'
ALTER TABLE users RENAME COLUMN name TO full_name;
ALTER TABLE legacy_posts RENAME TO posts;
ALTER TABLE posts RENAME COLUMN title TO headline;
DROP INDEX legacy_posts_author_idx;
SQL

cat >"$WORK/migrations/003_evolve.sql" <<'SQL'
CREATE TYPE post_status AS ENUM ('draft', 'published', 'archived');

ALTER TABLE posts ADD COLUMN slug VARCHAR(200) NOT NULL UNIQUE;
ALTER TABLE posts ADD COLUMN status post_status NOT NULL DEFAULT 'draft';
ALTER TABLE posts ADD COLUMN view_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE posts DROP COLUMN body;
ALTER TABLE posts ADD COLUMN body TEXT NOT NULL DEFAULT '';
ALTER TABLE posts DROP CONSTRAINT fk_legacy_posts_tag_id;
ALTER TABLE posts DROP COLUMN tag_id;
DROP TABLE tags;
SQL

# A down migration must be ignored: it creates a table that never exists in the
# forward history.
cat >"$WORK/migrations/003_evolve.down.sql" <<'SQL'
CREATE TABLE should_not_exist (id SERIAL PRIMARY KEY);
DROP TYPE post_status;
SQL

# Created and dropped in the same file: it must not appear in the output.
cat >"$WORK/migrations/004_churn.sql" <<'SQL'
CREATE TABLE scratch (id SERIAL PRIMARY KEY, note TEXT);
DROP TABLE scratch;
SQL

# Nested directories are scanned too (Prisma/Drizzle layout).
cat >"$WORK/migrations/nested/005_sessions.sql" <<'SQL'
CREATE TABLE sessions (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions(user_id);
SQL

cat >"$WORK/migrations/006_audit.sql" <<'SQL'
-- The common idempotent-enum idiom: CREATE TYPE inside an exception handler.
DO $$ BEGIN
    CREATE TYPE audit_action AS ENUM ('create', 'update', 'delete');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE users ADD COLUMN active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN last_action audit_action;
SQL

step "import the migrations"
(
  cd "$WORK"
  "$LATHE" import --from migrations --dialect postgres
)

SCHEMA="$WORK/schema/schema.go"
[ -f "$SCHEMA" ] || fail "schema.go was not written"

step "verify the final, chronologically applied shape"
assert_contains "$SCHEMA" 's.Table("users"'
assert_contains "$SCHEMA" 's.Table("posts"'
assert_contains "$SCHEMA" 's.Table("sessions"'
assert_missing  "$SCHEMA" 's.Table("legacy_posts"'
assert_missing  "$SCHEMA" 's.Table("tags"'
assert_missing  "$SCHEMA" 's.Table("scratch"'
assert_missing  "$SCHEMA" 's.Table("should_not_exist"'

assert_contains "$SCHEMA" 's.Text("full_name")'
assert_missing  "$SCHEMA" 's.Text("name")'
assert_contains "$SCHEMA" 's.VarChar("headline", 200)'
assert_missing  "$SCHEMA" 's.VarChar("title", 200)'
assert_missing  "$SCHEMA" '"tag_id"'
assert_contains "$SCHEMA" '.Name("fk_legacy_posts_author_id")'
assert_contains "$SCHEMA" '.OnDelete(s.Cascade)'
assert_contains "$SCHEMA" 's.Enum("status", "draft", "published", "archived")'
assert_contains "$SCHEMA" 's.Int("view_count")'
assert_contains "$SCHEMA" 's.Bool("active")'
assert_contains "$SCHEMA" 's.Enum("last_action", "create", "update", "delete").Nullable()'

step "generate models from the imported schema"
(
  cd "$WORK"
  go mod tidy
  "$LATHE" generate
)

DB="$WORK/db"
[ -d "$DB" ] || fail "generate did not write $DB"
grep -Rqs 'type User struct' "$DB" || fail "generated User model is missing"
grep -Rqs 'FullName' "$DB" || fail "renamed column did not reach the model"
grep -Rqs 'type Post struct' "$DB" || fail "generated Post model is missing"
grep -Rqs 'Headline' "$DB" || fail "renamed Post.title did not reach the model"
grep -Rqs 'PostStatus' "$DB" || fail "enum type did not reach the generated package"
if grep -Rqs 'type Tag struct' "$DB"; then fail "dropped tags table produced a model"; fi
if grep -Rqs 'type LegacyPost struct' "$DB"; then fail "renamed legacy_posts table produced a model"; fi
if grep -Rqs 'type Scratch struct' "$DB"; then fail "dropped scratch table produced a model"; fi

step "the generated schema compiles and loads"
(
  cd "$WORK"
  "$LATHE" migrate diff verify_import --dry-run >/dev/null
)

step "import is reproducible: a second run refuses to overwrite"
if (cd "$WORK" && "$LATHE" import --from migrations 2>/dev/null); then
  fail "import overwrote schema.go without --force"
fi
(cd "$WORK" && "$LATHE" import --from migrations --force >/dev/null)

step "unsupported constructs warn, and --strict fails before writing"
mkdir -p "$WORK/strict_migrations"
cat >"$WORK/strict_migrations/001_strict.sql" <<'SQL'
CREATE TABLE accounts (
    id SERIAL PRIMARY KEY,
    balance INTEGER NOT NULL CHECK (balance >= 0),
    tags TEXT[]
);
SQL
WARN_LOG="$WORK/strict-warnings.log"
(
  cd "$WORK"
  "$LATHE" import --from strict_migrations --dialect postgres --force 2>"$WARN_LOG"
)
grep -q 'CHECK constraint is not supported' "$WARN_LOG" || fail "CHECK warning was not reported"
grep -q 'array type; imported as json' "$WARN_LOG" || fail "array warning was not reported"
# The warning run wrote a usable schema; --strict must fail and leave no file.
rm -f "$WORK/schema/schema.go"
if (cd "$WORK" && "$LATHE" import --from strict_migrations --strict 2>/dev/null); then
  fail "--strict accepted unsupported SQL"
fi
[ -f "$WORK/schema/schema.go" ] && fail "--strict wrote a schema before failing"

step "dry run writes nothing"
rm -f "$WORK/schema/schema.go"
(cd "$WORK" && "$LATHE" import --from migrations --dry-run >/dev/null)
[ -f "$WORK/schema/schema.go" ] && fail "--dry-run wrote schema.go"

step "custom schema, output and migrations directories"
mkdir -p "$WORK/app/schema"
(cd "$WORK" && "$LATHE" import --from migrations --schema app/schema --output data --migrations sql)
CUSTOM="$WORK/app/schema/schema.go"
[ -f "$CUSTOM" ] || fail "--schema did not control the output path"
assert_contains "$CUSTOM" 'package schema'
assert_contains "$CUSTOM" 'Output:     "data"'
assert_contains "$CUSTOM" 'Migrations: "sql"'

step "mysql and sqlite dialect syntax"
mkdir -p "$WORK/dialects/mysql" "$WORK/dialects/sqlite"
cat >"$WORK/dialects/mysql/001_init.sql" <<'SQL'
CREATE TABLE users (
  id INT UNSIGNED NOT NULL AUTO_INCREMENT,
  email VARCHAR(255) NOT NULL,
  role ENUM('admin', 'member') NOT NULL DEFAULT 'member',
  active TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uq_users_email (email)
) ENGINE=InnoDB;
SQL
cat >"$WORK/dialects/sqlite/001_init.sql" <<'SQL'
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL
);
CREATE TABLE posts (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT NOT NULL
);
ALTER TABLE users RENAME COLUMN name TO full_name;
ALTER TABLE users RENAME TO people;
SQL

MYSQL_SCHEMA="$WORK/dialect-mysql.go"
SQLITE_SCHEMA="$WORK/dialect-sqlite.go"
(cd "$WORK" && "$LATHE" import --from dialects/mysql --dialect mysql --dry-run >"$MYSQL_SCHEMA" 2>"$WORK/mysql-warnings.log")
(cd "$WORK" && "$LATHE" import --from dialects/sqlite --dialect sqlite --dry-run >"$SQLITE_SCHEMA")
assert_contains "$MYSQL_SCHEMA" 'Dialect:    s.MySQL'
assert_contains "$MYSQL_SCHEMA" 's.Int("id").PrimaryKey().AutoIncrement()'
assert_contains "$MYSQL_SCHEMA" 's.Enum("role", "admin", "member").Default("member")'
assert_contains "$MYSQL_SCHEMA" 's.Bool("active").Default(true)'
assert_contains "$MYSQL_SCHEMA" 's.Timestamp("updated_at").DefaultNow()'
grep -q 'ON UPDATE clause is not supported' "$WORK/mysql-warnings.log" || fail "ON UPDATE warning was not reported"

assert_contains "$SQLITE_SCHEMA" 'Dialect:    s.SQLite'
assert_contains "$SQLITE_SCHEMA" 's.Table("people"'
assert_contains "$SQLITE_SCHEMA" 's.Text("full_name")'
assert_contains "$SQLITE_SCHEMA" 's.ForeignKey("user_id").References("people", "id").OnDelete(s.Cascade)'
assert_contains "$SQLITE_SCHEMA" 's.Int("id").PrimaryKey().AutoIncrement()'

step "produce the artifact"
ARTIFACT="$WORK/artifact"
mkdir -p "$ARTIFACT"
cp "$WORK/schema/schema.go" "$ARTIFACT/schema.go" 2>/dev/null || true
# Re-import without --dry-run for the artifact; the strict test removed it.
(cd "$WORK" && "$LATHE" import --from migrations >/dev/null && cp schema/schema.go "$ARTIFACT/schema.go")
cp -R "$DB" "$ARTIFACT/db"
cp -R "$WORK/migrations" "$ARTIFACT/migrations"

printf '\n\033[1;32m[import] all end-to-end checks passed\033[0m\n'
printf 'artifact: %s\n' "$ARTIFACT"
