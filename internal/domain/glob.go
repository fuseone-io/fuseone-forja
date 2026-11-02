package domain

import (
	"path"
	"strings"
)

// MatchPath reports whether the repository path name matches pattern, a
// glob of the contract. Both are anchored at the repository root and split
// on "/". A "**" segment matches zero or more whole segments; every other
// segment is a path.Match pattern, so "*" never crosses a "/". There is no
// basename matching: "Makefile" matches only the root's Makefile. A
// malformed pattern matches nothing.
func MatchPath(pattern, name string) bool {
	pats, segs := strings.Split(pattern, "/"), strings.Split(name, "/")
	// reach[j] reports that the pattern segments seen so far match segs[:j].
	reach := make([]bool, len(segs)+1)
	reach[0] = true
	for _, p := range pats {
		reach = advance(p, segs, reach)
	}
	return reach[len(segs)]
}

// advance is reach after one more pattern segment p.
func advance(p string, segs []string, reach []bool) []bool {
	next := make([]bool, len(reach))
	for j, ok := range reach {
		switch {
		case !ok:
		case p == "**":
			for k := j; k < len(next); k++ {
				next[k] = true
			}
			return next
		case j < len(segs) && segmentMatch(p, segs[j]):
			next[j+1] = true
		}
	}
	return next
}

func segmentMatch(p, seg string) bool {
	ok, err := path.Match(p, seg)
	return err == nil && ok
}
