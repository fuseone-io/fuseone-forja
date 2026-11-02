package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxRegistryBytes bounds a registry entry, and what an access line
// records of the registry and of the path.
const MaxRegistryBytes = 255

// MaxRegistryAccesses is how many accesses a report lists. The count goes
// on past it: the workspace decides how many requests it makes, not how
// much the control plane keeps.
const MaxRegistryAccesses = 100

// RegistryOutcome is whether the registry proxy forwarded a request.
type RegistryOutcome string

// The two outcomes of a request to the registry proxy.
const (
	RegistryAllowed RegistryOutcome = "allowed"
	RegistryDenied  RegistryOutcome = "denied"
)

// RegistryAccess is one request the workspace made to its registry proxy.
// Registry is the declared entry it matched, or on a denial the first
// segment it asked for; Path is what followed, without the query. Status
// is the HTTP status the workspace received.
type RegistryAccess struct {
	At       time.Time
	Registry string
	Path     string
	Outcome  RegistryOutcome
	Status   int
}

// RegistryReport is what a run's registry proxy served and refused: every
// request counted, the first MaxRegistryAccesses listed in order. The zero
// value is an empty report.
type RegistryReport struct {
	Accesses []RegistryAccess
	Count    int
}

// Record counts a, and lists it while the list has room.
func (r *RegistryReport) Record(a RegistryAccess) {
	r.Count++
	if len(r.Accesses) < MaxRegistryAccesses {
		r.Accesses = append(r.Accesses, a)
	}
}

// Truncated reports whether accesses were counted that the list leaves out.
func (r RegistryReport) Truncated() bool {
	return r.Count > len(r.Accesses)
}

// ValidRegistry checks entry against the grammar host[:port][/segment...]:
// lowercase, with no scheme, query, fragment, userinfo, wildcard, trailing
// slash, empty segment or dot segment. The error never repeats the entry,
// so a secret pasted in its place is not printed.
func ValidRegistry(entry string) error {
	if entry == "" || len(entry) > MaxRegistryBytes {
		return fmt.Errorf("registry entry must have 1 to %d bytes", MaxRegistryBytes)
	}
	for _, rule := range registryRules() {
		if rule.broken(entry) {
			return errors.New("registry entry " + rule.message)
		}
	}
	hostport, path, _ := strings.Cut(entry, "/")
	if err := validRegistryHost(hostport); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return validRegistrySegments(path)
}

type registryRule struct {
	broken  func(string) bool
	message string
}

// registryRules names the defects worth a message of their own; anything
// else is caught by the host and segment checks.
func registryRules() []registryRule {
	return []registryRule{
		{func(e string) bool { return strings.Contains(e, "://") }, "has a scheme: write host[:port][/path]"},
		{func(e string) bool { return strings.ContainsAny(e, "?#") }, "has a query or a fragment"},
		{func(e string) bool { return strings.Contains(e, "@") }, "has userinfo"},
		{func(e string) bool { return strings.Contains(e, "*") }, "has a wildcard"},
		{func(e string) bool { return strings.HasSuffix(e, "/") }, "ends in a slash"},
		{func(e string) bool { return strings.ToLower(e) != e }, "is not lowercase"},
	}
}

func validRegistryHost(hostport string) error {
	host, port, hasPort := strings.Cut(hostport, ":")
	if hasPort && !validPort(port) {
		return errors.New("registry entry port is not a number from 1 to 65535")
	}
	if !validHostname(host) {
		return errors.New("registry entry host is not a lowercase hostname or IPv4 address")
	}
	return nil
}

func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

func validHostname(host string) bool {
	return host != "" && len(host) <= 253 && !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "-") &&
		strings.TrimFunc(host, hostRune) == ""
}

func validRegistrySegments(path string) error {
	for _, seg := range strings.Split(path, "/") {
		switch {
		case seg == "":
			return errors.New("registry entry has an empty path segment")
		case seg == "." || seg == "..":
			return errors.New("registry entry has a dot path segment")
		case strings.TrimFunc(seg, segmentRune) != "":
			return errors.New("registry entry path segment has a character outside a-z 0-9 - . _ ~")
		}
	}
	return nil
}

func hostRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
}

func segmentRune(r rune) bool {
	return hostRune(r) || r == '_' || r == '~'
}

// MatchRegistry returns the longest of entries that path starts with on a
// segment boundary: path is the entry itself, or the entry followed by a
// slash. path carries no leading slash. Entries are expected valid.
func MatchRegistry(entries []string, path string) (string, bool) {
	best := ""
	for _, e := range entries {
		if (path == e || strings.HasPrefix(path, e+"/")) && len(e) > len(best) {
			best = e
		}
	}
	return best, best != ""
}
