package mcp

import (
	"context"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

type dnsDomainInfo struct {
	ConnectionID   int64    `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain"`
	ConnectionName string   `json:"connection_name" jsonschema:"name of that DNS connection"`
	Domain         string   `json:"domain" jsonschema:"the domain (zone), e.g. example.com"`
	ReadOnly       bool     `json:"read_only" jsonschema:"the provider does not allow changing its records"`
	Nameservers    []string `json:"nameservers" jsonschema:"the provider's nameservers, to be set where the domain is registered"`
	Adopted        bool     `json:"adopted" jsonschema:"the user chose to manage this domain in the panel"`
	UsedBy         []string `json:"used_by" jsonschema:"what of yours uses a domain inside it (your sites, redirects, e-mail; for administrators the panel itself); such a domain stays in the panel while it is used"`
}

func dnsDomainInfoOf(z panel.DNSZone) dnsDomainInfo {
	return dnsDomainInfo{
		ConnectionID: z.ConnectionID, ConnectionName: z.ConnectionName, Domain: z.Name, ReadOnly: z.ReadOnly,
		Nameservers: z.Nameservers, Adopted: z.Adopted, UsedBy: z.UsedBy,
	}
}

type dnsProblemInfo struct {
	ConnectionID   int64  `json:"connection_id" jsonschema:"id of the DNS connection"`
	ConnectionName string `json:"connection_name" jsonschema:"name of the DNS connection"`
	Error          string `json:"error" jsonschema:"why its domains could not be listed"`
}

type listDNSDomainsInput struct {
	Available bool `json:"available,omitempty" jsonschema:"list the domains of your DNS connections that are not managed in the panel yet, instead of those that are"`
}

type dnsDomainsOutput struct {
	Domains  []dnsDomainInfo  `json:"domains"`
	Problems []dnsProblemInfo `json:"problems"`
}

type dnsDomainInput struct {
	ConnectionID int64  `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain, from list_dns_domains"`
	Domain       string `json:"domain" jsonschema:"the domain (zone), e.g. example.com"`
}

type dnsDomainOutput struct {
	Domain dnsDomainInfo `json:"domain"`
}

type releasedDNSDomainOutput struct {
	Released string `json:"released" jsonschema:"the domain that is no longer managed in the panel"`
}

// addDNSTools registers the DNS tools: which domains of the user's DNS
// connections are managed in the panel, and their records.
func addDNSTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_dns_domains",
		Title: "List DNS domains",
		Description: "List the domains (DNS zones) managed in the panel through your own DNS connections: those you " +
			"chose to manage and those your own sites, redirects or e-mail use (for administrators also the panel " +
			"itself). With available, list the " +
			"domains of your connections that are not managed in the panel yet instead. A connection that cannot be " +
			"read is reported in problems.",
		Annotations: annotations("List DNS domains", true, false, true),
	}, func(ctx context.Context, c call, in listDNSDomainsInput) (dnsDomainsOutput, error) {
		var zones panel.DNSZones
		var err error
		if in.Available {
			zones, err = p.AvailableDNSZones(ctx, c.actor)
		} else {
			zones, err = p.DNSZones(ctx, c.actor)
		}
		if err != nil {
			return dnsDomainsOutput{}, err
		}
		out := dnsDomainsOutput{Domains: []dnsDomainInfo{}, Problems: []dnsProblemInfo{}}
		for _, z := range zones.Zones {
			out.Domains = append(out.Domains, dnsDomainInfoOf(z))
		}
		for _, pr := range zones.Problems {
			out.Problems = append(out.Problems, dnsProblemInfo(pr))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "manage_dns_domain",
		Title: "Manage a DNS domain in the panel",
		Description: "Start managing a domain of one of your DNS connections in the panel, so it shows up with its DNS " +
			"records. Nothing changes at the DNS provider. Safe to repeat.",
		Annotations: annotations("Manage a DNS domain in the panel", false, false, true),
	}, func(ctx context.Context, c call, in dnsDomainInput) (dnsDomainOutput, error) {
		z, err := p.AdoptDNSZone(ctx, c.actor, in.ConnectionID, in.Domain)
		return dnsDomainOutput{dnsDomainInfoOf(z)}, err
	})

	add(s, &sdk.Tool{
		Name:  "stop_managing_dns_domain",
		Title: "Stop managing a DNS domain in the panel",
		Description: "Stop managing a domain in the panel. The domain and its records stay at the DNS provider " +
			"unchanged; it can be managed here again later. A domain that your own sites, redirects or e-mail " +
			"(or, for administrators, the panel itself) use stays in the panel; the error says what uses it.",
		Annotations: annotations("Stop managing a DNS domain in the panel", false, false, false),
	}, func(ctx context.Context, c call, in dnsDomainInput) (releasedDNSDomainOutput, error) {
		if err := p.ReleaseDNSZone(ctx, c.actor, in.ConnectionID, in.Domain); err != nil {
			return releasedDNSDomainOutput{}, err
		}
		return releasedDNSDomainOutput{Released: in.Domain}, nil
	})

	addDNSRecordTools(s, p)
}

