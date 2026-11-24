//go:build live

package dns

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Read-only check against real DNS. DNS_LIVE_HOSTS is a comma-separated list
// of names to look up, e.g. one that exists and one that does not:
//
//	DNS_LIVE_HOSTS=www.example.com,nope.example.com go test -tags live -run Live -v ./internal/dns
func TestAuthoritativeLive(t *testing.T) {
	hosts := strings.Split(os.Getenv("DNS_LIVE_HOSTS"), ",")
	if hosts[0] == "" {
		hosts = []string{"www.example.com", "does-not-exist-12345.example.com"}
	}
	a := NewAuthoritative(net.DefaultResolver)
	for _, host := range hosts {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		addrs, err := a.LookupNetIP(ctx, "ip", host)
		cancel()
		t.Logf("%s -> %v err=%v notfound=%v (%v)", host, addrs, err, isNotFound(err), time.Since(start).Round(time.Millisecond))
	}
}
