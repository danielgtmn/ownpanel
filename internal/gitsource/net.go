package gitsource

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/netcheck"
)

// allowPrivate, when set, lets connections go to loopback and private
// addresses. Only for tests and local development (AllowPrivateHosts).
var allowPrivate bool

// AllowPrivateHosts lets the package connect to loopback and private
// addresses. Only for development and tests: it removes the protection
// against repository addresses that point into the server's own network.
func AllowPrivateHosts() { allowPrivate = true }

func publicAddr(ip netip.Addr) bool {
	if allowPrivate {
		return ip.IsValid()
	}
	return netcheck.Public(ip.AsSlice())
}

// errNotPublic is returned when a name resolves to an address that is not
// public.
var errNotPublic = errors.New("the server's address is not public")

// dialTimeout bounds connecting to a server.
const dialTimeout = 15 * time.Second

// dialer connects only to public addresses: Control sees the address the
// connection really goes to, after the name was resolved, so a name that
// resolves to the server's own network is refused whatever it resolved to
// when the address was checked.
func dialer() *net.Dialer {
	return &net.Dialer{
		Timeout: dialTimeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return errNotPublic
			}
			if !publicAddr(ap.Addr().Unmap()) {
				return fmt.Errorf("%w (%s)", errNotPublic, ap.Addr())
			}
			return nil
		},
	}
}

// dial connects to host:port over TCP.
func dial(ctx context.Context, hostPort string) (net.Conn, error) {
	return dialer().DialContext(ctx, "tcp", hostPort)
}
