package panel

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/dns"
)

// Record templates: ready-made record sets for common needs (a mail
// provider, a "prove you own this domain" value). Applying one first plans
// what would change at the zone's apex and only writes with confirm; the
// plan never removes a value it does not own:
//
//   - TXT values are added next to the other values of the name; the list
//     of servers that may send mail (SPF) is merged into an existing one,
//     never published twice.
//   - MX, SRV and CNAME record sets are replaced as a whole, shown with the
//     values they had.
//   - A CNAME next to other records at the same name is reported, never
//     written.
//
// The records of e-mail on this server are not a template here: they
// depend on the mail domain's keys and settings and are set up with
// SetUpMailRecords (the e-mail page of the domain); the catalogue lists an
// entry with OwnMail that points there.

// Actions of a planned template change.
const (
	TemplateAdd      = "add"      // the record set does not exist yet
	TemplateChange   = "change"   // the record set exists and gets other values
	TemplateOK       = "ok"       // already as it should be
	TemplateConflict = "conflict" // cannot be set up automatically; nothing is written
)

// DNSTemplateParam is a value a person fills in before applying a template.
type DNSTemplateParam struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Placeholder string `json:"placeholder"`
	Optional    bool   `json:"optional"`
}

// DNSTemplate is one entry of the catalogue.
type DNSTemplate struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Params      []DNSTemplateParam `json:"params"`
	// OwnMail: e-mail on this server. It has no records here; they are set
	// up with the mail domain (SetUpMailRecords).
	OwnMail bool `json:"own_mail"`

	build func(zone string, params map[string]string) ([]templateRecord, error)
}

// templateMode is how a template record meets what is already there.
type templateMode int

const (
	// replaceSet: the record set should hold exactly these values.
	replaceSet templateMode = iota
	// addTXT: the values are added to the TXT record set, others stay.
	addTXT
	// mergeSPFInclude: the include is merged into the one SPF value of the name,
	// or value is added when there is none.
	mergeSPFInclude
)

type templateRecord struct {
	name   string // relative to the zone
	typ    string
	values []string
	mode   templateMode
	// include is the SPF mechanism of mergeSPFInclude, like
	// "include:_spf.google.com".
	include string
}

// DNSTemplateChange is the planned (or made) change of one record set.
type DNSTemplateChange struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Action is add, change, ok or conflict.
	Action string `json:"action"`
	// Current holds all values of the record set now, New all values it
	// has afterwards (for TXT including the values that are kept).
	Current []string `json:"current"`
	New     []string `json:"new"`
	Note    string   `json:"note"`
}

// DNSTemplateResult is the outcome of ApplyDNSTemplate. Applied is true
// only when it was called with confirm and nothing stood in the way.
type DNSTemplateResult struct {
	Template string              `json:"template"`
	Zone     string              `json:"zone"`
	Changes  []DNSTemplateChange `json:"changes"`
	Applied  bool                `json:"applied"`
}

func spfDefault(include, all string) string { return "v=spf1 " + include + " " + all }

