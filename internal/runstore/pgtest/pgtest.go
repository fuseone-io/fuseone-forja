// Package pgtest starts a throwaway Postgres in a Docker container for the
// tests that run a store against the real thing. It is imported by tests
// only.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"net/url"
	"os"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Image is the Postgres the tests run against.
const Image = "postgres:16-alpine"

const startTimeout = 3 * time.Minute

var pgPort = network.MustParsePort("5432/tcp")

// Server is one Postgres container. Each database it hands out is empty
// and its own, so parallel tests never see each other's rows.
type Server struct {
	base url.URL
}

// Start runs a Postgres container removed when t ends, and waits until it
// accepts connections. Without FORJA_DOCKER_TEST=1 the test is skipped; with
// it, a daemon that does not answer fails the test.
func Start(t testing.TB) *Server {
	t.Helper()
	if os.Getenv("FORJA_DOCKER_TEST") != "1" {
		t.Skip("set FORJA_DOCKER_TEST=1 (make docker-test) to run the tests against a Postgres container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("pgtest: docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	password := randomHex(t)
	port := runContainer(ctx, t, cli, password)
	s := &Server{base: url.URL{
		Scheme: "postgres", User: url.UserPassword("postgres", password),
		Host: "127.0.0.1:" + port, Path: "/postgres", RawQuery: "sslmode=disable",
	}}
	waitReady(ctx, t, s.base.String())
	return s
}

// Database creates an empty database and returns its URL.
func (s *Server) Database(t testing.TB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, s.base.String())
	if err != nil {
		t.Fatalf("pgtest: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := "t_" + randomHex(t)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	u := s.base
	u.Path = "/" + name
	return u.String()
}

// runContainer starts the container with its port published on loopback
// and returns the host port.
func runContainer(ctx context.Context, t testing.TB, cli *client.Client, password string) string {
	t.Helper()
	ensureImage(ctx, t, cli)
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        Image,
			Env:          []string{"POSTGRES_PASSWORD=" + password},
			ExposedPorts: network.PortSet{pgPort: {}},
			Labels:       map[string]string{"forja.test": "pgtest"},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{pgPort: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
		},
	})
	if err != nil {
		t.Fatalf("pgtest: create container: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("pgtest: start container: %v", err)
	}
	inspect, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("pgtest: inspect container: %v", err)
	}
	bindings := inspect.Container.NetworkSettings.Ports[pgPort]
	if len(bindings) == 0 || bindings[0].HostPort == "" {
		t.Fatalf("pgtest: %s published no host port: %+v", pgPort, inspect.Container.NetworkSettings.Ports)
	}
	return bindings[0].HostPort
}

func ensureImage(ctx context.Context, t testing.TB, cli *client.Client) {
	t.Helper()
	_, err := cli.ImageInspect(ctx, Image)
	if err == nil {
		return
	}
	if !cerrdefs.IsNotFound(err) {
		t.Fatalf("pgtest: inspect image: %v", err)
	}
	pull, err := cli.ImagePull(ctx, Image, client.ImagePullOptions{})
	if err == nil {
		err = pull.Wait(ctx)
	}
	if err != nil {
		t.Fatalf("pgtest: pull %s: %v", Image, err)
	}
}

// waitReady polls until the server answers over TCP. The image's first
// boot serves only its socket while it initialises, so a TCP answer is the
// final server.
func waitReady(ctx context.Context, t testing.TB, dsn string) {
	t.Helper()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
			if err == nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pgtest: postgres never answered: %v", err)
		case <-tick.C:
		}
	}
}

func randomHex(t testing.TB) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("pgtest: random: %v", err)
	}
	return hex.EncodeToString(b[:])
}
