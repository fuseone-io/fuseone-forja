package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

// maxRedirects bounds the redirects the proxy follows for one request.
const maxRedirects = 10

// responseHeaderTimeout bounds the wait for an upstream's answer; the body
// that follows may take as long as it takes.
const responseHeaderTimeout = time.Minute

// errRedirectNotTLS refuses a redirect that would leave TLS.
var errRedirectNotTLS = errors.New("registry: redirect away from https")

// errTooManyRedirects ends a redirect loop.
var errTooManyRedirects = errors.New("registry: too many redirects")

// dropRequest are the request headers that never reach an upstream: the
// hop-by-hop ones, and whatever credential or session the workspace would
// send in place of the area's.
func dropRequest() []string {
	return []string{
		"Authorization", "Proxy-Authorization", "Cookie",
		"Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	}
}

// dropResponse are the response headers that never reach the workspace:
// the hop-by-hop ones, and a session an upstream opens for the area's
// credential.
func dropResponse() []string {
	return []string{
		"Set-Cookie",
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	}
}

// newClient dials every upstream through dial, redirects included, leaves
// bodies encoded as the upstream sent them, and never uses an environment
// proxy.
func newClient(dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dial,
			TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:     true,
			DisableCompression:    true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: responseHeaderTimeout,
		},
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect follows https redirects only, and strips the credential
// from any hop whose host:port is not the first request's. The standard
// library strips it only across domains; a subdomain or another port of
// the same host is not the registry the credential is for.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	if req.URL.Scheme != "https" {
		return errRedirectNotTLS
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// forward sends the request to https://<entry><rest> with the entry's
// credential, logs it, and streams the answer back unmodified. The line is
// written before the answer goes out: what the log cannot record, the
// workspace does not get.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, entry, rest string) error {
	res, err := p.roundTrip(r, entry, rest)
	switch {
	case errors.Is(err, egress.ErrNonPublicDestination), errors.Is(err, errRedirectNotTLS):
		return p.deny(w, entry, rest)
	case err != nil:
		logErr := p.record(entry, rest, domain.RegistryAllowed, http.StatusBadGateway)
		http.Error(w, "registry upstream unreachable", http.StatusBadGateway)
		return logErr
	}
	// The body was streamed or abandoned; closing it changes nothing sent.
	defer func() { _ = res.Body.Close() }()
	if err := p.record(entry, rest, domain.RegistryAllowed, res.StatusCode); err != nil {
		http.Error(w, "registry access not recorded", http.StatusBadGateway)
		return err
	}
	copyHeader(w.Header(), res.Header, dropResponse())
	w.WriteHeader(res.StatusCode)
	// A copy that stops midway is the client's or the upstream's to see.
	_, _ = io.Copy(w, res.Body)
	return nil
}

func (p *Proxy) roundTrip(r *http.Request, entry, rest string) (*http.Response, error) {
	target := "https://" + entry + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		return nil, err
	}
	out.ContentLength = r.ContentLength
	if r.ContentLength == 0 {
		out.Body = http.NoBody
	}
	copyHeader(out.Header, r.Header, dropRequest())
	c := p.creds[entry]
	out.SetBasicAuth(c.Username, c.Password)
	return p.client.Do(out)
}

// copyHeader copies every header of src into dst but those in drop.
func copyHeader(dst, src http.Header, drop []string) {
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	for _, k := range drop {
		dst.Del(k)
	}
}
