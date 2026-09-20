package gen

import (
	"strings"
	"unicode"
)

var initialisms = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "api": "API", "uuid": "UUID", "guid": "GUID",
	"http": "HTTP", "https": "HTTPS", "json": "JSON", "sql": "SQL", "html": "HTML", "xml": "XML",
	"ip": "IP", "ui": "UI", "uid": "UID", "tcp": "TCP", "udp": "UDP", "cpu": "CPU", "ttl": "TTL",
	"tls": "TLS", "ssl": "SSL", "ssh": "SSH", "css": "CSS", "dns": "DNS", "os": "OS", "rpc": "RPC",
	"sla": "SLA", "smtp": "SMTP", "vm": "VM", "acl": "ACL", "ascii": "ASCII", "eof": "EOF",
}

// pascal converts snake_case (or any non-alphanumeric separated text) to a Go
// exported identifier, upper-casing common initialisms: user_id -> UserID.
func pascal(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	for _, p := range parts {
		if up, ok := initialisms[strings.ToLower(p)]; ok {
			b.WriteString(up)
			continue
		}
		r := []rune(p)
		b.WriteRune(unicode.ToUpper(r[0]))
		b.WriteString(string(r[1:]))
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		out = "X" + out
	}
	return out
}

var irregular = map[string]string{
	"people": "person", "children": "child", "men": "man", "women": "woman", "mice": "mouse",
	"geese": "goose", "feet": "foot", "teeth": "tooth", "statuses": "status", "aliases": "alias",
	"buses": "bus", "analyses": "analysis", "indices": "index", "vertices": "vertex",
	"matrices": "matrix", "addresses": "address", "processes": "process", "classes": "class",
}

var uncountable = map[string]bool{
	"news": true, "series": true, "species": true, "sheep": true, "fish": true, "media": true,
	"info": true, "equipment": true, "data": true, "metadata": true,
}

// singular singularises the last word of a snake_case table name.
func singular(name string) string {
	i := strings.LastIndex(name, "_")
	head, last := name[:i+1], name[i+1:]
	lower := strings.ToLower(last)
	switch {
	case uncountable[lower]:
		return name
	case irregular[lower] != "":
		return head + irregular[lower]
	case len(lower) > 3 && strings.HasSuffix(lower, "ies"):
		return head + last[:len(last)-3] + "y"
	case strings.HasSuffix(lower, "sses"), strings.HasSuffix(lower, "xes"),
		strings.HasSuffix(lower, "ches"), strings.HasSuffix(lower, "shes"), strings.HasSuffix(lower, "zzes"):
		return head + last[:len(last)-2]
	case strings.HasSuffix(lower, "ss"), strings.HasSuffix(lower, "us"), strings.HasSuffix(lower, "is"):
		return name
	case len(lower) > 1 && strings.HasSuffix(lower, "s"):
		return head + last[:len(last)-1]
	}
	return name
}
