package caddy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientOverUnixSocket(t *testing.T) {
	// macOS limits socket paths to ~104 bytes; t.TempDir() can be longer.
	dir, err := os.MkdirTemp("", "gp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.Method + " " + r.URL.Path + " " + string(b)
		if strings.Contains(string(b), "bad") {
			http.Error(w, "nope", http.StatusBadRequest)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	admin, err := ParseAdmin("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(admin)
	if err := c.Load(context.Background(), map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != `POST /load {"a":"b"}` {
		t.Fatalf("server got %q", g)
	}
	if err := c.Load(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("rejection: %v", err)
	}
	<-got
}
