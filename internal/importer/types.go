package importer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

// typeInfo is the result of reading a SQL column type.
type typeInfo struct {
	typ           schema.Type
	enumName      string
	autoIncrement bool // serial/bigserial
	warning       string
	raw           string
}

// parseType reads a column type and its type parameters. It leaves column
// constraints in place for the caller.
func (s *state) parseType(p *parser) typeInfo {
	start := p.i
	name, ok := p.ident()
	if !ok {
		return typeInfo{typ: schema.Type{Kind: schema.KindText}, warning: "missing column type; imported as text"}
	}
	last := name
	for p.acceptPunct(".") {
		n, ok := p.ident()
		if !ok {
			break
		}
		last = n
	}
	lower := strings.ToLower(last)

	info := typeInfo{}
	set := func(k schema.Kind) { info.typ = schema.Type{Kind: k} }
	body := func() []token {
		if !p.isPunct("(") {
			return nil
		}
		p.next()
		b, _ := p.parenBody()
		return b
	}
	numbers := func(toks []token) []int {
		var out []int
		for _, t := range toks {
			if t.kind != tokNumber {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(t.text))
			if err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	varchar := func(length int, warn bool) {
		if length <= 0 {
			set(schema.KindText)
			if warn {
				info.warning = fmt.Sprintf("VARCHAR without a length; imported as text")
			}
			return
		}
		info.typ = schema.Type{Kind: schema.KindVarChar, Length: length}
	}

	switch lower {
	case "double":
		p.acceptWord("PRECISION")
		set(schema.KindDouble)
	case "real", "float4":
		set(schema.KindReal)
	case "float8":
		set(schema.KindDouble)
	case "float":
		ns := numbers(body())
		if len(ns) > 0 && ns[0] > 0 && ns[0] <= 24 {
			set(schema.KindReal)
		} else {
			set(schema.KindDouble)
		}
	case "decimal", "numeric", "dec", "fixed":
		ns := numbers(body())
		switch {
		case len(ns) >= 2:
			info.typ = schema.Type{Kind: schema.KindDecimal, Precision: ns[0], Scale: ns[1]}
		case len(ns) == 1:
			info.typ = schema.Type{Kind: schema.KindDecimal, Precision: ns[0], Scale: 0}
		default:
			info.typ = schema.Type{Kind: schema.KindDecimal, Precision: 19, Scale: 4}
			info.warning = "unbounded NUMERIC; imported as Decimal(19, 4)"
		}
	case "money", "smallmoney":
		info.typ = schema.Type{Kind: schema.KindDecimal, Precision: 19, Scale: 4}
	case "smallint", "int2", "smallserial", "serial2":
		body()
		set(schema.KindSmallInt)
		info.autoIncrement = lower == "smallserial" || lower == "serial2"
	case "tinyint":
		ns := numbers(body())
		if len(ns) > 0 && ns[0] == 1 {
			set(schema.KindBool)
		} else {
			set(schema.KindSmallInt)
		}
	case "int", "integer", "int4", "mediumint", "serial", "serial4":
		body()
		set(schema.KindInt)
		info.autoIncrement = lower == "serial" || lower == "serial4"
	case "bigint", "int8", "bigserial", "serial8":
		body()
		set(schema.KindBigInt)
		info.autoIncrement = lower == "bigserial" || lower == "serial8"
	case "bool", "boolean":
		set(schema.KindBool)
	case "bit":
		ns := numbers(body())
		if len(ns) == 1 && ns[0] == 1 {
			set(schema.KindBool)
		} else {
			set(schema.KindBytes)
		}
	case "varbit", "bit varying":
		set(schema.KindBytes)
	case "character", "char", "nchar", "bpchar", "national":
		if lower == "national" {
			p.acceptWord("CHARACTER")
			p.acceptWord("CHAR")
		}
		varying := p.acceptWord("VARYING")
		ns := numbers(body())
		if varying {
			if len(ns) > 0 {
				varchar(ns[0], false)
			} else {
				set(schema.KindText)
			}
		} else {
			n := 1
			if len(ns) > 0 {
				n = ns[0]
			}
			varchar(n, false)
		}
	case "varchar", "nvarchar", "varchar2", "nvarchar2":
		ns := numbers(body())
		if len(ns) > 0 {
			varchar(ns[0], false)
		} else {
			varchar(0, true)
		}
	case "text", "tinytext", "mediumtext", "longtext", "citext", "clob", "name":
		set(schema.KindText)
	case "uuid", "uniqueidentifier":
		set(schema.KindUUID)
	case "json", "jsonb":
		set(schema.KindJSON)
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob":
		set(schema.KindBytes)
	case "binary", "varbinary", "raw":
		body()
		set(schema.KindBytes)
	case "date":
		set(schema.KindDate)
	case "datetime", "timestamp", "timestamptz":
		body()
		if lower != "timestamptz" {
			p.acceptWord("WITH")
			p.acceptWord("WITHOUT")
			p.acceptWord("TIME")
			p.acceptWord("ZONE")
		}
		set(schema.KindTimestamp)
	case "time", "timetz":
		body()
		p.acceptWord("WITH")
		p.acceptWord("WITHOUT")
		p.acceptWord("TIME")
		p.acceptWord("ZONE")
		set(schema.KindText)
		info.warning = "time-of-day type; imported as text"
	case "interval":
		set(schema.KindText)
		info.warning = "INTERVAL type; imported as text"
	case "enum":
		b := body()
		var values []string
		for _, seg := range splitTopLevel(b, ",") {
			if len(seg) == 1 && seg[0].kind == tokString {
				values = append(values, seg[0].text)
			}
		}
		info.typ = schema.Type{Kind: schema.KindEnum, Values: values}
	case "set":
		body()
		set(schema.KindText)
		info.warning = "MySQL SET type; imported as text"
	case "year":
		body()
		set(schema.KindSmallInt)
	case "xml", "inet", "cidr", "macaddr", "macaddr8", "tsvector", "tsquery",
		"vector", "geometry", "geography", "point", "line", "lseg", "box",
		"path", "polygon", "circle":
		body()
		set(schema.KindText)
		info.warning = fmt.Sprintf("%s type; imported as text", strings.ToUpper(lower))
	case "oid":
		set(schema.KindBigInt)
	default:
		// A user-defined type: usually a PostgreSQL enum declared by an
		// earlier CREATE TYPE. Resolve it now when possible; finalize turns
		// anything still unresolved into text.
		info.enumName = last
		if e := s.enum(last); e != nil {
			info.typ = schema.Type{Kind: schema.KindEnum, Values: append([]string(nil), e.values...)}
		} else {
			info.typ = schema.Type{Kind: schema.KindEnum}
		}
	}

	// Array suffixes. lathe has no array type; JSON is the closest lossless
	// representation for values that will be migrated later.
	array := false
	for p.acceptPunct("[") {
		for !p.done() && !p.isPunct("]") {
			p.next()
		}
		p.acceptPunct("]")
		array = true
	}
	if array {
		info.typ = schema.Type{Kind: schema.KindJSON}
		info.enumName = ""
		info.warning = "array type; imported as json"
	}
	for p.acceptWord("UNSIGNED") || p.acceptWord("SIGNED") || p.acceptWord("ZEROFILL") {
	}
	info.raw = joinTokens(p.toks[start:p.i])
	return info
}
