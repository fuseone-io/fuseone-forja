package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// diffArgs keep the diff a function of the two commits alone: NUL-separated
// records, no rename or copy detection, and no external diff or text
// conversion the clone's configuration or the head's attributes could name.
var diffArgs = []string{"diff", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color"}

// changeKinds are the status letters a diff without rename detection
// prints between two commits.
var changeKinds = map[string]domain.ChangeKind{
	"A": domain.ChangeAdded,
	"M": domain.ChangeModified,
	"D": domain.ChangeDeleted,
	"T": domain.ChangeTypeChanged,
}

// lineCounts are one path's numstat; binary has none.
type lineCounts struct {
	added, deleted int
	binary         bool
}

// Diff lists every path head changes against the pinned commit, in git's
// path order, each with its kind and line counts. A head that is not a
// full commit name is refused before git starts; a record git prints that
// is not one of the four kinds is an error, never dropped.
func (c *Checkout) Diff(ctx context.Context, head string) ([]domain.FileChange, error) {
	if !commitSHA.MatchString(head) {
		return nil, fmt.Errorf("github: diff against %q refused: not a commit name", head)
	}
	status, err := c.git.run(ctx, c.dir, c.env, append(append([]string{}, diffArgs...), "--name-status", c.sha, head, "--")...)
	if err != nil {
		return nil, fmt.Errorf("github: diff %s..%s: %w", c.sha, head, err)
	}
	numstat, err := c.git.run(ctx, c.dir, c.env, append(append([]string{}, diffArgs...), "--numstat", c.sha, head, "--")...)
	if err != nil {
		return nil, fmt.Errorf("github: diff %s..%s: %w", c.sha, head, err)
	}
	changes, err := joinDiff(status, numstat)
	if err != nil {
		return nil, fmt.Errorf("github: diff %s..%s: %w", c.sha, head, err)
	}
	return changes, nil
}

// joinDiff pairs each "status NUL path NUL" record with the numstat of its
// path.
func joinDiff(status, numstat []byte) ([]domain.FileChange, error) {
	counts, err := parseNumstat(numstat)
	if err != nil {
		return nil, err
	}
	fields := nulFields(status)
	if len(fields)%2 != 0 {
		return nil, errors.New("name-status record without a path")
	}
	changes := make([]domain.FileChange, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		kind, ok := changeKinds[fields[i]]
		if !ok {
			return nil, fmt.Errorf("status %q of %q is not a change Forja knows", fields[i], fields[i+1])
		}
		n, ok := counts[fields[i+1]]
		if !ok {
			return nil, fmt.Errorf("no line counts for %q", fields[i+1])
		}
		changes = append(changes, domain.FileChange{Path: fields[i+1], Kind: kind, Added: n.added, Deleted: n.deleted, Binary: n.binary})
	}
	return changes, nil
}

// parseNumstat reads "added TAB deleted TAB path NUL" records; a binary
// file has "-" for both counts.
func parseNumstat(out []byte) (map[string]lineCounts, error) {
	counts := map[string]lineCounts{}
	for _, rec := range nulFields(out) {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("numstat record %q is not added, deleted and path", rec)
		}
		if parts[0] == "-" && parts[1] == "-" {
			counts[parts[2]] = lineCounts{binary: true}
			continue
		}
		added, errA := strconv.Atoi(parts[0])
		deleted, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil || added < 0 || deleted < 0 {
			return nil, fmt.Errorf("numstat counts %q, %q of %q are not line counts", parts[0], parts[1], parts[2])
		}
		counts[parts[2]] = lineCounts{added: added, deleted: deleted}
	}
	return counts, nil
}

// nulFields splits NUL-terminated output into its fields.
func nulFields(out []byte) []string {
	out = bytes.TrimSuffix(out, []byte{0})
	if len(out) == 0 {
		return nil
	}
	return strings.Split(string(out), "\x00")
}

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
