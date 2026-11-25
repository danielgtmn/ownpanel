package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// DNS records of a mail domain (wiki/E-Mail-Architecture.md, "DNS
// records"): the panel knows which records a mail domain needs, checks them
// at the authoritative nameservers and, when the domain's zone is in one of
// the actor's own DNS connections, writes them through the provider:
//
//	MX    @                      10 <mail_host>.        replaced only with confirm
//	TXT   @ (SPF)                v=spf1 mx -all         mx inserted into an existing SPF record
//	TXT   <selector>._domainkey  v=DKIM1; k=rsa; p=…    replaced (the selector is ours)
//	TXT   _dmarc                 v=DMARC1; p=none; …    never changed
//	SRV   _submissions._tcp      0 1 465 <mail_host>.   replaced
//	SRV   _imaps._tcp            0 1 993 <mail_host>.   replaced
//	CNAME autoconfig             <mail_host>.           optional; replaced only if a CNAME
//	CNAME autodiscover           <mail_host>.           optional; replaced only if a CNAME
//
// Other TXT values at @ are always kept. An SPF record that cannot be
// merged safely (several SPF records, +all/?all, more than 10 lookups) is
// reported, never changed. The two optional names let mail programs set
// themselves up (mailsetup.go); A/AAAA records there that point to this
// server are just as good as the CNAME, other records are left alone.
//
// The mail host itself needs one record too (admins, mail health):
//
//	TXT   <mail_host> (SPF)      v=spf1 a -all          a inserted into an existing SPF record
//
// because bounces (null sender: SPF checks the HELO name) and mail
// forwarded with an SRS address of the mail host are sent under its name.

// Kinds of mail records.
const (
	MailRecordMX             = "mx"
	MailRecordSPF            = "spf"
	MailRecordDKIM           = "dkim"
	MailRecordDMARC          = "dmarc"
	MailRecordSRVSubmissions = "srv-submissions"
	MailRecordSRVIMAPS       = "srv-imaps"
	MailRecordAutoconfig     = "autoconfig"
	MailRecordAutodiscover   = "autodiscover"
	// MailRecordHostSPF is the SPF record of the mail host itself.
	MailRecordHostSPF = "mail-host-spf"
)

// States of a mail record.
const (
	RecordOK        = "ok"
	RecordMissing   = "missing"
	RecordDifferent = "different"
	RecordConflict  = "conflict"
	RecordUnknown   = "unknown"
)

// Actions of a planned change.
const (
	ChangeCreate  = "create"
	ChangeReplace = "replace"
	ChangeAdd     = "add"
	ChangeKeep    = "keep"
	ChangeSkip    = "skip"
)

const (
	defaultSPF = "v=spf1 mx -all"
	// defaultHostSPF allows the mail host's own addresses.
	defaultHostSPF = "v=spf1 a -all"
	defaultDMARC   = "v=DMARC1; p=none; adkim=r; aspf=r"
	// maxSPFLookups is RFC 7208's limit of mechanisms that need a lookup.
	maxSPFLookups = 10
	// maxTXTString is the longest single string of a TXT record; longer
	// values are split, which some providers need done by hand.
	maxTXTString = 255
)

// MailRecord is one record a mail domain needs, with what the
// authoritative nameservers answer for it.
type MailRecord struct {
	Kind string `json:"kind"`
	Type string `json:"type"`
	// Name is relative to Zone when the records can be applied, else
	// relative to the mail domain ("@" is the name itself).
	Name  string   `json:"name"`
	FQDN  string   `json:"fqdn"`
	Want  string   `json:"want"`
	Found []string `json:"found"`
	// Status is ok, missing, different, conflict or unknown (the lookup
	// failed).
	Status string `json:"status"`
	Note   string `json:"note"`
	// Optional records are recommended, not needed for mail to work.
	Optional bool `json:"optional"`
}

// MailRecords lists the records of a mail domain. CanApply tells whether
// SetUpMailRecords can write them for this actor: the domain lies in a
// zone of exactly one of the actor's own DNS connections with write access.
type MailRecords struct {
	Domain       string       `json:"domain"`
	MailHost     string       `json:"mail_host"`
	Records      []MailRecord `json:"records"`
	CanApply     bool         `json:"can_apply"`
	ConnectionID int64        `json:"connection_id"`
	Zone         string       `json:"zone"`
	// ApplyNote says why the records cannot be applied here; empty when
	// they can.
	ApplyNote string `json:"apply_note"`
}

