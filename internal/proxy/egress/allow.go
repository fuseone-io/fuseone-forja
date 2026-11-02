package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNonPublicDestination is the dial refusal of an address the gateway
// must not reach on the workspace's behalf: loopback, private, link-local,
// shared or unspecified. On the node those are the control plane, the
// shared bridge and every other run's gateway.
var ErrNonPublicDestination = errors.New("egress: destination is not a public address")

// dialTimeout bounds the connection to an upstream.
const dialTimeout = 10 * time.Second

// ParseAllow reads a comma-separated list of host:port, each matched
// exactly and lowercased. An empty string is an empty list; any entry that
// is not a bare host or IP with a numeric port is an error naming it.
func ParseAllow(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	allow := make([]string, 0, len(parts))
	for _, part := range parts {
		entry := strings.ToLower(strings.TrimSpace(part))
		if err := validEntry(entry); err != nil {
			return nil, fmt.Errorf("egress allow entry %q: %w", part, err)
		}
		allow = append(allow, entry)
	}
	return allow, nil
}

func validEntry(entry string) error {
	host, port, err := net.SplitHostPort(entry)
	if err != nil {
		return err
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("port %q is not a number from 1 to 65535", port)
	}
	if !validHost(host) {
		return fmt.Errorf("host %q is not a hostname or an IP address", host)
	}
	if net.JoinHostPort(host, port) != entry {
		return errors.New("not in host:port form")
	}
	return nil
}

func validHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") {
		return false
	}
	return strings.TrimFunc(host, hostnameRune) == ""
}

// hostnameRune is a lowercase letter, a digit, a dot or a hyphen: no
// wildcard, no userinfo, no path.
func hostnameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
}

// DialPublic connects to addr once its name resolved to a public address,
// and refuses every other address with ErrNonPublicDestination. The check
// runs on the resolved address, so a listed name pointed at the bridge is
// refused as surely as an IP literal would be.
func DialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout, Control: refuseNonPublic}
	return d.DialContext(ctx, network, addr)
}

func refuseNonPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !public(ip.Unmap()) {
		return fmt.Errorf("%s: %w", ip, ErrNonPublicDestination)
	}
	return nil
}

// public excludes, besides what netip classifies, 100.64.0.0/10: shared
// carrier-grade NAT space, which netip does not count as private but is
// not the public internet either.
func public(ip netip.Addr) bool {
	shared := netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10)
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !shared.Contains(ip)
}
