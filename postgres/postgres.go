// Package postgres connects lathe to PostgreSQL.
//
// It works with any database/sql driver for PostgreSQL. Import the one you
// prefer for its side effect:
//
//	import _ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
//	// or
//	import _ "github.com/lib/pq" // registers "postgres"
//
// lathe itself depends on neither, so you only download what you use.
package postgres

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/internal/driverpick"
)

// Dialect is the PostgreSQL dialect.
var Dialect lathe.Dialect = dialect{}

// New wraps an open *sql.DB.
func New(db *sql.DB, opts ...lathe.Option) *lathe.DB { return lathe.New(db, Dialect, opts...) }

// Open connects with a URL or key/value DSN understood by the linked driver.
// Like sql.Open it does not dial; call Ping to verify the connection.
func Open(dsn string, opts ...lathe.Option) (*lathe.DB, error) {
	name, err := driverpick.Pick("postgres", `_ "github.com/jackc/pgx/v5/stdlib"`, "pgx", "postgres")
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(name, dsn)
	if err != nil {
		return nil, err
	}
	return New(db, opts...), nil
}

type dialect struct{}

func (dialect) Name() string                       { return "postgres" }
func (dialect) Placeholder(n int) string           { return "$" + strconv.Itoa(n) }
func (dialect) Quote(s string) string              { return quote(s) }
func (dialect) SupportsReturning() bool            { return true }
func (dialect) NativeILike() bool                  { return true }
func (dialect) ConflictStyle() lathe.ConflictStyle { return lathe.OnConflict }
func (dialect) MaxParams() int                     { return 65535 }
func (dialect) DefaultValues(t string) string      { return "INSERT INTO " + t + " DEFAULT VALUES" }

func (dialect) LimitOffset(limit, offset int64) string {
	s := ""
	if limit >= 0 {
		s += " LIMIT " + strconv.FormatInt(limit, 10)
	}
	if offset > 0 {
		s += " OFFSET " + strconv.FormatInt(offset, 10)
	}
	return s
}

func (dialect) ClassifyError(err error) error {
	var se interface{ SQLState() string }
	if !errors.As(err, &se) {
		return nil
	}
	switch se.SQLState() {
	case "23505":
		return lathe.ErrUniqueViolation
	case "23503":
		return lathe.ErrForeignKeyViolation
	case "23502":
		return lathe.ErrNotNullViolation
	case "23514":
		return lathe.ErrCheckViolation
	}
	return nil
}

func quote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}