// MailRecordChange is one planned (or made) change of SetUpMailRecords.
type MailRecordChange struct {
	Kind string `json:"kind"`
	Type string `json:"type"`
	// Name is relative to the zone.
	Name   string   `json:"name"`
	Action string   `json:"action"`
	Old    []string `json:"old"`
	New    []string `json:"new"`
	// NeedsConfirm: the change is only made with confirm (replacing MX
	// records that point elsewhere moves the domain's mail here).
	NeedsConfirm bool   `json:"needs_confirm"`
	Note         string `json:"note"`
	Optional     bool   `json:"optional"`
}

// MailRecordChanges is the result of SetUpMailRecords. Applied is false
// only when a change needs confirmation that was not given; nothing was
// written then.
type MailRecordChanges struct {
	Changes []MailRecordChange `json:"changes"`
	Applied bool               `json:"applied"`
}

// mailRecordSpec is one record a mail domain needs.
type mailRecordSpec struct {
	kind string
	typ  string
	sub  string // name relative to the mail domain
	want string // the value to publish (for SPF: when there is none yet)
	// optional records are recommended only (the names for setting up
	// mail programs).
	optional bool
}

func (s *Service) mailRecordSpecs(ctx context.Context, domain, host string) ([]mailRecordSpec, error) {
	specs := []mailRecordSpec{
		{kind: MailRecordMX, typ: "MX", sub: "@", want: "10 " + host + "."},
		{kind: MailRecordSPF, typ: "TXT", sub: "@", want: defaultSPF},
	}
	keys, err := s.store.DKIMKeys(ctx, domain)
	if err != nil {
		return nil, storeError(err)
	}
	for _, k := range keys {
		if k.State == store.DKIMActive {
			specs = append(specs, mailRecordSpec{kind: MailRecordDKIM, typ: "TXT", sub: k.Selector + "._domainkey", want: "v=DKIM1; k=rsa; p=" + k.PublicKey})
			break
		}
	}
	return append(specs,
		mailRecordSpec{kind: MailRecordDMARC, typ: "TXT", sub: "_dmarc", want: defaultDMARC},
		mailRecordSpec{kind: MailRecordSRVSubmissions, typ: "SRV", sub: "_submissions._tcp", want: "0 1 465 " + host + "."},
		mailRecordSpec{kind: MailRecordSRVIMAPS, typ: "SRV", sub: "_imaps._tcp", want: "0 1 993 " + host + "."},
		mailRecordSpec{kind: MailRecordAutoconfig, typ: "CNAME", sub: "autoconfig", want: host + ".", optional: true},
		mailRecordSpec{kind: MailRecordAutodiscover, typ: "CNAME", sub: "autodiscover", want: host + ".", optional: true},
	), nil
}

// hostSPFSpec is the SPF record of the mail host (relative to the mail
// host itself).
var hostSPFSpec = mailRecordSpec{kind: MailRecordHostSPF, typ: "TXT", sub: "@", want: defaultHostSPF}

// isSPFKind: the record is an SPF record, merged into an existing one.
func isSPFKind(kind string) bool { return kind == MailRecordSPF || kind == MailRecordHostSPF }

// subName returns the full name of a name relative to the mail domain.
func subName(sub, domain string) string {
	if sub == "@" {
		return domain
	}
	return sub + "." + domain
}

// zoneRelative returns a full name relative to zone.
func zoneRelative(full, zone string) string {
	if full == zone {
		return "@"
	}
	return strings.TrimSuffix(full, "."+zone)
}

// ---- judging records ----

