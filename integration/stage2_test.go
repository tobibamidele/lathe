//go:build stage2

package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/integration/db"
)

var ctx = context.Background()

// The stage 1 tests seeded a user, a post and two tags before the evolved
// schema was migrated in. Everything must have survived.
func TestDataSurvivedTheMigration(t *testing.T) {
	client := db.NewClient(openDB(t))

	u, err := client.Users.FindFirst().Where(db.Users.Email.Eq("keep@x.io")).One(ctx)
	must(t, err)
	if u.FullName != "Keeper" { // renamed column: name -> full_name
		t.Errorf("renamed column lost its data: %q", u.FullName)
	}
	if u.Role != db.UserRoleAdmin || !u.Balance.Equal(decimal.RequireFromString("42.5")) {
		t.Errorf("altered columns lost data: %+v", u)
	}
	if u.Nickname != nil {
		t.Errorf("new nullable column should be NULL: %v", u.Nickname)
	}

	posts, err := client.Posts.FindMany().All(ctx)
	must(t, err)
	if len(posts) != 1 || posts[0].Views != 7 || posts[0].Slug != nil { // views retyped to BIGINT
		t.Errorf("posts: %+v", posts)
	}

	labels, err := client.Labels.FindMany().OrderBy(db.Labels.Name.Asc()).All(ctx) // renamed table
	must(t, err)
	if len(labels) != 2 || labels[0].Name != "go" || labels[1].Name != "orm" {
		t.Errorf("renamed table lost its rows: %+v", labels)
	}
}

// Relations are derived from foreign keys, so tables added by a migration get
// them too, including a nullable belongs-to that is NULL.
func TestRelationsOnMigratedTables(t *testing.T) {
	client := db.NewClient(openDB(t))
	author, err := client.Users.FindFirst().Where(db.Users.Email.Eq("keep@x.io")).One(ctx)
	must(t, err)
	post, err := client.Posts.FindFirst().One(ctx)
	must(t, err)
	must(t, client.Comments.CreateMany(ctx, []db.Comment{
		{PostID: post.ID, UserID: &author.ID, Body: "signed"},
		{PostID: post.ID, Body: "anonymous"}, // user_id stays NULL
	}))

	loaded, err := client.Posts.FindFirst().Where(db.Posts.ID.Eq(post.ID)).
		With(db.Posts.Comments.OrderBy(db.Comments.ID.Asc()).With(db.Comments.User), db.Posts.Author).One(ctx)
	must(t, err)
	if loaded.Author == nil || loaded.Author.Email != "keep@x.io" {
		t.Errorf("author: %+v", loaded.Author)
	}
	if len(loaded.Comments) != 2 {
		t.Fatalf("comments: %+v", loaded.Comments)
	}
	if loaded.Comments[0].User == nil || loaded.Comments[0].User.ID != author.ID {
		t.Errorf("signed comment: %+v", loaded.Comments[0].User)
	}
	if loaded.Comments[1].User != nil {
		t.Errorf("a NULL foreign key must give a nil relation, got %+v", loaded.Comments[1].User)
	}

	// the reverse side, and the renamed table's relations
	withComments, err := client.Users.Get(ctx, author.ID)
	must(t, err)
	must(t, client.Users.Load(ctx, withComments, db.Users.Comments))
	if len(withComments.Comments) != 1 {
		t.Errorf("user comments: %+v", withComments.Comments)
	}
	_, err = client.Comments.Delete().AllRows().Exec(ctx)
	must(t, err)
}

func TestEvolvedSchemaWorks(t *testing.T) {
	client := db.NewClient(openDB(t))
	author, err := client.Users.FindFirst().Where(db.Users.Email.Eq("keep@x.io")).One(ctx)
	must(t, err)

	// the new enum value is accepted, the old check on unknown values still holds
	mod := db.User{Email: "mod@x.io", FullName: "Mod", PasswordHash: "h", Role: db.UserRoleModerator, Active: true}
	must(t, client.Users.Create(ctx, &mod))
	if mod.Role != db.UserRoleModerator {
		t.Errorf("role = %q", mod.Role)
	}
	bad := db.User{Email: "bad@x.io", FullName: "Bad", PasswordHash: "h", Role: "bogus"}
	if err := client.Users.Create(ctx, &bad); err == nil {
		t.Error("unknown enum value accepted after migration")
	}

	// the new table and its foreign keys
	post, err := client.Posts.FindFirst().One(ctx)
	must(t, err)
	c := db.Comment{PostID: post.ID, UserID: &author.ID, Body: "nice"}
	must(t, client.Comments.Create(ctx, &c))
	if c.ID == 0 || c.CreatedAt.IsZero() {
		t.Errorf("comment defaults: %+v", c)
	}
	ghost := int64(987654)
	orphan := db.Comment{PostID: post.ID, UserID: &ghost, Body: "x"}
	if err := client.Comments.Create(ctx, &orphan); !errors.Is(err, lathe.ErrForeignKeyViolation) {
		t.Errorf("comment fk: %v", err)
	}

	// the new unique index on posts.slug
	slug := "hello"
	p1 := db.Post{AuthorID: author.ID, Title: "one", Slug: &slug}
	must(t, client.Posts.Create(ctx, &p1))
	p2 := db.Post{AuthorID: author.ID, Title: "two", Slug: &slug}
	if err := client.Posts.Create(ctx, &p2); !errors.Is(err, lathe.ErrUniqueViolation) {
		t.Errorf("slug uniqueness: %v", err)
	}

	// the dropped table is gone
	if _, err := client.Exec(ctx, "SELECT 1 FROM post_tags"); err == nil {
		t.Error("post_tags should have been dropped")
	}

	// cascades still work after SQLite table rebuilds
	_, err = client.Users.Delete().Where(db.Users.ID.Eq(author.ID)).Exec(ctx)
	must(t, err)
	if n, _ := client.Comments.Count(ctx); n != 0 {
		t.Errorf("comments not cascaded: %d", n)
	}
	if n, _ := client.Posts.Count(ctx); n != 0 {
		t.Errorf("posts not cascaded: %d", n)
	}
}