type dnsConnectionInfo struct {
	ID            int64  `json:"id" jsonschema:"connection id; pass it as connection_id to the other DNS tools"`
	Provider      string `json:"provider" jsonschema:"the DNS provider, e.g. hetzner"`
	Name          string `json:"name" jsonschema:"name the user gave the connection"`
	Owner         string `json:"owner" jsonschema:"user who added the connection; only that user can use it"`
	CreatedAt     string `json:"created_at" jsonschema:"when the connection was added, RFC 3339 (UTC)"`
	LastCheckedAt string `json:"last_checked_at" jsonschema:"when the provider was last reached, RFC 3339 (UTC); empty if never"`
	LastError     string `json:"last_error" jsonschema:"why the provider could not be used the last time; empty if it worked"`
	CanWrite      *bool  `json:"can_write" jsonschema:"whether the provider accepted changes the last time; null if not known yet"`
	DomainCount   *int   `json:"domain_count" jsonschema:"how many domains the provider holds; null if not known yet"`
}

type dnsConnectionsOutput struct {
	Connections []dnsConnectionInfo `json:"connections"`
}

type dnsRecordInfo struct {
	Name   string   `json:"name" jsonschema:"name relative to the domain: @ for the domain itself, www for www.example.com"`
	Type   string   `json:"type" jsonschema:"record type, e.g. A, AAAA, CNAME, MX, TXT"`
	TTL    *int     `json:"ttl" jsonschema:"how long resolvers keep the answer, in seconds; null means the domain's default"`
	Values []string `json:"values" jsonschema:"all values of this name and type; TXT values are plain text, without quotes"`
	Locked bool     `json:"locked" jsonschema:"the provider protects the record set; it cannot be changed or deleted here"`
}

func dnsRecordInfoOf(rs panel.RecordSet) dnsRecordInfo {
	return dnsRecordInfo{Name: rs.Name, Type: rs.Type, TTL: rs.TTL, Values: rs.Values, Locked: rs.Locked}
}

type dnsRecordsOutput struct {
	Records []dnsRecordInfo `json:"records"`
}

type setDNSRecordInput struct {
	ConnectionID int64    `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain, from list_dns_domains"`
	Domain       string   `json:"domain" jsonschema:"the domain (zone) as shown by list_dns_domains, e.g. example.com"`
	Name         string   `json:"name" jsonschema:"name relative to the domain: @ for the domain itself, www for www.example.com"`
	Type         string   `json:"type" jsonschema:"record type: A, AAAA, CNAME, MX, TXT, CAA, SRV, NS, ..."`
	TTL          *int     `json:"ttl,omitempty" jsonschema:"seconds resolvers keep the answer; leave out for the domain's default"`
	Values       []string `json:"values" jsonschema:"the complete new list of values of this name and type, e.g. [\"203.0.113.5\"] or for MX [\"10 mail.example.com\"]; TXT values as plain text, without quotes"`
}

