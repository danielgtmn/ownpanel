// Package gitsource fetches one commit of a Git repository on any host and
// hands it on as a tar.gz of its files (#111).
//
// Everything about it is untrusted: the address someone typed, the server
// it names, what that server answers and the files of the repository. So:
//
//   - Addresses are SSH (git@host:owner/repo.git, ssh://…) or HTTPS only,
//     checked strictly (ParseAddress): no local paths, no other transports,
//     nothing that could be read as an option, no credentials.
//   - Connections go only to public addresses: the address a name resolves
//     to is checked when the connection is made (netcheck.Public), so a name
//     that later resolves elsewhere gains nothing.
//   - SSH servers must show the host key that was pinned for the source;
//     another key refuses the fetch (HostKeyChangedError).
//   - The panel speaks the fetch side of Git's protocol itself (protocol
//     version 0, which every server supports): it asks for the one commit
//     with a depth of 1, reads the pack into a temporary file with limits on
//     bytes, objects and object sizes, and writes the commit's files into a
//     tar.gz with limits on their number, size and depth. No working tree is
//     ever written, no hooks, filters or submodules exist here, and no other
//     program is run.
//   - Links in the tree must stay inside it, also when followed through
//     other links; a tree with names Git itself would refuse fails.
package gitsource

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Schemes of an Address.
const (
	SchemeSSH   = "ssh"
	SchemeHTTPS = "https"
)

// MaxAddressLength bounds a repository address.
const MaxAddressLength = 400

// Address is a checked repository address.
type Address struct {
	Scheme string // SchemeSSH or SchemeHTTPS
	// User is the SSH user ("git" if none was given); empty for HTTPS.
	User string
	// Host is a lower-case DNS name or an IP address (without brackets).
	Host string
	Port int
	// Path is the repository's path on the host. For SSH in the short form
	// (user@host:path) it is relative, otherwise it starts with "/".
	Path string
	// scp: the address was written as user@host:path.
	scp bool
}

// HostPort is host:port for connecting.
func (a Address) HostPort() string {
	return netJoin(a.Host, a.Port)
}

func netJoin(host string, port int) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

// String is the address in its canonical form.
func (a Address) String() string {
	switch {
	case a.Scheme == SchemeHTTPS:
		host := a.Host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if a.Port != 443 {
			host += ":" + strconv.Itoa(a.Port)
		}
		return "https://" + host + a.Path
	case a.scp:
		host := a.Host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return a.User + "@" + host + ":" + a.Path
	default:
		return "ssh://" + a.User + "@" + a.HostPort() + a.Path
	}
}

// ServerName names the server for people and for pinning its key: the
// host, with the port when it is not the default.
func (a Address) ServerName() string {
	def := 22
	if a.Scheme == SchemeHTTPS {
		def = 443
	}
	if a.Port == def {
		return a.Host
	}
	return a.HostPort()
}

// AddressError says why an address was refused. Its text is meant for
// people.
type AddressError struct{ Msg string }

func (e *AddressError) Error() string { return e.Msg }

func refuse(format string, args ...any) error {
	return &AddressError{Msg: fmt.Sprintf(format, args...)}
}

const addressHelp = "use an SSH address like git@codeberg.org:owner/site.git or ssh://git@host:2222/owner/site.git, or an HTTPS address like https://codeberg.org/owner/site.git"

