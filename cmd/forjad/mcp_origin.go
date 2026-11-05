package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxLoggedOrigin bounds the bytes of a refused Origin a log line carries.
const maxLoggedOrigin = 256

// originSet is the Origin values a request may carry, lower-cased. The
// zero value allows none.
type originSet map[string]struct{}

func (s originSet) allows(origin string) bool {
	_, ok := s[strings.ToLower(origin)]
	return ok
}

// parseOrigins reads a comma-separated list of exact scheme://host[:port]
// values. A blank list is empty; an empty entry, a wildcard, anything with
// a path, query, fragment or user, or a value no browser serialises (a
// non-ASCII host, the scheme's default port) is an error naming the entry.
func parseOrigins(raw string) (originSet, error) {
	set := originSet{}
	if strings.TrimSpace(raw) == "" {
		return set, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		origin, err := parseOrigin(strings.TrimSpace(entry))
		if err != nil {
			return nil, err
		}
		set[origin] = struct{}{}
	}
	return set, nil
}

func parseOrigin(entry string) (string, error) {
	origin, ok := serialisedOrigin(entry)
	if !ok {
		return "", fmt.Errorf("%q is not scheme://host[:port] as a browser sends it", entry)
	}
	return origin, nil
}

// defaultPort is the port a browser leaves out of an Origin of scheme.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// serialisedOrigin is entry lower-cased when it is an Origin a browser can
// send.
func serialisedOrigin(entry string) (string, bool) {
	if strings.Contains(entry, "*") || strings.HasSuffix(entry, ":") || !isASCII(entry) {
		return "", false
	}
	u, err := url.Parse(entry)
	if err != nil || u.Hostname() == "" || !browserPort(u.Scheme, u.Port()) {
		return "", false
	}
	// Whatever url.Parse put outside scheme and host makes the rebuilt
	// value differ from the entry.
	origin := u.Scheme + "://" + strings.ToLower(u.Host)
	return origin, origin == strings.ToLower(entry)
}

// browserPort is a port as a browser writes it in an Origin of scheme:
// absent, or a decimal in range with no leading zero that is not the
// scheme's default.
func browserPort(scheme, port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port && port != defaultPort(scheme)
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// mcpStack is the MCP endpoint as served: the Origin check, then the
// bearer check, then the SDK.
func mcpStack(server *sdk.Server, cfg httpConfig, logger *log.Logger) http.Handler {
	return requireOrigin(cfg.origins, logger, mcpHandler(server, cfg.clients))
}

// requireOrigin answers 403, with no body and no header of its own, a
// request whose Origin is not one value in allowed; a request without
// Origin reaches next. A preflight carries an Origin and is refused like
// any request, since no CORS is served.
func requireOrigin(allowed originSet, logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, sent := r.Header["Origin"]
		if !sent {
			next.ServeHTTP(w, r)
			return
		}
		if len(values) == 1 && allowed.allows(values[0]) && r.Method != http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		logger.Printf("forjad: origin refused remote=%s origin=%q", r.RemoteAddr, clip(strings.Join(values, ", "), maxLoggedOrigin))
		w.WriteHeader(http.StatusForbidden)
	})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