// normalValues normalizes record values for comparison like they would be
// written; values that do not parse stay as they are.
func normalValues(typ string, values []string) []string {
	out := []string{}
	for _, v := range values {
		if typ != "TXT" {
			if n, err := recordValue(typ, v); err == nil {
				v = n
			}
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func isSPF(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	return l == "v=spf1" || strings.HasPrefix(l, "v=spf1 ")
}

func isDMARC(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(l, "v=dmarc1") && (len(l) == 8 || l[8] == ';' || l[8] == ' ')
}

// spfMerge is the outcome of mergeSPF.
type spfMerge struct {
	value    string // the record to publish
	ok       bool   // the record already allows the domain's MX hosts
	conflict string // why the record is not changed automatically
}

// mergeSPF inserts mech ("mx" for a mail domain, "a" for the mail host)
// after "v=spf1" of an existing SPF record, keeping everything else
// (qualifiers, includes, redirect=). A record that allows every server
// (+all, ?all), excludes what mech allows or would need more than 10 DNS
// lookups is a conflict: it is left to a person.
func mergeSPF(existing, mech string) spfMerge {
	terms := strings.Fields(existing)
	if len(terms) == 0 || !strings.EqualFold(terms[0], "v=spf1") {
		return spfMerge{value: existing, conflict: "The SPF record does not start with v=spf1; fix it by hand."}
	}
	rest := terms[1:]
	lookups := 0
	has := false
	for _, t := range rest {
		l := strings.ToLower(t)
		q := byte('+')
		if l != "" && strings.ContainsRune("+-~?", rune(l[0])) {
			q, l = l[0], l[1:]
		}
		name := l
		if i := strings.IndexAny(name, ":/="); i >= 0 {
			name = name[:i]
		}
		eq := strings.Index(l, "=")
		isModifier := eq > 0 && !strings.ContainsAny(l[:eq], ":/")
		switch {
		case isModifier && name == "redirect":
			lookups++
		case isModifier:
		case name == "all" && q == '+':
			return spfMerge{value: existing, conflict: "The SPF record allows every server on the internet to send for this domain (+all). Replace it by hand with one that ends in -all or ~all."}
		case name == "all" && q == '?':
			return spfMerge{value: existing, conflict: "The SPF record makes no statement about other servers (?all), so it protects nothing. Change it by hand to end in -all or ~all."}
		case name == mech:
			lookups++
			bare := l == mech || strings.HasPrefix(l, mech+"/")
			if bare && q == '+' {
				has = true
			} else if bare {
				who := "the domain's own mail servers"
				if mech == "a" {
					who = "the mail server itself"
				}
				return spfMerge{value: existing, conflict: "The SPF record marks " + who + " as not allowed (" + t + "). Change it by hand."}
			}
		case name == "include" || name == "a" || name == "mx" || name == "ptr" || name == "exists":
			lookups++
		}
	}
	if has {
		return spfMerge{value: existing, ok: true}
	}
	if lookups+1 > maxSPFLookups {
		return spfMerge{value: existing, conflict: fmt.Sprintf("With %s added, the SPF record would need %d DNS lookups; receivers allow at most %d. Shorten it by hand (fewer include: entries) and add %s.", mech, lookups+1, maxSPFLookups, mech)}
	}
	return spfMerge{value: strings.Join(append([]string{"v=spf1", mech}, rest...), " ")}
}

func dkimEqual(a, b string) bool {
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, strings.TrimSuffix(strings.TrimSpace(s), ";"))
	}
	return strip(a) == strip(b)
}

// srvPointsHere: every value is an SRV record for host and port.
func srvPointsHere(values []string, host string, port string) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		f := strings.Fields(v)
		if len(f) != 4 || f[2] != port || strings.ToLower(strings.TrimSuffix(f[3], ".")) != host {
			return false
		}
	}
	return true
}

// judgement is what a record's values mean for the mail domain.
type judgement struct {
	status string
	want   string
	note   string
	// values is the full record set to write for a change (only for
	// missing and different), others are the values that stay (TXT).
	values []string
	// keepOthers: other values of the record set are kept (TXT at @ and
	// _dmarc).
	keepOthers bool
}

