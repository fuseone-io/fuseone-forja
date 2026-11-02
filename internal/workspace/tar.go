package workspace

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"
)

// copyTree streams tree as a tar archive into the container's workDir.
// The archive is produced while it is sent, so the tree never sits in
// memory twice; a failure on either side ends the other.
func copyTree(ctx context.Context, cli *client.Client, id, tree string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, tree)) }()
	_, err := cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: workDir,
		Content:         pr,
	})
	// Ends the writer if the copy stopped before the archive did.
	_ = pr.CloseWithError(err)
	return err
}

// writeTar archives every directory, regular file and symbolic link under
// root with paths relative to it. Anything else (a socket, a device) has
// no place in a source tree and is skipped.
func writeTar(w io.Writer, root string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		return writeEntry(tw, path, filepath.ToSlash(rel), d)
	})
	if err != nil {
		return fmt.Errorf("archive %s: %w", root, err)
	}
	return tw.Close()
}

func writeEntry(tw *tar.Writer, path, name string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	mode := info.Mode()
	if !mode.IsDir() && !mode.IsRegular() && mode&fs.ModeSymlink == 0 {
		return nil
	}
	hdr, err := header(path, name, info)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !mode.IsRegular() {
		return nil
	}
	return writeFile(tw, path)
}

// header names the entry by its path under the root; a directory carries
// the trailing slash tar readers expect.
func header(path, name string, info fs.FileInfo) (*tar.Header, error) {
	link := ""
	if info.Mode()&fs.ModeSymlink != 0 {
		var err error
		if link, err = os.Readlink(path); err != nil {
			return nil, err
		}
	}
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return nil, err
	}
	hdr.Name = name
	if info.IsDir() {
		hdr.Name += "/"
	}
	return hdr, nil
}

// tarFile is one entry the control plane writes into a container: a
// directory when body is nil, otherwise a regular file of size bytes.
type tarFile struct {
	name  string
	mode  int64
	owner int
	size  int64
	body  io.Reader
}

// copyFiles streams files, in order, as one archive into dir of the
// container, each with its own mode and owner.
func copyFiles(ctx context.Context, cli *client.Client, id, dir string, files ...tarFile) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeFiles(pw, files)) }()
	_, err := cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: dir, Content: pr})
	// Ends the writer if the copy stopped before the archive did.
	_ = pr.CloseWithError(err)
	return err
}

func writeFiles(w io.Writer, files []tarFile) error {
	tw := tar.NewWriter(w)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: f.mode, Uid: f.owner, Gid: f.owner, Size: f.size, Typeflag: tar.TypeReg}
		if f.body == nil {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if f.body == nil {
			continue
		}
		if _, err := io.Copy(tw, f.body); err != nil {
			return err
		}
	}
	return tw.Close()
}

// copyBinary streams the file at path into dir of the container as name,
// root-owned 0755: the container's user runs it and cannot change it.
func copyBinary(ctx context.Context, cli *client.Client, id, path, dir, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	// The file was only read; a failure to close it changes no byte sent.
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return copyFiles(ctx, cli, id, dir, tarFile{name: name, mode: 0o755, size: info.Size(), body: f})
}

func writeFile(tw *tar.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	// The file was only read; a failure to close it changes no byte sent.
	defer func() { _ = f.Close() }()
	_, err = io.Copy(tw, f)
	return err
}
