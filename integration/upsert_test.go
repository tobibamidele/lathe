//go:build stage1

package integration_test

import (
	"fmt"
	"strings"
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

func TestUpsertManyMixesInsertsAndUpdates(t *testing.T) {
	client := setup(t)
	existing := []db.User{newUser("a@x.io"), newUser("b@x.io"), newUser("c@x.io")}
	must(t, client.Users.CreateMany(ctx, existing))
	ids := map[string]int64{}
	for _, u := range existing {
		ids[u.Email] = u.ID
	}

	incoming := []db.User{
		{Email: "a@x.io", Name: "A2", PasswordHash: "ignored-a", Active: true},
		{Email: "d@x.io", Name: "D", PasswordHash: "pw-d", Active: true},
		{Email: "b@x.io", Name: "B2", PasswordHash: "ignored-b", Active: true},
		{Email: "e@x.io", Name: "E", PasswordHash: "pw-e", Active: true},
	}
	must(t, client.Users.UpsertMany(incoming).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx))

	if n, _ := client.Users.Count(ctx); n != 5 {
		t.Errorf("rows: %d, want 5", n)
	}
	for _, u := range incoming {
		if u.ID == 0 || u.Role != db.UserRoleMember || u.CreatedAt.IsZero() {
			t.Errorf("%s was not read back: %+v", u.Email, u)
		}
		if id, existed := ids[u.Email]; existed && u.ID != id {
			t.Errorf("%s changed id: %d -> %d", u.Email, id, u.ID)
		}
	}
	// updated rows: only the listed column was written, and the struct shows the stored password
	if incoming[0].Name != "A2" || incoming[0].PasswordHash != "hash-a@x.io" {
		t.Errorf("a: %+v", incoming[0])
	}
	if incoming[1].PasswordHash != "pw-d" {
		t.Errorf("a new row keeps what was inserted: %+v", incoming[1])
	}
	got, err := client.Users.FindMany().Where(db.Users.Email.In("a@x.io", "b@x.io", "c@x.io")).OrderBy(db.Users.Email.Asc()).All(ctx)
	must(t, err)
	if got[0].Name != "A2" || got[1].Name != "B2" || got[2].Name != existing[2].Name {
		t.Errorf("stored names: %s %s %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

func TestUpsertManyDoNothingLeavesExistingRowsAlone(t *testing.T) {
	client := setup(t)
	must(t, client.Users.Create(ctx, &db.User{Email: "keep@x.io", Name: "Original", PasswordHash: "h", Active: true}))
	rows := []db.User{
		{Email: "keep@x.io", Name: "Intruder", PasswordHash: "x", Active: true},
		{Email: "new@x.io", Name: "New", PasswordHash: "y", Active: true},
	}
	must(t, client.Users.UpsertMany(rows).OnConflict(db.Users.Email).DoNothing().Exec(ctx))
	if rows[0].Name != "Original" || rows[0].ID == 0 {
		t.Errorf("a skipped row must show the row that was already there: %+v", rows[0])
	}
	if rows[1].ID == 0 || rows[1].Name != "New" {
		t.Errorf("an inserted row: %+v", rows[1])
	}
	if n, _ := client.Users.Count(ctx); n != 2 {
		t.Errorf("rows: %d", n)
	}
}

func TestUpsertManyAcrossParameterLimits(t *testing.T) {
	client := setup(t)
	const total, preexisting = 7500, 3000 // more parameters than any driver allows in one statement
	seed := make([]db.User, preexisting)
	for i := range seed {
		seed[i] = newUser(fmt.Sprintf("bulk%05d@x.io", i))
	}
	must(t, client.Users.CreateMany(ctx, seed))
	oldID := map[string]int64{}
	for _, u := range seed {
		oldID[u.Email] = u.ID
	}

	rows := make([]db.User, total)
	for i := range rows {
		rows[i] = newUser(fmt.Sprintf("bulk%05d@x.io", i))
		rows[i].Name = fmt.Sprintf("renamed %d", i)
		if i%5 == 0 {
			rows[i].Role = db.UserRoleAdmin // a different insert column list
		}
	}
	must(t, client.Users.UpsertMany(rows).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx))

	stored, err := client.Users.FindMany().All(ctx)
	must(t, err)
	if len(stored) != total {
		t.Fatalf("rows: %d, want %d", len(stored), total)
	}
	byEmail := map[string]db.User{}
	for _, u := range stored {
		byEmail[u.Email] = u
	}
	for i, r := range rows {
		s := byEmail[r.Email]
		if r.ID != s.ID {
			t.Fatalf("row %d (%s): struct says id %d, the database says %d (read-back matched the wrong row)", i, r.Email, r.ID, s.ID)
		}
		if id, was := oldID[r.Email]; was && id != r.ID {
			t.Fatalf("row %d was updated but its id changed", i)
		}
		if s.Name != fmt.Sprintf("renamed %d", i) || r.Name != s.Name {
			t.Fatalf("row %d name: db %q struct %q", i, s.Name, r.Name)
		}
	}
}

func TestUpsertManyIsAtomic(t *testing.T) {
	client := setup(t)
	must(t, client.Users.Create(ctx, &db.User{Email: "atomic@x.io", Name: "Before", PasswordHash: "h", Active: true}))
	rows := []db.User{
		{Email: "atomic@x.io", Name: "After", PasswordHash: "h", Active: true},
		{Email: "fresh@x.io", Name: "Fresh", PasswordHash: "h", Active: true},
		{Email: "bad@x.io", Name: "Bad", PasswordHash: "h", Active: true, Role: "bogus"},
	}
	if err := client.Users.UpsertMany(rows).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx); err == nil {
		t.Fatal("an invalid row must fail the whole upsert")
	}
	if n, _ := client.Users.Count(ctx); n != 1 {
		t.Errorf("nothing may be inserted: %d rows", n)
	}
	u, _ := client.Users.FindFirst().Where(db.Users.Email.Eq("atomic@x.io")).One(ctx)
	if u.Name != "Before" {
		t.Errorf("nothing may be updated: %q", u.Name)
	}
}

func TestUpsertManyRejectsTwoRowsForOneKey(t *testing.T) {
	client := setup(t)
	rows := []db.User{newUser("dup@x.io"), newUser("other@x.io"), newUser("dup@x.io")}
	err := client.Users.UpsertMany(rows).OnConflict(db.Users.Email).DoUpdate().Exec(ctx)
	if err == nil || !strings.Contains(err.Error(), "same conflict key") {
		t.Errorf("got %v", err)
	}
	if n, _ := client.Users.Count(ctx); n != 0 {
		t.Errorf("a rejected upsert must not write anything: %d", n)
	}
}

func TestUpsertManyWithCompositeAndUUIDKeys(t *testing.T) {
	w := newWorld(t)
	// links: a1-go and a2-go exist, b1-orm is new
	links := []db.PostTag{
		{PostID: w.posts["a1"].ID, TagID: w.tags["go"].ID},
		{PostID: w.posts["b1"].ID, TagID: w.tags["orm"].ID},
		{PostID: w.posts["a2"].ID, TagID: w.tags["go"].ID},
	}
	must(t, w.client.PostTags.UpsertMany(links).OnConflict(db.PostTags.PostID, db.PostTags.TagID).DoNothing().Exec(ctx))
	if n, _ := w.client.PostTags.Count(ctx); n != 7 { // six seeded + one new
		t.Errorf("links: %d, want 7", n)
	}

	// client generated uuid keys, updated by id
	posts := []db.Post{
		{AuthorID: w.users["Bob"].ID, Title: "p1"},
		{AuthorID: w.users["Bob"].ID, Title: "p2"},
	}
	must(t, w.client.Posts.UpsertMany(posts).OnConflict(db.Posts.ID).DoUpdate(db.Posts.Title).Exec(ctx))
	posts[0].Title, posts[1].Title = "p1 edited", "p2 edited"
	must(t, w.client.Posts.UpsertMany(posts).OnConflict(db.Posts.ID).DoUpdate(db.Posts.Title).Exec(ctx))
	for _, p := range posts {
		got, err := w.client.Posts.Get(ctx, p.ID)
		must(t, err)
		if got.Title != p.Title || !strings.HasSuffix(got.Title, "edited") {
			t.Errorf("uuid upsert: %+v", got)
		}
	}
}

func TestUpsertManyReadsBackThroughCaseInsensitiveCollations(t *testing.T) {
	if dialectName() != "mysql" {
		t.Skip("PostgreSQL and SQLite compare text case-sensitively by default")
	}
	client := setup(t)
	must(t, client.Users.Create(ctx, &db.User{Email: "case@x.io", Name: "Lower", PasswordHash: "h", Active: true}))
	rows := []db.User{{Email: "CASE@X.IO", Name: "Upper", PasswordHash: "h", Active: true}}
	must(t, client.Users.UpsertMany(rows).OnConflict(db.Users.Email).DoUpdate(db.Users.Name).Exec(ctx))
	if rows[0].ID == 0 || rows[0].Email != "case@x.io" || rows[0].Name != "Upper" {
		t.Errorf("the database decides what matches, and the struct must show that row: %+v", rows[0])
	}
	if n, _ := client.Users.Count(ctx); n != 1 {
		t.Errorf("rows: %d", n)
	}
}
