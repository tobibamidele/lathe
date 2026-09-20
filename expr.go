package lathe

import "strings"

// Expr is a SQL expression: a column, a comparison, a function call, a raw
// fragment... Expressions are built with the helpers in this package and the
// methods of [Column]; they cannot be implemented outside of it.
type Expr interface {
	appendSQL(b *builder)
}

// Selectable is an Expr that can appear in a select list.
type Selectable interface {
	Expr
	appendSelect(b *builder)
}

// ColumnRef is implemented by every [Column].
type ColumnRef interface {
	Selectable
	TableName() string
	ColumnName() string
}

// Source is anything with a table name: the generated table descriptors, for
// example db.Users.
type Source interface {
	TableName() string
}

// ---- column ----

// Column is a typed reference to a table column. T is the Go type of the
// column, so comparisons are checked at compile time.
type Column[T any] struct {
	table, name string
}

// NewColumn creates a column reference. Generated code calls it.
func NewColumn[T any](table, name string) Column[T] {
	return Column[T]{table: table, name: name}
}

// TableName returns the table the column belongs to.
func (c Column[T]) TableName() string { return c.table }

// ColumnName returns the column's name.
func (c Column[T]) ColumnName() string { return c.name }

func (c Column[T]) appendSQL(b *builder)    { b.column(c.table, c.name) }
func (c Column[T]) appendSelect(b *builder) { b.column(c.table, c.name) }

// Eq is column = v.
func (c Column[T]) Eq(v T) Expr { return binary{c, "=", value{v}} }

// Ne is column <> v.
func (c Column[T]) Ne(v T) Expr { return binary{c, "<>", value{v}} }

// Gt is column > v.
func (c Column[T]) Gt(v T) Expr { return binary{c, ">", value{v}} }

// Gte is column >= v.
func (c Column[T]) Gte(v T) Expr { return binary{c, ">=", value{v}} }

// Lt is column < v.
func (c Column[T]) Lt(v T) Expr { return binary{c, "<", value{v}} }

// Lte is column <= v.
func (c Column[T]) Lte(v T) Expr { return binary{c, "<=", value{v}} }

// EqCol is column = other, for joins and column to column comparisons.
func (c Column[T]) EqCol(other ColumnRef) Expr { return binary{c, "=", other} }

// NeCol is column <> other.
func (c Column[T]) NeCol(other ColumnRef) Expr { return binary{c, "<>", other} }

// GtCol is column > other.
func (c Column[T]) GtCol(other ColumnRef) Expr { return binary{c, ">", other} }

// LtCol is column < other.
func (c Column[T]) LtCol(other ColumnRef) Expr { return binary{c, "<", other} }

// In is column IN (values...). An empty list matches nothing.
func (c Column[T]) In(values ...T) Expr { return inList{col: c, vals: toAny(values)} }

// NotIn is column NOT IN (values...). An empty list matches everything.
func (c Column[T]) NotIn(values ...T) Expr { return inList{col: c, vals: toAny(values), not: true} }

// InQuery is column IN (subquery).
func (c Column[T]) InQuery(q *SelectQuery) Expr { return binary{c, "IN", q} }

// Between is column BETWEEN lo AND hi.
func (c Column[T]) Between(lo, hi T) Expr { return between{col: c, lo: lo, hi: hi} }

// IsNull is column IS NULL.
func (c Column[T]) IsNull() Expr { return nullCheck{col: c} }

// IsNotNull is column IS NOT NULL.
func (c Column[T]) IsNotNull() Expr { return nullCheck{col: c, not: true} }

// Like is column LIKE pattern. % and _ in pattern are wildcards.
func (c Column[T]) Like(pattern string) Expr { return like{col: c, pattern: pattern} }

// NotLike is column NOT LIKE pattern.
func (c Column[T]) NotLike(pattern string) Expr { return like{col: c, pattern: pattern, not: true} }

// ILike is a case-insensitive Like. It is ILIKE on PostgreSQL and
// LOWER(column) LIKE LOWER(pattern) elsewhere.
func (c Column[T]) ILike(pattern string) Expr { return like{col: c, pattern: pattern, fold: true} }

// Contains matches values that contain s, treating s literally.
func (c Column[T]) Contains(s string) Expr {
	return like{col: c, pattern: "%" + escapeLike(s) + "%", escape: true}
}

// HasPrefix matches values that start with s, treating s literally.
func (c Column[T]) HasPrefix(s string) Expr {
	return like{col: c, pattern: escapeLike(s) + "%", escape: true}
}

// HasSuffix matches values that end with s, treating s literally.
func (c Column[T]) HasSuffix(s string) Expr {
	return like{col: c, pattern: "%" + escapeLike(s), escape: true}
}

// Asc orders ascending by the column.
func (c Column[T]) Asc() Order { return Order{e: c} }

