# Design notes

## Pipeline

```
schema/schema.go                    your Go code, using package schema
      │  lathe loads it: a throwaway `main` inside your module imports it,
      │  calls Schema.Export() and prints JSON (go run)
      ▼
schema.Snapshot  ──────────────┬──────────────────────────────┐
(tables, columns, indexes, FKs)│                              │
      │                        │ diff against migrations/     │
      ▼                        ▼ snapshot.json                ▼
internal/gen              internal/plan                 lathe migrate up/down
Go source: models,        ordered, reversible           a throwaway main that
typed columns, client     steps ──► internal/ddl        imports your driver and
                                    per-dialect SQL     package migrate
```

- **`schema`** is the only package user code needs while describing the
  database. It validates everything at `Export()` time and produces a plain
  serialisable value. Constraint and index names are derived deterministically
  (and fit the 63 byte PostgreSQL limit), so diffs are stable.
- **`internal/ddl`** renders CREATE/ALTER/DROP per dialect and defines the
  reversible `Step` types. **`internal/plan`** diffs two snapshots into steps.
  Down migrations are `reverse(invert(steps))`, so there is no second code path
  to keep correct.
- **`internal/gen`** renders Go with `text/template` and `go/format`.
- **`internal/loader`** runs your schema (and, for migrations, your driver)
  through `go run` inside your module, so your `go.mod` resolves every import
  and lathe itself has no driver dependencies.
- The runtime (root package `lathe`, `postgres`, `mysql`, `sqlite`, `migrate`)
  is what your program imports.

## Decisions worth knowing

**The schema is Go, executed.** A DSL file would need a parser, formatter and
editor support. Go gives type checking, autocomplete, reuse and conditionals
for free; the cost is one `go run` per CLI invocation.

**Typed columns via generics.** Go has no way to refer to `user.email` on a
value, so the generator emits `Column[T]` descriptors. Methods on generic types
are allowed, which gives `db.Users.Email.Eq("x")` with compile-time checking
(free generic functions would have made `Eq(col, value)` read worse).

**No reflection on the hot path.** Generated `TableSpec`s hold a closure per
field returning a pointer to it; scanning and argument building use them. The
only reflection is a zero-value check on INSERT/Save and struct mapping for
free-form `Scan`/`Raw` (cached per type).

**Drivers are ambient.** `postgres.Open` looks through `sql.Drivers()`. That
keeps the module dependency-free at the cost of MySQL error classification
reading `Number` through reflection and SQLite's matching messages.

**Constraint errors surface late.** `INSERT ... RETURNING` reports failures
while iterating the result, not from `QueryContext`. Every row iteration goes
through the same wrapping, which is what makes `errors.Is(err,
lathe.ErrUniqueViolation)` reliable. The end-to-end suite caught this.

**Migration ordering.** Up order: drop foreign keys that go away, drop removed
tables, rename tables, create tables (referenced first; cycles defer
constraints), alter tables, add new foreign keys. Dropping before renaming is
deliberate: the inverse recreates dropped tables after the rename is undone, so
their foreign keys name a table that exists again.

**SQLite.** No `ALTER COLUMN`, no constraint changes. The planner falls back to
rebuilding the table; `AUTOINCREMENT` tables use the inline primary key form
SQLite requires. Foreign key enforcement is switched off around the rebuild (the
pragma is a no-op inside a transaction) and `foreign_key_check` gates the
commit.

**UUID defaults are client-side.** MySQL has no RETURNING, so a key that only the
database knows cannot be read back. Generated code fills a zero UUID with
`uuid.New()`; `Create` returns a clear error for the remaining unreadable case
(a database-generated non-auto primary key on MySQL).

**`DefaultFunc` is a type-checked client-side default.** Every column constructor
returns a generic `*ColumnBuilder[T]` where `T` is the column's Go type, so
`DefaultFunc(fn)` only accepts `func() T` and a mismatched default is a compile
error. `fn` must be a named, exported, package-level function; the builder
records its fully qualified name (`runtime.FuncForPC`) in the snapshot instead
of calling it. Generated code imports the schema package and calls the function
per insert when the field still holds its zero value, so it can return a fresh
value per row (the normal case for hand-rolled ID generators). Closures (`.funcN`)
and method values (`-fm`) have no name the generated package could call and are
rejected at schema load. A column holds either a server-side default or a
DefaultFunc -- whichever was called last; `Validate` rejects a snapshot that
carries both.

**Relations are derived, batched and cycle-free.** They come from foreign keys, so
the schema stays the single source of truth. Each is a `Relation[Source, Target]`
holding a loader closure; `With` runs them after the main query. A loader
collects the parents' key values, fetches related rows with one `WHERE key IN
(...)` (an OR of ANDs for composite keys, in chunks that respect the driver's
parameter limit), and attaches them by key. Many-to-many goes through the join
table's own model. Keys are compared as formatted strings so both sides match
even when their Go types differ. The generated relation values only mention
table specs and column *names*, never other table variables, which avoids Go
initialisation cycles between `db.Users` and `db.Posts`.

**Upserts read back.** `RETURNING` gives PostgreSQL and SQLite the final row;
when `DO NOTHING` skips the insert (nothing is returned) and on MySQL, which has
no `RETURNING`, the row is reloaded by the conflict columns. The struct therefore
always reflects the database, at the cost of one extra query in those cases.

**One code path for one row or many.** `Upsert(row)` is `UpsertMany` with a single
row. Rows are grouped by the columns they actually insert (a zero-valued column
with a database default is left out, so rows can differ), each group is cut into
statements that fit the driver's parameter limit (Set-expression parameters
count against it in every statement), and the run is one transaction. The
results are reconciled into the caller's structs by conflict key: `RETURNING`
rows first, then one batched SELECT for rows a `DO NOTHING` skipped or MySQL
did not return, then a row-by-row `=` lookup for anything Go and the database
disagree about (collations). Duplicate keys inside one call are rejected before
any SQL runs.

Generated code embeds the runtime import path, so regenerate after renaming.
