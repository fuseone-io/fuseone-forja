package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/caller"
)

// The environment variables the MCP server over HTTP is configured from.
const (
	// EnvMCPAddr is the host:port serve speaks MCP over Streamable HTTP
	// on. Unset, serve speaks MCP over stdio.
	EnvMCPAddr = "FORJA_MCP_ADDR"
	// EnvClientsFile is the clients file: who may call, by the SHA-256 of
	// their bearer. Required with EnvMCPAddr.
	EnvClientsFile = "FORJA_CLIENTS_FILE"
	// EnvTLSCertFile and EnvTLSKeyFile are the PEM certificate and key the
	// HTTP server presents. Required unless EnvMCPAddr is on loopback.
	EnvTLSCertFile = "FORJA_TLS_CERT_FILE"
	EnvTLSKeyFile  = "FORJA_TLS_KEY_FILE"
	// EnvMCPAllowedOrigins is the comma-separated scheme://host[:port]
	// values a request's Origin may carry. Empty, any Origin is refused.
	EnvMCPAllowedOrigins = "FORJA_MCP_ALLOWED_ORIGINS"
)

// httpConfig is the MCP server over HTTP; the zero value is stdio.
type httpConfig struct {
	addr string
	// tls is nil only on a loopback address with no certificate.
	tls     *tls.Config
	clients caller.Registry
	// origins is what a request's Origin may be; nil refuses every Origin.
	origins originSet
	// drain is how long a stop waits for the calls in flight.
	drain time.Duration
}

// mcpHTTPConfig reads the HTTP transport from the environment. Unset
// EnvMCPAddr is stdio and reads nothing else. Otherwise a malformed
// address, a certificate without its key, a non-loopback address without a
// certificate, a clients file that is unset, unreadable, malformed or
// empty, or a malformed origin list is an error naming the variable.
func mcpHTTPConfig(getenv func(string) string) (httpConfig, error) {
	addr := getenv(EnvMCPAddr)
	if addr == "" {
		return httpConfig{}, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return httpConfig{}, fmt.Errorf("%s: %w", EnvMCPAddr, err)
	}
	tlsCfg, err := serverTLS(getenv)
	if err != nil {
		return httpConfig{}, err
	}
	if tlsCfg == nil && !isLoopback(host) {
		return httpConfig{}, fmt.Errorf("%s=%s is not loopback: set %s and %s, a bearer is never served in clear text",
			EnvMCPAddr, addr, EnvTLSCertFile, EnvTLSKeyFile)
	}
	clients, err := readClients(getenv(EnvClientsFile))
	if err != nil {
		return httpConfig{}, fmt.Errorf("%s: %w", EnvClientsFile, err)
	}
	origins, err := parseOrigins(getenv(EnvMCPAllowedOrigins))
	if err != nil {
		return httpConfig{}, fmt.Errorf("%s: %w", EnvMCPAllowedOrigins, err)
	}
	return httpConfig{addr: addr, tls: tlsCfg, clients: clients, origins: origins, drain: mcpShutdownTimeout}, nil
}

// isLoopback is a literal loopback IP. An empty host listens on every
// interface, and a name, localhost included, is resolved at bind time and
// may resolve off the machine.
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serverTLS loads the certificate and key; neither set is nil, one without
// the other is an error naming the missing one.
func serverTLS(getenv func(string) string) (*tls.Config, error) {
	certFile, keyFile := getenv(EnvTLSCertFile), getenv(EnvTLSKeyFile)
	switch {
	case certFile == "" && keyFile == "":
		return nil, nil
	case keyFile == "":
		return nil, fmt.Errorf("%s is set and %s is not", EnvTLSCertFile, EnvTLSKeyFile)
	case certFile == "":
		return nil, fmt.Errorf("%s is set and %s is not", EnvTLSKeyFile, EnvTLSCertFile)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%s, %s: %w", EnvTLSCertFile, EnvTLSKeyFile, err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

func readClients(path string) (caller.Registry, error) {
	if path == "" {
		return caller.Registry{}, fmt.Errorf("unset: %s requires a clients file, there is no anonymous mode", EnvMCPAddr)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return caller.Registry{}, fmt.Errorf("read: %w", err)
	}
	return caller.Parse(src)
}
