package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// jwtBackdate guards against clock skew between here and GitHub.
	jwtBackdate = 60 * time.Second
	// jwtLifetime keeps exp - iat within GitHub's ten-minute ceiling.
	jwtLifetime = 9 * time.Minute
)

type jwtClaims struct {
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Iss string `json:"iss"`
}

// signJWT builds the RS256 App JWT GitHub expects: iss is the App ID, iat is
// backdated and exp is less than ten minutes after it.
func signJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(jwtClaims{
		Iat: now.Add(-jwtBackdate).Unix(),
		Exp: now.Add(jwtLifetime).Unix(),
		Iss: appID,
	})
	if err != nil {
		return "", fmt.Errorf("encode jwt claims: %w", err)
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
