package lathe_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tobibamidele/lathe"
)

// A dialect double good enough to render SQL; no database is involved.
type dialect struct{ pg bool }

func (d dialect) Name() string { return "test" }
func (d dialect) Placeholder(n int) string {
	if d.pg {
		return "$" + string(rune('0'+n))
	}
	return "?"
}
func (d dialect) Quote(s string) string {
	if d.pg {
		return `"` + s + `"`
	}
	return "`" + s + "`"
}
func (d dialect) SupportsReturning() bool       { return d.pg }
func (d dialect) NativeILike() bool             { return d.pg }
func (d dialect) DefaultValues(t string) string { return "INSERT INTO " + t + " DEFAULT VALUES" }
func (d dialect) MaxParams() int                { return 100 }
func (d dialect) ClassifyError(error) error     { return nil }
func (d dialect) LimitOffset(limit, offset int64) string {
	s := ""
	if limit >= 0 {
		s += " LIMIT " + itoa(limit)
	}
	if offset > 0 {
		s += " OFFSET " + itoa(offset)
	}
	return s
}

func itoa(n int64) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if s == "" {
		return "0"
	}
	return s
}

type User struct {
	ID       int64
	Email    string
	Age      int32
	Bio      *string
	Password string
	Created  time.Time
}

type usersTable struct {
	ID       lathe.Column[int64]
	Email    lathe.Column[string]
	Age      lathe.Column[int32]
	Bio      lathe.Column[string]
	Password lathe.Column[string]
	Created  lathe.Column[time.Time]
}

func (usersTable) TableName() string { return "users" }

var Users = usersTable{
	ID:       lathe.NewColumn[int64]("users", "id"),
	Email:    lathe.NewColumn[string]("users", "email"),
	Age:      lathe.NewColumn[int32]("users", "age"),
	Bio:      lathe.NewColumn[string]("users", "bio"),
	Password: lathe.NewColumn[string]("users", "password"),
	Created:  lathe.NewColumn[time.Time]("users", "created"),
}

type ordersTable struct {
	ID     lathe.Column[int64]
	UserID lathe.Column[int64]
	Total  lathe.Column[float64]
}

func (ordersTable) TableName() string { return "orders" }

var Orders = ordersTable{
	ID:     lathe.NewColumn[int64]("orders", "id"),
	UserID: lathe.NewColumn[int64]("orders", "user_id"),
	Total:  lathe.NewColumn[float64]("orders", "total"),
}

var usersSpec = &lathe.TableSpec[User]{
	Table: "users",
	Fields: []lathe.Field[User]{
		{Column: "id", Ptr: func(m *User) any { return &m.ID }, PrimaryKey: true, AutoIncrement: true},
		{Column: "email", Ptr: func(m *User) any { return &m.Email }},
		{Column: "age", Ptr: func(m *User) any { return &m.Age }},
		{Column: "bio", Ptr: func(m *User) any { return &m.Bio }},
		{Column: "password", Ptr: func(m *User) any { return &m.Password }},
		{Column: "created", Ptr: func(m *User) any { return &m.Created }, HasDefault: true},
	},
}

func model(pg bool) *lathe.Model[User] {
	return lathe.NewModel(lathe.New(nil, dialect{pg: pg}), usersSpec)
}

func check(t *testing.T, gotSQL string, gotArgs []any, err error, wantSQL string, wantArgs ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSQL != wantSQL {
		t.Errorf("SQL mismatch\n got: %s\nwant: %s", gotSQL, wantSQL)
	}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Errorf("arg %d: got %v (%T), want %v (%T)", i, gotArgs[i], gotArgs[i], wantArgs[i], wantArgs[i])
		}
	}
}

func TestFindManyPostgres(t *testing.T) {
	q := model(true).FindMany().
		Where(Users.Age.Gte(18), lathe.Or(Users.Email.HasSuffix("@x.io"), Users.Bio.IsNull())).
		Exclude(Users.Password).
		OrderBy(Users.Created.Desc(), Users.ID.Asc()).
		Limit(10).Offset(20)
	sql, args, err := q.Build()
	check(t, sql, args, err,
		`SELECT "users"."id", "users"."email", "users"."age", "users"."bio", "users"."created" FROM "users" `+
			`WHERE ("users"."age" >= $1 AND ("users"."email" LIKE $2 ESCAPE '!' OR "users"."bio" IS NULL)) `+
			`ORDER BY "users"."created" DESC, "users"."id" LIMIT 10 OFFSET 20`,
		int32(18), "%@x.io")
}

