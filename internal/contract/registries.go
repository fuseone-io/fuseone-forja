package contract

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// registries reads the private registries setup fetches from, each
// lowercased and checked against the entry grammar the registry proxy
// matches requests with.
func (p *parser) registries(n *yaml.Node) {
	entries := p.stringList(n, "registries")
	p.c.Registries = entries
	if n.Kind != yaml.SequenceNode {
		return
	}
	for i, item := range n.Content {
		if item.Kind != yaml.ScalarNode || entries[i] == "" {
			continue
		}
		entries[i] = strings.ToLower(entries[i])
		if err := domain.ValidRegistry(entries[i]); err != nil {
			p.fail(indexed("registries", i), item, err.Error())
		}
	}
}
