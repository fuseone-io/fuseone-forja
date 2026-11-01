package contract

import (
	"time"

	"gopkg.in/yaml.v3"
)

func (p *parser) limits(n *yaml.Node) {
	p.object(n, "limits", map[string]func(*yaml.Node){
		"wall_clock":     func(v *yaml.Node) { p.c.Limits.WallClock = p.duration(v, "limits.wall_clock") },
		"max_diff_lines": func(v *yaml.Node) { p.c.Limits.MaxDiffLines = p.positive(v, "limits.max_diff_lines") },
	})
}

func (p *parser) duration(n *yaml.Node, path string) time.Duration {
	s, ok := p.scalar(n, path)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		p.fail(path, n, "must be a duration such as 30m")
		return 0
	}
	if d <= 0 {
		p.fail(path, n, "must be positive")
		return 0
	}
	return d
}

func (p *parser) positive(n *yaml.Node, path string) int {
	v, ok := p.integer(n, path)
	if !ok {
		return 0
	}
	if v <= 0 {
		p.fail(path, n, "must be a positive integer")
		return 0
	}
	return v
}