type dnsRecordOutput struct {
	Record dnsRecordInfo `json:"record"`
}

type dnsRecordKeyInput struct {
	ConnectionID int64  `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain, from list_dns_domains"`
	Domain       string `json:"domain" jsonschema:"the domain (zone) as shown by list_dns_domains, e.g. example.com"`
	Name         string `json:"name" jsonschema:"name relative to the domain, as shown by list_dns_records: @ for the domain itself"`
	Type         string `json:"type" jsonschema:"record type, e.g. A or TXT"`
}

type deletedDNSRecordOutput struct {
	Deleted string `json:"deleted" jsonschema:"name and type of the deleted record set, with the domain"`
}

type domainInput struct {
	Domain string `json:"domain" jsonschema:"a domain of one of your sites or of one of your whole-domain redirects, e.g. www.example.com"`
}

type domainZoneInfo struct {
	ConnectionID   int64  `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain"`
	ConnectionName string `json:"connection_name" jsonschema:"name of that DNS connection"`
	Zone           string `json:"zone" jsonschema:"the DNS domain (zone) the records are in"`
	RecordName     string `json:"record_name" jsonschema:"name of the records within that zone, @ for the zone itself"`
}

type domainCheckOutput struct {
	Domain string          `json:"domain" jsonschema:"the domain"`
	State  string          `json:"state" jsonschema:"points_here (ready), points_elsewhere (DNS points to another server), no_record (no DNS record yet) or lookup_failed (could not be checked, try again later)"`
	Found  []string        `json:"found" jsonschema:"addresses the domain currently points to"`
	Zone   *domainZoneInfo `json:"zone" jsonschema:"where point_domain_at_server would change the records; null if none of your DNS connections holds the domain"`
}

type pointDomainOutput struct {
	Changed []dnsRecordInfo `json:"changed" jsonschema:"the record sets that were written; a removed one has no values. Empty when the domain already pointed here"`
}

