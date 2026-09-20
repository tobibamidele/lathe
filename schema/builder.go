package schema

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Config controls how lathe treats a schema.
type Config struct {
	// Dialect is the target database engine. Required.
	Dialect Dialect `json:"dialect"`
	// Output is the directory (relative to the module root) that receives the
	// generated Go package. Defaults to "db".
	Output string `json:"output,omitempty"`
	// Package is the generated package name. Defaults to the last element of
	// Output.
	Package string `json:"package,omitempty"`
	// Migrations is the directory that holds SQL migrations. Defaults to
	// "migrations".
	Migrations string `json:"migrations,omitempty"`
	// Driver is the import path of the database/sql driver the CLI links when
	// it runs migrations for you (for example "github.com/lib/pq"). Optional;
	// a common default is chosen per dialect.
	Driver string `json:"driver,omitempty"`
}

// Export is what the CLI reads from a user's schema package.
type Export struct {
	Config   Config    `json:"config"`
	Snapshot *Snapshot `json:"snapshot"`
}

// Schema is the root value a project defines (conventionally as
// `var Schema = schema.New(...)`).
type Schema struct {
	cfg    Config
	tables []*TableBuilder
}

// New declares a schema.
func New(cfg Config, tables ...*TableBuilder) *Schema {
	return &Schema{cfg: cfg, tables: tables}
}

// Item is anything that can appear inside [Table]: columns, indexes, foreign
// keys and primary key declarations.
type Item interface {
	applyTo(*TableBuilder)
}

// TableBuilder builds a [TableDef].
type TableBuilder struct {
	name        string
	model       string
	renamedFrom string
	cols        []*ColumnBuilder
	pk          []string
	pkDeclared  bool
	indexes     []*IndexBuilder
	fks         []*ForeignKeyBuilder
	errs        []error
}

// Table declares a table.
func Table(name string, items ...Item) *TableBuilder {
	t := &TableBuilder{name: name}
	for _, it := range items {
		if it == nil {
			t.errs = append(t.errs, errors.New("nil item"))
			continue
		}
		it.applyTo(t)
	}
	return t
}

// Add appends more items to the table. It is handy when parts of a table are
// conditional.
func (t *TableBuilder) Add(items ...Item) *TableBuilder {
	for _, it := range items {
		if it == nil {
			t.errs = append(t.errs, errors.New("nil item"))
			continue
		}
		it.applyTo(t)
	}
	return t
}

// Model overrides the generated Go struct name for the table.
func (t *TableBuilder) Model(name string) *TableBuilder { t.model = name; return t }

// RenamedFrom tells the migration planner that this table used to be called
// old, so the change becomes a rename instead of a drop and create. Remove the
// call once the migration has been generated.
func (t *TableBuilder) RenamedFrom(old string) *TableBuilder { t.renamedFrom = old; return t }

// ---- columns ----

// ColumnBuilder builds a [ColumnDef].
type ColumnBuilder struct {
	col      ColumnDef
	pk       bool
	unique   bool
	index    bool
	ref      *ForeignKeyBuilder
	buildErr []error
}

func newCol(name string, t Type) *ColumnBuilder {
	return &ColumnBuilder{col: ColumnDef{Name: name, Type: t}}
}

func (c *ColumnBuilder) applyTo(t *TableBuilder) { t.cols = append(t.cols, c) }

// SmallInt declares a 16-bit integer column.
func SmallInt(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindSmallInt}) }

// Int declares a 32-bit integer column.
func Int(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindInt}) }

// BigInt declares a 64-bit integer column.
func BigInt(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindBigInt}) }

// Real declares a single precision float column.
func Real(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindReal}) }

// Double declares a double precision float column.
func Double(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindDouble}) }

// Decimal declares an exact numeric column. It maps to decimal.Decimal
// (github.com/shopspring/decimal) in generated code.
//
// SQLite has no exact numeric type: values are stored with NUMERIC affinity,
// which is exact for about 15 significant digits.
func Decimal(name string, precision, scale int) *ColumnBuilder {
	return newCol(name, Type{Kind: KindDecimal, Precision: precision, Scale: scale})
}

// Money declares a monetary amount: DECIMAL(19, 4), generated as
// decimal.Decimal. Store the currency in a separate column.
func Money(name string) *ColumnBuilder { return Decimal(name, 19, 4) }

