package contract

import (
	"gopkg.in/yaml.v3"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func (p *parser) gates(n *yaml.Node) {
	if n.Kind != yaml.SequenceNode {
		p.fail("gates", n, "must be a list")
		return
	}
	names := map[string]bool{}
	for i, item := range n.Content {
		path := indexed("gates", i)
		gate := p.gate(item, path)
		if gate.Name != "" && names[gate.Name] {
			p.fail(join(path, "name"), item, "duplicate gate name")
		}
		names[gate.Name] = true
		p.c.Gates = append(p.c.Gates, gate)
	}
}

func (p *parser) gate(n *yaml.Node, path string) domain.Gate {
	gate := domain.Gate{Needs: []string{}}
	seen := p.object(n, path, map[string]func(*yaml.Node){
		"name":  func(v *yaml.Node) { gate.Name = p.nonEmptyString(v, join(path, "name")) },
		"run":   func(v *yaml.Node) { gate.Run = p.nonEmptyString(v, join(path, "run")) },
		"needs": func(v *yaml.Node) { gate.Needs = p.gateNeeds(v, join(path, "needs")) },
	})
	if n.Kind == yaml.MappingNode {
		p.require(seen, path, n, "name", "run")
	}
	return gate
}

// gateNeeds reads the service names and defers their resolution to the end
// of the document.
func (p *parser) gateNeeds(n *yaml.Node, path string) []string {
	needs := p.stringList(n, path)
	if n.Kind != yaml.SequenceNode {
		return needs
	}
	for i, item := range n.Content {
		p.needs = append(p.needs, needRef{path: indexed(path, i), node: item})
	}
	return needs
}

func (p *parser) resolveNeeds() {
	for _, ref := range p.needs {
		if _, ok := p.c.Services[ref.node.Value]; !ok {
			p.fail(ref.path, ref.node, "names a service not declared under services")
		}
	}
}

func (p *parser) services(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.fail("services", n, "must be a mapping")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		path := join("services", key.Value)
		if _, dup := p.c.Services[key.Value]; dup {
			p.fail(path, key, "duplicate key")
			continue
		}
		p.c.Services[key.Value] = p.service(key, value, path)
	}
}

// service reads one sidecar; a missing field is reported at the line that
// names the service, since a block mapping starts on the line below it.
func (p *parser) service(key, n *yaml.Node, path string) domain.Service {
	service := domain.Service{Env: map[string]string{}}
	seen := p.object(n, path, map[string]func(*yaml.Node){
		"image": func(v *yaml.Node) { service.Image = p.nonEmptyString(v, join(path, "image")) },
		"env":   func(v *yaml.Node) { service.Env = p.stringMap(v, join(path, "env")) },
	})
	if n.Kind == yaml.MappingNode {
		p.require(seen, path, key, "image")
	}
	return service
}
