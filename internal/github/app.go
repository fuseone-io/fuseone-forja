package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// AppTokenSource mints an installation token per call, restricted to the
// repository and to the permissions asked for. Nothing is cached: a token
// lives as long as the call that needed it.
type AppTokenSource struct {
	AppID          string
	InstallationID string
	Key            *rsa.PrivateKey
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
	// Now is the clock the JWT is stamped with. It is injected so the
	// claims are testable; a nil clock is a configuration error.
	Now func() time.Time
}

type mintRequest struct {
	Repositories []string    `json:"repositories"`
	Permissions  Permissions `json:"permissions"`
}

type mintResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Token signs an App JWT and exchanges it for an installation token scoped
// to repo and to perms. An empty perms would mint whatever the installation
// allows, so it is refused. The JWT and the token appear in no error this
// method returns.
func (s *AppTokenSource) Token(ctx context.Context, repo string, perms Permissions) (Token, error) {
	if s.Now == nil || s.Key == nil {
		return Token{}, errors.New("github: AppTokenSource needs Key and Now")
	}
	if len(perms) == 0 {
		return Token{}, fmt.Errorf("github: mint installation token for %s: no permissions asked for", repo)
	}
	_, name, err := splitRepo(repo)
	if err != nil {
		return Token{}, err
	}
	jwt, err := signJWT(s.Key, s.AppID, s.Now())
	if err != nil {
		return Token{}, fmt.Errorf("github: mint installation token for %s: %w", repo, err)
	}
	body, err := json.Marshal(mintRequest{Repositories: []string{name}, Permissions: perms})
	if err != nil {
		return Token{}, fmt.Errorf("github: mint installation token for %s: %w", repo, err)
	}
	var minted mintResponse
	api := newAPIClient(s.HTTP, s.BaseURL, jwt)
	call := apiCall{path: "/app/installations/" + url.PathEscape(s.InstallationID) + "/access_tokens", body: body, want: http.StatusCreated}
	if err := api.do(ctx, call, &minted); err != nil {
		return Token{}, fmt.Errorf("github: mint installation token for %s: %w", repo, err)
	}
	if minted.Token == "" {
		return Token{}, fmt.Errorf("github: mint installation token for %s: response carries no token", repo)
	}
	return Token{Value: minted.Token, ExpiresAt: minted.ExpiresAt}, nil
}
