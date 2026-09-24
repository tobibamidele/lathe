package schema_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

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

// Package-level functions used by the DefaultFunc tests. They must be exported:
// DefaultFunc records their fully qualified name so generated code can call
// them across the schema-package boundary.
func GenSlug() string   { return "hello" }
func GenSeq() int64     { return 42 }
func GenUID() uuid.UUID { return uuid.MustParse("00000000-0000-0000-0000-000000000001") }
func GenPrice() decimal.Decimal {
	return decimal.NewFromInt(15).Div(decimal.NewFromInt(10))
}
func GenActive() bool    { return true }
func GenRole() string    { return "member" }
func GenMeta() string    { return `{"ok":true}` }
func GenAt() time.Time   { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
func GenBytes() []byte   { return []byte("x") }
func generateID() string { return "x" }

type idGen struct{}

func (idGen) Next() string { return "x" }

func TestDefaultFuncNamesFunction(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.Postgres},
		s.Table("t",
			s.Text("slug").PrimaryKey().DefaultFunc(GenSlug),
			s.BigInt("seq").DefaultFunc(GenSeq),
			s.UUID("uid").DefaultFunc(GenUID),
			s.Decimal("price", 10, 2).DefaultFunc(GenPrice),
			s.Bool("active").DefaultFunc(GenActive),
			s.Enum("role", "admin", "member").DefaultFunc(GenRole),
			s.JSON("meta").DefaultFunc(GenMeta),
			s.Timestamp("at").DefaultFunc(GenAt),
			s.Bytes("bin").DefaultFunc(GenBytes),
		)).Export()
	if err != nil {
		t.Fatal(err)
	}
	for col, fn := range map[string]string{
		"slug":   "GenSlug",
		"seq":    "GenSeq",
		"uid":    "GenUID",
		"price":  "GenPrice",
		"active": "GenActive",
		"role":   "GenRole",
		"meta":   "GenMeta",
		"at":     "GenAt",
		"bin":    "GenBytes",
	} {
		c := exp.Snapshot.Table("t").Column(col)
		if c.Default != nil {
			t.Errorf("column %q: DefaultFunc must not bake a server-side default, got %+v", col, c.Default)
		}
		if c.DefaultFunc == "" {
			t.Errorf("column %q: DefaultFunc did not record a function name", col)
			continue
		}
		if !strings.HasSuffix(c.DefaultFunc, "."+fn) {
			t.Errorf("column %q: DefaultFunc recorded %q, want it to end in %q", col, c.DefaultFunc, fn)
		}
	}
}

func TestDefaultFuncLastWriteWins(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.SQLite},
		s.Table("t",
			s.Text("a").PrimaryKey().Default("x").DefaultFunc(GenSlug),
			s.Text("b").DefaultFunc(GenSlug).Default("x"),
			s.Timestamp("c").DefaultFunc(GenAt).DefaultNow(),
			s.UUID("d").DefaultFunc(GenUID).DefaultUUID(),
			s.Text("e").DefaultFunc(GenSlug).DefaultExpr("upper(gen)"),
		)).Export()
	if err != nil {
		t.Fatal(err)
	}
	if got := exp.Snapshot.Table("t").Column("a"); got.Default != nil || !strings.HasSuffix(got.DefaultFunc, ".GenSlug") {
		t.Errorf("Default then DefaultFunc: want DefaultFunc to win, got Default=%+v DefaultFunc=%q", got.Default, got.DefaultFunc)
	}
	for _, col := range []string{"b", "c", "d", "e"} {
		got := exp.Snapshot.Table("t").Column(col)
		if got.DefaultFunc != "" {
			t.Errorf("column %q: server default after DefaultFunc should clear it, got DefaultFunc=%q Default=%+v", col, got.DefaultFunc, got.Default)
		}
	}
}

func TestDefaultFuncRejectsAnonymous(t *testing.T) {
	cases := []struct {
		name string
		say  func() *s.ColumnBuilder[string]
		want string
	}{
		{"closure", func() *s.ColumnBuilder[string] {
			return s.Text("id").PrimaryKey().DefaultFunc(func() string { return "x" })
		}, "closure"},
		{"unexported", func() *s.ColumnBuilder[string] { return s.Text("id").PrimaryKey().DefaultFunc(generateID) }, "not exported"},
		{"method value", func() *s.ColumnBuilder[string] { g := idGen{}; return s.Text("id").PrimaryKey().DefaultFunc(g.Next) }, "method value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.New(s.Config{Dialect: s.SQLite}, s.Table("t", tc.say())).Export()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDefaultFuncRoundTrip(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.SQLite},
		s.Table("t", s.Text("id").PrimaryKey().DefaultFunc(GenSlug)),
	).Export()
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
	if got := back.Table("t").Column("id").DefaultFunc; !strings.HasSuffix(got, ".GenSlug") {
		t.Errorf("DefaultFunc %q lost after round trip", got)
	}
}

func TestDefaultFuncValidation(t *testing.T) {
	exp, err := s.New(s.Config{Dialect: s.SQLite},
		s.Table("t", s.Text("id").PrimaryKey().DefaultFunc(GenSlug)),
	).Export()
	if err != nil {
		t.Fatal(err)
	}
	// a hand-edited snapshot that contradicts the builder's last-write-wins rule
	exp.Snapshot.Table("t").Column("id").Default = &s.Default{Kind: s.DefaultLiteral, Lit: s.LitString, Value: "x"}
	if err := s.Validate(exp.Snapshot); err == nil || !strings.Contains(err.Error(), "both a Default and a DefaultFunc") {
		t.Fatalf("want 'both a Default and a DefaultFunc', got %v", err)
	}
	exp.Snapshot.Table("t").Column("id").Default = nil
	exp.Snapshot.Table("t").Column("id").DefaultFunc = "example.com/schema.lowercase"
	if err := s.Validate(exp.Snapshot); err == nil || !strings.Contains(err.Error(), "exported") {
		t.Fatalf("want exported-name error, got %v", err)
	}
	exp.Snapshot.Table("t").Column("id").DefaultFunc = "not-fully-qualified"
	if err := s.Validate(exp.Snapshot); err == nil || !strings.Contains(err.Error(), "fully qualified") {
		t.Fatalf("want fully-qualified error, got %v", err)
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
