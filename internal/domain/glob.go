package domain

import (
	"path"
	"strings"
)

// doubleStar is the segment that matches zero or more segments.
const doubleStar = "**"

// MatchPath returns the first of patterns that p matches, and whether one
// did. Both are split on "/" and matched segment by segment: a "**"
// segment matches zero or more segments, any other segment is matched by
// path.Match against one segment, so "*" never crosses a slash. p carries
// no leading slash. Patterns are expected valid; a malformed segment
// matches nothing.
func MatchPath(patterns []string, p string) (string, bool) {
	segs := strings.Split(p, "/")
	for _, pattern := range patterns {
		if matchSegments(strings.Split(pattern, "/"), segs) {
			return pattern, true
		}
	}
	return "", false
}

// matchSegments walks the pattern once, keeping for each prefix of segs
// whether the pattern so far can end there, so a pattern with many "**"
// segments costs no more than its length times the path's.
func matchSegments(pattern, segs []string) bool {
	reach := make([]bool, len(segs)+1)
	reach[0] = true
	for _, ps := range pattern {
		next := make([]bool, len(segs)+1)
		for j, ok := range reach {
			switch {
			case !ok:
			case ps == doubleStar:
				fillFrom(next, j)
			case j < len(segs) && segmentMatches(ps, segs[j]):
				next[j+1] = true
			}
		}
		reach = next
	}
	return reach[len(segs)]
}

func fillFrom(reach []bool, from int) {
	for k := from; k < len(reach); k++ {
		reach[k] = true
	}
}

func segmentMatches(pattern, seg string) bool {
	ok, err := path.Match(pattern, seg)
	return err == nil && ok
}
