package github

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// MaxPatchBytes bounds the patch Changes returns.
const MaxPatchBytes = 64 << 10

// changeArgs keep a change a function of the two commits and the pinned
// commit's attributes: no rename detection, no external diff, no text
// conversion and no color, whatever the clone's configuration says.
var changeArgs = []string{"diff", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color"}

// Changes is what to changes against from: every path in git's order, a
// rename as both of its paths, and the patch, cut at MaxPatchBytes on a
// character with Truncated set. Whether a file is text comes from the
// pinned commit's attributes, never from either end's. An end that is not
// a full commit name is refused before git starts.
func (c *Checkout) Changes(ctx context.Context, from, to string) (domain.Change, error) {
	for _, end := range []string{from, to} {
		if !commitSHA.MatchString(end) {
			return domain.Change{}, fmt.Errorf("github: changes between %q and %q refused: not commit names", from, to)
		}
	}
	names, err := c.diff(ctx, "--name-only", "-z", from, to)
	if err != nil {
		return domain.Change{}, fmt.Errorf("github: changes %s..%s: %w", from, to, err)
	}
	patch, err := c.diff(ctx, from, to)
	if err != nil {
		return domain.Change{}, fmt.Errorf("github: changes %s..%s: %w", from, to, err)
	}
	text, truncated := bound(patch, MaxPatchBytes)
	return domain.Change{Paths: nulFields(names), Patch: text, Truncated: truncated}, nil
}

// diff runs git diff with changeArgs, the pinned commit as the attribute
// source, and args, ending the paths with "--".
func (c *Checkout) diff(ctx context.Context, args ...string) ([]byte, error) {
	full := append(append([]string{"--attr-source=" + c.sha}, changeArgs...), args...)
	return c.git.run(ctx, c.dir, c.env, append(full, "--")...)
}

// bound cuts data to at most limit bytes, back to the start of a
// character, and says whether it cut.
func bound(data []byte, limit int) (string, bool) {
	if len(data) <= limit {
		return string(data), false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(data[end]) {
		end--
	}
	return string(data[:end]), true
}
