package migrate_test

import (
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/tobibamidele/lathe/migrate"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		backslash bool
		want      []string
	}{
		{"simple", "select 1; select 2;", false, []string{"select 1", "select 2"}},
		{"no trailing semicolon", "select 1", false, []string{"select 1"}},
		{"semicolon in string", "insert into t values ('a;b'); select 2", false, []string{"insert into t values ('a;b')", "select 2"}},
		{"doubled quote", "select 'it''s; ok'; select 2", false, []string{"select 'it''s; ok'", "select 2"}},
		{"identifier", `select "a;b" from t; select 2`, false, []string{`select "a;b" from t`, "select 2"}},
		{"backtick", "select `a;b` from t; select 2", true, []string{"select `a;b` from t", "select 2"}},
		{"line comment", "select 1; -- trailing; comment\nselect 2", false, []string{"select 1", "-- trailing; comment\nselect 2"}},
		{"comment only statement dropped", "-- nothing here\n;select 1", false, []string{"select 1"}},
		{"block comment", "/* a; b */ select 1; select 2", false, []string{"/* a; b */ select 1", "select 2"}},
		{"dollar quoting", "create function f() returns int as $$ begin; return 1; end $$ language plpgsql; select 2", false,
			[]string{"create function f() returns int as $$ begin; return 1; end $$ language plpgsql", "select 2"}},
		{"tagged dollar quoting", "select $q$ a;b $q$; select 2", false, []string{"select $q$ a;b $q$", "select 2"}},
		{"mysql backslash escape", `select 'a\';b'; select 2`, true, []string{`select 'a\';b'`, "select 2"}},
		{"postgres positional param is not a tag", "select $1; select $2", false, []string{"select $1", "select $2"}},
		{"empty", "  \n ; ; ", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := migrate.SplitStatements(c.in, c.backslash)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"20260102000000_second.up.sql":   {Data: []byte("create table b (id int)")},
		"20260102000000_second.down.sql": {Data: []byte("drop table b")},
		"20260101000000_first.up.sql":    {Data: []byte("create table a (id int)")},
		"README.md":                      {Data: []byte("ignored")},
		"snapshot.json":                  {Data: []byte("{}")},
		"9_short.up.sql":                 {Data: []byte("select 1")},
	}
	got, err := migrate.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range got {
		order = append(order, m.Version)
	}
	// numeric ordering: a short version number sorts before a timestamp
	if !reflect.DeepEqual(order, []string{"9", "20260101000000", "20260102000000"}) {
		t.Errorf("order = %v", order)
	}
	if got[2].Down != "drop table b" || got[1].Down != "" {
		t.Errorf("down files not paired correctly: %+v", got)
	}
	if got[1].Checksum == got[2].Checksum || len(got[1].Checksum) != 64 {
		t.Error("checksums missing or identical")
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := migrate.Load(fstest.MapFS{"1_x.down.sql": {Data: []byte("x")}}); err == nil {
		t.Error("orphan down file must fail")
	}
	if _, err := migrate.Load(fstest.MapFS{
		"1_a.up.sql": {Data: []byte("x")},
		"1_b.up.sql": {Data: []byte("x")},
	}); err == nil {
		t.Error("duplicate version must fail")
	}
}