// dnsTemplates is the catalogue. Every value was checked against the
// provider's documentation linked next to it.
var dnsTemplates = []DNSTemplate{
	{
		ID:          "domain-verification",
		Title:       "Confirm that you own this domain",
		Description: "Adds the confirmation text a service gave you, next to the texts that are already there.",
		Params: []DNSTemplateParam{
			{ID: "value", Label: "Confirmation text", Description: "Exactly as the service shows it.", Placeholder: "example-verification=abc123"},
			{ID: "name", Label: "Name", Description: "Only if the service asks for a name below the domain. Leave empty for the domain itself.", Placeholder: "@", Optional: true},
		},
		build: func(zone string, p map[string]string) ([]templateRecord, error) {
			v := strings.TrimSpace(p["value"])
			if v == "" {
				return nil, errorf(Invalid, "give the confirmation text")
			}
			if _, err := recordValue("TXT", v); err != nil {
				return nil, err
			}
			name, err := recordName(zone, p["name"])
			if err != nil {
				return nil, err
			}
			return []templateRecord{{name: name, typ: "TXT", values: []string{v}, mode: addTXT}}, nil
		},
	},
	{
		// MX: https://knowledge.workspace.google.com/admin/domains/set-up-mx-records-for-google-workspace
		// ("smtp.google.com", priority 1, remove any other MX records).
		// SPF: https://knowledge.workspace.google.com/admin/security/set-up-spf
		// ("v=spf1 include:_spf.google.com ~all").
		ID:          "google-workspace",
		Title:       "Google Workspace mail",
		Description: "Sends the domain's mail to Google and allows Google to send mail for it.",
		build: func(zone string, _ map[string]string) ([]templateRecord, error) {
			return []templateRecord{
				{name: "@", typ: "MX", values: []string{"1 smtp.google.com."}},
				{name: "@", typ: "TXT", values: []string{spfDefault("include:_spf.google.com", "~all")}, mode: mergeSPFInclude, include: "include:_spf.google.com"},
			}, nil
		},
	},
	{
		// https://learn.microsoft.com/en-us/microsoft-365/admin/get-help-with-domains/create-dns-records-at-any-dns-hosting-provider
		// (MX at @ with the value from the admin center, priority 0; SPF
		// "v=spf1 include:spf.protection.outlook.com -all", merged into an
		// existing one) and
		// https://learn.microsoft.com/en-us/microsoft-365/enterprise/external-domain-name-system-records
		// (CNAME autodiscover -> autodiscover.outlook.com).
		ID:          "microsoft-365",
		Title:       "Microsoft 365 mail",
		Description: "Sends the domain's mail to Microsoft, allows Microsoft to send mail for it and lets Outlook set itself up.",
		Params: []DNSTemplateParam{
			{ID: "mx_host", Label: "Mail server", Description: "The mail server Microsoft shows for this domain in its admin center under Domains.", Placeholder: "example-com.mail.protection.outlook.com"},
		},
		build: func(zone string, p map[string]string) ([]templateRecord, error) {
			if strings.TrimSpace(p["mx_host"]) == "" {
				return nil, errorf(Invalid, "give the mail server Microsoft shows for this domain")
			}
			host, err := hostValue(p["mx_host"], false)
			if err != nil {
				return nil, err
			}
			return []templateRecord{
				{name: "@", typ: "MX", values: []string{"0 " + host}},
				{name: "@", typ: "TXT", values: []string{spfDefault("include:spf.protection.outlook.com", "-all")}, mode: mergeSPFInclude, include: "include:spf.protection.outlook.com"},
				{name: "autodiscover", typ: "CNAME", values: []string{"autodiscover.outlook.com."}},
			}, nil
		},
	},
	{
		// https://www.fastmail.help/hc/en-us/articles/360060591153-Manual-DNS-configuration
		// (MX in1-smtp/in2-smtp.messagingengine.com 10/20; SPF
		// "v=spf1 include:spf.messagingengine.com ?all"; DKIM CNAMEs
		// fm1..fm3._domainkey -> fmN.<domain>.dkim.fmhosted.com; SRV
		// _submissions._tcp 0 1 465 smtp.fastmail.com and _imaps._tcp
		// 0 1 993 imap.fastmail.com).
		ID:          "fastmail",
		Title:       "Fastmail mail",
		Description: "Sends the domain's mail to Fastmail, allows Fastmail to send and sign mail for it and lets mail programs set themselves up.",
		build: func(zone string, _ map[string]string) ([]templateRecord, error) {
			recs := []templateRecord{
				{name: "@", typ: "MX", values: []string{"10 in1-smtp.messagingengine.com.", "20 in2-smtp.messagingengine.com."}},
				{name: "@", typ: "TXT", values: []string{spfDefault("include:spf.messagingengine.com", "?all")}, mode: mergeSPFInclude, include: "include:spf.messagingengine.com"},
			}
			for _, k := range []string{"fm1", "fm2", "fm3"} {
				recs = append(recs, templateRecord{name: k + "._domainkey", typ: "CNAME", values: []string{k + "." + zone + ".dkim.fmhosted.com."}})
			}
			return append(recs,
				templateRecord{name: "_submissions._tcp", typ: "SRV", values: []string{"0 1 465 smtp.fastmail.com."}},
				templateRecord{name: "_imaps._tcp", typ: "SRV", values: []string{"0 1 993 imap.fastmail.com."}},
			), nil
		},
	},
	{
		ID:          "own-mail",
		Title:       "E-mail on this server",
		Description: "Leads to the domain's e-mail page, which sets up these records.",
		OwnMail:     true,
	},
}

// DNSTemplates lists the catalogue. Everyone who may use DNS records may
// read it.
func (s *Service) DNSTemplates(_ context.Context, a Actor) ([]DNSTemplate, error) {
	if err := needDNS(a); err != nil {
		return nil, err
	}
	out := make([]DNSTemplate, len(dnsTemplates))
	for i, t := range dnsTemplates {
		t.Params = slices.Clone(t.Params)
		if t.Params == nil {
			t.Params = []DNSTemplateParam{}
		}
		out[i] = t
	}
	return out, nil
}