// judge looks at the values found for a record (all values of the record
// set, normalized) and tells its status and the values to publish.
func judge(spec mailRecordSpec, host string, found []string) judgement {
	j := judgement{want: spec.want}
	switch spec.kind {
	case MailRecordMX:
		var targets []string
		for _, v := range found {
			f := strings.Fields(v)
			if len(f) != 2 {
				continue
			}
			h := strings.ToLower(strings.TrimSuffix(f[1], "."))
			if h == host {
				j.status = RecordOK
				return j
			}
			if h == "" {
				j.status, j.note = RecordDifferent, "The domain's MX record says it receives no e-mail (null MX). Replacing it moves the domain's mail to this server."
				j.values = []string{spec.want}
				return j
			}
			targets = append(targets, h)
		}
		if len(found) == 0 {
			j.status, j.note = RecordMissing, "The domain has no MX record yet; without it the domain receives no mail here."
		} else {
			j.status, j.note = RecordDifferent, fmt.Sprintf("Mail for this domain goes to %s now. Replacing the MX records moves the domain's mail to this server.", strings.Join(targets, ", "))
		}
		j.values = []string{spec.want}
	case MailRecordSPF, MailRecordHostSPF:
		mech, missing := "mx", "There is no SPF record yet; receivers cannot tell that this server may send for the domain."
		if spec.kind == MailRecordHostSPF {
			mech, missing = "a", "There is no SPF record for the mail server's name yet; receivers cannot tell that bounces and forwarded mail from this server are genuine."
		}
		var spfs []string
		for _, v := range found {
			if isSPF(v) {
				spfs = append(spfs, v)
			}
		}
		j.keepOthers = true
		switch len(spfs) {
		case 0:
			j.status, j.note = RecordMissing, missing
			j.values = []string{spec.want}
		case 1:
			m := mergeSPF(spfs[0], mech)
			j.want = m.value
			switch {
			case m.conflict != "":
				j.status, j.note = RecordConflict, m.conflict
			case m.ok:
				j.status = RecordOK
			default:
				j.status, j.note = RecordDifferent, "The SPF record does not allow this server yet. "+mech+" is added after v=spf1; everything else in it stays as it is."
				j.values = []string{m.value}
			}
		default:
			j.want = ""
			j.status, j.note = RecordConflict, "There are several SPF records; receivers then treat SPF as broken. Merge them into one by hand and include "+mech+"."
		}
	case MailRecordDKIM:
		switch {
		case len(found) == 1 && dkimEqual(found[0], spec.want):
			j.status = RecordOK
		case len(found) == 0:
			j.status, j.note = RecordMissing, "The DKIM key is not published yet; receivers cannot verify the signature of sent mail."
		case len(found) > 1:
			j.status, j.note = RecordDifferent, "There are several TXT records at this name; only the domain's DKIM key belongs there."
		default:
			j.status, j.note = RecordDifferent, "A different DKIM key is published at this name; it is replaced by the domain's key."
		}
		if j.status != RecordOK {
			j.values = []string{spec.want}
		}
	case MailRecordDMARC:
		var dmarcs []string
		for _, v := range found {
			if isDMARC(v) {
				dmarcs = append(dmarcs, v)
			}
		}
		j.keepOthers = true
		switch len(dmarcs) {
		case 0:
			j.status, j.note = RecordMissing, "There is no DMARC policy yet. The suggested one only asks receivers to report and changes nothing about delivery."
			j.values = []string{spec.want}
		case 1:
			j.want = dmarcs[0]
			j.status = RecordOK
			if !strings.EqualFold(strings.TrimSpace(dmarcs[0]), defaultDMARC) {
				j.note = "A DMARC policy is already published; it is left as it is."
			}
		default:
			j.want = ""
			j.status, j.note = RecordConflict, "There are several DMARC records; receivers then ignore all of them. Keep only one by hand."
		}
	case MailRecordSRVSubmissions, MailRecordSRVIMAPS:
		port := "465"
		if spec.kind == MailRecordSRVIMAPS {
			port = "993"
		}
		switch {
		case srvPointsHere(found, host, port):
			j.status = RecordOK
		case len(found) == 0:
			j.status, j.note = RecordMissing, "Mail programs use this record to find the server by themselves."
		default:
			j.status, j.note = RecordDifferent, "This record points mail programs to another server; it is replaced."
		}
		if j.status != RecordOK {
			j.values = []string{spec.want}
		}
	}
	return j
}

// ---- looking records up ----

// noMailLookups fails every TXT and SRV lookup; used when the resolver
// cannot do them (tests), so nothing goes to the network.
type noMailLookups struct{}

func (noMailLookups) LookupTXT(context.Context, string) ([]string, error) {
	return nil, errors.New("no TXT resolver configured")
}

func (noMailLookups) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, errors.New("no SRV resolver configured")
}

type txtSRVResolver interface {
	dns.TXTResolver
	dns.SRVResolver
}

func (s *Service) txtSRV() txtSRVResolver {
	if r, ok := s.resolver.(txtSRVResolver); ok {
		return r
	}
	return noMailLookups{}
}

// lookupRecord asks the authoritative nameservers for the values of one
// record; a name without such records gives no values and no error.
func (s *Service) lookupRecord(ctx context.Context, spec mailRecordSpec, domain string) ([]string, error) {
	full := subName(spec.sub, domain)
	var out []string
	var err error
	switch spec.typ {
	case "MX":
		var mx []*net.MX
		mx, err = s.mx.LookupMX(ctx, full)
		for _, m := range mx {
			out = append(out, fmt.Sprintf("%d %s", m.Pref, strings.ToLower(strings.TrimSuffix(m.Host, "."))+"."))
		}
	case "TXT":
		out, err = s.txtSRV().LookupTXT(ctx, full)
	case "SRV":
		var srv []*net.SRV
		_, srv, err = s.txtSRV().LookupSRV(ctx, "", "", full)
		for _, r := range srv {
			out = append(out, fmt.Sprintf("%d %d %d %s", r.Priority, r.Weight, r.Port, strings.ToLower(strings.TrimSuffix(r.Target, "."))+"."))
		}
	}
	var de *net.DNSError
	if errors.As(err, &de) && de.IsNotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return normalValues(spec.typ, out), nil
}

