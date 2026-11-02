package workspace

import (
	"bytes"
	"context"
	"fmt"
	"path"
)

// The agent inside the workspace: its binary on PATH, and a directory
// outside the tree for what the control plane hands it.
const (
	agentDir  = "/usr/local/bin"
	agentName = "claude"
	handoff   = "forja"
)

// installAgent copies the agent onto the workspace's PATH, root-owned
// 0755, and creates the directory the control plane hands files in.
func (c *Container) installAgent(ctx context.Context, binary string) error {
	if err := copyBinary(ctx, c.cli, c.id, binary, agentDir, agentName); err != nil {
		return fmt.Errorf("workspace: install agent: %w", err)
	}
	if err := copyFiles(ctx, c.cli, c.id, "/", tarFile{name: handoff + "/", mode: 0o755}); err != nil {
		return fmt.Errorf("workspace: create /%s: %w", handoff, err)
	}
	return nil
}

// WriteFile writes data to the absolute path in the workspace, root-owned
// 0644. The directory must exist.
func (c *Container) WriteFile(ctx context.Context, file string, data []byte) error {
	if !path.IsAbs(file) || path.Clean(file) != file {
		return fmt.Errorf("workspace: write %q: not a clean absolute path", file)
	}
	dir, name := path.Split(file)
	err := copyFiles(ctx, c.cli, c.id, dir, tarFile{name: name, mode: 0o644, size: int64(len(data)), body: bytes.NewReader(data)})
	if err != nil {
		return fmt.Errorf("workspace: write %s: %w", file, err)
	}
	return nil
}

// Halt kills and removes the workspace container alone, so nothing in it
// can reach the proxies while their logs are read. Close still removes
// the rest.
func (c *Container) Halt(ctx context.Context) error {
	return removeContainer(ctx, c.cli, c.id)
}