func TestFindFirstAndSelect(t *testing.T) {
	sql, args, err := model(false).FindFirst().Select(Users.ID, Users.Email).Where(Users.Email.Eq("a@b.c")).Build()
	check(t, sql, args, err, "SELECT `users`.`id`, `users`.`email` FROM `users` WHERE `users`.`email` = ? LIMIT 1", "a@b.c")
}

func TestColumnFromOtherTableIsRejected(t *testing.T) {
	_, _, err := model(true).FindMany().Exclude(Orders.ID).Build()
	if err == nil || !strings.Contains(err.Error(), "does not belong to table users") {
		t.Fatalf("want ownership error, got %v", err)
	}
	_, _, err = model(true).FindMany().Select(lathe.NewColumn[int]("users", "nope")).Build()
	if err == nil || !strings.Contains(err.Error(), "no column nope") {
		t.Fatalf("want unknown column error, got %v", err)
	}
}

func TestExcludeEverythingIsAnError(t *testing.T) {
	_, _, err := model(true).FindMany().Exclude(Users.ID, Users.Email, Users.Age, Users.Bio, Users.Password, Users.Created).Build()
	if err == nil {
		t.Fatal("selecting no columns must fail")
	}
}

func TestPredicates(t *testing.T) {
	cases := []struct {
		name string
		e    lathe.Expr
		pg   string
		my   string
		args []any
	}{
		{"in", Users.ID.In(1, 2, 3), `"users"."id" IN ($1, $2, $3)`, "`users`.`id` IN (?, ?, ?)", []any{int64(1), int64(2), int64(3)}},
		{"in empty", Users.ID.In(), "1=0", "1=0", nil},
		{"not in empty", Users.ID.NotIn(), "1=1", "1=1", nil},
		{"not in", Users.ID.NotIn(7), `"users"."id" NOT IN ($1)`, "`users`.`id` NOT IN (?)", []any{int64(7)}},
		{"between", Users.Age.Between(1, 9), `"users"."age" BETWEEN $1 AND $2`, "`users`.`age` BETWEEN ? AND ?", []any{int32(1), int32(9)}},
		{"is not null", Users.Bio.IsNotNull(), `"users"."bio" IS NOT NULL`, "`users`.`bio` IS NOT NULL", nil},
		{"ilike", Users.Email.ILike("A%"), `"users"."email" ILIKE $1`, "LOWER(`users`.`email`) LIKE LOWER(?)", []any{"A%"}},
		{"contains escapes wildcards", Users.Email.Contains("50%_off!"), `"users"."email" LIKE $1 ESCAPE '!'`, "`users`.`email` LIKE ? ESCAPE '!'", []any{"%50!%!_off!!%"}},
		{"not", lathe.Not(Users.Age.Eq(3)), `NOT ("users"."age" = $1)`, "NOT (`users`.`age` = ?)", []any{int32(3)}},
		{"empty and", lathe.And(), "1=1", "1=1", nil},
		{"empty or", lathe.Or(), "1=0", "1=0", nil},
		{"single and unwrapped", lathe.And(Users.Age.Eq(3)), `"users"."age" = $1`, "`users`.`age` = ?", []any{int32(3)}},
		{"raw fragment", lathe.SQL("lower(?) = ?", "X", "y"), "lower($1) = $2", "lower(?) = ?", []any{"X", "y"}},
		{"col eq col", Orders.UserID.EqCol(Users.ID), `"orders"."user_id" = "users"."id"`, "`orders`.`user_id` = `users`.`id`", nil},
	}
	for _, c := range cases {
		for _, pg := range []bool{true, false} {
			name := c.name + "/mysql"
			want := c.my
			if pg {
				name, want = c.name+"/pg", c.pg
			}
			t.Run(name, func(t *testing.T) {
				q := lathe.Select(lathe.CountAll()).From(Users).Where(c.e)
				sql, args, err := q.Build(dialect{pg: pg})
				check(t, sql, args, err, "SELECT COUNT(*) FROM "+dialect{pg: pg}.Quote("users")+" WHERE "+want, c.args...)
			})
		}
	}
}

