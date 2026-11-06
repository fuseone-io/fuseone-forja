// Package caller holds the clients allowed to call Forja over HTTP and
// authenticates a bearer against them. A client is a name, the SHA-256 of
// its bearer, the repositories it may reach and who rotates its bearer; the
// bearer itself is never stored.
package caller

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"slices"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
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
	c := r.clients[found]
	c.Repositories = slices.Clone(c.Repositories)
	return c, nil
}

// Lookup is the client named name, as the run engine scopes it: its name
// and the repositories it may reach. Every entry of a name has the same
// repositories, so whichever bearer authenticated it is the same client.
func (r Registry) Lookup(name string) (domain.Client, bool) {
	for _, c := range r.clients {
		if c.Name == name {
			return domain.Client{Name: c.Name, Repositories: slices.Clone(c.Repositories)}, true
		}
	}
	return domain.Client{}, false
}
