package model

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// DefaultUpstream is the model API the proxy forwards to unless told
// otherwise.
const DefaultUpstream = "https://api.anthropic.com"

// The two routes the proxy serves. Every other method or path is refused
// without reaching the upstream.
const (
	helloPath    = "/api/hello"
	messagesPath = "/v1/messages"
)

// ParseUpstream accepts an http or https URL with a host and nothing else:
// no path, query, fragment or userinfo. Empty is DefaultUpstream.
func ParseUpstream(raw string) (*url.URL, error) {
	if raw == "" {
		raw = DefaultUpstream
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("model: upstream: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("model: upstream must be an http or https URL")
	case u.Host == "" || u.Opaque != "":
		return nil, errors.New("model: upstream has no host")
	case u.User != nil, u.RawQuery != "", u.ForceQuery, u.Fragment != "", u.Path != "" && u.Path != "/":
		return nil, errors.New("model: upstream must be a scheme and a host only")
	}
	return u, nil
}

// handler serves one proxy's requests.
type handler struct {
	p       *Proxy
	forward http.Handler
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && r.URL.Path == helloPath:
		w.WriteHeader(http.StatusOK)
		return
	case r.Method != http.MethodPost || r.URL.Path != messagesPath:
		refuse(w, http.StatusNotFound, notFoundBody)
		return
	case !h.p.authorized(r):
		refuse(w, http.StatusUnauthorized, unauthorizedBody)
		return
	}
	if status, body, ok := h.p.admit(); !ok {
		refuse(w, status, body)
		return
	}
	h.forward.ServeHTTP(w, r)
}

func refuse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// reverseProxy forwards to the upstream with the real key in place of the
// token, asks for an uncompressed answer the meter can read, and meters
// the body as it streams to the client, flushing every write. fail stops
// the proxy when a usage line cannot be written.
func (p *Proxy) reverseProxy(fail func(error)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(p.upstream)
			pr.Out.URL.Path, pr.Out.URL.RawPath = messagesPath, ""
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Set("x-api-key", p.key)
			pr.Out.Header.Set("Accept-Encoding", "identity")
		},
		Transport:     p.transport,
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			if enc := res.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
				return fmt.Errorf("model: upstream answered %s-encoded; the proxy meters only plain bodies", enc)
			}
			status := res.StatusCode
			res.Body = &meteredBody{
				ReadCloser: res.Body,
				parser:     parserFor(res.Header.Get("Content-Type")),
				record: func(u domain.TokenUsage) {
					if err := p.record(status, u); err != nil {
						fail(err)
					}
				},
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			refuse(w, http.StatusBadGateway, `{"type":"error","error":{"type":"api_error","message":"forja: upstream unreachable"}}`)
		},
	}
}
