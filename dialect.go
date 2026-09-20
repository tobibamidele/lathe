package lathe

// ConflictStyle is the flavour of upsert SQL a database speaks.
type ConflictStyle int

const (
	// OnConflict is INSERT ... ON CONFLICT (cols) DO UPDATE / DO NOTHING
	// (PostgreSQL, SQLite).
	OnConflict ConflictStyle = iota
	// OnDuplicateKey is INSERT ... ON DUPLICATE KEY UPDATE (MySQL, MariaDB).
	OnDuplicateKey
)

// Dialect adapts SQL generation to a database engine. The postgres, mysql and
// sqlite packages provide implementations; user code rarely touches this
// interface directly.
type Dialect interface {
	// Name is "postgres", "mysql" or "sqlite".
	Name() string
	// Placeholder renders the n-th (1-based) bind parameter.
	Placeholder(n int) string
	// Quote quotes an identifier.
	Quote(ident string) string
	// SupportsReturning reports whether INSERT ... RETURNING is available.
	SupportsReturning() bool
	// NativeILike reports whether ILIKE exists.
	NativeILike() bool
	// DefaultValues renders an INSERT that uses only column defaults, given
	// the quoted table name.
	DefaultValues(quotedTable string) string
	// LimitOffset renders the LIMIT and OFFSET tail. A negative limit means
	// no limit; a zero offset means no offset. The result starts with a space
	// and is empty when both are unset.
	LimitOffset(limit, offset int64) string
	// MaxParams is the largest number of bind parameters in one statement.
	MaxParams() int
	// ConflictStyle selects the upsert syntax.
	ConflictStyle() ConflictStyle
	// ClassifyError maps a driver error to one of the Err*Violation sentinels,
	// or returns nil when the error is not a recognised constraint failure.
	ClassifyError(err error) error
}