// ParseAddress checks a repository address. It accepts:
//
//	user@host:path          (SSH, the short form Git hosts show)
//	ssh://[user@]host[:port]/path
//	https://host[:port]/path
//
// and refuses everything else, including local paths, file:// and other
// transports, plain http, passwords or tokens in the address, queries and
// fragments, percent-encoding, and any part that starts with "-" (which a
// program could read as an option). Host names must be DNS names with a
// dot or IP addresses; addresses that are not public are refused (when
// connecting, every resolved address is checked again).
func ParseAddress(s string) (Address, error) {
	if s == "" {
		return Address{}, refuse("enter the repository's address: %s", addressHelp)
	}
	if len(s) > MaxAddressLength {
		return Address{}, refuse("the address is longer than %d characters", MaxAddressLength)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r > 0x7e {
			return Address{}, refuse("the address may not contain spaces, control characters or characters outside ASCII")
		}
	}
	if strings.ContainsAny(s, "\\\"'`$;|&<>(){}%?#*!,") {
		return Address{}, refuse("the address contains a character that is not allowed in it (%s)", addressHelp)
	}
	if strings.HasPrefix(s, "-") {
		return Address{}, refuse("the address may not start with \"-\"")
	}
	var a Address
	var rest string
	switch {
	case strings.HasPrefix(s, "ssh://"):
		a.Scheme = SchemeSSH
		rest = s[len("ssh://"):]
	case strings.HasPrefix(s, "https://"):
		a.Scheme = SchemeHTTPS
		rest = s[len("https://"):]
	case strings.Contains(s, "://"):
		return Address{}, refuse("only SSH and HTTPS addresses are allowed: %s", addressHelp)
	case strings.Contains(s, "::") && !strings.Contains(s[:strings.Index(s, "::")], "["):
		// <transport>::<address> runs a helper program in Git.
		return Address{}, refuse("only SSH and HTTPS addresses are allowed: %s", addressHelp)
	default:
		a.Scheme = SchemeSSH
		a.scp = true
	}

	if a.scp {
		return parseSCP(s)
	}

	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return Address{}, refuse("the address has no path to the repository: %s", addressHelp)
	}
	authority, path := rest[:slash], rest[slash:]
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if a.Scheme == SchemeHTTPS {
			return Address{}, refuse("HTTPS addresses may not contain a user name, password or token: the panel only fetches public repositories over HTTPS; use an SSH address with a deploy key for private ones")
		}
		user := authority[:at]
		if err := checkUser(user); err != nil {
			return Address{}, err
		}
		a.User = user
		authority = authority[at+1:]
	} else if a.Scheme == SchemeSSH {
		a.User = "git"
	}
	host, port, err := splitHostPort(authority)
	if err != nil {
		return Address{}, err
	}
	a.Host = host
	a.Port = port
	if a.Port == 0 {
		a.Port = 22
		if a.Scheme == SchemeHTTPS {
			a.Port = 443
		}
	}
	if err := checkPath(path, true); err != nil {
		return Address{}, err
	}
	a.Path = path
	return a, nil
}

func parseSCP(s string) (Address, error) {
	a := Address{Scheme: SchemeSSH, scp: true, Port: 22}
	at := strings.IndexByte(s, '@')
	if at < 0 {
		// Without user@ this would be a local path (or host:path, which Git
		// also reads as SSH, but which is too close to a path to accept).
		return Address{}, refuse("this is not a repository address the panel can use: %s", addressHelp)
	}
	user := s[:at]
	if err := checkUser(user); err != nil {
		return Address{}, err
	}
	a.User = user
	rest := s[at+1:]
	var host, path string
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 || end+1 >= len(rest) || rest[end+1] != ':' {
			return Address{}, refuse("the address is not written correctly: %s", addressHelp)
		}
		host, path = rest[1:end], rest[end+2:]
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() {
			return Address{}, refuse("%q is not an IPv6 address", host)
		}
		if err := checkIP(ip); err != nil {
			return Address{}, err
		}
		host = ip.String()
	} else {
		colon := strings.IndexByte(rest, ':')
		if colon < 0 {
			return Address{}, refuse("the address has no path to the repository: %s", addressHelp)
		}
		var err error
		host, path = rest[:colon], rest[colon+1:]
		if host, err = checkHost(host); err != nil {
			return Address{}, err
		}
	}
	a.Host = host
	if strings.HasPrefix(path, "/") {
		if err := checkPath(path, true); err != nil {
			return Address{}, err
		}
	} else if err := checkPath(path, false); err != nil {
		return Address{}, err
	}
	a.Path = path
	return a, nil
}

