package panel

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/dns"
)

// RecordTypes are the record types people can create and change here.
var RecordTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "CAA", "SRV", "NS"}

const (
	minTTL       = 60
	maxTTL       = 86400
	maxValues    = 50
	maxTXTLength = 4000
)

var (
	recordLabelRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)
	hostLabelRe   = recordLabelRe
	recordTypeRe  = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}$`)
	caaRe         = regexp.MustCompile(`^(\d{1,3})\s+([A-Za-z0-9]{1,15})\s+(.*)$`)
)

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// recordName normalizes a record name relative to zone: "" and "@" are the
// zone itself; a name ending in a dot is a full name and must lie inside the
// zone. Labels may contain underscores (_dmarc) and the first may be "*".
func recordName(zone, name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(n, ".") {
		full := strings.TrimSuffix(n, ".")
		switch {
		case full == zone:
			return "@", nil
		case strings.HasSuffix(full, "."+zone):
			n = strings.TrimSuffix(full, "."+zone)
		default:
			return "", errorf(Invalid, "%q is not inside the zone %s", name, zone)
		}
	}
	if n == "" || n == "@" {
		return "@", nil
	}
	if hasNonASCII(n) {
		return "", errorf(Invalid, "the name %q contains non-ASCII characters; use the punycode form (xn--...)", name)
	}
	if len(n)+1+len(zone) > 253 {
		return "", errorf(Invalid, "the name %q is too long", name)
	}
	for i, l := range strings.Split(n, ".") {
		if l == "*" && i == 0 {
			continue
		}
		if !recordLabelRe.MatchString(l) {
			return "", errorf(Invalid, "the name %q is not valid: use letters, digits, hyphens and underscores, separated by dots", name)
		}
	}
	return n, nil
}

// recordType normalizes a record type and refuses those that are managed
// by the provider (SOA, and NS of the zone itself).
func recordType(name, typ string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(typ))
	if !recordTypeRe.MatchString(t) {
		return "", errorf(Invalid, "%q is not a record type", typ)
	}
	if t == "SOA" {
		return "", errorf(Invalid, "the SOA record is managed by the DNS provider and cannot be changed here")
	}
	if t == "NS" && name == "@" {
		return "", errorf(Invalid, "the NS records of the zone itself are managed by the DNS provider and cannot be changed here")
	}
	return t, nil
}

// hostValue normalizes a host name used as a record value to its full form
// with a trailing dot. "." (no host) is accepted only if allowRoot.
func hostValue(v string, allowRoot bool) (string, error) {
	h := strings.ToLower(strings.TrimSpace(v))
	if h == "." && allowRoot {
		return ".", nil
	}
	h = strings.TrimSuffix(h, ".")
	if hasNonASCII(h) {
		return "", errorf(Invalid, "the host name %q contains non-ASCII characters; use the punycode form (xn--...)", v)
	}
	labels := strings.Split(h, ".")
	if h == "" || len(h) > 253 || len(labels) < 2 {
		return "", errorf(Invalid, "%q is not a full host name (like mail.example.com)", v)
	}
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return "", errorf(Invalid, "%q is not a valid host name", v)
		}
	}
	return h + ".", nil
}

func uint16Field(s, what, value string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0, errorf(Invalid, "the %s in %q must be a number from 0 to 65535", what, value)
	}
	return n, nil
}

// recordValue checks and normalizes one value of a record of type typ.
func recordValue(typ, v string) (string, error) {
	switch typ {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(strings.TrimSpace(v))
		if err != nil || ip.Zone() != "" {
			return "", errorf(Invalid, "%q is not an IP address", v)
		}
		if typ == "A" && !ip.Is4() {
			return "", errorf(Invalid, "%q is not an IPv4 address; IPv6 addresses go into AAAA records", v)
		}
		if typ == "AAAA" && (!ip.Is6() || ip.Is4In6()) {
			return "", errorf(Invalid, "%q is not an IPv6 address; IPv4 addresses go into A records", v)
		}
		return ip.String(), nil
	case "CNAME", "NS":
		return hostValue(v, false)
	case "MX":
		f := strings.Fields(v)
		if len(f) != 2 {
			return "", errorf(Invalid, "an MX value is a priority and a mail server, like \"10 mail.example.com\", not %q", v)
		}
		prio, err := uint16Field(f[0], "priority", v)
		if err != nil {
			return "", err
		}
		host, err := hostValue(f[1], true)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %s", prio, host), nil
	case "SRV":
		f := strings.Fields(v)
		if len(f) != 4 {
			return "", errorf(Invalid, "an SRV value is priority, weight, port and target, like \"10 5 5060 sip.example.com\", not %q", v)
		}
		var nums [3]int
		for i, what := range []string{"priority", "weight", "port"} {
			n, err := uint16Field(f[i], what, v)
			if err != nil {
				return "", err
			}
			nums[i] = n
		}
		host, err := hostValue(f[3], true)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d %d %s", nums[0], nums[1], nums[2], host), nil
	case "CAA":
		m := caaRe.FindStringSubmatch(strings.TrimSpace(v))
		if m == nil {
			return "", errorf(Invalid, "a CAA value is flags, tag and value, like '0 issue \"letsencrypt.org\"', not %q", v)
		}
		flags, err := strconv.Atoi(m[1])
		if err != nil || flags > 255 {
			return "", errorf(Invalid, "the flags in %q must be a number from 0 to 255", v)
		}
		val := strings.TrimSpace(m[3])
		if len(val) >= 2 && strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) {
			val = val[1 : len(val)-1]
		}
		if strings.Contains(val, `"`) {
			return "", errorf(Invalid, "the value in %q must not contain double quotes", v)
		}
		return fmt.Sprintf("%d %s %q", flags, strings.ToLower(m[2]), val), nil
	case "TXT":
		if len(v) > maxTXTLength {
			return "", errorf(Invalid, "a TXT value can be at most %d characters long", maxTXTLength)
		}
		if !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f }) {
			return "", errorf(Invalid, "a TXT value must be plain text without line breaks")
		}
		return v, nil
	}
	return "", errorf(Invalid, "record type %s cannot be changed here; use one of %s", typ, strings.Join(RecordTypes, ", "))
}

