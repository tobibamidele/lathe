// Package schema describes a small publishing platform: users write posts,
// posts have tags and comments, users place orders.
package schema

import s "github.com/tobibamidele/lathe/schema"

var Schema = s.New(s.Config{
	Dialect:    s.Postgres,
	Output:     "db",
	Migrations: "migrations",
	Driver:     "github.com/lib/pq",
},
	s.Table("users",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.VarChar("email", 255).Unique(),
		s.VarChar("name", 120),
		s.Text("bio").Nullable(),
		s.Text("password_hash"),
		s.VarChar("api_key", 64).Nullable().Unique(),
		s.Enum("role", "admin", "editor", "reader").Default("reader"),
		s.Bool("active").Default(true),
		s.Int("karma").Default(0),
		s.JSON("settings").Nullable(),
		s.Timestamp("created_at").DefaultNow(),
	),

	s.Table("posts",
		s.UUID("id").PrimaryKey().DefaultUUID(),
		s.BigInt("author_id").References("users", "id").OnDelete(s.Cascade),
		s.VarChar("title", 200),
		s.VarChar("slug", 200).Nullable(),
		s.Text("body").Nullable(),
		s.Enum("status", "draft", "published", "archived").Default("draft"),
		s.BigInt("views").Default(0),
		s.Timestamp("published_at").Nullable(),
		s.Bytes("cover").Nullable(),

		s.Index("author_id", "status"),
		s.UniqueIndex("author_id", "slug"),
	),

	s.Table("tags",
		s.Int("id").PrimaryKey().AutoIncrement(),
		s.VarChar("name", 50).Unique(),
	),

	// composite primary key: a join table
	s.Table("post_tags",
		s.UUID("post_id").References("posts", "id").OnDelete(s.Cascade),
		s.Int("tag_id").References("tags", "id").OnDelete(s.Cascade),
		s.PrimaryKey("post_id", "tag_id"),
	),

	s.Table("comments",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.UUID("post_id").References("posts", "id").OnDelete(s.Cascade),
		// nullable + SET NULL: comments survive their author being deleted
		s.BigInt("user_id").Nullable().References("users", "id").OnDelete(s.SetNull),
		s.Text("body"),
		s.Timestamp("created_at").DefaultNow(),
	),

	s.Table("orders",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.BigInt("user_id").References("users", "id"),
		s.Money("total"), // DECIMAL(19,4) -> decimal.Decimal
		s.VarChar("currency", 3).Default("USD"),
		s.Timestamp("placed_at").DefaultNow(),
	),
)