func checkUser(u string) error {
	if u == "" || len(u) > 32 {
		return refuse("the user name before \"@\" must be 1 to 32 characters (usually git)")
	}
	if u[0] == '-' {
		return refuse("the user name may not start with \"-\"")
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return refuse("the user name may only contain letters, digits, \".\", \"_\" and \"-\"")
		}
	}
	return nil
}

func splitHostPort(authority string) (string, int, error) {
	host, portText := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", 0, refuse("the address is not written correctly: %s", addressHelp)
		}
		host = authority[1:end]
		after := authority[end+1:]
		if after != "" {
			if !strings.HasPrefix(after, ":") {
				return "", 0, refuse("the address is not written correctly: %s", addressHelp)
			}
			portText = after[1:]
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() {
			return "", 0, refuse("%q is not an IPv6 address", host)
		}
		if err := checkIP(ip); err != nil {
			return "", 0, err
		}
		host = ip.String()
	} else {
		if i := strings.LastIndexByte(authority, ':'); i >= 0 {
			host, portText = authority[:i], authority[i+1:]
		}
		var err error
		if host, err = checkHost(host); err != nil {
			return "", 0, err
		}
	}
	port := 0
	if portText != "" || strings.HasSuffix(authority, ":") {
		p, err := strconv.Atoi(portText)
		if err != nil || p < 1 || p > 65535 || portText[0] == '0' || portText[0] == '+' {
			return "", 0, refuse("the port must be a number from 1 to 65535")
		}
		port = p
	}
	return host, port, nil
}

// checkHost checks a DNS name or an IPv4 address and returns it in lower
// case.
func checkHost(h string) (string, error) {
	if h == "" {
		return "", refuse("the address has no host: %s", addressHelp)
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		if ip.Is6() {
			return "", refuse("write IPv6 addresses in brackets, like [2001:db8::1]")
		}
		if err := checkIP(ip); err != nil {
			return "", err
		}
		return ip.String(), nil
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if len(h) > 253 {
		return "", refuse("the host name is too long")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", refuse("%q is not a host name on the Internet: use the server's full name, like git.example.com", h)
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", refuse("%q is not a valid host name", h)
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", refuse("%q is not a valid host name", h)
			}
		}
	}
	last := labels[len(labels)-1]
	if allDigits(last) {
		return "", refuse("%q is not a valid host name or IPv4 address", h)
	}
	switch last {
	case "localhost", "local", "internal", "localdomain", "home", "lan", "arpa", "invalid", "test", "example", "onion":
		return "", refuse("%q is not a host on the Internet; the panel only fetches from public servers", h)
	}
	return h, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func checkIP(ip netip.Addr) error {
	if !publicAddr(ip) {
		return refuse("%s is not a public address; the panel only fetches from public servers", ip)
	}
	return nil
}

// checkPath checks the path of the repository on its host. Segments may
// contain letters, digits, ".", "_", "-" and "~" (only at the start of the
// first one, for hosts like ~user/repo), never start with "-", and never
// be "." or "..".
func checkPath(p string, absolute bool) error {
	if absolute {
		if !strings.HasPrefix(p, "/") {
			return refuse("the path must start with \"/\"")
		}
		p = p[1:]
	}
	if p == "" {
		return refuse("the address has no path to the repository: %s", addressHelp)
	}
	if len(p) > 256 {
		return refuse("the path of the repository is too long")
	}
	p = strings.TrimSuffix(p, "/")
	for i, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return refuse("the path of the repository may not contain empty parts, \".\" or \"..\"")
		}
		if seg[0] == '-' {
			return refuse("no part of the path may start with \"-\"")
		}
		for j := 0; j < len(seg); j++ {
			c := seg[j]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			case c == '~' && i == 0 && j == 0:
			default:
				return refuse("the path of the repository may only contain letters, digits, \".\", \"_\", \"-\" and \"/\"")
			}
		}
	}
	return nil
}
