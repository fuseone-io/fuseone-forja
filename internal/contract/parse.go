// Package contract parses and validates the .forja.yaml v1 contract. The
// standard library has no YAML parser, so gopkg.in/yaml.v3 decodes the
// document; the package walks its node tree instead of decoding into a
// struct because the nodes are what give every error a field path and a
// line. Nothing here reads a file: Parse takes the bytes the control plane
// fetched from the base commit.
package contract

import (
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Parse decodes and validates a contract. On failure the error is an Errors
// holding every defect found, not only the first.
func Parse(src []byte) (domain.Contract, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return domain.Contract{}, Errors{{Message: err.Error()}}
	}
	root := documentRoot(&doc)
	if root == nil {
		return domain.Contract{}, Errors{{Line: 1, Message: "contract is empty"}}
	}
	if root.Kind != yaml.MappingNode {
		return domain.Contract{}, Errors{{Line: root.Line, Message: "contract must be a mapping"}}
	}
	p := &parser{c: emptyContract()}
	seen := p.object(root, "", p.topLevel())
	p.require(seen, "", root, "version", "image")
	p.resolveNeeds()
	if len(p.errs) == 0 {
		return p.c, nil
	}
	sort.SliceStable(p.errs, func(i, j int) bool { return p.errs[i].Line < p.errs[j].Line })
	return domain.Contract{}, p.errs
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

// emptyContract is the normalised zero: every slice and map present and empty.
func emptyContract() domain.Contract {
	return domain.Contract{
		Setup:           []string{},
		Services:        map[string]domain.Service{},
		Registries:      []string{},
		Gates:           []domain.Gate{},
		Verify:          domain.Verify{Exclude: []string{}},
		ProofPaths:      []string{},
		HumanMergePaths: []string{},
		AgentContext:    []string{},
	}
}

// parser accumulates the contract and its defects while walking the tree.
type parser struct {
	c    domain.Contract
	errs Errors
	// needs are the gate references to services, resolved once the whole
	// document is read because services may be declared after gates.
	needs []needRef
}

type needRef struct {
	path string
	node *yaml.Node
}

// topLevel is the decision table of the root mapping: each key to the
// function that decodes its value.
func (p *parser) topLevel() map[string]func(*yaml.Node) {
	return map[string]func(*yaml.Node){
		"version":           p.version,
		"image":             func(n *yaml.Node) { p.c.Image = p.nonEmptyString(n, "image") },
		"setup":             func(n *yaml.Node) { p.c.Setup = p.stringList(n, "setup") },
		"services":          p.services,
		"registries":        func(n *yaml.Node) { p.c.Registries = p.stringList(n, "registries") },
		"gates":             p.gates,
		"verify":            p.verify,
		"proof_paths":       func(n *yaml.Node) { p.c.ProofPaths = p.globs(n, "proof_paths") },
		"human_merge_paths": func(n *yaml.Node) { p.c.HumanMergePaths = p.globs(n, "human_merge_paths") },
		"limits":            p.limits,
		"agent_context":     func(n *yaml.Node) { p.c.AgentContext = p.stringList(n, "agent_context") },
	}
}

func (p *parser) version(n *yaml.Node) {
	v, ok := p.integer(n, "version")
	if !ok {
		return
	}
	if v != 1 {
		p.fail("version", n, "unsupported version, want 1")
		return
	}
	p.c.Version = v
}
