package redact

import (
	"bytes"
	"encoding/json"
	"io"
)

// node is a parsed JSON value that keeps object member order and number text,
// so a redacted document differs from its input only in redacted values.
type node struct {
	kind    nodeKind
	members []member // object
	elems   []*node  // array
	str     string   // string
	lit     []byte   // number, true, false, null
}

type nodeKind int

const (
	kindLiteral nodeKind = iota
	kindString
	kindObject
	kindArray
)

type member struct {
	key string
	val *node
}

// get returns the string value of key in an object node.
func (n *node) get(key string) (string, bool) {
	if n.kind != kindObject {
		return "", false
	}
	for _, m := range n.members {
		if m.key == key && m.val.kind == kindString {
			return m.val.str, true
		}
	}
	return "", false
}

// parseTree parses one JSON value; trailing data is an error.
func parseTree(b []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := parseValue(dec)
	if err != nil {
		return nil, ErrInvalidJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrInvalidJSON
	}
	return n, nil
}

func parseValue(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			n := &node{kind: kindObject}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, member{key: kt.(string), val: v})
			}
			_, err := dec.Token()
			return n, err
		}
		n := &node{kind: kindArray, elems: []*node{}}
		for dec.More() {
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			n.elems = append(n.elems, v)
		}
		_, err := dec.Token()
		return n, err
	case string:
		return &node{kind: kindString, str: t}, nil
	case json.Number:
		return &node{lit: []byte(t)}, nil
	case bool:
		if t {
			return &node{lit: []byte("true")}, nil
		}
		return &node{lit: []byte("false")}, nil
	}
	return &node{lit: []byte("null")}, nil
}

// encode writes n as compact JSON without HTML escaping.
func (n *node) encode(buf *bytes.Buffer) {
	switch n.kind {
	case kindString:
		writeString(buf, n.str)
	case kindObject:
		buf.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, m.key)
			buf.WriteByte(':')
			m.val.encode(buf)
		}
		buf.WriteByte('}')
	case kindArray:
		buf.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				buf.WriteByte(',')
			}
			e.encode(buf)
		}
		buf.WriteByte(']')
	default:
		buf.Write(n.lit)
	}
}

func writeString(buf *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// indented renders n with two-space indentation, like Envoy's admin output.
func (n *node) indented() []byte {
	var c, out bytes.Buffer
	n.encode(&c)
	_ = json.Indent(&out, c.Bytes(), "", "  ")
	out.WriteByte('\n')
	return out.Bytes()
}