// Bool declares a boolean column.
func Bool(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindBool}) }

// VarChar declares a bounded string column.
func VarChar(name string, length int) *ColumnBuilder {
	return newCol(name, Type{Kind: KindVarChar, Length: length})
}

// Text declares an unbounded string column.
func Text(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindText}) }

// UUID declares a UUID column, generated as uuid.UUID (github.com/google/uuid).
func UUID(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindUUID}) }

// Timestamp declares a point-in-time column (timestamptz on PostgreSQL,
// DATETIME(6) on MySQL, DATETIME on SQLite), generated as time.Time.
func Timestamp(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindTimestamp}) }

// Date declares a calendar date column, generated as time.Time.
func Date(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindDate}) }

// JSON declares a JSON document column, generated as lathe.JSON.
func JSON(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindJSON}) }

// Bytes declares a binary column, generated as []byte.
func Bytes(name string) *ColumnBuilder { return newCol(name, Type{Kind: KindBytes}) }

// Enum declares a column restricted to the given string values. Generated code
// gets a named string type with a constant per value.
func Enum(name string, values ...string) *ColumnBuilder {
	return newCol(name, Type{Kind: KindEnum, Values: append([]string(nil), values...)})
}

// PrimaryKey marks the column as (part of) the primary key.
func (c *ColumnBuilder) PrimaryKey() *ColumnBuilder { c.pk = true; return c }

// Nullable allows NULL. Columns are NOT NULL unless declared otherwise, and
// nullable columns are generated as pointers.
func (c *ColumnBuilder) Nullable() *ColumnBuilder { c.col.Nullable = true; return c }

// AutoIncrement makes the database assign increasing integers. The column must
// be an integer and the table's only primary key column.
func (c *ColumnBuilder) AutoIncrement() *ColumnBuilder { c.col.AutoIncrement = true; return c }

// Unique adds a unique index on this column.
func (c *ColumnBuilder) Unique() *ColumnBuilder { c.unique = true; return c }

// Index adds a plain index on this column.
func (c *ColumnBuilder) Index() *ColumnBuilder { c.index = true; return c }

// Default sets a constant default. Accepts strings, bools, integers, floats and
// fmt.Stringer values such as decimal.Decimal.
func (c *ColumnBuilder) Default(v any) *ColumnBuilder {
	d, err := literalDefault(v)
	if err != nil {
		c.buildErr = append(c.buildErr, fmt.Errorf("column %q: %w", c.col.Name, err))
		return c
	}
	c.col.Default = d
	return c
}

// DefaultNow defaults to the current time.
func (c *ColumnBuilder) DefaultNow() *ColumnBuilder {
	c.col.Default = &Default{Kind: DefaultNow}
	return c
}

// DefaultUUID defaults to a random UUID generated by the database.
func (c *ColumnBuilder) DefaultUUID() *ColumnBuilder {
	c.col.Default = &Default{Kind: DefaultUUID}
	return c
}

// DefaultExpr defaults to a raw SQL expression. It is emitted verbatim, so it
// is not portable across dialects.
func (c *ColumnBuilder) DefaultExpr(sql string) *ColumnBuilder {
	c.col.Default = &Default{Kind: DefaultExpr, Value: sql}
	return c
}

// References adds a single column foreign key to table(column).
func (c *ColumnBuilder) References(table, column string) *ColumnBuilder {
	c.ref = &ForeignKeyBuilder{fk: ForeignKeyDef{Columns: []string{c.col.Name}, RefTable: table, RefColumns: []string{column}}}
	return c
}

// OnDelete sets the referential action for deletes of the referenced row.
// It must follow References.
func (c *ColumnBuilder) OnDelete(a Action) *ColumnBuilder {
	if c.ref == nil {
		c.buildErr = append(c.buildErr, fmt.Errorf("column %q: OnDelete requires References", c.col.Name))
		return c
	}
	c.ref.fk.OnDelete = a
	return c
}

// OnUpdate sets the referential action for updates of the referenced key.
// It must follow References.
func (c *ColumnBuilder) OnUpdate(a Action) *ColumnBuilder {
	if c.ref == nil {
		c.buildErr = append(c.buildErr, fmt.Errorf("column %q: OnUpdate requires References", c.col.Name))
		return c
	}
	c.ref.fk.OnUpdate = a
	return c
}

