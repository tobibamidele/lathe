package lathe_test

import (
	"fmt"

	"github.com/tobibamidele/lathe"
)

// Queries are built from typed columns. Build shows the SQL that would run;
// the terminal methods (All, One, Count, Exec...) render and execute it.
func ExampleModel_FindMany() {
	q := model(true).FindMany().
		Where(Users.Age.Gte(18), Users.Email.HasSuffix("@example.com")).
		Exclude(Users.Password).
		OrderBy(Users.Created.Desc()).
		Limit(10)

	sql, args, _ := q.Build()
	fmt.Println(sql)
	fmt.Println(args...)
	// Output:
	// SELECT "users"."id", "users"."email", "users"."age", "users"."bio", "users"."created" FROM "users" WHERE ("users"."age" >= $1 AND "users"."email" LIKE $2 ESCAPE '!') ORDER BY "users"."created" DESC LIMIT 10
	// 18 %@example.com
}

// Joins, grouping and aggregates use Select; results scan into any struct
// whose field names match the result columns.
func ExampleSelect() {
	q := lathe.Select(Users.ID, lathe.Sum(Orders.Total).As("spent")).
		From(Users).
		Join(Orders, Orders.UserID.EqCol(Users.ID)).
		GroupBy(Users.ID).
		Having(lathe.Sum(Orders.Total).Gt(100)).
		OrderBy(lathe.Sum(Orders.Total).Desc())

	sql, args, _ := q.Build(dialect{})
	fmt.Println(sql)
	fmt.Println(args...)
	// Output:
	// SELECT `users`.`id`, SUM(`orders`.`total`) AS `spent` FROM `users` INNER JOIN `orders` ON `orders`.`user_id` = `users`.`id` GROUP BY `users`.`id` HAVING SUM(`orders`.`total`) > ? ORDER BY SUM(`orders`.`total`) DESC
	// 100
}

// Raw fragments plug into any query; values are always bound, never spliced in.
func ExampleSQL() {
	q := lathe.Select(Users.ID).From(Users).
		Where(lathe.SQL("LOWER(email) = ?", "ann@example.com"), Users.Age.Between(20, 30))
	sql, args, _ := q.Build(dialect{pg: true})
	fmt.Println(sql)
	fmt.Println(args...)
	// Output:
	// SELECT "users"."id" FROM "users" WHERE (LOWER(email) = $1 AND "users"."age" BETWEEN $2 AND $3)
	// ann@example.com 20 30
}
