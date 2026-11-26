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
//
// GET /_dev/push?repo=acme/website&branch=main&message=Hello answers with
// a signed push delivery of the App installed on the repository's account
// (the body, and the headers GitHub would send as X-Dev-<header>): send
// the body to the panel's /api/github/webhook with those headers, without
// the prefix (scripts/github-push.sh does it). The push names the
// branch's newest commit from POST /_dev/commit, if there is one.
//
// POST /_dev/commit?repo=acme/website&branch=main with a tar.gz of a folder
// as the body records the folder as a new commit on the branch (its files
// are what a build of the commit gets) and answers the commit's id:
//
//	tar -czf - -C my-site . | curl --data-binary @- 'http://127.0.0.1:9101/_dev/commit?repo=acme/website'
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
	srv.Dev = true
	for _, r := range []struct {
		owner, name string
		private     bool
		branches    []string
	}{
		{"octo-admin", "blog", false, []string{"drafts"}}, {"octo-admin", "notes", true, nil},
		{"acme", "website", true, []string{"release/2.0", "dev"}}, {"acme", "docs", false, nil},
		{"acme", "landing-page", true, nil},
	} {
		id := srv.AddRepo(r.owner, r.name, r.private)
		for _, b := range r.branches {
			srv.AddBranch(id, b)
		}
	}
	fmt.Println("fake GitHub at", srv.URL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
