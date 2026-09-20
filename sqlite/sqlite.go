// Package sqlite connects lathe to SQLite.
//
// It works with any database/sql SQLite driver. Import the one you prefer for
// its side effect:
//
//	import _ "modernc.org/sqlite"          // pure Go, registers "sqlite"
//	// or
//	import _ "github.com/mattn/go-sqlite3" // cgo, registers "sqlite3"
//
// lathe itself depends on neither, so you choose (and download) exactly one.
// SQLite 3.35 or newer is required (RETURNING, DROP COLUMN).
package sqlite

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/internal/driverpick"
)

// Dialect is the SQLite dialect.
var Dialect lathe.Dialect = dialect{}

// New wraps an open *sql.DB.
//
// SQLite enforces foreign keys only when PRAGMA foreign_keys is on, and the
// setting is per connection, so a *sql.DB you open yourself must enable it for
// every connection (through the driver's DSN options). Open does that for you.
func New(db *sql.DB, opts ...lathe.Option) *lathe.DB { return lathe.New(db, Dialect, opts...) }

// Open opens the database at path (a file path, "file:" URI or ":memory:")
// with foreign keys enforced and a 5 second busy timeout on every connection.
// File databases use write-ahead logging.
func Open(path string, opts ...lathe.Option) (*lathe.DB, error) {
	name, err := driverpick.Pick("sqlite", `_ "modernc.org/sqlite"`, "sqlite", "sqlite3")
	if err != nil {
		return nil, err
	}
	memory := isMemory(path)
	db, err := sql.Open(name, DSN(name, path))
	if err != nil {
		return nil, err
	}
	if memory {
		// every connection to :memory: would be its own empty database
		db.SetMaxOpenConns(1)
	}
	return New(db, opts...), nil
}

// DSN appends the connection options lathe relies on to path, in the syntax
// of the driver registered under driverName ("sqlite3" for mattn/go-sqlite3,
// anything else is treated as modernc.org/sqlite).
func DSN(driverName, path string) string {
	var opts []string
	if driverName == "sqlite3" {
		opts = []string{"_foreign_keys=1", "_busy_timeout=5000"}
		if !isMemory(path) {
			opts = append(opts, "_journal_mode=WAL")
		}
	} else {
		opts = []string{"_pragma=foreign_keys(1)", "_pragma=busy_timeout(5000)"}
		if !isMemory(path) {
			opts = append(opts, "_pragma=journal_mode(WAL)")
		}
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(opts, "&")
}

func isMemory(path string) bool {
	return path == ":memory:" || path == "" || strings.Contains(path, "mode=memory")
}

type dialect struct{}

func (dialect) Name() string                       { return "sqlite" }
func (dialect) Placeholder(int) string             { return "?" }
func (dialect) SupportsReturning() bool            { return true }
func (dialect) NativeILike() bool                  { return false }
func (dialect) ConflictStyle() lathe.ConflictStyle { return lathe.OnConflict }
func (dialect) MaxParams() int                     { return 32766 }
func (dialect) DefaultValues(t string) string      { return "INSERT INTO " + t + " DEFAULT VALUES" }

func (dialect) Quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (dialect) LimitOffset(limit, offset int64) string {
	switch {
	case limit < 0 && offset > 0:
		return " LIMIT -1 OFFSET " + strconv.FormatInt(offset, 10)
	case limit < 0:
		return ""
	case offset > 0:
		return " LIMIT " + strconv.FormatInt(limit, 10) + " OFFSET " + strconv.FormatInt(offset, 10)
	}
	return " LIMIT " + strconv.FormatInt(limit, 10)
}

// ClassifyError inspects the message, which is stable across SQLite drivers.
func (dialect) ClassifyError(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique constraint failed"):
		return lathe.ErrUniqueViolation
	case strings.Contains(msg, "foreign key constraint failed"):
		return lathe.ErrForeignKeyViolation
	case strings.Contains(msg, "not null constraint failed"):
		return lathe.ErrNotNullViolation
	case strings.Contains(msg, "check constraint failed"):
		return lathe.ErrCheckViolation
	}
	return nil
}
