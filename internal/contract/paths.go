package contract

import (
	"path"

	"gopkg.in/yaml.v3"
)

// globs reads a list of patterns and checks each is syntactically valid.
// Only the syntax is checked here: "**" is a valid pattern to path.Match,
// and what it matches is decided where the patterns are applied.
func (p *parser) globs(n *yaml.Node, at string) []string {
	patterns := p.stringList(n, at)
	if n.Kind != yaml.SequenceNode {
		return patterns
	}
	for i, item := range n.Content {
		if _, err := path.Match(item.Value, ""); err != nil {
			p.fail(indexed(at, i), item, "invalid glob")
		}
	}
	return patterns
}

func (p *parser) verify(n *yaml.Node) {
	p.object(n, "verify", map[string]func(*yaml.Node){
		"exclude": func(v *yaml.Node) { p.c.Verify.Exclude = p.globs(v, "verify.exclude") },
	})
}
