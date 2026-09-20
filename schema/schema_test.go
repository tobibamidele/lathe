package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	s "github.com/tobibamidele/lathe/schema"
)

func blog() *s.Schema {
	return s.New(s.Config{Dialect: s.Postgres},
		s.Table("users",
			s.BigInt("id").PrimaryKey().AutoIncrement(),
			s.VarChar("email", 255).Unique(),
			s.Text("bio").Nullable(),
			s.Enum("role", "admin", "member").Default("member"),
			s.Money("balance").Default("0"),
			s.Timestamp("created_at").DefaultNow(),
		),
		s.Table("posts",
			s.UUID("id").PrimaryKey().DefaultUUID(),
			s.BigInt("author_id").References("users", "id").OnDelete(s.Cascade).Index(),
			s.Text("body"),
		),
	)
}

func TestExportNormalises(t *testing.T) {
	exp, err := blog().Export()
	if err != nil {
		t.Fatal(err)
	}
	if exp.Config.Output != "db" || exp.Config.Migrations != "migrations" || exp.Config.Package != "db" {
		t.Errorf("config defaults not applied: %+v", exp.Config)
	}
	posts := exp.Snapshot.Table("posts")
	users := exp.Snapshot.Table("users")
	if posts == nil || users == nil {
		t.Fatal("missing tables")
	}
	if exp.Snapshot.Tables[0].Name != "posts" {
		t.Errorf("tables not sorted by name: %s first", exp.Snapshot.Tables[0].Name)
	}
	if users.Column("bio").Nullable != true || users.Column("email").Nullable {
		t.Error("nullability wrong: columns must be NOT NULL unless Nullable() is used")
	}
	if got := users.Index("uq_users_email"); got == nil || !got.Unique {
		t.Errorf("Unique() should create uq_users_email, got %+v", users.Indexes)
	}
	fk := posts.ForeignKey("fk_posts_author_id")
	if fk == nil || fk.RefTable != "users" || fk.OnDelete != s.Cascade {
		t.Errorf("unexpected fk %+v", fk)
	}
	if posts.Index("idx_posts_author_id") == nil {
		t.Error("Index() should create idx_posts_author_id")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	exp, err := blog().Export()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(exp.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var back s.Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(&back)
	if string(b) != string(b2) {
		t.Errorf("snapshot changed after round trip:\n%s\n%s", b, b2)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		sch  *s.Schema
		want string
	}{
		{"bad dialect", s.New(s.Config{Dialect: "oracle"}), "unsupported dialect"},
		{"no pk", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Text("a"))), "no primary key"},
		{"dup column", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Text("a").PrimaryKey(), s.Text("a"))), "more than once"},
		{"autoinc on text", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Text("a").PrimaryKey().AutoIncrement())), "integer"},
		{"autoinc composite", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("a").AutoIncrement(), s.Int("b"), s.PrimaryKey("a", "b"))), "only primary key"},
		{"unknown fk table", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("a").PrimaryKey().References("nope", "id"))), "unknown table"},
		{"fk type mismatch", s.New(s.Config{Dialect: s.SQLite},
			s.Table("a", s.Int("id").PrimaryKey()),
			s.Table("b", s.Text("id").PrimaryKey().References("a", "id"))), "is text but"},
		{"fk to non-key", s.New(s.Config{Dialect: s.SQLite},
			s.Table("a", s.Int("id").PrimaryKey(), s.Int("x")),
			s.Table("b", s.Int("id").PrimaryKey().References("a", "x"))), "primary key or a unique index"},
		{"bad default", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("a").PrimaryKey().Default("abc"))), "not a number"},
		{"enum default", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("id").PrimaryKey(), s.Enum("e", "a").Default("z"))), "not one of"},
		{"now on int", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("a").PrimaryKey().DefaultNow())), "Timestamp or Date"},
		{"decimal scale", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("id").PrimaryKey(), s.Decimal("d", 4, 5))), "scale"},
		{"index unknown col", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("id").PrimaryKey(), s.Index("zzz"))), "unknown column"},
		{"ondelete without ref", s.New(s.Config{Dialect: s.SQLite}, s.Table("t", s.Int("id").PrimaryKey().OnDelete(s.Cascade))), "requires References"},
		{"bad ident", s.New(s.Config{Dialect: s.SQLite}, s.Table("my table", s.Int("id").PrimaryKey())), "invalid name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.sch.Export()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAutoNameFitsIdentifierLimit(t *testing.T) {
	long := strings.Repeat("x", 40)
	exp, err := s.New(s.Config{Dialect: s.Postgres},
		s.Table("t",
			s.Int("id").PrimaryKey(),
			s.Int(long+"_a"),
			s.Int(long+"_b"),
			s.Index(long+"_a", long+"_b"),
		)).Export()
	if err != nil {
		t.Fatal(err)
	}
	for _, ix := range exp.Snapshot.Table("t").Indexes {
		if len(ix.Name) > 63 {
			t.Errorf("index name %q is %d bytes", ix.Name, len(ix.Name))
		}
	}
}

func TestStripHints(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.SQLite},
		s.Table("people", s.Int("id").PrimaryKey(), s.Text("full_name").RenamedFrom("name")).RenamedFrom("users")).Export()
	if err != nil {
		t.Fatal(err)
	}
	if exp.Snapshot.Tables[0].RenamedFrom != "users" {
		t.Fatal("hint should survive export")
	}
	clean := exp.Snapshot.StripHints()
	if clean.Tables[0].RenamedFrom != "" || clean.Tables[0].Column("full_name").RenamedFrom != "" {
		t.Error("StripHints left hints behind")
	}
	if exp.Snapshot.Tables[0].RenamedFrom != "users" {
		t.Error("StripHints must not mutate the original")
	}
}

func TestCompositeForeignKey(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.Postgres},
		s.Table("orgs", s.Int("tenant").PrimaryKey(), s.Int("id").PrimaryKey()),
		s.Table("members",
			s.Int("id").PrimaryKey(),
			s.Int("org_tenant"), s.Int("org_id"),
			s.ForeignKey("org_tenant", "org_id").References("orgs", "tenant", "id").OnDelete(s.Cascade),
		),
	).Export()
	if err != nil {
		t.Fatal(err)
	}
	fk := exp.Snapshot.Table("members").ForeignKey("fk_members_org_tenant_org_id")
	if fk == nil || len(fk.Columns) != 2 || fk.RefTable != "orgs" || fk.OnDelete != s.Cascade {
		t.Fatalf("composite fk: %+v", exp.Snapshot.Table("members").ForeignKeys)
	}

	// a foreign key whose column counts differ, or that lacks References, is rejected
	_, err = s.New(s.Config{Dialect: s.Postgres},
		s.Table("orgs", s.Int("id").PrimaryKey()),
		s.Table("m", s.Int("id").PrimaryKey(), s.Int("a"), s.Int("b"), s.ForeignKey("a", "b").References("orgs", "id")),
	).Export()
	if err == nil || !strings.Contains(err.Error(), "same, non-zero number") {
		t.Errorf("mismatched column counts: %v", err)
	}
	_, err = s.New(s.Config{Dialect: s.Postgres},
		s.Table("m", s.Int("id").PrimaryKey(), s.Int("a"), s.ForeignKey("a")),
	).Export()
	if err == nil || !strings.Contains(err.Error(), "no References") {
		t.Errorf("missing References: %v", err)
	}
}
