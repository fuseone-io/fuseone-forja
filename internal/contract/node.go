package contract

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// The generic node readers: each reports a defect at the node's own line and
// returns a zero value, so the walk goes on and every defect is collected.

func (p *parser) fail(path string, n *yaml.Node, message string) {
	p.errs = append(p.errs, FieldError{Path: path, Line: n.Line, Message: message})
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func indexed(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}

// object walks a mapping, handing each known key's value to its field
// function and reporting unknown and duplicate keys at the key's line. It
// returns the keys it saw so the caller can check the required ones.
func (p *parser) object(n *yaml.Node, path string, fields map[string]func(*yaml.Node)) map[string]bool {
	seen := map[string]bool{}
	if n.Kind != yaml.MappingNode {
		p.fail(path, n, "must be a mapping")
		return seen
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		keyPath := join(path, key.Value)
		field, known := fields[key.Value]
		switch {
		case !known:
			p.fail(keyPath, key, "unknown key")
		case seen[key.Value]:
			p.fail(keyPath, key, "duplicate key")
		default:
			seen[key.Value] = true
			field(value)
		}
	}
	return seen
}

// require reports each key absent from a mapping, at the mapping's line.
func (p *parser) require(seen map[string]bool, path string, n *yaml.Node, keys ...string) {
	for _, key := range keys {
		if !seen[key] {
			p.fail(join(path, key), n, "is required")
		}
	}
}

// scalar reads a scalar as text. A null scalar reads as the empty string;
// any other kind is a defect.
func (p *parser) scalar(n *yaml.Node, path string) (string, bool) {
	if n.Kind != yaml.ScalarNode {
		p.fail(path, n, "must be a string")
		return "", false
	}
	if n.Tag == "!!null" {
		return "", true
	}
	return n.Value, true
}

func (p *parser) nonEmptyString(n *yaml.Node, path string) string {
	s, ok := p.scalar(n, path)
	if ok && s == "" {
		p.fail(path, n, "is empty")
	}
	return s
}

func (p *parser) integer(n *yaml.Node, path string) (int, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		p.fail(path, n, "must be an integer")
		return 0, false
	}
	var v int
	if err := n.Decode(&v); err != nil {
		p.fail(path, n, "must be an integer")
		return 0, false
	}
	return v, true
}

// stringList reads a sequence of non-empty strings; the result is never nil.
func (p *parser) stringList(n *yaml.Node, path string) []string {
	out := []string{}
	if n.Kind != yaml.SequenceNode {
		p.fail(path, n, "must be a list")
		return out
	}
	for i, item := range n.Content {
		out = append(out, p.nonEmptyString(item, indexed(path, i)))
	}
	return out
}

// stringMap reads a mapping of scalar values; the result is never nil.
func (p *parser) stringMap(n *yaml.Node, path string) map[string]string {
	out := map[string]string{}
	if n.Kind != yaml.MappingNode {
		p.fail(path, n, "must be a mapping")
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if s, ok := p.scalar(value, join(path, key.Value)); ok {
			out[key.Value] = s
		}
	}
	return out
}