// mailZone finds the zone of the actor's own connections that holds the
// mail domain, and whether the actor could write records into it; if not,
// why.
func (s *Service) mailZone(ctx context.Context, a Actor, domain string) (zoneMatch, string, error) {
	conns, err := s.ownConnections(ctx, a)
	if err != nil {
		return zoneMatch{}, "", err
	}
	matches, problems := s.zoneMatches(ctx, conns, domain)
	switch {
	case len(matches) == 0 && len(problems) > 0:
		if ctx.Err() != nil {
			return zoneMatch{}, "", newError(Internal, ctx.Err())
		}
		msg := problems[0].Error()
		if KindOf(problems[0]) == Internal {
			msg = "a DNS connection could not be used"
		}
		return zoneMatch{}, "None of your DNS connections could be asked for the domain's zone (" + msg + "). Create the records at your DNS provider as listed.", nil
	case len(matches) == 0:
		return zoneMatch{}, "The domain's zone is not in one of your DNS connections. Create the records at your DNS provider as listed.", nil
	case len(matches) > 1:
		return zoneMatch{}, fmt.Sprintf("The zone %s is in more than one of your DNS connections; remove the duplicate connection so it is clear which one to change.", matches[0].zone.Name), nil
	}
	m := matches[0]
	switch {
	case m.zone.ReadOnly:
		return m, fmt.Sprintf("The zone %s is not a primary zone at %s, so its records cannot be changed here.", m.zone.Name, providerLabel(m.conn.Provider)), nil
	case m.conn.CanWrite != nil && !*m.conn.CanWrite:
		return m, fmt.Sprintf("The token of the DNS connection %q may only read; connect it again with a token that may change records.", m.conn.Name), nil
	}
	return m, "", nil
}

func (s *Service) mailHostOrConflict() (string, error) {
	host := s.MailHost()
	if host == "" {
		return "", errorf(Conflict, "set the mail server name in Settings first")
	}
	return host, nil
}

// MailRecords lists the DNS records the mail domain needs and how they
// stand at the authoritative nameservers.
func (s *Service) MailRecords(ctx context.Context, a Actor, domain string) (MailRecords, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailRecords{}, err
	}
	host, err := s.mailHostOrConflict()
	if err != nil {
		return MailRecords{}, err
	}
	specs, err := s.mailRecordSpecs(ctx, md.Domain, host)
	if err != nil {
		return MailRecords{}, err
	}
	type looked struct {
		found []string
		err   error
		check dns.Check // for the names that set up mail programs
	}
	res := make([]looked, len(specs))
	ips := s.effectiveServerIPs()
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spec.typ == "CNAME" {
				if len(ips) > 0 {
					res[i].check = dns.CheckDomain(lctx, s.resolver, subName(spec.sub, md.Domain), ips)
				}
				return
			}
			f, err := s.lookupRecord(lctx, spec, md.Domain)
			res[i] = looked{found: f, err: err}
		}()
	}

	out := MailRecords{Domain: md.Domain, MailHost: host, Records: []MailRecord{}}
	switch {
	case needMailWrite(a) != nil || needDNS(a) != nil:
		out.ApplyNote = "Your access does not allow changing DNS records; create them at your DNS provider as listed or sign in with a password."
	default:
		m, note, err := s.mailZone(ctx, a, md.Domain)
		if err != nil {
			wg.Wait()
			return MailRecords{}, err
		}
		out.ApplyNote = note
		if note == "" {
			out.CanApply, out.ConnectionID, out.Zone = true, m.conn.ID, m.zone.Name
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return MailRecords{}, newError(Internal, ctx.Err())
	}

	setupChanged := false
	for i, spec := range specs {
		full := subName(spec.sub, md.Domain)
		r := MailRecord{Kind: spec.kind, Type: spec.typ, Name: spec.sub, FQDN: full, Want: spec.want, Found: []string{}, Optional: spec.optional}
		if out.CanApply {
			r.Name = zoneRelative(full, out.Zone)
		}
		if spec.typ == "CNAME" {
			c := res[i].check
			r.Status, r.Note = judgeSetupName(c, len(ips) > 0)
			r.Found = nonNil(c.Found)
			if md.Active && len(ips) > 0 && s.setupHosts.note(full, c.State) {
				setupChanged = true
			}
			out.Records = append(out.Records, r)
			continue
		}
		if res[i].err != nil {
			r.Status, r.Note = RecordUnknown, "The record could not be looked up at the domain's nameservers; try again in a moment."
		} else {
			r.Found = res[i].found
			j := judge(spec, host, r.Found)
			r.Status, r.Want, r.Note = j.status, j.want, j.note
		}
		if spec.typ == "TXT" && len(r.Want) > maxTXTString && !out.CanApply && r.Status != RecordOK {
			r.Note = strings.TrimSpace(r.Note + fmt.Sprintf(" The value is longer than %d characters; if your DNS provider asks for it, split it into several quoted parts of at most %d characters each, written one after the other.", maxTXTString, maxTXTString))
		}
		out.Records = append(out.Records, r)
	}
	if setupChanged {
		// A name for setting up mail programs points here now (or no
		// more): the web server serves it from now on (or stops).
		_ = s.syncSaved(ctx) // logged; the background loop retries
	}
	return out, nil
}