// addDNSRecordTools registers the tools that read and change DNS records
// through the user's own DNS connections.
func addDNSRecordTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_dns_connections",
		Title: "List DNS connections",
		Description: "List your connections to DNS provider accounts, with whether they worked the last time. " +
			"Administrators also see other people's connections, but each one can only be used by the person " +
			"who added it. Connections are added in the web interface, not through these tools.",
		Annotations: annotations("List DNS connections", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (dnsConnectionsOutput, error) {
		list, err := p.DNSConnections(ctx, c.actor)
		if err != nil {
			return dnsConnectionsOutput{}, err
		}
		out := dnsConnectionsOutput{Connections: []dnsConnectionInfo{}}
		for _, dc := range list {
			out.Connections = append(out.Connections, dnsConnectionInfo{
				ID: dc.ID, Provider: dc.Provider, Name: dc.Name, Owner: dc.Owner,
				CreatedAt: rfc3339(&dc.CreatedAt), LastCheckedAt: rfc3339(dc.LastCheckedAt),
				LastError: dc.LastError, CanWrite: dc.CanWrite, DomainCount: dc.ZoneCount,
			})
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "list_dns_records",
		Title: "List DNS records",
		Description: "List all DNS records of a domain of one of your DNS connections, as the provider has them now: " +
			"the domain itself (@) first, then by name and type. Records of one name and type are grouped into one " +
			"record set. Get connection_id and domain from list_dns_domains.",
		Annotations: annotations("List DNS records", true, false, true),
	}, func(ctx context.Context, c call, in dnsDomainInput) (dnsRecordsOutput, error) {
		list, err := p.DNSRecords(ctx, c.actor, in.ConnectionID, in.Domain)
		if err != nil {
			return dnsRecordsOutput{}, err
		}
		out := dnsRecordsOutput{Records: []dnsRecordInfo{}}
		for _, rs := range list {
			out.Records = append(out.Records, dnsRecordInfoOf(rs))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_dns_record",
		Title: "Set a DNS record",
		Description: "Create or replace the record set of one name and type at the DNS provider: the given values " +
			"replace all values that name and type had. The change is live at once, but resolvers may keep the old " +
			"answer until its TTL runs out. Safe to repeat with the same input. Returns the record set as written.",
		Annotations: annotations("Set a DNS record", false, true, true),
	}, func(ctx context.Context, c call, in setDNSRecordInput) (dnsRecordOutput, error) {
		rs, err := p.SetDNSRecord(ctx, c.actor, in.ConnectionID, in.Domain, in.Name, in.Type, in.TTL, in.Values)
		return dnsRecordOutput{dnsRecordInfoOf(rs)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_dns_record",
		Title: "Delete a DNS record",
		Description: "Delete all records of one name and type at the DNS provider; whatever relied on them (a site, " +
			"e-mail, a verification) stops working once resolvers forget the old answer. Only call it when the user " +
			"asked for it. Deleting a record set that does not exist is not an error. The domain's own SOA and NS " +
			"records cannot be deleted.",
		Annotations: annotations("Delete a DNS record", false, true, true),
	}, func(ctx context.Context, c call, in dnsRecordKeyInput) (deletedDNSRecordOutput, error) {
		if err := p.DeleteDNSRecord(ctx, c.actor, in.ConnectionID, in.Domain, in.Name, in.Type); err != nil {
			return deletedDNSRecordOutput{}, err
		}
		return deletedDNSRecordOutput{Deleted: strings.TrimSpace(in.Name) + " " + strings.ToUpper(strings.TrimSpace(in.Type)) + " in " + strings.TrimSpace(in.Domain)}, nil
	})

	add(s, &sdk.Tool{
		Name:  "check_domain",
		Title: "Check where a domain points",
		Description: "Check whether one domain of your sites or whole-domain redirects points at this server, and " +
			"which of your DNS connections holds it, i.e. where point_domain_at_server would change it. Use " +
			"check_site_domains to check all domains of a site at once. DNS changes can take a while to spread.",
		Annotations: annotations("Check where a domain points", true, false, true),
	}, func(ctx context.Context, c call, in domainInput) (domainCheckOutput, error) {
		st, err := p.DomainStatusOf(ctx, c.actor, in.Domain)
		if err != nil {
			return domainCheckOutput{}, err
		}
		out := domainCheckOutput{Domain: st.Domain, State: st.State, Found: st.Found}
		if out.Found == nil {
			out.Found = []string{}
		}
		if z := st.Zone; z != nil {
			out.Zone = &domainZoneInfo{ConnectionID: z.ConnectionID, ConnectionName: z.ConnectionName, Zone: z.Zone, RecordName: z.RecordName}
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "point_domain_at_server",
		Title: "Point a domain at this server",
		Description: "Make a domain of your sites or whole-domain redirects point at this server: writes its address " +
			"records (A, AAAA) with this server's addresses at the DNS provider of your connection that holds the " +
			"domain, and removes an address record of a kind this server has no address for. Refused when the name " +
			"has a CNAME record. Changes nothing if the domain already points here, so it is safe to repeat. " +
			"Returns what was changed.",
		Annotations: annotations("Point a domain at this server", false, true, true),
	}, func(ctx context.Context, c call, in domainInput) (pointDomainOutput, error) {
		res, err := p.PointDomainOf(ctx, c.actor, in.Domain)
		if err != nil {
			return pointDomainOutput{}, err
		}
		out := pointDomainOutput{Changed: []dnsRecordInfo{}}
		for _, rs := range res.Changed {
			out.Changed = append(out.Changed, dnsRecordInfoOf(rs))
		}
		return out, nil
	})
}
