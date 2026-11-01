// Package github is the control plane's side of GitHub: the App credentials
// and the calls that use them. Nothing here is reachable from a workspace.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Token is a GitHub credential scoped to one repository. Every text form of
// it is redacted, so a token that reaches a log or an error by mistake shows
// no value.
type Token struct {
	Value string
	// ExpiresAt is zero for a token that does not expire on its own.
	ExpiresAt time.Time
}

func (Token) String() string { return "github token (redacted)" }

// GoString redacts the %#v form as well.
func (t Token) GoString() string { return t.String() }

// MarshalJSON redacts the JSON form, so a token caught in a serialised
// struct shows no value either.
func (t Token) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// Permissions is the set of installation permissions a token is minted
// with, keyed by scope name with the access level as value.
type Permissions map[string]string

// The two permission sets the control plane asks for: reading the contract
// and writing the refs that reserve an issue.
var (
	ReadContents  = Permissions{"contents": "read"}
	WriteContents = Permissions{"contents": "write"}
)

// TokenSource yields a GitHub token scoped to one repository and to the
// permissions of one call. The App implementation mints an installation
// token; the development implementation returns the configured PAT
// unchanged.
type TokenSource interface {
	Token(ctx context.Context, repo string, perms Permissions) (Token, error)
}

// StaticTokenSource hands out a fixed personal access token. Development
// only: it is restricted to nothing beyond what the PAT itself is, so the
// permissions asked for are ignored.
type StaticTokenSource struct {
	Value string
}

// Token returns the PAT for any repository and any permissions.
func (s *StaticTokenSource) Token(context.Context, string, Permissions) (Token, error) {
	if s.Value == "" {
		return Token{}, errors.New("github: static token is empty")
	}
	return Token{Value: s.Value}, nil
}
