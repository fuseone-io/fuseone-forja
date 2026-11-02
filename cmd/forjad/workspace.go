package main

import (
	"fmt"
	"io"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// The environment variables the workspaces are configured from.
const (
	// EnvGatewayBinary is a static linux forjad for the Docker daemon's
	// architecture, copied into every run's egress gateway. Unset, the
	// server's own executable is used on linux and a launch is refused
	// elsewhere.
	EnvGatewayBinary = "FORJA_GATEWAY_BINARY"
	// EnvEgressAllow is the comma-separated host:port a workspace may
	// tunnel to through its gateway, matched exactly. Unset, nothing
	// leaves.
	EnvEgressAllow = "FORJA_EGRESS_ALLOW"
	// EnvRegistryCredentials is the path to a JSON file, mounted from the
	// vault, of each registry entry's read credential. Unset, a run whose
	// contract declares a registry is refused at launch.
	EnvRegistryCredentials = "FORJA_REGISTRY_CREDENTIALS"
	// EnvWorkspaceRuntime is the Docker runtime, as the daemon lists it,
	// every container of a run is created under: the workspace, its
	// sidecars and its proxies. Unset, the daemon's default runtime
	// applies and the server says so once at startup.
	EnvWorkspaceRuntime = "FORJA_WORKSPACE_RUNTIME"
)

// workspaceOptions assembles the Docker driver's options from the
// environment. A malformed allowlist or credentials file is an error; an
// unset runtime is one warning on stderr.
func workspaceOptions(getenv func(string) string, stderr io.Writer) (workspace.Options, error) {
	allow, err := egress.ParseAllow(getenv(EnvEgressAllow))
	if err != nil {
		return workspace.Options{}, fmt.Errorf("%s: %w", EnvEgressAllow, err)
	}
	creds, err := loadRegistryCredentials(getenv)
	if err != nil {
		return workspace.Options{}, err
	}
	runtime := getenv(EnvWorkspaceRuntime)
	if runtime == "" {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: %s is not set: workspaces run under the Docker daemon's default runtime\n", EnvWorkspaceRuntime)
	}
	return workspace.Options{GatewayBinary: getenv(EnvGatewayBinary), EgressAllow: allow, RegistryCredentials: creds, Runtime: runtime}, nil
}

// loadRegistryCredentials reads the file the variable names; unset, there
// is no credential.
func loadRegistryCredentials(getenv func(string) string) (map[string]registry.Credential, error) {
	path := getenv(EnvRegistryCredentials)
	if path == "" {
		return map[string]registry.Credential{}, nil
	}
	creds, err := registry.LoadCredentials(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvRegistryCredentials, err)
	}
	return creds, nil
}
