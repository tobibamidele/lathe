package lathe_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/mysql"
	"github.com/tobibamidele/lathe/postgres"
	"github.com/tobibamidele/lathe/sqlite"
)

type pgErr struct{ code string }

func (e pgErr) Error() string    { return "pg error " + e.code }
func (e pgErr) SQLState() string { return e.code }

// MySQLError mimics the driver's type; classification must not import it.
type MySQLError struct {
	Number  uint16
	Message string
}

func (e *MySQLError) Error() string { return fmt.Sprintf("Error %d: %s", e.Number, e.Message) }

func TestClassifyErrors(t *testing.T) {
	type tc struct {
		d    lathe.Dialect
		err  error
		want error
	}
	cases := []tc{
		{postgres.Dialect, pgErr{"23505"}, lathe.ErrUniqueViolation},
		{postgres.Dialect, fmt.Errorf("wrapped: %w", pgErr{"23503"}), lathe.ErrForeignKeyViolation},
		{postgres.Dialect, pgErr{"23502"}, lathe.ErrNotNullViolation},
		{postgres.Dialect, pgErr{"23514"}, lathe.ErrCheckViolation},
		{postgres.Dialect, pgErr{"42P01"}, nil},
		{postgres.Dialect, errors.New("boom"), nil},
		{mysql.Dialect, &MySQLError{1062, "Duplicate entry"}, lathe.ErrUniqueViolation},
		{mysql.Dialect, fmt.Errorf("wrapped: %w", &MySQLError{1452, "fk"}), lathe.ErrForeignKeyViolation},
		{mysql.Dialect, &MySQLError{1451, "fk"}, lathe.ErrForeignKeyViolation},
		{mysql.Dialect, &MySQLError{1048, "null"}, lathe.ErrNotNullViolation},
		{mysql.Dialect, &MySQLError{3819, "check"}, lathe.ErrCheckViolation},
		{mysql.Dialect, &MySQLError{1146, "no table"}, nil},
		{sqlite.Dialect, errors.New("constraint failed: UNIQUE constraint failed: users.email (2067)"), lathe.ErrUniqueViolation},
		{sqlite.Dialect, errors.New("FOREIGN KEY constraint failed"), lathe.ErrForeignKeyViolation},
		{sqlite.Dialect, errors.New("NOT NULL constraint failed: users.email"), lathe.ErrNotNullViolation},
		{sqlite.Dialect, errors.New("CHECK constraint failed: ck_users_role"), lathe.ErrCheckViolation},
		{sqlite.Dialect, errors.New("no such table: x"), nil},
	}
	for _, c := range cases {
		if got := c.d.ClassifyError(c.err); got != c.want {
			t.Errorf("%s: ClassifyError(%v) = %v, want %v", c.d.Name(), c.err, got, c.want)
		}
	}
}

func TestLimitOffset(t *testing.T) {
	cases := []struct {
		d             lathe.Dialect
		limit, offset int64
		want          string
	}{
		{postgres.Dialect, -1, 0, ""},
		{postgres.Dialect, 5, 0, " LIMIT 5"},
		{postgres.Dialect, -1, 3, " OFFSET 3"},
		{postgres.Dialect, 5, 3, " LIMIT 5 OFFSET 3"},
		{mysql.Dialect, -1, 3, " LIMIT 18446744073709551615 OFFSET 3"},
		{mysql.Dialect, 5, 3, " LIMIT 5 OFFSET 3"},
		{mysql.Dialect, 5, 0, " LIMIT 5"},
		{sqlite.Dialect, -1, 3, " LIMIT -1 OFFSET 3"},
		{sqlite.Dialect, -1, 0, ""},
	}
	for _, c := range cases {
		if got := c.d.LimitOffset(c.limit, c.offset); got != c.want {
			t.Errorf("%s LimitOffset(%d,%d) = %q, want %q", c.d.Name(), c.limit, c.offset, got, c.want)
		}
	}
}

func TestPlaceholdersAndQuoting(t *testing.T) {
	if postgres.Dialect.Placeholder(3) != "$3" || mysql.Dialect.Placeholder(3) != "?" || sqlite.Dialect.Placeholder(3) != "?" {
		t.Error("placeholder styles wrong")
	}
	if got := postgres.Dialect.Quote(`we"ird`); got != `"we""ird"` {
		t.Errorf("postgres quote: %s", got)
	}
	if got := mysql.Dialect.Quote("we`ird"); got != "`we``ird`" {
		t.Errorf("mysql quote: %s", got)
	}
}

func TestMySQLNormalizeDSN(t *testing.T) {
	cases := map[string]string{
		"u:p@tcp(h:3306)/db":                       "u:p@tcp(h:3306)/db?clientFoundRows=true&parseTime=true",
		"u:p@tcp(h:3306)/db?parseTime=false":       "u:p@tcp(h:3306)/db?clientFoundRows=true&parseTime=false",
		"u:p/w@tcp(h)/db?loc=UTC":                  "u:p/w@tcp(h)/db?clientFoundRows=true&loc=UTC&parseTime=true",
		"mysql://u:p@db.example.com/shop":          "u:p@tcp(db.example.com:3306)/shop?clientFoundRows=true&parseTime=true",
		"mysql://u:p@db.example.com:3307/shop?a=b": "u:p@tcp(db.example.com:3307)/shop?a=b&clientFoundRows=true&parseTime=true",
	}
	for in, want := range cases {
		got, err := mysql.NormalizeDSN(in)
		if err != nil || got != want {
			t.Errorf("NormalizeDSN(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := mysql.NormalizeDSN("nodatabase"); err == nil {
		t.Error("a DSN without a database must fail")
	}
}

func TestSQLiteDSN(t *testing.T) {
	if got := sqlite.DSN("sqlite3", "app.db"); got != "app.db?_foreign_keys=1&_busy_timeout=5000&_journal_mode=WAL" {
		t.Errorf("mattn dsn: %s", got)
	}
	if got := sqlite.DSN("sqlite", "file:app.db?cache=shared"); got != "file:app.db?cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" {
		t.Errorf("modernc dsn: %s", got)
	}
	if got := sqlite.DSN("sqlite", ":memory:"); got != ":memory:?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" {
		t.Errorf("memory dsn: %s", got)
	}
}

func TestOpenWithoutDriverExplainsWhatToImport(t *testing.T) {
	// no driver is linked into this test binary
	_, err := postgres.Open("postgres://x")
	if err == nil {
		t.Fatal("want an error when no driver is registered")
	}
	if msg := err.Error(); !contains(msg, "pgx") || !contains(msg, "import") {
		t.Errorf("error should say what to import: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
