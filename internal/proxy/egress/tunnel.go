package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// established is the answer that turns the connection into a tunnel.
const established = "HTTP/1.1 200 Connection Established\r\n\r\n"

// connect tunnels a CONNECT to a listed destination that resolves to a
// public address, and denies every other one. An upstream that cannot be
// reached is a 502, not a denial: the policy let it through.
func (g *Gateway) connect(w http.ResponseWriter, r *http.Request) error {
	destination := strings.ToLower(r.URL.Host)
	if destination == "" {
		destination = strings.ToLower(r.Host)
	}
	if !g.allow[destination] {
		return g.deny(w, domain.EgressConnect, destination)
	}
	upstream, err := g.dial(r.Context(), "tcp", destination)
	if errors.Is(err, ErrNonPublicDestination) {
		return g.deny(w, domain.EgressConnect, destination)
	}
	if err != nil {
		http.Error(w, "egress upstream unreachable", http.StatusBadGateway)
		return nil
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "egress tunnel unsupported", http.StatusInternalServerError)
		return nil
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return nil
	}
	if _, err := io.WriteString(client, established); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return nil
	}
	pipe(r.Context(), client, buffered.Reader, upstream)
	return nil
}

// pipe copies both ways until either side closes or ctx ends, then closes
// both connections and returns once neither copy is running. fromClient
// is the client's buffered reader: bytes the client sent right behind the
// CONNECT may already sit in it.
func pipe(ctx context.Context, client net.Conn, fromClient *bufio.Reader, upstream net.Conn) {
	closeBoth := func() {
		_ = client.Close()
		_ = upstream.Close()
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	up := make(chan struct{})
	go func() {
		defer close(up)
		_, _ = io.Copy(upstream, fromClient)
		closeWrite(upstream)
	}()
	_, _ = io.Copy(client, upstream)
	closeBoth()
	<-up
}

// closeWrite tells the upstream the client is done sending, keeping the
// way back open for the rest of the answer.
func closeWrite(c net.Conn) {
	if tcp, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = c.Close()
}