func dnsTemplate(id string) (DNSTemplate, error) {
	for _, t := range dnsTemplates {
		if t.ID == id {
			return t, nil
		}
	}
	return DNSTemplate{}, errorf(NotFound, "there is no record template %q", id)
}

// ApplyDNSTemplate plans the records of a template for the zone of one of
// the actor's own connections and, with confirm, writes them through the
// same path as SetDNSRecord. Without confirm nothing is written: the plan
// is the preview, and a read-only actor may see it. With confirm a plan
// that has a conflict is refused as a whole.
func (s *Service) ApplyDNSTemplate(ctx context.Context, a Actor, connID, zone, templateID string, params map[string]string, confirm bool) (DNSTemplateResult, error) {
	if err := needDNS(a); err != nil {
		return DNSTemplateResult{}, err
	}
	if confirm {
		if err := needWrite(a); err != nil {
			return DNSTemplateResult{}, err
		}
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return DNSTemplateResult{}, err
	}
	z, err := s.zoneIn(ctx, c, zone)
	if err != nil {
		return DNSTemplateResult{}, err
	}
	if z.ReadOnly {
		return DNSTemplateResult{}, errorf(Conflict, "the zone %s is not a primary zone at %s, so its records cannot be changed here", z.Name, providerLabel(c.Provider))
	}
	t, err := dnsTemplate(templateID)
	if err != nil {
		return DNSTemplateResult{}, err
	}
	if t.OwnMail {
		return DNSTemplateResult{}, errorf(Invalid, "the records for e-mail on this server are set up with the domain's e-mail settings (the e-mail page of the domain, or set_up_mail_records)")
	}
	recs, err := t.build(z.Name, params)
	if err != nil {
		return DNSTemplateResult{}, err
	}
	p, token, err := s.provider(c)
	if err != nil {
		return DNSTemplateResult{}, err
	}
	sets, err := p.RecordSets(ctx, z.Name)
	if err != nil {
		return DNSTemplateResult{}, dnsError(c.Provider, c.Name, token, err)
	}
	changes, writes := planTemplate(recs, sets)
	res := DNSTemplateResult{Template: t.ID, Zone: z.Name, Changes: changes}
	if !confirm {
		return res, nil
	}
	var blocked []string
	for _, ch := range changes {
		if ch.Action == TemplateConflict {
			blocked = append(blocked, ch.Type+" "+ch.Name)
		}
	}
	if len(blocked) > 0 {
		return res, errorf(Conflict, "nothing was changed: %s cannot be set up automatically; see the preview", strings.Join(blocked, ", "))
	}
	// Check every record set before writing the first, so that a bad value
	// does not leave the template half applied.
	type write struct {
		rs  dns.RecordSet
		ttl *int
	}
	var ws []write
	for _, w := range writes {
		if w == nil {
			continue
		}
		var ttl *int
		if w.TTL > 0 {
			n := w.TTL
			ttl = &n
		}
		rs, err := validateRecordSet(z.Name, w.Name, w.Type, ttl, w.Values)
		if err != nil {
			return res, err
		}
		ws = append(ws, write{rs, ttl})
	}
	for _, w := range ws {
		if _, err := s.SetDNSRecord(ctx, a, c.ID, z.Name, w.rs.Name, w.rs.Type, w.ttl, w.rs.Values); err != nil {
			return res, err
		}
	}
	if len(ws) > 0 {
		s.record(ctx, a, "dns.template.apply", z.Name, fmt.Sprintf("%s: %d record sets (connection %q)", t.ID, len(ws), c.Name))
	}
	res.Applied = true
	return res, nil
}

