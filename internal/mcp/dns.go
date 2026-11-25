package mcp

import (
	"context"

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

// addDNSTools registers the tools that choose which domains of the user's
// DNS connections are managed in the panel.
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
}
