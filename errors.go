package lathe

import (
	"database/sql"
	"errors"
)

// Sentinel errors. Use errors.Is to test for them.
var (
	// ErrNotFound is returned when a query that must yield a row yields none.
	// errors.Is(err, sql.ErrNoRows) is also true for it.
	ErrNotFound error = notFoundError{}

	// ErrUniqueViolation is wrapped around driver errors for unique or primary
	// key violations.
	ErrUniqueViolation = errors.New("lathe: unique constraint violation")
	// ErrForeignKeyViolation is wrapped around foreign key violations.
	ErrForeignKeyViolation = errors.New("lathe: foreign key violation")
	// ErrNotNullViolation is wrapped around NOT NULL violations.
	ErrNotNullViolation = errors.New("lathe: not-null constraint violation")
	// ErrCheckViolation is wrapped around CHECK constraint violations.
	ErrCheckViolation = errors.New("lathe: check constraint violation")

	// ErrMissingWhere is returned by Update and Delete queries that have no
	// Where clause. Call AllRows to confirm that touching every row is intended.
	ErrMissingWhere = errors.New("lathe: update or delete without WHERE; call AllRows() to affect every row")
)

type notFoundError struct{}

func (notFoundError) Error() string { return "lathe: record not found" }
func (notFoundError) Is(target error) bool {
	return target == sql.ErrNoRows
}

// constraintError carries both the lathe sentinel and the original driver
// error, so errors.Is matches the former and errors.As can reach the latter.
type constraintError struct {
	kind error
	err  error
}

func (e *constraintError) Error() string   { return e.kind.Error() + ": " + e.err.Error() }
func (e *constraintError) Unwrap() []error { return []error{e.kind, e.err} }
