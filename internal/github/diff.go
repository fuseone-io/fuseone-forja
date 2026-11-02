package github

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
)

// DiffLines counts the lines head adds and deletes against the pinned
// commit. A rename is a delete and an add, and a binary file counts no
// line. Whether a file is text comes from the pinned commit's attributes,
// never from what head committed, and no diff driver or text conversion
// runs. The attribute source is a command-line option: a git that does
// not know it refuses the count rather than ignore it.
func (c *Checkout) DiffLines(ctx context.Context, head string) (int, error) {
	if !commitSHA.MatchString(head) {
		return 0, fmt.Errorf("github: diff of %q refused: not a commit name", head)
	}
	out, err := c.git.run(ctx, c.dir, c.env,
		"--attr-source="+c.sha, "diff", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", c.sha, head)
	if err != nil {
		return 0, fmt.Errorf("github: diff %s..%s: %w", c.sha, head, err)
	}
	n, err := sumNumstat(out)
	if err != nil {
		return 0, fmt.Errorf("github: diff %s..%s: %w", c.sha, head, err)
	}
	return n, nil
}

// sumNumstat adds up the "added TAB deleted TAB path NUL" records of git
// diff --numstat -z without renames; a binary file's "-" counts zero.
func sumNumstat(out []byte) (int, error) {
	total := 0
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		fields := bytes.SplitN(rec, []byte{'\t'}, 3)
		if len(fields) != 3 {
			return 0, fmt.Errorf("numstat record %q has no path", rec)
		}
		for _, f := range fields[:2] {
			n, err := lineCount(f)
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	return total, nil
}

func lineCount(field []byte) (int, error) {
	if string(field) == "-" {
		return 0, nil
	}
	n, err := strconv.Atoi(string(field))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("numstat count %q is not a line count", field)
	}
	return n, nil
}
