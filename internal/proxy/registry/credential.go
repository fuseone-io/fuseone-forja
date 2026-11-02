package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// redacted is how a Credential prints, whatever the verb.
const redacted = "registry.Credential{redacted}"

// Credential is the read credential of one area on one registry. It never
// prints and never marshals: the only way out is EncodeCredentials, into
// the proxy's own container.
type Credential struct {
	Username string
	Password string
}

func (Credential) String() string   { return redacted }
func (Credential) GoString() string { return redacted }

// Format covers every fmt verb, %d and %x included, not only those that
// reach String.
func (Credential) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalJSON keeps the credential out of any JSON a caller builds around it.
func (Credential) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// wireCredential is the form of one entry in the credentials file.
type wireCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoadCredentials reads the file at path: a JSON object from registry
// entry to {"username", "password"}. A malformed file is an error that
// never quotes what the file holds.
func LoadCredentials(path string) (map[string]Credential, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("registry credentials: %w", err)
	}
	return parseCredentials(body)
}

func parseCredentials(body []byte) (map[string]Credential, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var raw map[string]wireCredential
	if err := dec.Decode(&raw); err != nil {
		return nil, decodeError(err)
	}
	if raw == nil || dec.More() {
		return nil, errors.New("registry credentials: want one JSON object of registry entry to {username, password}")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("registry credentials: data after the JSON object")
	}
	creds := make(map[string]Credential, len(raw))
	for _, entry := range sortedKeys(raw) {
		c := raw[entry]
		if err := checkEntry(entry, c); err != nil {
			return nil, err
		}
		creds[entry] = Credential(c)
	}
	return creds, nil
}

// decodeError describes a decoding failure by its offset only: the
// decoder's own message can quote a byte of the file.
func decodeError(err error) error {
	var syntax *json.SyntaxError
	var kind *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Errorf("registry credentials: not valid JSON at byte %d", syntax.Offset)
	case errors.As(err, &kind):
		return fmt.Errorf("registry credentials: a value at byte %d is not a string", kind.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errors.New("registry credentials: the file ends before the JSON object does")
	}
	return errors.New("registry credentials: want registry entry to {username, password} and no other field")
}

// checkEntry names the entry only once it is known to be a registry, so
// a secret written where the entry goes is never printed.
func checkEntry(entry string, c wireCredential) error {
	if err := domain.ValidRegistry(entry); err != nil {
		return fmt.Errorf("registry credentials: a key is not a registry entry: %w", err)
	}
	switch {
	case c.Username == "":
		return fmt.Errorf("registry credentials: %s has no username", entry)
	case c.Password == "":
		return fmt.Errorf("registry credentials: %s has no password", entry)
	case strings.Contains(c.Username, ":"):
		return fmt.Errorf("registry credentials: the username of %s has a colon, which Basic authentication cannot carry", entry)
	}
	return nil
}

func sortedKeys(m map[string]wireCredential) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EncodeCredentials is the credentials file for creds, the form
// LoadCredentials reads.
func EncodeCredentials(creds map[string]Credential) ([]byte, error) {
	wire := make(map[string]wireCredential, len(creds))
	for entry, c := range creds {
		wire[entry] = wireCredential(c)
	}
	return json.Marshal(wire)
}
