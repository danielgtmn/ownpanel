package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// E-mail tools. There is deliberately no tool that takes a mailbox
// password (creating a mailbox or setting its password): passwords of
// mailboxes are entered in the web interface or sent through the HTTP API
// and never pass through an AI agent.

func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type mailDomainInfo struct {
	Domain       string `json:"domain" jsonschema:"the mail domain"`
	Owner        string `json:"owner" jsonschema:"user the mail domain belongs to"`
	Active       bool   `json:"active" jsonschema:"whether mail is received for the domain; it becomes active once its MX record points to the mail server"`
	Problem      string `json:"problem" jsonschema:"why the domain is not active yet; empty when active"`
	CheckedAt    string `json:"checked_at" jsonschema:"when the MX record was last looked at, RFC 3339 (UTC); empty if never"`
	Mailboxes    int    `json:"mailboxes" jsonschema:"number of mailboxes"`
	Aliases      int    `json:"aliases" jsonschema:"number of aliases"`
	DKIMSelector string `json:"dkim_selector" jsonschema:"DKIM selector; publish dkim_record as a TXT record at <selector>._domainkey.<domain>"`
	DKIMRecord   string `json:"dkim_record" jsonschema:"value of the DKIM TXT record"`
	CreatedAt    string `json:"created_at" jsonschema:"when e-mail was enabled, RFC 3339 (UTC)"`
}

