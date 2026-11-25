package timeouts

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServer(t *testing.T) {
	s := Server("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout != ReadHeader || s.ReadTimeout != Read || s.WriteTimeout != Write || s.IdleTimeout != Idle {
		t.Errorf("limits not set: %+v", s)
	}
	for name, d := range map[string]time.Duration{"ReadHeader": ReadHeader, "Read": Read, "Write": Write, "Idle": Idle} {
		if d <= 0 {
			t.Errorf("%s must be set", name)
		}
	}
	if Idle <= 2*time.Minute {
		t.Error("Idle must outlast Caddy's idle connections to the panel (2 minutes)")
	}
	if LongRead < Read || LongWrite <= LongRead {
		t.Error("the long limits must extend the normal ones")
	}
}

// slowPost sends a body in pieces, pausing between them, and returns the
// status or an error.
func slowPost(t *testing.T, addr string, pause time.Duration) (string, error) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	parts := []string{"aaaa", "bbbb", "cccc"}
	if _, err := io.WriteString(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 12\r\nConnection: close\r\n\r\n"); err != nil {
		return "", err
	}
	for _, p := range parts {
		time.Sleep(pause)
		if _, err := io.WriteString(c, p); err != nil {
			return "", err
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(c)
	return string(b), err
}

func TestLongOutlastsReadTimeout(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusRequestTimeout)
			return
		}
		_, _ = w.Write(b)
	})
	serve := func(h http.Handler) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := &http.Server{Handler: h, ReadTimeout: 150 * time.Millisecond, WriteTimeout: 150 * time.Millisecond}
		go func() { _ = s.Serve(l) }()
		t.Cleanup(func() { _ = s.Close() })
		return l.Addr().String()
	}

	// Without Long the body (sent over ~300 ms) runs into ReadTimeout.
	got, _ := slowPost(t, serve(echo), 100*time.Millisecond)
	if strings.Contains(got, "aaaabbbbcccc") {
		t.Fatalf("plain handler read the slow body: %q", got)
	}
	// With Long it arrives completely.
	got, err := slowPost(t, serve(Long(echo)), 100*time.Millisecond)
	if err != nil || !strings.HasPrefix(got, "HTTP/1.1 200") || !strings.HasSuffix(got, "aaaabbbbcccc") {
		t.Fatalf("long handler: %q, %v", got, err)
	}
}
