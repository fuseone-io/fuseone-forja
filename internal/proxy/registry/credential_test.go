package registry_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

const canaryPassword = "Qcanary-registry-secret-7f3a"

func TestCredential_format_redacted(t *testing.T) {
	t.Parallel()
	c := registry.Credential{Username: "forja-reader", Password: canaryPassword}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%q", "%x"} {
		for _, operand := range []any{c, &c, map[string]registry.Credential{"ghcr.io/my-org": c}} {
			if got := fmt.Sprintf(verb, operand); strings.Contains(got, canaryPassword) || strings.Contains(got, "forja-reader") {
				t.Errorf("Sprintf(%s, %T) = %q, prints the credential", verb, operand, got)
			}
		}
	}
	if b, err := json.Marshal(c); err != nil || strings.Contains(string(b), canaryPassword) {
		t.Errorf("json.Marshal = %s, %v; want the credential left out", b, err)
	}
}

func writeCredentials(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registries.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCredentials_wellFormed_byEntry(t *testing.T) {
	t.Parallel()
	path := writeCredentials(t, `{"ghcr.io/my-org": {"username": "u1", "password": "p1"}, "npm.pkg.github.com": {"username": "u2", "password": "p2"}}`)

	creds, err := registry.LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	want := map[string]registry.Credential{
		"ghcr.io/my-org":     {Username: "u1", Password: "p1"},
		"npm.pkg.github.com": {Username: "u2", Password: "p2"},
	}
	if len(creds) != len(want) {
		t.Fatalf("LoadCredentials = %d entries, want %d", len(creds), len(want))
	}
	for entry, c := range want {
		if creds[entry] != c {
			t.Errorf("entry %s did not load as written", entry)
		}
	}
}

// Every way the file can be wrong is a startup error, and none of them
// prints a byte of what the file holds.
func TestLoadCredentials_malformed_errorOmitsSecret(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"unquoted secret":       `{"ghcr.io/my-org": {"username": "u", "password": ` + canaryPassword + `}}`,
		"secret as a number":    `{"ghcr.io/my-org": {"username": "u", "password": 7357}}`,
		"secret as the key":     `{"` + canaryPassword + `": {"username": "u", "password": "p"}}`,
		"unknown field":         `{"ghcr.io/my-org": {"username": "u", "` + canaryPassword + `": "p"}}`,
		"no password":           `{"ghcr.io/my-org": {"username": "u"}}`,
		"no username":           `{"ghcr.io/my-org": {"password": "` + canaryPassword + `"}}`,
		"colon in username":     `{"ghcr.io/my-org": {"username": "u:x", "password": "` + canaryPassword + `"}}`,
		"not an object":         `["` + canaryPassword + `"]`,
		"null":                  `null`,
		"trailing data":         `{} "` + canaryPassword + `"`,
		"truncated after value": `{"ghcr.io/my-org": {"username": "u", "password": "` + canaryPassword,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := registry.LoadCredentials(writeCredentials(t, body))
			if err == nil {
				t.Fatal("LoadCredentials = nil error, want a refusal")
			}
			for _, leak := range []string{canaryPassword, "'Q'", "7357", "u:x"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q prints %q from the file", err, leak)
				}
			}
		})
	}
}

func TestLoadCredentials_missingFile_errorNamesPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.json")
	if _, err := registry.LoadCredentials(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("LoadCredentials(absent) = %v, want an error naming the path", err)
	}
}

// The file the control plane writes into the proxy's container decodes
// back to the same credentials, and holds only what it was given.
func TestEncodeCredentials_roundTrip(t *testing.T) {
	t.Parallel()
	creds := map[string]registry.Credential{"ghcr.io/my-org": {Username: "u", Password: canaryPassword}}
	body, err := registry.EncodeCredentials(creds)
	if err != nil {
		t.Fatalf("EncodeCredentials: %v", err)
	}
	got, err := registry.LoadCredentials(writeCredentials(t, string(body)))
	if err != nil || len(got) != 1 || got["ghcr.io/my-org"] != creds["ghcr.io/my-org"] {
		t.Fatalf("round trip = %d entries, %v; want the one written", len(got), err)
	}
}
