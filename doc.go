// Package lathe is a schema-first ORM for Go.
//
// The schema is written in Go (see the schema package). The lathe command turns
// it into a generated package with one struct per table, typed column
// descriptors and a client, and into SQL migrations. This package is the
// runtime those generated packages build on: typed expressions, query
// builders, transactions, error classification and raw SQL escape hatches.
//
// Queries read like the SQL they produce:
//
//	user, err := client.Users.FindFirst().
//		Where(db.Users.Email.Eq(email), db.Users.Active.Eq(true)).
//		Exclude(db.Users.PasswordHash).
//		One(ctx)
//
// Column descriptors are generic, so db.Users.Email.Eq(42) does not compile.
// Everything is parameterised; values never end up in the SQL text.
package lathe
