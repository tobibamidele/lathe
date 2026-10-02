//go:build stage1

package integration_test

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/tobibamidele/lathe"
	"github.com/tobibamidele/lathe/integration/db"
)

// TestReturningProjectsOnlyTheRequestedValues checks that Returning returns a
// row holding exactly the requested columns, in order, NULL as nil, and that a
// column left out of the projection never surfaces as a zero value.
func TestReturningProjectsOnlyTheRequestedValues(t *testing.T) {
	client := setup(t)
	u := newUser("returning@x.io")
	must(t, client.Users.Create(ctx, &u))

	row, err := client.Users.FindFirst().
		Where(db.Users.Email.Eq("returning@x.io")).
		Returning(db.Users.ID, db.Users.Email, db.Users.Bio, db.Users.Balance).
		One(ctx)
	must(t, err)
	if len(row) != 4 {
		t.Fatalf("want 4 values, got %d: %+v", len(row), row)
	}
	id, ok := row[0].(int64)
	if !ok || id != u.ID {
		t.Errorf("position 0: want id %d, got %v (%T)", u.ID, row[0], row[0])
	}
	if email, ok := row[1].(string); !ok || email != "returning@x.io" {
		t.Errorf("position 1: want the email as a string, got %v (%T)", row[1], row[1])
	}
	if row[2] != nil {
		t.Errorf("position 2: bio is NULL, want nil, got %v (%T)", row[2], row[2])
	}
	if b, ok := row[3].(decimal.Decimal); !ok || !b.Equal(decimal.RequireFromString("0")) {
		t.Errorf("position 3: want balance 0, got %v (%T)", row[3], row[3])
	}

	rows, err := client.Users.FindMany().
		Where(db.Users.Email.Eq("returning@x.io")).
		Returning(db.Users.ID, db.Users.Email, db.Users.Bio).
		All(ctx)
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if len(rows[0]) != 3 || rows[0][0] != id || rows[0][1] != "returning@x.io" || rows[0][2] != nil {
		t.Errorf("projection mismatch: %+v", rows[0])
	}

	// Only the projected values are present: the password hash (a password_hash,
	// which is never '') must not leak into the row boundaries.
	got, err := client.Users.FindFirst().
		Where(db.Users.Email.Eq("returning@x.io")).
		Returning(db.Users.PasswordHash).
		One(ctx)
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("want a single projected value, got %d: %+v", len(got), got)
	}
	hash, ok := got[0].(string)
	if !ok || hash != "hash-returning@x.io" {
		t.Errorf("want the password hash, got %v (%T)", got[0], got[0])
	}
}

// TestReturningDistinguishesNULLFromEmptyName checks the guarantee that what was
// not selected is never fabricated: a NULL column surfaces as nil while an
// empty non-null column keeps its real empty value.
func TestReturningDistinguishesNULLFromEmpty(t *testing.T) {
	client := setup(t)

	nullUser := newUser("null@x.io")
	nullUser.Bio = nil
	must(t, client.Users.Create(ctx, &nullUser))
	row, err := client.Users.FindFirst().
		Where(db.Users.Email.Eq("null@x.io")).
		Returning(db.Users.Email, db.Users.Bio).
		One(ctx)
	must(t, err)
	if row[1] != nil {
		t.Errorf("NULL bio must surface as nil, got %v (%T)", row[1], row[1])
	}

	emptyUser := newUser("empty@x.io")
	emptyUser.Name = "" // name is NOT NULL: "" is a stored value, not absence
	must(t, client.Users.Create(ctx, &emptyUser))
	row, err = client.Users.FindFirst().
		Where(db.Users.Email.Eq("empty@x.io")).
		Returning(db.Users.Name).
		One(ctx)
	must(t, err)
	if name, ok := row[0].(string); !ok || name != "" {
		t.Errorf("empty NOT NULL name must come back as its real value \"\", got %v (%T)", row[0], row[0])
	}
}

func TestReturningOneIsErrNotFoundWhenAbsent(t *testing.T) {
	client := setup(t)
	if _, err := client.Users.FindFirst().
		Where(db.Users.Email.Eq("nobody@x.io")).
		Returning(db.Users.ID).
		One(ctx); !errors.Is(err, lathe.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
