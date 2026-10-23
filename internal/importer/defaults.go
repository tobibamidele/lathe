package importer

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

type defaultKind int

const (
	defaultNull defaultKind = iota
	defaultString
	defaultNumber
	defaultBool
	defaultNow
	defaultUUID
	defaultNextval
	defaultExpr
)

type defaultValue struct {
	kind defaultKind
	text string // string value, number literal or SQL expression
	raw  string
}

var defaultStopWords = map[string]bool{
	"NOT": true, "PRIMARY": true, "UNIQUE": true, "REFERENCES": true,
	"CHECK": true, "CONSTRAINT": true, "COLLATE": true, "GENERATED": true,
	"AUTO_INCREMENT": true, "AUTOINCREMENT": true, "COMMENT": true,
	"STORAGE": true, "ENCRYPTED": true,
	// MySQL appends ON UPDATE CURRENT_TIMESTAMP to a DEFAULT clause.
	"ON": true,
}

// parseDefaultExpr consumes a DEFAULT expression up to the next column
// modifier.
func (p *parser) parseDefaultExpr() defaultValue {
	if p.acceptWord("NULL") {
		// DEFAULT NULL means "no default".
		return defaultValue{kind: defaultNull, raw: "NULL"}
	}
	var toks []token
	depth := 0
	for !p.done() {
		t := p.peek()
		if depth == 0 && t.kind == tokWord && defaultStopWords[strings.ToUpper(t.text)] {
			break
		}
		p.next()
		switch {
		case t.isPunct("(") || t.isPunct("["):
			depth++
		case t.isPunct(")") || t.isPunct("]"):
			if depth > 0 {
				depth--
			}
		}
		toks = append(toks, t)
	}
	return classifyDefault(toks)
}

func classifyDefault(toks []token) defaultValue {
	toks = stripOuterParens(toks)
	raw := joinTokens(toks)
	if len(toks) == 0 {
		return defaultValue{kind: defaultNull, raw: "NULL"}
	}

	// Drop a trailing PostgreSQL cast: 'x'::text, 0::bigint, ...
	base := toks
	for i, t := range toks {
		if t.isPunct("::") {
			base = toks[:i]
			break
		}
	}
	base = stripOuterParens(base)
	if len(base) == 0 {
		return defaultValue{kind: defaultExpr, text: raw, raw: raw}
	}
	if len(base) == 1 {
		t := base[0]
		switch t.kind {
		case tokString:
			return defaultValue{kind: defaultString, text: t.text, raw: t.raw}
		case tokNumber:
			return defaultValue{kind: defaultNumber, text: t.text, raw: t.raw}
		case tokWord:
			switch strings.ToUpper(t.text) {
			case "TRUE":
				return defaultValue{kind: defaultBool, text: "true", raw: t.raw}
			case "FALSE":
				return defaultValue{kind: defaultBool, text: "false", raw: t.raw}
			case "NULL":
				return defaultValue{kind: defaultNull, raw: t.raw}
			}
		case tokQuoted:
			// A quoted identifier is not a literal; fall through to expr.
		}
	}

	// Unary sign on a numeric literal.
	if len(base) == 2 && (base[0].isPunct("-") || base[0].isPunct("+")) && base[1].kind == tokNumber {
		sign := ""
		if base[0].isPunct("-") {
			sign = "-"
		}
		return defaultValue{kind: defaultNumber, text: sign + base[1].text, raw: sign + base[1].raw}
	}

	if base[0].kind == tokWord || base[0].kind == tokQuoted {
		word := strings.ToUpper(base[0].text)
		hasCall := len(base) > 1 && base[1].isPunct("(")
		switch word {
		case "NOW", "CURRENT_TIMESTAMP", "LOCALTIMESTAMP":
			return defaultValue{kind: defaultNow, raw: raw}
		case "CURRENT_DATE":
			return defaultValue{kind: defaultNow, raw: raw}
		case "CURRENT_TIME", "LOCALTIME":
			return defaultValue{kind: defaultExpr, text: raw, raw: raw}
		case "GEN_RANDOM_UUID", "UUID_GENERATE_V4", "UUID_GENERATE_V1", "NEWID", "UUID":
			if hasCall {
				return defaultValue{kind: defaultUUID, raw: raw}
			}
		case "NEXTVAL":
			return defaultValue{kind: defaultNextval, raw: raw}
		}
	}

	return defaultValue{kind: defaultExpr, text: raw, raw: raw}
}

var (
	integerLiteral = regexp.MustCompile(`^-?[0-9]+$`)
	numberLiteral  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
)

// applyDefault stores a parsed default on col, translating it into the kind
// lathe can express for the column's type.
func (s *state) applyDefault(col *schema.ColumnDef, dv defaultValue) {
	switch dv.kind {
	case defaultNull:
		col.Default = nil
	case defaultNow:
		if col.Type.Kind == schema.KindTimestamp || col.Type.Kind == schema.KindDate {
			col.Default = &schema.Default{Kind: schema.DefaultNow}
		} else {
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
		}
	case defaultUUID:
		if col.Type.Kind == schema.KindUUID {
			col.Default = &schema.Default{Kind: schema.DefaultUUID}
		} else {
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
		}
	case defaultNextval:
		// A sequence-backed integer key is lathe's AutoIncrement.
		col.AutoIncrement = true
		col.Default = nil
		col.Nullable = false
	case defaultString:
		switch col.Type.Kind {
		case schema.KindBool:
			switch strings.ToLower(dv.text) {
			case "1", "true":
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitBool, Value: "true"}
			case "0", "false":
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitBool, Value: "false"}
			default:
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
			}
		case schema.KindSmallInt, schema.KindInt, schema.KindBigInt,
			schema.KindReal, schema.KindDouble, schema.KindDecimal:
			if numberLiteral.MatchString(dv.text) {
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitNumber, Value: dv.text}
			} else {
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
			}
		case schema.KindBytes:
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
		case schema.KindJSON:
			if json.Valid([]byte(dv.text)) {
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitString, Value: dv.text}
			} else {
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
			}
		default:
			col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitString, Value: dv.text}
		}
	case defaultNumber:
		if col.Type.Kind == schema.KindBool {
			switch dv.text {
			case "1":
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitBool, Value: "true"}
			case "0":
				col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitBool, Value: "false"}
			default:
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
			}
		} else if col.Type.Kind.IsNumeric() {
			value := dv.text
			if !numberLiteral.MatchString(value) {
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
				return
			}
			if integerLiteral.MatchString(value) {
				if n, err := strconv.ParseInt(value, 10, 64); err == nil {
					value = strconv.FormatInt(n, 10)
				} else {
					col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
					return
				}
			}
			if col.Type.Kind.IsInteger() && strings.Contains(value, ".") {
				col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
				return
			}
			col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitNumber, Value: value}
		} else {
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
		}
	case defaultBool:
		if col.Type.Kind == schema.KindBool {
			col.Default = &schema.Default{Kind: schema.DefaultLiteral, Lit: schema.LitBool, Value: dv.text}
		} else {
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.raw}
		}
	case defaultExpr:
		if dv.text != "" {
			col.Default = &schema.Default{Kind: schema.DefaultExpr, Value: dv.text}
		}
	}
}
