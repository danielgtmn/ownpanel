package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

type dnsTemplateParamInfo struct {
	ID          string `json:"id" jsonschema:"key to use in params"`
	Label       string `json:"label" jsonschema:"what the value is, for the user"`
	Description string `json:"description" jsonschema:"where the user finds the value"`
	Placeholder string `json:"placeholder" jsonschema:"an example value"`
	Optional    bool   `json:"optional" jsonschema:"the value may be left out"`
}

type dnsTemplateInfo struct {
	ID          string                 `json:"id" jsonschema:"template id for apply_dns_record_template"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Params      []dnsTemplateParamInfo `json:"params" jsonschema:"values the user has to give, e.g. a verification text from the provider"`
	OwnMail     bool                   `json:"own_mail" jsonschema:"e-mail on this server: it has no records here, use set_up_mail_records for the mail domain instead"`
}

type dnsTemplatesOutput struct {
	Templates []dnsTemplateInfo `json:"templates"`
}

type applyDNSTemplateInput struct {
	ConnectionID int64             `json:"connection_id" jsonschema:"id of your DNS connection that holds the domain, from list_dns_domains"`
	Domain       string            `json:"domain" jsonschema:"the domain (zone), e.g. example.com"`
	Template     string            `json:"template" jsonschema:"template id from list_dns_record_templates"`
	Params       map[string]string `json:"params,omitempty" jsonschema:"the template's parameters by id"`
	Confirm      bool              `json:"confirm,omitempty" jsonschema:"false (default): only show what would change; true: write the changes, only after the user agreed to the preview"`
}

type dnsTemplateChangeInfo struct {
	Name    string   `json:"name" jsonschema:"record name relative to the domain (@ is the domain itself)"`
	Type    string   `json:"type" jsonschema:"DNS record type"`
	Action  string   `json:"action" jsonschema:"add (new record set), change (existing values are replaced, see current), ok (already right) or conflict (cannot be set up automatically, see note; nothing is written)"`
	Current []string `json:"current" jsonschema:"all values of the record set now"`
	New     []string `json:"new" jsonschema:"all values afterwards; for TXT the other values are kept"`
	Note    string   `json:"note" jsonschema:"explanation for the user"`
}

type dnsTemplateResultOutput struct {
	Template string                  `json:"template"`
	Domain   string                  `json:"domain"`
	Changes  []dnsTemplateChangeInfo `json:"changes"`
	Applied  bool                    `json:"applied" jsonschema:"the changes were written"`
}

// addDNSTemplateTools registers the tools for ready-made record sets.
func addDNSTemplateTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_dns_record_templates",
		Title: "List DNS record templates",
		Description: "List ready-made sets of DNS records for common needs (mail providers such as Google Workspace, " +
			"Microsoft 365 or Fastmail, or a text that proves to a service that you own the domain), with the " +
			"parameters each one needs. Apply one with apply_dns_record_template.",
		Annotations: annotations("List DNS record templates", true, false, true),
	}, func(ctx context.Context, c call, _ struct{}) (dnsTemplatesOutput, error) {
		list, err := p.DNSTemplates(ctx, c.actor)
		if err != nil {
			return dnsTemplatesOutput{}, err
		}
		out := dnsTemplatesOutput{Templates: []dnsTemplateInfo{}}
		for _, t := range list {
			info := dnsTemplateInfo{ID: t.ID, Title: t.Title, Description: t.Description, Params: []dnsTemplateParamInfo{}, OwnMail: t.OwnMail}
			for _, prm := range t.Params {
				info.Params = append(info.Params, dnsTemplateParamInfo(prm))
			}
			out.Templates = append(out.Templates, info)
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "apply_dns_record_template",
		Title: "Apply a DNS record template",
		Description: "Show what a record template would change in a domain of your DNS connections, and with confirm " +
			"write it. Call it first without confirm and show the user the changes: replaced MX records move the " +
			"domain's mail, the list of servers allowed to send mail (SPF) is merged into the existing one, other " +
			"TXT values are kept. If any change is a conflict, nothing is written. Safe to repeat.",
		Annotations: annotations("Apply a DNS record template", false, true, true),
	}, func(ctx context.Context, c call, in applyDNSTemplateInput) (dnsTemplateResultOutput, error) {
		r, err := p.ApplyDNSTemplate(ctx, c.actor, in.ConnectionID, in.Domain, in.Template, in.Params, in.Confirm)
		if err != nil {
			return dnsTemplateResultOutput{}, err
		}
		out := dnsTemplateResultOutput{Template: r.Template, Domain: r.Zone, Changes: []dnsTemplateChangeInfo{}, Applied: r.Applied}
		for _, ch := range r.Changes {
			out.Changes = append(out.Changes, dnsTemplateChangeInfo(ch))
		}
		return out, nil
	})
}
