package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Display names and organisations. App passwords are deliberately not
// offered: they are passwords, and passwords never pass through an AI agent
// (neither is "mail programs only with app passwords", a security setting
// an agent should not weaken).

type organisationInfo struct {
	ID        string   `json:"id" jsonschema:"organisation id; pass it to set_mail_domain_organisation, set_mail_organisation or delete_mail_organisation"`
	Name      string   `json:"name" jsonschema:"the organisation's name"`
	Owner     string   `json:"owner" jsonschema:"user the organisation belongs to"`
	Domains   []string `json:"domains" jsonschema:"mail domains in the organisation; their mailboxes can share with each other and see each other in the address book once those features exist"`
	CreatedAt string   `json:"created_at" jsonschema:"when it was created, RFC 3339 (UTC)"`
}

func organisationInfoOf(o panel.Organisation) organisationInfo {
	return organisationInfo{ID: o.ID, Name: o.Name, Owner: o.Owner, Domains: o.Domains, CreatedAt: o.CreatedAt.UTC().Format(time.RFC3339)}
}

type organisationsOutput struct {
	Organisations []organisationInfo `json:"organisations"`
}

type organisationOutput struct {
	Organisation organisationInfo `json:"organisation"`
}

type setOrganisationInput struct {
	ID   string `json:"id,omitempty" jsonschema:"id of an existing organisation to rename; leave empty to create a new one"`
	Name string `json:"name" jsonschema:"the organisation's name, e.g. the company, at most 64 characters"`
}

type deleteOrganisationInput struct {
	ID          string `json:"id" jsonschema:"organisation id from list_mail_organisations"`
	ConfirmName string `json:"confirm_name" jsonschema:"the organisation's name exactly as listed; confirms that its domains stand alone again"`
}

type domainOrganisationInput struct {
	Domain       string `json:"domain" jsonschema:"the mail domain"`
	Organisation string `json:"organisation" jsonschema:"organisation id from list_mail_organisations; empty lets the domain stand alone"`
}

type displayNameInput struct {
	ID          int64  `json:"id" jsonschema:"mailbox id from list_mailboxes"`
	DisplayName string `json:"display_name" jsonschema:"the name shown as sender and in address books, at most 64 characters, without < > or quotes; empty removes it"`
}

func addMailAccountTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_mail_organisations",
		Title: "List mail organisations",
		Description: "List the organisations you manage. An organisation groups mail domains (e.g. example.com and example.de " +
			"of one company): only mailboxes of one organisation will be able to share with each other. A domain outside " +
			"every organisation stands alone.",
		Annotations: annotations("List mail organisations", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (organisationsOutput, error) {
		list, err := p.Organisations(ctx, c.actor)
		if err != nil {
			return organisationsOutput{}, err
		}
		out := organisationsOutput{Organisations: []organisationInfo{}}
		for _, o := range list {
			out.Organisations = append(out.Organisations, organisationInfoOf(o))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_mail_organisation",
		Title: "Create or rename a mail organisation",
		Description: "Create an organisation (without id) or rename one (with id). Put domains into it with " +
			"set_mail_domain_organisation. Returns the organisation.",
		Annotations: annotations("Create or rename a mail organisation", false, false, false),
	}, func(ctx context.Context, c call, in setOrganisationInput) (organisationOutput, error) {
		var o panel.Organisation
		var err error
		if in.ID != "" {
			o, err = p.RenameOrganisation(ctx, c.actor, in.ID, in.Name)
		} else {
			o, err = p.CreateOrganisation(ctx, c.actor, in.Name)
		}
		return organisationOutput{organisationInfoOf(o)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_mail_organisation",
		Title: "Delete a mail organisation",
		Description: "Delete an organisation; its domains then stand alone and their mailboxes can no longer share with " +
			"each other. Only call it when the user asked for it, and repeat the name in confirm_name.",
		Annotations: annotations("Delete a mail organisation", false, true, false),
	}, func(ctx context.Context, c call, in deleteOrganisationInput) (deletedOutput, error) {
		list, err := p.Organisations(ctx, c.actor)
		if err != nil {
			return deletedOutput{}, err
		}
		for _, o := range list {
			if o.ID != in.ID {
				continue
			}
			if in.ConfirmName != o.Name {
				return deletedOutput{}, plainError("not deleted: confirm_name does not match the organisation's name " + o.Name + "; repeat it exactly")
			}
			if err := p.DeleteOrganisation(ctx, c.actor, in.ID); err != nil {
				return deletedOutput{}, err
			}
			return deletedOutput{Deleted: o.Name}, nil
		}
		return deletedOutput{}, p.DeleteOrganisation(ctx, c.actor, in.ID) // answers "not found"
	})

	add(s, &sdk.Tool{
		Name:  "set_mail_domain_organisation",
		Title: "Put a mail domain into an organisation",
		Description: "Put a mail domain into one of your organisations, or let it stand alone again (organisation empty). " +
			"Grouping domains of different owners is for administrators only. Returns the domain.",
		Annotations: annotations("Put a mail domain into an organisation", false, false, true),
	}, func(ctx context.Context, c call, in domainOrganisationInput) (mailDomainOutput, error) {
		d, err := p.SetMailDomainOrganisation(ctx, c.actor, in.Domain, in.Organisation)
		return mailDomainOutput{mailDomainInfoOf(d)}, err
	})

	add(s, &sdk.Tool{
		Name:  "set_mailbox_display_name",
		Title: "Set a mailbox's display name",
		Description: "Set the name of a mailbox's owner (a person or team), shown as the sender's name and in address books. " +
			"Returns the mailbox.",
		Annotations: annotations("Set a mailbox's display name", false, false, true),
	}, func(ctx context.Context, c call, in displayNameInput) (mailboxOutput, error) {
		m, err := p.SetMailboxAccount(ctx, c.actor, in.ID, panel.MailboxAccount{DisplayName: &in.DisplayName})
		return mailboxOutput{mailboxInfoOf(m)}, err
	})
}