// Desc orders descending by the column.
func (c Column[T]) Desc() Order { return Order{e: c, desc: true} }

// As gives the column an alias in a select list.
func (c Column[T]) As(alias string) Aliased { return Aliased{e: c, alias: alias} }

// Set assigns v to the column in an UPDATE.
func (c Column[T]) Set(v T) Assignment { return Assignment{col: c, val: value{v}} }

// SetNull assigns NULL to the column in an UPDATE.
func (c Column[T]) SetNull() Assignment { return Assignment{col: c, val: rawExpr{sql: "NULL"}} }

// SetExpr assigns an expression to the column in an UPDATE.
func (c Column[T]) SetExpr(e Expr) Assignment { return Assignment{col: c, val: e} }

// Incr is column = column + by, evaluated by the database (no read-modify-write
// race).
func Incr[T any](c Column[T], by T) Assignment {
	return Assignment{col: c, val: binary{c, "+", value{by}}}
}

// Decr is column = column - by.
func Decr[T any](c Column[T], by T) Assignment {
	return Assignment{col: c, val: binary{c, "-", value{by}}}
}

func toAny[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// escapeLike escapes LIKE wildcards using ! as the escape character, which is
// portable (backslash is special in MySQL string literals).
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// ---- assignments and ordering ----

// Assignment sets a column in an UPDATE.
type Assignment struct {
	col ColumnRef
	val Expr
}

// Order is an ORDER BY term.
type Order struct {
	e    Expr
	desc bool
}

func (o Order) appendOrder(b *builder) {
	o.e.appendSQL(b)
	if o.desc {
		b.write(" DESC")
	}
}

// Aliased is a select list expression with an AS alias.
type Aliased struct {
	e     Expr
	alias string
}

func (a Aliased) appendSQL(b *builder) { a.e.appendSQL(b) }
func (a Aliased) appendSelect(b *builder) {
	a.e.appendSQL(b)
	b.write(" AS ")
	b.ident(a.alias)
}

// ---- generic expression nodes ----

type value struct{ v any }

func (v value) appendSQL(b *builder) { b.arg(v.v) }

type binary struct {
	l  Expr
	op string
	r  Expr
}

func (e binary) appendSQL(b *builder) {
	e.l.appendSQL(b)
	b.write(" " + e.op + " ")
	e.r.appendSQL(b)
}

type inList struct {
	col  Expr
	vals []any
	not  bool
}

func (e inList) appendSQL(b *builder) {
	if len(e.vals) == 0 {
		if e.not {
			b.write("1=1")
		} else {
			b.write("1=0")
		}
		return
	}
	e.col.appendSQL(b)
	if e.not {
		b.write(" NOT")
	}
	b.write(" IN (")
	for i, v := range e.vals {
		if i > 0 {
			b.write(", ")
		}
		b.arg(v)
	}
	b.write(")")
}

type between struct {
	col    Expr
	lo, hi any
}

func (e between) appendSQL(b *builder) {
	e.col.appendSQL(b)
	b.write(" BETWEEN ")
	b.arg(e.lo)
	b.write(" AND ")
	b.arg(e.hi)
}

type nullCheck struct {
	col Expr
	not bool
}

func (e nullCheck) appendSQL(b *builder) {
	e.col.appendSQL(b)
	if e.not {
		b.write(" IS NOT NULL")
	} else {
		b.write(" IS NULL")
	}
}

type like struct {
	col     Expr
	pattern string
	not     bool
	fold    bool // case-insensitive
	escape  bool // pattern was escaped with !
}

func (e like) appendSQL(b *builder) {
	fold := e.fold && !b.d.NativeILike()
	if fold {
		b.write("LOWER(")
		e.col.appendSQL(b)
		b.write(")")
	} else {
		e.col.appendSQL(b)
	}
	if e.not {
		b.write(" NOT")
	}
	switch {
	case e.fold && b.d.NativeILike():
		b.write(" ILIKE ")
	default:
		b.write(" LIKE ")
	}
	if fold {
		b.write("LOWER(")
		b.arg(e.pattern)
		b.write(")")
	} else {
		b.arg(e.pattern)
	}
	if e.escape {
		b.write(" ESCAPE '!'")
	}
}

type logical struct {
	op    string // "AND" or "OR"
	parts []Expr
}

func (e logical) appendSQL(b *builder) {
	switch len(e.parts) {
	case 0:
		if e.op == "AND" {
			b.write("1=1")
		} else {
			b.write("1=0")
		}
		return
	case 1:
		e.parts[0].appendSQL(b)
		return
	}
	b.write("(")
	for i, p := range e.parts {
		if i > 0 {
			b.write(" " + e.op + " ")
		}
		p.appendSQL(b)
	}
	b.write(")")
}

// And combines expressions with AND. And() with no arguments is true.
func And(exprs ...Expr) Expr { return logical{op: "AND", parts: exprs} }

// Or combines expressions with OR. Or() with no arguments is false.
func Or(exprs ...Expr) Expr { return logical{op: "OR", parts: exprs} }

type not struct{ e Expr }

func (e not) appendSQL(b *builder) {
	b.write("NOT (")
	e.e.appendSQL(b)
	b.write(")")
}

// Not negates an expression.
func Not(e Expr) Expr { return not{e} }

type exists struct{ q *SelectQuery }

func (e exists) appendSQL(b *builder) {
	b.write("EXISTS ")
	e.q.appendSQL(b)
}

// Exists is EXISTS (subquery).
func Exists(q *SelectQuery) Expr { return exists{q} }

// ---- raw fragments ----

// RawExpr is a fragment of SQL with bind parameters.
type RawExpr struct {
	sql  string
	args []any
}

// SQL embeds a raw SQL fragment in a query. Use ? for bind parameters (?? for
// a literal question mark). Values are always bound, never interpolated:
//
//	Where(lathe.SQL("lower(email) = ?", strings.ToLower(email)))
//
// Identifiers cannot be bound; quote them yourself and never build them from
// untrusted input.
func SQL(sql string, args ...any) RawExpr { return RawExpr{sql: sql, args: args} }

func (r RawExpr) appendSQL(b *builder)    { b.rawSQL(r.sql, r.args) }
func (r RawExpr) appendSelect(b *builder) { b.rawSQL(r.sql, r.args) }

// As gives the fragment an alias in a select list.
func (r RawExpr) As(alias string) Aliased { return Aliased{e: r, alias: alias} }

// Asc orders ascending by the fragment.
func (r RawExpr) Asc() Order { return Order{e: r} }

// Desc orders descending by the fragment.
func (r RawExpr) Desc() Order { return Order{e: r, desc: true} }

// rawExpr is the internal, argument-free form.
type rawExpr struct{ sql string }

func (r rawExpr) appendSQL(b *builder) { b.write(r.sql) }

// ---- functions ----

// Func is a SQL function call such as COUNT(*) or SUM(column).
type Func struct {
	name     string
	args     []Expr
	star     bool
	distinct bool
}

func (f Func) appendSQL(b *builder) {
	b.write(f.name + "(")
	switch {
	case f.star:
		b.write("*")
	default:
		if f.distinct {
			b.write("DISTINCT ")
		}
		for i, a := range f.args {
			if i > 0 {
				b.write(", ")
			}
			a.appendSQL(b)
		}
	}
	b.write(")")
}

func (f Func) appendSelect(b *builder) { f.appendSQL(b) }

// As gives the call an alias in a select list.
func (f Func) As(alias string) Aliased { return Aliased{e: f, alias: alias} }

// Asc orders ascending by the call's result.
func (f Func) Asc() Order { return Order{e: f} }

// Desc orders descending by the call's result.
func (f Func) Desc() Order { return Order{e: f, desc: true} }

// Eq is f = v.
func (f Func) Eq(v any) Expr { return binary{f, "=", value{v}} }

// Ne is f <> v.
func (f Func) Ne(v any) Expr { return binary{f, "<>", value{v}} }

// Gt is f > v (useful in Having).
func (f Func) Gt(v any) Expr { return binary{f, ">", value{v}} }

// Gte is f >= v.
func (f Func) Gte(v any) Expr { return binary{f, ">=", value{v}} }

// Lt is f < v.
func (f Func) Lt(v any) Expr { return binary{f, "<", value{v}} }

// Lte is f <= v.
func (f Func) Lte(v any) Expr { return binary{f, "<=", value{v}} }

// CountAll is COUNT(*).
func CountAll() Func { return Func{name: "COUNT", star: true} }

// Count is COUNT(e).
func Count(e Expr) Func { return Func{name: "COUNT", args: []Expr{e}} }

// CountDistinct is COUNT(DISTINCT e).
func CountDistinct(e Expr) Func { return Func{name: "COUNT", args: []Expr{e}, distinct: true} }

// Sum is SUM(e).
func Sum(e Expr) Func { return Func{name: "SUM", args: []Expr{e}} }

// Avg is AVG(e).
func Avg(e Expr) Func { return Func{name: "AVG", args: []Expr{e}} }

// Min is MIN(e).
func Min(e Expr) Func { return Func{name: "MIN", args: []Expr{e}} }

// Max is MAX(e).
func Max(e Expr) Func { return Func{name: "MAX", args: []Expr{e}} }

// Lower is LOWER(e).
func Lower(e Expr) Func { return Func{name: "LOWER", args: []Expr{e}} }

// Upper is UPPER(e).
func Upper(e Expr) Func { return Func{name: "UPPER", args: []Expr{e}} }

// Coalesce is COALESCE(e, fallback).
func Coalesce(e Expr, fallback any) Func {
	return Func{name: "COALESCE", args: []Expr{e, value{fallback}}}
}