// judgeSetupName judges an optional name for setting up mail programs by
// where it leads: a CNAME to the mail host and A/AAAA records with the
// server's addresses both end at this server.
func judgeSetupName(c dns.Check, haveIPs bool) (status, note string) {
	switch {
	case !haveIPs:
		return RecordUnknown, "The server's addresses are not set in the settings, so it cannot be checked where this name points."
	case c.State == dns.PointsHere:
		return RecordOK, ""
	case c.State == dns.NoRecord:
		return RecordMissing, "Recommended: mail programs such as Thunderbird and Outlook look up this name to set themselves up with just the address and password."
	case c.State == dns.PointsElsewhere:
		return RecordDifferent, "This name points to another server, so mail programs that look here are not set up for this one. Point it to the mail server's name."
	}
	return RecordUnknown, "The record could not be looked up at the domain's nameservers; try again in a moment."
}

// ---- applying ----

// planMailRecords compares the records the mail domain needs with the
// zone's record sets and returns the changes, with the record sets to
// write (nil for keep and skip).
func planMailRecords(specs []mailRecordSpec, host, domain, zone string, sets []dns.RecordSet, ips []netip.Addr) ([]MailRecordChange, []*dns.RecordSet) {
	changes := []MailRecordChange{}
	var writes []*dns.RecordSet
	for _, spec := range specs {
		name := zoneRelative(subName(spec.sub, domain), zone)
		if spec.typ == "CNAME" {
			c, w := planSetupName(spec, host, name, sets, ips)
			changes, writes = append(changes, c), append(writes, w)
			continue
		}
		var existing *dns.RecordSet
		hasCNAME := false
		for i := range sets {
			if !strings.EqualFold(sets[i].Name, name) {
				continue
			}
			switch strings.ToUpper(sets[i].Type) {
			case spec.typ:
				existing = &sets[i]
			case "CNAME":
				hasCNAME = true
			}
		}
		var old []string
		if existing != nil {
			old = normalValues(spec.typ, existing.Values)
		}
		c := MailRecordChange{Kind: spec.kind, Type: spec.typ, Name: name, Old: []string{}, New: []string{}}
		if hasCNAME {
			c.Action, c.Note = ChangeSkip, "There is a CNAME record at this name, so no other record can be added; remove it first."
			changes, writes = append(changes, c), append(writes, nil)
			continue
		}
		j := judge(spec, host, old)
		c.Note = j.note
		var write *dns.RecordSet
		switch {
		case j.status == RecordOK:
			c.Action, c.Old, c.New = ChangeKeep, keptValues(spec, old), keptValues(spec, old)
		case j.status == RecordConflict:
			c.Action, c.Old, c.New = ChangeSkip, keptValues(spec, old), keptValues(spec, old)
		case j.keepOthers:
			// TXT at @ or _dmarc: only our value is added or replaced.
			var mine, others []string
			for _, v := range old {
				if (isSPFKind(spec.kind) && isSPF(v)) || (spec.kind == MailRecordDMARC && isDMARC(v)) {
					mine = append(mine, v)
				} else {
					others = append(others, v)
				}
			}
			c.Old, c.New = nonNil(mine), j.values
			switch {
			case existing == nil:
				c.Action = ChangeCreate
			case len(mine) == 0:
				c.Action = ChangeAdd
				c.Note = strings.TrimSpace(c.Note + " The other TXT records at this name are kept.")
			default:
				c.Action = ChangeReplace
				c.Note = strings.TrimSpace(c.Note + " The other TXT records at this name are kept.")
			}
			write = &dns.RecordSet{Name: name, Type: spec.typ, Values: append(slices.Clone(others), j.values...)}
		default:
			c.Old, c.New = nonNil(old), j.values
			c.Action = ChangeCreate
			if existing != nil {
				c.Action = ChangeReplace
			}
			c.NeedsConfirm = spec.kind == MailRecordMX && existing != nil
			write = &dns.RecordSet{Name: name, Type: spec.typ, Values: slices.Clone(j.values)}
		}
		if write != nil && existing != nil {
			write.TTL = existing.TTL
		}
		changes, writes = append(changes, c), append(writes, write)
	}
	return changes, writes
}

