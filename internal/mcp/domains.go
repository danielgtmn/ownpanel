package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

type ownDomainInfo struct {
	Domain    string   `json:"domain" jsonschema:"the domain, e.g. example.com"`
	CreatedAt string   `json:"created_at" jsonschema:"when it was added (RFC 3339)"`
	UsedBy    []string `json:"used_by" jsonschema:"what of yours uses a name in it (your sites, redirects, e-mail; for administrators the panel itself)"`
}

func ownDomainInfoOf(d panel.Domain) ownDomainInfo {
	return ownDomainInfo{Domain: d.Name, CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339), UsedBy: d.UsedBy}
}

type ownDomainsOutput struct {
	Domains []ownDomainInfo `json:"domains"`
}

type domainNameInput struct {
	Domain string `json:"domain" jsonschema:"the registrable domain, e.g. example.com (not a subdomain like www.example.com)"`
}

type ownDomainOutput struct {
	Domain ownDomainInfo `json:"domain"`
}

type removedDomainOutput struct {
	Removed     string   `json:"removed" jsonschema:"the domain whose entry was removed"`
	StillUsedBy []string `json:"still_used_by" jsonschema:"what of yours still uses a name in it; while anything does, the domain stays in the panel through that use"`
}

// addDomainTools registers the tools for domains whose DNS is managed
// elsewhere, kept in the panel as domains of their own.
func addDomainTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_domains",
		Title: "List domains with DNS elsewhere",
		Description: "List the domains you added to the panel whose DNS is managed elsewhere (at another provider or " +
			"the registrar). Domains of your DNS connections are listed by list_dns_domains instead.",
		Annotations: annotations("List domains with DNS elsewhere", true, false, true),
	}, func(ctx context.Context, c call, _ struct{}) (ownDomainsOutput, error) {
		list, err := p.Domains(ctx, c.actor)
		if err != nil {
			return ownDomainsOutput{}, err
		}
		out := ownDomainsOutput{Domains: []ownDomainInfo{}}
		for _, d := range list {
			out.Domains = append(out.Domains, ownDomainInfoOf(d))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "add_domain",
		Title: "Add a domain with DNS elsewhere",
		Description: "Add a domain whose DNS is managed elsewhere to the panel, so it shows up as a domain of its own, " +
			"for example to create a site, a redirect or e-mail for it later. Nothing changes at any DNS provider; " +
			"its records are set wherever the domain is managed. Safe to repeat.",
		Annotations: annotations("Add a domain with DNS elsewhere", false, false, true),
	}, func(ctx context.Context, c call, in domainNameInput) (ownDomainOutput, error) {
		d, err := p.AddDomain(ctx, c.actor, in.Domain)
		return ownDomainOutput{ownDomainInfoOf(d)}, err
	})

	add(s, &sdk.Tool{
		Name:  "remove_domain",
		Title: "Remove a domain with DNS elsewhere",
		Description: "Remove a domain added with add_domain from the panel. Only the entry goes; nothing changes at any " +
			"DNS provider, and sites, redirects and e-mail stay. If something of yours still uses a name in the " +
			"domain, it stays in the panel through that use; still_used_by says what.",
		Annotations: annotations("Remove a domain with DNS elsewhere", false, false, false),
	}, func(ctx context.Context, c call, in domainNameInput) (removedDomainOutput, error) {
		r, err := p.RemoveDomain(ctx, c.actor, in.Domain)
		if err != nil {
			return removedDomainOutput{}, err
		}
		return removedDomainOutput{Removed: r.Name, StillUsedBy: r.UsedBy}, nil
	})
}
