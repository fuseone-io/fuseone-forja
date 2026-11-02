package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

// The registry proxy container: the gateway's image and binary, reachable
// on the run's network by one name, holding the run's credentials in a
// file only its user can read.
const (
	registryAlias = "forja-registry"
	// registryCredentialsDir is created by the copy, under /run.
	registryCredentialsDir  = "/run"
	registryCredentialsName = "forja/registries.json"
	registryCredentialsPath = registryCredentialsDir + "/" + registryCredentialsName
	// registryOwner is gatewayUser as a number, for the file's owner.
	registryOwner = 65534
)

// registryURL is the address the workspace's setup points its tools at.
// It is an address, not a credential.
const registryURL = "http://" + registryAlias + ":" + registry.Port

// ErrRegistryCredentialMissing is the refusal of a launch whose contract
// declares a registry the operator holds no credential for.
var ErrRegistryCredentialMissing = errors.New("workspace: no credential for a declared registry")

// credentialsFor picks the credential of each declared entry, and only
// those: what the run does not declare never reaches its proxy.
func credentialsFor(all map[string]registry.Credential, entries []string) (map[string]registry.Credential, error) {
	picked := make(map[string]registry.Credential, len(entries))
	for _, entry := range entries {
		c, ok := all[entry]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrRegistryCredentialMissing, entry)
		}
		picked[entry] = c
	}
	return picked, nil
}

// createRegistry creates the run's registry proxy on its network under
// the alias the workspace's URL names. It is not started. The command
// names the entries and the file, never a credential: docker inspect
// shows both the command and the environment.
func (c *Container) createRegistry(ctx context.Context, entries []string) (string, error) {
	cmd := []string{gatewayDir + "/" + gatewayBin, "registry", "-run", c.runID,
		"-registries", strings.Join(entries, ","), "-credentials", registryCredentialsPath}
	netName := networkName(c.runID)
	created, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   netName + "-registry",
		Config: &container.Config{Image: gatewayImage, User: gatewayUser, Cmd: cmd, Labels: map[string]string{labelRun: c.runID}},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(netName),
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Runtime:     c.runtime,
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{registryAlias}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create registry proxy of run %s: %w", c.runID, err)
	}
	return created.ID, nil
}

// registrySpec is one registry proxy to start.
type registrySpec struct {
	id, runID, binary string
	creds             map[string]registry.Credential
}

// startRegistry copies the binary and the credentials in, gives the proxy
// its way out, starts it and waits for its readiness line.
func startRegistry(ctx context.Context, cli *client.Client, r registrySpec) error {
	if err := copyBinary(ctx, cli, r.id, r.binary, gatewayDir, gatewayBin); err != nil {
		return fmt.Errorf("workspace: copy registry binary: %w", err)
	}
	body, err := registry.EncodeCredentials(r.creds)
	if err != nil {
		return fmt.Errorf("workspace: encode registry credentials: %w", err)
	}
	err = copyFiles(ctx, cli, r.id, registryCredentialsDir,
		tarFile{name: path.Dir(registryCredentialsName) + "/", mode: 0o500, owner: registryOwner},
		tarFile{name: registryCredentialsName, mode: 0o400, owner: registryOwner, size: int64(len(body)), body: bytes.NewReader(body)},
	)
	if err != nil {
		return fmt.Errorf("workspace: copy registry credentials: %w", err)
	}
	if _, err := cli.NetworkConnect(ctx, "bridge", client.NetworkConnectOptions{Container: r.id}); err != nil {
		return fmt.Errorf("workspace: connect registry proxy to bridge: %w", err)
	}
	if _, err := cli.ContainerStart(ctx, r.id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("workspace: start registry proxy: %w", err)
	}
	ready := func(rd io.Reader) error { return registry.AwaitReady(rd, r.runID) }
	if err := awaitReady(ctx, cli, r.id, ready); err != nil {
		return fmt.Errorf("workspace: registry proxy of run %s: %w", r.runID, err)
	}
	return nil
}

// Registry reads the registry proxy's log and returns every request the
// workspace made to it. A run that declares no registry has no proxy and
// an empty report.
func (c *Container) Registry(ctx context.Context) (domain.RegistryReport, error) {
	if c.registry == "" {
		return domain.RegistryReport{}, ctx.Err()
	}
	var report domain.RegistryReport
	err := c.readLog(ctx, c.registry, "registry proxy", func(r io.Reader) error {
		var err error
		report, err = registry.ReadAccesses(r, c.runID)
		return err
	})
	if err != nil {
		return domain.RegistryReport{}, err
	}
	return report, nil
}
