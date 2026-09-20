# lathe

A schema-first ORM toolchain for Go, designed to feel like Drizzle or Prisma
rather than GORM: you describe the database once, in Go, and get typed models,
typed columns, a client and SQL migrations out of it.

```go
user, err := client.Users.FindFirst().
    Where(db.Users.Email.Eq(email), db.Users.Active.Eq(true)).
    Exclude(db.Users.PasswordHash, db.Users.APIKey).
    One(ctx)
```

`db.Users.Email` is a `lathe.Column[string]`. `db.Users.Email.Eq(42)` does not
compile. Reads use generated field accessors instead of reflection, there are
no struct tags to keep in sync and no string column names in your code.
Requires Go 1.22 or newer.

> **Status: v0.1.** The whole pipeline (schema, codegen, migration planning,
> migration running, queries) is implemented and tested against real
> PostgreSQL 16, MySQL 8 and SQLite. See [Limitations](#limitations) for what
> is deliberately not there yet. "lathe" is a working name: the module path
> appears in `go.mod`, the imports of generated code and the CLI templates, so
> renaming is one `sed` (see [docs/design.md](docs/design.md#renaming)).

- [Quickstart](#quickstart)
- [Schema reference](#schema-reference)
- [Querying](#querying)
- [Migrations](#migrations)
- [Drivers](#drivers)
- [Money and decimals](#money-and-decimals)
- [Limitations](#limitations)

## Quickstart

```sh
go install github.com/tobibamidele/lathe/cmd/lathe@latest

cd myapp                                  # any Go module
go get github.com/tobibamidele/lathe@latest
lathe init --dialect postgres             # writes schema/schema.go
```

Edit `schema/schema.go`. It is ordinary Go, so you get autocomplete, compile
errors and the ability to use variables, loops and conditionals:

```go
package schema

import s "github.com/tobibamidele/lathe/schema"

var Schema = s.New(s.Config{
    Dialect:    s.Postgres,
    Output:     "db",          // generated package
    Migrations: "migrations",  // SQL files and the schema snapshot
},
    s.Table("users",
        s.BigInt("id").PrimaryKey().AutoIncrement(),
        s.VarChar("email", 255).Unique(),
        s.Text("password_hash"),
        s.Text("bio").Nullable(),
        s.Enum("role", "admin", "member").Default("member"),
        s.Money("balance").Default("0"),
        s.Timestamp("created_at").DefaultNow(),
    ),
    s.Table("posts",
        s.UUID("id").PrimaryKey().DefaultUUID(),
        s.BigInt("author_id").References("users", "id").OnDelete(s.Cascade).Index(),
        s.VarChar("title", 200),
        s.JSON("meta").Nullable(),
    ),
)
```

```sh
lathe generate                    # writes ./db (models, typed columns, client)
lathe migrate diff init           # writes migrations/<ts>_init.up.sql and .down.sql
lathe migrate up --url "$DATABASE_URL"
go get github.com/shopspring/decimal github.com/google/uuid   # if the schema uses them
```

Use it:

```go
import (
    _ "github.com/jackc/pgx/v5/stdlib"   // or github.com/lib/pq

    "github.com/tobibamidele/lathe/postgres"
    "myapp/db"
)

conn, err := postgres.Open(os.Getenv("DATABASE_URL"))
client := db.NewClient(conn)

u := db.User{Email: "ann@example.com", PasswordHash: hash}
err = client.Users.Create(ctx, &u)      // u.ID, u.Role, u.CreatedAt are filled in

admins, err := client.Users.FindMany().
    Where(db.Users.Role.Eq(db.UserRoleAdmin)).
    OrderBy(db.Users.CreatedAt.Desc()).
    Limit(20).
    All(ctx)
```

Change the schema, run `lathe generate` and `lathe migrate diff add_bio` again.
`lathe migrate diff --dry-run` prints the SQL without writing files.

## Schema reference

### Columns

| Builder | Go type | PostgreSQL | MySQL | SQLite |
|---|---|---|---|---|
| `SmallInt` / `Int` / `BigInt` | `int16` / `int32` / `int64` | `SMALLINT` / `INTEGER` / `BIGINT` | `SMALLINT` / `INT` / `BIGINT` | `INTEGER` |
| `Real` / `Double` | `float32` / `float64` | `REAL` / `DOUBLE PRECISION` | `FLOAT` / `DOUBLE` | `REAL` |
| `Decimal(p, s)` | `decimal.Decimal` | `NUMERIC(p,s)` | `DECIMAL(p,s)` | `DECIMAL(p,s)` (numeric affinity) |
| `Money` | `decimal.Decimal` | `NUMERIC(19,4)` | `DECIMAL(19,4)` | `DECIMAL(19,4)` |
| `Bool` | `bool` | `BOOLEAN` | `BOOLEAN` | `BOOLEAN` |
| `VarChar(n)` | `string` | `VARCHAR(n)` | `VARCHAR(n)` | `TEXT` |
| `Text` | `string` | `TEXT` | `TEXT` | `TEXT` |
| `UUID` | `uuid.UUID` | `UUID` | `CHAR(36)` | `TEXT` |
| `Timestamp` | `time.Time` | `TIMESTAMPTZ` | `DATETIME(6)` | `DATETIME` |
| `Date` | `time.Time` | `DATE` | `DATE` | `DATE` |
| `JSON` | `lathe.JSON` | `JSONB` | `JSON` | `TEXT` |
| `Bytes` | `[]byte` | `BYTEA` | `LONGBLOB` | `BLOB` |
| `Enum(v...)` | generated `string` type | `TEXT` + `CHECK` | `ENUM(...)` | `TEXT` + `CHECK` |

Columns are `NOT NULL` unless you call `.Nullable()`, and nullable columns
become pointers in the generated struct (`Bio *string`).

### Modifiers

`PrimaryKey()`, `Nullable()`, `AutoIncrement()`, `Unique()`, `Index()`,
`Default(v)`, `DefaultNow()`, `DefaultUUID()`, `DefaultExpr(sql)`,
`References(table, column)` with `OnDelete(...)` / `OnUpdate(...)` and
`Relation(forward, reverse)` (see [Relations](#relations)), `Field(name)`
(override the Go field name) and `RenamedFrom(old)` (see
[renames](#renames)).

Table level: `s.Index(cols...)`, `s.UniqueIndex(cols...)`,
`s.PrimaryKey(cols...)` for composite keys, `s.ForeignKey(cols...).References(table, cols...)`
for composite foreign keys, and on the table itself `.Model("Name")`.
`Add(items...)` appends items, which helps with conditional schemas.

Schema mistakes (unknown references, type mismatches across a foreign key,
bad defaults, missing primary keys...) are reported together, before anything
is generated.

### Defaults, precisely

A Go zero value is sometimes a real value (`false`, `0`), so `Create` never
silently replaces one with a constant default. `Create` leaves a column out of
the `INSERT`, letting the database fill it, only when zero means "not set":

- auto increment keys,
- columns defaulting to `now`, a UUID or an expression,
- string, enum, time and JSON columns with a constant default, when empty
  (`Currency: ""` means "use `'USD'`"),
- **nullable** columns with any default, when the pointer is nil.

So `Active bool` with `Default(true)` inserts whatever you set (`false` unless
you say otherwise), and so does `Karma int` with `Default(0)`. To get a
bool or number default from the database, make the column `Nullable()` and
leave the pointer nil. UUID defaults are generated in Go (`uuid.New()`) when
the field is zero, so the value is known on every dialect, and the column keeps
its database default for rows inserted by other tools.

## Querying

The generated `Client` has a field per table. Each is a `lathe.Model` that
embeds these methods; the package-level variables (`db.Users`) hold the typed
columns.

### Reading

```go
client.Users.Get(ctx, id)                       // by primary key, ErrNotFound if absent
client.Users.FindFirst().Where(...).One(ctx)    // *User, ErrNotFound if absent
client.Users.FindMany().Where(...).All(ctx)     // []User

q := client.Users.FindMany().
    Where(db.Users.Age.Gte(18), db.Users.Email.HasSuffix("@example.com")). // ANDed
    OrderBy(db.Users.CreatedAt.Desc(), db.Users.ID.Asc()).
    Limit(20).Offset(40).
    Exclude(db.Users.PasswordHash).      // or Select(db.Users.ID, db.Users.Email)
    All(ctx)

client.Users.Count(ctx, db.Users.Active.Eq(true))
client.Users.Exists(ctx, db.Users.Email.Eq(email))
```

Fields left out by `Exclude` or `Select` keep their zero values. Passing a
column of another table is an error, not silent misuse.

### Writing

```go
client.Users.Create(ctx, &u)               // reads generated columns back
client.Users.CreateMany(ctx, users)        // one transaction, batched multi-row INSERTs
client.Users.Save(ctx, &u)                 // UPDATE every column by primary key
client.Users.Update().
    Set(db.Users.Bio.Set("hi"), lathe.Incr(db.Users.Age, 1)).
    Where(db.Users.ID.Eq(id)).
    Exec(ctx)                              // (rowsMatched, error)
client.Users.Delete().Where(db.Users.ID.Eq(id)).Exec(ctx)
```

`Update` and `Delete` without a `Where` return `lathe.ErrMissingWhere`; add
`.AllRows()` when you really mean every row.

### Relations

Relations come from your foreign keys; there is nothing else to declare.

| Foreign key | Generated |
|---|---|
| `posts.author_id -> users.id` | `Post.Author *User` (belongs-to) and `User.Posts []Post` (has-many) |
| the same, but the key is unique or the primary key (`profiles.user_id`) | `Profile.User *User` and `User.Profile *Profile` (has-one) |
| a join table: two foreign keys to two tables that together are its primary key (`post_tags`) | `Post.Tags []Tag` and `Tag.Posts []Post` (many-to-many), next to `Post.PostTags` |
| several keys to one table (`messages.sender_id`, `.recipient_id`) | `Message.Sender`, `Message.Recipient`, `User.MessagesBySender`, `User.MessagesByRecipient` |

Composite foreign keys work. Names come from the column (`author_id` becomes
`Author`); override them with `.Relation("Writer", "Articles")` on the
reference, use `""` to keep a derived name and `"-"` to skip that side.
Generation stops with an explanation if two names collide.

Relation fields on the structs are `nil` until loaded, and `With` loads them in
batches: one extra query per relation (two for many-to-many), however many rows
came back, so there is no N+1.

```go
users, err := client.Users.FindMany().
    Where(db.Users.Active.Eq(true)).
    With(
        db.Users.Profile,
        db.Users.Posts.
            Where(db.Posts.Status.Eq(db.PostStatusPublished)).   // filter the related rows
            OrderBy(db.Posts.Views.Desc()).                      // order each parent's list
            Exclude(db.Posts.Cover).                             // leave columns out
            With(                                                // nested, to any depth
                db.Posts.Tags.OrderBy(db.Tags.Name.Asc()),
                db.Posts.Comments.With(db.Comments.User),
            ),
    ).
    All(ctx)

for _, u := range users {
    for _, p := range u.Posts { fmt.Println(u.Name, p.Title, len(p.Tags), p.Comments[0].User.Name) }
}
```

- Has-many and many-to-many fields of a row without related rows are empty,
  non-nil slices (`loaded, none found`); a belongs-to or has-one with a `NULL` or
  dangling key stays `nil`.
- `db.Users.Posts.Where(...)` returns a modified copy; the original is never
  changed.
- `With` works on `FindFirst().One`, `FindMany().All`, and on rows you already
  hold: `client.Users.Load(ctx, &u, db.Users.Posts)` and
  `client.Users.LoadMany(ctx, users, db.Users.Posts)`.
- The query must select the columns a relation reads (`Select` that leaves out
  `author_id` while asking for `With(db.Posts.Author)` is an error), and
  `Exclude` cannot remove the column that links related rows to their parents.
- The structs also serialise: relation fields are `json:"author,omitempty"`.

### Upserts

```go
// insert, or update the existing row with the same email
err := client.Users.Upsert(&u).
    OnConflict(db.Users.Email).
    DoUpdate(db.Users.Name, db.Users.Bio).      // overwrite only these columns
    Exec(ctx)

.DoUpdate()                                     // no arguments: every inserted column except keys
.Set(lathe.Incr(db.Counters.Hits, 1))           // arbitrary expressions on conflict
.DoNothing()                                    // keep the existing row: get-or-create

// many rows at once
err = client.Users.UpsertMany(users).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx)
```

It renders `INSERT ... ON CONFLICT (email) DO UPDATE SET ... = EXCLUDED....` on
PostgreSQL and SQLite and `INSERT ... ON DUPLICATE KEY UPDATE ... = VALUES(...)`
on MySQL, where the conflict columns are only used to read the row back
(`ON DUPLICATE KEY` applies to whichever unique key collides).

`UpsertMany` sends multi-row statements sized to the driver's parameter limit,
groups rows that insert different column sets (some with an explicit `Role`,
some relying on the default), and runs everything in one transaction, so it is
all or nothing. Locally, 5,000 mixed inserts and updates took 150 ms on
PostgreSQL, 220 ms on MySQL and 65 ms on SQLite, against 2.2 s, 3.8 s and 0.4 s
for a loop of single upserts. Two rows with the same conflict key in one call are
rejected up front (PostgreSQL refuses them, and elsewhere the outcome would
depend on row order).

After `Exec` the struct **reflects the database**: the inserted row (with its
id and defaults), the updated row, or, for `DoNothing`, the row that was already
there. That makes `Upsert(&tag).OnConflict(db.Tags.Name).DoNothing()` a
get-or-create in one call. Rows are matched to the database by their
`OnConflict` columns (not by position, which `DoNothing` would break), and where
the database compares keys differently than Go does, such as MySQL's
case-insensitive collations, the database decides which row is meant. Columns you list in `DoUpdate` must be part of the
`INSERT` (a zero-valued column with a database default is left out, see
[defaults](#defaults-precisely)), and misuse (no action, both actions, a
missing conflict target on PostgreSQL/SQLite) is reported before any SQL runs.

### Conditions

| | |
|---|---|
| `Eq Ne Gt Gte Lt Lte` | comparisons, typed by the column |
| `In(...) NotIn(...)` | an empty list matches nothing / everything, never invalid SQL |
| `Between(lo, hi)` `IsNull()` `IsNotNull()` | |
| `Like ILike Contains HasPrefix HasSuffix` | `Contains`, `HasPrefix` and `HasSuffix` escape `%` and `_`; `ILike` is native on PostgreSQL |
| `EqCol NeCol GtCol LtCol` | column to column, for joins |
| `lathe.And Or Not` | combine; `And()` is true, `Or()` is false |
| `lathe.Exists(q)` `col.InQuery(q)` | subqueries |
| `lathe.SQL("lower(email) = ?", v)` | raw fragment, values always bound |

### Transactions

```go
err := client.Tx(ctx, func(tx *db.Client) error {
    if err := tx.Users.Create(ctx, &u); err != nil {
        return err               // rolls back
    }
    return tx.Posts.Create(ctx, &p)   // commits when this returns nil
})
```

Panics roll back and re-panic. Calling `tx.Tx` inside a transaction uses a
savepoint, so an inner failure (even a constraint violation on PostgreSQL)
rolls back only the inner work. `TxOptions` takes `*sql.TxOptions`.

### Joins, aggregates, projections

```go
type stat struct {
    Email string
    Posts int64
}
rows, err := lathe.Scan[stat](ctx, client.DB,
    lathe.Select(db.Users.Email, lathe.Count(db.Posts.ID).As("posts")).
        From(db.Users).
        LeftJoin(db.Posts, db.Posts.AuthorID.EqCol(db.Users.ID)).
        GroupBy(db.Users.Email).
        Having(lathe.Count(db.Posts.ID).Gt(0)).
        OrderBy(lathe.Count(db.Posts.ID).Desc()))
```

Result columns map to struct fields by `db` tag or by name (case and
underscores ignored: `total_paid` fills `TotalPaid`). A result column with no
matching field is an error. `lathe.ScanOne` returns the first row or
`ErrNotFound`; scanning into a scalar works for one-column queries.

### Raw SQL

```go
rows, err := lathe.Raw[stat](ctx, client.DB,
    "SELECT email, count(*) AS posts FROM users JOIN posts ON ... WHERE age > ? GROUP BY email", 21)
res, err := client.Exec(ctx, "UPDATE users SET age = age + ? WHERE id = ?", 1, id)
rows2, err := client.Query(ctx, "SELECT ...")   // plain *sql.Rows
```

Use `?` on every dialect (it is rewritten to `$1, $2...` for PostgreSQL) and
`??` for a literal question mark, such as the jsonb operators. Placeholders
inside string literals, quoted identifiers and comments are left alone.

### Errors

```go
errors.Is(err, lathe.ErrNotFound)             // also matches sql.ErrNoRows
errors.Is(err, lathe.ErrUniqueViolation)
errors.Is(err, lathe.ErrForeignKeyViolation)
errors.Is(err, lathe.ErrNotNullViolation)
errors.Is(err, lathe.ErrCheckViolation)
errors.Is(err, lathe.ErrMissingWhere)
```

The classified errors still wrap the driver error, so `errors.As` reaches it.

### Logging and tracing

```go
conn, _ := postgres.Open(dsn, lathe.WithLogger(func(ctx context.Context, e lathe.QueryEvent) {
    slog.DebugContext(ctx, "sql", "query", e.SQL, "args", e.Args, "took", e.Duration, "err", e.Err)
}))
```

## Migrations

```
migrations/
  20260920120000_init.up.sql
  20260920120000_init.down.sql
  20260921090000_add_bio.up.sql
  20260921090000_add_bio.down.sql
  snapshot.json          # the schema as of the last diff
```

| Command | |
|---|---|
| `lathe migrate diff [name]` | diff the schema against `snapshot.json`, write the migration pair and update the snapshot. `--dry-run` prints instead. |
| `lathe migrate up [--steps N]` | apply pending migrations |
| `lathe migrate down [--steps N]` | revert the newest migration (default 1) |
| `lathe migrate status` | list migrations: `applied`, `pending`, `MODIFIED`, `MISSING` |
| `lathe migrate new name` | empty pair for hand-written SQL |
| `lathe migrate snapshot` | record the current schema without writing a migration |

`up`, `down` and `status` read the URL from `--url` or `$DATABASE_URL`.

**Down migrations are generated too**, as the exact inverse of the up steps in
reverse order. Data destroyed by an up migration (a dropped column or table)
comes back as structure only, and the diff prints a warning for those changes:
dropped tables and columns, narrowing type changes, `NULL` to `NOT NULL`.

### Renames

A diff cannot tell a rename from a drop plus an add, and guessing loses data. Tell
it:

```go
s.Table("people",
    s.VarChar("full_name", 200).RenamedFrom("name"),
).RenamedFrom("users")
```

Run `migrate diff`, then delete the `RenamedFrom` calls; a stale hint is
ignored.

### Running migrations from your app

```go
//go:embed migrations/*.sql
var files embed.FS

sub, _ := fs.Sub(files, "migrations")
applied, err := migrate.Up(ctx, conn, sub)   // safe to call from every instance
```

Applied versions are stored with a SHA-256 of the up file in `lathe_migrations`;
editing an applied migration is an error. Concurrent runners are serialised
with an advisory lock (PostgreSQL, MySQL).

### Per-database notes

- **PostgreSQL / SQLite**: each migration runs in a transaction, together with
  its bookkeeping row.
- **MySQL** commits DDL implicitly, so a failed migration can be partly
  applied; the error says which statement failed.
- **SQLite** cannot alter columns or constraints. The planner uses the
  documented rebuild procedure (create new table, copy rows, drop, rename,
  recreate indexes) and the runner turns foreign key enforcement off around
  it, then verifies `PRAGMA foreign_key_check` before committing.
- Changing a primary key is refused with instructions, because it cannot be
  done safely without knowing your data; use `migrate new` and
  `migrate snapshot`.

## Drivers

lathe talks to `database/sql` and never imports a driver, so its `go.mod` has
no requirements and you download only what you use.

| Dialect | Import one of | Open with |
|---|---|---|
| PostgreSQL | `github.com/jackc/pgx/v5/stdlib`, `github.com/lib/pq` | `postgres.Open(url)` |
| MySQL / MariaDB | `github.com/go-sql-driver/mysql` | `mysql.Open(dsn)` (also accepts `mysql://` URLs) |
| SQLite | `modernc.org/sqlite` (pure Go), `github.com/mattn/go-sqlite3` (cgo) | `sqlite.Open(path)` |

`Open` adds what lathe relies on: `parseTime` and `clientFoundRows` for MySQL,
and `foreign_keys=ON`, a busy timeout and WAL for SQLite (per connection).
If you build the `*sql.DB` yourself, pass it to `postgres.New`, `mysql.New` or
`sqlite.New` and set those options yourself.

`lathe migrate` links a driver into a scratch program in your module. Set
`Driver: "github.com/lib/pq"` in `s.Config` if you do not use the default
(`pgx`, `go-sql-driver/mysql`, `modernc.org/sqlite`).

## Money and decimals

`s.Decimal(p, s)` and `s.Money(name)` (a `DECIMAL(19,4)`) generate
`decimal.Decimal` from `github.com/shopspring/decimal`, exact on PostgreSQL and
MySQL. Comparisons and `SUM` work in the database:

```go
rich, _ := client.Users.FindMany().Where(db.Users.Balance.Gt(decimal.RequireFromString("50"))).All(ctx)
```

SQLite has no exact numeric type. lathe declares the column with numeric
affinity, which keeps ordering and aggregates numeric but is exact only to about
15 significant digits. Keep currency in its own column; there is no `Money`
struct with a built-in currency on purpose.

## Limitations

Deliberately not there yet:

- **Nested writes** (creating a post together with its tags in one call). Loading
  is covered by [relations](#relations), writing goes through the per-table
  clients inside a transaction.
- **A per-parent limit on preloaded rows** (`Comments.Limit(3)` for each post);
  it needs window functions and differs per database.
- A `Where` on `DO UPDATE` ("update only if the incoming row is newer"). Plain,
  bulk and expression upserts are supported.
- Table aliases, so self-joins need raw SQL.
- Rename detection is hint based (`RenamedFrom`), never guessed.
- Migration snapshots are a single JSON file: two branches that both run
  `migrate diff` conflict in `snapshot.json`, and the resolution is to rebase
  and re-run the diff.

Tested against PostgreSQL 16 (`lib/pq`), MySQL 8.0 (`go-sql-driver/mysql`
1.7.1) and SQLite (`mattn/go-sqlite3`). `pgx` and `modernc.org/sqlite` go
through the same `database/sql` interfaces but were not part of the automated
runs.

## Development

```sh
make test          # unit tests with the race detector, no database needed
make e2e-sqlite    # whole toolchain against SQLite
make e2e-postgres  # needs a server, see integration/run.sh
make e2e-mysql
```

`integration/run.sh` generates code, writes and applies migrations, runs the
CRUD suite, evolves the schema over existing rows (add, rename, retype, index,
drop and create tables), checks the data survived, reverts the migration,
checks the old shape and data, applies it again and finally checks that the
schema has no drift. CI runs it for all three databases.

`examples/showcase` is a runnable tour of the whole API against PostgreSQL. See
[docs/design.md](docs/design.md) for how the pieces fit together.

## License

MIT
