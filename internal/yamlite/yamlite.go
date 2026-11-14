// Package yamlite parses the small YAML subset used by platform/inventory.yaml:
// block mappings and sequences, single-line flow collections, plain and quoted
// scalars, and comments. All scalars are returned as strings.
package yamlite

import (
	"fmt"
	"strconv"
	"strings"
)

type line struct {
	indent, n int
	text      string
}

// Parse returns map[string]any, []any, string or nil.
func Parse(src []byte) (any, error) {
	var lines []line
	for n, raw := range strings.Split(string(src), "\n") {
		raw = strings.TrimRight(stripComment(raw), " \t\r")
		t := strings.TrimLeft(raw, " ")
		if t == "" || t == "---" || t == "..." {
			continue
		}
		if strings.HasPrefix(t, "\t") {
			return nil, fmt.Errorf("yaml line %d: tab indentation", n+1)
		}
		lines = append(lines, line{indent: len(raw) - len(t), n: n + 1, text: t})
	}
	if len(lines) == 0 {
		return nil, nil
	}
	p := &parser{lines: lines}
	v, err := p.block(lines[0].indent)
	if err == nil && p.i < len(lines) {
		err = fmt.Errorf("yaml line %d: unexpected content", lines[p.i].n)
	}
	return v, err
}

type parser struct {
	lines []line
	i     int
}

func isItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

func (p *parser) block(indent int) (any, error) {
	if isItem(p.lines[p.i].text) {
		return p.seq(indent)
	}
	return p.mapping(indent)
}

// nested parses the value of a "key:" or "-" with nothing after it.
func (p *parser) nested(parent int, seqOK bool) (any, error) {
	if p.i < len(p.lines) {
		next := p.lines[p.i]
		if next.indent > parent || (seqOK && next.indent == parent && isItem(next.text)) {
			return p.block(next.indent)
		}
	}
	return nil, nil
}

func (p *parser) seq(indent int) (any, error) {
	out := []any{}
	for p.i < len(p.lines) && p.lines[p.i].indent == indent && isItem(p.lines[p.i].text) {
		l := p.lines[p.i]
		rest := strings.TrimLeft(l.text[1:], " ")
		var v any
		var err error
		switch {
		case rest == "":
			p.i++
			v, err = p.nested(indent, false)
		case !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "{") && hasKey(rest):
			// "- key: value" starts a mapping indented at the key's column.
			col := indent + len(l.text) - len(rest)
			p.lines[p.i] = line{indent: col, n: l.n, text: rest}
			v, err = p.mapping(col)
		default:
			p.i++
			v, err = scalar(rest, l.n)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *parser) mapping(indent int) (any, error) {
	m := map[string]any{}
	for p.i < len(p.lines) && p.lines[p.i].indent == indent && !isItem(p.lines[p.i].text) {
		l := p.lines[p.i]
		k, rest, ok := splitKey(l.text)
		if !ok {
			return nil, fmt.Errorf("yaml line %d: expected \"key: value\"", l.n)
		}
		key, err := unquote(k)
		if err != nil {
			return nil, fmt.Errorf("yaml line %d: %w", l.n, err)
		}
		p.i++
		var v any
		if rest == "" {
			v, err = p.nested(indent, true)
		} else {
			v, err = scalar(rest, l.n)
		}
		if err != nil {
			return nil, err
		}
		m[key] = v
	}
	if p.i < len(p.lines) && p.lines[p.i].indent > indent {
		return nil, fmt.Errorf("yaml line %d: unexpected indentation", p.lines[p.i].n)
	}
	return m, nil
}

func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

// splitKey splits "key: rest" at the first ": " (or trailing ":") outside quotes.
func splitKey(s string) (key, rest string, ok bool) {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == ':' && (i+1 == len(s) || s[i+1] == ' '):
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), i > 0
		}
	}
	return "", "", false
}

func hasKey(s string) bool { _, _, ok := splitKey(s); return ok }

func unquote(s string) (string, error) {
	switch {
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		return strconv.Unquote(s)
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	}
	return s, nil
}

func scalar(s string, n int) (any, error) {
	if s[0] == '[' || s[0] == '{' {
		f := &flow{s: s}
		v, err := f.value()
		if err == nil {
			f.ws()
			if f.i != len(f.s) {
				err = fmt.Errorf("trailing characters")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("yaml line %d: flow collection: %w", n, err)
		}
		return v, nil
	}
	if s == "~" || s == "null" {
		return nil, nil
	}
	v, err := unquote(s)
	if err != nil {
		return nil, fmt.Errorf("yaml line %d: %w", n, err)
	}
	return v, nil
}

type flow struct {
	s string
	i int
}

func (f *flow) ws() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flow) value() (any, error) {
	f.ws()
	if f.i >= len(f.s) {
		return nil, fmt.Errorf("unexpected end")
	}
	switch f.s[f.i] {
	case '[':
		f.i++
		out := []any{}
		for {
			f.ws()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return out, nil
			}
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			if err := f.sep(']'); err != nil {
				return nil, err
			}
		}
	case '{':
		f.i++
		m := map[string]any{}
		for {
			f.ws()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return m, nil
			}
			k, err := f.atom(":")
			if err != nil {
				return nil, err
			}
			if f.i >= len(f.s) || f.s[f.i] != ':' {
				return nil, fmt.Errorf("expected ':'")
			}
			f.i++
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			m[k] = v
			if err := f.sep('}'); err != nil {
				return nil, err
			}
		}
	}
	return f.atom(",]}")
}

// sep consumes "," or peeks the closing bracket.
func (f *flow) sep(closer byte) error {
	f.ws()
	switch {
	case f.i < len(f.s) && f.s[f.i] == ',':
		f.i++
		return nil
	case f.i < len(f.s) && f.s[f.i] == closer:
		return nil
	}
	return fmt.Errorf("expected ',' or '%c'", closer)
}

func (f *flow) atom(stops string) (string, error) {
	f.ws()
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		q := f.s[f.i]
		for j := f.i + 1; j < len(f.s); j++ {
			if f.s[j] == '\\' && q == '"' {
				j++
				continue
			}
			if f.s[j] == q {
				if q == '\'' && j+1 < len(f.s) && f.s[j+1] == '\'' {
					j++
					continue
				}
				v, err := unquote(f.s[f.i : j+1])
				f.i = j + 1
				f.ws()
				return v, err
			}
		}
		return "", fmt.Errorf("unterminated quote")
	}
	start := f.i
	for f.i < len(f.s) && !strings.ContainsRune(stops, rune(f.s[f.i])) {
		f.i++
	}
	return strings.TrimSpace(f.s[start:f.i]), nil
}