// planSetupName plans an optional name for setting up mail programs: a
// CNAME to the mail host is created where the name is unused, and replaces
// a CNAME that leads elsewhere. Address records that point to this server
// are fine as they are; other records at the name are never touched.
func planSetupName(spec mailRecordSpec, host, name string, sets []dns.RecordSet, ips []netip.Addr) (MailRecordChange, *dns.RecordSet) {
	c := MailRecordChange{Kind: spec.kind, Type: spec.typ, Name: name, Old: []string{}, New: []string{}, Optional: true}
	var cname *dns.RecordSet
	var addrs []string
	others := false
	for i := range sets {
		if !strings.EqualFold(sets[i].Name, name) {
			continue
		}
		switch strings.ToUpper(sets[i].Type) {
		case "CNAME":
			cname = &sets[i]
		case "A", "AAAA":
			addrs = append(addrs, normalValues(strings.ToUpper(sets[i].Type), sets[i].Values)...)
		default:
			others = true
		}
	}
	switch {
	case cname != nil:
		old := normalValues("CNAME", cname.Values)
		c.Old = old
		if len(old) == 1 && strings.EqualFold(strings.TrimSuffix(old[0], "."), host) {
			c.Action, c.New = ChangeKeep, old
			return c, nil
		}
		c.Action, c.New = ChangeReplace, []string{spec.want}
		c.Note = "This name points mail programs to another server; it is replaced."
		return c, &dns.RecordSet{Name: name, Type: "CNAME", TTL: cname.TTL, Values: []string{spec.want}}
	case len(addrs) > 0:
		c.Old, c.New = addrs, addrs
		here := true
		for _, v := range addrs {
			ip, err := netip.ParseAddr(v)
			if err != nil || !slices.Contains(ips, ip.Unmap()) {
				here = false
			}
		}
		if here {
			c.Action = ChangeKeep
			return c, nil
		}
		c.Action = ChangeSkip
		c.Note = "There are address records at this name that point to another server; change them by hand to this server's addresses, or remove them so the name can point to the mail server."
		return c, nil
	case others:
		c.Action = ChangeSkip
		c.Note = "There are other records at this name, so it cannot point to the mail server; set it up by hand if mail programs should find their settings here."
		return c, nil
	}
	c.Action, c.New = ChangeCreate, []string{spec.want}
	return c, &dns.RecordSet{Name: name, Type: "CNAME", Values: []string{spec.want}}
}

