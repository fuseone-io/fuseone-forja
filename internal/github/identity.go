package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// botSuffix is what GitHub appends to an App's slug in the login it
// attributes the App's work to.
const botSuffix = "[bot]"

// Login is the login the App's installation tokens act as: its slug, read
// with an App JWT from GET /app, plus the [bot] suffix. The JWT appears in
// no error.
func (s *AppTokenSource) Login(ctx context.Context) (string, error) {
	if s.Now == nil || s.Key == nil {
		return "", errors.New("github: AppTokenSource needs Key and Now")
	}
	jwt, err := signJWT(s.Key, s.AppID, s.Now())
	if err != nil {
		return "", fmt.Errorf("github: read the App: %w", err)
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := newAPIClient(s.HTTP, s.BaseURL, jwt).do(ctx, apiCall{path: "/app", want: http.StatusOK}, &app); err != nil {
		return "", fmt.Errorf("github: read the App: %w", err)
	}
	if app.Slug == "" {
		return "", errors.New("github: read the App: response carries no slug")
	}
	return app.Slug + botSuffix, nil
}

// Login is the login of the user the personal access token belongs to,
// from GET /user. The token appears in no error.
func (s *StaticTokenSource) Login(ctx context.Context) (string, error) {
	if s.Value == "" {
		return "", errors.New("github: static token is empty")
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := newAPIClient(s.HTTP, s.BaseURL, s.Value).do(ctx, apiCall{path: "/user", want: http.StatusOK}, &user); err != nil {
		return "", fmt.Errorf("github: read the token's user: %w", err)
	}
	if user.Login == "" {
		return "", errors.New("github: read the token's user: response carries no login")
	}
	return user.Login, nil
}
