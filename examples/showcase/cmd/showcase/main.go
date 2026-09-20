// Command showcase walks through the query API against a live database.
//
//	DATABASE_URL=postgres://... go run ./cmd/showcase
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/shopspring/decimal"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/examples/showcase/db"
	"github.com/tobibamidele/lathe/postgres"
)

var ctx = context.Background()

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func section(title string) { fmt.Printf("\n── %s\n", title) }

func main() {
	var statements int
	conn, err := postgres.Open(os.Getenv("DATABASE_URL"),
		lathe.WithLogger(func(_ context.Context, e lathe.QueryEvent) { statements++ }))
	check(err)
	defer conn.Close()
	client := db.NewClient(conn)
	reset(client)

	// ───────────────────────────────────────────── create
	section("Create: generated columns are read back")
	ann := db.User{Email: "ann@example.com", Name: "Ann", PasswordHash: "h1", Role: db.UserRoleAdmin, Active: true}
	check(client.Users.Create(ctx, &ann))
	fmt.Println("id:", ann.ID, "| role:", ann.Role, "| created_at set:", !ann.CreatedAt.IsZero())

	bob := db.User{Email: "bob@example.com", Name: "Bob", PasswordHash: "h2", Active: true} // Role left empty -> database default
	check(client.Users.Create(ctx, &bob))
	fmt.Println("bob's role (database default):", bob.Role)

	bio, key := "Writes about Go.", "key-123"
	cy := db.User{Email: "cy@example.com", Name: "Cy", PasswordHash: "h3", Bio: &bio, APIKey: &key, Active: false}
	check(client.Users.Create(ctx, &cy))

	settings, _ := lathe.NewJSON(map[string]any{"theme": "dark", "notifications": true})
	dee := db.User{Email: "dee@example.com", Name: "Dee", PasswordHash: "h4", Role: db.UserRoleEditor, Active: true, Settings: settings}
	check(client.Users.Create(ctx, &dee))

	section("CreateMany: one transaction, batched INSERTs, ids and defaults filled in")
	now := time.Now()
	posts := []db.Post{
		{AuthorID: ann.ID, Title: "Hello, lathe", Status: db.PostStatusPublished, PublishedAt: &now, Views: 120},
		{AuthorID: ann.ID, Title: "Typed columns", Status: db.PostStatusPublished, PublishedAt: &now, Views: 45},
		{AuthorID: ann.ID, Title: "Draft ideas"},
		{AuthorID: bob.ID, Title: "Migrations without fear", Status: db.PostStatusPublished, PublishedAt: &now, Views: 300},
		{AuthorID: dee.ID, Title: "Old news", Status: db.PostStatusArchived, Views: 9},
		{AuthorID: dee.ID, Title: "Dee's second post", Status: db.PostStatusPublished, PublishedAt: &now, Views: 3},
	}
	check(client.Posts.CreateMany(ctx, posts))
	fmt.Println("first post id (client-side uuid):", posts[0].ID, "| default status:", posts[2].Status)

	for _, name := range []string{"go", "orm", "sql"} {
		check(client.Tags.Create(ctx, &db.Tag{Name: name}))
	}
	tags, err := client.Tags.FindMany().OrderBy(db.Tags.Name.Asc()).All(ctx)
	check(err)
	for _, t := range tags[:2] { // "Hello, lathe" is tagged go + orm
		check(client.PostTags.Create(ctx, &db.PostTag{PostID: posts[0].ID, TagID: t.ID}))
	}
	check(client.PostTags.Create(ctx, &db.PostTag{PostID: posts[3].ID, TagID: tags[2].ID}))

	check(client.Comments.CreateMany(ctx, []db.Comment{
		{PostID: posts[0].ID, UserID: &bob.ID, Body: "Nice!"},
		{PostID: posts[0].ID, UserID: &cy.ID, Body: "+1"},
		{PostID: posts[3].ID, UserID: &ann.ID, Body: "Useful"},
		{PostID: posts[0].ID, UserID: &dee.ID, Body: "Second!"},
	}))
	check(client.Orders.CreateMany(ctx, []db.Order{
		{UserID: bob.ID, Total: decimal.RequireFromString("19.99")},
		{UserID: bob.ID, Total: decimal.RequireFromString("80.01"), Currency: "EUR"},
		{UserID: cy.ID, Total: decimal.RequireFromString("5.5")},
	}))

	// ───────────────────────────────────────────── read
	section("Get / FindFirst / FindMany")
	u, err := client.Users.Get(ctx, ann.ID)
	check(err)
	fmt.Println("Get:", u.Email)

	u, err = client.Users.FindFirst().
		Where(db.Users.Email.Eq("cy@example.com"), db.Users.Active.Eq(false)).
		Exclude(db.Users.PasswordHash, db.Users.APIKey).
		One(ctx)
	check(err)
	fmt.Printf("FindFirst+Exclude: %s | password_hash=%q api_key=%v | bio=%q\n", u.Email, u.PasswordHash, u.APIKey, *u.Bio)

	_, err = client.Users.Get(ctx, 999999)
	fmt.Println("missing row -> ErrNotFound:", errors.Is(err, lathe.ErrNotFound), "| also sql.ErrNoRows:", errors.Is(err, sql.ErrNoRows))

	section("Conditions")
	names := func(us []db.User, err error) []string {
		check(err)
		var out []string
		for _, u := range us {
			out = append(out, u.Name)
		}
		return out
	}
	byName := db.Users.Name.Asc()
	fmt.Println("And (variadic):    ", names(client.Users.FindMany().Where(db.Users.Active.Eq(true), db.Users.Role.Ne(db.UserRoleReader)).OrderBy(byName).All(ctx)))
	fmt.Println("Or:                ", names(client.Users.FindMany().Where(lathe.Or(db.Users.Name.Eq("Ann"), db.Users.Bio.IsNotNull())).OrderBy(byName).All(ctx)))
	fmt.Println("Not + In:          ", names(client.Users.FindMany().Where(lathe.Not(db.Users.Name.In("Ann", "Bob"))).OrderBy(byName).All(ctx)))
	fmt.Println("IsNull:            ", names(client.Users.FindMany().Where(db.Users.Bio.IsNull()).OrderBy(byName).All(ctx)))
	fmt.Println("HasSuffix:         ", names(client.Users.FindMany().Where(db.Users.Email.HasSuffix("@example.com"), db.Users.Name.HasPrefix("A")).All(ctx)))
	fmt.Println("Contains (ILike):  ", names(client.Users.FindMany().Where(db.Users.Email.ILike("%BOB%")).All(ctx)))
	fmt.Println("Between (karma):   ", names(client.Users.FindMany().Where(db.Users.Karma.Between(0, 10)).OrderBy(byName).Limit(2).All(ctx)))
	fmt.Println("Raw fragment:      ", names(client.Users.FindMany().Where(lathe.SQL("length(name) = ?", 3)).OrderBy(byName).All(ctx)))
	fmt.Println("enum comparison:   ", names(client.Users.FindMany().Where(db.Users.Role.Eq(db.UserRoleAdmin)).All(ctx)))

	section("Ordering, paging, projections, counting")
	page, err := client.Posts.FindMany().
		Where(db.Posts.Status.Eq(db.PostStatusPublished)).
		OrderBy(db.Posts.Views.Desc(), db.Posts.Title.Asc()).
		Limit(2).Offset(1).
		Select(db.Posts.Title, db.Posts.Views).
		All(ctx)
	check(err)
	for _, p := range page {
		fmt.Printf("  %-16s views=%d (body/status not selected: %q %q)\n", p.Title, p.Views, p.Status, p.ID)
	}
	n, err := client.Posts.Count(ctx, db.Posts.Status.Eq(db.PostStatusPublished))
	check(err)
	ok, err := client.Users.Exists(ctx, db.Users.Email.Eq("nobody@example.com"))
	check(err)
	fmt.Println("published posts:", n, "| nobody exists:", ok)

	sqlText, args, _ := client.Posts.FindMany().Where(db.Posts.Views.Gt(100)).OrderBy(db.Posts.ID.Asc()).Limit(5).Build()
	fmt.Println("Build() shows the SQL without running it:\n  ", sqlText, args)

	section("JSON, bytes, nullable time")
	got, err := client.Users.Get(ctx, dee.ID)
	check(err)
	var prefs map[string]any
	check(got.Settings.Decode(&prefs))
	fmt.Println("settings:", prefs)

	// ───────────────────────────────────────────── update / delete
	section("Update, Save, Delete")
	rows, err := client.Posts.Update().
		Set(lathe.Incr(db.Posts.Views, 1), db.Posts.PublishedAt.SetNull()).
		Where(db.Posts.Status.Eq(db.PostStatusDraft)).
		Exec(ctx)
	check(err)
	fmt.Println("bulk update matched:", rows)

	rows, err = client.Users.Update().
		Set(db.Users.Karma.SetExpr(lathe.SQL("karma + ? * 2", 5)), db.Users.Bio.Set("Updated bio")).
		Where(db.Users.ID.Eq(bob.ID)).Exec(ctx)
	check(err)
	bob2, _ := client.Users.Get(ctx, bob.ID)
	fmt.Println("SetExpr karma:", bob2.Karma, "| rows:", rows)

	bob2.Name = "Robert"
	check(client.Users.Save(ctx, bob2))

	_, err = client.Users.Delete().Exec(ctx)
	fmt.Println("delete without WHERE:", err)
	_, err = client.Posts.Delete().Where(db.Posts.Status.Eq(db.PostStatusArchived)).Exec(ctx)
	check(err)

	// ───────────────────────────────────────────── select builder
	section("Select: joins, aggregates, HAVING, subqueries, scanning into structs")
	type authorStats struct {
		Author string
		Posts  int64
		Views  decimal.Decimal
	}
	stats, err := lathe.Scan[authorStats](ctx, conn,
		lathe.Select(
			db.Users.Name.As("author"),
			lathe.Count(db.Posts.ID).As("posts"),
			lathe.Coalesce(lathe.Sum(db.Posts.Views), 0).As("views"),
		).
			From(db.Users).
			LeftJoin(db.Posts, db.Posts.AuthorID.EqCol(db.Users.ID)).
			GroupBy(db.Users.Name).
			Having(lathe.Count(db.Posts.ID).Gt(0)).
			OrderBy(lathe.Count(db.Posts.ID).Desc(), db.Users.Name.Asc()))
	check(err)
	for _, s := range stats {
		fmt.Printf("  %-6s posts=%d views=%s\n", s.Author, s.Posts, s.Views)
	}

	type revenue struct {
		Currency string
		Total    decimal.Decimal
	}
	rev, err := lathe.Scan[revenue](ctx, conn,
		lathe.Select(db.Orders.Currency, lathe.Sum(db.Orders.Total).As("total")).
			From(db.Orders).GroupBy(db.Orders.Currency).OrderBy(db.Orders.Currency.Asc()))
	check(err)
	fmt.Println("revenue (exact decimals):", rev)

	published := lathe.Select(db.Posts.AuthorID).From(db.Posts).Where(db.Posts.Status.Eq(db.PostStatusPublished))
	authors, err := lathe.Scan[string](ctx, conn,
		lathe.Select(db.Users.Name).From(db.Users).Where(db.Users.ID.InQuery(published)).OrderBy(db.Users.Name.Asc()))
	check(err)
	fmt.Println("IN (subquery), scalar scan:", authors)

	commented := lathe.Select(lathe.SQL("1")).From(db.Comments).Where(db.Comments.PostID.EqCol(db.Posts.ID))
	titles, err := lathe.Scan[string](ctx, conn,
		lathe.Select(db.Posts.Title).From(db.Posts).Where(lathe.Exists(commented)).OrderBy(db.Posts.Title.Asc()))
	check(err)
	fmt.Println("EXISTS (correlated subquery):", titles)

	statuses, err := lathe.Scan[db.PostStatus](ctx, conn, lathe.Select(db.Posts.Status).From(db.Posts).Distinct().OrderBy(db.Posts.Status.Asc()))
	check(err)
	fmt.Println("DISTINCT:", statuses)

	top, err := lathe.ScanOne[authorStats](ctx, conn,
		lathe.Select(db.Users.Name.As("author"), lathe.Count(db.Posts.ID).As("posts"), lathe.Sum(db.Posts.Views).As("views")).
			From(db.Users).Join(db.Posts, db.Posts.AuthorID.EqCol(db.Users.ID)).
			GroupBy(db.Users.Name).OrderBy(lathe.Sum(db.Posts.Views).Desc()))
	check(err)
	fmt.Println("ScanOne (most viewed author):", top.Author)

	section("Raw SQL: ? placeholders on every dialect")
	type row struct {
		Name string
		N    int64
	}
	raw, err := lathe.Raw[row](ctx, conn, "SELECT name, karma::bigint AS n FROM users WHERE active = ? ORDER BY name", true)
	check(err)
	fmt.Println("Raw:", raw)
	one, err := lathe.RawOne[int64](ctx, conn, "SELECT count(*) FROM comments WHERE body <> 'what?'")
	check(err)
	fmt.Println("RawOne (a quoted ? is left alone):", one)
	res, err := client.Exec(ctx, "UPDATE users SET karma = karma + ? WHERE id = ?", 1, ann.ID)
	check(err)
	affected, _ := res.RowsAffected()
	fmt.Println("Exec affected:", affected)

	// ───────────────────────────────────────────── transactions
	section("Transactions, savepoints, isolation")
	boom := errors.New("boom")
	err = client.Tx(ctx, func(tx *db.Client) error {
		check(tx.Tags.Create(ctx, &db.Tag{Name: "rolled-back"}))
		return boom
	})
	c, _ := client.Tags.Count(ctx, db.Tags.Name.Eq("rolled-back"))
	fmt.Println("error rolls back:", errors.Is(err, boom), "| rows left:", c)

	err = client.Tx(ctx, func(tx *db.Client) error {
		check(tx.Tags.Create(ctx, &db.Tag{Name: "outer"}))
		inner := tx.Tx(ctx, func(tx2 *db.Client) error { // a savepoint
			check(tx2.Tags.Create(ctx, &db.Tag{Name: "inner"}))
			return tx2.Tags.Create(ctx, &db.Tag{Name: "outer"}) // unique violation
		})
		fmt.Println("inner failed with unique violation:", errors.Is(inner, lathe.ErrUniqueViolation))
		return nil // the outer transaction is still healthy and commits
	})
	check(err)
	inner, _ := client.Tags.Count(ctx, db.Tags.Name.Eq("inner"))
	outer, _ := client.Tags.Count(ctx, db.Tags.Name.Eq("outer"))
	fmt.Println("after commit: outer =", outer, "| inner =", inner)

	check(client.TxOptions(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}, func(tx *db.Client) error {
		n, err := tx.Posts.Count(ctx)
		fmt.Println("read-only serializable tx sees", n, "posts")
		return err
	}))

	// ───────────────────────────────────────────── errors
	section("Errors you can test for")
	_, err = client.Users.Delete().Where(db.Users.ID.Eq(bob.ID)).Exec(ctx)
	fmt.Println("delete user with orders (no cascade) -> FK violation:", errors.Is(err, lathe.ErrForeignKeyViolation))
	err = client.Users.Create(ctx, &db.User{Email: "ann@example.com", Name: "Dup", PasswordHash: "x"})
	fmt.Println("duplicate email -> unique violation:", errors.Is(err, lathe.ErrUniqueViolation))
	err = client.Users.Create(ctx, &db.User{Email: "z@example.com", Name: "Z", PasswordHash: "x", Role: "superuser"})
	fmt.Println("invalid enum value -> check violation:", errors.Is(err, lathe.ErrCheckViolation))

	// ON DELETE actions: deleting dee keeps her comment (SET NULL) and cascades to her posts
	_, err = client.Users.Delete().Where(db.Users.ID.Eq(dee.ID)).Exec(ctx)
	check(err)
	orphans, _ := client.Comments.Count(ctx, db.Comments.UserID.IsNull())
	deleted, _ := client.Posts.Count(ctx, db.Posts.AuthorID.Eq(dee.ID))
	fmt.Println("comments with a deleted author (SET NULL):", orphans, "| her posts left (CASCADE):", deleted)

	fmt.Printf("\n%d statements were observed by the WithLogger hook\n", statements)
}

func reset(c *db.Client) {
	for _, del := range []func() (int64, error){
		func() (int64, error) { return c.Comments.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return c.PostTags.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return c.Posts.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return c.Tags.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return c.Orders.Delete().AllRows().Exec(ctx) },
		func() (int64, error) { return c.Users.Delete().AllRows().Exec(ctx) },
	} {
		if _, err := del(); err != nil {
			log.Fatal(err)
		}
	}
}
