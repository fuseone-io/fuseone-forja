// Package caller holds the clients allowed to call Forja over HTTP and
// authenticates a bearer against them. A client is a name, the SHA-256 of
// its bearer, the repositories it may reach and who rotates its bearer; the
// bearer itself is never stored.
package caller

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

// ErrUnknownBearer is a bearer whose hash is in no entry.
var ErrUnknownBearer = errors.New("unknown bearer")

// Client is one entry of the clients file.
type Client struct {
	// Name appears on runs and in logs; it stays the same across a
	// rotation, so two entries may share it.
	Name string
	// Repositories are owner/name or owner/* patterns.
	Repositories []string
	// RotationOwner is a team or role, never logged.
	RotationOwner string
	tokenSHA256   [sha256.Size]byte
}

// Registry is the parsed clients file. The zero Registry authenticates no
// one.
type Registry struct {
	clients []Client
}

// Authenticate returns the client whose hash is the bearer's. Every entry
// is compared, in constant time, whatever the outcome of the others, so
// the time taken does not depend on which entry matched or how much of a
// hash a guess shares.
func (r Registry) Authenticate(bearer string) (Client, error) {
	sum := sha256.Sum256([]byte(bearer))
	found := -1
	for i := range r.clients {
		eq := subtle.ConstantTimeCompare(sum[:], r.clients[i].tokenSHA256[:])
		found = subtle.ConstantTimeSelect(eq, i, found)
	}
	if found < 0 {
		return Client{}, ErrUnknownBearer
	}
	return r.clients[found], nil
}
