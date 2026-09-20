//go:build stage1

package integration_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"context"
	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/integration/db"
)

// world builds a small graph: 3 users, posts, tags, links, a profile, messages.
type world struct {
	client *db.Client
	users  map[string]*db.User
	posts  map[string]*db.Post
	tags   map[string]*db.Tag
	stmts  *atomic.Int64
}

func newWorld(t *testing.T) *world {
	t.Helper()
	var stmts atomic.Int64
	client := setup(t, lathe.WithLogger(func(_ context.Context, _ lathe.QueryEvent) { stmts.Add(1) }))
	w := &world{client: client, users: seedUsers(t, client), posts: map[string]*db.Post{}, tags: map[string]*db.Tag{}, stmts: &stmts}

	mk := func(author, title string) {
		p := db.Post{AuthorID: w.users[author].ID, Title: title}
		must(t, client.Posts.Create(ctx, &p))
		w.posts[title] = &p
	}
	mk("Alice", "a1")
	mk("Alice", "a2")
	mk("Bob", "b1")

	for _, name := range []string{"go", "orm", "sql"} {
		tag := db.Tag{Name: name}
		must(t, client.Tags.Create(ctx, &tag))
		w.tags[name] = &tag
	}
	link := func(post string, tags ...string) {
		for _, tg := range tags {
			must(t, client.PostTags.Create(ctx, &db.PostTag{PostID: w.posts[post].ID, TagID: w.tags[tg].ID}))
		}
	}
	link("a1", "go", "orm", "sql")
	link("a2", "go")
	link("b1", "sql", "go")

	site := "https://alice.example"
	must(t, client.Profiles.Create(ctx, &db.Profile{UserID: w.users["Alice"].ID, Website: &site}))
	for _, m := range []db.Message{
		{SenderID: w.users["Alice"].ID, RecipientID: w.users["Bob"].ID, Body: "hi bob"},
		{SenderID: w.users["Alice"].ID, RecipientID: w.users["Carol"].ID, Body: "hi carol"},
		{SenderID: w.users["Bob"].ID, RecipientID: w.users["Alice"].ID, Body: "hi alice"},
	} {
		m := m
		must(t, client.Messages.Create(ctx, &m))
	}
	return w
}

func TestPreloadHasManyBelongsToHasOne(t *testing.T) {
	w := newWorld(t)
	users, err := w.client.Users.FindMany().OrderBy(db.Users.Name.Asc()).
		With(db.Users.Posts.OrderBy(db.Posts.Title.Desc()), db.Users.Profile).
		All(ctx)
	must(t, err)

	byName := map[string]db.User{}
	for _, u := range users {
		byName[u.Name] = u
	}
	alice, bob, carol := byName["Alice"], byName["Bob"], byName["Carol"]
	if len(alice.Posts) != 2 || alice.Posts[0].Title != "a2" || alice.Posts[1].Title != "a1" {
		t.Errorf("has-many with order: %+v", alice.Posts)
	}
	if len(bob.Posts) != 1 {
		t.Errorf("bob's posts: %+v", bob.Posts)
	}
	if carol.Posts == nil || len(carol.Posts) != 0 {
		t.Errorf("a parent with no children gets an empty, non-nil slice: %#v", carol.Posts)
	}
	if alice.Profile == nil || alice.Profile.Website == nil || *alice.Profile.Website != "https://alice.example" {
		t.Errorf("has-one: %+v", alice.Profile)
	}
	if bob.Profile != nil {
		t.Errorf("no profile expected for bob: %+v", bob.Profile)
	}

	// belongs-to, and the reverse one-to-one
	posts, err := w.client.Posts.FindMany().OrderBy(db.Posts.Title.Asc()).With(db.Posts.Author).All(ctx)
	must(t, err)
	for _, p := range posts {
		want := map[string]string{"a1": "Alice", "a2": "Alice", "b1": "Bob"}[p.Title]
		if p.Author == nil || p.Author.Name != want {
			t.Errorf("post %s author = %+v, want %s", p.Title, p.Author, want)
		}
	}
	profile, err := w.client.Profiles.FindFirst().With(db.Profiles.User).One(ctx)
	must(t, err)
	if profile.User == nil || profile.User.Name != "Alice" {
		t.Errorf("profile owner: %+v", profile.User)
	}
}

