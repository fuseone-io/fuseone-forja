package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// app answers GET /app for a JWT the App key signed, with the fields the
// REST description's 200 schema requires around slug; the values are the
// test's. Another bearer is the 401 the API gives a token that is not a
// JWT.
func (f *fakeApp) app(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.Count(auth, ".") != 2 {
		writeJSON(w, http.StatusUnauthorized, `{"message":"A JSON web token could not be decoded","status":"401"}`)
		return
	}
	f.jwt = strings.TrimPrefix(auth, "Bearer ")
	f.claims = f.verifyJWT(f.jwt)
	writeJSON(w, http.StatusOK, `{"id":12345,"slug":"forja-dev","node_id":"A_kwDOBforja","client_id":"Iv23liForja","owner":{"login":"fuseone-io","type":"Organization"},"name":"Forja Dev","permissions":{"contents":"write","metadata":"read","pull_requests":"write","statuses":"write","issues":"read"},"events":[]}`)
}

// The App's work is attributed to its slug with the [bot] suffix, read
// with a JWT the App key signed, never with an installation token.
func TestAppTokenSource_login_slugBotSignedWithJWT(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	fake, server := newFakeApp(t, &key.PublicKey, http.StatusCreated, accessTokensResponse())
	source := &github.AppTokenSource{AppID: "12345", InstallationID: "777", Key: key, BaseURL: server.URL, Now: fixedNow}

	login, err := source.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login != "forja-dev[bot]" {
		t.Fatalf("Login = %q, want the slug with the [bot] suffix", login)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assertClaims(t, fake.claims, "12345")
}

func TestAppTokenSource_loginWithoutKeyOrClock_failsBeforeTheAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a source with no clock or key reached the API")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	for name, source := range map[string]*github.AppTokenSource{
		"no clock": {AppID: "12345", Key: testKey(t), BaseURL: server.URL},
		"no key":   {AppID: "12345", BaseURL: server.URL, Now: fixedNow},
	} {
		if _, err := source.Login(context.Background()); err == nil {
			t.Errorf("%s: Login succeeded", name)
		}
	}
}

// A personal access token acts as its user: GET /user with the token.
func TestStaticTokenSource_login_fromUser(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, "github_pat_dev")
	source := &github.StaticTokenSource{Value: "github_pat_dev", BaseURL: fake.URL()}

	login, err := source.Login(context.Background())
	if err != nil || login != "phbpx" {
		t.Fatalf("Login = %q, %v; want the token's user phbpx", login, err)
	}
	wrong := &github.StaticTokenSource{Value: "github_pat_wrong", BaseURL: fake.URL()}
	_, err = wrong.Login(context.Background())
	if err == nil || strings.Contains(err.Error(), "github_pat_wrong") {
		t.Fatalf("Login with a rejected token = %v, want an error without the token", err)
	}
	if _, err := (&github.StaticTokenSource{BaseURL: fake.URL()}).Login(context.Background()); err == nil {
		t.Fatal("an empty static source logged in")
	}
}
