package workspace

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// MaxBundleBytes bounds the bundle of an agent's commits the control plane
// copies out of a workspace.
const MaxBundleBytes = 32 << 20

// bundleFile is where the workspace writes the bundle, outside the tree.
const bundleFile = "/" + handoff + "/commits.bundle"

// The export command's own exit codes, apart from git's.
const (
	exitNoCommits = 3
	exitNoBranch  = 4
)

var (
	exportBase   = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	exportBranch = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
)

// ExportCommits writes to dst, a path on the control plane that must not
// exist, a git bundle of branch beyond the commit base as the workspace's
// tree holds it. A branch still at base wraps domain.ErrNoCommits. The
// bundle is copied out only as one regular file of at most MaxBundleBytes;
// anything else is refused and leaves nothing at dst. The workspace must
// still be running.
func (c *Container) ExportCommits(ctx context.Context, base, branch, dst string) error {
	cmd, err := exportCommand(base, branch)
	if err != nil {
		return err
	}
	res, err := c.Exec(ctx, cmd)
	if err != nil {
		return fmt.Errorf("workspace: export commits: %w", err)
	}
	switch res.ExitCode {
	case 0:
	case exitNoCommits:
		return fmt.Errorf("workspace: export %s: %w", branch, domain.ErrNoCommits)
	case exitNoBranch:
		return fmt.Errorf("workspace: export %s: the branch is not in the tree", branch)
	default:
		return fmt.Errorf("workspace: export %s: git exited %d", branch, res.ExitCode)
	}
	copied, err := c.cli.CopyFromContainer(ctx, c.id, client.CopyFromContainerOptions{SourcePath: bundleFile})
	if err != nil {
		return fmt.Errorf("workspace: copy %s out: %w", bundleFile, err)
	}
	// The bundle was read whole or refused; closing the stream changes
	// neither.
	defer func() { _ = copied.Content.Close() }()
	if err := extractBundle(copied.Content, dst, MaxBundleBytes); err != nil {
		return fmt.Errorf("workspace: export %s: %w", branch, err)
	}
	return nil
}

// exportCommand bundles base..branch into bundleFile, refusing a base or a
// branch that would need quoting. A branch at base exits exitNoCommits,
// since git refuses an empty bundle; a branch the tree lacks exits
// exitNoBranch.
func exportCommand(base, branch string) (string, error) {
	if !exportBase.MatchString(base) {
		return "", fmt.Errorf("workspace: export: base %q is not a commit name", base)
	}
	if !exportBranch.MatchString(branch) || strings.Contains(branch, "..") {
		return "", fmt.Errorf("workspace: export: branch %q is not a plain branch name", branch)
	}
	ref := "refs/heads/" + branch
	git := "git -c safe.directory=" + workDir
	return strings.Join([]string{
		fmt.Sprintf("head=$(%s rev-parse --verify --quiet %s^{commit}) || exit %d", git, ref, exitNoBranch),
		fmt.Sprintf(`test "$head" != %s || exit %d`, base, exitNoCommits),
		"rm -rf " + bundleFile,
		fmt.Sprintf("%s bundle create -q %s %s..%s", git, bundleFile, base, ref),
	}, " && "), nil
}

// extractBundle writes the single regular file of the tar stream r to dst,
// created for the owner alone, refusing it over limit bytes. Any refusal
// removes what was written.
func extractBundle(r io.Reader, dst string, limit int64) (err error) {
	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	if err != nil {
		return fmt.Errorf("read bundle archive: %w", err)
	}
	if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != path.Base(bundleFile) {
		return fmt.Errorf("bundle archive holds %q of type %c, want the one regular file", hdr.Name, hdr.Typeflag)
	}
	if hdr.Size > limit {
		return fmt.Errorf("bundle of %d bytes is over the cap of %d", hdr.Size, limit)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			// The refusal is the error; a file that could not be removed on
			// top of it adds nothing the caller can act on.
			_ = os.Remove(dst)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(tr, limit+1))
	switch {
	case err != nil:
		return fmt.Errorf("copy bundle: %w", err)
	case n > limit:
		return fmt.Errorf("bundle is over the cap of %d bytes", limit)
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return errors.New("bundle archive holds more than the bundle")
	}
	return nil
}