func TestPreloadManyToMany(t *testing.T) {
	w := newWorld(t)
	posts, err := w.client.Posts.FindMany().OrderBy(db.Posts.Title.Asc()).
		With(db.Posts.Tags.OrderBy(db.Tags.Name.Asc())).All(ctx)
	must(t, err)
	names := func(ts []db.Tag) string {
		var n []string
		for _, t := range ts {
			n = append(n, t.Name)
		}
		return strings.Join(n, ",")
	}
	got := fmt.Sprint(names(posts[0].Tags), " | ", names(posts[1].Tags), " | ", names(posts[2].Tags))
	if got != "go,orm,sql | go | go,sql" {
		t.Errorf("many-to-many with order: %s", got)
	}

	// from the other side, filtered
	tag, err := w.client.Tags.FindFirst().Where(db.Tags.Name.Eq("go")).
		With(db.Tags.Posts.Where(db.Posts.Title.Ne("a2")).OrderBy(db.Posts.Title.Asc())).One(ctx)
	must(t, err)
	if len(tag.Posts) != 2 || tag.Posts[0].Title != "a1" || tag.Posts[1].Title != "b1" {
		t.Errorf("filtered many-to-many: %+v", tag.Posts)
	}
}

func TestNestedPreloadsUseAConstantNumberOfQueries(t *testing.T) {
	w := newWorld(t)
	// more parents must not mean more queries
	for i := 0; i < 30; i++ {
		u := newUser(fmt.Sprintf("extra%d@x.io", i))
		must(t, w.client.Users.Create(ctx, &u))
		p := db.Post{AuthorID: u.ID, Title: fmt.Sprintf("extra post %d", i)}
		must(t, w.client.Posts.Create(ctx, &p))
	}

	w.stmts.Store(0)
	users, err := w.client.Users.FindMany().
		With(db.Users.Posts.With(db.Posts.Tags, db.Posts.Author), db.Users.Profile).
		All(ctx)
	must(t, err)
	// users; posts; tags = links + tags; author; profile
	if n := w.stmts.Load(); n != 6 {
		t.Errorf("expected 6 statements for %d users, got %d (N+1?)", len(users), n)
	}
	for _, u := range users {
		for _, p := range u.Posts {
			if p.Author == nil || p.Author.ID != u.ID {
				t.Fatalf("nested belongs-to wrong for %s: %+v", p.Title, p.Author)
			}
		}
	}
	var alice db.User
	for _, u := range users {
		if u.Name == "Alice" {
			alice = u
		}
	}
	tagCount := 0
	for _, p := range alice.Posts {
		tagCount += len(p.Tags)
	}
	if tagCount != 4 {
		t.Errorf("alice's posts carry %d tags, want 4", tagCount)
	}
}

func TestSeveralForeignKeysToOneTable(t *testing.T) {
	w := newWorld(t)
	alice, err := w.client.Users.FindFirst().Where(db.Users.Name.Eq("Alice")).
		With(db.Users.MessagesBySender.OrderBy(db.Messages.ID.Asc()), db.Users.MessagesByRecipient).One(ctx)
	must(t, err)
	if len(alice.MessagesBySender) != 2 || len(alice.MessagesByRecipient) != 1 || alice.MessagesByRecipient[0].Body != "hi alice" {
		t.Errorf("sender/recipient: %+v / %+v", alice.MessagesBySender, alice.MessagesByRecipient)
	}
	msgs, err := w.client.Messages.FindMany().OrderBy(db.Messages.ID.Asc()).With(db.Messages.Sender, db.Messages.Recipient).All(ctx)
	must(t, err)
	if msgs[0].Sender.Name != "Alice" || msgs[0].Recipient.Name != "Bob" {
		t.Errorf("message endpoints: %s -> %s", msgs[0].Sender.Name, msgs[0].Recipient.Name)
	}
}

