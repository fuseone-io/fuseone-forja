package domain

import "strings"

// StdioClientName is the client of a server over stdio: the process that
// started it. No client of a clients file may take it.
const StdioClientName = "stdio"

// Client is who calls Forja's tools: the name the runs it starts are
// recorded under, and the repositories it may name. The zero Client owns
// no run and reaches no repository.
type Client struct {
	Name string
	// Repositories are owner/name or owner/* patterns.
	Repositories []string
	// Unrestricted reaches every repository, leaving the GitHub App's
	// installations as the only bound.
	Unrestricted bool
}

// Reaches reports whether the client may name repository. Owners and names
// compare without regard to ASCII case, as GitHub's do. A repository that
// is not one owner and one name, each of ASCII letters, digits, '.', '_'
// or '-' and neither "." nor "..", is reached by no pattern; a pattern
// that is not owner/name or owner/* reaches nothing.
func (c Client) Reaches(repository string) bool {
	if c.Unrestricted {
		return true
	}
	owner, name, ok := splitRepository(repository)
	if !ok {
		return false
	}
	for _, p := range c.Repositories {
		if !ValidRepositoryPattern(p) {
			continue
		}
		po, pn, _ := strings.Cut(p, "/")
		if strings.EqualFold(po, owner) && (pn == "*" || strings.EqualFold(pn, name)) {
			return true
		}
	}
	return false
}

// ValidRepositoryPattern reports whether p is a pattern Reaches can match:
// owner/name or owner/*, each segment of the shape a repository must have.
func ValidRepositoryPattern(p string) bool {
	owner, name, found := strings.Cut(p, "/")
	return found && validSegment(owner) && (name == "*" || validSegment(name))
}

func splitRepository(repository string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(repository, "/")
	if !found || !validSegment(owner) || !validSegment(name) {
		return "", "", false
	}
	return owner, name, true
}

// validSegment is an owner or a name: ASCII only, so that a case-folded
// comparison cannot match a different string through Unicode folding.
func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := range len(s) {
		if !segmentByte(s[i]) {
			return false
		}
	}
	return true
}

func segmentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return b == '.' || b == '_' || b == '-'
}