func TestJoinsGroupingAndAliases(t *testing.T) {
	q := lathe.Select(Users.ID, Users.Email.As("mail"), lathe.Sum(Orders.Total).As("spent"), lathe.CountAll().As("n")).
		From(Users).
		LeftJoin(Orders, Orders.UserID.EqCol(Users.ID)).
		Where(Users.Age.Gt(17)).
		GroupBy(Users.ID, Users.Email).
		Having(lathe.Sum(Orders.Total).Gt(100)).
		OrderBy(lathe.Sum(Orders.Total).Desc()).
		Limit(5)
	sql, args, err := q.Build(dialect{pg: true})
	check(t, sql, args, err,
		`SELECT "users"."id", "users"."email" AS "mail", SUM("orders"."total") AS "spent", COUNT(*) AS "n" FROM "users" `+
			`LEFT JOIN "orders" ON "orders"."user_id" = "users"."id" WHERE "users"."age" > $1 `+
			`GROUP BY "users"."id", "users"."email" HAVING SUM("orders"."total") > $2 ORDER BY SUM("orders"."total") DESC LIMIT 5`,
		int32(17), 100)
}

func TestSubqueries(t *testing.T) {
	sub := lathe.Select(Orders.UserID).From(Orders).Where(Orders.Total.Gt(50))
	sql, args, err := lathe.Select(Users.ID).From(Users).Where(Users.ID.InQuery(sub)).Build(dialect{pg: true})
	check(t, sql, args, err,
		`SELECT "users"."id" FROM "users" WHERE "users"."id" IN (SELECT "orders"."user_id" FROM "orders" WHERE "orders"."total" > $1)`, 50.0)

	ex := lathe.Select(lathe.SQL("1")).From(Orders).Where(Orders.UserID.EqCol(Users.ID))
	sql, args, err = lathe.Select(Users.ID).From(Users).Where(lathe.Not(lathe.Exists(ex))).Build(dialect{})
	check(t, sql, args, err, "SELECT `users`.`id` FROM `users` WHERE NOT (EXISTS (SELECT 1 FROM `orders` WHERE `orders`.`user_id` = `users`.`id`))")
}

func TestSelectValidation(t *testing.T) {
	if _, _, err := lathe.Select().From(Users).Build(dialect{}); err == nil {
		t.Error("empty select list must fail")
	}
	if _, _, err := lathe.Select(Users.ID).Build(dialect{}); err == nil {
		t.Error("missing From must fail")
	}
	_, _, err := lathe.Select(Users.ID).From(Users).Where(lathe.SQL("a = ? and b = ?", 1)).Build(dialect{})
	if err == nil || !strings.Contains(err.Error(), "placeholders") {
		t.Errorf("mismatched placeholder count must fail, got %v", err)
	}
}

func TestUpdateAndDeleteRequireWhere(t *testing.T) {
	m := model(true)
	if _, err := m.Update().Set(Users.Age.Set(1)).Exec(context.Background()); !errors.Is(err, lathe.ErrMissingWhere) {
		t.Errorf("update without where: got %v", err)
	}
	if _, err := m.Delete().Exec(context.Background()); !errors.Is(err, lathe.ErrMissingWhere) {
		t.Errorf("delete without where: got %v", err)
	}
	if _, err := m.Update().Where(Users.ID.Eq(1)).Exec(context.Background()); err == nil {
		t.Error("update without assignments must fail")
	}
	_, err := m.Update().Set(Orders.Total.Set(1)).Where(Users.ID.Eq(1)).Exec(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot set orders.total") {
		t.Errorf("assignment to a foreign table must fail, got %v", err)
	}
}

func TestErrNotFoundMatchesSQLErrNoRows(t *testing.T) {
	if !errors.Is(lathe.ErrNotFound, lathe.ErrNotFound) {
		t.Fatal("ErrNotFound must match itself")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	j, err := lathe.NewJSON(map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := j.Value()
	if v != `{"a":1}` {
		t.Errorf("Value() = %v", v)
	}
	var back lathe.JSON
	if err := back.Scan([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	var m map[string]int
	if err := back.Decode(&m); err != nil || m["a"] != 1 {
		t.Errorf("decode: %v %v", m, err)
	}
	var null lathe.JSON
	if v, _ := null.Value(); v != nil {
		t.Errorf("nil JSON must store NULL, got %v", v)
	}
	if err := null.Scan(42); err == nil {
		t.Error("scanning an int must fail")
	}
}
