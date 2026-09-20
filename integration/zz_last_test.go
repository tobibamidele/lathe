//go:build stage1

// These run last (Go orders test files by name): they leave rows behind for the
// migration scenario in run.sh, and inspect them after a rollback, so no test
// that cleans the tables may run in between.

package integration_test

import (
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/tobibamidele/lathe/integration/db"
)

// TestZSeed leaves known rows behind for the migration scenario in run.sh.
func TestZSeed(t *testing.T) {
	if os.Getenv("LATHE_SEED") != "1" {
		t.Skip("LATHE_SEED not set")
	}
	client := setup(t)
	u := db.User{Email: "keep@x.io", Name: "Keeper", PasswordHash: "h", Active: true, Role: db.UserRoleAdmin,
		Balance: decimal.RequireFromString("42.5")}
	must(t, client.Users.Create(ctx, &u))
	p := db.Post{AuthorID: u.ID, Title: "Kept post", Views: 7}
	must(t, client.Posts.Create(ctx, &p))
	for _, name := range []string{"go", "orm"} {
		tag := db.Tag{Name: name}
		must(t, client.Tags.Create(ctx, &tag))
	}
}

// TestZAfterRollback checks the state after stage 2 was applied and reverted.
func TestZAfterRollback(t *testing.T) {
	if os.Getenv("LATHE_AFTER_ROLLBACK") != "1" {
		t.Skip("LATHE_AFTER_ROLLBACK not set")
	}
	client := db.NewClient(openDB(t))
	u, err := client.Users.FindFirst().Where(db.Users.Email.Eq("keep@x.io")).One(ctx)
	must(t, err)
	if u.Name != "Keeper" || u.Role != db.UserRoleAdmin || !u.Balance.Equal(decimal.RequireFromString("42.5")) {
		t.Errorf("user after rollback: %+v", u)
	}
	posts, err := client.Posts.FindMany().All(ctx)
	must(t, err)
	if len(posts) != 1 || posts[0].Views != 7 {
		t.Errorf("posts after rollback: %+v", posts)
	}
	tags, err := client.Tags.FindMany().OrderBy(db.Tags.Name.Asc()).All(ctx)
	must(t, err)
	if len(tags) != 2 || tags[0].Name != "go" {
		t.Errorf("tags after rollback: %+v", tags)
	}
	// the new-in-stage-2 objects are gone again
	if _, err := client.Exec(ctx, "SELECT 1 FROM comments"); err == nil {
		t.Error("comments table should not exist after rollback")
	}
}
