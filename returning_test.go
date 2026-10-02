package lathe_test

import (
	"strings"
	"testing"

	"github.com/tobibamidele/lathe"
)

func TestReturningBuildSelectsOnlyTheRequestedColumns(t *testing.T) {
	sql, args, err := model(true).FindFirst().
		Where(Users.Email.Eq("a@b.c")).
		Returning(Users.ID, Users.Email, Users.Bio).
		Build()
	check(t, sql, args, err,
		`SELECT "users"."id", "users"."email", "users"."bio" FROM "users" `+
			`WHERE "users"."email" = $1 LIMIT 1`,
		"a@b.c")
}

func TestReturningPreservesColumnOrder(t *testing.T) {
	sql, args, err := model(true).FindMany().Returning(Users.Bio, Users.ID, Users.Email).Build()
	check(t, sql, args, err, `SELECT "users"."bio", "users"."id", "users"."email" FROM "users"`)
}

func TestReturningReplacesSelect(t *testing.T) {
	sql, args, err := model(true).FindMany().Select(Users.Email).Returning(Users.ID).Build()
	check(t, sql, args, err, `SELECT "users"."id" FROM "users"`)
}

func TestReturningKeepsWhereOrderLimitOffset(t *testing.T) {
	sql, args, err := model(false).FindMany().
		Where(Users.Age.Gte(18)).
		Returning(Users.Email).
		OrderBy(Users.Email.Asc()).
		Limit(5).Offset(10).
		Build()
	check(t, sql, args, err,
		"SELECT `users`.`email` FROM `users` WHERE `users`.`age` >= ? "+
			"ORDER BY `users`.`email` LIMIT 5 OFFSET 10",
		int32(18))
}

func TestReturningValidation(t *testing.T) {
	if _, _, err := model(true).FindMany().Returning().Build(); err == nil || !strings.Contains(err.Error(), "Returning needs at least one column") {
		t.Fatalf("empty Returning must fail, got %v", err)
	}
	if _, _, err := model(true).FindMany().Returning(lathe.NewColumn[int]("users", "nope")).Build(); err == nil || !strings.Contains(err.Error(), "no column nope") {
		t.Fatalf("unknown column must fail, got %v", err)
	}
	if _, _, err := model(true).FindMany().Returning(Orders.ID, Users.ID).Build(); err == nil || !strings.Contains(err.Error(), "does not belong to table users") {
		t.Fatalf("column of another table must fail, got %v", err)
	}

	rel := lathe.BelongsTo[User, User]("self", lathe.BelongsToSpec[User, User]{
		Source:     usersSpec,
		Target:     usersSpec,
		SourceCols: []string{"id"},
		TargetCols: []string{"id"},
		Set:        func(*User, *User) {},
	})
	if _, _, err := model(true).FindMany().With(rel).Returning(Users.ID).Build(); err == nil || !strings.Contains(err.Error(), "cannot be combined with With") {
		t.Fatalf("Returning with With must fail, got %v", err)
	}
}
