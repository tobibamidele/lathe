//go:build stage1

package integration_test

import (
	"testing"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/integration/db"
)

func TestUpsertDoUpdate(t *testing.T) {
	client := setup(t)
	bio := "first bio"
	age := int32(30)
	u := db.User{Email: "up@x.io", Name: "Original", PasswordHash: "h", Active: true, Bio: &bio, Age: &age}
	must(t, client.Users.Upsert(&u).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx))
	if u.ID == 0 || u.Role != db.UserRoleMember || u.CreatedAt.IsZero() {
		t.Fatalf("insert path must fill generated columns: %+v", u)
	}
	firstID := u.ID

	// conflict: only the listed column is overwritten, the rest is kept
	otherBio := "should not be written"
	again := db.User{Email: "up@x.io", Name: "Renamed", PasswordHash: "other", Active: true, Bio: &otherBio}
	must(t, client.Users.Upsert(&again).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx))
	if again.ID != firstID {
		t.Errorf("the row must keep its id: %d vs %d", again.ID, firstID)
	}
	if again.Name != "Renamed" || again.PasswordHash != "h" || again.Bio == nil || *again.Bio != "first bio" {
		t.Errorf("the struct must reflect the database afterwards: %+v", again)
	}
	if n, _ := client.Users.Count(ctx); n != 1 {
		t.Errorf("an upsert must not create a second row: %d", n)
	}

	// DoUpdate() with no columns overwrites every inserted non-key column
	third := db.User{Email: "up@x.io", Name: "Third", PasswordHash: "h3", Active: true}
	must(t, client.Users.Upsert(&third).OnConflict(db.Users.Email).DoUpdate().Exec(ctx))
	got, err := client.Users.Get(ctx, firstID)
	must(t, err)
	if got.Name != "Third" || got.PasswordHash != "h3" || got.Bio != nil {
		t.Errorf("DoUpdate(): %+v", got)
	}
}

func TestUpsertDoNothingReturnsTheExistingRow(t *testing.T) {
	client := setup(t)
	first := newUser("once@x.io")
	first.Name = "Keeper"
	must(t, client.Users.Upsert(&first).OnConflict(db.Users.Email).DoNothing().Exec(ctx))

	second := newUser("once@x.io")
	second.Name = "Intruder"
	must(t, client.Users.Upsert(&second).OnConflict(db.Users.Email).DoNothing().Exec(ctx))
	if second.ID != first.ID || second.Name != "Keeper" {
		t.Errorf("get-or-create: the struct must hold the row that was already there: %+v", second)
	}
	if n, _ := client.Users.Count(ctx); n != 1 {
		t.Errorf("rows: %d", n)
	}
}

func TestUpsertWithExpressions(t *testing.T) {
	client := setup(t)
	age := int32(30)
	u := newUser("counter@x.io")
	u.Age = &age
	must(t, client.Users.Upsert(&u).OnConflict(db.Users.Email).Set(lathe.Incr(db.Users.Age, 1)).Exec(ctx))
	if *u.Age != 30 {
		t.Fatalf("the insert path stores the value as given: %d", *u.Age)
	}
	for i := 0; i < 3; i++ {
		again := newUser("counter@x.io")
		again.Age = &age
		must(t, client.Users.Upsert(&again).OnConflict(db.Users.Email).Set(lathe.Incr(db.Users.Age, 1)).Exec(ctx))
	}
	got, err := client.Users.Get(ctx, u.ID)
	must(t, err)
	if got.Age == nil || *got.Age != 33 {
		t.Errorf("Set(Incr) on conflict: %v", got.Age)
	}
}

func TestUpsertCompositeAndClientGeneratedKeys(t *testing.T) {
	w := newWorld(t)
	link := db.PostTag{PostID: w.posts["a1"].ID, TagID: w.tags["go"].ID} // already linked
	must(t, w.client.PostTags.Upsert(&link).OnConflict(db.PostTags.PostID, db.PostTags.TagID).DoNothing().Exec(ctx))
	if n, _ := w.client.PostTags.Count(ctx); n != 6 {
		t.Errorf("link count after a no-op upsert: %d", n)
	}

	// a client generated uuid key, updated through an explicit id
	p := db.Post{AuthorID: w.users["Bob"].ID, Title: "v1"}
	must(t, w.client.Posts.Upsert(&p).OnConflict(db.Posts.ID).DoUpdate(db.Posts.Title).Exec(ctx))
	id := p.ID
	p2 := db.Post{ID: id, AuthorID: w.users["Bob"].ID, Title: "v2"}
	must(t, w.client.Posts.Upsert(&p2).OnConflict(db.Posts.ID).DoUpdate(db.Posts.Title).Exec(ctx))
	got, err := w.client.Posts.Get(ctx, id)
	must(t, err)
	if got.Title != "v2" {
		t.Errorf("uuid upsert: %+v", got)
	}
}

func TestUpsertMisuseIsReported(t *testing.T) {
	client := setup(t)
	u := newUser("x@x.io")
	if err := client.Users.Upsert(&u).OnConflict(db.Users.Email).Exec(ctx); err == nil {
		t.Error("an upsert without an action must fail")
	}
	if err := client.Users.Upsert(&u).OnConflict(db.Users.Email).DoUpdate(db.Users.CreatedAt).Exec(ctx); err == nil {
		t.Error("updating a column that is not part of the INSERT must fail")
	}
}
