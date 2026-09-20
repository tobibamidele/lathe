package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tobibamidele/lathe/migrate"
)

func migrationsFS(t *testing.T) fstest.MapFS {
	t.Helper()
	dir := os.DirFS("migrations/" + dialectName())
	files, err := migrate.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, m := range files {
		fsys[m.Version+"_"+m.Name+".up.sql"] = &fstest.MapFile{Data: []byte(m.Up)}
		if m.Down != "" {
			fsys[m.Version+"_"+m.Name+".down.sql"] = &fstest.MapFile{Data: []byte(m.Down)}
		}
	}
	return fsys
}

// The CLI applied the migrations before the tests started.
func TestMigrationsAreApplied(t *testing.T) {
	conn := openDB(t)
	states, err := migrate.Status(context.Background(), conn, migrationsFS(t))
	must(t, err)
	if len(states) == 0 {
		t.Fatal("no migrations found")
	}
	for _, st := range states {
		if !st.Applied || st.Modified || st.Missing {
			t.Errorf("unexpected state: %+v", st)
		}
	}
}

func TestUpIsIdempotent(t *testing.T) {
	conn := openDB(t)
	applied, err := migrate.Up(context.Background(), conn, migrationsFS(t))
	must(t, err)
	if len(applied) != 0 {
		t.Errorf("nothing should be pending, applied %d", len(applied))
	}
}

func TestEditedMigrationIsRejected(t *testing.T) {
	conn := openDB(t)
	fsys := migrationsFS(t)
	for name, f := range fsys {
		if strings.HasSuffix(name, ".up.sql") {
			f.Data = append([]byte("-- tampered\n"), f.Data...)
			break
		}
	}
	_, err := migrate.Up(context.Background(), conn, fsys)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want a checksum error, got %v", err)
	}
}

func TestDownWithoutFileFails(t *testing.T) {
	conn := openDB(t)
	fsys := migrationsFS(t)
	for name := range fsys {
		if strings.HasSuffix(name, ".down.sql") {
			delete(fsys, name)
		}
	}
	if _, err := migrate.Down(context.Background(), conn, fsys, 1); err == nil {
		t.Fatal("reverting without a down file must fail")
	}
}
