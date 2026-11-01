package github

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// The environment variables the control plane reads its credentials from.
const (
	EnvAppID          = "FORJA_GITHUB_APP_ID"
	EnvInstallationID = "FORJA_GITHUB_INSTALLATION_ID"
	EnvPrivateKeyFile = "FORJA_GITHUB_PRIVATE_KEY_FILE"
	EnvToken          = "FORJA_GITHUB_TOKEN"
)

// ErrNoCredentials means neither the App variables nor the development
// token are set.
var ErrNoCredentials = errors.New("no GitHub credentials: set FORJA_GITHUB_APP_ID, " +
	"FORJA_GITHUB_INSTALLATION_ID and FORJA_GITHUB_PRIVATE_KEY_FILE, or FORJA_GITHUB_TOKEN for development")

// TokenSourceFromEnv picks the credential the environment declares: the App
// when FORJA_GITHUB_APP_ID is set (FORJA_GITHUB_TOKEN is then ignored), the
// static token when only it is set, ErrNoCredentials when neither is. The
// private key is read from the path in FORJA_GITHUB_PRIVATE_KEY_FILE, never
// from a variable's value.
func TokenSourceFromEnv(getenv func(string) string) (TokenSource, error) {
	if appID := getenv(EnvAppID); appID != "" {
		return appSourceFromEnv(appID, getenv)
	}
	if token := getenv(EnvToken); token != "" {
		return &StaticTokenSource{Value: token}, nil
	}
	return nil, ErrNoCredentials
}

func appSourceFromEnv(appID string, getenv func(string) string) (TokenSource, error) {
	installation := getenv(EnvInstallationID)
	if installation == "" {
		return nil, fmt.Errorf("%s is set but %s is empty", EnvAppID, EnvInstallationID)
	}
	keyFile := getenv(EnvPrivateKeyFile)
	if keyFile == "" {
		return nil, fmt.Errorf("%s is set but %s is empty", EnvAppID, EnvPrivateKeyFile)
	}
	key, err := readPrivateKey(keyFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvPrivateKeyFile, err)
	}
	// The wall clock is injected here, at the edge where the process is
	// assembled from its environment.
	return &AppTokenSource{AppID: appID, InstallationID: installation, Key: key, Now: time.Now}, nil
}

// readPrivateKey loads a PEM-encoded RSA key (PKCS#1, as GitHub issues it,
// or PKCS#8). Errors describe the failure, never the file's contents.
func readPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("private key file holds no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("private key is neither PKCS#1 nor PKCS#8 RSA")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}
