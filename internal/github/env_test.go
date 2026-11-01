package github_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

func writePEM(t *testing.T) string {
	t.Helper()
	key := testKey(t)
	path := filepath.Join(t.TempDir(), "forja.pem")
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o400); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return path
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func getenvFrom(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestTokenSourceFromEnv(t *testing.T) {
	t.Parallel()
	pemPath := writePEM(t)
	const garbage = "this-is-not-a-key-and-must-not-be-printed"
	garbagePath := writeFile(t, "garbage.pem", garbage)

	tests := []struct {
		name     string
		env      map[string]string
		wantType string
		wantErr  error
		absent   []string
	}{
		{
			name:     "app variables",
			env:      map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777", "FORJA_GITHUB_PRIVATE_KEY_FILE": pemPath},
			wantType: "*github.AppTokenSource",
		},
		{
			name:     "app variables and a token: the token is ignored",
			env:      map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777", "FORJA_GITHUB_PRIVATE_KEY_FILE": pemPath, "FORJA_GITHUB_TOKEN": "github_pat_dev"},
			wantType: "*github.AppTokenSource",
		},
		{
			name:     "only a token",
			env:      map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev"},
			wantType: "*github.StaticTokenSource",
		},
		{
			name:    "nothing",
			env:     map[string]string{},
			wantErr: github.ErrNoCredentials,
		},
		{
			name:   "app id without installation id",
			env:    map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_PRIVATE_KEY_FILE": pemPath},
			absent: []string{"FORJA_GITHUB_INSTALLATION_ID"},
		},
		{
			name:   "app id without key file",
			env:    map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777"},
			absent: []string{"FORJA_GITHUB_PRIVATE_KEY_FILE"},
		},
		{
			name: "key file unreadable",
			env:  map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777", "FORJA_GITHUB_PRIVATE_KEY_FILE": filepath.Join(t.TempDir(), "missing.pem")},
		},
		{
			name: "key file is not a key",
			env:  map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777", "FORJA_GITHUB_PRIVATE_KEY_FILE": garbagePath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source, err := github.TokenSourceFromEnv(getenvFrom(tt.env))
			if tt.wantType != "" {
				assertSourceType(t, source, err, tt.wantType)
				return
			}
			if err == nil {
				t.Fatalf("TokenSourceFromEnv = %T, want an error", source)
			}
			assertEnvError(t, err, tt.wantErr, tt.absent)
			if strings.Contains(err.Error(), garbage) {
				t.Errorf("error %q prints the key file's contents", err)
			}
		})
	}
}

// assertEnvError checks the sentinel when one is expected, that a
// misconfigured App is not reported as missing credentials, and that the
// error names each variable the caller must set.
func assertEnvError(t *testing.T, err, want error, absent []string) {
	t.Helper()
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if want == nil && errors.Is(err, github.ErrNoCredentials) {
		t.Fatalf("a misconfigured App is not missing credentials: %v", err)
	}
	for _, name := range absent {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func assertSourceType(t *testing.T, source github.TokenSource, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("TokenSourceFromEnv: %v", err)
	}
	if got := typeName(source); got != want {
		t.Fatalf("TokenSourceFromEnv = %s, want %s", got, want)
	}
}

func typeName(source github.TokenSource) string {
	switch source.(type) {
	case *github.AppTokenSource:
		return "*github.AppTokenSource"
	case *github.StaticTokenSource:
		return "*github.StaticTokenSource"
	default:
		return "unknown"
	}
}

func TestTokenSourceFromEnv_noCredentials_namesBothWays(t *testing.T) {
	t.Parallel()
	_, err := github.TokenSourceFromEnv(getenvFrom(nil))
	if !errors.Is(err, github.ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
	for _, name := range []string{"FORJA_GITHUB_APP_ID", "FORJA_GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestTokenSourceFromEnv_onlyToken_handsItOutUnchanged(t *testing.T) {
	t.Parallel()
	source, err := github.TokenSourceFromEnv(getenvFrom(map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev"}))
	if err != nil {
		t.Fatalf("TokenSourceFromEnv: %v", err)
	}
	tok, err := source.Token(context.Background(), "fuseone-io/fuseone-forja")
	if err != nil || tok.Value != "github_pat_dev" {
		t.Fatalf("Token = %+v, %v; want the PAT", tok, err)
	}
}