// planTemplate compares the template's records with the zone's record sets.
// It returns one change per template record and the record set to write
// (nil for ok and conflict).
func planTemplate(recs []templateRecord, sets []dns.RecordSet) ([]DNSTemplateChange, []*dns.RecordSet) {
	changes := []DNSTemplateChange{}
	var writes []*dns.RecordSet
	for _, r := range recs {
		var existing *dns.RecordSet
		var cname *dns.RecordSet
		var others []string
		for i := range sets {
			if !strings.EqualFold(sets[i].Name, r.name) {
				continue
			}
			typ := strings.ToUpper(sets[i].Type)
			switch {
			case typ == r.typ:
				existing = &sets[i]
			case typ == "CNAME":
				cname = &sets[i]
			default:
				others = append(others, typ)
			}
		}
		c := DNSTemplateChange{Name: r.name, Type: r.typ, Current: []string{}, New: []string{}}
		var current []string
		if existing != nil {
			current = normalValues(r.typ, existing.Values)
			c.Current = current
		}
		switch {
		case r.typ != "CNAME" && cname != nil:
			c.Action = TemplateConflict
			c.Current = normalValues("CNAME", cname.Values)
			c.Note = "This name points to another name, so it cannot have other records. Remove the alias first."
			changes, writes = append(changes, c), append(writes, nil)
			continue
		case r.typ == "CNAME" && len(others) > 0:
			c.Action = TemplateConflict
			c.Note = fmt.Sprintf("This name already has other records (%s), so it cannot become an alias. Remove them first.", strings.Join(others, ", "))
			changes, writes = append(changes, c), append(writes, nil)
			continue
		}
		var want []string
		switch r.mode {
		case addTXT:
			want = slices.Clone(current)
			for _, v := range r.values {
				if !slices.Contains(want, v) {
					want = append(want, v)
				}
			}
			if len(current) > 0 && len(want) > len(current) {
				c.Note = "The other texts at this name are kept."
			}
		case mergeSPFInclude:
			var spf []string
			for _, v := range current {
				if isSPF(v) {
					spf = append(spf, v)
				}
			}
			switch {
			case len(spf) > 1:
				c.Action, c.New = TemplateConflict, current
				c.Note = "There is more than one list of servers allowed to send mail for this domain; mail receivers then trust none. Merge them into one by hand."
				changes, writes = append(changes, c), append(writes, nil)
				continue
			case len(spf) == 0:
				want = append(slices.Clone(current), r.values...)
				if len(current) > 0 {
					c.Note = "The other texts at this name are kept."
				}
			default:
				merged, note, ok := mergeSPFTerm(spf[0], r.include)
				if !ok {
					c.Action, c.New = TemplateConflict, current
					c.Note = note
					changes, writes = append(changes, c), append(writes, nil)
					continue
				}
				want = make([]string, 0, len(current))
				for _, v := range current {
					if v == spf[0] {
						v = merged
					}
					want = append(want, v)
				}
				if merged != spf[0] {
					c.Note = "The servers that may already send mail for this domain stay allowed; the new provider is added to the same list. The other texts at this name are kept."
				}
			}
		default:
			want = normalValues(r.typ, r.values)
			if existing != nil && !sameSet(current, want) {
				c.Note = "The values it has now are replaced."
				if r.typ == "MX" {
					c.Note = "Mail for this domain goes to the new provider instead of where it goes now."
				}
			}
		}
		c.New = want
		var w *dns.RecordSet
		switch {
		case existing == nil:
			c.Action = TemplateAdd
			w = &dns.RecordSet{Name: r.name, Type: r.typ, Values: slices.Clone(want)}
		case sameSet(current, want):
			c.Action = TemplateOK
			c.Note = ""
		default:
			c.Action = TemplateChange
			w = &dns.RecordSet{Name: r.name, Type: r.typ, TTL: existing.TTL, Values: slices.Clone(want)}
		}
		changes, writes = append(changes, c), append(writes, w)
	}
	return changes, writes
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// mergeSPFTerm adds term (an include: mechanism) to an existing SPF value
// right after "v=spf1", keeping every other term. ok is false when the
// record cannot be merged safely; note says why.
func mergeSPFTerm(existing, term string) (merged, note string, ok bool) {
	terms := strings.Fields(existing)
	if len(terms) == 0 || !strings.EqualFold(terms[0], "v=spf1") {
		return existing, "The list of servers allowed to send mail for this domain is not valid; fix it by hand.", false
	}
	lookups := 0
	for _, t := range terms[1:] {
		l := strings.ToLower(t)
		if l != "" && strings.ContainsRune("+-~?", rune(l[0])) {
			if strings.EqualFold(l[1:], term) && l[0] == '+' {
				return existing, "", true
			}
			if strings.EqualFold(l[1:], term) {
				return existing, "The list of servers allowed to send mail for this domain excludes the new provider (" + t + "); change it by hand.", false
			}
			l = l[1:]
		}
		if strings.EqualFold(l, term) {
			return existing, "", true
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
		case name == "include" || name == "a" || name == "mx" || name == "ptr" || name == "exists":
			lookups++
		}
	}
	if lookups+1 > maxSPFLookups {
		return existing, fmt.Sprintf("With the new provider added, mail receivers would have to look up %d names to check the list of servers allowed to send mail; they allow at most %d. Shorten it by hand first.", lookups+1, maxSPFLookups), false
	}
	return strings.Join(append([]string{"v=spf1", term}, terms[1:]...), " "), "", true
}
