// Package schema is the schema used by the end-to-end tests. The dialect comes
// from LATHE_DIALECT so the same schema runs against PostgreSQL, MySQL and
// SQLite. LATHE_STAGE=2 selects an evolved version that exercises migrations
// against existing data.
package schema

import (
	"os"

	s "github.com/tobibamidele/lathe/schema"
)

var (
	dialect = s.Dialect(os.Getenv("LATHE_DIALECT"))
	stage2  = os.Getenv("LATHE_STAGE") == "2"
)

var drivers = map[s.Dialect]string{
	s.Postgres: "github.com/lib/pq",
	s.MySQL:    "github.com/go-sql-driver/mysql",
	s.SQLite:   "github.com/mattn/go-sqlite3",
}

// Schema is the entry point lathe looks for.
var Schema = s.New(s.Config{
	Dialect:    dialect,
	Output:     "db",
	Migrations: "migrations/" + string(dialect),
	Driver:     drivers[dialect],
}, tables()...)

func tables() []*s.TableBuilder {
	users := s.Table("users",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.VarChar("email", 255).Unique(),
		s.Text("bio").Nullable(),
		s.Text("password_hash"),
		s.VarChar("api_key", 64).Nullable(),
		s.Bool("active").Default(true),
		s.Int("age").Nullable(),
		s.JSON("settings").Nullable(),
		s.Timestamp("created_at").DefaultNow(),
	)
	posts := s.Table("posts",
		s.UUID("id").PrimaryKey().DefaultUUID(),
		s.BigInt("author_id").References("users", "id").OnDelete(s.Cascade).Index(),
		s.VarChar("title", 200),
		s.Text("body").Nullable(),
		s.Timestamp("published_at").Nullable(),
		s.Bytes("cover").Nullable(),
	)
	tags := s.Table("tags",
		s.Int("id").PrimaryKey().AutoIncrement(),
		s.VarChar("name", 50).Unique(),
	)
	postTags := s.Table("post_tags",
		s.UUID("post_id").References("posts", "id").OnDelete(s.Cascade),
		s.Int("tag_id").References("tags", "id").OnDelete(s.Cascade),
		s.PrimaryKey("post_id", "tag_id"),
	)

	profiles := s.Table("profiles", // one-to-one: the foreign key is the primary key
		s.BigInt("user_id").PrimaryKey().References("users", "id").OnDelete(s.Cascade),
		s.VarChar("website", 200).Nullable(),
	)
	messages := s.Table("messages", // two foreign keys to the same table
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.BigInt("sender_id").References("users", "id").OnDelete(s.Cascade),
		s.BigInt("recipient_id").References("users", "id").OnDelete(s.Cascade),
		s.Text("body"),
	)
	orgs := s.Table("orgs", // composite primary key ...
		s.Int("tenant").PrimaryKey(), s.Int("id").PrimaryKey(), s.VarChar("name", 50),
	)
	members := s.Table("members", // ... referenced by a composite foreign key
		s.Int("id").PrimaryKey().AutoIncrement(),
		s.Int("org_tenant"), s.Int("org_id"), s.VarChar("name", 50),
		s.ForeignKey("org_tenant", "org_id").References("orgs", "tenant", "id"),
	)
	common := []*s.TableBuilder{profiles, messages, orgs, members}

	if !stage2 {
		users.Add(
			s.VarChar("name", 120),
			s.Enum("role", "admin", "member", "guest").Default("member"),
			s.Money("balance").Default("0"),
		)
		posts.Add(s.Int("views").Default(0))
		return append([]*s.TableBuilder{users, posts, tags, postTags}, common...)
	}

	// Stage 2: add a column, rename + widen a column, add an enum value,
	// change a default, add indexes, retype a column, rename a table, drop a
	// table and create a new one that references the others.
	users.Add(
		s.VarChar("full_name", 200).RenamedFrom("name"),
		s.VarChar("nickname", 50).Nullable(),
		s.Enum("role", "admin", "moderator", "member", "guest").Default("member"),
		s.Decimal("balance", 20, 4).Default("0"),
		s.Index("created_at"),
	)
	posts.Add(
		s.BigInt("views").Default(0),
		s.VarChar("slug", 200).Nullable().Unique(),
	)
	comments := s.Table("comments",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.UUID("post_id").References("posts", "id").OnDelete(s.Cascade),
		s.BigInt("user_id").Nullable().References("users", "id").OnDelete(s.Cascade), // nullable: anonymous comments
		s.Text("body"),
		s.Timestamp("created_at").DefaultNow(),
	)
	labels := s.Table("labels",
		s.Int("id").PrimaryKey().AutoIncrement(),
		s.VarChar("name", 50).Unique(),
	).RenamedFrom("tags")
	return append([]*s.TableBuilder{users, posts, comments, labels}, common...)
}
