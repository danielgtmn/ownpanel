package gitsource

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	allowPrivate = false
	defer func() { allowPrivate = true }()
	good := []struct{ in, out, server, path string }{
		{"git@codeberg.org:owner/site.git", "git@codeberg.org:owner/site.git", "codeberg.org", "owner/site.git"},
		{"git@GitHub.com:Owner/Site.git", "git@github.com:Owner/Site.git", "github.com", "Owner/Site.git"},
		{"git@git.sr.ht:~ada/site", "git@git.sr.ht:~ada/site", "git.sr.ht", "~ada/site"},
		{"ssh://git@gitlab.com/group/sub/site.git", "ssh://git@gitlab.com:22/group/sub/site.git", "gitlab.com", "/group/sub/site.git"},
		{"ssh://forgejo@git.example.org:2222/a/b.git", "ssh://forgejo@git.example.org:2222/a/b.git", "git.example.org:2222", "/a/b.git"},
		{"ssh://git.example.org/a/b", "ssh://git@git.example.org:22/a/b", "git.example.org", "/a/b"},
		{"https://codeberg.org/owner/site.git", "https://codeberg.org/owner/site.git", "codeberg.org", "/owner/site.git"},
		{"https://git.example.org:8443/owner/site", "https://git.example.org:8443/owner/site", "git.example.org:8443", "/owner/site"},
		{"https://1.1.1.1/a/b.git", "https://1.1.1.1/a/b.git", "1.1.1.1", "/a/b.git"},
		{"git@[2606:4700:4700::1111]:a/b.git", "git@[2606:4700:4700::1111]:a/b.git", "2606:4700:4700::1111", "a/b.git"},
	}
	for _, c := range good {
		a, err := ParseAddress(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if a.String() != c.out || a.ServerName() != c.server || a.Path != c.path {
			t.Errorf("%q: got %q server %q path %q", c.in, a.String(), a.ServerName(), a.Path)
		}
		// The canonical form reads back the same.
		if b, err := ParseAddress(a.String()); err != nil || b != a {
			t.Errorf("%q: canonical form %q reads back as %+v, %v", c.in, a.String(), b, err)
		}
	}

	hostile := []string{
		"",
		"/srv/git/site.git",
		"./site",
		"../site",
		"~/site.git",
		"site.git",
		"file:///etc/passwd",
		"file://host/srv/repo",
		"ext::sh -c touch% /tmp/pwned",
		"ext::ssh -oProxyCommand=evil x",
		"fd::17",
		"http://example.com/a/b.git",
		"git://example.com/a/b.git",
		"ftp://example.com/a/b.git",
		"-oProxyCommand=evil@host:a/b",
		"--upload-pack=touch /tmp/x",
		"git@-oProxyCommand=evil:a/b",
		"-git@example.com:a/b",
		"git@example.com:-a/b",
		"git@example.com:a/-b",
		"ssh://-oProxyCommand=evil/a/b",
		"ssh://git@-host.example.com/a/b",
		"ssh://git@example.com/-a",
		"ssh://git@example.com/a/../../etc",
		"ssh://git@example.com/a/./b",
		"ssh://git@example.com//a",
		"git@example.com:a//b",
		"git@example.com:a b",
		"git@example.com:a'b",
		"git@example.com:a;b",
		"git@example.com:a$(id)",
		"git@example.com:a`id`",
		"git@example.com:a\\b",
		"git@example.com:a%2e%2e/b",
		"git@example.com:a\nb",
		"git@example.com:a\x00b",
		"git@exämple.com:a/b",
		"https://user:pass@example.com/a/b.git",
		"https://token@example.com/a/b.git",
		"https://example.com/a/b.git?x=1",
		"https://example.com/a/b.git#frag",
		"https://example.com",
		"https://example.com:0/a",
		"https://example.com:65536/a",
		"https://example.com:abc/a",
		"ssh://git@example.com:/a",
		"git@localhost:a/b",
		"git@localhost.:a/b",
		"git@intranet:a/b",
		"git@git.local:a/b",
		"git@git.internal:a/b",
		"git@127.0.0.1:a/b",
		"git@10.0.0.1:a/b",
		"git@192.168.1.10:a/b",
		"git@172.16.5.4:a/b",
		"git@169.254.169.254:a/b",
		"git@100.64.0.1:a/b",
		"git@0.0.0.0:a/b",
		"git@[::1]:a/b",
		"git@[fd00::1]:a/b",
		"git@[fe80::1]:a/b",
		"git@[::ffff:127.0.0.1]:a/b",
		"git@[64:ff9b::a00:1]:a/b",
		"https://[::1]/a/b",
		"https://127.0.0.1/a/b",
		"https://2130706433/a/b",
		"https://0x7f.0.0.1/a/b",
		"https://127.1/a/b",
		"ssh://git@::1/a/b",
		"git@2001:db8::1:a/b",
		"git@exa_mple.com:" + strings.Repeat("a", 300),
		strings.Repeat("a", 401),
		"toolonguser" + strings.Repeat("x", 30) + "@example.com:a/b",
		"git@example.com:a/b*",
		"git@example.com:a/{b,c}",
		"git@example.com:a/b|c",
		"git@example.com:a/b&c",
		"git@example.com:a/b>c",
		"git@example.com:a/b\"c",
	}
	for _, in := range hostile {
		a, err := ParseAddress(in)
		if err == nil {
			t.Errorf("accepted %q as %+v", in, a)
			continue
		}
		var ae *AddressError
		if !errors.As(err, &ae) {
			t.Errorf("%q: not an AddressError: %v", in, err)
		}
	}
}
