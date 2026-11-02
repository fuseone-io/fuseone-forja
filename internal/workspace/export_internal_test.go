package workspace

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	hdr  tar.Header
	body string
}

// archive is a tar stream of entries, as CopyFromContainer answers.
func archive(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func bundleEntry(body string) entry {
	return entry{hdr: tar.Header{Name: "commits.bundle", Typeflag: tar.TypeReg, Mode: 0o644}, body: body}
}

func TestExtractBundle_oneFileUnderTheCap_writtenOwnerOnly(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "commits.bundle")

	if err := extractBundle(archive(t, bundleEntry("# v2 git bundle\n")), dst, 64); err != nil {
		t.Fatalf("extractBundle: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "# v2 git bundle\n" {
		t.Fatalf("dst = %q, %v", got, err)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o600 {
		t.Fatalf("dst mode %v, want 0600", info.Mode().Perm())
	}
}

// What the workspace hands back is checked before a byte lands: one
// regular file, under the cap, at a path that did not exist.
func TestExtractBundle_notOneFileUnderTheCap_refusedNothingLeft(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("x", 65)
	tests := map[string]func(t *testing.T) *bytes.Buffer{
		"over the cap":  func(t *testing.T) *bytes.Buffer { return archive(t, bundleEntry(over)) },
		"a symlink":     func(t *testing.T) *bytes.Buffer { return archive(t, entry{hdr: tar.Header{Name: "commits.bundle", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}) },
		"a directory":   func(t *testing.T) *bytes.Buffer { return archive(t, entry{hdr: tar.Header{Name: "commits.bundle/", Typeflag: tar.TypeDir, Mode: 0o755}}) },
		"two entries":   func(t *testing.T) *bytes.Buffer { return archive(t, bundleEntry("a"), bundleEntry("b")) },
		"another name":  func(t *testing.T) *bytes.Buffer { return archive(t, entry{hdr: tar.Header{Name: "other", Typeflag: tar.TypeReg}, body: "a"}) },
		"empty archive": func(t *testing.T) *bytes.Buffer { return archive(t) },
		"not a tar":     func(*testing.T) *bytes.Buffer { return bytes.NewBufferString("not a tar stream at all, just bytes") },
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dst := filepath.Join(t.TempDir(), "commits.bundle")

			if err := extractBundle(stream(t), dst, 64); err == nil {
				t.Fatal("extractBundle = nil, want a refusal")
			}
			if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dst left behind after a refusal: %v", err)
			}
		})
	}
}

func TestExtractBundle_dstExists_refusedNotOverwritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("control plane file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "commits.bundle")
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}

	if err := extractBundle(archive(t, bundleEntry("bundle")), dst, 64); err == nil {
		t.Fatal("extractBundle over an existing path = nil, want a refusal")
	}
	if got, _ := os.ReadFile(target); string(got) != "control plane file\n" {
		t.Fatalf("the file behind dst was overwritten: %q", got)
	}
}

func TestExportCommand_refusesWhatCouldBeQuoted(t *testing.T) {
	t.Parallel()
	const base = "5ceb0229fc86eec9a6367c9809a1cc3b0c8e6c27"
	for _, tt := range []struct{ base, branch string }{
		{base: "main", branch: "forja/7"},
		{base: base + "; rm -rf /", branch: "forja/7"},
		{base: base, branch: "forja/7'; touch /x; '"},
		{base: base, branch: "-delete"},
		{base: base, branch: "forja/../x"},
		{base: base, branch: ""},
	} {
		if cmd, err := exportCommand(tt.base, tt.branch); err == nil {
			t.Errorf("exportCommand(%q, %q) = %q, want a refusal", tt.base, tt.branch, cmd)
		}
	}
	if _, err := exportCommand(base, "forja/7"); err != nil {
		t.Fatalf("exportCommand of a valid base and branch: %v", err)
	}
}
