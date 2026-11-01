//go:build integration

package workspace_test

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// sslRequestCode is the code a Postgres client sends, in place of a
// protocol version, to ask whether the server speaks TLS.
const sslRequestCode = 80877103

// TestPostgresSidecar_answersSSLRequestByName runs inside a Forja run,
// where the contract's postgres service is a sidecar the tree reaches by
// name. A name that does not resolve fails: a skip would let the gate
// pass with no Postgres at all.
func TestPostgresSidecar_answersSSLRequestByName(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "postgres:5432", 10*time.Second)
	if err != nil {
		t.Fatalf("dial postgres:5432: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	request := make([]byte, 8)
	binary.BigEndian.PutUint32(request[0:4], 8)
	binary.BigEndian.PutUint32(request[4:8], sslRequestCode)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("send SSLRequest: %v", err)
	}
	answer := make([]byte, 1)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("read the answer to SSLRequest: %v", err)
	}
	if answer[0] != 'S' && answer[0] != 'N' {
		t.Fatalf("answer to SSLRequest = %q, want S or N from a Postgres server", answer)
	}
}
