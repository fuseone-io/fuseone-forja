package github

import (
	"net/http"
	"testing"
)

// A caller that supplies no client must not end up on http.DefaultClient,
// which never times out.
func TestNewAPIClient_noClient_hasATimeout(t *testing.T) {
	t.Parallel()
	api := newAPIClient(nil, "", "token")
	if api.http == http.DefaultClient || api.http.Timeout <= 0 {
		t.Fatalf("default client = %+v, want one with a timeout", api.http)
	}
	if api.base != DefaultBaseURL {
		t.Fatalf("base = %q, want %q", api.base, DefaultBaseURL)
	}
}
