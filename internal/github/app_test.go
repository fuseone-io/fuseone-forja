package github_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// The response copies the documented shape of POST
// /app/installations/{id}/access_tokens (token, expires_at, permissions,
// repository_selection, repositories as objects). The token value is not
// 40 characters on purpose: newly minted tokens are not.
const mintedToken = "ghs_12345_eyJhbGciOiJSUzI1NiJ9.notreallyajwt.signature"

func accessTokensResponse() string {
	return fmt.Sprintf(`{"token":%q,"expires_at":"2026-11-01T13:00:00Z","permissions":{"contents":"read","issues":"read"},"repository_selection":"selected","repositories":[{"id":1296269,"node_id":"MDEwOlJlcG9zaXRvcnkxMjk2MjY5","name":"fuseone-forja","full_name":"fuseone-io/fuseone-forja","private":false}]}`, mintedToken)
}

// jwtClaims is what the fake endpoint read from the Authorization header.
type jwtClaims struct {
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

type mintRequest struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

// fakeApp serves the access_tokens endpoint and verifies the JWT against
// the test key's public half.
type fakeApp struct {
	t      *testing.T
	pub    *rsa.PublicKey
	status int
	body   string

	mu     sync.Mutex
	claims jwtClaims
	req    mintRequest
	jwt    string
}

func newFakeApp(t *testing.T, pub *rsa.PublicKey, status int, body string) (*fakeApp, *httptest.Server) {
	t.Helper()
	f := &fakeApp{t: t, pub: pub, status: status, body: body}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.mint)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeApp) mint(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, `{"message":"A JSON web token could not be decoded","status":"401"}`)
		return
	}
	f.jwt = strings.TrimPrefix(auth, "Bearer ")
	f.claims = f.verifyJWT(f.jwt)
	if err := json.NewDecoder(r.Body).Decode(&f.req); err != nil {
		f.t.Errorf("decode mint body: %v", err)
	}
	if r.PathValue("id") != "777" {
		f.t.Errorf("installation id = %q, want 777", r.PathValue("id"))
	}
	body := f.body
	if f.status >= 500 {
		// A hostile or buggy server echoes the request: the error the
		// caller sees must still carry none of it.
		body = fmt.Sprintf(`{"message":"boom %s"}`, auth)
	}
	writeJSON(w, f.status, body)
}

