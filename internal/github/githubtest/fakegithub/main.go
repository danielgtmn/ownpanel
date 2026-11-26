// Command fakegithub runs the GitHub stand-in of package githubtest, to
// click through "Connect GitHub" in a development panel without a public
// address:
//
//	go run ./internal/github/githubtest/fakegithub -listen 127.0.0.1:9101
//	bin/ownpaneld -github-url http://127.0.0.1:9101 …
//
// Creating the App asks for one click, as on GitHub; installing confirms
// at once with all of the account's repositories chosen. Apps created
// for the person's own account belong to "octo-admin". It is not part of
// the panel's binary.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9101", "address to listen on (loopback only)")
	flag.Parse()
	srv, err := githubtest.NewAt(*listen)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	srv.AskToConfirm = true
	for _, r := range []struct {
		owner, name string
		private     bool
	}{
		{"octo-admin", "blog", false}, {"octo-admin", "notes", true},
		{"acme", "website", true}, {"acme", "docs", false}, {"acme", "landing-page", true},
	} {
		srv.AddRepo(r.owner, r.name, r.private)
	}
	fmt.Println("fake GitHub at", srv.URL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