func mailDomainInfoOf(d panel.MailDomain) mailDomainInfo {
	return mailDomainInfo{
		Domain: d.Domain, Owner: d.Owner, Active: d.Active, Problem: d.Problem, CheckedAt: rfc3339(d.CheckedAt),
		Mailboxes: d.Mailboxes, Aliases: d.Aliases, DKIMSelector: d.DKIMSelector, DKIMRecord: d.DKIMRecord,
		CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type mailboxInfo struct {
	ID          int64  `json:"id" jsonschema:"mailbox id; pass it to set_mailbox_limits, unblock_mailbox or delete_mailbox"`
	Address     string `json:"address" jsonschema:"the mailbox's e-mail address, also its user name in mail programs"`
	Owner       string `json:"owner" jsonschema:"user the mail domain belongs to"`
	QuotaBytes  int64  `json:"quota_bytes" jsonschema:"most mail the mailbox may store, in bytes; 0 means no limit"`
	SendPerHour int    `json:"send_per_hour" jsonschema:"most messages it may send per hour"`
	SendPerDay  int    `json:"send_per_day" jsonschema:"most messages it may send per day"`
	UsedBytes   *int64 `json:"used_bytes,omitempty" jsonschema:"mail stored now, in bytes; missing if the mail server could not be asked"`
	Messages    *int   `json:"messages,omitempty" jsonschema:"number of stored messages; missing if the mail server could not be asked"`
	Blocked     bool   `json:"blocked" jsonschema:"whether the mail server stopped the mailbox from sending; only admins can lift it"`
	BlockReason string `json:"block_reason,omitempty" jsonschema:"why it was blocked"`
	CreatedAt   string `json:"created_at" jsonschema:"when the mailbox was created, RFC 3339 (UTC)"`
}

func mailboxInfoOf(m panel.Mailbox) mailboxInfo {
	v := mailboxInfo{
		ID: m.ID, Address: m.Address, Owner: m.Owner, QuotaBytes: m.QuotaBytes, SendPerHour: m.SendPerHour,
		SendPerDay: m.SendPerDay, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u := m.Usage; u != nil {
		used, n := u.UsedBytes, u.Messages
		v.UsedBytes, v.Messages, v.Blocked, v.BlockReason = &used, &n, u.Blocked, u.BlockedReason
	}
	return v
}

type mailAliasInfo struct {
	ID        int64    `json:"id" jsonschema:"alias id; pass it to set_mail_alias to change the alias or to delete_mail_alias"`
	Address   string   `json:"address" jsonschema:"the alias address; *@domain is the catch-all for every address of the domain without a mailbox or alias"`
	CatchAll  bool     `json:"catch_all" jsonschema:"whether this is the domain's catch-all"`
	Targets   []string `json:"targets" jsonschema:"addresses mail is delivered to"`
	Owner     string   `json:"owner" jsonschema:"user the mail domain belongs to"`
	CreatedAt string   `json:"created_at" jsonschema:"when the alias was created, RFC 3339 (UTC)"`
}

func mailAliasInfoOf(a panel.MailAlias) mailAliasInfo {
	return mailAliasInfo{ID: a.ID, Address: a.Address, CatchAll: a.CatchAll, Targets: a.Targets, Owner: a.Owner,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339)}
}

type mailStatusOutput struct {
	MailHost     string   `json:"mail_host" jsonschema:"the mail server's name: MX target and the server to enter in mail programs"`
	ServiceSetUp bool     `json:"service_set_up" jsonschema:"whether a mail service runs on this server; without it mail domains and mailboxes are only stored"`
	Reachable    bool     `json:"reachable" jsonschema:"whether the mail service answered"`
	InSync       bool     `json:"in_sync" jsonschema:"whether the mail service runs the current settings"`
	Problem      string   `json:"problem" jsonschema:"what is wrong, if anything"`
	Listening    []string `json:"listening" jsonschema:"ports the mail service listens on"`
	IMAPPort     int      `json:"imap_port" jsonschema:"IMAP port for mail programs (TLS)"`
	SMTPPort     int      `json:"smtp_port" jsonschema:"SMTP port for sending from mail programs (TLS)"`
	SMTPStartTLS int      `json:"smtp_starttls_port" jsonschema:"alternative SMTP port with STARTTLS"`
}

type mailDomainInput struct {
	Domain string `json:"domain" jsonschema:"the mail domain, e.g. example.com"`
}

type mailDomainOutput struct {
	MailDomain mailDomainInfo `json:"mail_domain"`
}

type mailDomainsOutput struct {
	MailDomains []mailDomainInfo `json:"mail_domains"`
}

type deleteMailDomainInput struct {
	Domain        string `json:"domain" jsonschema:"the mail domain to delete"`
	ConfirmDomain string `json:"confirm_domain" jsonschema:"must repeat domain exactly; confirms that every mailbox of the domain and all its stored mail is deleted"`
}

type deletedOutput struct {
	Deleted string `json:"deleted" jsonschema:"what was deleted"`
}

type listByDomainInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"only this mail domain; leave empty for all you can see"`
}

type mailboxesOutput struct {
	Mailboxes []mailboxInfo `json:"mailboxes"`
}

type mailboxOutput struct {
	Mailbox mailboxInfo `json:"mailbox"`
}

type mailboxLimitsInput struct {
	ID          int64  `json:"id" jsonschema:"mailbox id from list_mailboxes"`
	QuotaBytes  *int64 `json:"quota_bytes,omitempty" jsonschema:"new quota in bytes (at least 1 MiB); leave out to keep it. Only admins can raise it"`
	SendPerHour *int   `json:"send_per_hour,omitempty" jsonschema:"new messages per hour (at least 1); leave out to keep it. Only admins can raise it"`
	SendPerDay  *int   `json:"send_per_day,omitempty" jsonschema:"new messages per day (at least 1); leave out to keep it. Only admins can raise it"`
}

type mailboxIDInput struct {
	ID int64 `json:"id" jsonschema:"mailbox id from list_mailboxes"`
}

type deleteMailboxInput struct {
	ID             int64  `json:"id" jsonschema:"mailbox id from list_mailboxes"`
	ConfirmAddress string `json:"confirm_address" jsonschema:"the mailbox's address exactly as shown by list_mailboxes; confirms that the mailbox and all its stored mail is deleted"`
}

type aliasesOutput struct {
	Aliases []mailAliasInfo `json:"aliases"`
}

type aliasOutput struct {
	Alias mailAliasInfo `json:"alias"`
}

type setAliasInput struct {
	ID        int64    `json:"id,omitempty" jsonschema:"id of an existing alias to replace; leave empty (or 0) to create a new alias"`
	Domain    string   `json:"domain,omitempty" jsonschema:"mail domain of a new alias; ignored when id is given (an alias stays in its domain)"`
	LocalPart string   `json:"local_part" jsonschema:"the part before the @, e.g. sales; * makes the catch-all for every address of the domain without a mailbox or alias"`
	Targets   []string `json:"targets" jsonschema:"addresses to deliver to: mailboxes here or outside addresses, at most 50"`
}

type deleteAliasInput struct {
	ID             int64  `json:"id" jsonschema:"alias id from list_mail_aliases"`
	ConfirmAddress string `json:"confirm_address" jsonschema:"the alias's address exactly as shown by list_mail_aliases"`
}

type mailRecordInfo struct {
	Kind   string   `json:"kind" jsonschema:"which record: mx, spf, dkim, dmarc, srv-submissions, srv-imaps, autoconfig or autodiscover"`
	Type   string   `json:"type" jsonschema:"DNS record type (MX, TXT, SRV, CNAME)"`
	Name   string   `json:"name" jsonschema:"record name relative to zone when can_apply, else relative to the mail domain; @ is the name itself"`
	FQDN   string   `json:"fqdn" jsonschema:"full record name"`
	Want   string   `json:"want" jsonschema:"the value that should be published (for SPF: the existing record with mx added)"`
	Found  []string `json:"found" jsonschema:"values the domain's nameservers answer now"`
	Status string   `json:"status" jsonschema:"ok, missing, different, conflict (needs a person; never changed automatically) or unknown (lookup failed)"`
	Note   string   `json:"note" jsonschema:"why the record is not ok, in plain words"`
	// Optional: autoconfig and autodiscover only let mail programs set
	// themselves up; A/AAAA records with the server's addresses count too.
	Optional bool `json:"optional" jsonschema:"recommended only: mail works without it (the names mail programs look up to set themselves up)"`
}

type mailRecordsOutput struct {
	Domain       string           `json:"domain" jsonschema:"the mail domain"`
	MailHost     string           `json:"mail_host" jsonschema:"the mail server's name"`
	Records      []mailRecordInfo `json:"records"`
	CanApply     bool             `json:"can_apply" jsonschema:"whether set_up_mail_records can write the records for you (the zone is in one of your own DNS connections with write access)"`
	ConnectionID int64            `json:"connection_id" jsonschema:"the DNS connection that would be changed; 0 if none"`
	Zone         string           `json:"zone" jsonschema:"the zone that would be changed; empty if none"`
	ApplyNote    string           `json:"apply_note" jsonschema:"why the records cannot be written here; the user then creates them at their DNS provider"`
}

type setUpMailRecordsInput struct {
	Domain  string `json:"domain" jsonschema:"the mail domain"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"set true only after the user agreed to the changes marked needs_confirm (replacing MX records moves the domain's mail to this server)"`
}

type mailRecordChangeInfo struct {
	Kind         string   `json:"kind" jsonschema:"which record: mx, spf, dkim, dmarc, srv-submissions, srv-imaps, autoconfig, autodiscover or mail-host-spf"`
	Type         string   `json:"type" jsonschema:"DNS record type"`
	Name         string   `json:"name" jsonschema:"record name relative to the zone"`
	Action       string   `json:"action" jsonschema:"create, replace, add (to other TXT values that stay), keep (already right) or skip (needs a person)"`
	Old          []string `json:"old" jsonschema:"values before"`
	New          []string `json:"new" jsonschema:"values after"`
	NeedsConfirm bool     `json:"needs_confirm" jsonschema:"the change is only made with confirm"`
	Note         string   `json:"note" jsonschema:"explanation"`
	Optional     bool     `json:"optional" jsonschema:"recommended only: mail works without it"`
}

type mailRecordChangesOutput struct {
	Changes []mailRecordChangeInfo `json:"changes"`
	Applied bool                   `json:"applied" jsonschema:"false when a change needs confirmation and nothing was written; true when the changes were written"`
}

type mailHealthCheckInfo struct {
	ID       string   `json:"id" jsonschema:"service, mail_host_dns, mail_host_spf, ptr, outbound_port, certificate, listeners, postmaster or blocklists"`
	State    string   `json:"state" jsonschema:"ok, warning, error or unknown"`
	Found    []string `json:"found" jsonschema:"what was found"`
	Expected []string `json:"expected" jsonschema:"what was expected"`
	Detail   string   `json:"detail" jsonschema:"explanation and what to do"`
	CanApply bool     `json:"can_apply" jsonschema:"for mail_host_spf: set_up_mail_host_spf can write the expected record (the zone is in one of your own DNS connections)"`
}

type mailHealthOutput struct {
	MailHost string                `json:"mail_host" jsonschema:"the mail server's name"`
	Checks   []mailHealthCheckInfo `json:"checks"`
}

// addMailTools registers the e-mail tools.
func addMailTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "get_mail_status",
		Title: "Show the mail server's state",
		Description: "Show whether the mail service runs and is up to date, the mail server's name and the settings for " +
			"mail programs (server name, ports; the user name is the full e-mail address).",
		Annotations: annotations("Show the mail server's state", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (mailStatusOutput, error) {
		st, err := p.MailStatus(ctx, c.actor)
		if err != nil {
			return mailStatusOutput{}, err
		}
		out := mailStatusOutput{
			MailHost: st.MailHost, ServiceSetUp: st.ServiceSetUp, Reachable: st.Reachable, InSync: st.InSync, Problem: st.Problem,
			Listening: []string{}, IMAPPort: st.Client.IMAPPort, SMTPPort: st.Client.SMTPPort, SMTPStartTLS: st.Client.SMTPStartTLS,
		}
		for _, l := range st.Listeners {
			if l.Listening {
				out.Listening = append(out.Listening, l.Name+" "+l.Address)
			}
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "get_mail_records",
		Title: "Show a mail domain's DNS records",
		Description: "List the DNS records a mail domain needs (MX, SPF, DKIM, DMARC, and SRV records for mail programs), " +
			"plus the optional autoconfig and autodiscover names that let mail programs set themselves up, " +
			"what the domain's nameservers answer now and whether each is ok. can_apply tells whether set_up_mail_records " +
			"can write them; otherwise tell the user what to create at their DNS provider.",
		Annotations: annotations("Show a mail domain's DNS records", true, false, true),
	}, func(ctx context.Context, c call, in mailDomainInput) (mailRecordsOutput, error) {
		r, err := p.MailRecords(ctx, c.actor, in.Domain)
		if err != nil {
			return mailRecordsOutput{}, err
		}
		out := mailRecordsOutput{Domain: r.Domain, MailHost: r.MailHost, Records: []mailRecordInfo{}, CanApply: r.CanApply,
			ConnectionID: r.ConnectionID, Zone: r.Zone, ApplyNote: r.ApplyNote}
		for _, x := range r.Records {
			out.Records = append(out.Records, mailRecordInfo(x))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_up_mail_records",
		Title: "Set up a mail domain's DNS records",
		Description: "Write the DNS records a mail domain needs into its zone in your own DNS connection. Other TXT values " +
			"are kept, an existing DMARC policy is never changed and an SPF record that cannot be merged safely is skipped. " +
			"Call it without confirm first: if a change has needs_confirm (replacing MX records that point elsewhere moves " +
			"the domain's mail to this server), nothing is written; show the changes to the user and call again with " +
			"confirm true only if they agree.",
		Annotations: annotations("Set up a mail domain's DNS records", false, true, false),
	}, func(ctx context.Context, c call, in setUpMailRecordsInput) (mailRecordChangesOutput, error) {
		r, err := p.SetUpMailRecords(ctx, c.actor, in.Domain, in.Confirm)
		if err != nil {
			return mailRecordChangesOutput{}, err
		}
		out := mailRecordChangesOutput{Changes: []mailRecordChangeInfo{}, Applied: r.Applied}
		for _, x := range r.Changes {
			out.Changes = append(out.Changes, mailRecordChangeInfo(x))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "get_mail_health",
		Title: "Check the mail server's health",
		Description: "Check what decides whether mail from this server is accepted elsewhere (admins only): the mail service " +
			"runs, the mail server's name points here and has an SPF record (bounces and forwarded mail are sent under it; " +
			"set_up_mail_host_spf writes it), each server address has it as reverse name (PTR), outgoing port 25 " +
			"works, the certificate is valid, every port listens, postmaster mail has somewhere to go and no blocklist refused " +
			"this server's queries in the last 7 days.",
		Annotations: annotations("Check the mail server's health", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (mailHealthOutput, error) {
		h, err := p.MailHealth(ctx, c.actor)
		if err != nil {
			return mailHealthOutput{}, err
		}
		out := mailHealthOutput{MailHost: h.MailHost, Checks: []mailHealthCheckInfo{}}
		for _, x := range h.Checks {
			out.Checks = append(out.Checks, mailHealthCheckInfo(x))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_up_mail_host_spf",
		Title: "Set up the SPF record of the mail server's name",
		Description: "Write the SPF record of the mail server's name (v=spf1 a -all, or a added to an existing SPF record) " +
			"into its zone in your own DNS connection (admins only). Bounces and forwarded mail are sent under that name. " +
			"An SPF record that cannot be merged safely is skipped and reported.",
		Annotations: annotations("Set up the SPF record of the mail server's name", false, false, true),
	}, func(ctx context.Context, c call, _ noInput) (mailRecordChangesOutput, error) {
		r, err := p.SetUpMailHostSPF(ctx, c.actor)
		if err != nil {
			return mailRecordChangesOutput{}, err
		}
		out := mailRecordChangesOutput{Changes: []mailRecordChangeInfo{}, Applied: r.Applied}
		for _, x := range r.Changes {
			out.Changes = append(out.Changes, mailRecordChangeInfo(x))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:        "list_mail_domains",
		Title:       "List mail domains",
		Description: "List the domains with e-mail you can see, with whether each is active (receives mail) and why not.",
		Annotations: annotations("List mail domains", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (mailDomainsOutput, error) {
		list, err := p.MailDomains(ctx, c.actor)
		if err != nil {
			return mailDomainsOutput{}, err
		}
		out := mailDomainsOutput{MailDomains: []mailDomainInfo{}}
		for _, d := range list {
			out.MailDomains = append(out.MailDomains, mailDomainInfoOf(d))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "enable_mail_domain",
		Title: "Enable e-mail for a domain",
		Description: "Enable e-mail for a domain of one of your sites or redirects, or one in a zone of your DNS connections. " +
			"The domain receives mail only once its MX record points to the mail server (mail_host of get_mail_status); " +
			"until then it is listed as not active with the reason. Mailboxes are created in the web interface.",
		Annotations: annotations("Enable e-mail for a domain", false, false, false),
	}, func(ctx context.Context, c call, in mailDomainInput) (mailDomainOutput, error) {
		d, err := p.EnableMailDomain(ctx, c.actor, in.Domain)
		return mailDomainOutput{mailDomainInfoOf(d)}, err
	})

	add(s, &sdk.Tool{
		Name:  "check_mail_domain",
		Title: "Check a mail domain",
		Description: "Look at a mail domain's MX record now and activate the domain if it points to the mail server. " +
			"Use it after changing the MX record. Returns the domain with its state.",
		Annotations: annotations("Check a mail domain", false, false, true),
	}, func(ctx context.Context, c call, in mailDomainInput) (mailDomainOutput, error) {
		d, err := p.CheckMailDomain(ctx, c.actor, in.Domain)
		return mailDomainOutput{mailDomainInfoOf(d)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_mail_domain",
		Title: "Delete a mail domain",
		Description: "Turn off e-mail for a domain: every mailbox and alias of it and all stored mail are deleted. This cannot " +
			"be undone. Only call it when the user clearly asked for it, and repeat the domain in confirm_domain.",
		Annotations: annotations("Delete a mail domain", false, true, false),
	}, func(ctx context.Context, c call, in deleteMailDomainInput) (deletedOutput, error) {
		if err := p.DeleteMailDomain(ctx, c.actor, in.Domain, in.ConfirmDomain); err != nil {
			return deletedOutput{}, err
		}
		return deletedOutput{Deleted: strings.ToLower(strings.TrimSpace(in.Domain))}, nil
	})

	add(s, &sdk.Tool{
		Name:        "list_mailboxes",
		Title:       "List mailboxes",
		Description: "List the mailboxes you can see (of one mail domain, or all), with quota, send limits, usage and whether sending is blocked.",
		Annotations: annotations("List mailboxes", true, false, true),
	}, func(ctx context.Context, c call, in listByDomainInput) (mailboxesOutput, error) {
		list, err := p.Mailboxes(ctx, c.actor, in.Domain)
		if err != nil {
			return mailboxesOutput{}, err
		}
		out := mailboxesOutput{Mailboxes: []mailboxInfo{}}
		for _, m := range list {
			out.Mailboxes = append(out.Mailboxes, mailboxInfoOf(m))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_mailbox_limits",
		Title: "Change a mailbox's limits",
		Description: "Change the quota and send limits of a mailbox. Owners can only lower them; admins can also raise them. " +
			"Returns the mailbox.",
		Annotations: annotations("Change a mailbox's limits", false, false, true),
	}, func(ctx context.Context, c call, in mailboxLimitsInput) (mailboxOutput, error) {
		m, err := p.SetMailboxLimits(ctx, c.actor, in.ID, panel.MailboxLimits{QuotaBytes: in.QuotaBytes, SendPerHour: in.SendPerHour, SendPerDay: in.SendPerDay})
		return mailboxOutput{mailboxInfoOf(m)}, err
	})

	add(s, &sdk.Tool{
		Name:        "unblock_mailbox",
		Title:       "Let a blocked mailbox send again",
		Description: "Lift the sending block the mail server put on a mailbox (admins only). Check why it was blocked first.",
		Annotations: annotations("Let a blocked mailbox send again", false, false, true),
	}, func(ctx context.Context, c call, in mailboxIDInput) (mailboxOutput, error) {
		if err := p.UnblockMailbox(ctx, c.actor, in.ID); err != nil {
			return mailboxOutput{}, err
		}
		m, err := p.MailboxOf(ctx, c.actor, in.ID)
		return mailboxOutput{mailboxInfoOf(m)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_mailbox",
		Title: "Delete a mailbox",
		Description: "Delete a mailbox and all mail stored in it. This cannot be undone. Only call it when the user clearly " +
			"asked for it, and repeat the mailbox's address in confirm_address.",
		Annotations: annotations("Delete a mailbox", false, true, false),
	}, func(ctx context.Context, c call, in deleteMailboxInput) (deletedOutput, error) {
		m, err := p.MailboxOf(ctx, c.actor, in.ID)
		if err != nil {
			return deletedOutput{}, err
		}
		if strings.ToLower(strings.TrimSpace(in.ConfirmAddress)) != m.Address {
			return deletedOutput{}, plainError(fmt.Sprintf("not deleted: confirm_address %q does not match the mailbox's address %q; repeat it exactly to delete the mailbox", in.ConfirmAddress, m.Address))
		}
		if err := p.DeleteMailbox(ctx, c.actor, in.ID); err != nil {
			return deletedOutput{}, err
		}
		return deletedOutput{Deleted: m.Address}, nil
	})

	add(s, &sdk.Tool{
		Name:        "list_mail_aliases",
		Title:       "List mail aliases",
		Description: "List the aliases you can see (of one mail domain, or all): addresses that deliver to other addresses.",
		Annotations: annotations("List mail aliases", true, false, true),
	}, func(ctx context.Context, c call, in listByDomainInput) (aliasesOutput, error) {
		list, err := p.MailAliases(ctx, c.actor, in.Domain)
		if err != nil {
			return aliasesOutput{}, err
		}
		out := aliasesOutput{Aliases: []mailAliasInfo{}}
		for _, a := range list {
			out.Aliases = append(out.Aliases, mailAliasInfoOf(a))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_mail_alias",
		Title: "Create or change a mail alias",
		Description: "Create an alias that delivers mail for an address of a mail domain to other addresses (mailboxes here " +
			"or outside), or replace an existing one when id is given. An address is either a mailbox or an alias. " +
			"Local part * makes the domain's catch-all. Returns the saved alias.",
		Annotations: annotations("Create or change a mail alias", false, true, false),
	}, func(ctx context.Context, c call, in setAliasInput) (aliasOutput, error) {
		var a panel.MailAlias
		var err error
		if in.ID > 0 {
			a, err = p.UpdateMailAlias(ctx, c.actor, in.ID, in.LocalPart, in.Targets)
		} else {
			a, err = p.CreateMailAlias(ctx, c.actor, in.Domain, in.LocalPart, in.Targets)
		}
		return aliasOutput{mailAliasInfoOf(a)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_mail_alias",
		Title: "Delete a mail alias",
		Description: "Delete an alias; mail to its address is then refused (or goes to the catch-all). Only call it when the " +
			"user asked for it, and repeat the alias's address in confirm_address.",
		Annotations: annotations("Delete a mail alias", false, true, false),
	}, func(ctx context.Context, c call, in deleteAliasInput) (deletedOutput, error) {
		a, err := p.MailAliasOf(ctx, c.actor, in.ID)
		if err != nil {
			return deletedOutput{}, err
		}
		if strings.ToLower(strings.TrimSpace(in.ConfirmAddress)) != a.Address {
			return deletedOutput{}, plainError(fmt.Sprintf("not deleted: confirm_address %q does not match the alias's address %q; repeat it exactly to delete the alias", in.ConfirmAddress, a.Address))
		}
		if err := p.DeleteMailAlias(ctx, c.actor, in.ID); err != nil {
			return deletedOutput{}, err
		}
		return deletedOutput{Deleted: a.Address}, nil
	})
}