func TestCompositeKeyRelation(t *testing.T) {
	client := setup(t)
	for _, o := range []db.Org{{Tenant: 1, ID: 1, Name: "acme"}, {Tenant: 1, ID: 2, Name: "globex"}, {Tenant: 2, ID: 1, Name: "initech"}} {
		o := o
		must(t, client.Orgs.Create(ctx, &o))
	}
	for _, m := range []db.Member{
		{OrgTenant: 1, OrgID: 1, Name: "ann"}, {OrgTenant: 1, OrgID: 1, Name: "bob"},
		{OrgTenant: 2, OrgID: 1, Name: "cy"}, {OrgTenant: 1, OrgID: 2, Name: "dee"},
	} {
		m := m
		must(t, client.Members.Create(ctx, &m))
	}
	orgs, err := client.Orgs.FindMany().OrderBy(db.Orgs.Tenant.Asc(), db.Orgs.ID.Asc()).
		With(db.Orgs.Members.OrderBy(db.Members.Name.Asc())).All(ctx)
	must(t, err)
	var got []string
	for _, o := range orgs {
		var ms []string
		for _, m := range o.Members {
			ms = append(ms, m.Name)
		}
		got = append(got, fmt.Sprintf("%s=%v", o.Name, ms))
	}
	// (1,1) and (2,1) share an id; only the full key may match
	if fmt.Sprint(got) != "[acme=[ann bob] globex=[dee] initech=[cy]]" {
		t.Errorf("composite key matching: %v", got)
	}
	members, err := client.Members.FindMany().OrderBy(db.Members.Name.Asc()).With(db.Members.Org).All(ctx)
	must(t, err)
	if members[2].Name != "cy" || members[2].Org == nil || members[2].Org.Name != "initech" {
		t.Errorf("composite belongs-to: %+v", members[2])
	}
}

func TestLoadOntoExistingRows(t *testing.T) {
	w := newWorld(t)
	bob, err := w.client.Users.Get(ctx, w.users["Bob"].ID)
	must(t, err)
	if bob.Posts != nil {
		t.Fatal("relations must be nil until loaded")
	}
	must(t, w.client.Users.Load(ctx, bob, db.Users.Posts))
	if len(bob.Posts) != 1 {
		t.Errorf("Load: %+v", bob.Posts)
	}

	posts, err := w.client.Posts.FindMany().All(ctx)
	must(t, err)
	must(t, w.client.Posts.LoadMany(ctx, posts, db.Posts.Author, db.Posts.Tags))
	for _, p := range posts {
		if p.Author == nil || p.Tags == nil {
			t.Fatalf("LoadMany left %s without relations", p.Title)
		}
	}
}

func TestRelationOptionsAreImmutableAndValidated(t *testing.T) {
	w := newWorld(t)
	_ = db.Users.Posts.Where(db.Posts.Title.Eq("nope")) // must not change db.Users.Posts itself
	alice, err := w.client.Users.FindFirst().Where(db.Users.Name.Eq("Alice")).With(db.Users.Posts).One(ctx)
	must(t, err)
	if len(alice.Posts) != 2 {
		t.Errorf("a derived relation leaked into the original: %d posts", len(alice.Posts))
	}

	// Exclude drops columns of the related rows...
	alice, err = w.client.Users.FindFirst().Where(db.Users.Name.Eq("Alice")).
		With(db.Users.Posts.Exclude(db.Posts.Title)).One(ctx)
	must(t, err)
	if alice.Posts[0].Title != "" || alice.Posts[0].AuthorID != alice.ID {
		t.Errorf("Exclude on a relation: %+v", alice.Posts[0])
	}
	// ...but never the column that links them
	_, err = w.client.Users.FindFirst().With(db.Users.Posts.Exclude(db.Posts.AuthorID)).One(ctx)
	if err == nil || !strings.Contains(err.Error(), "links the related rows") {
		t.Errorf("excluding the link column: %v", err)
	}
	// and the parent query must select the columns the relation reads
	_, err = w.client.Posts.FindMany().Select(db.Posts.Title).With(db.Posts.Author).All(ctx)
	if err == nil || !strings.Contains(err.Error(), "does not select") {
		t.Errorf("With on a query that leaves out the foreign key: %v", err)
	}
}

func TestDeletingAParentCascadesThroughRelations(t *testing.T) {
	w := newWorld(t)
	_, err := w.client.Users.Delete().Where(db.Users.ID.Eq(w.users["Alice"].ID)).Exec(ctx)
	must(t, err)
	posts, err := w.client.Posts.FindMany().With(db.Posts.Tags).All(ctx)
	must(t, err)
	if len(posts) != 1 || len(posts[0].Tags) != 2 {
		t.Errorf("after cascade: %+v", posts)
	}
}