// Field overrides the generated Go field name.
func (c *ColumnBuilder) Field(name string) *ColumnBuilder { c.col.Field = name; return c }

// RenamedFrom tells the migration planner that this column used to be called
// old. Remove the call once the migration has been generated.
func (c *ColumnBuilder) RenamedFrom(old string) *ColumnBuilder { c.col.RenamedFrom = old; return c }

func literalDefault(v any) (*Default, error) {
	switch x := v.(type) {
	case string:
		return &Default{Kind: DefaultLiteral, Lit: LitString, Value: x}, nil
	case bool:
		return &Default{Kind: DefaultLiteral, Lit: LitBool, Value: strconv.FormatBool(x)}, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return &Default{Kind: DefaultLiteral, Lit: LitNumber, Value: fmt.Sprint(x)}, nil
	case float32:
		return &Default{Kind: DefaultLiteral, Lit: LitNumber, Value: strconv.FormatFloat(float64(x), 'f', -1, 32)}, nil
	case float64:
		return &Default{Kind: DefaultLiteral, Lit: LitNumber, Value: strconv.FormatFloat(x, 'f', -1, 64)}, nil
	case fmt.Stringer:
		s := x.String()
		if numberRE.MatchString(s) {
			return &Default{Kind: DefaultLiteral, Lit: LitNumber, Value: s}, nil
		}
		return &Default{Kind: DefaultLiteral, Lit: LitString, Value: s}, nil
	}
	return nil, fmt.Errorf("unsupported default value of type %T", v)
}

// ---- indexes ----

// IndexBuilder builds an [IndexDef].
type IndexBuilder struct{ ix IndexDef }

// Index declares an index over the given columns.
func Index(columns ...string) *IndexBuilder {
	return &IndexBuilder{ix: IndexDef{Columns: append([]string(nil), columns...)}}
}

// UniqueIndex declares a unique index over the given columns.
func UniqueIndex(columns ...string) *IndexBuilder {
	i := Index(columns...)
	i.ix.Unique = true
	return i
}

// Unique makes the index unique.
func (i *IndexBuilder) Unique() *IndexBuilder { i.ix.Unique = true; return i }

// Name overrides the generated index name.
func (i *IndexBuilder) Name(name string) *IndexBuilder { i.ix.Name = name; return i }

func (i *IndexBuilder) applyTo(t *TableBuilder) { t.indexes = append(t.indexes, i) }

// ---- foreign keys ----

// ForeignKeyBuilder builds a [ForeignKeyDef].
type ForeignKeyBuilder struct {
	fk         ForeignKeyDef
	referenced bool
}

// ForeignKey declares a (possibly composite) foreign key on the given columns.
// Follow it with References.
func ForeignKey(columns ...string) *ForeignKeyBuilder {
	return &ForeignKeyBuilder{fk: ForeignKeyDef{Columns: append([]string(nil), columns...)}}
}

// References sets the referenced table and columns.
func (f *ForeignKeyBuilder) References(table string, columns ...string) *ForeignKeyBuilder {
	f.fk.RefTable = table
	f.fk.RefColumns = append([]string(nil), columns...)
	f.referenced = true
	return f
}

// OnDelete sets the action taken when the referenced row is deleted.
func (f *ForeignKeyBuilder) OnDelete(a Action) *ForeignKeyBuilder { f.fk.OnDelete = a; return f }

// OnUpdate sets the action taken when the referenced key is updated.
func (f *ForeignKeyBuilder) OnUpdate(a Action) *ForeignKeyBuilder { f.fk.OnUpdate = a; return f }

// Name overrides the generated constraint name.
func (f *ForeignKeyBuilder) Name(name string) *ForeignKeyBuilder { f.fk.Name = name; return f }

func (f *ForeignKeyBuilder) applyTo(t *TableBuilder) { t.fks = append(t.fks, f) }

// ---- composite primary key ----

type pkItem []string

// PrimaryKey declares a (composite) primary key. Use it instead of marking
// individual columns with PrimaryKey().
func PrimaryKey(columns ...string) Item { return pkItem(columns) }

