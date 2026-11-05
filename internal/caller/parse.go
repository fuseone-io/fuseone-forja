package caller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

// The errors Parse returns; each wraps the field or entry at fault.
var (
	ErrNoClients = errors.New("no clients")
	ErrMalformed = errors.New("malformed clients file")
)

// repositoryPattern is owner/name or owner/*: a wildcard stands for the
// whole name, never for the owner or part of a name.
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/([A-Za-z0-9_.-]+|\*)$`)

// namePattern keeps a client's name to one token that starts with a letter
// or digit, so that it cannot split a log line or pass for its "-".
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// hexRun is what a decoding error may echo of a misplaced hash, as a
// value or as a key; it is dropped before the error is returned.
var hexRun = regexp.MustCompile(`[0-9A-Fa-f]{16,}`)

type file struct {
	Clients []entry `yaml:"clients"`
}

type entry struct {
	Name          string   `yaml:"name"`
	TokenSHA256   string   `yaml:"token_sha256"`
	Repositories  []string `yaml:"repositories"`
	RotationOwner string   `yaml:"rotation_owner"`
}

// Parse reads the clients file. An unknown field, an entry missing a field,
// a hash that is not 64 hex characters, a repository that is not owner/name
// or owner/*, or one hash on two entries is ErrMalformed; a file with no
// entry is ErrNoClients. No error carries a hash.
func Parse(src []byte) (Registry, error) {
	f, err := decode(src)
	if err != nil {
		return Registry{}, err
	}
	if len(f.Clients) == 0 {
		return Registry{}, ErrNoClients
	}
	reg := Registry{clients: make([]Client, 0, len(f.Clients))}
	for i, e := range f.Clients {
		c, err := e.client()
		if err == nil {
			err = reg.admits(c)
		}
		if err != nil {
			return Registry{}, fmt.Errorf("%w: clients[%d]: %w", ErrMalformed, i, err)
		}
		reg.clients = append(reg.clients, c)
	}
	return reg, nil
}

// decode reads the one YAML document of src; a second document is
// refused rather than ignored.
func decode(src []byte) (file, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return file{}, fmt.Errorf("%w: %s", ErrMalformed, hexRun.ReplaceAllString(err.Error(), "…"))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return file{}, fmt.Errorf("%w: more than one YAML document", ErrMalformed)
	}
	return f, nil
}

// admits refuses a hash already on another entry, and a rotation entry
// whose repositories or rotation owner differ from the name's first
// entry: one name is one scope, whichever bearer is presented.
func (r Registry) admits(c Client) error {
	for _, prev := range r.clients {
		if prev.tokenSHA256 == c.tokenSHA256 {
			return fmt.Errorf("(%s): token_sha256 is already another entry's", c.Name)
		}
		if prev.Name == c.Name && (!slices.Equal(prev.Repositories, c.Repositories) || prev.RotationOwner != c.RotationOwner) {
			return fmt.Errorf("(%s): repositories and rotation_owner differ from the name's other entry", c.Name)
		}
	}
	return nil
}

func (e entry) client() (Client, error) {
	if !namePattern.MatchString(e.Name) {
		return Client{}, errors.New("name is not a letter or digit followed by letters, digits, '.', '_' or '-'")
	}
	sum, err := hex.DecodeString(e.TokenSHA256)
	if err != nil || len(sum) != sha256.Size {
		return Client{}, fmt.Errorf("(%s): token_sha256 is not %d hex characters", e.Name, 2*sha256.Size)
	}
	if len(e.Repositories) == 0 {
		return Client{}, fmt.Errorf("(%s): repositories is empty", e.Name)
	}
	for j, repo := range e.Repositories {
		if !repositoryPattern.MatchString(repo) {
			return Client{}, fmt.Errorf("(%s): repositories[%d] is not owner/name or owner/*", e.Name, j)
		}
	}
	if e.RotationOwner == "" {
		return Client{}, fmt.Errorf("(%s): rotation_owner is empty", e.Name)
	}
	c := Client{Name: e.Name, Repositories: slices.Compact(slices.Sorted(slices.Values(e.Repositories))), RotationOwner: e.RotationOwner}
	copy(c.tokenSHA256[:], sum)
	return c, nil
}
