package gen_test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/tobibamidele/lathe/internal/gen"
	s "github.com/tobibamidele/lathe/schema"
)

func export(t *testing.T, tables ...*s.TableBuilder) *s.Export {
	t.Helper()
	exp, err := s.New(s.Config{Dialect: s.Postgres, Output: "internal/store"}, tables...).Export()
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func generate(t *testing.T, exp *s.Export) map[string]string {
	t.Helper()
	files, err := gen.Generate(exp)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		if _, err := parser.ParseFile(fset, f.Name, f.Content, parser.AllErrors); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", f.Name, err, f.Content)
		}
		if !strings.HasPrefix(string(f.Content), gen.Header) {
			t.Errorf("%s lacks the generated code header", f.Name)
		}
		out[f.Name] = string(f.Content)
	}
	return out
}

func TestGeneratedShape(t *testing.T) {
	files := generate(t, export(t,
		s.Table("user_sessions",
			s.UUID("id").PrimaryKey().DefaultUUID(),
			s.BigInt("user_id"),
			s.Text("ip_address").Nullable(),
			s.Enum("kind", "web", "mobile-app").Default("web"),
			s.Money("amount"),
			s.Bool("active").Default(true),
			s.Bool("flag").Nullable().Default(true),
			s.VarChar("note", 20).Default("n/a"),
			s.Int("count").Default(0),
			s.JSON("meta").Nullable(),
			s.Bytes("blob").Nullable(),
		),
	))
	src := files["user_sessions.gen.go"]
	for _, want := range []string{
		"package store",
		"type UserSession struct",
		"UserID    int64",
		"IPAddress *string",
		"Kind      UserSessionKind",
		"Amount    decimal.Decimal",
		"Meta      lathe.JSON",
		"Blob      []byte",
		"UserSessionKindMobileApp UserSessionKind = \"mobile-app\"",
		"type UserSessionColumns struct",
		"var UserSessions = UserSessionColumns{",
		"func (c *UserSessionClient) Get(ctx context.Context, id uuid.UUID)",
		"uuid.New()", // client-side default for DefaultUUID
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source lacks %q", want)
		}
	}
	// a constant default on a plain bool must not be applied for a zero value;
	// on a nullable one it must; enums and UUIDs are treated as "unset" when zero
	if strings.Contains(lineWith(src, `Column: "active"`), "HasDefault") {
		t.Error("non-nullable bool with a constant default must always be sent")
	}
	if !strings.Contains(lineWith(src, `Column: "flag"`), "HasDefault: true") {
		t.Error("nullable bool with a default should let the database apply it when nil")
	}
	if !strings.Contains(lineWith(src, `Column: "note"`), "HasDefault: true") {
		t.Error("a string with a constant default should be treated as unset when empty")
	}
	if strings.Contains(lineWith(src, `Column: "count"`), "HasDefault") {
		t.Error("a non-nullable number with a constant default must always be sent")
	}
	if !strings.Contains(lineWith(src, `Column: "kind"`), "HasDefault: true") {
		t.Error("enum with a default should be treated as unset when empty")
	}

	client := files["client.gen.go"]
	for _, want := range []string{"type Client struct", "UserSessions *UserSessionClient", "func NewClient(db *lathe.DB) *Client", "func (c *Client) Tx("} {
		if !strings.Contains(client, want) {
			t.Errorf("client lacks %q", want)
		}
	}
}

func lineWith(src, needle string) string {
	for _, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestCompositeKeyGet(t *testing.T) {
	files := generate(t, export(t, s.Table("memberships",
		s.Int("user_id").References("users", "id"), s.Int("team_id"), s.PrimaryKey("team_id", "user_id")),
		s.Table("users", s.Int("id").PrimaryKey())))
	if !strings.Contains(files["memberships.gen.go"], "Get(ctx context.Context, teamID int32, userID int32)") {
		t.Errorf("Get must take key columns in declared key order:\n%s", files["memberships.gen.go"])
	}
}

func TestNameCollisionsAreReported(t *testing.T) {
	cases := []struct {
		name   string
		tables []*s.TableBuilder
		want   string
	}{
		{"reserved model", []*s.TableBuilder{s.Table("clients", s.Int("id").PrimaryKey())}, "reserves"},
		{"reserved client field", []*s.TableBuilder{s.Table("tx", s.Int("id").PrimaryKey()).Model("Transaction")}, "collides with a Client method"},
		{"two tables one model", []*s.TableBuilder{
			s.Table("person", s.Int("id").PrimaryKey()),
			s.Table("people", s.Int("id").PrimaryKey()),
		}, "both generate"},
		{"two columns one field", []*s.TableBuilder{
			s.Table("t", s.Int("id").PrimaryKey(), s.Int("user_id"), s.Int("user__id")),
		}, "both become the field"},
		{"enum constants collide", []*s.TableBuilder{
			s.Table("t", s.Int("id").PrimaryKey(), s.Enum("k", "a-b", "a_b")),
		}, "both become"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := gen.Generate(export(t, c.tables...))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestModelOverride(t *testing.T) {
	files := generate(t, export(t, s.Table("clients", s.Int("id").PrimaryKey()).Model("Customer")))
	if !strings.Contains(files["clients.gen.go"], "type Customer struct") {
		t.Error("Model() override ignored")
	}
}

func TestSingularTableNameDoesNotCollideWithItsColumnsVar(t *testing.T) {
	// "sheep" is its own singular; the column namespace gets a suffix
	files := generate(t, export(t, s.Table("sheep", s.Int("id").PrimaryKey())))
	src := files["sheep.gen.go"]
	if !strings.Contains(src, "type Sheep struct") || !strings.Contains(src, "var SheepTable = SheepColumns{") {
		t.Errorf("unexpected naming:\n%s", src)
	}
}

func TestEmptySchemaProducesNothing(t *testing.T) {
	files, err := gen.Generate(export(t))
	if err != nil || len(files) != 0 {
		t.Errorf("got %d files, err %v", len(files), err)
	}
}

func TestTableNamedLikeAClientMethodGetsASuffix(t *testing.T) {
	files := generate(t, export(t, s.Table("tx", s.Int("id").PrimaryKey())))
	if !strings.Contains(files["client.gen.go"], "TxTable *TxClient") {
		t.Errorf("expected a suffixed Client field:\n%s", files["client.gen.go"])
	}
}