// verifyJWT checks the RS256 signature and returns the claims.
func (f *fakeApp) verifyJWT(token string) jwtClaims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		f.t.Errorf("jwt has %d parts, want 3", len(parts))
		return jwtClaims{}
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !strings.Contains(string(header), `"RS256"`) {
		f.t.Errorf("jwt header %q is not RS256 (%v)", header, err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		f.t.Errorf("jwt payload: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		f.t.Errorf("jwt signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, digest[:], sig); err != nil {
		f.t.Errorf("jwt signature does not verify against the App key: %v", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		f.t.Errorf("jwt claims: %v", err)
	}
	return claims
}

func TestAppTokenSource_mintsContentsReadAndIssuesReadForTheRepository(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	fake, server := newFakeApp(t, &key.PublicKey, http.StatusCreated, accessTokensResponse())
	source := &github.AppTokenSource{AppID: "12345", InstallationID: "777", Key: key, BaseURL: server.URL, Now: fixedNow}

	tok, err := source.Token(context.Background(), "fuseone-io/fuseone-forja")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.Value != mintedToken {
		t.Errorf("Token.Value = %q, want the minted token", tok.Value)
	}
	if want := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC); !tok.ExpiresAt.Equal(want) {
		t.Errorf("Token.ExpiresAt = %v, want %v", tok.ExpiresAt, want)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assertClaims(t, fake.claims, "12345")
	assertMintRequest(t, fake.req)
}

// assertClaims checks iss and that the JWT window is at most ten minutes
// and open at the fixed clock.
func assertClaims(t *testing.T, claims jwtClaims, appID string) {
	t.Helper()
	if claims.Iss != appID {
		t.Errorf("iss = %q, want the App ID %q", claims.Iss, appID)
	}
	now := fixedNow().Unix()
	window := claims.Exp - claims.Iat
	if claims.Iat > now || claims.Exp <= now || window > int64((10*time.Minute).Seconds()) {
		t.Errorf("claims iat=%d exp=%d around now=%d: want iat <= now < exp and exp - iat <= 10m", claims.Iat, claims.Exp, now)
	}
}

func assertMintRequest(t *testing.T, req mintRequest) {
	t.Helper()
	if got := req.Repositories; len(got) != 1 || got[0] != "fuseone-forja" {
		t.Errorf("repositories = %q, want [fuseone-forja] (name only)", got)
	}
	// Both read-only: the clone and the issue the agent will see. Nothing
	// that writes is minted here.
	want := map[string]string{"contents": "read", "issues": "read"}
	if !reflect.DeepEqual(req.Permissions, want) {
		t.Errorf("permissions = %v, want exactly %v", req.Permissions, want)
	}
}

func TestToken_textAndJSONForms_redacted(t *testing.T) {
	t.Parallel()
	tok := github.Token{Value: mintedToken, ExpiresAt: fixedNow()}
	printed := fmt.Sprintf("%v %+v %s %#v %q", tok, tok, tok.String(), tok, tok.String())
	if strings.Contains(printed, mintedToken) || strings.Contains(printed, "ghs_") {
		t.Fatalf("a printed token shows its value: %s", printed)
	}
	encoded, err := json.Marshal(struct{ Tok github.Token }{tok})
	if err != nil || strings.Contains(string(encoded), mintedToken) || strings.Contains(string(encoded), "ghs_") {
		t.Fatalf("a serialised token shows its value: %s (%v)", encoded, err)
	}
}

// A failing mint must not surface the JWT it sent, even when the server
// echoes the request back.
func TestAppTokenSource_tokenNeverLogged(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	fake, server := newFakeApp(t, &key.PublicKey, http.StatusInternalServerError, "")
	source := &github.AppTokenSource{AppID: "12345", InstallationID: "777", Key: key, BaseURL: server.URL, Now: fixedNow}
	_, err := source.Token(context.Background(), "fuseone-io/fuseone-forja")
	if err == nil {
		t.Fatal("Token succeeded on a 500")
	}
	fake.mu.Lock()
	jwt := fake.jwt
	fake.mu.Unlock()
	if jwt == "" {
		t.Fatal("the fake saw no JWT")
	}
	if strings.Contains(err.Error(), jwt) || strings.Contains(err.Error(), "ghs_") || strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q carries the credential or the echoed request", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not carry the status", err)
	}
}

func TestAppTokenSource_zeroNowOrKey_failsLoudly(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a source with no clock or key reached the API")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	sources := map[string]*github.AppTokenSource{
		"no clock": {AppID: "12345", InstallationID: "777", Key: testKey(t), BaseURL: server.URL},
		"no key":   {AppID: "12345", InstallationID: "777", BaseURL: server.URL, Now: fixedNow},
	}
	for name, source := range sources {
		if _, err := source.Token(context.Background(), "fuseone-io/fuseone-forja"); err == nil {
			t.Errorf("%s: Token succeeded", name)
		}
	}
}

func TestStaticTokenSource_returnsThePATUnchanged(t *testing.T) {
	t.Parallel()
	source := &github.StaticTokenSource{Value: "github_pat_dev"}

	tok, err := source.Token(context.Background(), "fuseone-io/fuseone-forja")
	if err != nil || tok.Value != "github_pat_dev" || !tok.ExpiresAt.IsZero() {
		t.Fatalf("Token = %+v, %v; want the PAT with no expiry", tok, err)
	}
	if _, err := (&github.StaticTokenSource{}).Token(context.Background(), "fuseone-io/fuseone-forja"); err == nil {
		t.Fatal("an empty static source handed out an empty token")
	}
}