// keptValues are the values of a record set that concern the record (for
// TXT at @ only the SPF value).
func keptValues(spec mailRecordSpec, values []string) []string {
	out := []string{}
	for _, v := range values {
		switch spec.kind {
		case MailRecordSPF, MailRecordHostSPF:
			if !isSPF(v) {
				continue
			}
		case MailRecordDMARC:
			if !isDMARC(v) {
				continue
			}
		}
		out = append(out, v)
	}
	return out
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// SetUpMailRecords writes the records the mail domain needs into its zone
// in the actor's own DNS connection. Without confirm, nothing is written
// when a change needs confirmation (replacing MX records that point
// elsewhere); the plan is returned with Applied false. Otherwise every
// create, replace and add is written and the domain is checked for
// activation right away.
func (s *Service) SetUpMailRecords(ctx context.Context, a Actor, domain string, confirm bool) (MailRecordChanges, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailRecordChanges{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailRecordChanges{}, err
	}
	if err := needDNS(a); err != nil {
		return MailRecordChanges{}, err
	}
	host, err := s.mailHostOrConflict()
	if err != nil {
		return MailRecordChanges{}, err
	}
	m, note, err := s.mailZone(ctx, a, md.Domain)
	if err != nil {
		return MailRecordChanges{}, err
	}
	if note != "" && m.conn.ID == 0 {
		return MailRecordChanges{}, errorf(Conflict, "%s", strings.TrimSuffix(note, "."))
	}
	specs, err := s.mailRecordSpecs(ctx, md.Domain, host)
	if err != nil {
		return MailRecordChanges{}, err
	}
	res, kinds, err := s.writeMailRecords(ctx, a, m, specs, host, md.Domain, confirm, "mail.records.set", md.Domain)
	if err != nil || !res.Applied {
		return res, err
	}
	if len(kinds) > 0 {
		// The records were just written: the domain may become active now,
		// and the names for setting up mail programs may point here.
		if fresh, err := s.store.MailDomain(ctx, md.Domain); err == nil && !fresh.Active {
			if fresh, err = s.checkMailDomain(ctx, fresh); err == nil && fresh.Active {
				_ = s.mailSaved(ctx) // logged; the background loop retries
			}
		}
		s.setupHosts.checkSoon()
	}
	return res, nil
}

// SetUpMailHostSPF writes the SPF record of the mail host (v=spf1 a -all,
// or a merged into an existing one) into its zone in the admin's own DNS
// connection. Only admins: the mail host is a setting of the whole server.
func (s *Service) SetUpMailHostSPF(ctx context.Context, a Actor) (MailRecordChanges, error) {
	if err := needAdmin(a); err != nil {
		return MailRecordChanges{}, err
	}
	if err := needDNS(a); err != nil {
		return MailRecordChanges{}, err
	}
	host, err := s.mailHostOrConflict()
	if err != nil {
		return MailRecordChanges{}, err
	}
	m, note, err := s.mailZone(ctx, a, host)
	if err != nil {
		return MailRecordChanges{}, err
	}
	if note != "" && m.conn.ID == 0 {
		return MailRecordChanges{}, errorf(Conflict, "%s", strings.TrimSuffix(strings.Replace(note, "The domain's zone", "The zone of the mail server's name", 1), "."))
	}
	res, _, err := s.writeMailRecords(ctx, a, m, []mailRecordSpec{hostSPFSpec}, host, host, true, "mail.host-spf.set", host)
	return res, err
}

// writeMailRecords plans the records of specs for domain (relative to the
// zone of m) and writes them through the actor's DNS connection: the same
// checks as every other DNS change (write access, the zone still in the
// connection, a primary zone). Without confirm nothing is written when a
// change needs confirmation. It returns the kinds written.
func (s *Service) writeMailRecords(ctx context.Context, a Actor, m zoneMatch, specs []mailRecordSpec, host, domain string, confirm bool, action, target string) (MailRecordChanges, []string, error) {
	c, z, err := s.writableZone(ctx, a, m.conn.ID, m.zone.Name)
	if err != nil {
		return MailRecordChanges{}, nil, err
	}
	p, token, err := s.provider(c)
	if err != nil {
		return MailRecordChanges{}, nil, err
	}
	sets, err := p.RecordSets(ctx, z.Name)
	if err != nil {
		return MailRecordChanges{}, nil, dnsError(c.Provider, c.Name, token, err)
	}
	changes, writes := planMailRecords(specs, host, domain, z.Name, sets, s.effectiveServerIPs())
	if !confirm && slices.ContainsFunc(changes, func(c MailRecordChange) bool { return c.NeedsConfirm }) {
		return MailRecordChanges{Changes: changes, Applied: false}, nil, nil
	}

	var kinds []string
	finish := func(err error) error {
		if len(kinds) > 0 {
			s.dnsCache.dropZones(c.ID)
			s.noteWrite(ctx, c, nil, nil)
			s.record(ctx, a, action, target, fmt.Sprintf("%s in zone %s (connection %q)", strings.Join(kinds, ", "), z.Name, c.Name))
		}
		if err != nil {
			derr := dnsError(c.Provider, c.Name, token, err)
			s.noteWrite(ctx, c, err, derr)
			return derr
		}
		return nil
	}
	for i, w := range writes {
		if w == nil {
			continue
		}
		var ttl *int
		if w.TTL > 0 {
			t := w.TTL
			ttl = &t
		}
		rs, err := validateRecordSet(z.Name, w.Name, w.Type, ttl, w.Values)
		if err != nil {
			if ferr := finish(nil); ferr != nil {
				return MailRecordChanges{}, kinds, ferr
			}
			return MailRecordChanges{}, kinds, err
		}
		if err := p.SetRecordSet(ctx, z.Name, rs); err != nil {
			return MailRecordChanges{}, kinds, finish(err)
		}
		kinds = append(kinds, changes[i].Kind)
	}
	if err := finish(nil); err != nil {
		return MailRecordChanges{}, kinds, err
	}
	return MailRecordChanges{Changes: changes, Applied: true}, kinds, nil
}
