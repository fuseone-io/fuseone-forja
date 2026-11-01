package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// binaryProbe is how far git itself looks for a NUL before calling a file
// binary.
const binaryProbe = 8000

// agentContext reads the declared entries in order: a file is one item, a
// directory is its regular files in tree order at the entry's position, a
// missing path or a binary file is a warning. Any other read failure is
// an error.
func agentContext(ctx context.Context, co Checkout, entries []string) ([]ContextFile, []string, error) {
	b := &contextBuilder{co: co, files: []ContextFile{}, warnings: []string{}}
	for _, entry := range entries {
		if err := b.entry(ctx, strings.TrimSuffix(entry, "/")); err != nil {
			return nil, nil, err
		}
	}
	return b.files, b.warnings, nil
}

// contextBuilder accumulates the items and warnings of one assembly.
type contextBuilder struct {
	co       Checkout
	files    []ContextFile
	warnings []string
}

func (b *contextBuilder) entry(ctx context.Context, path string) error {
	err := b.file(ctx, path)
	switch {
	case errors.Is(err, github.ErrPathMissing):
		b.warnings = append(b.warnings, fmt.Sprintf("agent_context: %s: not found at %s", path, b.co.SHA()))
		return nil
	case errors.Is(err, github.ErrIsDirectory):
		return b.directory(ctx, path)
	}
	return err
}

func (b *contextBuilder) file(ctx context.Context, path string) error {
	content, err := b.co.ReadFile(ctx, path)
	if err != nil {
		return fmt.Errorf("agent_context: %s: %w", path, err)
	}
	if isBinary(content) {
		b.warnings = append(b.warnings, fmt.Sprintf("agent_context: %s: binary file skipped", path))
		return nil
	}
	b.files = append(b.files, ContextFile{Path: path, Content: string(content)})
	return nil
}

func (b *contextBuilder) directory(ctx context.Context, dir string) error {
	paths, err := b.co.ListFiles(ctx, dir)
	if err != nil {
		return fmt.Errorf("agent_context: %s: %w", dir, err)
	}
	for _, path := range paths {
		if err := b.file(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func isBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), binaryProbe)], 0) >= 0
}
