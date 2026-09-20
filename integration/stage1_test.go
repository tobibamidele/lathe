//go:build stage1

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/integration/db"
)

var ctx = context.Background()

func setup(t *testing.T, opts ...lathe.Option) *db.Client {
	t.Helper()
	client := db.NewClient(openDB(t, opts...))
	for _, del := range []func() (int64, error){
		func() (int64, error) { return client.Members.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Orgs.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Messages.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Profiles.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.PostTags.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Posts.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Tags.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return client.Users.Delete().AllRows().Exec(ctx) },
	} {
		if _, err := del(); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

func newUser(email string) db.User {
	return db.User{Email: email, Name: "Name " + email, PasswordHash: "hash-" + email, Active: true}
}

func TestCreateFillsGeneratedColumns(t *testing.T) {
	client := setup(t)
	u := newUser("ann@x.io")
	u.Balance = decimal.RequireFromString("12345.6789")
	must(t, client.Users.Create(ctx, &u))

	if u.ID == 0 {
		t.Error("auto increment id was not read back")
	}
	if u.Role != db.UserRoleMember {
		t.Errorf("role default not applied: %q", u.Role)
	}
	if d := time.Since(u.CreatedAt); d < -time.Minute || d > time.Minute {
		t.Errorf("created_at default not applied/read back: %v", u.CreatedAt)
	}

	got, err := client.Users.Get(ctx, u.ID)
	must(t, err)
	if got.Email != "ann@x.io" || !got.Balance.Equal(decimal.RequireFromString("12345.6789")) || got.Bio != nil || !got.Active {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestBoolAndNumberZeroValuesAreNotSwallowedByDefaults(t *testing.T) {
	client := setup(t)
	u := newUser("inactive@x.io")
	u.Active = false // the column default is true; an explicit false must win
	must(t, client.Users.Create(ctx, &u))
	got, err := client.Users.Get(ctx, u.ID)
	must(t, err)
	if got.Active {
		t.Error("explicit false was replaced by the column default")
	}
}

func TestConstraintErrors(t *testing.T) {
	client := setup(t)
	u := newUser("dup@x.io")
	must(t, client.Users.Create(ctx, &u))

	dup := newUser("dup@x.io")
	if err := client.Users.Create(ctx, &dup); !errors.Is(err, lathe.ErrUniqueViolation) {
		t.Errorf("duplicate email: want ErrUniqueViolation, got %v", err)
	}

	orphan := db.Post{AuthorID: 999999, Title: "x"}
	if err := client.Posts.Create(ctx, &orphan); !errors.Is(err, lathe.ErrForeignKeyViolation) {
		t.Errorf("unknown author: want ErrForeignKeyViolation, got %v", err)
	}

	bad := newUser("bad-role@x.io")
	bad.Role = "bogus"
	err := client.Users.Create(ctx, &bad)
	switch dialectName() {
	case "mysql": // strict mode reports "Data truncated", which is not a CHECK error
		if err == nil {
			t.Error("invalid enum value was accepted")
		}
	default:
		if !errors.Is(err, lathe.ErrCheckViolation) {
			t.Errorf("invalid enum value: want ErrCheckViolation, got %v", err)
		}
	}

	nn := db.User{Email: "nn@x.io"} // name and password_hash are NOT NULL but "" is a value, so use raw SQL
	_ = nn
	_, err = client.Exec(ctx, "INSERT INTO users (email) VALUES (?)", "raw@x.io")
	if err == nil {
		t.Error("insert without required columns must fail")
	}
	if dialectName() != "mysql" && !errors.Is(err, lathe.ErrNotNullViolation) {
		t.Errorf("want ErrNotNullViolation, got %v", err)
	}
}

func seedUsers(t *testing.T, client *db.Client) map[string]*db.User {
	t.Helper()
	out := map[string]*db.User{}
	bio := "hello"
	age := func(n int32) *int32 { return &n }
	rows := []db.User{
		{Email: "alice@x.io", Name: "Alice", PasswordHash: "h1", Active: true, Age: age(31), Bio: &bio, Role: db.UserRoleAdmin},
		{Email: "bob@x.io", Name: "Bob", PasswordHash: "h2", Active: true, Age: age(25)},
		{Email: "carol@x.io", Name: "Carol", PasswordHash: "h3", Active: false, Age: age(40), Role: db.UserRoleGuest},
		{Email: "dave_50%@x.io", Name: "Dave", PasswordHash: "h4", Active: true},
		{Email: "eve@y.io", Name: "Eve", PasswordHash: "h5", Active: true, Age: age(19)},
	}
	for i := range rows {
		must(t, client.Users.Create(ctx, &rows[i]))
		out[rows[i].Name] = &rows[i]
	}
	return out
}

func emails(us []db.User) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.Email)
	}
	return out
}

func TestFindQueries(t *testing.T) {
	client := setup(t)
	seedUsers(t, client)

	check := func(name string, q *lathe.FindQuery[db.User], want ...string) {
		t.Helper()
		got, err := q.All(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fmt.Sprint(emails(got)) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, want %v", name, emails(got), want)
		}
	}
	byEmail := db.Users.Email.Asc()
	find := func() *lathe.FindQuery[db.User] { return client.Users.FindMany().OrderBy(byEmail) }

	check("eq", find().Where(db.Users.Email.Eq("bob@x.io")), "bob@x.io")
	check("gt+and", find().Where(db.Users.Age.Gt(20), db.Users.Active.Eq(true)), "alice@x.io", "bob@x.io")
	check("or", find().Where(lathe.Or(db.Users.Age.Lt(20), db.Users.Age.Gt(35))), "carol@x.io", "eve@y.io")
	check("not", find().Where(lathe.Not(db.Users.Active.Eq(true))), "carol@x.io")
	check("in", find().Where(db.Users.Email.In("bob@x.io", "eve@y.io", "nobody")), "bob@x.io", "eve@y.io")
	check("in empty", find().Where(db.Users.Email.In()))
	check("not in", find().Where(db.Users.Email.NotIn("bob@x.io", "eve@y.io")), "alice@x.io", "carol@x.io", "dave_50%@x.io")
	check("between", find().Where(db.Users.Age.Between(25, 31)), "alice@x.io", "bob@x.io")
	check("is null", find().Where(db.Users.Age.IsNull()), "dave_50%@x.io")
	check("is not null bio", find().Where(db.Users.Bio.IsNotNull()), "alice@x.io")
	check("has suffix", find().Where(db.Users.Email.HasSuffix("@x.io")), "alice@x.io", "bob@x.io", "carol@x.io", "dave_50%@x.io")
	check("has prefix", find().Where(db.Users.Email.HasPrefix("al")), "alice@x.io")
	check("contains treats wildcards literally", find().Where(db.Users.Email.Contains("50%")), "dave_50%@x.io")
	check("contains underscore literal", find().Where(db.Users.Email.Contains("e_5")), "dave_50%@x.io")
	check("ilike", find().Where(db.Users.Email.ILike("ALICE@%")), "alice@x.io")
	check("enum", find().Where(db.Users.Role.Eq(db.UserRoleAdmin)), "alice@x.io")
	check("order desc + limit + offset", client.Users.FindMany().OrderBy(db.Users.Email.Desc()).Limit(2).Offset(1), "dave_50%@x.io", "carol@x.io")
	check("offset without limit", client.Users.FindMany().OrderBy(byEmail).Offset(3), "dave_50%@x.io", "eve@y.io")
	check("raw fragment", find().Where(lathe.SQL("LOWER(email) = ?", "bob@x.io")), "bob@x.io")

	n, err := client.Users.Count(ctx, db.Users.Active.Eq(true))
	must(t, err)
	if n != 4 {
		t.Errorf("count = %d, want 4", n)
	}
	ok, err := client.Users.Exists(ctx, db.Users.Email.Eq("nobody"))
	must(t, err)
	if ok {
		t.Error("exists reported a phantom row")
	}
}

func TestExcludeAndSelect(t *testing.T) {
	client := setup(t)
	seedUsers(t, client)

	u, err := client.Users.FindFirst().Where(db.Users.Email.Eq("alice@x.io")).
		Exclude(db.Users.PasswordHash, db.Users.APIKey).One(ctx)
	must(t, err)
	if u.PasswordHash != "" || u.Email != "alice@x.io" || u.Name != "Alice" {
		t.Errorf("Exclude: %+v", u)
	}

	u, err = client.Users.FindFirst().Where(db.Users.Email.Eq("alice@x.io")).Select(db.Users.ID, db.Users.Email).One(ctx)
	must(t, err)
	if u.ID == 0 || u.Email == "" || u.Name != "" || u.Bio != nil {
		t.Errorf("Select: %+v", u)
	}

	_, err = client.Users.FindMany().Exclude(db.Posts.Title).All(ctx)
	if err == nil {
		t.Error("excluding another table's column must fail")
	}
}

func TestNotFound(t *testing.T) {
	client := setup(t)
	_, err := client.Users.Get(ctx, 424242)
	if !errors.Is(err, lathe.ErrNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("want ErrNotFound (and sql.ErrNoRows), got %v", err)
	}
}

func TestUpdateSaveDelete(t *testing.T) {
	client := setup(t)
	users := seedUsers(t, client)

	// bulk update with Set and Incr
	n, err := client.Users.Update().
		Set(db.Users.Bio.Set("edited"), lathe.Incr(db.Users.Age, 1)).
		Where(db.Users.Email.In("alice@x.io", "bob@x.io")).Exec(ctx)
	must(t, err)
	if n != 2 {
		t.Errorf("update matched %d rows, want 2", n)
	}
	bob, err := client.Users.Get(ctx, users["Bob"].ID)
	must(t, err)
	if bob.Bio == nil || *bob.Bio != "edited" || *bob.Age != 26 {
		t.Errorf("update result: %+v", bob)
	}

	// SetNull
	_, err = client.Users.Update().Set(db.Users.Bio.SetNull()).Where(db.Users.ID.Eq(bob.ID)).Exec(ctx)
	must(t, err)
	bob, _ = client.Users.Get(ctx, bob.ID)
	if bob.Bio != nil {
		t.Error("SetNull left a value")
	}

	// Save writes every column, including no-op saves
	bob.Name = "Robert"
	must(t, client.Users.Save(ctx, bob))
	must(t, client.Users.Save(ctx, bob)) // unchanged: must still count as a match
	again, _ := client.Users.Get(ctx, bob.ID)
	if again.Name != "Robert" {
		t.Errorf("Save: %+v", again)
	}
	ghost := *bob
	ghost.ID = 987654
	if err := client.Users.Save(ctx, &ghost); !errors.Is(err, lathe.ErrNotFound) {
		t.Errorf("Save of a missing row: got %v", err)
	}

	// the WHERE guard
	if _, err := client.Users.Delete().Exec(ctx); !errors.Is(err, lathe.ErrMissingWhere) {
		t.Errorf("unguarded delete: got %v", err)
	}
	if _, err := client.Users.Update().Set(db.Users.Active.Set(false)).Exec(ctx); !errors.Is(err, lathe.ErrMissingWhere) {
		t.Errorf("unguarded update: got %v", err)
	}

	n, err = client.Users.Delete().Where(db.Users.Active.Eq(false)).Exec(ctx)
	must(t, err)
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	total, _ := client.Users.Count(ctx)
	if total != 4 {
		t.Errorf("count after delete = %d", total)
	}
}

func TestUUIDBytesTimeAndCascade(t *testing.T) {
	client := setup(t)
	author := newUser("writer@x.io")
	must(t, client.Users.Create(ctx, &author))

	pub := time.Now().UTC().Truncate(time.Second)
	post := db.Post{AuthorID: author.ID, Title: "Hello", PublishedAt: &pub, Cover: []byte{0, 1, 2, 0xff}}
	must(t, client.Posts.Create(ctx, &post))
	if post.ID == uuid.Nil {
		t.Fatal("uuid was not generated")
	}
	got, err := client.Posts.Get(ctx, post.ID)
	must(t, err)
	if got.Title != "Hello" || string(got.Cover) != string([]byte{0, 1, 2, 0xff}) || got.Body != nil {
		t.Errorf("post round trip: %+v", got)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(pub) {
		t.Errorf("timestamp round trip: got %v want %v", got.PublishedAt, pub)
	}

	// composite primary key + Get with two parameters
	tag := db.Tag{Name: "go"}
	must(t, client.Tags.Create(ctx, &tag))
	link := db.PostTag{PostID: post.ID, TagID: tag.ID}
	must(t, client.PostTags.Create(ctx, &link))
	if _, err := client.PostTags.Get(ctx, post.ID, tag.ID); err != nil {
		t.Errorf("composite Get: %v", err)
	}

	// deleting the author cascades to posts and their tag links
	_, err = client.Users.Delete().Where(db.Users.ID.Eq(author.ID)).Exec(ctx)
	must(t, err)
	if n, _ := client.Posts.Count(ctx); n != 0 {
		t.Errorf("posts left after cascade: %d", n)
	}
	if n, _ := client.PostTags.Count(ctx); n != 0 {
		t.Errorf("post_tags left after cascade: %d", n)
	}
}

func TestJSONColumn(t *testing.T) {
	client := setup(t)
	u := newUser("json@x.io")
	settings, err := lathe.NewJSON(map[string]any{"theme": "dark", "n": 3})
	must(t, err)
	u.Settings = settings
	must(t, client.Users.Create(ctx, &u))
	got, err := client.Users.Get(ctx, u.ID)
	must(t, err)
	var m map[string]any
	must(t, got.Settings.Decode(&m))
	if m["theme"] != "dark" || m["n"] != float64(3) {
		t.Errorf("json round trip: %v", m)
	}
	none := newUser("nojson@x.io")
	must(t, client.Users.Create(ctx, &none))
	got, _ = client.Users.Get(ctx, none.ID)
	if got.Settings != nil {
		t.Errorf("NULL json should scan to nil, got %s", got.Settings)
	}
}

func TestDecimalAggregatesAndRaw(t *testing.T) {
	client := setup(t)
	author := newUser("rich@x.io")
	author.Balance = decimal.RequireFromString("100.10")
	must(t, client.Users.Create(ctx, &author))
	other := newUser("poor@x.io")
	other.Balance = decimal.RequireFromString("0.20")
	must(t, client.Users.Create(ctx, &other))

	type row struct {
		Total decimal.Decimal
		N     int64
	}
	rows, err := lathe.Scan[row](ctx, client.DB,
		lathe.Select(lathe.Sum(db.Users.Balance).As("total"), lathe.CountAll().As("n")).From(db.Users))
	must(t, err)
	if len(rows) != 1 || !rows[0].Total.Equal(decimal.RequireFromString("100.30")) || rows[0].N != 2 {
		t.Errorf("aggregate: %+v", rows)
	}

	rich, err := client.Users.FindMany().Where(db.Users.Balance.Gt(decimal.RequireFromString("50"))).All(ctx)
	must(t, err)
	if len(rich) != 1 || rich[0].Email != "rich@x.io" {
		t.Errorf("decimal comparison: %v", emails(rich))
	}
}

func TestJoinsGroupingAndScanning(t *testing.T) {
	client := setup(t)
	users := seedUsers(t, client)
	for i, title := range []string{"a", "b", "c"} {
		p := db.Post{AuthorID: users["Alice"].ID, Title: title}
		if i == 2 {
			p.AuthorID = users["Bob"].ID
		}
		must(t, client.Posts.Create(ctx, &p))
	}

	type stat struct {
		Email string
		Posts int64
	}
	stats, err := lathe.Scan[stat](ctx, client.DB,
		lathe.Select(db.Users.Email, lathe.Count(db.Posts.ID).As("posts")).
			From(db.Users).
			LeftJoin(db.Posts, db.Posts.AuthorID.EqCol(db.Users.ID)).
			GroupBy(db.Users.Email).
			Having(lathe.Count(db.Posts.ID).Gt(0)).
			OrderBy(lathe.Count(db.Posts.ID).Desc(), db.Users.Email.Asc()))
	must(t, err)
	if fmt.Sprint(stats) != "[{alice@x.io 2} {bob@x.io 1}]" {
		t.Errorf("stats: %v", stats)
	}

	// scalar scanning and a subquery
	ids, err := lathe.Scan[int64](ctx, client.DB,
		lathe.Select(db.Users.ID).From(db.Users).
			Where(db.Users.ID.InQuery(lathe.Select(db.Posts.AuthorID).From(db.Posts))).
			OrderBy(db.Users.ID.Asc()))
	must(t, err)
	if len(ids) != 2 {
		t.Errorf("subquery ids: %v", ids)
	}

	one, err := lathe.ScanOne[stat](ctx, client.DB,
		lathe.Select(db.Users.Email, lathe.CountAll().As("posts")).From(db.Users).Where(db.Users.Email.Eq("eve@y.io")).GroupBy(db.Users.Email))
	must(t, err)
	if one.Email != "eve@y.io" || one.Posts != 1 {
		t.Errorf("ScanOne: %+v", one)
	}
	_, err = lathe.ScanOne[stat](ctx, client.DB, lathe.Select(db.Users.Email, lathe.CountAll().As("posts")).From(db.Users).Where(db.Users.Email.Eq("none")).GroupBy(db.Users.Email))
	if !errors.Is(err, lathe.ErrNotFound) {
		t.Errorf("ScanOne without rows: %v", err)
	}

	// raw SQL with portable placeholders, including a literal ?? and a quoted ?
	type named struct{ Email string }
	raw, err := lathe.Raw[named](ctx, client.DB,
		"SELECT email FROM users WHERE age > ? AND email <> 'what?' ORDER BY email", 20)
	must(t, err)
	if len(raw) != 3 {
		t.Errorf("raw: %v", raw)
	}
	res, err := client.Exec(ctx, "UPDATE users SET age = age + ? WHERE email = ?", 1, "bob@x.io")
	must(t, err)
	if n, _ := res.RowsAffected(); n != 1 {
		t.Errorf("raw exec affected %d", n)
	}

	// a result column without a matching field is an error, not silent loss
	type tooSmall struct{ Email string }
	if _, err := lathe.Raw[tooSmall](ctx, client.DB, "SELECT email, name FROM users"); err == nil || !strings.Contains(err.Error(), "no matching field") {
		t.Errorf("unmatched column: %v", err)
	}
}

func TestTransactions(t *testing.T) {
	client := setup(t)
	boom := errors.New("boom")
	count := func() int64 { n, err := client.Users.Count(ctx); must(t, err); return n }

	// commit
	must(t, client.Tx(ctx, func(tx *db.Client) error {
		u := newUser("committed@x.io")
		return tx.Users.Create(ctx, &u)
	}))
	if count() != 1 {
		t.Fatal("commit lost the row")
	}

	// rollback on error; work inside sees its own writes
	err := client.Tx(ctx, func(tx *db.Client) error {
		u := newUser("rolled@x.io")
		if err := tx.Users.Create(ctx, &u); err != nil {
			return err
		}
		if n, _ := tx.Users.Count(ctx); n != 2 {
			t.Errorf("tx should see its own insert, count=%d", n)
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != 1 {
		t.Errorf("rollback failed: err=%v count=%d", err, count())
	}

	// rollback on panic, and the panic propagates
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = client.Tx(ctx, func(tx *db.Client) error {
			u := newUser("panic@x.io")
			_ = tx.Users.Create(ctx, &u)
			panic("kaboom")
		})
	}()
	if count() != 1 {
		t.Error("panic did not roll back")
	}

	// nested transactions are savepoints: a failed inner block, even one that
	// hit a constraint, rolls back alone
	must(t, client.Tx(ctx, func(tx *db.Client) error {
		a := newUser("outer@x.io")
		if err := tx.Users.Create(ctx, &a); err != nil {
			return err
		}
		inner := tx.Tx(ctx, func(tx2 *db.Client) error {
			b := newUser("inner@x.io")
			if err := tx2.Users.Create(ctx, &b); err != nil {
				return err
			}
			dup := newUser("outer@x.io")
			return tx2.Users.Create(ctx, &dup) // unique violation
		})
		if !errors.Is(inner, lathe.ErrUniqueViolation) {
			t.Errorf("inner: want unique violation, got %v", inner)
		}
		c := newUser("after@x.io")
		return tx.Users.Create(ctx, &c) // the outer transaction is still usable
	}))
	got, err := client.Users.FindMany().OrderBy(db.Users.Email.Asc()).All(ctx)
	must(t, err)
	if fmt.Sprint(emails(got)) != "[after@x.io committed@x.io outer@x.io]" {
		t.Errorf("after savepoint test: %v", emails(got))
	}
}

func TestCreateMany(t *testing.T) {
	client := setup(t)
	const n = 7500 // enough parameters to force chunking on every dialect
	rows := make([]db.User, n)
	for i := range rows {
		rows[i] = newUser(fmt.Sprintf("bulk%05d@x.io", i))
		if i%3 == 0 {
			rows[i].Role = db.UserRoleAdmin // a different column set than the default-role rows
		}
	}
	must(t, client.Users.CreateMany(ctx, rows))

	seen := map[int64]bool{}
	byID := map[int64]string{}
	all, err := client.Users.FindMany().All(ctx)
	must(t, err)
	for _, u := range all {
		byID[u.ID] = u.Email
	}
	for i, r := range rows {
		if r.ID == 0 || seen[r.ID] {
			t.Fatalf("row %d: missing or duplicate id %d", i, r.ID)
		}
		seen[r.ID] = true
		if byID[r.ID] != r.Email {
			t.Fatalf("row %d: id %d belongs to %q in the database, not %q (returned rows were matched to the wrong inputs)", i, r.ID, byID[r.ID], r.Email)
		}
		want := db.UserRoleMember
		if i%3 == 0 {
			want = db.UserRoleAdmin
		}
		if r.Role != want || r.CreatedAt.IsZero() {
			t.Fatalf("row %d: role=%q created_at=%v", i, r.Role, r.CreatedAt)
		}
	}

	// atomic: one bad row rolls the whole batch back
	before, _ := client.Users.Count(ctx)
	batch := []db.User{newUser("ok1@x.io"), newUser("ok2@x.io"), newUser("bulk00001@x.io")}
	if err := client.Users.CreateMany(ctx, batch); !errors.Is(err, lathe.ErrUniqueViolation) {
		t.Errorf("want unique violation, got %v", err)
	}
	after, _ := client.Users.Count(ctx)
	if before != after {
		t.Errorf("CreateMany is not atomic: %d -> %d rows", before, after)
	}
}

func TestLoggerSeesStatements(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	client := setup(t, lathe.WithLogger(func(_ context.Context, e lathe.QueryEvent) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e.SQL)
	}))
	mu.Lock()
	seen = nil
	mu.Unlock()
	u := newUser("logged@x.io")
	must(t, client.Users.Create(ctx, &u))
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !strings.Contains(strings.ToUpper(seen[0]), "INSERT INTO") {
		t.Errorf("logger saw %v", seen)
	}
}