func (p pkItem) applyTo(t *TableBuilder) {
	if t.pkDeclared {
		t.errs = append(t.errs, errors.New("primary key declared more than once"))
		return
	}
	t.pkDeclared = true
	t.pk = append([]string(nil), p...)
}

// ---- assembly ----

// Export validates the schema and returns its normalised form.
func (s *Schema) Export() (*Export, error) {
	cfg := s.cfg
	if !cfg.Dialect.Valid() {
		return nil, fmt.Errorf("schema: unsupported dialect %q (want postgres, mysql or sqlite)", cfg.Dialect)
	}
	if cfg.Output == "" {
		cfg.Output = "db"
	}
	if cfg.Migrations == "" {
		cfg.Migrations = "migrations"
	}
	if cfg.Package == "" {
		cfg.Package = path.Base(cfg.Output)
	}

	snap := &Snapshot{Version: SnapshotVersion, Dialect: cfg.Dialect}
	var errs []error
	for _, tb := range s.tables {
		t, err := tb.build()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		snap.Tables = append(snap.Tables, t)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	snap.sort()
	if err := Validate(snap); err != nil {
		return nil, err
	}
	return &Export{Config: cfg, Snapshot: snap}, nil
}

func (tb *TableBuilder) build() (*TableDef, error) {
	errs := append([]error(nil), tb.errs...)
	t := &TableDef{Name: tb.name, Model: tb.model, RenamedFrom: tb.renamedFrom}
	prefix := fmt.Sprintf("table %q: ", tb.name)

	var pk []string
	for _, cb := range tb.cols {
		for _, e := range cb.buildErr {
			errs = append(errs, errors.New(prefix+e.Error()))
		}
		c := cb.col
		if cb.pk {
			pk = append(pk, c.Name)
			c.Nullable = false
		}
		t.Columns = append(t.Columns, &c)
	}
	switch {
	case tb.pkDeclared && len(pk) > 0:
		errs = append(errs, errors.New(prefix+"use either PrimaryKey() on columns or a PrimaryKey(...) item, not both"))
	case tb.pkDeclared:
		t.PrimaryKey = tb.pk
		for _, name := range tb.pk {
			if c := t.Column(name); c != nil {
				c.Nullable = false
			}
		}
	default:
		t.PrimaryKey = pk
	}

	for _, cb := range tb.cols {
		name := cb.col.Name
		if cb.unique {
			t.Indexes = append(t.Indexes, &IndexDef{Name: AutoName("uq", tb.name, name), Columns: []string{name}, Unique: true})
		}
		if cb.index {
			t.Indexes = append(t.Indexes, &IndexDef{Name: AutoName("idx", tb.name, name), Columns: []string{name}})
		}
		if cb.ref != nil {
			fk := cb.ref.fk
			fk.Name = AutoName("fk", tb.name, name)
			t.ForeignKeys = append(t.ForeignKeys, &fk)
		}
	}
	for _, ib := range tb.indexes {
		ix := ib.ix
		if ix.Name == "" {
			prefix := "idx"
			if ix.Unique {
				prefix = "uq"
			}
			ix.Name = AutoName(prefix, tb.name, ix.Columns...)
		}
		t.Indexes = append(t.Indexes, &ix)
	}
	for _, fb := range tb.fks {
		fk := fb.fk
		if !fb.referenced {
			errs = append(errs, fmt.Errorf("%sforeign key on (%s) has no References(...)", prefix, strings.Join(fk.Columns, ", ")))
			continue
		}
		if fk.Name == "" {
			fk.Name = AutoName("fk", tb.name, fk.Columns...)
		}
		t.ForeignKeys = append(t.ForeignKeys, &fk)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return t, nil
}

// maxIdent is the smallest identifier limit of the supported databases
// (PostgreSQL: 63 bytes, MySQL: 64).
const maxIdent = 63

// AutoName builds a deterministic constraint or index name that fits every
// supported database.
func AutoName(prefix, table string, cols ...string) string {
	full := prefix + "_" + table + "_" + strings.Join(cols, "_")
	if len(full) <= maxIdent {
		return full
	}
	sum := sha1.Sum([]byte(full))
	return full[:maxIdent-9] + "_" + hex.EncodeToString(sum[:])[:8]
}
