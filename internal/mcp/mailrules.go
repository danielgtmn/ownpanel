package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Out-of-office of a mailbox for its domain owner (staff who left). The
// person's own filters are their data and are not offered here.

type outOfOfficeInput struct {
	ID      int64  `json:"id" jsonschema:"mailbox id from list_mailboxes"`
	Subject string `json:"subject,omitempty" jsonschema:"subject of the reply; empty uses 'Auto:' and the subject of the message answered"`
	Text    string `json:"text" jsonschema:"the reply, plain text, at most 8 KB"`
	Start   string `json:"start,omitempty" jsonschema:"first day the reply is sent, YYYY-MM-DD; empty: from now"`
	End     string `json:"end,omitempty" jsonschema:"last day the reply is sent, YYYY-MM-DD; empty: until cleared"`
	Zone    string `json:"zone,omitempty" jsonschema:"time zone of start and end as +hhmm or -hhmm; empty: UTC"`
	Days    int    `json:"days,omitempty" jsonschema:"each sender gets the reply at most once in this many days, 1 to 90; default 7"`
	Replace bool   `json:"replace,omitempty" jsonschema:"set only after the person's filters were reported as written in a mail program and the user agreed to replace them"`
}

type clearOutOfOfficeInput struct {
	ID int64 `json:"id" jsonschema:"mailbox id from list_mailboxes"`
}

type outOfOfficeInfo struct {
	Enabled     bool   `json:"enabled" jsonschema:"an out-of-office reply is set"`
	On          bool   `json:"on" jsonschema:"it is being sent now (within its dates)"`
	Start       string `json:"start" jsonschema:"first day, empty if open"`
	End         string `json:"end" jsonschema:"last day, empty if open"`
	SetBy       string `json:"set_by" jsonschema:"panel user who set it; empty when the person set it themselves (then its text is not shown)"`
	HandWritten bool   `json:"hand_written" jsonschema:"the person's filters were written in a mail program; setting the reply replaces them only with replace"`
}

type outOfOfficeOutput struct {
	OutOfOffice outOfOfficeInfo `json:"out_of_office"`
}

func outOfOfficeOf(r panel.MailboxRules) outOfOfficeOutput {
	o := r.OutOfOffice
	return outOfOfficeOutput{outOfOfficeInfo{Enabled: o.Enabled, On: o.On, Start: o.Start, End: o.End, SetBy: o.SetBy, HandWritten: r.HandWritten}}
}

func addMailRulesTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "get_mailbox_out_of_office",
		Title: "Show a mailbox's out-of-office reply",
		Description: "Tell whether a mailbox has an out-of-office reply, whether it is being sent now, its dates and who set it. " +
			"The text is the person's and is not shown unless it was set from the panel.",
		Annotations: annotations("Show a mailbox's out-of-office reply", true, false, true),
	}, func(ctx context.Context, c call, in clearOutOfOfficeInput) (outOfOfficeOutput, error) {
		r, err := p.MailboxRulesOf(ctx, c.actor, in.ID)
		return outOfOfficeOf(r), err
	})

	add(s, &sdk.Tool{
		Name:  "set_mailbox_out_of_office",
		Title: "Set a mailbox's out-of-office reply",
		Description: "Set an automatic out-of-office reply for a mailbox of your domains, e.g. for someone who left. " +
			"Senders get it at most once per period; mailing lists, automatic mail and junk get none. The person sees that you set it, " +
			"and the activity log records it. If the person's filters were written in a mail program, the call fails until replace is set " +
			"with the user's agreement.",
		Annotations: annotations("Set a mailbox's out-of-office reply", false, false, true),
	}, func(ctx context.Context, c call, in outOfOfficeInput) (outOfOfficeOutput, error) {
		r, err := p.SetMailboxOutOfOffice(ctx, c.actor, in.ID, panel.OutOfOfficeInput{
			Enabled: true, Subject: in.Subject, Text: in.Text, Start: in.Start, End: in.End, Zone: in.Zone, Days: in.Days, Replace: in.Replace,
		})
		return outOfOfficeOf(r), err
	})

	add(s, &sdk.Tool{
		Name:        "clear_mailbox_out_of_office",
		Title:       "Turn off a mailbox's out-of-office reply",
		Description: "Turn off the out-of-office reply of a mailbox of your domains. The activity log records it.",
		Annotations: annotations("Turn off a mailbox's out-of-office reply", false, true, true),
	}, func(ctx context.Context, c call, in clearOutOfOfficeInput) (outOfOfficeOutput, error) {
		r, err := p.SetMailboxOutOfOffice(ctx, c.actor, in.ID, panel.OutOfOfficeInput{Enabled: false})
		return outOfOfficeOf(r), err
	})
}