// validateRecordSet checks a record set to be written into zone and returns
// it normalized. ttl nil means the zone's default.
func validateRecordSet(zone, name, typ string, ttl *int, values []string) (dns.RecordSet, error) {
	n, err := recordName(zone, name)
	if err != nil {
		return dns.RecordSet{}, err
	}
	t, err := recordType(n, typ)
	if err != nil {
		return dns.RecordSet{}, err
	}
	if !slices.Contains(RecordTypes, t) {
		return dns.RecordSet{}, errorf(Invalid, "record type %s cannot be changed here; use one of %s", t, strings.Join(RecordTypes, ", "))
	}
	rs := dns.RecordSet{Name: n, Type: t}
	if ttl != nil {
		if *ttl < minTTL || *ttl > maxTTL {
			return dns.RecordSet{}, errorf(Invalid, "the TTL must be between %d and %d seconds", minTTL, maxTTL)
		}
		rs.TTL = *ttl
	}
	if len(values) == 0 {
		return dns.RecordSet{}, errorf(Invalid, "give at least one value")
	}
	for _, v := range values {
		clean, err := recordValue(t, v)
		if err != nil {
			return dns.RecordSet{}, err
		}
		if !slices.Contains(rs.Values, clean) {
			rs.Values = append(rs.Values, clean)
		}
	}
	if len(rs.Values) > maxValues {
		return dns.RecordSet{}, errorf(Invalid, "a record set can have at most %d values", maxValues)
	}
	if t == "CNAME" {
		if n == "@" {
			return dns.RecordSet{}, errorf(Invalid, "the zone itself (@) cannot have a CNAME record; use A and AAAA records")
		}
		if len(rs.Values) > 1 {
			return dns.RecordSet{}, errorf(Invalid, "a CNAME record can only have one value")
		}
	}
	return rs, nil
}

// recordDetail describes a record set for the activity log. TXT values
// longer than 60 characters (keys, verification codes) are left out.
func recordDetail(rs dns.RecordSet) string {
	vals := make([]string, len(rs.Values))
	for i, v := range rs.Values {
		if rs.Type == "TXT" && utf8.RuneCountInString(v) > 60 {
			v = fmt.Sprintf("(%d characters)", utf8.RuneCountInString(v))
		} else if rs.Type == "TXT" {
			v = strconv.Quote(v)
		}
		vals[i] = v
	}
	d := rs.Name + " " + rs.Type
	if len(vals) > 0 {
		d += ": " + strings.Join(vals, ", ")
	}
	if rs.TTL > 0 {
		d += fmt.Sprintf(" (TTL %d)", rs.TTL)
	}
	if utf8.RuneCountInString(d) > 500 {
		d = string([]rune(d)[:499]) + "…"
	}
	return d
}
