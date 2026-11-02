package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
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
	// fail stops the proxy when a line cannot be written.
	fail func(error)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && r.URL.Path == helloPath:
		w.WriteHeader(http.StatusOK)
	case r.Method != http.MethodPost || r.URL.Path != messagesPath:
		refuse(w, http.StatusNotFound, notFoundBody)
	case !h.p.authorized(r):
		refuse(w, http.StatusUnauthorized, unauthorizedBody)
	default:
		h.message(w, r)
	}
}

// checkedKey carries the checked request to the response's meter.
type checkedKey struct{}

// message forwards one Messages request, one at a time, with the body the
// proxy checked in place of the one it was sent.
func (h *handler) message(w http.ResponseWriter, r *http.Request) {
	release, ok := h.p.acquire(r.Context())
	if !ok {
		return
	}
	defer release()
	req, err := readBody(w, r)
	switch {
	case errors.Is(err, errTooLarge):
		refuse(w, http.StatusRequestEntityTooLarge, tooLargeBody)
		return
	case err != nil:
		refuse(w, http.StatusBadRequest, invalidBody)
		return
	}
	status, body, err := h.p.admit(req.maxTokens)
	if err != nil {
		h.fail(err)
	}
	if status != 0 {
		refuse(w, status, body)
		return
	}
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(req.body)), int64(len(req.body))
	r.Header.Del("Content-Length")
	h.forward.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), checkedKey{}, req)))
}

func refuse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// reverseProxy forwards to the upstream with the real key in place of the
// token and only the allowlisted betas, asks for an uncompressed answer the
// meter can read, and meters the body as it streams to the client,
// flushing every write. fail stops the proxy when a usage line cannot be
// written.
func (p *Proxy) reverseProxy(fail func(error)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(p.upstream)
			pr.Out.URL.Path, pr.Out.URL.RawPath = messagesPath, ""
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Set("x-api-key", p.key)
			pr.Out.Header.Set("Accept-Encoding", "identity")
			pr.Out.Header.Del("anthropic-beta")
			if kept := allowedBetas(pr.In.Header.Values("anthropic-beta")); kept != "" {
				pr.Out.Header.Set("anthropic-beta", kept)
			}
		},
		Transport:     p.transport,
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			if enc := res.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
				return fmt.Errorf("%w: upstream answered %s-encoded; the proxy meters only plain bodies", errUnmetered, enc)
			}
			status := res.StatusCode
			req, _ := res.Request.Context().Value(checkedKey{}).(checked)
			res.Body = &meteredBody{
				ReadCloser: res.Body,
				parser:     parserFor(res.Header.Get("Content-Type")),
				record: func(m metered) {
					if err := p.record(status, charge(status, m, req)); err != nil {
						fail(err)
					}
				},
			}
			return nil
		},
		ErrorHandler: p.unanswered(fail),
	}
}

// unanswered answers a request the upstream did not answer in a form the
// proxy could relay, charging it when the upstream may have spent on it.
func (p *Proxy) unanswered(fail func(error)) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, errUnmetered) || r.Context().Err() != nil {
			req, _ := r.Context().Value(checkedKey{}).(checked)
			if err := p.record(http.StatusBadGateway, charge(http.StatusOK, metered{}, req)); err != nil {
				fail(err)
			}
		}
		refuse(w, http.StatusBadGateway, `{"type":"error","error":{"type":"api_error","message":"forja: upstream unreachable"}}`)
	}
}

// errUnmetered marks an upstream answer the proxy could not meter. Like a
// request whose client left before the answer, it may have spent upstream,
// so it is charged as a success whose usage was never said.
var errUnmetered = errors.New("model: answer not metered")
