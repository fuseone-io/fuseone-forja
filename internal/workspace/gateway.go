package workspace

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

// The gateway container: a small image the control plane's own binary is
// copied into, reachable on the run's network by one name.
const (
	gatewayImage = "alpine:3.20"
	gatewayAlias = "forja-gateway"
	gatewayDir   = "/usr/local/bin"
	gatewayBin   = "forjad"
	// gatewayUser is nobody: the gateway needs no privilege to listen on
	// its port and holds none.
	gatewayUser = "65534"
	// readyTimeout bounds the wait for the gateway's readiness line.
	readyTimeout = 10 * time.Second
)

// gatewayURL is the proxy the workspace is pointed at.
const gatewayURL = "http://" + gatewayAlias + ":" + egress.Port

// ErrNoGatewayBinary is the refusal of a launch when no linux forjad is
// configured and this process is not one.
var ErrNoGatewayBinary = errors.New("workspace: no gateway binary: set Options.GatewayBinary to a static linux forjad")

// resolveGatewayBinary is the configured binary or, on linux, this
// process's own executable; it must exist.
func resolveGatewayBinary(configured, goos string, executable func() (string, error)) (string, error) {
	path := configured
	if path == "" {
		if goos != "linux" {
			return "", ErrNoGatewayBinary
		}
		var err error
		if path, err = executable(); err != nil {
			return "", fmt.Errorf("workspace: gateway binary: %w", err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("workspace: gateway binary %s: %w", path, err)
	}
	return path, nil
}

// createGateway creates the run's gateway on its network under the alias
// the workspace's proxy variables name. It is not started.
func createGateway(ctx context.Context, cli *client.Client, runID string, allow []string) (string, error) {
	cmd := []string{gatewayDir + "/" + gatewayBin, "gateway", "-run", runID}
	if len(allow) > 0 {
		cmd = append(cmd, "-allow", strings.Join(allow, ","))
	}
	netName := networkName(runID)
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   netName + "-gateway",
		Config: &container.Config{Image: gatewayImage, User: gatewayUser, Cmd: cmd, Labels: map[string]string{labelRun: runID}},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(netName),
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{gatewayAlias}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create gateway of run %s: %w", runID, err)
	}
	return created.ID, nil
}

// startGateway copies the binary in, gives the gateway its way out when
// there is anywhere to go, starts it and waits for its readiness line.
func startGateway(ctx context.Context, cli *client.Client, g gatewaySpec) error {
	if err := copyBinary(ctx, cli, g.id, g.binary); err != nil {
		return fmt.Errorf("workspace: copy gateway binary: %w", err)
	}
	ready := func(r io.Reader) error { return egress.AwaitReady(r, g.runID) }
	if len(g.allow) > 0 {
		if _, err := cli.NetworkConnect(ctx, "bridge", client.NetworkConnectOptions{Container: g.id}); err != nil {
			return fmt.Errorf("workspace: connect gateway to bridge: %w", err)
		}
	}
	if _, err := cli.ContainerStart(ctx, g.id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("workspace: start gateway: %w", err)
	}
	if err := awaitReady(ctx, cli, g.id, ready); err != nil {
		return fmt.Errorf("workspace: gateway of run %s: %w", g.runID, err)
	}
	return nil
}

// gatewaySpec is one gateway to start.
type gatewaySpec struct {
	id, runID, binary string
	allow             []string
}

// copyBinary streams the binary in as root-owned 0755: the container's
// user runs it and cannot change it.
func copyBinary(ctx context.Context, cli *client.Client, id, binary string) error {
	f, err := os.Open(binary)
	if err != nil {
		return err
	}
	// The file was only read; a failure to close it changes no byte sent.
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return copyFile(ctx, cli, id, containerFile{dest: gatewayDir, name: gatewayBin, mode: 0o755, size: info.Size(), content: f})
}

// containerFile is one file to place in a container before it starts:
// name is relative to dest, and a parent directory in name is created
// owned like the file, readable and searchable by its owner only.
type containerFile struct {
	dest, name string
	mode       int64
	uid, gid   int
	size       int64
	content    io.Reader
}

// copyFile streams f into the container as a tar archive.
func copyFile(ctx context.Context, cli *client.Client, id string, f containerFile) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeContainerFile(pw, f)) }()
	_, err := cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: f.dest, Content: pr})
	// Ends the writer if the copy stopped before the archive did.
	_ = pr.CloseWithError(err)
	return err
}

func writeContainerFile(w io.Writer, f containerFile) error {
	tw := tar.NewWriter(w)
	if dir := path.Dir(f.name); dir != "." {
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o500, Uid: f.uid, Gid: f.gid, Typeflag: tar.TypeDir}); err != nil {
			return err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Uid: f.uid, Gid: f.gid, Size: f.size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f.content); err != nil {
		return err
	}
	return tw.Close()
}

// awaitReady follows a proxy's stdout until ready finds its readiness
// line, for at most readyTimeout. A proxy that exits first ends the stream.
func awaitReady(ctx context.Context, cli *client.Client, id string, ready func(io.Reader) error) error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	logs, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, Follow: true})
	if err != nil {
		return fmt.Errorf("follow log: %w", err)
	}
	err = readStdout(logs, ready)
	if ctx.Err() != nil {
		return fmt.Errorf("not ready within %v: %w", readyTimeout, ctx.Err())
	}
	return err
}

// readStdout demultiplexes the stdout of a log stream into read and closes
// the stream once read returns, which also ends the copy feeding it.
func readStdout(logs io.ReadCloser, read func(io.Reader) error) error {
	pr, pw := io.Pipe()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, err := stdcopy.StdCopy(pw, io.Discard, logs)
		pw.CloseWithError(err)
	}()
	err := read(pr)
	_ = logs.Close()
	_ = pr.Close()
	<-copied
	return err
}

// Egress reads the gateway's log and returns what it refused. A gateway
// that is no longer running may have missed attempts after it stopped,
// so its log is refused as evidence.
func (c *Container) Egress(ctx context.Context) (domain.EgressReport, error) {
	var report domain.EgressReport
	err := c.readLog(ctx, c.gateway, "gateway", func(r io.Reader) error {
		var err error
		report, err = egress.ReadDenials(r, c.runID)
		return err
	})
	if err != nil {
		return domain.EgressReport{}, err
	}
	return report, nil
}

// readLog hands the stdout of the run's proxy id, named what, to read,
// and refuses the log of a proxy that is no longer running: it may have
// missed what happened after it stopped.
func (c *Container) readLog(ctx context.Context, id, what string, read func(io.Reader) error) error {
	inspect, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("workspace: inspect %s: %w", what, err)
	}
	if state := inspect.Container.State; state == nil || !state.Running {
		return fmt.Errorf("workspace: %s of run %s is not running: its log may be incomplete", what, c.runID)
	}
	logs, err := c.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return fmt.Errorf("workspace: read %s log: %w", what, err)
	}
	if err := readStdout(logs, read); err != nil {
		return fmt.Errorf("workspace: %s log: %w", what, err)
	}
	return nil
}
