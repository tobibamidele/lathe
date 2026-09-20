package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/mysql"
	"github.com/tobibamidele/lathe/postgres"
	"github.com/tobibamidele/lathe/sqlite"
)

func dialectName() string { return os.Getenv("LATHE_DIALECT") }

// openDB connects to the database selected by LATHE_DIALECT and DATABASE_URL.
func openDB(t testing.TB, opts ...lathe.Option) *lathe.DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if dialectName() == "" || url == "" {
		t.Skip("set LATHE_DIALECT and DATABASE_URL (see integration/run.sh)")
	}
	var (
		conn *lathe.DB
		err  error
	)
	switch dialectName() {
	case "postgres":
		conn, err = postgres.Open(url, opts...)
	case "mysql":
		conn, err = mysql.Open(url, opts...)
	case "sqlite":
		conn, err = sqlite.Open(url, opts...)
	default:
		t.Fatalf("unknown dialect %q", dialectName())
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("cannot reach the database: %v", err)
	}
	return conn
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
