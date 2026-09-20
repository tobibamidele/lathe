package lathe

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
	// ClassifyError maps a driver error to one of the Err*Violation sentinels,
	// or returns nil when the error is not a recognised constraint failure.
	ClassifyError(err error) error
}
