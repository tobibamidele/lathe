// Package mysql connects lathe to MySQL and MariaDB.
//
// It works with github.com/go-sql-driver/mysql, imported for its side effect:
//
//	import _ "github.com/go-sql-driver/mysql"
//
// lathe itself does not depend on the driver, so you only download it if you
// use it.
package mysql

import (
	"database/sql"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/internal/driverpick"
)

// Dialect is the MySQL dialect.
var Dialect lathe.Dialect = dialect{}

// New wraps an open *sql.DB. The DSN must include parseTime=true (so DATETIME
// columns scan into time.Time) and clientFoundRows=true (so UPDATE reports
// matched rather than changed rows); Open adds both.
func New(db *sql.DB, opts ...lathe.Option) *lathe.DB { return lathe.New(db, Dialect, opts...) }

// Open connects using a go-sql-driver DSN ("user:pass@tcp(host:3306)/db") or a
// mysql:// URL. parseTime=true and clientFoundRows=true are added unless set.
// Like sql.Open it does not dial; call Ping to verify the connection.
func Open(dsn string, opts ...lathe.Option) (*lathe.DB, error) {
	name, err := driverpick.Pick("mysql", `_ "github.com/go-sql-driver/mysql"`, "mysql")
	if err != nil {
		return nil, err
	}
	norm, err := NormalizeDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(name, norm)
	if err != nil {
		return nil, err
	}
	return New(db, opts...), nil
}

// NormalizeDSN converts a mysql:// URL to go-sql-driver form and makes sure
// parseTime and clientFoundRows are enabled.
func NormalizeDSN(dsn string) (string, error) {
	if strings.HasPrefix(dsn, "mysql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		host := u.Host
		if !strings.Contains(host, ":") && host != "" {
			host += ":3306"
		}
		out := ""
		if u.User != nil {
			out = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				out += ":" + pw
			}
			out += "@"
		}
		out += "tcp(" + host + ")/" + strings.TrimPrefix(u.Path, "/")
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		dsn = out
	}

	slash := strings.LastIndex(dsn, "/")
	if slash < 0 {
		return "", errors.New("lathe/mysql: DSN has no database part (want user:pass@tcp(host:3306)/dbname)")
	}
	base, query := dsn, ""
	if q := strings.Index(dsn[slash:], "?"); q >= 0 {
		base, query = dsn[:slash+q], dsn[slash+q+1:]
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	if !vals.Has("parseTime") {
		vals.Set("parseTime", "true")
	}
	if !vals.Has("clientFoundRows") {
		vals.Set("clientFoundRows", "true")
	}
	return base + "?" + vals.Encode(), nil
}

type dialect struct{}

func (dialect) Name() string                  { return "mysql" }
func (dialect) Placeholder(int) string        { return "?" }
func (dialect) SupportsReturning() bool       { return false }
func (dialect) NativeILike() bool             { return false }
func (dialect) MaxParams() int                { return 65535 }
func (dialect) DefaultValues(t string) string { return "INSERT INTO " + t + " () VALUES ()" }

func (dialect) Quote(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func (dialect) LimitOffset(limit, offset int64) string {
	switch {
	case limit < 0 && offset > 0:
		// MySQL has no OFFSET without LIMIT; this is the documented workaround.
		return " LIMIT 18446744073709551615 OFFSET " + strconv.FormatInt(offset, 10)
	case limit < 0:
		return ""
	case offset > 0:
		return " LIMIT " + strconv.FormatInt(limit, 10) + " OFFSET " + strconv.FormatInt(offset, 10)
	}
	return " LIMIT " + strconv.FormatInt(limit, 10)
}

func (dialect) ClassifyError(err error) error {
	for e := err; e != nil; e = errors.Unwrap(e) {
		n, ok := mysqlErrorNumber(e)
		if !ok {
			continue
		}
		switch n {
		case 1062:
			return lathe.ErrUniqueViolation
		case 1451, 1452:
			return lathe.ErrForeignKeyViolation
		case 1048, 1364:
			return lathe.ErrNotNullViolation
		case 3819, 4025:
			return lathe.ErrCheckViolation
		}
		return nil
	}
	return nil
}

// mysqlErrorNumber reads Number from a driver *MySQLError without importing
// the driver.
func mysqlErrorNumber(err error) (uint64, bool) {
	v := reflect.ValueOf(err)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || v.Type().Name() != "MySQLError" {
		return 0, false
	}
	f := v.FieldByName("Number")
	if !f.IsValid() || !f.CanUint() {
		return 0, false
	}
	return f.Uint(), true
}
